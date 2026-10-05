package conformance

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// giteaFamilyProducts are the two products whose run listing is one implementation.
var giteaFamilyProducts = []spec.Product{spec.Gitea, spec.Forgejo}

// runInstance is a run listing the way the Gitea family's instances serve it: pages
// numbered from 1 at an effective size of the smaller of the limit asked for and the
// instance's own maximum, no paging header, and the whole listing's size in the
// body. A request with no page answers every run, which is Forgejo's own defect on
// that route. Its settings route states statedMax, which a conforming instance sets
// to the maximum it serves; a case setting it above that stands for an instance
// whose administrator lowered the maximum after the connection read it.
type runInstance struct {
	row       map[string]any
	stated    *int
	setup     []wire
	max       int
	statedMax int
	total     int
	noTotal   bool
}

// newRunInstance serves one product's run row, from its fixture, total times.
func newRunInstance(t *testing.T, p spec.Product, maxPage, total int) *runInstance {
	t.Helper()
	f, err := loadFixture(p, "Checks.ListRuns")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var listing struct {
		Runs []map[string]any `json:"workflow_runs"`
	}
	if err := json.Unmarshal([]byte(f.Routes[0].Body), &listing); err != nil || len(listing.Runs) == 0 {
		t.Fatalf("Setup: %s carries no run row to repeat: %v", f.path, err)
	}
	return &runInstance{
		row: listing.Runs[0], setup: setupRoutes(t, requireEntry(t, p, "Checks.ListRuns")),
		max: maxPage, statedMax: maxPage, total: total,
	}
}

// page renders the rows one request asks for. Each row is the fixture's own with
// the run's number in its id and its page address, so a walk that repeats a row or
// skips one is visible in the page addresses it answers.
func (ri *runInstance) page(t *testing.T, query map[string][]string) []byte {
	limit, page := ri.max, 0
	if v, err := strconv.Atoi(first(query["limit"])); err == nil && v > 0 {
		limit = min(v, ri.max)
	}
	if v, err := strconv.Atoi(first(query["page"])); err == nil {
		page = v
	}
	from, to := 0, ri.total
	if page >= 1 {
		from, to = min((page-1)*limit, ri.total), min(page*limit, ri.total)
	}
	rows := make([]map[string]any, 0, to-from)
	for n := from + 1; n <= to; n++ {
		row := maps.Clone(ri.row)
		row["id"] = n
		row["html_url"] = fmt.Sprintf("https://forge.example/%s/actions/runs/%d", selector, n)
		rows = append(rows, row)
	}
	answer := map[string]any{"workflow_runs": rows}
	switch {
	case ri.stated != nil:
		answer["total_count"] = *ri.stated
	case !ri.noTotal:
		answer["total_count"] = ri.total
	}
	out, err := json.Marshal(answer)
	if err != nil {
		t.Errorf("Setup: encoding a run page: %v", err)
	}
	return out
}

