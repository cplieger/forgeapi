package transport

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/cplieger/forgeapi"
)

// governor is one connection's cost-aware pacing.
//
// It is per CONNECTION, because a budget is per token per instance and a shared
// one would starve one forge for another's spending, and it sits UNDER the
// per-call caps rather than replacing them, so a cycle is bounded by whichever it
// reaches first. What it can do that no cap can is see how much budget upstream
// has left, because that signal rides every response and never crosses the
// library's surface.
type governor struct {
	counters  forgeapi.Counters
	now       func() time.Time
	reset     time.Time
	window    time.Time
	cursor    forgeapi.RotationCursor
	budget    forgeapi.Budget
	windowLen time.Duration
	spent     time.Duration
	foldCost  time.Duration
	family    forgeapi.Family
	remaining int
	lastCost  int
	reserve   int
	reads     int
	folds     int
	mu        sync.Mutex
}

func newGovernor(set *forgeapi.Settings, counters *forgeapi.Counters, family forgeapi.Family, clock func() time.Time) *governor {
	if clock == nil {
		clock = time.Now
	}
	return &governor{
		counters:  *counters,
		now:       clock,
		cursor:    set.RotationCursor,
		budget:    set.Budget,
		windowLen: windowLength(set.Budget.StatusTimePerInterval),
		family:    family,
		remaining: forgeapi.BudgetRemainingUnknown,
		reserve:   set.MutationReserve,
	}
}

// windowLength is the rolling interval's own LENGTH, held separately from the
// wall-clock allowance spent inside it.
//
// A zero allowance is the case the two must not share a field for. Read as the
// interval's length it rolls the window on every single call, which resets the COUNT
// cap too, so a zero on one knob switches the other off; read as the allowance it
// means what the option says it means, that no fold is admitted. So a zero takes the
// library's own interval and states nothing about its length.
func windowLength(clock time.Duration) time.Duration {
	if clock > 0 {
		return clock
	}
	return forgeapi.DefaultStatusTimePerInterval
}

// state is the read-only view. Remaining is what the FORGE reported for the
// credential, which on two of the four products is the account's whole quota
// rather than this client's share of it.
func (g *governor) state() forgeapi.BudgetState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return forgeapi.BudgetState{
		Remaining:      g.remaining,
		Reset:          g.reset,
		LastCost:       g.lastCost,
		RotationCursor: g.cursor,
	}
}

// observe reads the product's budget signal off one response.
func (g *governor) observe(header http.Header, signal Signal) {
	if signal == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if remaining, reset, ok := signal(header); ok {
		g.remaining = remaining
		g.reset = reset
	}
}

// price publishes what the call in flight has SENT so far, counted at the wire and
// carried on that call's own context.
//
// It is a call's price rather than a request's, because on this family a list costs
// one request plus one per row and the constant one would report a figure an order of
// magnitude below what the poller's commonest call actually spent. Four properties
// follow from counting the call rather than the operation. The figure is that call's
// own, so it resets with each one. A failed attempt counts, which is the rule every
// published price obeys and the reason the counter sits at the wire rather than on
// the answers. A ZERO is a figure like any other, because a call refused before the
// wire spent nothing and the field names the last call. And where two calls interleave
// on one connection the figure is the more recent one's, which is the only reading one
// value can carry.
func (g *governor) price(cost int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastCost = cost
}

// admit reports whether one further read may be issued, and why not when it may
// not. A read that would cross the mutation reserve is DEFERRED rather than
// issued, so a cycle of reads never spends what the merge the user is about to
// click will need.
func (g *governor) admit(op string) (forgeapi.PartialReason, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.remaining != forgeapi.BudgetRemainingUnknown && g.remaining <= g.reserve {
		if g.counters.ReadDeferred != nil {
			g.counters.ReadDeferred(g.family, op)
		}
		return forgeapi.PartialRateLimited, false
	}
	return forgeapi.PartialUnknown, true
}

// admitFold reports whether one folded-status read may be issued inside the
// current rolling interval, which is what the two per-interval knobs bound: how
// MANY such reads it may issue and how much wall clock they may spend. A read past
// either is not sent and its row is marked with the budget reason.
func (g *governor) admitFold(op string) (forgeapi.PartialReason, bool) {
	if reason, ok := g.admit(op); !ok {
		return reason, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if g.window.IsZero() || now.Sub(g.window) >= g.windowLen {
		g.window, g.reads, g.spent = now, 0, 0
	}
	if g.reads >= g.budget.StatusReadsPerInterval || g.spent >= g.budget.StatusTimePerInterval {
		return forgeapi.PartialBudget, false
	}
	// A read that does not FIT in the interval's remaining clock is not sent
	// either, which is the bound as the option states it. What one costs is
	// measured rather than assumed, so the first fold on a connection is admitted
	// and every later one is held to what its predecessors took.
	if cost := g.meanFoldCost(); cost > 0 && cost > g.windowLen-now.Sub(g.window) {
		return forgeapi.PartialBudget, false
	}
	g.reads++
	return forgeapi.PartialUnknown, true
}

// meanFoldCost is what one folded-status read has cost on this connection so far,
// zero before the first one has been measured.
func (g *governor) meanFoldCost() time.Duration {
	if g.folds == 0 {
		return 0
	}
	return g.foldCost / time.Duration(g.folds)
}

// spendFold charges one folded-status read's measured wall clock to the current
// interval and to the running cost of such a read, which is the half of the clock
// bound no cap can supply for itself: the knob is time SPENT, so something has to
// report what was spent.
func (g *governor) spendFold(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.spent += d
	g.foldCost += d
	g.folds++
}

// rotate records the row the rotation reached, so the next interval starts after it
// rather than at the first and no row starves under a poller that always presents
// the same lists in the same order. Which LIST that row was in is the family's own
// state, because this cursor is the one value that crosses the surface and a
// consumer persists it across a restart where no list exists.
func (g *governor) rotate(cursor forgeapi.RotationCursor) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cursor = cursor
}

// DiagID mints the diagnostic id one failed operation carries: 64 random bits
// rendered as base32, in the user-visible error and in every log line for that
// operation, retained only in the log. It is not a session id, not stable across
// retries and never a lookup key for state.
func DiagID() string {
	var raw [8]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		binary.BigEndian.PutUint64(raw[:], uint64(time.Now().UnixNano()))
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])
}

// jsonEncoder is the request-body encoder. It is a function rather than an inline
// construction so the one place a body is encoded is the one place its settings
// live.
func jsonEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc
}
