package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/ssrf/v4"
)

func TestTheRetryableRuleFollowsKindAndStatus(t *testing.T) {
	for _, test := range []struct {
		name   string
		kind   forgeapi.ErrorKind
		status int
		want   bool
	}{
		{name: "a_transport_failure", kind: forgeapi.KindTransient, want: true},
		{name: "a_throttle", kind: forgeapi.KindRateLimited, status: http.StatusForbidden, want: true},
		{name: "a_deferral", kind: forgeapi.KindRateLimited, want: true},
		{name: "a_server_failure_mapped_upstream", kind: forgeapi.KindUpstream, status: http.StatusInternalServerError, want: true},
		{name: "a_timeout_status", kind: forgeapi.KindUpstream, status: http.StatusRequestTimeout, want: true},
		{name: "a_429_mapped_forbidden", kind: forgeapi.KindForbidden, status: http.StatusTooManyRequests, want: true},
		{name: "a_dead_credential", kind: forgeapi.KindUnauthorized, status: http.StatusUnauthorized},
		{name: "a_missing_permission", kind: forgeapi.KindForbidden, status: http.StatusForbidden},
		{name: "a_validation_refusal", kind: forgeapi.KindUpstream, status: http.StatusUnprocessableEntity},
		{name: "a_local_refusal", kind: forgeapi.KindUnknown},
		{name: "a_refused_handshake", kind: forgeapi.KindUpstream},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Retryable(test.kind, test.status); got != test.want {
				t.Errorf("Retryable(%v, %d) = %v, want %v", test.kind, test.status, got, test.want)
			}
		})
	}
}

// dialTimeoutError is the error net answers a dial whose own deadline passed, the one
// errors.Is reports as context.DeadlineExceeded.
func dialTimeoutError(t *testing.T) error {
	t.Helper()
	_, err := (&net.Dialer{Deadline: time.Now().Add(-time.Second)}).Dial("tcp", "192.0.2.1:443")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Setup: an expired dial = %v, want an error matching context.DeadlineExceeded", err)
	}
	return err
}

func unroutable(errno syscall.Errno) error {
	return fmt.Errorf("dial %q: all 1 addresses failed: %w", "forge.example",
		&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)})
}

type failureCase struct {
	name      string
	err       error
	code      string
	kind      forgeapi.ErrorKind
	retryable bool
	// notRetried is a retryable failure the library leaves to its caller.
	notRetried bool
}

func failureCases(t *testing.T) []failureCase {
	t.Helper()
	unreachable := func(name string, err error) failureCase {
		return failureCase{name: name, err: err, code: forgeapi.CodeUnreachable, kind: forgeapi.KindTransient, retryable: true}
	}
	refused := func(name string, err error, code string) failureCase {
		return failureCase{name: name, err: err, code: code, kind: forgeapi.KindUpstream}
	}
	timedOut := unreachable("a_live_operations_dial_timeout", fmt.Errorf("dial %q: all 1 addresses failed: %w", "forge.example", dialTimeoutError(t)))
	timedOut.notRetried = true
	denied := &ssrf.Error{Kind: ssrf.KindPolicyDenied, Host: "forge.example", Msg: "SSRF dial: resolved IP 10.0.0.1 is not public"}
	return []failureCase{
		unreachable("no_route_to_host", unroutable(syscall.EHOSTUNREACH)),
		unreachable("network_unreachable", unroutable(syscall.ENETUNREACH)),
		unreachable("a_failed_lookup", &ssrf.Error{Kind: ssrf.KindDNSFailed, Host: "forge.example", Msg: "dial: DNS lookup failed"}),
		timedOut,
		refused("a_policy_refusal", denied, forgeapi.CodePrivateAddressRefused),
		refused("a_wrapped_policy_refusal", fmt.Errorf("proxy dial: %w", denied), forgeapi.CodePrivateAddressRefused),
		refused("a_non_public_address", &ssrf.Error{Kind: ssrf.KindNonPublicIP, Host: "forge.example"}, forgeapi.CodePrivateAddressRefused),
		refused("a_refused_port", &ssrf.Error{Kind: ssrf.KindBadPort, Host: "forge.example"}, forgeapi.CodeRedirectPortRefused),
		refused("an_invalid_address", &ssrf.Error{Kind: ssrf.KindInvalidURL, Host: "forge.example"}, ""),
		refused("an_untrusted_certificate", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, forgeapi.CodeTLSRefused),
		refused("a_peer_that_is_not_tls", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, forgeapi.CodeTLSRefused),
		refused("a_plaintext_answer", http.ErrSchemeMismatch, forgeapi.CodeTLSRefused),
		refused("a_peer_alert", &net.OpError{Op: "remote error", Err: errors.New("tls: certificate required")}, forgeapi.CodeTLSRefused),
	}
}

