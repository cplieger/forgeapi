package conformance

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// settingsRoute is the Gitea family's statement of the most rows one page of a list
// serves, its max_response_items.
const settingsRoute = "/api/v1/settings/api"

// settingsAnswer is that route's answer stating rows a page, in the shape
// codeberg.org, gitea.com and the local Gitea and Forgejo instances answered it.
func settingsAnswer(rows int) string {
	return fmt.Sprintf(`{"default_git_trees_per_page":1000,"default_max_blob_size":10485760,"default_paging_num":30,"max_response_items":%d}`, rows)
}

// otherRepository is a second repository under the canonical subject's owner.
const otherRepository = "other"

// otherRepo is the reference to that repository on one product.
func otherRepo(p spec.Product) forgeapi.RepoRef {
	ref := forgeapi.RepoRef{Family: family(p), Selector: owner + "/" + otherRepository, DisplayPath: owner + "/" + otherRepository}
	ref.ID = ref.Encode()
	return ref
}

// pagedLabel is the name a paged instance gives its n-th label.
func pagedLabel(n int) string { return "label-" + strconv.Itoa(n) }

// pagedList is one list route a paged instance answers: the row its fixture records,
// repeated, and whether the route carries its rows inside an object beside their
// total, as the run listings of GitHub and the Gitea family do.
type pagedList struct {
	row  map[string]any
	runs bool
}

// pagedInstance serves the page-numbered lists of one product the way a forge
// addressed by page number serves them: pages numbered from 1, each holding the
// smaller of the limit asked for and the most rows the instance serves, every
// paging header the products send, and on a run listing the whole listing's size
// in the body. It answers the Gitea family's settings route with the maximum it
// states, which a conforming instance sets to the most rows it serves.
type pagedInstance struct {
	lists          map[string]pagedList
	rec            *recorder
	extras         []wire
	settingsBody   string
	settingsStatus int
	serves         int
	total          int
}

// newPagedInstance stands up every page-numbered list the table supports on one
// product, each holding total rows, serving at most serves rows a page.
func newPagedInstance(t *testing.T, p spec.Product, total, serves int) *pagedInstance {
	t.Helper()
	pi := &pagedInstance{
		lists:          map[string]pagedList{},
		rec:            &recorder{},
		settingsBody:   settingsAnswer(serves),
		settingsStatus: http.StatusOK,
		serves:         serves,
		total:          total,
	}
	for _, e := range pageNumberedLists(t, p) {
		f, err := loadFixture(p, e.Method)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		list, ok := pagedRow(f)
		if !ok {
			t.Fatalf("Setup: %s carries no row to repeat", f.path)
		}
		head := f.Routes[0]
		for _, path := range append([]string{head.Path}, head.Paths...) {
			if path == "" {
				continue
			}
			pi.lists[path] = list
			pi.lists[elsewhere(path)] = list
		}
		pi.extras = append(pi.extras, f.Routes[1:]...)
	}
	return pi
}

// restate has the instance's administrator set the most rows a page serves, which its
// settings route then states.
func (pi *pagedInstance) restate(serves int) {
	pi.serves = serves
	pi.settingsBody = settingsAnswer(serves)
}

// elsewhere is one route's path addressed to the other repository.
func elsewhere(path string) string {
	path = strings.ReplaceAll(path, "/"+owner+"/"+repository+"/", "/"+owner+"/"+otherRepository+"/")
	return strings.ReplaceAll(path, owner+"%2F"+repository+"/", owner+"%2F"+otherRepository+"/")
}

// pagedRow is the row one list fixture's route records, and whether that route
// carries its rows inside a run listing's object.
func pagedRow(f fixture) (pagedList, bool) {
	if len(f.Routes) == 0 {
		return pagedList{}, false
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(f.Routes[0].Body), &rows); err == nil && len(rows) > 0 {
		return pagedList{row: rows[0]}, true
	}
	var listing struct {
		Runs []map[string]any `json:"workflow_runs"`
	}
	if err := json.Unmarshal([]byte(f.Routes[0].Body), &listing); err == nil && len(listing.Runs) > 0 {
		return pagedList{row: listing.Runs[0], runs: true}, true
	}
	return pagedList{}, false
}