// serve stands the instance up for one case. The recorder holds the run route's
// requests alone: the settings read is the connection's discovery, not the
// listing's, and so is the rest of the connection read a case runs first, answered
// from that read's own fixture, so a walk's requests are its pages.
func (ri *runInstance) serve(t *testing.T) (*httptest.Server, *recorder) {
	t.Helper()
	route := "/api/v1/repos/" + selector + "/actions/runs"
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == settingsRoute {
			if _, err := io.WriteString(w, settingsAnswer(ri.statedMax)); err != nil {
				t.Errorf("Setup: writing the settings answer: %v", err)
			}
			return
		}
		for i := range ri.setup {
			if ri.setup[i].matches(r, nil) {
				if _, err := w.Write(ri.setup[i].Body); err != nil {
					t.Errorf("Setup: writing the connection read's answer to %s %s: %v", r.Method, r.URL.Path, err)
				}
				return
			}
		}
		rec.record(sent{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.Query()})
		if r.Method != http.MethodGet || r.URL.Path != route {
			rec.miss(r.Method, r.URL.EscapedPath())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if _, err := w.Write(ri.page(t, r.URL.Query())); err != nil {
			t.Errorf("Setup: writing a run page: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// runWalk is what one walk over the run listing answered: each page's rows and
// continuation, and the query each page's request carried.
type runWalk struct {
	queries [][2]string
	sizes   []int
	webURLs []string
	nexts   []forgeapi.Cursor
}

// walkRuns follows the listing's own continuation until it ends, passing opts on
// every page, and fails the case when the walk does not end within bound pages.
func walkRuns(t *testing.T, e spec.Entry, srv *httptest.Server, rec *recorder, bound int, opts ...forgeapi.ListOption) runWalk {
	t.Helper()
	var walk runWalk
	var next forgeapi.Cursor
	for range bound {
		pageOpts := opts
		if next != "" {
			pageOpts = append(slices.Clone(opts), forgeapi.WithAfter(next))
		}
		got, requests, err := listOn(t, e, srv, rec, pageOpts...)
		if err != nil {
			t.Fatalf("ListRuns on %s page %d = error %v, want the page", e.Product, len(walk.sizes)+1, err)
		}
		page, ok := got.(forgeapi.Page[forgeapi.Run])
		if !ok {
			t.Fatalf("ListRuns on %s answered %T, want a page of runs", e.Product, got)
		}
		for _, req := range requests {
			walk.queries = append(walk.queries, [2]string{req.query.Get("page"), req.query.Get("limit")})
		}
		walk.sizes = append(walk.sizes, len(page.Items))
		for _, run := range page.Items {
			walk.webURLs = append(walk.webURLs, run.WebURL)
		}
		walk.nexts = append(walk.nexts, page.Next)
		if page.Next == "" {
			if misses := rec.misses(); len(misses) > 0 {
				t.Errorf("ListRuns on %s sent request(s) the instance does not answer: %v", e.Product, misses)
			}
			return walk
		}
		next = page.Next
	}
	t.Fatalf("ListRuns on %s was still continuing after %d page(s), want the walk to end where the stated total is served: page sizes %v", e.Product, bound, walk.sizes)
	return walk
}

// checkWalk holds one walk to the pages it should have read and the rows it should
// have answered: every run once, in the instance's order.
func checkWalk(t *testing.T, e spec.Entry, got runWalk, sizes []int, queries [][2]string, total int) {
	t.Helper()
	if !slices.Equal(got.sizes, sizes) {
		t.Errorf("ListRuns on %s answered pages of %v, want %v", e.Product, got.sizes, sizes)
	}
	if !slices.Equal(got.queries, queries) {
		t.Errorf("ListRuns on %s sent (page, limit) %v, want %v: page from 1 and one limit on every page of a walk", e.Product, got.queries, queries)
	}
	want := make([]string, 0, total)
	for n := 1; n <= total; n++ {
		want = append(want, fmt.Sprintf("https://forge.example/%s/actions/runs/%d", selector, n))
	}
	if !slices.Equal(got.webURLs, want) {
		t.Errorf("ListRuns on %s answered %d run(s) [%s], want each of the %d run(s) once, in order",
			e.Product, len(got.webURLs), strings.Join(got.webURLs, " "), total)
	}
}

// runsEntry is one product's run-listing entry, skipping the case where the table
// claims no support for it yet.
func runsEntry(t *testing.T, p spec.Product) spec.Entry {
	t.Helper()
	e := requireEntry(t, p, "Checks.ListRuns")
	if pending(e) {
		t.Skip(pendingReason(e))
	}
	return e
}

// An instance serves a page of its own maximum whatever limit is asked for, so a
// page short of the limit while the stated total says more is a clamp, and the walk
// continues past it to the last run. The instance here states a maximum above the
// one it serves, which is what a connection opened before its administrator lowered
// the maximum meets.
func TestTheRunListingContinuesPastAClampedPageWhileItsTotalStatesMore(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			e := runsEntry(t, p)
			instance := newRunInstance(t, p, 50, 120)
			instance.statedMax = 100
			srv, rec := instance.serve(t)
			got := walkRuns(t, e, srv, rec, 5)
			checkWalk(t, e, got, []int{50, 50, 20}, [][2]string{{"1", "100"}, {"2", "100"}, {"3", "100"}}, 120)
		})
	}
}

// The run listing asks for the smaller of the page bound and the instance's stated
// maximum, as every page-numbered list of the family does, so the instance never
// clamps its pages.
func TestTheRunListingAsksForTheInstancesStatedMaximum(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			e := runsEntry(t, p)
			srv, rec := newRunInstance(t, p, 50, 120).serve(t)
			got := walkRuns(t, e, srv, rec, 5)
			checkWalk(t, e, got, []int{50, 50, 20}, [][2]string{{"1", "50"}, {"2", "50"}, {"3", "50"}}, 120)
		})
	}
}

