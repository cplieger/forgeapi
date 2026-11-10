package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v2"
)

// discardingSource is a credential source with Discard, answering retry for
// every token it is told about and recording each.
type discardingSource struct {
	told  []string
	mu    sync.Mutex
	retry bool
}

func (*discardingSource) Token(context.Context) (string, error) { return "discard-placeholder", nil }

func (*discardingSource) Kind() forgeapi.CredKind   { return forgeapi.CredKindAppInstallation }
func (*discardingSource) State() forgeapi.CredState { return forgeapi.CredValid }

func (s *discardingSource) Discard(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.told = append(s.told, token)
	return s.retry
}

func (s *discardingSource) discarded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.told)
}

// scriptedWire answers its nth request with statuses[n], the last status for
// every request past the list, and records each request's Authorization header
// and the bubble's time it arrived.
type scriptedWire struct {
	statuses []int
	auth     []string
	at       []time.Duration
	start    time.Time
	mu       sync.Mutex
}

func newScriptedWire(statuses ...int) *scriptedWire {
	return &scriptedWire{statuses: statuses, start: time.Now()}
}

func (w *scriptedWire) RoundTrip(r *http.Request) (*http.Response, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	status := w.statuses[min(len(w.auth), len(w.statuses)-1)]
	w.auth = append(w.auth, r.Header.Get("Authorization"))
	w.at = append(w.at, time.Since(w.start))
	return jsonAnswer(r, status, `{"message":"scripted"}`), nil
}

func (w *scriptedWire) sent() (auth []string, at []time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.auth), slices.Clone(w.at)
}

// discardCase is one connection over a scripted wire with source as its
// credential, recording to buf and counting into spy.
type discardCase struct {
	conn *Conn
	wire *scriptedWire
	spy  *counterSpy
	buf  *bytes.Buffer
}

func openDiscardCase(t *testing.T, source forgeapi.CredentialSource, wire *scriptedWire, extra ...forgeapi.Option) discardCase {
	t.Helper()
	dc := discardCase{wire: wire, spy: newCounterSpy(), buf: &bytes.Buffer{}}
	opts := append([]forgeapi.Option{
		forgeapi.WithWireTransport(wire),
		forgeapi.WithCredentialSource(source),
		forgeapi.WithCounters(dc.spy.counters()),
		forgeapi.WithLogger(recordingLogger(dc.buf)),
		forgeapi.WithRetries(0),
	}, extra...)
	dc.conn = openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"}, opts...)
	return dc
}

