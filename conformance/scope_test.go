package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// listCall drives one list operation with the options a case names.
type listCall func(ctx context.Context, c any, s subject, opts ...forgeapi.ListOption) (any, error)

// lists is every list operation the roles declare, keyed by the table's spelling, so
// a case about the list-option vocabulary can reach each one with options the
// contract's own invoke does not pass.
var lists = map[string]listCall{
	"Repos.ListRepos": func(ctx context.Context, c any, _ subject, opts ...forgeapi.ListOption) (any, error) {
		r, ok := c.(forgeapi.Repos)
		if !ok {
			return nil, roleAbsent("Repos")
		}
		return r.ListRepos(ctx, opts...)
	},
	"PullRequests.ListPRs": func(ctx context.Context, c any, s subject, opts ...forgeapi.ListOption) (any, error) {
		r, ok := c.(forgeapi.PullRequests)
		if !ok {
			return nil, roleAbsent("PullRequests")
		}
		return r.ListPRs(ctx, s.repo, opts...)
	},
	"PullRequests.ListMyPRs": func(ctx context.Context, c any, _ subject, opts ...forgeapi.ListOption) (any, error) {
		r, ok := c.(forgeapi.PullRequests)
		if !ok {
			return nil, roleAbsent("PullRequests")
		}
		return r.ListMyPRs(ctx, opts...)
	},
	"Issues.ListIssues": func(ctx context.Context, c any, s subject, opts ...forgeapi.ListOption) (any, error) {
		r, ok := c.(forgeapi.Issues)
		if !ok {
			return nil, roleAbsent("Issues")
		}
		return r.ListIssues(ctx, s.repo, opts...)
	},
	"Issues.ListMyIssues": func(ctx context.Context, c any, _ subject, opts ...forgeapi.ListOption) (any, error) {
		if _, ok := c.(forgeapi.Issues); !ok {
			return nil, roleAbsent("Issues")
		}
		r, ok := c.(crossRepositoryIssues)
		if !ok {
			return nil, methodAbsent("Issues.ListMyIssues")
		}
		return r.ListMyIssues(ctx, opts...)
	},
	"Checks.ListRuns": func(ctx context.Context, c any, s subject, opts ...forgeapi.ListOption) (any, error) {
		if _, ok := c.(forgeapi.Checks); !ok {
			return nil, roleAbsent("Checks")
		}
		return callListRuns(ctx, c, s.repo, opts...)
	},
	"Releases.ListReleases": func(ctx context.Context, c any, s subject, opts ...forgeapi.ListOption) (any, error) {
		r, ok := c.(forgeapi.Releases)
		if !ok {
			return nil, roleAbsent("Releases")
		}
		return r.ListReleases(ctx, s.repo, opts...)
	},
	"Labels.ListLabels": func(ctx context.Context, c any, s subject, opts ...forgeapi.ListOption) (any, error) {
		r, ok := c.(forgeapi.Labels)
		if !ok {
			return nil, roleAbsent("Labels")
		}
		return r.ListLabels(ctx, s.repo, opts...)
	},
}

// crossRepositoryLists are the two lists whose scope is the credential's own
// authorship by default and an owner's open items under the owner option.
var crossRepositoryLists = []string{"PullRequests.ListMyPRs", "Issues.ListMyIssues"}

// entryFor is one product's table entry for one operation.
func entryFor(p spec.Product, method string) (spec.Entry, bool) {
	for _, e := range spec.Table {
		if e.Product == p && e.Method == method {
			return e, true
		}
	}
	return spec.Entry{}, false
}

// requireEntry is entryFor for a case that has nothing to hold without the entry: a
// table that carries no entry for a published operation is the failure itself.
func requireEntry(t *testing.T, p spec.Product, method string) spec.Entry {
	t.Helper()
	e, ok := entryFor(p, method)
	if !ok {
		t.Fatalf("spec.Table carries no %s entry for %s, want one: the operation is published on every product, measured or pending", method, p)
	}
	return e
}

// listAgainst drives one list against the server it is given, with the options the
// case names, after the prelude the operation declares, and answers what the list
// returned and the requests the list itself sent.
func listAgainst(t *testing.T, e spec.Entry, f fixture, opts ...forgeapi.ListOption) (got any, requests []sent, err error) {
	t.Helper()
	srv, rec := newFixtureServer(t, f)
	return listOn(t, e, srv, rec, opts...)
}

