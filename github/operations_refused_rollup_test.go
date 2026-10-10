package github

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// rollupNodesPath is where a pull request's selection reaches its check rollup's
// context rows, after the pull request's own path.
var rollupNodesPath = []any{"commits", "nodes", 0, "commit", "statusCheckRollup", "contexts", "nodes"}

// refusedRollupRow is the recorded search row as a fine-grained token was answered on a
// private repository, measured 2026-10-11: every field of the row present and its
// rollup's context rows null, beside one FORBIDDEN error at that path. number keeps two
// rows of one page apart.
func refusedRollupRow(t *testing.T, number int) string {
	t.Helper()
	var row map[string]any
	readCaptures(t).row(t, "graphql_search_row", &row)
	row["number"] = number
	commits, _ := row["commits"].(map[string]any)
	nodes, _ := commits["nodes"].([]any)
	node, _ := nodes[0].(map[string]any)
	commit, _ := node["commit"].(map[string]any)
	rollup, _ := commit["statusCheckRollup"].(map[string]any)
	contexts, _ := rollup["contexts"].(map[string]any)
	contexts["nodes"] = nil
	out, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("Setup: encoding the refused row: %v", err)
	}
	return string(out)
}

// withErrors adds envelope errors to a document answer, each of type kind at its path.
func withErrors(t *testing.T, body, kind string, paths ...[]any) string {
	t.Helper()
	errs := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		errs = append(errs, map[string]any{"type": kind, "message": "Resource not accessible by personal access token", "path": p})
	}
	raw, err := json.Marshal(errs)
	if err != nil {
		t.Fatalf("Setup: encoding the envelope errors: %v", err)
	}
	return strings.TrimSuffix(body, "}") + `,"errors":` + string(raw) + `}`
}

func under(prefix []any, rest ...any) []any {
	return append(append([]any{}, prefix...), rest...)
}

// refusedRollupList is one list document answering a recorded row and a second row
// whose rollup the product refused, with the path its rows sit under.
type refusedRollupList struct {
	list   func(*testing.T, *harness) forgeapi.Page[forgeapi.PullRequest]
	answer func(t *testing.T, rows string) string
	name   string
	rows   []any
}

func refusedRollupLists() []refusedRollupList {
	return []refusedRollupList{
		{
			name: "ListMyPRs", rows: []any{"search", "nodes"},
			answer: func(_ *testing.T, rows string) string {
				return documentEnvelope(`"search":{"issueCount":2,"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[` + rows + `]}`)
			},
			list: func(t *testing.T, h *harness) forgeapi.Page[forgeapi.PullRequest] {
				t.Helper()
				page, err := h.client.ListMyPRs(t.Context())
				if err != nil {
					t.Fatalf("ListMyPRs = %v, want the page", err)
				}
				return page
			},
		},
		{
			name: "ListPRs", rows: []any{"repository", "pullRequests", "nodes"},
			answer: func(_ *testing.T, rows string) string {
				return documentEnvelope(`"repository":{"nameWithOwner":"example/example","viewerPermission":"WRITE",` +
					`"pullRequests":{"totalCount":2,"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[` + rows + `]}}`)
			},
			list: func(t *testing.T, h *harness) forgeapi.Page[forgeapi.PullRequest] {
				t.Helper()
				page, err := h.client.ListPRs(t.Context(), testRef())
				if err != nil {
					t.Fatalf("ListPRs = %v, want the page", err)
				}
				return page
			},
		},
	}
}

