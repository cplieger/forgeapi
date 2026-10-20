package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

// TestADeferralEndsWhenTheReportedWindowRenews holds the reserve to the window the
// instance named: a read deferred at the reserve stays deferred until Reset, and from
// Reset on it is sent, because only a response can report the renewed budget.
func TestADeferralEndsWhenTheReportedWindowRenews(t *testing.T) {
	clock := newFakeClock()
	sent := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	defer srv.Close()
	opts := testOptions()
	opts.Clock = clock.read
	opts.Signal = func(http.Header) (int, time.Time, bool) { return 1, clock.read().Add(time.Minute), true }
	c := openConnWith(t, forgeapi.Connection{WebBaseURL: srv.URL}, opts, forgeapi.WithMutationReserve(1))
	read := func() error {
		_, err := c.Do(Call(t.Context(), "ListPRs"), &Request{Op: "ListPRs", Method: http.MethodGet, Path: "/x"})
		return err
	}
	if err := read(); err != nil {
		t.Fatalf("Setup: the first read = %v, want nil", err)
	}
	clock.advance(59 * time.Second)
	if err := read(); !IsDeferred(err) {
		t.Fatalf("a read at the reserve one second before Reset = %v, want the deferral", err)
	}
	clock.advance(time.Second)
	if err := read(); err != nil {
		t.Fatalf("a read at Reset = %v, want it sent: the window it was deferred against has renewed", err)
	}
	if got := sent.Load(); got != 2 {
		t.Errorf("the instance saw %d request(s), want 2: the deferred read sends nothing and the read at Reset is sent", got)
	}
	if got, want := c.BudgetState(), clock.read().Add(time.Minute); got.Remaining != 1 || !got.Reset.Equal(want) {
		t.Errorf("BudgetState after the read at Reset = (Remaining %d, Reset %v), want (1, %v): its response reports the renewed window", got.Remaining, got.Reset, want)
	}
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

func pooledConn(t *testing.T, pools map[string][3]string, reserve int, parent budgetParent) (*Conn, func(path string) int64) {
	t.Helper()
	var mu sync.Mutex
	sent := map[string]int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		mu.Lock()
		sent[path]++
		mu.Unlock()
		p := pools[path]
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Pool", p[0])
		w.Header().Set("X-Remaining", p[1])
		w.Header().Set("X-Limit", p[2])
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	opts := testOptions()
	opts.Pool = func(h http.Header) (string, int) {
		limit, _ := strconv.Atoi(h.Get("X-Limit"))
		return h.Get("X-Pool"), limit
	}
	opts.PoolParent = parent
	c := openConnWith(t, forgeapi.Connection{WebBaseURL: srv.URL}, opts, forgeapi.WithMutationReserve(reserve))
	return c, func(path string) int64 {
		mu.Lock()
		defer mu.Unlock()
		return sent[path]
	}
}

func readOp(t *testing.T, c *Conn, op, path string) error {
	t.Helper()
	_, err := c.Do(Call(t.Context(), op), &Request{Op: op, Method: http.MethodGet, Path: path})
	return err
}

// TestAReadIsHeldAgainstThePoolItsOperationDrewOn holds the reserve to the pool a
// read draws on where the product names its pools: the last response's figure may be
// another route's pool, and holding a read against it defers a read whose own pool
// has budget to spare.
func TestAReadIsHeldAgainstThePoolItsOperationDrewOn(t *testing.T) {
	c, sent := pooledConn(t, map[string][3]string{
		"/main":   {"main", "1990", "2000"},
		"/narrow": {"narrow", "5", "1000"},
	}, 10, nil)
	if err := readOp(t, c, "ListPRs", "/main"); err != nil {
		t.Fatalf("Setup: the first ListPRs = %v, want nil", err)
	}
	if err := readOp(t, c, "ListRepos", "/narrow"); err != nil {
		t.Fatalf("Setup: the first ListRepos = %v, want nil: no answer has placed the operation yet", err)
	}
	if got := c.BudgetState().Remaining; got != 5 {
		t.Fatalf("Setup: BudgetState.Remaining = %d, want 5, the last response's pool", got)
	}
	if err := readOp(t, c, "ListPRs", "/main"); err != nil {
		t.Errorf("ListPRs after another route's pool reached the reserve = %v, want it sent: its own pool has 1990 left", err)
	}
	if err := readOp(t, c, "ListPRs", "/main"); err != nil {
		t.Fatalf("Setup: ListPRs = %v, want nil", err)
	}
	if err := readOp(t, c, "ListRepos", "/narrow"); !IsDeferred(err) {
		t.Errorf("ListRepos with its own pool at 5 under a reserve of 10 = %v, want the deferral", err)
	}
	if got := c.BudgetState().Remaining; got != 5 {
		t.Errorf("BudgetState.Remaining after the deferral = %d, want 5, the figure of the pool that held the read", got)
	}
	if got, want := sent("/main")+sent("/narrow"), int64(4); got != want {
		t.Errorf("the instance saw %d request(s), want %d: only the deferred ListRepos is held back", got, want)
	}
}

// TestAPoolWhoseLimitIsUnderTheReserveIsNotHeld holds a pool too small to keep the
// reserve, as gitlab.com's 100-a-minute pool on its project listings is beside a
// reserve of 250: holding it would defer its every read after the first of each
// window.
func TestAPoolWhoseLimitIsUnderTheReserveIsNotHeld(t *testing.T) {
	c, sent := pooledConn(t, map[string][3]string{"/projects": {"burst", "99", "100"}}, 250, nil)
	for read := 1; read <= 2; read++ {
		if err := readOp(t, c, "ListRepos", "/projects"); err != nil {
			t.Fatalf("ListRepos %d of 2 from a pool of 100 under a reserve of 250 = %v, want it sent", read, err)
		}
	}
	if got := sent("/projects"); got != 2 {
		t.Errorf("the instance saw %d request(s), want 2", got)
	}
}

// TestAnUnnamedPoolHoldsEveryRead holds the product that reports one figure for every
// route to it: every read, an operation not answered yet included, is held against
// that figure.
func TestAnUnnamedPoolHoldsEveryRead(t *testing.T) {
	c, sent := pooledConn(t, map[string][3]string{"/user": {"", "5", "1000"}, "/labels": {"", "5", "1000"}}, 10, nil)
	if err := readOp(t, c, "Whoami", "/user"); err != nil {
		t.Fatalf("Setup: Whoami = %v, want nil", err)
	}
	if err := readOp(t, c, "ListLabels", "/labels"); !IsDeferred(err) {
		t.Errorf("a first ListLabels under the one figure at 5 with a reserve of 10 = %v, want the deferral", err)
	}
	if got := sent("/labels"); got != 0 {
		t.Errorf("the instance saw %d ListLabels request(s), want 0", got)
	}
}

// apiParent is a product whose burst pool overlaps its API-wide pool, which every
// read, the burst pool's included, counts against.
func apiParent(name string) string {
	if name == "api" {
		return ""
	}
	return "api"
}

// TestAReadUnderANarrowerPoolSpendsTheParentItOverlaps holds the reserve to the
// API-wide pool where an answer names only the narrower pool it also drew on: each
// such answer spends one of the parent's last figure, and once that reaches the
// reserve every read is held, the narrower pool's own and one not answered yet.
func TestAReadUnderANarrowerPoolSpendsTheParentItOverlaps(t *testing.T) {
	c, sent := pooledConn(t, map[string][3]string{
		"/user":     {"api", "252", "2000"},
		"/projects": {"burst", "90", "100"},
	}, 250, apiParent)
	if err := readOp(t, c, "Whoami", "/user"); err != nil {
		t.Fatalf("Setup: Whoami = %v, want nil", err)
	}
	for read := 1; read <= 2; read++ {
		if err := readOp(t, c, "ListRepos", "/projects"); err != nil {
			t.Fatalf("ListRepos %d of 2 with the parent at %d = %v, want it sent", read, 253-read, err)
		}
	}
	if err := readOp(t, c, "ListRepos", "/projects"); !IsDeferred(err) {
		t.Errorf("ListRepos after two answers took the parent from 252 to 250 under a reserve of 250 = %v, want the deferral", err)
	}
	if got := c.BudgetState().Remaining; got != 250 {
		t.Errorf("BudgetState.Remaining after the deferral = %d, want 250, the parent that held the read", got)
	}
	if err := readOp(t, c, "ListPRs", "/prs"); !IsDeferred(err) {
		t.Errorf("a first ListPRs with the parent at the reserve = %v, want the deferral", err)
	}
	if got := sent("/projects") + sent("/prs"); got != 2 {
		t.Errorf("the instance saw %d listing request(s), want 2", got)
	}
}

// TestEveryAnswerUnderANarrowerPoolSpendsTheParent holds the parent's count to the
// requests the instance answered rather than the reads asked for: a retried
// attempt and a followed redirect hop each spent the parent exactly once, so one
// read answered twice takes the parent from 252 to 250, the reserve of 250 holds
// the next read and a reserve of 249 sends it.
func TestEveryAnswerUnderANarrowerPoolSpendsTheParent(t *testing.T) {
	for _, test := range []struct {
		name  string
		first func(w http.ResponseWriter)
	}{
		{name: "a_retried_attempt", first: func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusBadGateway)
		}},
		{name: "a_followed_redirect_hop", first: func(w http.ResponseWriter) {
			w.Header().Set("Location", "/api/v1/projects-moved")
			w.WriteHeader(http.StatusFound)
		}},
	} {
		t.Run(test.name+"_holds_at_a_reserve_of_250", func(t *testing.T) {
			c, prs := twiceAnsweredListing(t, test.first, 250)
			if err := readOp(t, c, "ListPRs", "/prs"); !IsDeferred(err) {
				t.Errorf("ListPRs after two answers took the parent from 252 to 250 under a reserve of 250 = %v, want the deferral", err)
			}
			if got := prs.Load(); got != 0 {
				t.Errorf("the instance saw %d ListPRs request(s), want 0", got)
			}
		})
		t.Run(test.name+"_sends_at_a_reserve_of_249", func(t *testing.T) {
			c, prs := twiceAnsweredListing(t, test.first, 249)
			if err := readOp(t, c, "ListPRs", "/prs"); err != nil {
				t.Errorf("ListPRs after two answers took the parent from 252 to 250 under a reserve of 249 = %v, want it sent", err)
			}
			if got := prs.Load(); got != 1 {
				t.Errorf("the instance saw %d ListPRs request(s), want 1", got)
			}
		})
	}
}

