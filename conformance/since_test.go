package conformance

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// TestASinceBoundOnAListWithNoCreationBoundIsRefused holds the since bound to the one
// list it narrows: on every other list it is a misuse, refused before any request.
func TestASinceBoundOnAListWithNoCreationBoundIsRefused(t *testing.T) {
	for _, method := range slices.Sorted(maps.Keys(lists)) {
		if method == "Checks.ListRuns" {
			continue
		}
		for _, p := range spec.Products {
			t.Run(string(p)+"_"+method, func(t *testing.T) {
				e := requireEntry(t, p, method)
				if pending(e) {
					t.Skip(pendingReason(e))
				}
				_, requests, err := listAgainst(t, e, fixture{Routes: setupRoutes(t, e)}, forgeapi.WithSince(createdAt))
				checkLocalRefusal(t, e, forgeapi.CodeListSinceInvalid, err, requests)
			})
		}
	}
}

// runCreatedKey is the key each product's run row spells its creation time under in
// the canonical fixture.
var runCreatedKey = map[spec.Product]string{
	spec.GitHub: "created_at", spec.GitLab: "created_at", spec.Gitea: "created_at", spec.Forgejo: "created",
}

// runFixture is one product's run-listing fixture with its first route answering
// rows, each the fixture's own row with the overrides given, and the stated total
// where the product's envelope carries one.
func runFixture(t *testing.T, p spec.Product, total int, rows ...map[string]any) fixture {
	t.Helper()
	f, err := loadFixture(p, "Checks.ListRuns")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var row map[string]any
	if p == spec.GitLab {
		var listing []map[string]any
		if err := json.Unmarshal(f.Routes[0].Body, &listing); err != nil || len(listing) == 0 {
			t.Fatalf("Setup: %s carries no pipeline row: %v", f.path, err)
		}
		row = listing[0]
	} else {
		var listing struct {
			Runs []map[string]any `json:"workflow_runs"`
		}
		if err := json.Unmarshal(f.Routes[0].Body, &listing); err != nil || len(listing.Runs) == 0 {
			t.Fatalf("Setup: %s carries no run row: %v", f.path, err)
		}
		row = listing.Runs[0]
	}
	answer := make([]map[string]any, 0, len(rows))
	for _, overrides := range rows {
		r := maps.Clone(row)
		maps.Copy(r, overrides)
		answer = append(answer, r)
	}
	var body any = answer
	if p != spec.GitLab {
		body = map[string]any{"total_count": total, "workflow_runs": answer}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Setup: encoding the run page: %v", err)
	}
	f.Routes[0].Body = raw
	f.Routes[0].Headers = map[string]string{"Content-Type": "application/json"}
	return f
}

// TestASinceBoundHoldsTheRunListingToTheRunsCreatedSinceIt holds every product's run
// listing under a bound read at whole seconds: the filter the table says the route
// takes is sent, a run stamped in the bound's own second is answered, the run created
// before it is dropped, and where the route takes no time filter a page still holding
// a run inside the bound goes on to the next.
func TestASinceBoundHoldsTheRunListingToTheRunsCreatedSinceIt(t *testing.T) {
	bound := createdAt.Add(-time.Hour)
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			e := runsEntry(t, p)
			key := runCreatedKey[p]
			// A stated total above the rows served, so a walk continues on the products
			// that page by the total.
			f := runFixture(t, p, 120,
				map[string]any{"id": 3, key: createdAt.Format(time.RFC3339)},
				map[string]any{"id": 2, key: bound.Format(time.RFC3339)},
				map[string]any{"id": 1, key: bound.Add(-time.Second).Format(time.RFC3339)})
			got, requests, err := listAgainst(t, e, f, forgeapi.WithSince(bound.Add(500*time.Millisecond)))
			if err != nil {
				t.Fatalf("ListRuns(WithSince(%v)) on %s = error %v, want the page", bound, p, err)
			}
			page := got.(forgeapi.Page[forgeapi.Run])
			if len(page.Items) != 2 || page.Items[0].Ref.ID != 3 || page.Items[1].Ref.ID != 2 {
				t.Errorf("ListRuns(WithSince(%v)) on %s answered %+v, want runs 3 and 2, the ones stamped in or after the bound's second", bound, p, page.Items)
			}
			filtered := slices.ContainsFunc(e.Sends, func(row spec.Sent) bool { return row.Holds == spec.HoldsSinceBound })
			if continued := page.Next != ""; continued == filtered {
				t.Errorf("ListRuns(WithSince(%v)) on %s = Next %q, want a continuation exactly where the route takes no time filter (filter sent: %t)", bound, p, page.Next, filtered)
			}
			sub := canonicalSubject(p)
			sub.since = bound
			for _, row := range e.Sends {
				if row.Holds != spec.HoldsSinceBound {
					continue
				}
				want, ok := wanted(&row, &sub)
				req, found := carrier(requests, &row)
				if !ok || !found || carried(req, &row) != want {
					t.Errorf("ListRuns(WithSince(%v)) on %s sent %s in %v, want %q: %s", bound, p, row.Field, requests, want, row.Says)
				}
			}
		})
	}
}