// failingConn answers every attempt with err from beneath the retry layer, as the
// wire would, and counts the attempts.
func failingConn(t *testing.T, err error, extra ...forgeapi.Option) (*Conn, *atomic.Int64) {
	t.Helper()
	attempts := &atomic.Int64{}
	wire := roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts.Add(1)
		return nil, err
	})
	opts := append([]forgeapi.Option{forgeapi.WithWireTransport(wire)}, extra...)
	return openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"}, opts...), attempts
}

func TestATransportFailureIsClassifiedByWhatAnotherAttemptCanChange(t *testing.T) {
	for _, test := range failureCases(t) {
		t.Run(test.name, func(t *testing.T) {
			c, _ := failingConn(t, test.err, forgeapi.WithRetries(0))
			_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
			var fe *forgeapi.Error
			if !asError(err, &fe) {
				t.Fatalf("Do over %v = %v, want a *forgeapi.Error", test.err, err)
			}
			if fe.Code != test.code || fe.Kind != test.kind || fe.Status != 0 || fe.Retryable != test.retryable {
				t.Errorf("Do over %v = (code %q, kind %v, status %d, retryable %v), want (%q, %v, 0, %v)",
					test.err, fe.Code, fe.Kind, fe.Status, fe.Retryable, test.code, test.kind, test.retryable)
			}
			if test.code == forgeapi.CodeUnreachable && !strings.HasPrefix(fe.Message, "cannot reach forge.example: ") {
				t.Errorf("Do over %v = message %q, want it to name the host it could not reach", test.err, fe.Message)
			}
		})
	}
	t.Run("the_operations_deadline", func(t *testing.T) {
		wire := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"},
			forgeapi.WithWireTransport(wire), forgeapi.WithRetries(0), forgeapi.WithOperationTimeout(10*time.Millisecond))
		_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
		if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, new(*forgeapi.Error)) {
			t.Errorf("Do past the operation deadline = %v, want the bare deadline sentinel", err)
		}
	})
	t.Run("the_callers_cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		wire := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			cancel()
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"}, forgeapi.WithWireTransport(wire), forgeapi.WithRetries(0))
		_, err := c.Do(ctx, &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
		if !errors.Is(err, context.Canceled) || errors.As(err, new(*forgeapi.Error)) {
			t.Errorf("Do after the caller cancelled = %v, want the bare cancellation sentinel", err)
		}
	})
}

// A dial timeout is retryable but not retried: a second attempt timing out too ends
// as the operation's own deadline, which names no host.
func TestTheLibraryRetriesExactlyTheTransportFailuresItCallsRetryable(t *testing.T) {
	cases := append(failureCases(t),
		failureCase{name: "its_own_refusal_from_beneath_the_call", err: &forgeapi.Error{Code: forgeapi.CodeRedirectHopCap, Kind: forgeapi.KindUpstream}},
	)
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, attempts := failingConn(t, test.err, forgeapi.WithRetries(1), forgeapi.WithOperationTimeout(time.Hour))
				_, _ = c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
				want := int64(1)
				if test.retryable && !test.notRetried {
					want = 2
				}
				if got := attempts.Load(); got != want {
					t.Errorf("Do over %v made %d attempt(s), want %d", test.err, got, want)
				}
			})
		})
	}
}

// handshakeCounter starts a TLS server that counts the handshakes it is offered.
func handshakeCounter(t *testing.T, cfg *tls.Config) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	offered := &atomic.Int64{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		offered.Add(1)
		return nil, nil
	}
	srv.TLS = cfg
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, offered
}

func tlsConn(t *testing.T, conn *forgeapi.Connection) *Conn {
	t.Helper()
	c, err := Open(conn, settingsFor(
		forgeapi.WithCredentialSource(stubCredential{}),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithRetries(2),
		forgeapi.WithLogger(discardLogger()),
	), testOptions())
	if err != nil {
		t.Fatalf("Setup: Open(%q): %v", conn.WebBaseURL, err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestAnUntrustedCertificateIsRefusedOnce(t *testing.T) {
	srv, offered := handshakeCounter(t, &tls.Config{MinVersion: tls.VersionTLS12})
	c := tlsConn(t, &forgeapi.Connection{WebBaseURL: srv.URL})
	_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	var fe *forgeapi.Error
	if !asError(err, &fe) || fe.Code != forgeapi.CodeTLSRefused || fe.Retryable {
		t.Fatalf("a read from an instance whose certificate no trusted CA signed = %v, want code %q, not retryable", err, forgeapi.CodeTLSRefused)
	}
	if got := offered.Load(); got != 1 {
		t.Errorf("the instance was offered %d handshake(s), want 1: another attempt meets the same certificate", got)
	}
}

func TestAPeerThatRefusesTheHandshakeIsRefusedOnce(t *testing.T) {
	srv, offered := handshakeCounter(t, &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert})
	c := tlsConn(t, &forgeapi.Connection{
		WebBaseURL: srv.URL,
		CABytes:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}),
	})
	_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	var fe *forgeapi.Error
	if !asError(err, &fe) || fe.Code != forgeapi.CodeTLSRefused || fe.Retryable {
		t.Fatalf("a read from an instance requiring a client certificate it was not given = %v, want code %q, not retryable", err, forgeapi.CodeTLSRefused)
	}
	if got := offered.Load(); got != 1 {
		t.Errorf("the instance was offered %d handshake(s), want 1: another attempt meets the same refusal", got)
	}
}

