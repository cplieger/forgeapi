package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// TestTheLastCostIsTheCallsOwnPrice holds the three properties the published price
// rests on, each of which a consumer reads as this call's cost.
//
// A price is one CALL's, so it resets with each call rather than growing under a
// poller that repeats one operation. It counts every request this library sent,
// failed attempts included, because a refused request spends the user's shared quota
// exactly as an answered one does. And a call that reached no wire publishes zero,
// because the field names the LAST call and that one spent nothing.
func TestTheLastCostIsTheCallsOwnPrice(t *testing.T) {
	t.Run("consecutive_calls_of_one_operation", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if _, err := io.WriteString(w, `{}`); err != nil {
				t.Errorf("Setup: writing the answer: %v", err)
			}
		}))
		defer srv.Close()
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL})
		for call := 1; call <= 3; call++ {
			ctx := Call(t.Context(), "Whoami")
			if _, err := c.Do(ctx, &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
				t.Fatalf("call %d = %v, want nil", call, err)
			}
			if got := c.BudgetState().LastCost; got != 1 {
				t.Errorf("after call %d, LastCost = %d, want 1: the figure is that call's price, not a running total over the operation's name", call, got)
			}
		}
	})
	t.Run("a_call_whose_first_attempt_was_refused", func(t *testing.T) {
		seen := &atomic.Int64{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if seen.Add(1) == 1 {
				w.WriteHeader(http.StatusBadGateway)
				if _, err := io.WriteString(w, `{"message":"gateway"}`); err != nil {
					t.Errorf("Setup: writing the gateway answer: %v", err)
				}
				return
			}
			if _, err := io.WriteString(w, `{}`); err != nil {
				t.Errorf("Setup: writing the answer: %v", err)
			}
		}))
		defer srv.Close()
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithRetries(2))
		ctx := Call(t.Context(), "Whoami")
		if _, err := c.Do(ctx, &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
			t.Fatalf("a read whose first attempt answered 502 = %v, want nil after the retry", err)
		}
		if got, want := c.BudgetState().LastCost, int(seen.Load()); got != want {
			t.Errorf("LastCost = %d, want %d: the instance saw that many requests, and a refused one spends the same quota as an answered one", got, want)
		}
	})
	t.Run("two_concurrent_calls_of_one_operation", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if _, err := io.WriteString(w, `{}`); err != nil {
				t.Errorf("Setup: writing the answer: %v", err)
			}
		}))
		defer srv.Close()
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL})
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				ctx := Call(t.Context(), "Whoami")
				if _, err := c.Do(ctx, &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
					t.Errorf("a concurrent call = %v, want nil", err)
				}
			})
		}
		wg.Wait()
		if got := c.BudgetState().LastCost; got != 1 {
			t.Errorf("after two concurrent calls, LastCost = %d, want 1: each call sent one request, so the figure is one of the two and never their sum", got)
		}
	})
	t.Run("a_call_of_several_requests", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if _, err := io.WriteString(w, `{}`); err != nil {
				t.Errorf("Setup: writing the answer: %v", err)
			}
		}))
		defer srv.Close()
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL})
		ctx := Call(t.Context(), "ListPRs")
		for range 3 {
			if _, err := c.Do(ctx, &Request{Op: "ListPRs", Method: http.MethodGet, Path: "/x"}); err != nil {
				t.Fatalf("a request of the call = %v, want nil", err)
			}
		}
		if got := c.BudgetState().LastCost; got != 3 {
			t.Errorf("LastCost = %d, want 3: every request of one call is that call's price, which is what a list plus one fold per row costs", got)
		}
	})
	// The two arms a call can leave by without reaching the wire at all. On a
	// connection that has already answered one call, a figure left standing from
	// that call reports a cost for a call that spent nothing, which is the one
	// reading the published field cannot carry.
	t.Run("a_call_whose_context_was_already_done", func(t *testing.T) {
		// The remaining figure is well above the reserve here, so what stops this
		// call is its own context and not the governor: the two arms are separate
		// cases and each has to be reached by the thing it names.
		c := answeringConn(t, "Whoami", "9999")
		done, cancel := context.WithCancel(Call(t.Context(), "Whoami"))
		cancel()
		if _, err := c.Do(done, &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("a read on a cancelled context = %v, want the context sentinel: this call is stopped by its own context rather than by the governor", err)
		}
		if got := c.BudgetState().LastCost; got != 0 {
			t.Errorf("after a call that sent nothing, LastCost = %d, want 0: the figure is what the LAST call spent, and this one reached no wire", got)
		}
	})
	t.Run("a_call_the_governor_deferred", func(t *testing.T) {
		c := answeringConn(t, "ListPRs", "1", forgeapi.WithMutationReserve(1))
		if got := c.BudgetState().LastCost; got != 1 {
			t.Fatalf("Setup: LastCost = %d, want 1: the first call is what reads the remaining signal", got)
		}
		ctx := Call(t.Context(), "ListPRs")
		if _, err := c.Do(ctx, &Request{Op: "ListPRs", Method: http.MethodGet, Path: "/x"}); !IsDeferred(err) {
			t.Fatalf("a read past the mutation reserve = %v, want the deferral", err)
		}
		if got := c.BudgetState().LastCost; got != 0 {
			t.Errorf("after a deferred call, LastCost = %d, want 0: the request was never issued, so that call spent nothing", got)
		}
	})
}

