package conformance

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// prListCeiling is the most rows a page of GitHub's repository pull-request list
// asks for: 50 answered on every attempt on kubernetes/kubernetes and 75 on none.
const prListCeiling = 50

// prListEndCursor is the end cursor every page the document server below answers
// names.
const prListEndCursor = "Y3Vyc29yOjUw"

// newPRListServer answers GitHub's repository pull-request document with the
// fixture's first row repeated rows times, more pages after it, and records every
// request's variables.
func newPRListServer(t *testing.T, rows int) (*httptest.Server, *recorder) {
	t.Helper()
	f, err := loadFixture(spec.GitHub, "PullRequests.ListPRs")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var answer map[string]any
	if err := json.Unmarshal([]byte(f.Routes[0].Body), &answer); err != nil {
		t.Fatalf("Setup: %s's body is not a JSON object: %v", f.path, err)
	}
	data, _ := answer["data"].(map[string]any)
	repo, _ := data["repository"].(map[string]any)
	prs, _ := repo["pullRequests"].(map[string]any)
	nodes, _ := prs["nodes"].([]any)
	if len(nodes) == 0 {
		t.Fatalf("Setup: %s carries no pull-request row to repeat", f.path)
	}
	repeated := make([]any, rows)
	for i := range repeated {
		repeated[i] = nodes[0]
	}
	prs["nodes"] = repeated
	prs["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": prListEndCursor}
	body, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("Setup: re-encoding the document's answer: %v", err)
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
		if _, err := w.Write(body); err != nil {
			t.Errorf("Setup: writing the document's answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// documentVariables are one document request's variables.
func documentVariables(t *testing.T, req sent) map[string]any {
	t.Helper()
	var doc struct {
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(req.body, &doc); err != nil {
		t.Fatalf("Setup: the document request's body is not JSON: %v", err)
	}
	return doc.Variables
}

// prListOne drives one ListPRs call over the document server and answers its page
// and the one request it sent.
func prListOne(t *testing.T, srv *httptest.Server, rec *recorder, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], sent) {
	t.Helper()
	e := requireEntry(t, spec.GitHub, "PullRequests.ListPRs")
	got, requests, err := listOn(t, e, srv, rec, opts...)
	if err != nil {
		t.Fatalf("ListPRs on %s = error %v, want the page", spec.GitHub, err)
	}
	if len(requests) != 1 {
		t.Fatalf("ListPRs on %s sent %d request(s) [%s], want 1: one request per page", spec.GitHub, len(requests), describeRequests(requests))
	}
	page, ok := got.(forgeapi.Page[forgeapi.PullRequest])
	if !ok {
		t.Fatalf("ListPRs on %s answered %T, want a page of pull requests", spec.GitHub, got)
	}
	return page, requests[0]
}

// pageSizeVariable is the variable of the document that carries the page size: the
// one that follows the page bound across two calls asking for different bounds.
func pageSizeVariable(t *testing.T) string {
	t.Helper()
	srv, rec := newPRListServer(t, 1)
	_, seven := prListOne(t, srv, rec, forgeapi.WithPageBound(7))
	_, nine := prListOne(t, srv, rec, forgeapi.WithPageBound(9))
	a, b := documentVariables(t, seven), documentVariables(t, nine)
	var found []string
	for _, name := range slices.Sorted(maps.Keys(a)) {
		if x, ok := a[name].(float64); ok && x == 7 {
			if y, ok := b[name].(float64); ok && y == 9 {
				found = append(found, name)
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("Setup: ListPRs on %s carried its page bound in %d variable(s) %v of %v, want exactly one", spec.GitHub, len(found), found, a)
	}
	return found[0]
}

// The repository pull-request list asks for the smaller of the page bound and the
// measured ceiling, whatever bound is asked for, the default included.
func TestTheRepositoryPullRequestListAsksForAtMostItsMeasuredPage(t *testing.T) {
	name := pageSizeVariable(t)
	bounds := map[string]struct {
		opts []forgeapi.ListOption
		want float64
	}{
		"default_bound":    {want: prListCeiling},
		"bound_100":        {opts: []forgeapi.ListOption{forgeapi.WithPageBound(100)}, want: prListCeiling},
		"bound_51":         {opts: []forgeapi.ListOption{forgeapi.WithPageBound(51)}, want: prListCeiling},
		"bound_at_ceiling": {opts: []forgeapi.ListOption{forgeapi.WithPageBound(prListCeiling)}, want: prListCeiling},
		"bound_below":      {opts: []forgeapi.ListOption{forgeapi.WithPageBound(20)}, want: 20},
	}
	for label, bound := range bounds {
		t.Run(label, func(t *testing.T) {
			srv, rec := newPRListServer(t, 1)
			_, req := prListOne(t, srv, rec, bound.opts...)
			if got := documentVariables(t, req)[name]; got != bound.want {
				t.Errorf("ListPRs on %s sent %s = %v, want %v: the smaller of the page bound and %d", spec.GitHub, name, got, bound.want, prListCeiling)
			}
		})
	}
}

// A page the ceiling shortened is an ordinary page: it carries the document's
// continuation and no partial marker, and resuming it asks for the ceiling again
// from the position the page ended at, so the ceiling costs requests and never rows.
func TestARepositoryPullRequestPageTheCeilingShortenedContinues(t *testing.T) {
	name := pageSizeVariable(t)
	srv, rec := newPRListServer(t, prListCeiling)
	page, _ := prListOne(t, srv, rec, forgeapi.WithPageBound(100))
	if len(page.Items) != prListCeiling || page.Next == "" || page.Partial != nil {
		t.Fatalf("ListPRs(WithPageBound(100)) on %s = %d row(s), Next %q, Partial %+v, want %d rows, a continuation and no marker",
			spec.GitHub, len(page.Items), page.Next, page.Partial, prListCeiling)
	}

	_, req := prListOne(t, srv, rec, forgeapi.WithPageBound(100), forgeapi.WithAfter(page.Next))
	vars := documentVariables(t, req)
	if got := vars[name]; got != float64(prListCeiling) {
		t.Errorf("ListPRs resumed on %s sent %s = %v, want %d", spec.GitHub, name, got, prListCeiling)
	}
	carried := false
	for _, v := range vars {
		if v == prListEndCursor {
			carried = true
		}
	}
	if !carried {
		t.Errorf("ListPRs resumed on %s sent variables %v, want the end cursor %q the page named", spec.GitHub, vars, prListEndCursor)
	}
}