// pageNumberedLists are the lists of one product whose continuation is a page
// number: every supported list the table routes over REST rather than over a
// document.
func pageNumberedLists(t *testing.T, p spec.Product) []spec.Entry {
	t.Helper()
	var out []spec.Entry
	for _, method := range slices.Sorted(maps.Keys(lists)) {
		e, ok := entryFor(p, method)
		if !ok || pending(e) || refuses(e) {
			continue
		}
		f, err := loadFixture(p, method)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		if len(f.Routes) == 0 || f.Routes[0].GraphQL {
			continue
		}
		out = append(out, e)
	}
	return out
}

// serve stands the instance up for one case.
func (pi *pagedInstance) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(pi.handler(t))
	t.Cleanup(srv.Close)
	return srv
}

// handler is the instance's answer to every request, for a case that serves it
// under a root of its own.
func (pi *pagedInstance) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxFixtureBody))
		if err != nil {
			body = nil
		}
		path := r.URL.EscapedPath()
		pi.rec.record(sent{method: r.Method, path: path, query: r.URL.Query(), body: body})
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && path == settingsRoute {
			w.WriteHeader(pi.settingsStatus)
			if _, err := io.WriteString(w, pi.settingsBody); err != nil {
				t.Errorf("Setup: writing the settings answer: %v", err)
			}
			return
		}
		if list, ok := pi.lists[path]; ok && r.Method == http.MethodGet {
			pi.page(t, w, r, list)
			return
		}
		for _, x := range pi.extras {
			if !x.GraphQL && x.Method == r.Method && x.Path == path {
				for k, v := range x.Headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(x.Status)
				if _, err := w.Write(x.Body); err != nil {
					t.Errorf("Setup: writing %s %s: %v", r.Method, path, err)
				}
				return
			}
		}
		pi.rec.miss(r.Method, path)
		w.WriteHeader(http.StatusInternalServerError)
	})
}

// page writes the rows one request asks for, with the paging headers the products
// send: the whole listing's size, the page, its size and, where more rows remain,
// the next page's number and link.
func (pi *pagedInstance) page(t *testing.T, w http.ResponseWriter, r *http.Request, list pagedList) {
	q := r.URL.Query()
	limit := pi.serves
	for _, name := range []string{"limit", "per_page"} {
		if v, err := strconv.Atoi(q.Get(name)); err == nil && v > 0 {
			limit = min(v, pi.serves)
		}
	}
	page := 1
	if v, err := strconv.Atoi(q.Get("page")); err == nil && v > 0 {
		page = v
	}
	from, to := min((page-1)*limit, pi.total), min(page*limit, pi.total)
	rows := make([]map[string]any, 0, to-from)
	for n := from + 1; n <= to; n++ {
		row := maps.Clone(list.row)
		row["id"] = n
		if _, numbered := row["number"]; numbered {
			row["number"] = n
		}
		if strings.HasSuffix(r.URL.Path, "/labels") {
			row["name"] = pagedLabel(n)
		}
		rows = append(rows, row)
	}
	pages := (pi.total + limit - 1) / limit
	h := w.Header()
	h.Set("X-Total-Count", strconv.Itoa(pi.total))
	h.Set("X-Total", strconv.Itoa(pi.total))
	h.Set("X-Total-Pages", strconv.Itoa(pages))
	h.Set("X-Page", strconv.Itoa(page))
	h.Set("X-Per-Page", strconv.Itoa(limit))
	if to < pi.total {
		// The link names the address the request arrived at, root included, which
		// a handler serving the instance under a root of its own has stripped from
		// the request's path.
		next := *r.URL
		if arrived, err := url.ParseRequestURI(r.RequestURI); err == nil {
			next = *arrived
		}
		nq := next.Query()
		nq.Set("page", strconv.Itoa(page+1))
		next.RawQuery = nq.Encode()
		h.Set("X-Next-Page", strconv.Itoa(page+1))
		h.Set("Link", fmt.Sprintf(`<http://%s%s>; rel="next"`, r.Host, next.RequestURI()))
	}
	var answer any = rows
	if list.runs {
		answer = map[string]any{"total_count": pi.total, "workflow_runs": rows}
	}
	out, err := json.Marshal(answer)
	if err != nil {
		t.Errorf("Setup: encoding a page: %v", err)
	}
	if _, err := w.Write(out); err != nil {
		t.Errorf("Setup: writing a page: %v", err)
	}
}

