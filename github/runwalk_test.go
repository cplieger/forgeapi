package github

import (
	"cmp"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

type modelRun struct {
	created time.Time
	id      int64
}

// modelRuns lays runs out newest first from start. pairs are (seconds, runs each):
// that many consecutive seconds, each holding that many runs. Ids fall with time and
// are spaced as a busy repository's are, so a continuation naming them costs what
// it would on github.com.
func modelRuns(start time.Time, pairs ...int) []modelRun {
	var runs []modelRun
	id := int64(38_104_061_176)
	second := 0
	for i := 0; i+1 < len(pairs); i += 2 {
		for range pairs[i] {
			at := start.Add(-time.Duration(second) * time.Second)
			for range pairs[i+1] {
				id -= 137
				runs = append(runs, modelRun{id: id, created: at})
			}
			second++
		}
	}
	return runs
}

// runsListing serves runs as "List workflow runs for a repository" serves a created
// filter, per the reference and reads of a repository with 1,045 runs in 72 hours:
// ">=" or the inclusive "lo..hi" search-syntax range, newest first, one second's runs
// in an order the query decides, at most 100 a page with a next relation while the
// total holds more, through the first whole page reaching the 1,000th run; a page
// past that answers no rows, total_count 0 and no link.
// https://docs.github.com/en/rest/actions/workflow-runs#list-workflow-runs-for-a-repository
type runsListing struct {
	listed map[string][]modelRun
	tie    func(filter string, id int64) int64
	runs   []modelRun
	mu     sync.Mutex
}

// matching is the runs filter selects in the order the route serves them, kept per
// filter so a long walk sorts each one once.
func (l *runsListing) matching(filter string, lo, hi time.Time) []modelRun {
	l.mu.Lock()
	defer l.mu.Unlock()
	if listed, ok := l.listed[filter]; ok {
		return listed
	}
	var matching []modelRun
	for _, run := range l.runs {
		if !run.created.Before(lo) && !run.created.After(hi) {
			matching = append(matching, run)
		}
	}
	tie := l.tie
	if tie == nil {
		tie = tieRank
	}
	slices.SortStableFunc(matching, func(a, b modelRun) int {
		if c := b.created.Compare(a.created); c != 0 {
			return c
		}
		return cmp.Compare(tie(filter, a.id), tie(filter, b.id))
	})
	if l.listed == nil {
		l.listed = map[string][]modelRun{}
	}
	l.listed[filter] = matching
	return matching
}

func (l *runsListing) handle(t *testing.T, in *instance) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.EscapedPath()
		q := r.URL.Query()
		in.mu.Lock()
		in.requests = append(in.requests, key)
		in.queries[key] = q
		in.mu.Unlock()
		lo, hi, ok := createdRange(q.Get("created"))
		if key != runsRoute || !ok {
			http.Error(w, `{"message":"Validation Failed"}`, http.StatusUnprocessableEntity)
			return
		}
		matching := l.matching(q.Get("created"), lo, hi)
		n, _ := strconv.Atoi(q.Get("per_page"))
		n = min(n, restPerPageMax)
		page, _ := strconv.Atoi(q.Get("page"))
		answer := restWorkflowRunsBody{Runs: []map[string]any{}}
		if (page-1)*n < 1000 {
			answer.TotalCount = len(matching)
			for _, run := range matching[min((page-1)*n, len(matching)):min(page*n, len(matching))] {
				answer.Runs = append(answer.Runs, map[string]any{"id": run.id, "run_number": 1, "created_at": run.created.Format(time.RFC3339)})
			}
			if page*n < len(matching) {
				w.Header().Set("Link", `<https://forge.example/runs?page=`+strconv.Itoa(page+1)+`>; rel="next"`)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(answer); err != nil {
			t.Errorf("Setup: writing the run listing: %v", err)
		}
	}
}

type restWorkflowRunsBody struct {
	Runs       []map[string]any `json:"workflow_runs"`
	TotalCount int              `json:"total_count"`
}

// tieRank orders the runs of one second by the query that lists them, so two
// queries over one second may serve its runs in two orders.
func tieRank(filter string, id int64) int64 {
	h := fnv.New32a()
	fmt.Fprintf(h, "%s/%d", filter, id)
	return int64(uint16(h.Sum32()))
}

func createdRange(filter string) (lo, hi time.Time, ok bool) {
	far := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	if rest, found := strings.CutPrefix(filter, ">="); found {
		lo, err := time.Parse(time.RFC3339, rest)
		return lo, far, err == nil
	}
	from, to, found := strings.Cut(filter, "..")
	if !found {
		return lo, hi, false
	}
	lo, errLo := time.Parse(time.RFC3339, from)
	hi, errHi := time.Parse(time.RFC3339, to)
	return lo, hi, errLo == nil && errHi == nil && !hi.Before(lo)
}

// listingHarness is a client over runsListing, with a page cap no walk here reaches.
func listingHarness(t *testing.T, runs []modelRun) *harness {
	t.Helper()
	return orderedListingHarness(t, &runsListing{runs: runs})
}

func orderedListingHarness(t *testing.T, l *runsListing) *harness {
	t.Helper()
	in := newInstance()
	in.server = httptest.NewServer(l.handle(t, in))
	t.Cleanup(in.server.Close)
	return newHarnessOver(t, in, forgeapi.WithListPages(1<<20))
}

type walkedRuns struct {
	last    forgeapi.Page[forgeapi.Run]
	ids     []int64
	filters []string
	pages   int
}

// walkRuns follows one walk's continuations to its end, failing on an error, on a
// page before the last carrying a marker, and on a walk that does not end.
func walkRuns(t *testing.T, h *harness, opts ...forgeapi.ListOption) walkedRuns {
	t.Helper()
	var w walkedRuns
	var after forgeapi.Cursor
	for w.pages < 20_000 {
		call := opts
		if after != "" {
			call = append(slices.Clone(opts), forgeapi.WithAfter(after))
		}
		page, err := h.client.ListRuns(t.Context(), testRef(), call...)
		if err != nil {
			t.Fatalf("ListRuns page %d = %v, want the page", w.pages+1, err)
		}
		w.pages++
		w.filters = append(w.filters, h.instance.query(runsRoute, "created"))
		for _, run := range page.Items {
			w.ids = append(w.ids, run.Ref.ID)
		}
		if page.Next == "" {
			w.last = page
			return w
		}
		if page.Partial != nil {
			t.Errorf("ListRuns page %d = Partial %+v beside Next, want none: the page cap is out of reach", w.pages, page.Partial)
		}
		after = page.Next
	}
	t.Fatalf("ListRuns walked %d pages without ending", w.pages)
	return w
}

func duplicates(ids []int64) []int64 {
	seen := map[int64]bool{}
	var dup []int64
	for _, id := range ids {
		if seen[id] {
			dup = append(dup, id)
		}
		seen[id] = true
	}
	return dup
}

var walkBound = time.Date(2026, 10, 8, 2, 11, 46, 0, time.UTC)

// A walk under a since bound that reaches the route's window goes on under a created
// range ending at the oldest second the window served, so it answers every run
// created at or after the bound exactly once and ends complete.
func TestABoundedRunWalkPastTheResultWindowAnswersEveryRunOnce(t *testing.T) {
	start := walkBound.Add(72 * time.Hour)
	for name, c := range map[string]struct {
		runs      []modelRun
		pageBound int
		alone     bool
	}{
		// The 1,000th run shares its second with two more, as measured.
		"measured_shape_bound_100": {runs: modelRuns(start, 999, 1, 1, 3, 43, 1), pageBound: 100},
		"measured_shape_bound_30":  {runs: modelRuns(start, 999, 1, 1, 3, 43, 1), pageBound: 30},
		// The window's last page and the page before it both hold that second's runs.
		"second_across_the_last_two_pages_bound_2": {runs: modelRuns(start, 996, 1, 1, 7, 42, 1), pageBound: 2},
		// The window's last page is all one second, which then fills the narrowed
		// range's first two pages and part of its third.
		"second_across_three_pages_bound_100": {runs: modelRuns(start, 880, 1, 1, 250, 50, 1), pageBound: 100},
		"three_windows_bound_100":             {runs: modelRuns(start, 500, 1, 500, 2, 500, 3), pageBound: 100},
		"three_windows_bound_7":               {runs: modelRuns(start, 500, 1, 500, 2, 500, 3), pageBound: 7},
		// One second's runs answered before the window's last page are more than a
		// continuation names.
		"second_past_a_continuation_bound_100": {runs: modelRuns(start, 700, 1, 1, 400), pageBound: 100, alone: true},
		"second_past_a_continuation_bound_2":   {runs: modelRuns(start, 880, 1, 1, 200, 50, 1), pageBound: 2, alone: true},
		"a_window_in_one_second_bound_100":     {runs: modelRuns(start, 10, 1, 1, 1000, 10, 1), pageBound: 100, alone: true},
	} {
		t.Run(name, func(t *testing.T) {
			h := listingHarness(t, c.runs)
			w := walkRuns(t, h, forgeapi.WithSince(walkBound), forgeapi.WithPageBound(c.pageBound))
			if dup := duplicates(w.ids); len(dup) > 0 {
				t.Errorf("the walk answered %d run(s) twice, first %d", len(dup), dup[0])
			}
			if len(w.ids) != len(c.runs) {
				t.Errorf("the walk answered %d run(s), want all %d", len(w.ids), len(c.runs))
			}
			if w.last.Partial != nil {
				t.Errorf("the walk ended with Partial %+v, want none: every run was reachable", w.last.Partial)
			}
			narrowed, alone := 0, 0
			for _, f := range w.filters {
				if strings.HasPrefix(f, "2026-10-08T02:11:46Z..") {
					narrowed++
				}
				if lo, hi, found := strings.Cut(f, ".."); found && lo == hi {
					alone++
				}
			}
			if narrowed == 0 {
				t.Errorf("the walk sent created filters %v, want a range from the bound once past the window", slices.Compact(w.filters))
			}
			if c.alone && alone == 0 {
				t.Errorf("the walk sent created filters %v, want one second's own range for the second a continuation cannot name", slices.Compact(w.filters))
			}
		})
	}
}

// More runs than the window holds share one second, so no created range reaches the
// rest of that second: the walk answers every other run once, goes on below that
// second, and ends with the result window counting the walk and the runs of such
// seconds it never answered.
func TestABoundedRunWalkEndsWithTheResultWindowWhereOneSecondHoldsMoreThanTheWindow(t *testing.T) {
	start := walkBound.Add(time.Hour)
	for name, c := range map[string]struct {
		start time.Time
		runs  []int
	}{
		"990_then_1100_in_one_second":              {runs: []int{990, 1, 1, 1100}},
		"1100_in_one_second_partly_answered_first": {runs: []int{10, 1, 1, 1100}},
		"runs_below_a_dense_second":                {runs: []int{10, 1, 1, 1100, 50, 1}},
		"two_dense_seconds":                        {runs: []int{10, 1, 1, 1100, 20, 1, 1, 1200, 5, 1}},
		"a_dense_second_at_the_bound":              {start: walkBound.Add(10 * time.Second), runs: []int{10, 1, 1, 1100, 5, 1}},
	} {
		t.Run(name, func(t *testing.T) {
			runs := modelRuns(cmp.Or(c.start, start), c.runs...)
			w := walkRuns(t, listingHarness(t, runs), forgeapi.WithSince(walkBound), forgeapi.WithPageBound(100))
			if dup := duplicates(w.ids); len(dup) > 0 {
				t.Errorf("the walk answered %d run(s) twice, first %d", len(dup), dup[0])
			}
			reachable, dense := runsReached(runs, runWindow)
			if missed := missing(reachable, w.ids); len(missed) > 0 {
				t.Errorf("the walk left %d run(s) outside a second holding more than the window, first %d", len(missed), missed[0])
			}
			eligible := len(reachable) + dense
			p := w.last.Partial
			if p == nil || p.Reason != forgeapi.PartialResultWindow || p.Fetched != len(w.ids) || p.OmittedAtLeast != eligible-len(w.ids) || p.OmittedAtLeast < 1 {
				t.Errorf("the walk over %d run(s) at or after the bound answered %d and ended with Partial %+v, want result_window with %d fetched and %d omitted", eligible, len(w.ids), p, len(w.ids), eligible-len(w.ids))
			}
		})
	}
}

// Two created filters can serve two windows of one second's runs, so the runs a walk
// answered under one filter count beside the window that second's own range serves:
// the walk ends complete where the two together hold every run of the second.
func TestABoundedRunWalkCountsTheRunsOfADenseSecondItAnsweredBeforeReadingItAlone(t *testing.T) {
	start := walkBound.Add(time.Hour)
	// The open range serves the second's runs lowest id first, the second's own range
	// highest id first, so the open range's first page answers 90 runs the second's
	// own window leaves out.
	lowestFirst := func(filter string, id int64) int64 {
		if strings.HasPrefix(filter, ">=") {
			return id
		}
		return -id
	}
	for name, c := range map[string]struct {
		runs []int
		left int
	}{
		"the_two_windows_hold_the_second":        {runs: []int{10, 1, 1, 1090, 30, 1}},
		"the_two_windows_leave_runs_of_a_second": {runs: []int{10, 1, 1, 1100, 30, 1}, left: 10},
	} {
		t.Run(name, func(t *testing.T) {
			runs := modelRuns(start, c.runs...)
			w := walkRuns(t, orderedListingHarness(t, &runsListing{runs: runs, tie: lowestFirst}), forgeapi.WithSince(walkBound), forgeapi.WithPageBound(100))
			if dup := duplicates(w.ids); len(dup) > 0 {
				t.Errorf("the walk answered %d run(s) twice, first %d", len(dup), dup[0])
			}
			if len(w.ids) != len(runs)-c.left {
				t.Errorf("the walk answered %d of %d run(s), want %d", len(w.ids), len(runs), len(runs)-c.left)
			}
			p := w.last.Partial
			switch {
			case c.left == 0 && p != nil:
				t.Errorf("the walk ended with Partial %+v, want none: it answered every run", p)
			case c.left > 0 && (p == nil || p.Reason != forgeapi.PartialResultWindow || p.Fetched != len(w.ids) || p.OmittedAtLeast != c.left):
				t.Errorf("the walk ended with Partial %+v, want result_window with %d fetched and %d omitted", p, len(w.ids), c.left)
			}
		})
	}
}

// runsReached is the runs created at or after walkBound outside every second holding
// more than a range serves, which a walk answers whatever the order it is served them
// in, and the count of runs in those seconds.
func runsReached(runs []modelRun, served int) (reachable []int64, dense int) {
	per := map[time.Time]int{}
	for _, run := range runs {
		per[run.created]++
	}
	for _, run := range runs {
		switch {
		case run.created.Before(walkBound):
		case per[run.created] > served:
			dense++
		default:
			reachable = append(reachable, run.id)
		}
	}
	return reachable, dense
}

func missing(want, got []int64) []int64 {
	var missed []int64
	for _, id := range want {
		if !slices.Contains(got, id) {
			missed = append(missed, id)
		}
	}
	return missed
}

// sweptRuns is a random repository from start back: mostly one run a second, with
// bursts of up to burst runs in one second, ids falling by random gaps up to gap.
func sweptRuns(r *rand.Rand, start time.Time, total, burst int, gap int64) (runs []modelRun, densest int) {
	id := int64(38_104_061_176)
	for second := 0; len(runs) < total; second++ {
		n := 1
		if r.IntN(40) == 0 {
			n = 1 + r.IntN(burst)
		}
		densest = max(densest, n)
		for range n {
			id -= 1 + r.Int64N(gap)
			runs = append(runs, modelRun{id: id, created: start.Add(-time.Duration(second) * time.Second)})
		}
	}
	return runs, densest
}

// Over random repositories, a walk answers every run once but the ones of a second
// holding more runs than a range serves through the window that no filter it sent
// served, ends complete where it left none, and otherwise ends with the result window
// counting what it answered and what it left, whatever its page bound and however much
// room its continuation has for one second's runs.
func TestABoundedRunWalkEndsWithTheResultWindowOnlyWhereOneSecondHoldsMoreThanTheWindow(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	start := walkBound.Add(72 * time.Hour)
	for i := range 24 {
		bound := 2 + r.IntN(99)
		runs, densest := sweptRuns(r, start, 900+r.IntN(2600), []int{300, 1000, 1000, 1150}[i%4], []int64{137, 5000, 200_000}[i%3])
		n := min(bound, restPerPageMax)
		served := (runWindow + n - 1) / n * n
		t.Run(fmt.Sprintf("walk_%d_bound_%d_densest_%d", i, bound, densest), func(t *testing.T) {
			w := walkRuns(t, listingHarness(t, runs), forgeapi.WithSince(walkBound), forgeapi.WithPageBound(bound))
			if dup := duplicates(w.ids); len(dup) > 0 {
				t.Errorf("the walk answered %d run(s) twice, first %d", len(dup), dup[0])
			}
			if densest <= served && len(w.ids) != len(runs) {
				t.Errorf("the walk answered %d of %d run(s), want all: no second holds more than the %d a range serves", len(w.ids), len(runs), served)
			}
			reachable, _ := runsReached(runs, served)
			if missed := missing(reachable, w.ids); len(missed) > 0 {
				t.Errorf("the walk left %d run(s) outside a second holding more than the window, first %d", len(missed), missed[0])
			}
			left, p := len(runs)-len(w.ids), w.last.Partial
			switch {
			case left == 0 && p != nil:
				t.Errorf("the walk answered all %d run(s) and ended with Partial %+v, want none", len(runs), p)
			case left > 0 && (p == nil || p.Reason != forgeapi.PartialResultWindow || p.Fetched != len(w.ids) || p.OmittedAtLeast != left):
				t.Errorf("the walk answered %d of %d run(s) and ended with Partial %+v, want result_window with %d fetched and %d omitted", len(w.ids), len(runs), p, len(w.ids), left)
			}
		})
	}
}

func runCursor(t *testing.T, client *Client, w runWalk, opts ...forgeapi.ListOption) forgeapi.Cursor {
	t.Helper()
	w.call = listCall(t, client, "ListRuns", testRef(), opts...)
	return w.mint()
}

// boundedRunsPage is one page of a created-filtered run listing inside its window:
// rows one second apart, newest first from 2026-01-02, and the listing's whole total.
func boundedRunsPage(rows, total int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"total_count":%d,"workflow_runs":[`, total)
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for i := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":%d,"created_at":%q}`, i+1, base.Add(-time.Duration(i)*time.Second).Format(time.RFC3339))
	}
	b.WriteString("]}")
	return b.String()
}