// listOn is listAgainst over a server the case built itself.
func listOn(t *testing.T, e spec.Entry, srv *httptest.Server, rec *recorder, opts ...forgeapi.ListOption) (got any, requests []sent, err error) {
	t.Helper()
	list, ok := lists[e.Method]
	if !ok {
		t.Fatalf("Setup: %s is not a list this suite drives with options", e.Method)
	}
	conn := forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL + apiRoot(e.Product)}
	client, err := guard(func() (any, error) { return newClient(e.Product, conn, offlineOptions(srv)...) })
	if err != nil {
		t.Fatalf("Setup: %s client: %v", e.Product, err)
	}
	s := canonicalSubject(e.Product)
	if op, declared := operationFor(e.Method); declared {
		if prelude := preludeMethod(e, op); prelude != "" {
			runPrelude(t, e, prelude, client, s)
		}
	}
	before := rec.count()
	got, err = guard(func() (any, error) { return list(t.Context(), client, s, opts...) })
	return got, rec.since(before), err
}

// listFixture drives one list against its own fixture.
func listFixture(t *testing.T, e spec.Entry, opts ...forgeapi.ListOption) (got any, requests []sent, err error) {
	t.Helper()
	f, err := loadFixture(e.Product, e.Method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return listAgainst(t, e, f, opts...)
}

// arm is what one scope's request carries on one product: the route path on a REST
// product, the query parameters it sends with their values, the ones it must not
// send, and on a document product the search terms its body carries and the ones it
// must not.
type arm struct {
	query   map[string]string
	differ  map[string]string
	path    string
	absent  []string
	terms   []string
	without []string
}

// viewerArm is the default scope's request, the rows the credential itself
// authored, as the table spells it per product.
func viewerArm(p spec.Product, method string) arm {
	issues := method == "Issues.ListMyIssues"
	switch p {
	case spec.GitHub:
		a := arm{terms: []string{"author:@me"}}
		if issues {
			a.terms = append(a.terms, "type:issue")
		}
		return a
	case spec.GitLab:
		if issues {
			return arm{path: "/api/v4/issues", query: map[string]string{
				"scope": "created_by_me", "state": "opened", "with_labels_details": "true",
			}}
		}
		return arm{path: "/api/v4/merge_requests", query: map[string]string{"scope": "created_by_me"}}
	default:
		return arm{path: "/api/v1/repos/issues/search", query: map[string]string{
			"type": giteaSearchType(issues), "created": "true",
		}}
	}
}

// giteaSearchType is the Gitea family's issue-search type parameter for one of the
// two cross-repository lists.
func giteaSearchType(issues bool) string {
	if issues {
		return "issues"
	}
	return "pulls"
}

// checkArm holds the requests one list sent to the scope it was asked for: exactly
// one request, a page of one, carrying what selects the scope and nothing that would
// narrow it to the other.
func checkArm(t *testing.T, e spec.Entry, want arm, requests []sent) {
	t.Helper()
	if len(requests) != 1 {
		t.Errorf("%s on %s sent %d request(s) [%s], want 1: a cross-repository list is one request per page under either scope",
			e.Method, e.Product, len(requests), describeRequests(requests))
		return
	}
	req := requests[0]
	if want.path != "" && req.path != want.path {
		t.Errorf("%s on %s reached %s, want %s", e.Method, e.Product, req, want.path)
	}
	for _, name := range slices.Sorted(maps.Keys(want.query)) {
		if got := req.query.Get(name); !req.query.Has(name) || got != want.query[name] {
			t.Errorf("%s on %s sent %s=%q in %s, want %s=%q", e.Method, e.Product, name, got, req, name, want.query[name])
		}
	}
	for _, name := range want.absent {
		if req.query.Has(name) {
			t.Errorf("%s on %s sent %s=%q in %s, want it absent: it selects the other scope",
				e.Method, e.Product, name, req.query.Get(name), req)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(want.differ)) {
		if req.query.Get(name) == want.differ[name] {
			t.Errorf("%s on %s sent %s=%q in %s, want any other value or none: that value selects the other scope",
				e.Method, e.Product, name, want.differ[name], req)
		}
	}
	if len(want.terms) == 0 && len(want.without) == 0 {
		return
	}
	body := searchText(req.body)
	for _, term := range want.terms {
		if !strings.Contains(body, term) {
			t.Errorf("%s on %s sent a document request carrying no %q, want it in the search", e.Method, e.Product, term)
		}
	}
	for _, term := range want.without {
		if strings.Contains(body, term) {
			t.Errorf("%s on %s sent a document request carrying %q, want it absent: it selects the other scope", e.Method, e.Product, term)
		}
	}
}

// searchText is every string a document request carries, its query text and every
// variable value, which is where a search's terms can ride.
func searchText(body []byte) string {
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return string(body)
	}
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case map[string]any:
			for _, k := range slices.Sorted(maps.Keys(x)) {
				walk(x[k])
			}
		case []any:
			for _, item := range x {
				walk(item)
			}
		}
	}
	walk(decoded)
	return strings.Join(out, "\n")
}