// TestTheDeferralRendersWithNoGapWhereACauseWouldBe holds the one failure this library
// publishes with no code at all.
//
// A deferral is reported as the rate-limited KIND with no status, which is the pair an
// upstream throttle carries minus the status, and it mints no code of its own. The
// rendered string is what a user reads out beside the diagnostic id, so the absent
// cause must not surface as an empty slot between the operation and the message.
func TestTheDeferralRendersWithNoGapWhereACauseWouldBe(t *testing.T) {
	c := answeringConn(t, "ListPRs", "1", forgeapi.WithMutationReserve(1))
	ctx := Call(t.Context(), "ListPRs")
	_, err := c.Do(ctx, &Request{Op: "ListPRs", Method: http.MethodGet, Path: "/x"})
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("a read past the mutation reserve = %v, want a *forgeapi.Error", err)
	}
	if fe.Code != "" {
		t.Fatalf("the deferral = code %q, want none: this is the one failure with no code, and a code here would be a second spelling of the throttle", fe.Code)
	}
	if got, want := fe.Error(), "ListPRs: this read was deferred to hold the mutation reserve"; got != want {
		t.Errorf("the deferral renders as %q, want %q: the separator belongs to the cause, so a failure with no cause has no slot for one", got, want)
	}
}

// answeringConn is a connection whose instance answers every read and reports the
// remaining budget the caller names, with one call of op already made. It is the state
// both sent-nothing cases need: a figure already published by an earlier call, so a
// stale one is visible, and a remaining figure the case chooses, so the arm it means to
// reach is the arm that stops the next call.
func answeringConn(t *testing.T, op, remaining string, extra ...forgeapi.Option) *Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Remaining", remaining)
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, extra...)
	ctx := Call(t.Context(), op)
	if _, err := c.Do(ctx, &Request{Op: op, Method: http.MethodGet, Path: "/user"}); err != nil {
		t.Fatalf("Setup: the first call = %v, want nil", err)
	}
	return c
}

// TestAZeroOperationTimeoutIsADeadlineAlreadyPassed holds the option's own clause
// against the implementation.
//
// There is one configuration door, so the zero a caller passes is the literal zero,
// and the option publishes it as a deadline that has already passed. An uncapped
// context there is the opposite of what it says: every operation on that client runs
// unbounded while the doc comment promises the strictest bound on the surface.
func TestAZeroOperationTimeoutIsADeadlineAlreadyPassed(t *testing.T) {
	seen := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		seen.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	defer srv.Close()
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithOperationTimeout(0))
	_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a read under a zero operation bound = %v, want the deadline sentinel: the bound has already passed", err)
	}
	if got := seen.Load(); got != 0 {
		t.Errorf("the instance saw %d request(s), want 0: a deadline already passed admits none", got)
	}
}