var nextLink = http.Header{"Link": []string{`<https://forge.example/runs?page=next>; rel="next"`}}

// The page that reaches the window answers every run but those of its oldest second,
// and its continuation asks the inclusive created range from the bound to that second
// from the first page, so the next page serves that second's runs from its first row.
func TestABoundedRunWalkReachingTheResultWindowGoesOnBelowItsOldestSecond(t *testing.T) {
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		pageBound, page, rows, total int
		oldest                       string
	}{
		"bound_200_page_10": {pageBound: 200, page: 10, rows: 100, total: 1872, oldest: "2026-01-01T23:58:21Z"},
		"bound_30_page_34":  {pageBound: 30, page: 34, rows: 30, total: 2500, oldest: "2026-01-01T23:59:31Z"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{runsRoute: boundedRunsPage(c.rows, c.total)}, forgeapi.WithListPages(40))
			h.instance.answerHeaders(runsRoute, nextLink)
			opts := []forgeapi.ListOption{forgeapi.WithPageBound(c.pageBound), forgeapi.WithSince(bound)}
			after := runCursor(t, h.client, runWalk{page: c.page, window: c.page, fetched: (c.page - 1) * min(c.pageBound, 100)}, opts...)
			page, err := h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(after))...)
			if err != nil {
				t.Fatalf("ListRuns(page %d at bound %d under a since bound) = %v, want the page", c.page, c.pageBound, err)
			}
			if len(page.Items) != c.rows-1 || page.Partial != nil || page.Next == "" {
				t.Fatalf("ListRuns(page %d at bound %d, total %d) = %d row(s), Partial %+v, Next %q, want %d rows, no marker and a continuation", c.page, c.pageBound, c.total, len(page.Items), page.Partial, page.Next, c.rows-1)
			}
			h.instance.serve(runsRoute, `{"total_count":0,"workflow_runs":[]}`)
			if _, err := h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(page.Next))...); err != nil {
				t.Fatalf("ListRuns(WithAfter(Next)) = %v, want the page", err)
			}
			if got, want := h.instance.query(runsRoute, "created"), "2026-01-01T00:00:00Z.."+c.oldest; got != want {
				t.Errorf("ListRuns(WithAfter(Next)) sent created %q, want %q", got, want)
			}
			if got := h.instance.query(runsRoute, "page"); got != "1" {
				t.Errorf("ListRuns(WithAfter(Next)) sent page %q, want 1, the narrowed range's first", got)
			}
		})
	}
}

