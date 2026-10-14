package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// scriptedCredential answers a fixed token, except that its third token, the one
// the resend of the second read asks for, is the test's to choose.
type scriptedCredential struct {
	third func(context.Context) (string, error)
	calls atomic.Int32
}

func (s *scriptedCredential) Token(ctx context.Context) (string, error) {
	if s.calls.Add(1) == 3 && s.third != nil {
		return s.third(ctx)
	}
	return "test-token", nil
}
func (*scriptedCredential) Kind() forgeapi.CredKind   { return forgeapi.CredKindStaticPAT }
func (*scriptedCredential) State() forgeapi.CredState { return forgeapi.CredValid }

// TestEveryCacheableExchangeEndsInOneOutcome holds every way a cacheable read can
// leave [Conn.Do] to one of two final states: the answer the exchange vouched for
// is kept, or the key is forgotten. Each case starts from an answer the connection
// holds, then changes the instance, or switches on an observer, a cut connection or
// a failing resend, before the second read.
func TestEveryCacheableExchangeEndsInOneOutcome(t *testing.T) {
	type setup struct {
		origin          *origin
		req             *Request
		cred            *scriptedCredential
		abort           atomic.Bool
		changeRequest   atomic.Bool
		changeAnswer    atomic.Bool
		notModifiedOnly bool
	}
	// resend makes the second read's 304 name another representation, so the read
	// is sent once more.
	resend := func(s *setup) { s.origin.etag, s.origin.notModified = `"v2"`, 1 }
	for _, test := range []struct {
		change  func(*setup)
		name    string
		timeout time.Duration
		kept    bool
	}{
		{name: "a_200_with_a_validator", kept: true, change: func(s *setup) {
			s.origin.body, s.origin.etag = `{"login":"new"}`, `"v2"`
		}},
		{name: "a_200_without_a_validator", change: func(s *setup) {
			s.origin.body, s.origin.etag = `{"login":"new"}`, ""
		}},
		{name: "a_200_forbidding_storage", change: func(s *setup) {
			s.origin.body, s.origin.etag = `{"login":"new"}`, `"v2"`
			s.origin.header.Set("Cache-Control", "no-store")
		}},
		{name: "a_304_to_the_held_answer", kept: true, change: func(*setup) {}},
		{name: "a_304_naming_a_new_validator", kept: true, change: func(s *setup) {
			s.origin.etag, s.origin.notModified = `"v2"`, 1
		}},
		{name: "a_304_forbidding_storage", change: func(s *setup) {
			s.origin.header.Set("Cache-Control", "no-store")
		}},
		{name: "a_404", change: func(s *setup) { s.origin.status = http.StatusNotFound }},
		{name: "a_500", change: func(s *setup) { s.origin.status = http.StatusInternalServerError }},
		{name: "an_unreadable_body", change: func(s *setup) {
			s.origin.body, s.origin.etag = `{"login":"`+strings.Repeat("x", 4096)+`"}`, `"v2"`
			s.req.MaxBytes = 64
		}},
		{name: "an_undecodable_body", change: func(s *setup) {
			s.origin.body, s.origin.etag = `not json`, `"v2"`
		}},
		{name: "an_answer_an_observer_changed", change: func(s *setup) { s.changeAnswer.Store(true) }},
		{name: "a_request_an_observer_changed", change: func(s *setup) { s.changeRequest.Store(true) }},
		{name: "a_transport_failure", change: func(s *setup) { s.abort.Store(true) }},
		{name: "a_resend_answered_304_again", change: func(s *setup) {
			s.origin.etag, s.origin.notModified = `"v2"`, 2
		}},
		{name: "a_resend_whose_credential_fails", change: func(s *setup) {
			resend(s)
			s.cred.third = func(context.Context) (string, error) { return "", errors.New("refresh failed") }
		}},
		{name: "a_resend_past_the_deadline", timeout: 2 * time.Second, change: func(s *setup) {
			resend(s)
			s.cred.third = func(ctx context.Context) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			}
		}},
		{name: "a_resend_under_a_rotated_token", change: func(s *setup) {
			resend(s)
			s.cred.third = func(context.Context) (string, error) { return "rotated-token", nil }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &setup{
				origin: newOrigin(`{"login":"octo"}`, `"v1"`), req: &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"},
				cred: &scriptedCredential{},
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if s.abort.Load() {
					panic(http.ErrAbortHandler)
				}
				s.origin.ServeHTTP(w, r)
			}))
			defer srv.Close()
			changeRequest := func(next http.RoundTripper) http.RoundTripper {
				return roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if s.changeRequest.Load() {
						r = r.Clone(r.Context())
						r.Header.Set("X-Trace", "1")
					}
					return next.RoundTrip(r)
				})
			}
			changeAnswer := answerRewrite(func(resp *http.Response) {
				if s.changeAnswer.Load() {
					resp.Header.Set("X-Forged", "1")
				}
			})
			opts := []forgeapi.Option{
				forgeapi.WithRetries(0), forgeapi.WithCredentialSource(s.cred),
				forgeapi.WithRequestObserver(changeRequest), forgeapi.WithAttemptObserver(changeAnswer),
			}
			if test.timeout > 0 {
				opts = append(opts, forgeapi.WithOperationTimeout(test.timeout))
			}
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, opts...)
			if _, _, err := read(t, c, nil); err != nil || c.BudgetState().ReadCache.Bytes == 0 {
				t.Fatalf("Setup: the first read = %v, holding %d bytes, want its answer kept", err, c.BudgetState().ReadCache.Bytes)
			}
			s.origin.set(func(*origin) { test.change(s) })
			_, _, _ = read(t, c, s.req)
			s.origin.set(func(o *origin) {
				o.status, o.body, o.etag, o.notModified, o.header = http.StatusOK, `{"login":"octo"}`, `"v1"`, 0, http.Header{}
			})
			s.abort.Store(false)
			s.changeRequest.Store(false)
			s.changeAnswer.Store(false)
			held := c.BudgetState().ReadCache.Bytes
			switch {
			case test.kept && held == 0:
				t.Errorf("the cache holds nothing after the second read, want the vouched answer kept")
			case !test.kept && held != 0:
				t.Errorf("the cache holds %d bytes after the second read, want the key forgotten", held)
			}
		})
	}
}

