package transport

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// heldRefusal stands in for a consumer's own refusal type, which the call's error
// must reach through errors.As.
type heldRefusal struct{ pool string }

func (r *heldRefusal) Error() string { return "the " + r.pool + " pool is held" }

// refusingHook answers refusal, without passing the attempt on, for every attempt
// from the from-th on, and counts the attempts it saw.
type refusingHook struct {
	refusal error
	calls   atomic.Int32
	from    int32
}

func (h *refusingHook) wrap(next http.RoundTripper) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if h.calls.Add(1) >= h.from {
			return nil, h.refusal
		}
		return next.RoundTrip(r)
	})
}

// failingServer answers 502 to every request, which the retry layer retries, and
// counts the requests it saw.
func failingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var sent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	return srv, &sent
}

func checkRefused(t *testing.T, err error, op string, refusal error) {
	t.Helper()
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("the refused call = %v (%T), want a *forgeapi.Error", err, err)
	}
	if fe.Code != forgeapi.CodeAttemptRefused || fe.Kind != forgeapi.KindUnknown || fe.Retryable || fe.Status != 0 {
		t.Errorf("the refused call = code %q kind %v retryable %t status %d, want %q, unknown, false, 0",
			fe.Code, fe.Kind, fe.Retryable, fe.Status, forgeapi.CodeAttemptRefused)
	}
	if fe.Op != op || fe.DiagID == "" {
		t.Errorf("the refused call = op %q diag %q, want op %q and a diagnostic id", fe.Op, fe.DiagID, op)
	}
	if fe.Refusal != refusal || !errors.Is(err, refusal) {
		t.Errorf("Error.Refusal = %v, want the hook's own error %v, reachable through errors.Is", fe.Refusal, refusal)
	}
}

func TestAttemptHookRefusalSendsNothing(t *testing.T) {
	read := Request{Op: "ReadPR", Method: http.MethodGet, Path: "/pulls/1"}
	write := Request{Op: "ClosePR", Method: http.MethodPatch, Path: "/pulls/1"}
	for _, test := range []struct {
		refusal error
		req     Request
		name    string
		write   bool
		from    int32
		sent    int32
	}{
		{name: "a_read", req: read, from: 1, refusal: &heldRefusal{pool: "core"}},
		{name: "a_write", req: write, write: true, from: 1, refusal: &heldRefusal{pool: "core"}},
		{name: "a_read_refused_on_its_retry", req: read, from: 2, sent: 1, refusal: &heldRefusal{pool: "core"}},
		{name: "a_refusal_that_is_a_forgeapi_error", req: read, from: 1, refusal: &forgeapi.Error{
			Code: forgeapi.CodeUnreachable, Kind: forgeapi.KindTransient, Retryable: true,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv, sent := failingServer(t)
			hook := &refusingHook{refusal: test.refusal, from: test.from}
			var retried atomic.Int32
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithRetries(2),
				forgeapi.WithAttemptObserver(hook.wrap),
				forgeapi.WithCounters(forgeapi.Counters{Retried: func(forgeapi.Family, string) { retried.Add(1) }}))
			ctx := Call(t.Context(), test.req.Op)
			if test.write {
				ctx = CallWrite(t.Context(), test.req.Op)
			}
			req := test.req
			_, err := c.Do(ctx, &req)
			checkRefused(t, err, req.Op, test.refusal)
			if want, held := test.refusal.(*heldRefusal); held {
				if got, ok := errors.AsType[*heldRefusal](err); !ok || got != want {
					t.Errorf("errors.As on the call's error = %p, %t, want the hook's own %p", got, ok, want)
				}
			}
			if got := sent.Load(); got != test.sent {
				t.Errorf("the instance saw %d request(s), want %d: a refused attempt sends nothing", got, test.sent)
			}
			if got := hook.calls.Load(); got != test.from {
				t.Errorf("the hook saw %d attempt(s), want %d: a refused attempt is never retried", got, test.from)
			}
			if got := retried.Load(); got != test.from-1 {
				t.Errorf("Counters.Retried fired %d time(s), want %d", got, test.from-1)
			}
			if got := c.BudgetState().LastCost; got != int(test.sent) {
				t.Errorf("LastCost = %d, want %d: a refused attempt costs nothing", got, test.sent)
			}
		})
	}
}

func TestAnOAuthEndpointAttemptTheHookRefusesSendsNothing(t *testing.T) {
	srv, sent := failingServer(t)
	hook := &refusingHook{refusal: &heldRefusal{pool: "core"}, from: 1}
	c, err := OpenEndpoint(&forgeapi.Connection{WebBaseURL: srv.URL}, settingsFor(
		forgeapi.WithWireTransport(&http.Transport{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(discardLogger()),
		forgeapi.WithAttemptObserver(hook.wrap),
	), forgeapi.FamilyGitHub)
	if err != nil {
		t.Fatalf("Setup: OpenEndpoint(%q): %v", srv.URL, err)
	}
	t.Cleanup(c.Close)
	_, err = c.PostForm(t.Context(), "PollDeviceGrant", "/login/oauth/access_token", url.Values{"device_code": {"d"}})
	checkRefused(t, err, "PollDeviceGrant", hook.refusal)
	if got := sent.Load(); got != 0 {
		t.Errorf("the instance saw %d request(s), want none", got)
	}
}

// A hook that passed the attempt on and then failed it reports the wire's failure,
// which is no refusal: the attempt was sent, and is retried as any other.
func TestAFailureBelowTheAttemptHookIsNoRefusal(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	var calls atomic.Int32
	hook := func(next http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			resp, err := next.RoundTrip(r)
			if err != nil {
				return nil, fmt.Errorf("observed: %w", err)
			}
			return resp, nil
		})
	}
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: base}, forgeapi.WithRetries(2), forgeapi.WithAttemptObserver(hook))
	_, err := c.Do(Call(t.Context(), "ReadPR"), &Request{Op: "ReadPR", Method: http.MethodGet, Path: "/pulls/1"})
	var fe *forgeapi.Error
	if !asError(err, &fe) || fe.Code != forgeapi.CodeUnreachable || fe.Refusal != nil {
		t.Fatalf("a read the wire failed below the hook = %v, want %q with no refusal", err, forgeapi.CodeUnreachable)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("the hook saw %d attempt(s), want 3: a sent attempt that failed is retried", got)
	}
}