// The page before the window's last still has a page inside the window after it, so
// the walk goes on to it under the same filter rather than narrowing.
func TestABoundedRunWalkInsideTheResultWindowContinues(t *testing.T) {
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, map[string]string{runsRoute: boundedRunsPage(30, 2500)}, forgeapi.WithListPages(40))
	h.instance.answerHeaders(runsRoute, nextLink)
	opts := []forgeapi.ListOption{forgeapi.WithPageBound(30), forgeapi.WithSince(bound)}
	after := runCursor(t, h.client, runWalk{page: 33, window: 33, fetched: 960}, opts...)
	page, err := h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(after))...)
	if err != nil || len(page.Items) != 30 || page.Partial != nil || page.Next == "" {
		t.Fatalf("ListRuns(page 33 at bound 30, total 2500) = %d row(s), Partial %+v, Next %q, error %v, want 30 rows, no marker and a continuation", len(page.Items), page.Partial, page.Next, err)
	}
	if _, err := h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(page.Next))...); err != nil {
		t.Fatalf("ListRuns(WithAfter(Next)) = %v, want the page", err)
	}
	if got, want := h.instance.query(runsRoute, "created")+" page "+h.instance.query(runsRoute, "page"), ">=2026-01-01T00:00:00+00:00 page 34"; got != want {
		t.Errorf("ListRuns(WithAfter(Next)) sent created %q, want %q: page 34 is still inside the window", got, want)
	}
}