// The stated total ends a walk exactly where it is served: a full page that serves
// the total is not continued past it, and a short page that serves it is the end.
func TestTheRunListingEndsWhereItsStatedTotalIsServed(t *testing.T) {
	walks := []struct {
		name  string
		limit string
		max   int
		total int
	}{
		{name: "full_page", max: 100, total: 100, limit: "100"},
		{name: "short_page", max: 50, total: 30, limit: "50"},
	}
	for _, p := range giteaFamilyProducts {
		for _, w := range walks {
			t.Run(string(p)+"_"+w.name, func(t *testing.T) {
				e := runsEntry(t, p)
				srv, rec := newRunInstance(t, p, w.max, w.total).serve(t)
				got := walkRuns(t, e, srv, rec, 3)
				checkWalk(t, e, got, []int{w.total}, [][2]string{{"1", w.limit}}, w.total)
			})
		}
	}
}

// Every page of one walk asks for the same limit, which is what lets the stated
// total decide the continuation.
func TestTheRunListingSendsTheCallersPageBoundOnEveryPageOfAWalk(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			e := runsEntry(t, p)
			srv, rec := newRunInstance(t, p, 50, 5).serve(t)
			got := walkRuns(t, e, srv, rec, 5, forgeapi.WithPageBound(2))
			checkWalk(t, e, got, []int{2, 2, 1}, [][2]string{{"1", "2"}, {"2", "2"}, {"3", "2"}}, 5)
		})
	}
}

// An answer whose total cannot be read says neither that the listing ended nor that
// it goes on, so it is the malformed answer it is.
func TestTheRunListingRefusesAPageWithoutItsStatedTotal(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			e := runsEntry(t, p)
			instance := newRunInstance(t, p, 50, 3)
			instance.noTotal = true
			srv, rec := instance.serve(t)
			got, _, err := listOn(t, e, srv, rec)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) {
				t.Errorf("ListRuns on %s over a page with no total_count = (%v, error %v), want a *forgeapi.Error: the continuation is decided by that total",
					p, got, err)
			}
		})
	}
}

// A total below zero counts nothing, so it is as malformed as an absent one, and a
// page carrying it is refused rather than read as a listing served whole.
func TestTheRunListingRefusesAPageWhoseStatedTotalIsNegative(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			e := runsEntry(t, p)
			instance := newRunInstance(t, p, 50, 1)
			instance.stated = new(-1)
			srv, rec := instance.serve(t)
			got, _, err := listOn(t, e, srv, rec)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeValidation || fe.Kind != forgeapi.KindUpstream {
				t.Errorf("ListRuns on %s over a page whose total_count is -1 = (%v, error %v), want code %q of kind %v", p, got, err, forgeapi.CodeValidation, forgeapi.KindUpstream)
			}
		})
	}
}

// Any positive page bound is one a walk may begin at, so the continuation its first
// page mints resumes the walk at that bound, however large, when the resumed call
// asks for the same one. The instance states a maximum as large as the bound, so the
// bound is the limit sent.
func TestARunWalkResumesAtWhateverPositiveBoundItBeganWith(t *testing.T) {
	const bound = 1<<31 + 1
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			e := runsEntry(t, p)
			instance := newRunInstance(t, p, 50, 120)
			instance.statedMax = bound
			srv, rec := instance.serve(t)
			first, _, err := listOn(t, e, srv, rec, forgeapi.WithPageBound(bound))
			page, ok := first.(forgeapi.Page[forgeapi.Run])
			if err != nil || !ok || page.Next == "" {
				t.Fatalf("Setup: ListRuns(WithPageBound(%d)) on %s over 50 of 120 runs = (%v, error %v), want a page with a continuation", bound, p, first, err)
			}
			got, requests, err := listOn(t, e, srv, rec, forgeapi.WithPageBound(bound), forgeapi.WithAfter(page.Next))
			if _, ok := got.(forgeapi.Page[forgeapi.Run]); err != nil || !ok || len(requests) != 1 {
				t.Fatalf("ListRuns(WithPageBound(%d), WithAfter(%q)) on %s = (%v, %d request(s), error %v), want the next page from the continuation its own first page minted", bound, page.Next, p, got, len(requests), err)
			}
			if sent := requests[0].query.Get("limit"); sent != strconv.Itoa(bound) {
				t.Errorf("ListRuns(WithAfter(%q)) on %s sent limit %s, want %d, the bound the walk began with", page.Next, p, sent, bound)
			}
		})
	}
}