// TestARunWithNoCreationTimeAnswersAsTheTableStates holds the products whose table
// says a release's run row carries no creation time to it: the row answers zero times
// with no bound, and a bound, which such a row cannot be held to, is refused rather
// than answered as a page holding no run.
func TestARunWithNoCreationTimeAnswersAsTheTableStates(t *testing.T) {
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			e := runsEntry(t, p)
			if _, departs := departureFor(&e, createdBeforeV28); !departs {
				t.Skipf("the table states a creation time on every %s run row", p)
			}
			f := runFixture(t, p, 1, map[string]any{runCreatedKey[p]: nil, "updated_at": nil})
			got, _, err := listAgainst(t, e, f)
			if err != nil {
				t.Fatalf("ListRuns on %s over a row with no creation time = error %v, want the page", p, err)
			}
			row := first(got.(forgeapi.Page[forgeapi.Run]).Items)
			failures, _, _ := cellFailures(e, []check{
				eq(createdBeforeV28, row.CreatedAt, createdAt),
				eq(updatedBeforeV28, row.UpdatedAt, createdAt),
			})
			for _, f := range failures {
				t.Error(f)
			}
			_, _, err = listAgainst(t, e, runFixture(t, p, 1, map[string]any{runCreatedKey[p]: nil}), forgeapi.WithSince(createdAt))
			var fe *forgeapi.Error
			if !errors.As(err, &fe) || fe.Code != forgeapi.CodeRunUndated || fe.Kind != forgeapi.KindUpstream {
				t.Errorf("ListRuns(WithSince) on %s over a row with no creation time = %v, want code %q of kind %v", p, err, forgeapi.CodeRunUndated, forgeapi.KindUpstream)
			}
		})
	}
}

// TestABoundedRunWalkEndingShortOfItsTotalAnswersAsTheTableStates holds the products
// whose table states a window on a bounded run listing to it: a walk ending with no
// continuation while the stated total says more carries the result window.
func TestABoundedRunWalkEndingShortOfItsTotalAnswersAsTheTableStates(t *testing.T) {
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			e := runsEntry(t, p)
			if _, departs := departureFor(&e, nextUnderSince); !departs {
				t.Skipf("the table states no window on %s's bounded run listing", p)
			}
			f := runFixture(t, p, 1500, map[string]any{runCreatedKey[p]: createdAt.Format(time.RFC3339)})
			got, _, err := listAgainst(t, e, f, forgeapi.WithSince(createdAt.Add(-time.Hour)))
			if err != nil {
				t.Fatalf("ListRuns(WithSince) on %s = error %v, want the page", p, err)
			}
			page := got.(forgeapi.Page[forgeapi.Run])
			if pt := page.Partial; page.Next != "" || pt == nil || pt.Reason != forgeapi.PartialResultWindow || pt.Fetched != 1 || pt.OmittedAtLeast != 1499 {
				t.Errorf("ListRuns(WithSince) on %s over 1 of a stated 1500 = Next %q, Partial %+v, want no continuation and result_window with 1 fetched and at least 1499 omitted", p, page.Next, pt)
			}
		})
	}
}