// The window's last page served the whole stated total, so its next relation leads
// only past the window, which GitHub serves no row of: the walk ends complete there.
func TestABoundedRunWalkFollowsNoNextRelationPastTheResultWindow(t *testing.T) {
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, map[string]string{runsRoute: boundedRunsPage(100, 950)}, forgeapi.WithListPages(40))
	h.instance.answerHeaders(runsRoute, nextLink)
	opts := []forgeapi.ListOption{forgeapi.WithPageBound(100), forgeapi.WithSince(bound)}
	after := runCursor(t, h.client, runWalk{page: 10, window: 10, fetched: 900}, opts...)
	page, err := h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(after))...)
	if err != nil || len(page.Items) != 100 || page.Next != "" || page.Partial != nil {
		t.Errorf("ListRuns(page 10 at bound 100, total 950, a next relation) = %d row(s), Next %q, Partial %+v, error %v, want 100 rows, complete", len(page.Items), page.Next, page.Partial, err)
	}
}

// A page whose listing sends no next relation while its total states more narrows as
// the window's last page does, and the narrowed range answers the held-back second.
func TestABoundedRunPageEndingShortOfItsTotalGoesOnBelowItsOldestSecond(t *testing.T) {
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, map[string]string{runsRoute: `{"total_count":10,"workflow_runs":[` +
		`{"id":6,"created_at":"2026-01-02T00:00:01Z"},{"id":5,"created_at":"2026-01-02T00:00:00Z"}]}`}, forgeapi.WithListPages(40))
	opts := []forgeapi.ListOption{forgeapi.WithPageBound(2), forgeapi.WithSince(bound)}
	page, err := h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(runCursor(t, h.client, runWalk{page: 3, window: 3, fetched: 4}, opts...)))...)
	if err != nil || len(page.Items) != 1 || page.Items[0].Ref.ID != 6 || page.Next == "" || page.Partial != nil {
		t.Fatalf("ListRuns(page 3 under a bound, total 10, no next relation) = %+v, error %v, want run 6 alone and a continuation", page, err)
	}
	h.instance.serve(runsRoute, `{"total_count":2,"workflow_runs":[{"id":5,"created_at":"2026-01-02T00:00:00Z"},{"id":4,"created_at":"2026-01-01T12:00:00Z"}]}`)
	last, err := h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(page.Next))...)
	if got, want := h.instance.query(runsRoute, "created"), "2026-01-01T00:00:00Z..2026-01-02T00:00:00Z"; got != want {
		t.Errorf("ListRuns(WithAfter(Next)) sent created %q, want %q", got, want)
	}
	if err != nil || len(last.Items) != 2 || last.Items[0].Ref.ID != 5 || last.Next != "" || last.Partial != nil {
		t.Errorf("ListRuns(the narrowed range) = %+v, error %v, want runs 5 and 4, complete", last, err)
	}
	unbounded := newHarness(t, map[string]string{runsRoute: `{"total_count":10,"workflow_runs":[{"id":2},{"id":1}]}`})
	if page, err := unbounded.client.ListRuns(t.Context(), testRef(), forgeapi.WithPageBound(2)); err != nil || page.Partial != nil || page.Next != "" {
		t.Errorf("ListRuns(no bound, no next, total 10) = Next %q, Partial %+v, error %v, want neither: the window applies to a filtered listing", page.Next, page.Partial, err)
	}
}

