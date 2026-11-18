package conformance

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// noCheckFields is each check-reading operation's verdict field, whose departure, where
// a product declares one it cannot supply, is what that product answers instead of
// [forgeapi.CheckNone].
var noCheckFields = map[string]string{
	"PullRequests.ListPRs":   "Items[].Action.Checks",
	"PullRequests.ListMyPRs": "Items[].Action.Checks",
	"PullRequests.ReadPR":    "Action.Checks",
	"Checks.CommitStatus":    "State",
}

// strikeChecks rewrites a fixture answer the way each product answers a head that
// carries no check: GitHub's statusCheckRollup null, GitLab's headPipeline null and its
// statuses route an empty array, and the Gitea family's combined status with no row.
// With nulledBy set, each struck GraphQL field also carries an envelope error at its
// path, which is how a product answers a field a resolver failed rather than one that
// is empty. It reports how many fields it struck.
func strikeChecks(t *testing.T, w *wire, nulledBy string) int {
	t.Helper()
	if !w.GraphQL && strings.Contains(w.Path, "/repository/commits/") && strings.HasSuffix(w.Path, "/statuses") {
		w.Body = json.RawMessage(`[]`)
		return 1
	}
	var answer any
	if err := json.Unmarshal(w.Body, &answer); err != nil {
		return 0
	}
	struck := 0
	var errs []any
	var visit func(v any, path []any)
	visit = func(v any, path []any) {
		switch node := v.(type) {
		case map[string]any:
			if _, combined := node["statuses"]; combined && !w.GraphQL {
				node["statuses"] = []any{}
				node["total_count"] = 0
				struck++
				return
			}
			for k, child := range node {
				if w.GraphQL && (k == "statusCheckRollup" || k == "headPipeline") {
					if child != nil {
						node[k] = nil
						struck++
						if nulledBy != "" {
							errs = append(errs, map[string]any{"message": nulledBy, "path": append(slices.Clone(path), k)})
						}
					}
					continue
				}
				visit(child, append(slices.Clone(path), k))
			}
		case []any:
			for i, child := range node {
				visit(child, append(slices.Clone(path), i))
			}
		}
	}
	if root, ok := answer.(map[string]any); ok && w.GraphQL {
		visit(root["data"], nil)
		if len(errs) > 0 {
			root["errors"] = errs
		}
	} else {
		visit(answer, nil)
	}
	out, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("Setup: re-encoding the struck answer: %v", err)
	}
	w.Body = out
	return struck
}

// struckCase drives one check-reading operation against its fixture with every head's
// checks struck, returning what each answered item carries about them.
func struckCase(t *testing.T, e spec.Entry, nulledBy string) (items []refusedItem, struck int, pagePartial *forgeapi.Partial) {
	t.Helper()
	f, err := loadFixture(e.Product, e.Method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	for i := range f.Routes {
		struck += strikeChecks(t, &f.Routes[i], nulledBy)
	}
	op, _ := operationFor(e.Method)
	srv, rec := newFixtureServer(t, f)
	got, err := invokeOn(t, e, op, srv)
	if err != nil {
		t.Fatalf("%s on %s with its checks struck = %v, want the answer", e.Method, e.Product, err)
	}
	if misses := rec.misses(); len(misses) > 0 {
		t.Errorf("%s on %s sent request(s) its fixture does not answer: %v", e.Method, e.Product, misses)
	}
	if page, isPage := got.(forgeapi.Page[forgeapi.PullRequest]); isPage {
		pagePartial = page.Partial
	}
	return refusedItems(t, got), struck, pagePartial
}

// A head whose checks were read and found absent is CheckNone with no count, on every
// product whose answer can tell that from a verdict nobody read. A product whose table
// row says it cannot supply the verdict at all answers CheckUnknown, as it does for a
// head that carries checks.
func TestAHeadCarryingNoCheckIsCheckNone(t *testing.T) {
	for _, e := range spec.Table {
		field, reads := noCheckFields[e.Method]
		if !reads {
			continue
		}
		t.Run(e.Method+"_"+string(e.Product), func(t *testing.T) {
			want := forgeapi.CheckNone
			if d, departs := departureFor(&e, field); departs && d.Kind == spec.CannotSupply {
				want = forgeapi.CheckUnknown
			}
			items, struck, _ := struckCase(t, e, "")
			if want == forgeapi.CheckNone && struck == 0 {
				t.Fatalf("Setup: %s on %s: the fixture carries no check field to strike", e.Method, e.Product)
			}
			for i, item := range items {
				if item.checks != want || item.total != 0 {
					t.Errorf("%s on %s item %d with no check on its head = %v over %d, want %v over 0", e.Method, e.Product, i, item.checks, item.total, want)
				}
				if want == forgeapi.CheckNone && item.partial != nil {
					t.Errorf("%s on %s item %d with no check on its head = partial %+v, want none: the read was whole", e.Method, e.Product, i, *item.partial)
				}
			}
		})
	}
}

// A check field a resolver error nulled is a verdict nobody read, so it stays
// CheckUnknown on the two products whose documents answer such an error beside the
// data, rather than reading as a head that carries no check.
func TestACheckFieldAnErrorNulledIsNotCheckNone(t *testing.T) {
	gitlabDocuments := map[string]bool{"PullRequests.ListPRs": true, "PullRequests.ReadPR": true}
	for _, e := range spec.Table {
		onGitHub := e.Product == spec.GitHub && noCheckFields[e.Method] != ""
		onGitLab := e.Product == spec.GitLab && gitlabDocuments[e.Method]
		if !onGitHub && !onGitLab {
			continue
		}
		t.Run(e.Method+"_"+string(e.Product), func(t *testing.T) {
			items, struck, pagePartial := struckCase(t, e, "Internal server error")
			if struck == 0 {
				t.Fatalf("Setup: %s on %s: the fixture carries no check field to strike", e.Method, e.Product)
			}
			for i, item := range items {
				if item.checks != forgeapi.CheckUnknown || item.total != 0 {
					t.Errorf("%s on %s item %d with its check field nulled by an error = %v over %d, want %v over 0",
						e.Method, e.Product, i, item.checks, item.total, forgeapi.CheckUnknown)
				}
				partial := item.partial
				if partial == nil {
					partial = pagePartial
				}
				if partial == nil || partial.Reason != forgeapi.PartialGraphQLPartial {
					t.Errorf("%s on %s item %d with its check field nulled by an error = partial %v, want %v on the item or its page",
						e.Method, e.Product, i, partial, forgeapi.PartialGraphQLPartial)
				}
			}
		})
	}
}