// A fine-grained token holds no Checks permission, so on a repository whose check runs
// it cannot read the product refuses one row's rollup and answers every other field.
// The list is whole: the refusal rides that row's own marker, with no verdict, and the
// page carries none.
func TestARollupRefusedToTheCredentialMarksItsOwnRowAndNotThePage(t *testing.T) {
	for _, test := range refusedRollupLists() {
		t.Run(test.name, func(t *testing.T) {
			rows := readCaptures(t).raw(t, "graphql_search_row") + "," + refusedRollupRow(t, 2)
			h := newHarness(t, map[string]string{
				documentRoute: withErrors(t, test.answer(t, rows), "FORBIDDEN", under(test.rows, append([]any{1}, rollupNodesPath...)...)),
			})
			page := test.list(t, h)
			if page.Partial != nil {
				t.Errorf("%s with one row's rollup refused = page partial %+v, want none: every row was answered", test.name, *page.Partial)
			}
			if len(page.Items) != 2 {
				t.Fatalf("%s with one row's rollup refused = %d row(s), want 2", test.name, len(page.Items))
			}
			if p := page.Items[0].Partial; p != nil {
				t.Errorf("%s's readable row = partial %+v, want none", test.name, *p)
			}
			refused := page.Items[1]
			if refused.Partial == nil || refused.Partial.Reason != forgeapi.PartialForbidden {
				t.Errorf("%s's refused row = partial %v, want %v", test.name, refused.Partial, forgeapi.PartialForbidden)
			}
			if refused.Action.Checks != forgeapi.CheckUnknown || refused.Action.ChecksTotal != 0 || refused.Action.ChecksPassing != 0 {
				t.Errorf("%s's refused row = checks %v total %d passing %d, want unknown and no count: part of the rollup was refused",
					test.name, refused.Action.Checks, refused.Action.ChecksTotal, refused.Action.ChecksPassing)
			}
			if refused.Ref.Number != 2 || refused.Title == "" || refused.HeadSHA == "" {
				t.Errorf("%s's refused row = number %d title %q head %q, want the row's own fields", test.name, refused.Ref.Number, refused.Title, refused.HeadSHA)
			}
			if got := h.spy.times("PartialResult:forbidden"); got != 1 {
				t.Errorf("%s counted %d forbidden partial(s), want 1", test.name, got)
			}
		})
	}
}

// Only a refusal under the rollup is the row's own: an error of another type there, or
// a refusal of another field, leaves what the row lacks unnamed, so the page stays
// partial as before.
func TestAnErrorBesideTheRowsThatIsNoRollupRefusalStillMarksThePage(t *testing.T) {
	for _, test := range refusedRollupLists() {
		for name, errs := range map[string]struct {
			kind string
			path []any
		}{
			"another_type_under_the_rollup": {kind: "INTERNAL", path: under(test.rows, append([]any{1}, rollupNodesPath...)...)},
			"a_refusal_of_another_field":    {kind: "FORBIDDEN", path: under(test.rows, 1, "headRepository")},
		} {
			t.Run(test.name+"_"+name, func(t *testing.T) {
				rows := readCaptures(t).raw(t, "graphql_search_row") + "," + refusedRollupRow(t, 2)
				h := newHarness(t, map[string]string{documentRoute: withErrors(t, test.answer(t, rows), errs.kind, errs.path)})
				page := test.list(t, h)
				if page.Partial == nil || page.Partial.Reason != forgeapi.PartialGraphQLPartial {
					t.Errorf("%s beside %s = page partial %v, want %v", test.name, name, page.Partial, forgeapi.PartialGraphQLPartial)
				}
				for i := range page.Items {
					if p := page.Items[i].Partial; p != nil && p.Reason == forgeapi.PartialForbidden {
						t.Errorf("%s beside %s marked row %d forbidden, want no row marked: the refusal is not the rollup's", test.name, name, i)
					}
				}
			})
		}
	}
}

// refusedPRRead is the single-pull-request answer with its rollup's context rows null.
func refusedPRRead(t *testing.T) string {
	t.Helper()
	var repo map[string]any
	readCaptures(t).row(t, "graphql_pr_read", &repo)
	pr, _ := repo["pullRequest"].(map[string]any)
	commits, _ := pr["commits"].(map[string]any)
	nodes, _ := commits["nodes"].([]any)
	node, _ := nodes[len(nodes)-1].(map[string]any)
	commit, _ := node["commit"].(map[string]any)
	if rollup, ok := commit["statusCheckRollup"].(map[string]any); ok {
		if contexts, ok := rollup["contexts"].(map[string]any); ok {
			contexts["nodes"] = nil
		}
	}
	out, err := json.Marshal(repo)
	if err != nil {
		t.Fatalf("Setup: encoding the refused read: %v", err)
	}
	return documentEnvelope(`"repository":` + string(out))
}