// A page that ends its range short of its total with no row goes on under the
// previous page's oldest second alone, dropping the runs of it already answered.
func TestABoundedRunPageServingNoRowGoesOnUnderThePreviousPagesOldestSecond(t *testing.T) {
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	second := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, map[string]string{runsRoute: `{"total_count":2500,"workflow_runs":[]}`}, forgeapi.WithListPages(40))
	opts := []forgeapi.ListOption{forgeapi.WithPageBound(100), forgeapi.WithSince(bound)}
	after := runCursor(t, h.client, runWalk{page: 3, window: 3, fetched: 200, seen: second.Unix(), ids: []int64{7}}, opts...)
	page, err := h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(after))...)
	if err != nil || len(page.Items) != 0 || page.Partial != nil || page.Next == "" {
		t.Fatalf("ListRuns(page 3 serving no row of 2500) = %+v, error %v, want no row, no marker and a continuation", page, err)
	}
	h.instance.serve(runsRoute, `{"total_count":2,"workflow_runs":[{"id":8,"created_at":"2026-01-02T00:00:00Z"},{"id":7,"created_at":"2026-01-02T00:00:00Z"}]}`)
	alone, err := h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(page.Next))...)
	if got, want := h.instance.query(runsRoute, "created"), "2026-01-02T00:00:00Z..2026-01-02T00:00:00Z"; got != want {
		t.Errorf("ListRuns(WithAfter(Next)) sent created %q, want %q", got, want)
	}
	if err != nil || len(alone.Items) != 1 || alone.Items[0].Ref.ID != 8 || alone.Next == "" || alone.Partial != nil {
		t.Errorf("ListRuns(the second's own range) = %+v, error %v, want run 8 alone and a continuation below the second", alone, err)
	}
}