// twiceAnsweredListing opens a connection under the given reserve whose parent
// reads 252, then sends one ListRepos the instance answers twice, first with
// first, and returns the connection and the instance's ListPRs count.
func twiceAnsweredListing(t *testing.T, first func(w http.ResponseWriter), reserve int) (*Conn, *atomic.Int32) {
	t.Helper()
	var listings, prs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user":
			w.Header().Set("X-Pool", "api")
			w.Header().Set("X-Remaining", "252")
			w.Header().Set("X-Limit", "2000")
		case "/api/v1/prs":
			prs.Add(1)
		default:
			w.Header().Set("X-Pool", "burst")
			w.Header().Set("X-Remaining", strconv.Itoa(int(90-listings.Load())))
			w.Header().Set("X-Limit", "100")
			if listings.Add(1) == 1 {
				first(w)
				return
			}
		}
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	opts := testOptions()
	opts.Pool = func(h http.Header) (string, int) {
		limit, _ := strconv.Atoi(h.Get("X-Limit"))
		return h.Get("X-Pool"), limit
	}
	opts.PoolParent = apiParent
	c := openConnWith(t, forgeapi.Connection{WebBaseURL: srv.URL}, opts, forgeapi.WithMutationReserve(reserve))
	if err := readOp(t, c, "Whoami", "/user"); err != nil {
		t.Fatalf("Setup: Whoami = %v, want nil", err)
	}
	if err := readOp(t, c, "ListRepos", "/projects"); err != nil {
		t.Fatalf("ListRepos with the parent at 252 = %v, want it sent", err)
	}
	if got := listings.Load(); got != 2 {
		t.Fatalf("Setup: the instance answered %d listing request(s), want 2", got)
	}
	return c, &prs
}

// TestANarrowPoolUnderTheReserveIsNotHeldBesideItsParent holds the exemption of a
// pool too small to keep the reserve where it overlaps a parent with budget to spare:
// its every read is sent.
func TestANarrowPoolUnderTheReserveIsNotHeldBesideItsParent(t *testing.T) {
	c, sent := pooledConn(t, map[string][3]string{
		"/user":     {"api", "1990", "2000"},
		"/projects": {"burst", "5", "100"},
	}, 250, apiParent)
	if err := readOp(t, c, "Whoami", "/user"); err != nil {
		t.Fatalf("Setup: Whoami = %v, want nil", err)
	}
	for read := 1; read <= 5; read++ {
		if err := readOp(t, c, "ListRepos", "/projects"); err != nil {
			t.Fatalf("ListRepos %d of 5 from a pool of 100 at 5 with its parent at %d = %v, want it sent", read, 1991-read, err)
		}
	}
	if got := sent("/projects"); got != 5 {
		t.Errorf("the instance saw %d ListRepos request(s), want 5", got)
	}
}