// isList reports whether one request reached a list route of the instance, rather
// than its settings route or a row's own read.
func (pi *pagedInstance) isList(req sent) bool {
	_, ok := pi.lists[req.path]
	return ok
}

// listRequests are the requests of one window that reached a list route.
func (pi *pagedInstance) listRequests(window []sent) []sent {
	var out []sent
	for _, req := range window {
		if pi.isList(req) {
			out = append(out, req)
		}
	}
	return out
}

// settingsReads counts the requests of one window that read the settings route.
func settingsReads(window []sent) int {
	n := 0
	for _, req := range window {
		if req.path == settingsRoute {
			n++
		}
	}
	return n
}

// pagedClient is one connection to an instance this suite stood up, built with the
// family's constructor and never set up, as every offline case's client is.
func pagedClient(t *testing.T, p spec.Product, srv *httptest.Server, extra ...forgeapi.Option) any {
	t.Helper()
	conn := forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL + apiRoot(p)}
	client, err := guard(func() (any, error) {
		return newClient(p, conn, append(offlineOptions(srv), extra...)...)
	})
	if err != nil {
		t.Fatalf("Setup: %s client: %v", p, err)
	}
	return client
}

// callList drives one list on a client and answers what it returned and the
// requests the instance received while it ran.
func callList(t *testing.T, e spec.Entry, client any, rec *recorder, s subject, opts ...forgeapi.ListOption) (got any, window []sent, err error) {
	t.Helper()
	list, ok := lists[e.Method]
	if !ok {
		t.Fatalf("Setup: %s is not a list this suite drives with options", e.Method)
	}
	before := rec.count()
	got, err = guard(func() (any, error) { return list(t.Context(), client, s, opts...) })
	return got, rec.since(before), err
}

// pageParts are one list answer's row count and continuation, read from whichever
// item type the page carries.
func pageParts(got any) (rows int, next forgeapi.Cursor, ok bool) {
	v := reflect.ValueOf(got)
	if v.Kind() != reflect.Struct {
		return 0, "", false
	}
	items, cursor := v.FieldByName("Items"), v.FieldByName("Next")
	if !items.IsValid() || items.Kind() != reflect.Slice || !cursor.IsValid() {
		return 0, "", false
	}
	next, ok = reflect.TypeAssert[forgeapi.Cursor](cursor)
	return items.Len(), next, ok
}

// pagedWalk is what one walk over a paged instance answered: each page's row count
// and the page and limit each list request carried.
type pagedWalk struct {
	queries [][2]string
	sizes   []int
}

// walkPaged follows one list's continuation on one client until it ends, passing
// opts on every page, and fails the case when the walk does not end within bound
// pages.
func walkPaged(t *testing.T, e spec.Entry, client any, pi *pagedInstance, bound int, opts ...forgeapi.ListOption) pagedWalk {
	t.Helper()
	var walk pagedWalk
	s := canonicalSubject(e.Product)
	var next forgeapi.Cursor
	for range bound {
		pageOpts := opts
		if next != "" {
			pageOpts = append(slices.Clone(opts), forgeapi.WithAfter(next))
		}
		got, window, err := callList(t, e, client, pi.rec, s, pageOpts...)
		if err != nil {
			t.Fatalf("%s on %s page %d = error %v, want the page", e.Method, e.Product, len(walk.sizes)+1, err)
		}
		rows, cursor, ok := pageParts(got)
		if !ok {
			t.Fatalf("%s on %s answered %T, want a page", e.Method, e.Product, got)
		}
		for _, req := range pi.listRequests(window) {
			walk.queries = append(walk.queries, [2]string{req.query.Get("page"), pageLimit(req)})
		}
		walk.sizes = append(walk.sizes, rows)
		if cursor == "" {
			if misses := pi.rec.misses(); len(misses) > 0 {
				t.Errorf("%s on %s sent request(s) the instance does not answer: %v", e.Method, e.Product, misses)
			}
			return walk
		}
		next = cursor
	}
	t.Fatalf("%s on %s was still continuing after %d page(s), want the walk to end at a short page: page sizes %v", e.Method, e.Product, bound, walk.sizes)
	return walk
}

// pageLimit is the page size one list request asked for, under whichever name its
// family spells it.
func pageLimit(req sent) string {
	if req.query.Has("limit") {
		return req.query.Get("limit")
	}
	return req.query.Get("per_page")
}