// Short of its stated total, a range that served no row or a second's own range below
// the window has no second holding more runs than the window to explain the rest, so
// the walk fails on the total the route stated rather than ending with the result window.
func TestABoundedRunRangeEndingShortOfItsTotalBelowTheWindowFails(t *testing.T) {
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	second := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC).Unix()
	opts := []forgeapi.ListOption{forgeapi.WithPageBound(100), forgeapi.WithSince(bound)}
	for name, c := range map[string]struct {
		body string
		walk runWalk
	}{
		"open_range_first_page_with_no_row":    {body: `{"total_count":2500,"workflow_runs":[]}`},
		"open_range_pages_with_no_row":         {body: `{"total_count":2500,"workflow_runs":[]}`, walk: runWalk{page: 2, window: 2}},
		"bound_range_first_page_with_no_row":   {body: `{"total_count":2500,"workflow_runs":[]}`, walk: runWalk{page: 4, window: 1, kind: boundRange, upper: second, fetched: 300}},
		"second_range_short_on_its_first_page": {body: `{"total_count":5,"workflow_runs":[{"id":8,"created_at":"2026-01-02T00:00:00Z"}]}`, walk: runWalk{page: 4, window: 1, kind: secondRange, seen: second, fetched: 300}},
		"second_range_short_on_a_later_page":   {body: `{"total_count":500,"workflow_runs":[]}`, walk: runWalk{page: 6, window: 3, kind: secondRange, seen: second, fetched: 500}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{runsRoute: c.body}, forgeapi.WithListPages(40))
			call := opts
			if c.walk.page > 0 {
				call = append(slices.Clone(opts), forgeapi.WithAfter(runCursor(t, h.client, c.walk, opts...)))
			}
			page, err := h.client.ListRuns(t.Context(), testRef(), call...)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeValidation || fe.Kind != forgeapi.KindUpstream {
				t.Errorf("ListRuns(%s) = %+v, %v, want code %q of kind %v", name, page, err, forgeapi.CodeValidation, forgeapi.KindUpstream)
			}
		})
	}
}

// A page whose oldest second's runs fill its continuation to within 5 bytes of the
// cap holds that second back for its own range: that range runs to a later page
// and more answers, whose longer numbers would leave a continuation carrying the
// same runs over the cap.
func TestABoundedRunPageFillingItsContinuationHoldsItsOldestSecondBack(t *testing.T) {
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	second := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	opts := []forgeapi.ListOption{forgeapi.WithPageBound(100), forgeapi.WithSince(bound)}
	h := newHarness(t, nil, forgeapi.WithListPages(40))
	h.instance.answerHeaders(runsRoute, nextLink)
	call := listCall(t, h.client, "ListRuns", testRef(), opts...)
	for m := 1; m < 100; m++ {
		ids := make([]int64, m)
		for i := range ids {
			ids[i] = 1_000_000 + int64(i)*1000
		}
		candidate := runWalk{call: call, page: 10, window: 10, fetched: 900, seen: second.Unix(), ids: ids}
		if n := len(candidate.mint()); n < 508 || n > 512 {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, `{"total_count":5000,"workflow_runs":[{"id":1,"created_at":%q}`, second.Add(time.Second).Format(time.RFC3339))
		for _, id := range ids {
			fmt.Fprintf(&b, `,{"id":%d,"created_at":%q}`, id, second.Format(time.RFC3339))
		}
		b.WriteString("]}")
		h.instance.serve(runsRoute, b.String())
		after := runCursor(t, h.client, runWalk{page: 9, window: 9, fetched: 800}, opts...)
		page, err := h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(after))...)
		if err != nil || len(page.Items) != 1 || page.Items[0].Ref.ID != 1 || page.Next == "" {
			t.Fatalf("ListRuns(page 9, then %d runs of one second filling its continuation) = %+v, error %v, want run 1 alone and a continuation", m, page, err)
		}
		if _, err := h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(page.Next))...); err != nil {
			t.Fatalf("ListRuns(WithAfter(Next)) = %v, want the page", err)
		}
		if got, want := h.instance.query(runsRoute, "created"), "2026-01-02T00:00:00Z..2026-01-02T00:00:00Z"; got != want {
			t.Errorf("ListRuns(WithAfter(Next)) sent created %q, want %q", got, want)
		}
		return
	}
	t.Fatal("Setup: no run count fills the continuation to within 5 bytes of its cap")
}

