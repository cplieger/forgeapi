package conformance

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// The end cursors the search answers below name. A page's request carries the
// cursor of the page before it inside the library's own continuation, and the
// server reads which page is asked for by finding it in the request.
const (
	firstWindowCursor  = "Y3Vyc29yOjE="
	secondWindowCursor = "Y3Vyc29yOjI="
)

func TestTheResultWindowReasonCrossesTheWireAsResultWindow(t *testing.T) {
	if forgeapi.PartialResultWindow != forgeapi.PartialLabelsNotApplied+1 {
		t.Errorf("PartialResultWindow = %d, want %d: the member is appended after labels_not_applied, so every earlier member keeps its value",
			int(forgeapi.PartialResultWindow), int(forgeapi.PartialLabelsNotApplied+1))
	}
	if got := forgeapi.PartialResultWindow.String(); got != "result_window" {
		t.Errorf("PartialReason(%d).String() = %q, want %q: the member after labels_not_applied is the result window", int(forgeapi.PartialResultWindow), got, "result_window")
	}
}

// searchPage is one page of a search document's answer, cut from the product's own
// fixture: its first row repeated, the page's continuation and the total the
// search states. withErrors adds a document error beside the data, which is a
// partial success.
type searchPage struct {
	endCursor  string
	rows       int
	total      int
	hasNext    bool
	withErrors bool
}

// body renders the page over the fixture of one cross-repository list.
func (sp searchPage) body(t *testing.T, method string) []byte {
	t.Helper()
	f, err := loadFixture(spec.GitHub, method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var answer map[string]any
	if err := json.Unmarshal([]byte(f.Routes[0].Body), &answer); err != nil {
		t.Fatalf("Setup: %s's body is not a JSON object: %v", f.path, err)
	}
	data, _ := answer["data"].(map[string]any)
	search, _ := data["search"].(map[string]any)
	nodes, _ := search["nodes"].([]any)
	if len(nodes) == 0 {
		t.Fatalf("Setup: %s carries no search row to repeat", f.path)
	}
	rows := make([]any, sp.rows)
	for i := range rows {
		rows[i] = nodes[0]
	}
	var cursor any
	if sp.endCursor != "" {
		cursor = sp.endCursor
	}
	search["nodes"] = rows
	search["issueCount"] = sp.total
	search["pageInfo"] = map[string]any{"hasNextPage": sp.hasNext, "endCursor": cursor}
	if sp.withErrors {
		answer["errors"] = []any{map[string]any{
			"type":    "FORBIDDEN",
			"path":    []any{"search", "nodes", 0, "author"},
			"message": "Resource not accessible by integration",
		}}
	}
	out, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("Setup: re-encoding the search answer: %v", err)
	}
	return out
}