func whoami() *Request { return &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"} }

const bearer = "Bearer discard-placeholder"

func TestA401IsResentWhileTheSourcesDiscardAsksForIt(t *testing.T) {
	t.Run("a_401_then_an_answer", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			source := &discardingSource{retry: true}
			dc := openDiscardCase(t, source, newScriptedWire(http.StatusUnauthorized, http.StatusOK))
			if _, err := dc.conn.Do(t.Context(), whoami()); err != nil {
				t.Fatalf("a read answered 401 then 200 = %v, want nil after the resend", err)
			}
			auth, at := dc.wire.sent()
			if !slices.Equal(auth, []string{bearer, bearer}) || !slices.Equal(at, []time.Duration{0, time.Second}) {
				t.Errorf("the wire saw %q at %v, want the same token twice, at 0s and 1s", auth, at)
			}
			if got := source.discarded(); !slices.Equal(got, []string{"discard-placeholder"}) {
				t.Errorf("Discard was told %q, want the token the 401 answered, once", got)
			}
			line := onlyRecord(t, dc.buf)
			if line["attempt"] != float64(2) || line["retried_on"] != "401" {
				t.Errorf("the record = attempt %v, retried_on %v, want 2, 401", line["attempt"], line["retried_on"])
			}
			if got := dc.spy.times("Retried:Whoami"); got != 1 {
				t.Errorf("the Retried counter fired %d time(s), want 1", got)
			}
		})
	})
	t.Run("a_persistent_401", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			source := &discardingSource{retry: true}
			dc := openDiscardCase(t, source, newScriptedWire(http.StatusUnauthorized))
			_, err := dc.conn.Do(t.Context(), whoami())
			var fe *forgeapi.Error
			if !asError(err, &fe) || fe.Status != http.StatusUnauthorized {
				t.Fatalf("a read answered 401 three times = %v, want the 401's *forgeapi.Error", err)
			}
			if _, at := dc.wire.sent(); !slices.Equal(at, []time.Duration{0, time.Second, 3 * time.Second}) {
				t.Errorf("the wire saw requests at %v, want 0s, 1s and 3s: resends after 1s, then 2s", at)
			}
			if got := len(source.discarded()); got != 3 {
				t.Errorf("Discard was told %d time(s), want 3: once per 401, the last one included", got)
			}
			if line := onlyRecord(t, dc.buf); line["attempt"] != float64(3) {
				t.Errorf("the record = attempt %v, want 3", line["attempt"])
			}
		})
	})
	t.Run("a_resend_past_the_deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			source := &discardingSource{retry: true}
			dc := openDiscardCase(t, source, newScriptedWire(http.StatusUnauthorized),
				forgeapi.WithOperationTimeout(1500*time.Millisecond))
			_, err := dc.conn.Do(t.Context(), whoami())
			var fe *forgeapi.Error
			if !asError(err, &fe) || fe.Status != http.StatusUnauthorized {
				t.Fatalf("a 401 the deadline leaves no 2s for = %v, want the 401's *forgeapi.Error, not a deadline", err)
			}
			if _, at := dc.wire.sent(); !slices.Equal(at, []time.Duration{0, time.Second}) {
				t.Errorf("the wire saw requests at %v, want 0s and 1s: 0.5s left cannot hold the 2s wait", at)
			}
		})
	})
	t.Run("a_cancel_during_the_pause", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			source := &discardingSource{retry: true}
			dc := openDiscardCase(t, source, newScriptedWire(http.StatusUnauthorized, http.StatusOK))
			ctx, cancel := context.WithCancel(t.Context())
			time.AfterFunc(500*time.Millisecond, cancel)
			if _, err := dc.conn.Do(ctx, whoami()); !errors.Is(err, context.Canceled) {
				t.Fatalf("a read cancelled during the resend's pause = %v, want context.Canceled", err)
			}
			if auth, _ := dc.wire.sent(); len(auth) != 1 {
				t.Errorf("the wire saw %d request(s), want 1", len(auth))
			}
			if got := dc.spy.times("Retried:Whoami"); got != 0 {
				t.Errorf("the Retried counter fired %d time(s), want 0: the resend was never sent", got)
			}
			if line := onlyRecord(t, dc.buf); line["attempt"] != float64(1) {
				t.Errorf("the record = attempt %v, want 1", line["attempt"])
			}
		})
	})
	t.Run("a_retry_after_the_resend", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			source := &discardingSource{retry: true}
			dc := openDiscardCase(t, source,
				newScriptedWire(http.StatusUnauthorized, http.StatusBadGateway, http.StatusOK), forgeapi.WithRetries(1))
			if _, err := dc.conn.Do(t.Context(), whoami()); err != nil {
				t.Fatalf("a read answered 401, 502, 200 = %v, want nil", err)
			}
			line := onlyRecord(t, dc.buf)
			if line["attempt"] != float64(3) || line["retried_on"] != "502" {
				t.Errorf("the record = attempt %v, retried_on %v, want 3, 502: the resend's attempts count on", line["attempt"], line["retried_on"])
			}
		})
	})
	t.Run("a_mutation", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			source := &discardingSource{retry: true}
			dc := openDiscardCase(t, source, newScriptedWire(http.StatusUnauthorized, http.StatusCreated))
			if _, err := dc.conn.Do(t.Context(), &Request{Op: "Comment", Method: http.MethodPost, Path: "/comments", Body: map[string]string{}}); err != nil {
				t.Fatalf("a mutation answered 401 then 201 = %v, want nil: a 401 changed nothing", err)
			}
			if auth, _ := dc.wire.sent(); len(auth) != 2 {
				t.Errorf("the wire saw %d request(s), want 2", len(auth))
			}
		})
	})
}

// lateBodyWire answers every request 401, its body arriving delay after the
// headers, and counts the requests.
type lateBodyWire struct {
	body  string
	delay time.Duration
	mu    sync.Mutex
	count int
}

func (w *lateBodyWire) RoundTrip(r *http.Request) (*http.Response, error) {
	w.mu.Lock()
	w.count++
	w.mu.Unlock()
	resp := jsonAnswer(r, http.StatusUnauthorized, "")
	resp.Body = io.NopCloser(&lateReader{body: strings.NewReader(w.body), delay: w.delay})
	return resp, nil
}

func (w *lateBodyWire) requests() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.count
}

// lateReader is a body whose first read waits delay.
type lateReader struct {
	body  io.Reader
	delay time.Duration
}

func (r *lateReader) Read(p []byte) (int, error) {
	if r.delay > 0 {
		time.Sleep(r.delay)
		r.delay = 0
	}
	return r.body.Read(p)
}