// The page cap counts the walk's pages, so a narrowed range's first page is the walk's
// next page, not its first.
func TestTheListPageCapCountsABoundedRunWalkAcrossItsRanges(t *testing.T) {
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, map[string]string{runsRoute: boundedRunsPage(100, 500)})
	h.instance.answerHeaders(runsRoute, nextLink)
	opts := []forgeapi.ListOption{forgeapi.WithPageBound(100), forgeapi.WithSince(bound)}
	upper := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC).Unix()
	after := runCursor(t, h.client, runWalk{page: 11, window: 1, kind: boundRange, upper: upper, fetched: 999}, opts...)
	page, err := h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(after))...)
	if err != nil || page.Next == "" || page.Partial == nil || page.Partial.Reason != forgeapi.PartialPaginationCap {
		t.Errorf("ListRuns(page 11 of a walk, the first of its range, cap %d) = Next %q, Partial %+v, error %v, want pagination_cap beside a continuation", forgeapi.DefaultListPages, page.Next, page.Partial, err)
	}
	unpaged := newHarness(t, nil, forgeapi.WithListPages(0))
	first, err := unpaged.client.ListRuns(t.Context(), testRef(), opts...)
	if want := runCursor(t, unpaged.client, runWalk{page: 1, window: 1}, opts...); err != nil || first.Next != want || unpaged.instance.count() != 0 {
		t.Errorf("ListRuns(cap 0 under a bound) = Next %q, error %v after %d request(s), want %q and none", first.Next, err, unpaged.instance.count(), want)
	}
}

func TestAContinuationMintedUnderOneBoundIsRefusedUnderAnother(t *testing.T) {
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, nil)
	after := runCursor(t, h.client, runWalk{page: 2, window: 2, fetched: 30}, forgeapi.WithSince(bound))
	for name, opts := range map[string][]forgeapi.ListOption{
		"no_bound":      {forgeapi.WithAfter(after)},
		"another_bound": {forgeapi.WithSince(bound.Add(time.Second)), forgeapi.WithAfter(after)},
		"a_page_cursor": {forgeapi.WithSince(bound), forgeapi.WithAfter(restCursor(t, h.client, "ListRuns", testRef(), 2, forgeapi.WithSince(bound)))},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := h.client.ListRuns(t.Context(), testRef(), opts...)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
				t.Errorf("ListRuns(a continuation minted under %v) = %v, want code %q", bound, err, forgeapi.CodeCursorInvalid)
			}
		})
	}
	if got := h.instance.count(); got != 0 {
		t.Errorf("the refused continuations sent %d request(s), want 0", got)
	}
}