// newSearchServer answers a search document with first, and with the page after
// the one whose end cursor the request carries: second after the first page,
// third after the second.
func newSearchServer(t *testing.T, method string, pages ...searchPage) (*httptest.Server, *recorder) {
	t.Helper()
	bodies := make([][]byte, len(pages))
	for i, p := range pages {
		bodies[i] = p.body(t, method)
	}
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxFixtureBody))
		if err != nil {
			raw = nil
		}
		rec.record(sent{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.Query(), body: raw})
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			rec.miss(r.Method, r.URL.EscapedPath())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		page := 0
		switch {
		case bytes.Contains(raw, []byte(secondWindowCursor)):
			page = 2
		case bytes.Contains(raw, []byte(firstWindowCursor)):
			page = 1
		}
		if page >= len(bodies) {
			rec.miss(r.Method, r.URL.EscapedPath()+" past the last page")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if _, err := w.Write(bodies[page]); err != nil {
			t.Errorf("Setup: writing search page %d: %v", page, err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// searchScopes are the two scopes of both cross-repository lists: the credential's
// own authorship and every open item under one owner.
var searchScopes = map[string][]forgeapi.ListOption{
	"viewer": nil,
	"owner":  {forgeapi.WithOwner(owner)},
}

// partialOf is the response-level marker of one cross-repository list's page.
func partialOf(t *testing.T, got any) (*forgeapi.Partial, forgeapi.Cursor) {
	t.Helper()
	switch page := got.(type) {
	case forgeapi.Page[forgeapi.PullRequest]:
		return page.Partial, page.Next
	case forgeapi.Page[forgeapi.Issue]:
		return page.Partial, page.Next
	}
	t.Fatalf("the list answered %T, want a page of pull requests or issues", got)
	return nil, ""
}

// walkSearch drives one cross-repository list over a search server for as many
// pages as the walk's continuation reaches, answering every page in order.
func walkSearch(t *testing.T, e spec.Entry, srv *httptest.Server, rec *recorder, scope []forgeapi.ListOption, pages int) []*forgeapi.Partial {
	t.Helper()
	var partials []*forgeapi.Partial
	var next forgeapi.Cursor
	for i := range pages {
		opts := scope
		if i > 0 {
			opts = append(append([]forgeapi.ListOption{}, scope...), forgeapi.WithAfter(next))
		}
		got, _, err := listOn(t, e, srv, rec, opts...)
		if err != nil {
			t.Fatalf("%s on %s page %d = error %v, want the page", e.Method, e.Product, i+1, err)
		}
		partial, cursor := partialOf(t, got)
		partials = append(partials, partial)
		if i < pages-1 && cursor == "" {
			t.Fatalf("%s on %s page %d answered no continuation, want one: the search answered hasNextPage true", e.Method, e.Product, i+1)
		}
		if i == pages-1 && cursor != "" {
			t.Errorf("%s on %s page %d = Next %q, want none: the search answered hasNextPage false, so no request reaches the remainder", e.Method, e.Product, i+1, cursor)
		}
		next = cursor
	}
	if misses := rec.misses(); len(misses) > 0 {
		t.Errorf("%s on %s sent request(s) the search server does not answer: %v", e.Method, e.Product, misses)
	}
	return partials
}

// checkWindowMarker holds the page where a walk ends to the result-window marker
// and the totals it carries: the rows the whole walk was served, and the stated
// total less those.
func checkWindowMarker(t *testing.T, e spec.Entry, got *forgeapi.Partial, fetched, omitted int) {
	t.Helper()
	if got == nil {
		t.Errorf("%s on %s ended its walk with no partial marker, want the result window, reason %d: the search states %d result(s) and served %d",
			e.Method, e.Product, int(forgeapi.PartialResultWindow), fetched+omitted, fetched)
		return
	}
	if got.Reason != forgeapi.PartialResultWindow || got.Fetched != fetched || got.OmittedAtLeast != omitted {
		t.Errorf("%s on %s ended its walk with partial {reason %d, fetched %d, omitted at least %d}, want {reason %d, fetched %d, omitted at least %d}",
			e.Method, e.Product, int(got.Reason), got.Fetched, got.OmittedAtLeast, int(forgeapi.PartialResultWindow), fetched, omitted)
	}
}

// A walk the search stops serving while its own total states more ends on a page
// carrying the marker, and the rows it counts as served are the whole walk's, which
// the library counts through its own continuation.
func TestASearchWindowThatEndsShortOfItsTotalIsMarkedPartial(t *testing.T) {
	for _, method := range crossRepositoryLists {
		for scope, opts := range searchScopes {
			t.Run(method+"_"+scope, func(t *testing.T) {
				e := requireEntry(t, spec.GitHub, method)
				srv, rec := newSearchServer(t, method,
					searchPage{rows: 1, total: 5, hasNext: true, endCursor: firstWindowCursor},
					searchPage{rows: 1, total: 5, endCursor: secondWindowCursor},
				)
				partials := walkSearch(t, e, srv, rec, opts, 2)
				if partials[0] != nil {
					t.Errorf("%s on %s page 1 = partial %+v, want none: the marker rides the page where the walk ends", method, e.Product, *partials[0])
				}
				checkWindowMarker(t, e, partials[1], 2, 3)
			})
		}
	}
}

func TestASearchWindowShortOfItsTotalOnItsOnlyPageIsMarkedPartial(t *testing.T) {
	for _, method := range crossRepositoryLists {
		t.Run(method, func(t *testing.T) {
			e := requireEntry(t, spec.GitHub, method)
			srv, rec := newSearchServer(t, method, searchPage{rows: 1, total: 4})
			partials := walkSearch(t, e, srv, rec, nil, 1)
			checkWindowMarker(t, e, partials[0], 1, 3)
		})
	}
}

// A walk whose total is within what it was served is a complete list, so it carries
// no marker, on one page or over several.
func TestASearchWindowThatServesItsWholeTotalCarriesNoMarker(t *testing.T) {
	walks := map[string][]searchPage{
		"one_page": {{rows: 1, total: 1}},
		"two_pages": {
			{rows: 1, total: 2, hasNext: true, endCursor: firstWindowCursor},
			{rows: 1, total: 2, endCursor: secondWindowCursor},
		},
	}
	for _, method := range crossRepositoryLists {
		for name, pages := range walks {
			t.Run(method+"_"+name, func(t *testing.T) {
				e := requireEntry(t, spec.GitHub, method)
				srv, rec := newSearchServer(t, method, pages...)
				partials := walkSearch(t, e, srv, rec, nil, len(pages))
				if last := partials[len(partials)-1]; last != nil {
					t.Errorf("%s on %s ended a walk served its whole total with partial %+v, want none", method, e.Product, *last)
				}
			})
		}
	}
}

// A document's partial errors say the page's own rows are in question, which
// outranks a statement about rows beyond the page.
func TestAShortWindowWithDocumentErrorsKeepsTheGraphQLPartialReason(t *testing.T) {
	for _, method := range crossRepositoryLists {
		t.Run(method, func(t *testing.T) {
			e := requireEntry(t, spec.GitHub, method)
			srv, rec := newSearchServer(t, method, searchPage{rows: 1, total: 4, withErrors: true})
			partials := walkSearch(t, e, srv, rec, nil, 1)
			if got := partials[0]; got == nil || got.Reason != forgeapi.PartialGraphQLPartial {
				t.Errorf("%s on %s answered partial %v over data with document errors and a short window, want reason %v",
					method, e.Product, got, forgeapi.PartialGraphQLPartial)
			}
		})
	}
}