// slowAccount is an account whose decode takes a while, as a large answer's can.
type slowAccount struct {
	account
}

//deadset:ignore DS1103 -- encoding/json calls it through json.Unmarshaler when the read decodes into a *slowAccount.
func (a *slowAccount) UnmarshalJSON(b []byte) error {
	time.Sleep(300 * time.Millisecond)
	return json.Unmarshal(b, &a.account)
}

// TestAnOperationNeverOutlivesItsTimeout holds one read to one WithOperationTimeout
// bound across the wire, the body and the decode: a read whose phases outlast the
// bound answers the deadline, and keeps nothing.
func TestAnOperationNeverOutlivesItsTimeout(t *testing.T) {
	const bound = 400 * time.Millisecond
	for _, test := range []struct {
		into  func() any
		name  string
		delay time.Duration
	}{
		{name: "a_slow_body", delay: 600 * time.Millisecond, into: func() any { return &account{} }},
		{name: "a_slow_wire_then_a_slow_decode", delay: 250 * time.Millisecond, into: func() any { return &slowAccount{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"v1"`)
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				timer := time.NewTimer(test.delay)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-r.Context().Done():
					return
				}
				_, _ = w.Write([]byte(`{"login":"octo"}`))
			}))
			defer srv.Close()
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithOperationTimeout(bound), forgeapi.WithRetries(0))
			started := time.Now()
			_, err := c.Do(Call(t.Context(), "Whoami"), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user", Into: test.into()})
			elapsed := time.Since(started)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("a read whose phases outlast its %v bound = %v, want context.DeadlineExceeded", bound, err)
			}
			if elapsed >= bound+250*time.Millisecond {
				t.Errorf("the read returned after %v, want it ended by its %v bound", elapsed, bound)
			}
			if held := c.BudgetState().ReadCache.Bytes; held != 0 {
				t.Errorf("a read past its deadline left %d bytes held, want nothing kept", held)
			}
		})
	}
}

// eofBody calls onEOF once the wrapped body reports io.EOF.
type eofBody struct {
	io.ReadCloser
	onEOF func()
	fired atomic.Bool
}

func (b *eofBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) && b.fired.CompareAndSwap(false, true) {
		b.onEOF()
	}
	return n, err
}

// The operation's deadline bounds the decode [Conn.Do] makes, so a write, which
// names no decode, is answered as the instance answered it once its body is read,
// even if its context ends a moment later.
func TestAWriteReadInFullIsReportedAsAnswered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":7}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cutAtEOF := func(next http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			resp, err := next.RoundTrip(r)
			if err == nil {
				resp.Body = &eofBody{ReadCloser: resp.Body, onEOF: cancel}
			}
			return resp, err
		})
	}
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithRequestObserver(cutAtEOF), forgeapi.WithRetries(0))
	resp, err := c.Do(Call(ctx, "CreateIssue"), &Request{Op: "CreateIssue", Method: http.MethodPost, Path: "/repos/o/r/issues", Body: map[string]string{"title": "x"}})
	if err != nil || resp == nil || resp.Status != http.StatusCreated {
		t.Errorf("a write answered 201 and read in full before its context ended = %v, %v, want the 201", resp, err)
	}
}