// A continuation is untrusted input on its way into a request, so one naming a
// position no walk this library runs can stand at is refused before any request.
func TestABoundedRunContinuationNamingNoWalkIsRefused(t *testing.T) {
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	since, upper := bound.Unix(), bound.Add(time.Hour).Unix()
	h := newHarness(t, nil)
	opts := []forgeapi.ListOption{forgeapi.WithSince(bound)}
	call := listCall(t, h.client, "ListRuns", testRef(), opts...)
	for name, position := range map[string][]int64{
		"window_zero":                         {2, 0, 0, 0, 0, 0, 0, 0},
		"window_past_the_page":                {2, 3, 0, 0, 0, 0, 0, 0},
		"open_range_window_behind_its_page":   {3, 1, 0, 0, 2, 0, 0, 0},
		"range_kind_three":                    {2, 1, 3, upper, 0, 0, 0, 0},
		"bound_range_on_the_walks_first_page": {1, 1, 1, upper, 0, 0, 0, 0},
		"bound_range_window_at_its_page":      {2, 2, 1, upper, 0, 0, 0, 0},
		"range_before_the_bound":              {2, 1, 1, since - 1, 0, 0, 0, 0},
		"upper_on_the_open_range":             {2, 2, 0, upper, 0, 0, 0, 0},
		"second_range_on_the_walks_page":      {2, 2, 2, 0, 0, upper, 0, 0},
		"second_range_naming_an_upper":        {3, 1, 2, upper, 0, upper, 0, 0},
		"second_range_before_the_bound":       {3, 1, 2, 0, 0, since - 1, 0, 0},
		"answers_on_the_first_page":           {1, 1, 0, 0, 1, 0, 0, 0},
		"answers_past_the_pages_before":       {3, 3, 0, 0, 201, 0, 0, 0},
		"negative_fetched":                    {2, 2, 0, 0, -1, 0, 0, 0},
		"ids_past_the_answers":                {3, 3, 0, 0, 1, upper, 0, 2, 5, 4},
		"second_without_ids":                  {2, 2, 0, 0, 0, upper, 0, 0},
		"ids_without_a_second":                {2, 2, 0, 0, 1, 0, 0, 1, 5},
		"ids_on_a_ranges_first_page":          {3, 1, 1, upper, 10, upper, 0, 1, 5},
		"second_before_the_bound":             {2, 2, 0, 0, 1, since - 1, 0, 1, 5},
		"second_above_the_range":              {3, 2, 1, upper, 1, upper + 1, 0, 1, 5},
		"id_count_short":                      {2, 2, 0, 0, 2, upper, 0, 2, 5},
		"id_count_negative":                   {2, 2, 0, 0, 1, upper, 0, -1, 5},
		"ids_not_ascending":                   {2, 2, 0, 0, 2, upper, 0, 2, 5, 0},
		"id_zero":                             {2, 2, 0, 0, 1, upper, 0, 1, 0},
		"too_few_numbers":                     {2, 2, 0, 0, 0, 0, 0},
		"ids_overflowing_the_number":          {2, 2, 0, 0, 2, upper, 0, 2, 1<<62 + 1, 1 << 62},
		"open_range_past_the_window":          {11, 11, 0, 0, 1000, upper, 0, 1, 5},
		"bound_range_past_the_window":         {12, 11, 1, upper, 1000, upper, 0, 1, 5},
		"second_range_past_the_window":        {12, 11, 2, 0, 1000, upper, 0, 0},
		"omitted_negative":                    {3, 1, 1, upper, 0, 0, -1, 0},
		"omitted_on_the_open_range":           {2, 2, 0, 0, 0, 0, 1, 0},
		"open_range_at_the_largest_page":      {math.MaxInt, math.MaxInt, 0, 0, 0, 0, 0, 0},
		"bound_range_at_the_largest_page":     {math.MaxInt, 1, 1, upper, 0, 0, 0, 0},
		"bound_range_past_the_page_reserve":   {math.MaxInt - runWindow - 1, 1, 1, upper, 0, 0, 0, 0},
		"second_range_at_the_largest_page":    {math.MaxInt, 1, 2, 0, 0, upper, 0, 0},
		"bound_range_past_the_answer_reserve": {math.MaxInt - runWindow - 2, 1, 1, upper, math.MaxInt - runWindow - 299, 0, 0, 0},
		"second_range_at_the_largest_answers": {math.MaxInt - runWindow - 2, 1, 2, 0, math.MaxInt, upper, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			after := call.MintWide(runWalkPrefix, position...)
			_, err := h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(after))...)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
				t.Errorf("ListRuns(WithAfter(%q)) = %v, want code %q", after, err, forgeapi.CodeCursorInvalid)
			}
		})
	}
	minted := runWalk{call: call, page: 3, window: 3, fetched: 200}
	edited := forgeapi.Cursor(strings.Replace(string(minted.mint()), "w3.3.", "w3.1.", 1))
	_, err := h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(edited))...)
	if fe := (*forgeapi.Error)(nil); !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
		t.Errorf("ListRuns(WithAfter(%q), the page-3 continuation edited to the open range's first page) = %v, want code %q", edited, err, forgeapi.CodeCursorInvalid)
	}
	if got := h.instance.count(); got != 0 {
		t.Errorf("the refused continuations sent %d request(s), want 0", got)
	}
	set := &forgeapi.ListSettings{Since: bound, SinceSet: true, PageBound: forgeapi.DefaultPageBound}
	for name, c := range map[string]struct {
		position []int64
		kind     int64
	}{
		"bound_range":                           {position: []int64{3, 2, 1, upper, 7, upper, 0, 2, 5, 9}, kind: boundRange},
		"bound_range_at_the_reserve":            {position: []int64{math.MaxInt - runWindow - 2, 2, 1, upper, math.MaxInt - runWindow - 300, upper, 0, 2, 5, 9}, kind: boundRange},
		"bound_range_behind_a_dense_second":     {position: []int64{3, 2, 1, upper, 7, upper, 4, 2, 5, 9}, kind: boundRange},
		"second_range_behind_a_dense_second":    {position: []int64{3, 1, 2, 0, 7, upper, 4, 2, 5, 9}, kind: secondRange},
		"second_range":                          {position: []int64{3, 1, 2, 0, 7, upper, 0, 2, 5, 9}, kind: secondRange},
		"open_range_at_the_windows_last_page":   {position: []int64{10, 10, 0, 0, 900, upper, 0, 2, 5, 9}, kind: openRange},
		"bound_range_at_the_windows_last_page":  {position: []int64{11, 10, 1, upper, 900, upper, 0, 2, 5, 9}, kind: boundRange},
		"second_range_at_the_windows_last_page": {position: []int64{11, 10, 2, 0, 900, upper, 0, 2, 5, 9}, kind: secondRange},
	} {
		set.After = call.MintWide(runWalkPrefix, c.position...)
		if w, err := runWalkAt(call, set); err != nil || !slices.Equal(w.ids, []int64{5, 14}) || w.seen != upper || w.kind != c.kind || int64(w.omitted) != c.position[6] {
			t.Errorf("runWalkAt(%q) = %+v, %v, want the %s walk holding runs 5 and 14 at its second and %d omitted", set.After, w, err, name, c.position[6])
		}
	}
}

// A range serves 10 pages of 100 through the window, so a returned continuation edited
// to an eleventh names no position a walk reaches and is refused before its request.
func TestABoundedRunContinuationEditedPastTheResultWindowIsRefused(t *testing.T) {
	h := listingHarness(t, modelRuns(walkBound.Add(time.Hour), 1500, 1))
	opts := []forgeapi.ListOption{forgeapi.WithSince(walkBound), forgeapi.WithPageBound(100)}
	first, err := h.client.ListRuns(t.Context(), testRef(), opts...)
	if err != nil || first.Next == "" {
		t.Fatalf("ListRuns(the first page of 1,500 runs) = Next %q, error %v, want a continuation", first.Next, err)
	}
	edited := forgeapi.Cursor(strings.Replace(string(first.Next), "w2.2.", "w11.11.", 1))
	if edited == first.Next {
		t.Fatalf("Setup: continuation %q names no second page to edit", first.Next)
	}
	_, err = h.client.ListRuns(t.Context(), testRef(), append(opts, forgeapi.WithAfter(edited))...)
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
		t.Errorf("ListRuns(WithAfter(%q)) = %v, want code %q", edited, err, forgeapi.CodeCursorInvalid)
	}
	if got := h.instance.count(); got != 1 {
		t.Errorf("the walk sent %d request(s), want 1: the edited continuation reaches none", got)
	}
}
