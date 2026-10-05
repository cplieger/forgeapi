package conformance

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// labelReads are GitLab's two reads whose document answers one merge request's
// label connection, each with the path to the merge request in its answer.
var labelReads = map[string][]string{
	"PullRequests.ListPRs": {"project", "mergeRequests", "nodes", "0"},
	"PullRequests.ReadPR":  {"project", "mergeRequest"},
}

// labelPage is one merge request's label connection: the nodes one page serves,
// the count the connection states for the whole set, and whether it names a
// further page.
type labelPage struct {
	nodes   int
	count   int
	hasNext bool
}

// newLabelInstance answers one of GitLab's label reads with its fixture, the merge
// request's label connection replaced by the page given, and the prelude's own
// document and routes from the prelude's fixture.
func newLabelInstance(t *testing.T, method string, page labelPage) (srv *httptest.Server, rec *recorder, document string) {
	t.Helper()
	e := requireEntry(t, spec.GitLab, method)
	document, ok := tableDocument(&e)
	if !ok {
		t.Fatalf("Setup: the table names no document for %s on %s", method, spec.GitLab)
	}
	f, err := loadFixture(spec.GitLab, method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var answer map[string]any
	if err := json.Unmarshal(f.Routes[0].Body, &answer); err != nil {
		t.Fatalf("Setup: %s's body is not a JSON object: %v", f.path, err)
	}
	mr := mergeRequestIn(t, answer, labelReads[method])
	labels := labelConnectionAnswer(page)
	mr["labels"] = labels
	counted, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("Setup: re-encoding the document's answer: %v", err)
	}
	delete(labels, "count")
	uncounted, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("Setup: re-encoding the document's answer: %v", err)
	}
	prelude, routes := preludeAnswers(t, e)
	rec = &recorder{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxFixtureBody))
		if err != nil {
			raw = nil
		}
		path := r.URL.EscapedPath()
		rec.record(sent{method: r.Method, path: path, query: r.URL.Query(), body: raw})
		w.Header().Set("Content-Type", "application/json")
		out, ok := routes[r.Method+" "+path]
		if r.Method == http.MethodPost {
			out, ok = prelude, prelude != nil
			if operationName(raw) == document {
				out, ok = uncounted, true
				if selectsLabelCount(raw) {
					out = counted
				}
			}
		}
		if !ok {
			rec.miss(r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if _, err := w.Write(out); err != nil {
			t.Errorf("Setup: writing %s %s: %v", r.Method, path, err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec, document
}

// mergeRequestIn is the merge request a document answer holds at the path given.
func mergeRequestIn(t *testing.T, answer map[string]any, path []string) map[string]any {
	t.Helper()
	at := answer["data"]
	for _, step := range path {
		switch node := at.(type) {
		case map[string]any:
			at = node[step]
		case []any:
			if len(node) == 0 {
				t.Fatalf("Setup: the fixture's %v holds no merge request", path)
			}
			at = node[0]
		}
	}
	mr, ok := at.(map[string]any)
	if !ok {
		t.Fatalf("Setup: the fixture answers no merge request at %v", path)
	}
	return mr
}

// selectsLabelCount reports whether a document request selects `count` on its label
// connection, since a GraphQL instance answers the fields a document selects and no
// others.
func selectsLabelCount(raw []byte) bool {
	var req struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return false
	}
	_, after, ok := strings.Cut(req.Query, "labels(")
	if !ok {
		return false
	}
	_, block, ok := strings.Cut(after, "{")
	if !ok {
		return false
	}
	var top strings.Builder
	depth := 0
	for _, r := range block {
		switch {
		case r == '{':
			depth++
			top.WriteRune(' ')
		case r == '}' && depth == 0:
			return slices.Contains(strings.Fields(top.String()), "count")
		case r == '}':
			depth--
		case depth == 0:
			top.WriteRune(r)
		}
	}
	return false
}

// labelConnectionAnswer is the label connection GitLab's documents answer for one
// page: its nodes, the stated count and the page info.
func labelConnectionAnswer(page labelPage) map[string]any {
	nodes := make([]any, 0, page.nodes)
	for i := range page.nodes {
		nodes = append(nodes, map[string]any{"title": fmt.Sprintf("label-%03d", i+1), "color": "#ededed", "description": ""})
	}
	return map[string]any{
		"count":    page.count,
		"pageInfo": map[string]any{"hasNextPage": page.hasNext},
		"nodes":    nodes,
	}
}

// readLabels reads the merge request one of GitLab's label reads answers, through
// that read on a connection whose prelude has run, and the requests the read sent.
func readLabels(t *testing.T, method string, page labelPage) (pr forgeapi.PullRequest, pagePartial *forgeapi.Partial, sentDocs int) {
	t.Helper()
	srv, rec, document := newLabelInstance(t, method, page)
	client := pagedClient(t, spec.GitLab, srv)
	e := requireEntry(t, spec.GitLab, method)
	op, _ := operationFor(method)
	s := canonicalSubject(spec.GitLab)
	if prelude := preludeMethod(e, op); prelude != "" {
		runPrelude(t, e, prelude, client, s)
	}
	before := rec.count()
	got, err := guard(func() (any, error) { return op.invoke(t.Context(), client, s) })
	if err != nil {
		t.Fatalf("%s on %s over a label connection of %d node(s), count %d, hasNextPage %t = error %v, want the merge request",
			method, spec.GitLab, page.nodes, page.count, page.hasNext, err)
	}
	for _, req := range rec.since(before) {
		if req.method == http.MethodPost && operationName(req.body) == document {
			sentDocs++
		}
	}
	switch g := got.(type) {
	case forgeapi.Page[forgeapi.PullRequest]:
		return first(g.Items), g.Partial, sentDocs
	case forgeapi.PullRequest:
		return g, nil, sentDocs
	}
	t.Fatalf("%s on %s answered %T, want a pull request or a page of them", method, spec.GitLab, got)
	return forgeapi.PullRequest{}, nil, 0
}

// A merge request's label connection serves at most one page of a set GitLab does
// not cap, so where it names a further page the row or the read says its labels are
// cut: the pagination cap, the nodes read as fetched and the connection's own count
// less them as omitted, at no request beyond the read's own document.
func TestAGitLabMergeRequestWhoseLabelsRunPastTheirPageIsMarkedCut(t *testing.T) {
	cases := map[string]struct {
		page labelPage
		want forgeapi.Partial
	}{
		"count_past_the_page": {
			page: labelPage{nodes: 100, count: 105, hasNext: true},
			want: forgeapi.Partial{Reason: forgeapi.PartialPaginationCap, Fetched: 100, OmittedAtLeast: 5},
		},
		"a_further_page_the_count_does_not_account_for": {
			page: labelPage{nodes: 100, count: 100, hasNext: true},
			want: forgeapi.Partial{Reason: forgeapi.PartialPaginationCap, Fetched: 100, OmittedAtLeast: 1},
		},
	}
	for method := range labelReads {
		for name, tc := range cases {
			t.Run(method+"_"+name, func(t *testing.T) {
				pr, pagePartial, docs := readLabels(t, method, tc.page)
				if pr.Partial == nil || *pr.Partial != tc.want {
					t.Errorf("%s on %s over %d label node(s), count %d, hasNextPage true: Partial = %+v, want %+v",
						method, spec.GitLab, tc.page.nodes, tc.page.count, pr.Partial, tc.want)
				}
				if len(pr.Labels) != tc.page.nodes {
					t.Errorf("%s on %s over %d label node(s): %d label(s), want the %d read", method, spec.GitLab, tc.page.nodes, len(pr.Labels), tc.page.nodes)
				}
				if pagePartial != nil {
					t.Errorf("%s on %s: the page's Partial = %+v, want nil: the cut is the row's, not the list's", method, spec.GitLab, pagePartial)
				}
				if docs != 1 {
					t.Errorf("%s on %s sent %d document request(s), want 1: the cut is read off the answer at no further request", method, spec.GitLab, docs)
				}
			})
		}
	}
}

// A label connection that names no further page served the whole set, however
// full its one page is, so the row or the read carries no marker for it.
func TestAGitLabMergeRequestWhoseLabelsFitTheirPageIsNotMarked(t *testing.T) {
	pages := map[string]labelPage{
		"a_full_page":     {nodes: 100, count: 100},
		"under_the_page":  {nodes: 3, count: 3},
		"no_label_at_all": {},
	}
	for method := range labelReads {
		for name, page := range pages {
			t.Run(method+"_"+name, func(t *testing.T) {
				pr, _, _ := readLabels(t, method, page)
				if pr.Partial != nil {
					t.Errorf("%s on %s over %d label node(s), count %d, hasNextPage false: Partial = %+v, want nil",
						method, spec.GitLab, page.nodes, page.count, pr.Partial)
				}
				if len(pr.Labels) != page.nodes {
					t.Errorf("%s on %s over %d label node(s): %d label(s), want every one", method, spec.GitLab, page.nodes, len(pr.Labels))
				}
			})
		}
	}
}