// A 401 body still arriving uses the time a resend's wait needs; the answer
// GitHub sent is what the caller reads, as it is with no Discard.
func TestA401WhoseBodyUsesTheResendsTimeIsAnswered(t *testing.T) {
	badCredentials := `{"message":"Bad credentials"}` + strings.Repeat(" ", 4000-len(`{"message":"Bad credentials"}`))
	late := func(t *testing.T, source forgeapi.CredentialSource) (*lateBodyWire, *counterSpy, *bytes.Buffer, error) {
		t.Helper()
		wire := &lateBodyWire{body: badCredentials, delay: 1500 * time.Millisecond}
		spy, buf := newCounterSpy(), &bytes.Buffer{}
		conn := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"},
			forgeapi.WithWireTransport(wire), forgeapi.WithCredentialSource(source),
			forgeapi.WithCounters(spy.counters()), forgeapi.WithLogger(recordingLogger(buf)),
			forgeapi.WithRetries(0), forgeapi.WithOperationTimeout(2*time.Second))
		_, err := conn.Do(t.Context(), whoami())
		return wire, spy, buf, err
	}
	t.Run("a_body_completing_inside_the_wait", func(t *testing.T) {
		var want *forgeapi.Error
		synctest.Test(t, func(t *testing.T) {
			if _, _, _, err := late(t, stubCredential{}); !asError(err, &want) || want.Status != http.StatusUnauthorized {
				t.Fatalf("with no Discard, a 401 whose body took 1.5s of 2s = %v, want the 401's *forgeapi.Error", err)
			}
		})
		synctest.Test(t, func(t *testing.T) {
			wire, spy, buf, err := late(t, &discardingSource{retry: true})
			var fe *forgeapi.Error
			if !asError(err, &fe) || fe.Status != want.Status || fe.Kind != want.Kind || fe.Message != "Bad credentials" {
				t.Fatalf("a 401 whose body took 1.5s of 2s = %v, want status %d, kind %v, \"Bad credentials\", as with no Discard", err, want.Status, want.Kind)
			}
			if got := wire.requests(); got != 1 {
				t.Errorf("the wire saw %d request(s), want 1: 0.5s left cannot hold the 1s wait", got)
			}
			if got := spy.times("Retried:Whoami"); got != 0 {
				t.Errorf("the Retried counter fired %d time(s), want 0: nothing was resent", got)
			}
			if line := onlyRecord(t, buf); line["attempt"] != float64(1) || line["retried_on"] != nil {
				t.Errorf("the record = attempt %v, retried_on %v, want 1 and none", line["attempt"], line["retried_on"])
			}
		})
	})
	t.Run("a_body_over_the_bound", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			source := &discardingSource{retry: true}
			wire := &lateBodyWire{body: badCredentials}
			conn := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"},
				forgeapi.WithWireTransport(wire), forgeapi.WithCredentialSource(source), forgeapi.WithRetries(0))
			req := whoami()
			req.MaxBytes = 64
			_, err := conn.Do(t.Context(), req)
			var fe *forgeapi.Error
			if !asError(err, &fe) || fe.Code != forgeapi.CodeResponseTooLarge || fe.Status != http.StatusUnauthorized {
				t.Fatalf("a 401 over a 64-byte bound = %v, want response_too_large with status 401", err)
			}
			if got := wire.requests(); got != 1 {
				t.Errorf("the wire saw %d request(s), want 1: an unreadable answer is not resent", got)
			}
		})
	})
}

func TestA401EndsTheRequestWhenNoSourceAsksForAResend(t *testing.T) {
	t.Run("a_discard_answering_false", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			source := &discardingSource{}
			dc := openDiscardCase(t, source, newScriptedWire(http.StatusUnauthorized, http.StatusOK))
			_, err := dc.conn.Do(t.Context(), whoami())
			var fe *forgeapi.Error
			if !asError(err, &fe) || fe.Status != http.StatusUnauthorized {
				t.Fatalf("a read answered 401 = %v, want the 401's *forgeapi.Error", err)
			}
			if auth, _ := dc.wire.sent(); len(auth) != 1 {
				t.Errorf("the wire saw %d request(s), want 1", len(auth))
			}
			if got := source.discarded(); !slices.Equal(got, []string{"discard-placeholder"}) {
				t.Errorf("Discard was told %q, want the token the 401 answered, once", got)
			}
			if got := dc.spy.times("Retried:Whoami"); got != 0 {
				t.Errorf("the Retried counter fired %d time(s), want 0", got)
			}
		})
	})
	t.Run("a_source_without_discard", func(t *testing.T) {
		dc := openDiscardCase(t, stubCredential{}, newScriptedWire(http.StatusUnauthorized, http.StatusOK))
		if _, err := dc.conn.Do(t.Context(), whoami()); err == nil {
			t.Error("a read answered 401 = nil, want the 401's error")
		}
		if auth, _ := dc.wire.sent(); len(auth) != 1 {
			t.Errorf("the wire saw %d request(s), want 1", len(auth))
		}
	})
}

func TestAnAnswerOtherThan401TellsDiscardNothing(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			source := &discardingSource{retry: true}
			dc := openDiscardCase(t, source, newScriptedWire(status))
			_, _ = dc.conn.Do(t.Context(), whoami())
			if got := source.discarded(); len(got) != 0 {
				t.Errorf("a %d told Discard %q, want nothing", status, got)
			}
		})
	}
}