// A single read hands its data on beside the marker its errors earn rather than as a
// whole answer: the rollup refused to the credential is forbidden with no verdict, an
// error of another type under it is a partial with no verdict, and an error on another
// field is a partial beside the verdict read.
func TestASinglePullRequestReadMarksWhatItsErrorsLeftOut(t *testing.T) {
	prPath := []any{"repository", "pullRequest"}
	for _, test := range []struct {
		name    string
		kind    string
		path    []any
		reason  forgeapi.PartialReason
		unknown bool
	}{
		{name: "rollup_refused", kind: "FORBIDDEN", path: under(prPath, rollupNodesPath...), reason: forgeapi.PartialForbidden, unknown: true},
		{name: "rollup_failed", kind: "INTERNAL", path: under(prPath, rollupNodesPath...), reason: forgeapi.PartialGraphQLPartial, unknown: true},
		{name: "another_field", kind: "FORBIDDEN", path: under(prPath, "headRepository"), reason: forgeapi.PartialGraphQLPartial},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: withErrors(t, refusedPRRead(t), test.kind, test.path)})
			pr, err := h.client.ReadPR(t.Context(), testRef(), forgeapi.PRRef{Number: 1})
			if err != nil {
				t.Fatalf("ReadPR beside a %s error = %v, want the pull request", test.kind, err)
			}
			if pr.Partial == nil || pr.Partial.Reason != test.reason {
				t.Errorf("ReadPR beside a %s error at %v = partial %v, want %v", test.kind, test.path, pr.Partial, test.reason)
			}
			if test.unknown && (pr.Action.Checks != forgeapi.CheckUnknown || pr.Action.ChecksTotal != 0) {
				t.Errorf("ReadPR beside a %s rollup error = checks %v total %d, want unknown and no count", test.kind, pr.Action.Checks, pr.Action.ChecksTotal)
			}
			if pr.Title == "" || pr.HeadSHA == "" {
				t.Errorf("ReadPR beside a %s error = title %q head %q, want the fields read", test.kind, pr.Title, pr.HeadSHA)
			}
		})
	}
}

// The commit's own rollup read is held to the same terms: a refused rollup is forbidden
// and any other error beside the data a partial, both with no verdict, since the fold
// would otherwise be over part of the evidence with nothing saying so.
func TestACommitRollupReadMarksWhatItsErrorsLeftOut(t *testing.T) {
	answer := documentEnvelope(`"repository":{"nameWithOwner":"example/example","object":{"__typename":"Commit","oid":"` + testHeadSHA + `",` +
		`"statusCheckRollup":{"state":"SUCCESS","contexts":{"totalCount":1,"checkRunCount":1,"statusContextCount":0,` +
		`"checkRunCountsByState":[{"state":"SUCCESS","count":1}],"statusContextCountsByState":[],` +
		`"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":null}}}}`)
	path := []any{"repository", "object", "statusCheckRollup", "contexts", "nodes"}
	for _, test := range []struct {
		name   string
		kind   string
		reason forgeapi.PartialReason
	}{
		{name: "refused", kind: "FORBIDDEN", reason: forgeapi.PartialForbidden},
		{name: "failed", kind: "INTERNAL", reason: forgeapi.PartialGraphQLPartial},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: withErrors(t, answer, test.kind, path)})
			cc, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA)
			if err != nil {
				t.Fatalf("CommitStatus beside a %s error = %v, want the answer", test.kind, err)
			}
			if cc.Partial == nil || cc.Partial.Reason != test.reason {
				t.Errorf("CommitStatus beside a %s error = partial %v, want %v", test.kind, cc.Partial, test.reason)
			}
			if cc.State != forgeapi.CheckUnknown || cc.Total != 0 || cc.Passing != 0 {
				t.Errorf("CommitStatus beside a %s error = state %v total %d passing %d, want unknown and no count", test.kind, cc.State, cc.Total, cc.Passing)
			}
		})
	}
}