// runRowKeys are the keys each product's published run shape names a run's number
// and its trigger under: GitHub's REST workflow run (run_number, event), GitLab's
// pipeline (iid, source), Gitea's ActionWorkflowRun (run_number, event) and Forgejo's
// ActionRun (index_in_repo, trigger_event).
var runRowKeys = map[spec.Product][2]string{
	spec.GitHub: {"run_number", "event"}, spec.GitLab: {"iid", "source"},
	spec.Gitea: {"run_number", "event"}, spec.Forgejo: {"index_in_repo", "trigger_event"},
}

// TestEveryProductsRunRowCarriesItsNumberAndItsTrigger holds the two run fields the
// recorded fixtures were trimmed of to the canonical row carrying them under the
// product's own keys, on the table's terms.
func TestEveryProductsRunRowCarriesItsNumberAndItsTrigger(t *testing.T) {
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			e := runsEntry(t, p)
			keys := runRowKeys[p]
			got, _, err := listAgainst(t, e, runFixture(t, p, 1, map[string]any{keys[0]: 5, keys[1]: "push"}))
			if err != nil {
				t.Fatalf("ListRuns on %s = error %v, want the page", p, err)
			}
			row := first(got.(forgeapi.Page[forgeapi.Run]).Items)
			failures, _, _ := cellFailures(e, []check{
				eq("Items[].Ref.Number", row.Ref.Number, int64(5)),
				eq("Items[].Trigger", row.Trigger, forgeapi.TriggerPush),
			})
			for _, f := range failures {
				t.Error(f)
			}
		})
	}
}

// runStartedKey is the key each product's published run shape names the current
// attempt's start under: GitHub's REST workflow run (run_started_at), Gitea's
// ActionWorkflowRun (started_at) and Forgejo's ActionRun (started). GitLab's
// pipeline listing row carries none (only the single-pipeline route does), so its
// row is served without one.
var runStartedKey = map[spec.Product]string{
	spec.GitHub: "run_started_at", spec.Gitea: "started_at", spec.Forgejo: "started",
}

// TestEveryProductsRunRowCarriesItsStartTime holds the run's start time, which the
// recorded fixtures were trimmed of, to the canonical row carrying it under the
// product's own key, on the table's terms.
func TestEveryProductsRunRowCarriesItsStartTime(t *testing.T) {
	started := createdAt.Add(30 * time.Second)
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			e := runsEntry(t, p)
			overrides := map[string]any{}
			if key, ok := runStartedKey[p]; ok {
				overrides[key] = started.Format(time.RFC3339)
			}
			got, _, err := listAgainst(t, e, runFixture(t, p, 1, overrides))
			if err != nil {
				t.Fatalf("ListRuns on %s = error %v, want the page", p, err)
			}
			row := first(got.(forgeapi.Page[forgeapi.Run]).Items)
			failures, _, _ := cellFailures(e, []check{eq(runStartedAt, row.StartedAt, started)})
			for _, f := range failures {
				t.Error(f)
			}
		})
	}
}

// RunRef.Number is a place in the repository's run sequence, zero where absent, so a
// row carrying a negative one is refused on every product rather than published.
func TestARunWithANegativeNumberIsRefused(t *testing.T) {
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			e := runsEntry(t, p)
			_, _, err := listAgainst(t, e, runFixture(t, p, 1, map[string]any{runRowKeys[p][0]: -1}))
			var fe *forgeapi.Error
			if !errors.As(err, &fe) || fe.Code != forgeapi.CodeValidation || fe.Kind != forgeapi.KindUpstream {
				t.Errorf("ListRuns on %s over a row numbered -1 = %v, want code %q of kind %v", p, err, forgeapi.CodeValidation, forgeapi.KindUpstream)
			}
		})
	}
}