// A dial that times out on every address while the operation has time left names
// the instance, rather than ending as the operation's own deadline.
func TestABlackholedDialAnswersUnreachableWithItsHost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := openPipedConn(t, "http://forge.example", drain, forgeapi.WithRetries(2), forgeapi.WithOperationTimeout(time.Hour))
		dials := &atomic.Int64{}
		c.wire.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			dials.Add(1)
			timer := time.NewTimer(dialTimeout)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil, fmt.Errorf("dial %q: all 1 addresses failed: %w", "forge.example",
					&net.OpError{Op: "dial", Net: "tcp", Err: context.DeadlineExceeded})
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		started := time.Now()
		_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
		var fe *forgeapi.Error
		if !asError(err, &fe) || fe.Code != forgeapi.CodeUnreachable || !fe.Retryable {
			t.Fatalf("a read whose every dial timed out = %v, want code %q, retryable", err, forgeapi.CodeUnreachable)
		}
		if !strings.Contains(fe.Message, "forge.example") {
			t.Errorf("the failure = message %q, want it to name the host", fe.Message)
		}
		if got := dials.Load(); got != 1 {
			t.Errorf("the read dialled %d time(s), want 1", got)
		}
		if waited := time.Since(started); waited != dialTimeout {
			t.Errorf("the read failed after %v, want the one dial's %v", waited, dialTimeout)
		}
	})
}

// cutBody is a body the connection dropped partway through.
type cutBody struct{ sent bool }

func (b *cutBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, io.ErrUnexpectedEOF
	}
	b.sent = true
	return copy(p, "{"), nil
}

func (*cutBody) Close() error { return nil }

func bodyAnswer(status int, body func() io.ReadCloser) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
			Body: body(), Request: r,
		}, nil
	}
}

func TestABodyFailureIsClassifiedApartFromReachability(t *testing.T) {
	over := func(n int) func() io.ReadCloser {
		return func() io.ReadCloser { return io.NopCloser(strings.NewReader(strings.Repeat("a", n+1))) }
	}
	cut := func() io.ReadCloser { return &cutBody{} }
	for _, test := range []struct {
		name      string
		wire      roundTripFunc
		endpoint  bool
		code      string
		kind      forgeapi.ErrorKind
		retryable bool
	}{
		{name: "an_api_answer_over_its_bound", wire: bodyAnswer(http.StatusOK, over(16)), code: forgeapi.CodeResponseTooLarge, kind: forgeapi.KindUpstream},
		{name: "an_oauth_answer_over_its_bound", wire: bodyAnswer(http.StatusOK, over(maxEndpointBytes)), endpoint: true, code: forgeapi.CodeResponseTooLarge, kind: forgeapi.KindUpstream},
		{name: "an_api_answer_cut_short", wire: bodyAnswer(http.StatusOK, cut), kind: forgeapi.KindTransient, retryable: true},
		{name: "an_oauth_answer_cut_short", wire: bodyAnswer(http.StatusOK, cut), endpoint: true, kind: forgeapi.KindTransient, retryable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var err error
			if test.endpoint {
				c, openErr := OpenEndpoint(&forgeapi.Connection{WebBaseURL: "http://forge.example"}, settingsFor(
					forgeapi.WithWireTransport(test.wire), forgeapi.WithPlaintextHTTP(true),
					forgeapi.WithCredentialSource(stubCredential{}), forgeapi.WithLogger(discardLogger()),
				), forgeapi.FamilyGitHub)
				if openErr != nil {
					t.Fatalf("Setup: OpenEndpoint: %v", openErr)
				}
				t.Cleanup(c.Close)
				_, err = c.PostForm(t.Context(), "Token", "/login/oauth/access_token", url.Values{"a": {"b"}})
			} else {
				c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"}, forgeapi.WithWireTransport(test.wire))
				_, err = c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user", MaxBytes: 16})
			}
			var fe *forgeapi.Error
			if !asError(err, &fe) {
				t.Fatalf("the read = %v, want a *forgeapi.Error", err)
			}
			if fe.Code != test.code || fe.Kind != test.kind || fe.Status != http.StatusOK || fe.Retryable != test.retryable {
				t.Errorf("the read = (code %q, kind %v, status %d, retryable %v), want (%q, %v, 200, %v)",
					fe.Code, fe.Kind, fe.Status, fe.Retryable, test.code, test.kind, test.retryable)
			}
		})
	}
}