// TestTheGovernorsWindowLengthIsNotTheClockAllowance holds the one coupling the two
// per-interval knobs must not have, which is why the length is held separately from the
// allowance spent inside it.
func TestTheGovernorsWindowLengthIsNotTheClockAllowance(t *testing.T) {
	if got := windowLength(0); got != forgeapi.DefaultStatusTimePerInterval {
		t.Errorf("windowLength(0) = %v, want %v: a zero allowance admits no fold, and rolling the window on every call would switch the count cap off instead", got, forgeapi.DefaultStatusTimePerInterval)
	}
	if got := windowLength(time.Minute); got != time.Minute {
		t.Errorf("windowLength(time.Minute) = %v, want %v", got, time.Minute)
	}
}

// Swaps the package's random source, so it must not run beside a test that reads it.
func TestADiagnosticIDRendersBitsFromTheRandomSource(t *testing.T) {
	source := rand.Reader
	t.Cleanup(func() { rand.Reader = source })
	rand.Reader = bytes.NewReader([]byte{0, 1, 2, 3, 4, 5, 6, 7})
	if got, want := diagID(), "AAAQEAYEAUDAO"; got != want {
		t.Errorf("diagID() over the bytes 0 to 7 = %q, want %q", got, want)
	}
}

type fakeClock struct {
	now time.Time
	mu  sync.Mutex
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func foldConn(t *testing.T, clock *fakeClock, reads int) *Conn {
	t.Helper()
	opts := testOptions()
	opts.Clock = clock.read
	return openConnWith(t, forgeapi.Connection{WebBaseURL: "http://forge.example"}, opts,
		forgeapi.WithStatusReadsPerInterval(reads),
		forgeapi.WithStatusTimePerInterval(time.Minute),
	)
}

func TestAFoldedReadPastTheCountCapWaitsForTheNextInterval(t *testing.T) {
	clock := newFakeClock()
	c := foldConn(t, clock, 2)
	for read := 1; read <= 2; read++ {
		if reason, ok := c.AdmitFold("ListPRs"); !ok {
			t.Fatalf("folded read %d of 2 = (%v, false), want it admitted", read, reason)
		}
	}
	if reason, ok := c.AdmitFold("ListPRs"); ok || reason != forgeapi.PartialBudget {
		t.Fatalf("folded read 3 of 2 = (%v, %v), want (%v, false)", reason, ok, forgeapi.PartialBudget)
	}
	clock.advance(time.Minute)
	if reason, ok := c.AdmitFold("ListPRs"); !ok {
		t.Errorf("a folded read one minute into a one-minute interval = (%v, false), want it admitted: that minute began the next interval", reason)
	}
}

func TestAFoldedReadIsNotSentOnceTheIntervalsClockIsSpent(t *testing.T) {
	c := foldConn(t, newFakeClock(), 10)
	if reason, ok := c.AdmitFold("ListPRs"); !ok {
		t.Fatalf("the first folded read = (%v, false), want it admitted", reason)
	}
	c.SpendFold(time.Minute)
	if reason, ok := c.AdmitFold("ListPRs"); ok || reason != forgeapi.PartialBudget {
		t.Errorf("a folded read after one that took the whole minute = (%v, %v), want (%v, false)", reason, ok, forgeapi.PartialBudget)
	}
}

func TestAFoldedReadIsAdmittedOnlyWhereItsMeasuredCostFitsTheIntervalLeft(t *testing.T) {
	clock := newFakeClock()
	c := foldConn(t, clock, 10)
	for read := 1; read <= 2; read++ {
		if reason, ok := c.AdmitFold("ListPRs"); !ok {
			t.Fatalf("folded read %d = (%v, false), want it admitted", read, reason)
		}
		c.SpendFold(10 * time.Second)
	}
	clock.advance(50 * time.Second)
	if reason, ok := c.AdmitFold("ListPRs"); !ok {
		t.Errorf("a folded read costing 10s with 10s of the interval left = (%v, false), want it admitted: it fits exactly", reason)
	}
	clock.advance(time.Second)
	if reason, ok := c.AdmitFold("ListPRs"); ok || reason != forgeapi.PartialBudget {
		t.Errorf("a folded read costing 10s with 9s of the interval left = (%v, %v), want (%v, false)", reason, ok, forgeapi.PartialBudget)
	}
}
