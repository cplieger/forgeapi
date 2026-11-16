package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/callmark"
)

// atReserve is a pool whose every answer reports 10 left of 5000, beside a reserve
// of 10: the figure at which the governor defers every ordinary read. A read is held
// against the pool its operation last drew on, so every read here is one operation's.
var atReserve = map[string][3]string{"/pulls": {"core", "10", "5000"}}

func readUnder(ctx context.Context, c *Conn, op string) error {
	_, err := c.Do(Call(ctx, op), &Request{Op: op, Method: http.MethodGet, Path: "/pulls"})
	return err
}

// placedAtReserve answers the first read, which places the pool at the reserve,
// and holds that a plain read is deferred there, so a read an action passes
// through afterwards is passed by the action's bound alone.
func placedAtReserve(t *testing.T, extra ...forgeapi.Option) (*Conn, func(path string) int64) {
	t.Helper()
	c, sent := pooledConn(t, atReserve, 10, nil, extra...)
	if err := readUnder(t.Context(), c, "ReadPR"); err != nil {
		t.Fatalf("Setup: the first read = %v, want it sent: no answer has placed the pool yet", err)
	}
	if err := readUnder(t.Context(), c, "ReadPR"); !IsDeferred(err) {
		t.Fatalf("Setup: a plain read at the reserve = %v, want the deferral", err)
	}
	return c, sent
}

func TestAnActionsReadsPassTheReserveUpToTheirBound(t *testing.T) {
	c, sent := placedAtReserve(t)
	action := forgeapi.ActionReads(t.Context(), 2)
	for i := 1; i <= 2; i++ {
		if err := readUnder(action, c, "ReadPR"); err != nil {
			t.Errorf("read %d under ActionReads(ctx, 2) at the reserve = %v, want it sent", i, err)
		}
	}
	err := readUnder(action, c, "ReadPR")
	var fe *forgeapi.Error
	if !asError(err, &fe) || !IsDeferred(err) || fe.Kind != forgeapi.KindRateLimited {
		t.Errorf("the third read under ActionReads(ctx, 2) at the reserve = %v, want the deferral of kind %v", err, forgeapi.KindRateLimited)
	}
	if got := sent("/pulls"); got != 3 {
		t.Errorf("the instance saw %d request(s), want 3: the setup read and the bound's two", got)
	}
}

func TestAnActionsReadIsDeferredAtAPoolOfZero(t *testing.T) {
	c, sent := pooledConn(t, map[string][3]string{"/pulls": {"core", "0", "5000"}}, 10, nil)
	if err := readUnder(t.Context(), c, "ReadPR"); err != nil {
		t.Fatalf("Setup: the first read = %v, want it sent", err)
	}
	action := forgeapi.ActionReads(t.Context(), 3)
	if err := readUnder(action, c, "ReadPR"); !IsDeferred(err) {
		t.Errorf("a read under ActionReads(ctx, 3) with its pool at zero = %v, want the deferral", err)
	}
	if got := sent("/pulls"); got != 1 {
		t.Errorf("the instance saw %d request(s), want 1: a pool at zero is sent nothing", got)
	}
	left := 0
	for callmark.TakeActionRead(action) {
		left++
	}
	if left != 3 {
		t.Errorf("the bound has %d read(s) left after the deferral at zero, want 3: a read not sent spends none", left)
	}
}

func TestANestedActionReadsKeepsTheOuterBound(t *testing.T) {
	c, sent := placedAtReserve(t)
	action := forgeapi.ActionReads(forgeapi.ActionReads(t.Context(), 2), 15)
	for i := 1; i <= 2; i++ {
		if err := readUnder(action, c, "ReadPR"); err != nil {
			t.Errorf("read %d under the nested bound = %v, want it sent", i, err)
		}
	}
	if err := readUnder(action, c, "ReadPR"); !IsDeferred(err) {
		t.Errorf("the third read under ActionReads(ActionReads(ctx, 2), 15) = %v, want the deferral: a nested call cannot raise the bound", err)
	}
	if got := sent("/pulls"); got != 3 {
		t.Errorf("the instance saw %d request(s), want 3", got)
	}
}