// TestTheCrossRepositoryListsSelectWhatTheCredentialAuthored holds the default scope
// of both cross-repository lists to the request the table names: what selects the
// credential's own rows rides the one request the list sends. A list that dropped it
// would answer every open item the instance holds, and the fixture would serve the
// same row either way, so no field check can see the difference.
func TestTheCrossRepositoryListsSelectWhatTheCredentialAuthored(t *testing.T) {
	for _, method := range crossRepositoryLists {
		for _, p := range spec.Products {
			t.Run(string(p)+"_"+method, func(t *testing.T) {
				e := requireEntry(t, p, method)
				if pending(e) {
					t.Skip(pendingReason(e))
				}
				_, requests, err := listFixture(t, e)
				if err != nil {
					t.Fatalf("%s on %s = error %v, want the authored rows", method, p, err)
				}
				checkArm(t, e, viewerArm(p, method), requests)
			})
		}
	}
}

// fixedStateLists are the lists whose state is fixed by construction, so a state
// filter is a misuse the call refuses before any request.
var fixedStateLists = []string{"PullRequests.ListMyPRs", "Issues.ListMyIssues", "Checks.ListRuns"}

// TestTheFixedStateListsRefuseAStateFilter holds the state filter's refusal on every
// list with no state to filter by: the cross-repository lists answer open items
// only, and the run listing has no state parameter of its own, so a filter there
// would be ignored silently were it not refused.
func TestTheFixedStateListsRefuseAStateFilter(t *testing.T) {
	for _, method := range fixedStateLists {
		for _, p := range spec.Products {
			t.Run(string(p)+"_"+method, func(t *testing.T) {
				e := requireEntry(t, p, method)
				if pending(e) {
					t.Skip(pendingReason(e))
				}
				_, requests, err := listAgainst(t, e, fixture{Routes: setupRoutes(t, e)}, forgeapi.WithState(forgeapi.ListStateOpen))
				checkLocalRefusal(t, e, forgeapi.CodeListStateInvalid, err, requests)
			})
		}
	}
}

// checkLocalRefusal holds a refusal the library issues before any request to its
// code and to the local group's reading of the four fields an upstream answer
// writes: no operation, no family, no status and no diagnostic id.
func checkLocalRefusal(t *testing.T, e spec.Entry, code string, err error, requests []sent) {
	t.Helper()
	if len(requests) != 0 {
		t.Errorf("%s on %s sent %d request(s) %v, want 0: the refusal precedes any request", e.Method, e.Product, len(requests), requests)
	}
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Errorf("%s on %s = error %v, want a *forgeapi.Error with code %q", e.Method, e.Product, err, code)
		return
	}
	if fe.Code != code {
		t.Errorf("%s on %s = code %q, want %q", e.Method, e.Product, fe.Code, code)
	}
	if fe.Op != "" {
		t.Errorf("%s on %s = Op %q, want empty: a local refusal precedes the operation", e.Method, e.Product, fe.Op)
	}
	if fe.Family != forgeapi.FamilyUnknown {
		t.Errorf("%s on %s = family %v, want the unknown member: a local refusal names no answering family", e.Method, e.Product, fe.Family)
	}
	if fe.Status != 0 {
		t.Errorf("%s on %s = status %d, want 0: no request was sent", e.Method, e.Product, fe.Status)
	}
	if fe.DiagID != "" {
		t.Errorf("%s on %s = DiagID %q, want empty: a local refusal mints no diagnostic id", e.Method, e.Product, fe.DiagID)
	}
}

// describeRequests is a short rendering of what arrived, for a failure message.
func describeRequests(requests []sent) string {
	out := make([]string, 0, len(requests))
	for _, r := range requests {
		out = append(out, fmt.Sprintf("%s?%s", r, r.query.Encode()))
	}
	return strings.Join(out, ", ")
}
