package transport

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"io"
	"maps"
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
	counters forgeapi.Counters
	now      func() time.Time
	// pools is each rate-limit pool's last reported figure, by the name the
	// product gives it, less the requests counted against it by parent since;
	// drawn is the pool each operation's last answer drew on.
	pools     map[string]poolFigure
	drawn     map[string]string
	parent    budgetParent
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

func newGovernor(set *forgeapi.Settings, counters *forgeapi.Counters, family forgeapi.Family, clock func() time.Time, parent budgetParent) *governor {
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
		pools:     map[string]poolFigure{},
		drawn:     map[string]string{},
		parent:    parent,
	}
}

// poolFigure is one rate-limit pool as its last response reported it; limit is
// zero where the response stated none.
type poolFigure struct {
	reset     time.Time
	remaining int
	limit     int
}

// holds reports whether reads must leave reserve of p untouched: p is at or
// under it, and its whole limit is above it. A pool whose whole limit is the
// reserve or less, such as gitlab.com's per-minute burst limit of 100 on one
// route (measured: RateLimit-Name burst_traffic_per_user_free_plan), would
// otherwise defer its every read after the first of each window.
func (p poolFigure) holds(reserve int) bool {
	return p.remaining <= reserve && (p.limit == 0 || p.limit > reserve)
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

func (g *governor) observe(op string, header http.Header, signal budgetSignal, pool budgetPool) {
	if signal == nil {
		return
	}
	remaining, reset, ok := signal(header)
	if !ok {
		return
	}
	var name string
	var limit int
	if pool != nil {
		name, limit = pool(header)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.remaining = remaining
	g.reset = reset
	g.pools[name] = poolFigure{remaining: remaining, reset: reset, limit: limit}
	g.drawn[op] = name
	// The answer reports name alone, so the request's draw on its parent is
	// counted here until an answer reports the parent again.
	if parent := g.parentOf(name); parent != "" {
		if p, ok := g.pools[parent]; ok && p.remaining > 0 {
			p.remaining--
			g.pools[parent] = p
		}
	}
}

func (g *governor) parentOf(name string) string {
	if g.parent == nil {
		return ""
	}
	if p := g.parent(name); p != name {
		return p
	}
	return ""
}

// expire forgets each observation whose reported window has renewed. Only a response
// can report the new budget, so a stale figure left in place would defer every read
// that could refresh it. The caller holds mu.
func (g *governor) expire() {
	now := g.now()
	if !g.reset.IsZero() && !now.Before(g.reset) {
		g.remaining, g.reset = forgeapi.BudgetRemainingUnknown, time.Time{}
	}
	maps.DeleteFunc(g.pools, func(_ string, p poolFigure) bool { return !p.reset.IsZero() && !now.Before(p.reset) })
}

// price publishes what the call in flight has SENT so far, counted at the wire and
// carried on that call's own context. It is a call's price, not a request's: on
// this family a list costs one request plus one per row. The figure resets with
// each call, counts a failed attempt (the rule every published price obeys, so the
// counter sits at the wire), is zero for a call refused before the wire, and where
// two calls interleave on one connection is the more recent one's.
func (g *governor) price(cost int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastCost = cost
}

// admit reports whether one further read may be issued, and why not when it may
// not. A read that would cross the mutation reserve is DEFERRED rather than
// issued, so a cycle of reads never spends what the merge the user is about to
// click will need. The pools held are the one op's last answer drew on, else the
// unnamed pool, and the parent that pool overlaps ([forgeapi.WithMutationReserve]
// owns why). A deferral publishes the pool it held, so the published figure is
// the budget that stopped the read.
func (g *governor) admit(op string) (forgeapi.PartialReason, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expire()
	held := []string{g.drawn[op]}
	if parent := g.parentOf(held[0]); parent != "" {
		held = append(held, parent)
	}
	for _, name := range held {
		p, ok := g.pools[name]
		if !ok || p.remaining == forgeapi.BudgetRemainingUnknown || !p.holds(g.reserve) {
			continue
		}
		g.remaining, g.reset = p.remaining, p.reset
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

// diagID mints the diagnostic id one failed operation carries: 64 random bits
// rendered as base32, in the user-visible error and in every log line for that
// operation, retained only in the log. It is not a session id, not stable across
// retries and never a lookup key for state.
func diagID() string {
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