// A page that serves no row ends the walk whatever the total states, since no later
// page can serve what it did not, and where the total states more than the walk was
// served the page says so with the result window and the walk's own count.
func TestARunWalkEndsAtAnEmptyPageAndMarksWhatItsTotalStatesBeyondIt(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			e := runsEntry(t, p)
			instance := newRunInstance(t, p, 50, 60)
			instance.stated = new(80)
			srv, rec := instance.serve(t)
			var next forgeapi.Cursor
			var last forgeapi.Page[forgeapi.Run]
			sizes := []int{}
			for range 5 {
				var opts []forgeapi.ListOption
				if next != "" {
					opts = append(opts, forgeapi.WithAfter(next))
				}
				got, _, err := listOn(t, e, srv, rec, opts...)
				page, ok := got.(forgeapi.Page[forgeapi.Run])
				if err != nil || !ok {
					t.Fatalf("ListRuns on %s page %d = (%v, error %v), want the page", p, len(sizes)+1, got, err)
				}
				sizes = append(sizes, len(page.Items))
				last, next = page, page.Next
				if next == "" {
					break
				}
			}
			if next != "" || !slices.Equal(sizes, []int{50, 10, 0}) {
				t.Fatalf("ListRuns on %s over 60 runs stating 80 answered pages of %v, Next %q, want [50 10 0] ending at the empty page", p, sizes, next)
			}
			if pt := last.Partial; pt == nil || pt.Reason != forgeapi.PartialResultWindow || pt.Fetched != 60 || pt.OmittedAtLeast != 20 {
				t.Errorf("ListRuns on %s at the empty page = Partial %+v, want result_window with 60 fetched and at least 20 omitted", p, pt)
			}
		})
	}
}

// TestLiveTheRunListingContinuesWhileItsTotalStatesMore holds a real instance's
// first page of runs to its own stated total, read independently by a one-row
// request on the same route: where the total says more runs exist than the page
// served, the page carries a continuation, which is what an instance that clamps
// the page size makes the library prove.
func TestLiveTheRunListingContinuesWhileItsTotalStatesMore(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			e := runsEntry(t, p)
			base := os.Getenv(liveURLVar(p))
			if base == "" {
				t.Skipf("%s is unset, so no live instance is named for %s", liveURLVar(p), p)
			}
			token := os.Getenv(liveTokenVar(p))
			if token == "" && !anonymousReads[p][e.Method] {
				t.Skipf("%s is unset and %s answers its run listing to no anonymous caller", liveTokenVar(p), p)
			}
			s := liveSubject(p)
			if s.repo.Selector == "" {
				t.Skipf("no live subject is named for %s", p)
			}
			total := liveRunTotal(t, base, s.repo.Selector, token)
			wire := &counting{next: &http.Transport{}}
			client, err := guard(func() (any, error) {
				return newClient(p, forgeapi.Connection{WebBaseURL: base}, liveOptions(base, wire, token)...)
			})
			if err != nil {
				t.Fatalf("Setup: %s client for %s: %v", p, base, err)
			}
			got, err := guard(func() (any, error) { return lists[e.Method](t.Context(), client, s) })
			if err != nil {
				t.Fatalf("ListRuns(%q) on %s = error %v, want the first page", s.repo.Selector, base, err)
			}
			page := got.(forgeapi.Page[forgeapi.Run])
			if more := total > len(page.Items); more != (page.Next != "") {
				t.Errorf("ListRuns(%q) on %s served %d run(s) of a stated %d with Next %q, want a continuation exactly when the total states more",
					s.repo.Selector, base, len(page.Items), total, page.Next)
			}
			t.Logf("ListRuns(%q) on %s served %d of %d run(s) in %d request(s), continuation %v", s.repo.Selector, base, len(page.Items), total, wire.n.Load(), page.Next != "")
		})
	}
}

// liveRunTotal is the stated total of one repository's run listing, read with a
// one-row page so the read costs what the route charges for its smallest answer.
func liveRunTotal(t *testing.T, base, repo, token string) int {
	t.Helper()
	address := strings.TrimSuffix(base, "/") + "/api/v1/repos/" + repo + "/actions/runs?page=1&limit=1"
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
	if err != nil {
		t.Fatalf("Setup: request for %s: %v", address, err)
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := (&http.Transport{}).RoundTrip(req)
	if err != nil {
		t.Fatalf("Setup: GET %s = error %v", address, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Errorf("closing GET %s: %v", address, closeErr)
		}
	}()
	var listing struct {
		Total *int `json:"total_count"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxFixtureBody)).Decode(&listing); err != nil || resp.StatusCode != http.StatusOK || listing.Total == nil {
		t.Fatalf("Setup: GET %s = status %d, decode error %v, want a 200 stating total_count", address, resp.StatusCode, err)
	}
	return *listing.Total
}
