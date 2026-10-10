package conformance

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// The table's fields for what the check-reading operations answer where the product
// refuses the check rollup to the credential.
const (
	rowChecksUnderFineGrained   = "Items[].Action.Checks, under a fine-grained token"
	checksUnderFineGrained      = "Action.Checks, under a fine-grained token"
	commitStateUnderFineGrained = "State, under a fine-grained token"
)

// refusedRollupFields is each check-reading operation's departure field.
var refusedRollupFields = map[string]string{
	"PullRequests.ListPRs":   rowChecksUnderFineGrained,
	"PullRequests.ListMyPRs": rowChecksUnderFineGrained,
	"PullRequests.ReadPR":    checksUnderFineGrained,
	"Checks.CommitStatus":    commitStateUnderFineGrained,
}

// refuseRollups rewrites a document answer the way the product answers a credential
// it refuses the rollup to: each rollup's contexts nodes null, beside one error of
// the product's own discriminator at that path. It reports how many it refused.
func refuseRollups(t *testing.T, body json.RawMessage) (json.RawMessage, int) {
	t.Helper()
	var answer map[string]any
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("Setup: the fixture body is not a JSON object: %v", err)
	}
	var errs []any
	var visit func(v any, path []any)
	visit = func(v any, path []any) {
		switch node := v.(type) {
		case map[string]any:
			for k, child := range node {
				if k == "statusCheckRollup" {
					if rollup, ok := child.(map[string]any); ok {
						if contexts, ok := rollup["contexts"].(map[string]any); ok {
							contexts["nodes"] = nil
							at := append(slices.Clone(path), k, "contexts", "nodes")
							errs = append(errs, map[string]any{"type": "FORBIDDEN", "message": "Resource not accessible by personal access token", "path": at})
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
	visit(answer["data"], nil)
	if len(errs) > 0 {
		answer["errors"] = errs
	}
	out, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("Setup: re-encoding the refused answer: %v", err)
	}
	return out, len(errs)
}

// A product that refuses the check rollup to a fine-grained token answers every other
// field, so each check-reading operation hands the answer on with the refusal on the
// item it belongs to: no verdict, the forbidden marker, and on a list no page marker.
func TestARollupRefusedToTheCredentialIsTheItemsOwnForbiddenMarker(t *testing.T) {
	for _, e := range spec.Table {
		field, reads := refusedRollupFields[e.Method]
		if !reads {
			continue
		}
		if _, departs := departureFor(&e, field); !departs {
			continue
		}
		t.Run(e.Method+"_"+string(e.Product), func(t *testing.T) {
			f, err := loadFixture(e.Product, e.Method)
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			refused := 0
			for i := range f.Routes {
				if f.Routes[i].GraphQL {
					var n int
					f.Routes[i].Body, n = refuseRollups(t, f.Routes[i].Body)
					refused += n
				}
			}
			if refused == 0 {
				t.Fatalf("Setup: %s's fixture %s carries no check rollup to refuse", e.Method, f.path)
			}
			op, _ := operationFor(e.Method)
			srv, rec := newFixtureServer(t, f)
			got, err := invokeOn(t, e, op, srv)
			if err != nil {
				t.Fatalf("%s on %s with its rollup refused = %v, want the answer", e.Method, e.Product, err)
			}
			for _, item := range refusedItems(t, got) {
				if item.partial == nil || item.partial.Reason != forgeapi.PartialForbidden {
					t.Errorf("%s on %s with its rollup refused = partial %v, want %v", e.Method, e.Product, item.partial, forgeapi.PartialForbidden)
				}
				if item.checks != forgeapi.CheckUnknown || item.total != 0 {
					t.Errorf("%s on %s with its rollup refused = checks %v over %d, want unknown and no count", e.Method, e.Product, item.checks, item.total)
				}
			}
			if misses := rec.misses(); len(misses) > 0 {
				t.Errorf("%s on %s sent request(s) its fixture does not answer: %v", e.Method, e.Product, misses)
			}
			if page, isPage := got.(forgeapi.Page[forgeapi.PullRequest]); isPage && page.Partial != nil {
				t.Errorf("%s on %s with a row's rollup refused = page partial %+v, want none: every row was answered", e.Method, e.Product, *page.Partial)
			}
		})
	}
}

// refusedItem is what one answered item carries about its checks.
type refusedItem struct {
	partial *forgeapi.Partial
	checks  forgeapi.CheckState
	total   int
}

func refusedItems(t *testing.T, got any) []refusedItem {
	t.Helper()
	switch g := got.(type) {
	case forgeapi.Page[forgeapi.PullRequest]:
		if len(g.Items) == 0 {
			t.Fatal("the list answered no row, want the fixture's rows")
		}
		out := make([]refusedItem, 0, len(g.Items))
		for i := range g.Items {
			out = append(out, refusedItem{partial: g.Items[i].Partial, checks: g.Items[i].Action.Checks, total: g.Items[i].Action.ChecksTotal})
		}
		return out
	case forgeapi.PullRequest:
		return []refusedItem{{partial: g.Partial, checks: g.Action.Checks, total: g.Action.ChecksTotal}}
	case forgeapi.CommitChecks:
		return []refusedItem{{partial: g.Partial, checks: g.State, total: g.Total}}
	}
	t.Fatalf("the operation answered %T, want pull requests or a commit's checks", got)
	return nil
}

// invokeOn drives one operation against a server the case built, after the prelude the
// operation declares.
func invokeOn(t *testing.T, e spec.Entry, op operation, srv *httptest.Server) (any, error) {
	t.Helper()
	conn := forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL + apiRoot(e.Product)}
	client, err := guard(func() (any, error) { return newClient(e.Product, conn, offlineOptions(srv)...) })
	if err != nil {
		t.Fatalf("Setup: %s client: %v", e.Product, err)
	}
	s := canonicalSubject(e.Product)
	if prelude := preludeMethod(e, op); prelude != "" {
		runPrelude(t, e, prelude, client, s)
	}
	return guard(func() (any, error) { return op.invoke(t.Context(), client, s) })
}

func TestTheForbiddenReasonCrossesTheWireAsForbidden(t *testing.T) {
	if forgeapi.PartialForbidden != forgeapi.PartialResultWindow+1 {
		t.Errorf("PartialForbidden = %d, want %d: the member is appended after result_window, so every earlier member keeps its value",
			int(forgeapi.PartialForbidden), int(forgeapi.PartialResultWindow+1))
	}
	if got := forgeapi.PartialForbidden.String(); got != "forbidden" {
		t.Errorf("PartialForbidden.String() = %q, want %q", got, "forbidden")
	}
}
