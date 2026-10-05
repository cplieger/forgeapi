package conformance

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/internal/spec"
)

// laneListInstance serves one cross-repository list's fixture to the live lane, once
// per case the lane runs for the entry, and points the product's lane variables at
// it with the fixture repository as the sandbox, whose owner is the canonical one.
func laneListInstance(t *testing.T, p spec.Product, method string) *recorder {
	t.Helper()
	f, err := loadFixture(p, method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	f.Routes = append(slices.Clone(f.Routes), f.Routes...)
	srv, rec := newFixtureServer(t, f)
	pointLiveLaneAt(t, p, srv.URL, selector)
	return rec
}

// ownerScoped is the requests that carry an owner selection in any of the places a
// product puts one: a group route, an owner filter or a search qualifier.
func ownerScoped(requests []sent) []sent {
	var out []sent
	for _, r := range requests {
		if strings.Contains(r.path, "/groups/") || r.query.Has("owner") || strings.Contains(searchText(r.body), "user:") {
			out = append(out, r)
		}
	}
	return out
}

// TestTheLaneSendsEachCrossRepositoryListOnTheSandboxOwnersRoute holds the live lane
// to the owner scope of both cross-repository lists: beside the viewer's case, the
// lane lists under the owner its sandbox variable names, so a scheduled run sends the
// owner route on every product and goes red when that route moves. The instance is
// the offline fixture, so the owner case's price and cells hold against the table
// exactly as the live answer is held.
func TestTheLaneSendsEachCrossRepositoryListOnTheSandboxOwnersRoute(t *testing.T) {
	for _, method := range crossRepositoryLists {
		for _, p := range spec.Products {
			t.Run(string(p)+"_"+method, func(t *testing.T) {
				e := requireEntry(t, p, method)
				if pending(e) {
					t.Skip(pendingReason(e))
				}
				requireOwnerArm(t, e)
				op, ok := operationFor(method)
				if !ok {
					t.Fatalf("Setup: the contract declares no case for %s", method)
				}
				rec := laneListInstance(t, p, method)
				runLiveEntry(t, e, op)
				checkArm(t, e, ownerArm(p, method), ownerScoped(rec.since(0)))
			})
		}
	}
}