func TestAnActionsReadBound_ConcurrentReadsShareIt(t *testing.T) {
	c, sent := placedAtReserve(t)
	action := forgeapi.ActionReads(t.Context(), 4)
	var admitted, deferred atomic.Int64
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			switch err := readUnder(action, c, "ReadPR"); {
			case err == nil:
				admitted.Add(1)
			case IsDeferred(err):
				deferred.Add(1)
			default:
				t.Errorf("a concurrent read under the bound = %v, want it sent or deferred", err)
			}
		})
	}
	wg.Wait()
	if admitted.Load() != 4 || deferred.Load() != 8 {
		t.Errorf("12 concurrent reads under ActionReads(ctx, 4) = %d sent and %d deferred, want 4 and 8", admitted.Load(), deferred.Load())
	}
	if got := sent("/pulls"); got != 5 {
		t.Errorf("the instance saw %d request(s), want 5: the setup read and the bound's four", got)
	}
}

func TestAReadOnlyClientIgnoresTheActionsBound(t *testing.T) {
	c, sent := placedAtReserve(t, forgeapi.WithMutations(false))
	if err := readUnder(forgeapi.ActionReads(t.Context(), 5), c, "ReadPR"); !IsDeferred(err) {
		t.Errorf("a read under ActionReads on a read-only client at the reserve = %v, want the deferral", err)
	}
	if got := sent("/pulls"); got != 1 {
		t.Errorf("the instance saw %d request(s), want 1", got)
	}
}

// A write, a mutating operation's own read and a credential source's read are each
// governed as they are without the mark, and none of them spends the bound.
func TestOnlyAnActionsOwnReadsSpendItsBound(t *testing.T) {
	c, sent := placedAtReserve(t)
	action := forgeapi.ActionReads(t.Context(), 1)
	if _, err := c.Do(Call(action, "MergePR"), &Request{Op: "MergePR", Method: http.MethodPut, Path: "/pulls"}); err != nil {
		t.Fatalf("a write under the bound = %v, want it sent", err)
	}
	if _, err := c.Do(CallWrite(action, "MergePR"), &Request{Op: "MergePR", Method: http.MethodGet, Path: "/pulls"}); err != nil {
		t.Fatalf("a mutating operation's read under the bound = %v, want it sent as that operation's", err)
	}
	if _, err := c.Do(Call(action, "ReadPR"), &Request{Op: "ReadPR", Method: http.MethodGet, Path: "/pulls", Credential: true}); !IsDeferred(err) {
		t.Errorf("a credential read under the bound at the reserve = %v, want the deferral it meets without the mark", err)
	}
	if err := readUnder(action, c, "ReadPR"); err != nil {
		t.Errorf("the action's read after a write, a write's read and a credential read = %v, want it sent: none of them spent the bound", err)
	}
	if err := readUnder(action, c, "ReadPR"); !IsDeferred(err) {
		t.Errorf("a second read under ActionReads(ctx, 1) = %v, want the deferral", err)
	}
	if got := sent("/pulls"); got != 4 {
		t.Errorf("the instance saw %d request(s), want 4: the setup read, the write, the write's read and the action's one", got)
	}
}

func TestBudgetStateCountsTheActionsReads(t *testing.T) {
	var answered atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Remaining", strconv.FormatInt(11-answered.Add(1), 10))
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithMutationReserve(10))
	if err := readUnder(t.Context(), c, "ReadPR"); err != nil {
		t.Fatalf("Setup: the first read = %v, want it sent", err)
	}
	action := forgeapi.ActionReads(t.Context(), 2)
	for i := 1; i <= 2; i++ {
		if err := readUnder(action, c, "ReadPR"); err != nil {
			t.Fatalf("read %d under the bound = %v, want it sent", i, err)
		}
	}
	if got := c.BudgetState(); got.Remaining != 8 || got.LastCost != 1 {
		t.Errorf("BudgetState after the bound's two reads = remaining %d and last cost %d, want 8 and 1, the last answer's figure and that read's price", got.Remaining, got.LastCost)
	}
	if err := readUnder(action, c, "ReadPR"); !IsDeferred(err) {
		t.Errorf("the third read = %v, want the deferral", err)
	}
	if got := c.BudgetState().Remaining; got != 8 {
		t.Errorf("BudgetState.Remaining after the deferral = %d, want 8, the pool that held it", got)
	}
}
