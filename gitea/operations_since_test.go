package gitea

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// The two products' run rows under the keys their own structs declare: Gitea's
// ActionWorkflowRun (modules/structs/repo_actions.go) numbers a run run_number and
// names its event, Forgejo's ActionRun (modules/structs/action.go) numbers it
// index_in_repo and sends the webhook event beside the workflow's trigger_event.
const (
	giteaNumberedRun   = `{"id":7,"run_number":3,"event":"pull_request","path":"ci.yml@refs/heads/main","created_at":"2026-01-01T00:00:00Z"}`
	forgejoNumberedRun = `{"id":9,"index_in_repo":4,"event":"pull_request_sync","trigger_event":"pull_request","workflow_id":"ci.yml","created":"2026-01-01T00:00:00Z"}`
)

func TestARunCarriesItsIdentityAndWhatStartedItOnEitherProduct(t *testing.T) {
	for name, test := range map[string]struct {
		row  string
		want forgeapi.RunRef
	}{
		"gitea":   {row: giteaNumberedRun, want: forgeapi.RunRef{ID: 7, Number: 3}},
		"forgejo": {row: forgejoNumberedRun, want: forgeapi.RunRef{ID: 9, Number: 4}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{runsRoute: `{"total_count":1,"workflow_runs":[` + test.row + `]}`})
			page, err := h.client.ListRuns(t.Context(), testRef())
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("ListRuns = %d row(s), %v, want one", len(page.Items), err)
			}
			if got := page.Items[0].Ref; got != test.want {
				t.Errorf("ListRuns row Ref = %+v, want %+v", got, test.want)
			}
			if got := page.Items[0].Trigger; got != forgeapi.TriggerPullRequest {
				t.Errorf("ListRuns row Trigger = %v, want %v", got, forgeapi.TriggerPullRequest)
			}
		})
	}
}

// TestTheRunEventMapsTotally holds the trigger to one table over both products'
// spellings in every release this family serves: the webhook event types
// (modules/webhook/type.go), the names their Event method serializes, which Forgejo
// publishes as a run's event (services/convert/action.go), and the workflow on: keys
// both accept (modules/actions/github.go).
func TestTheRunEventMapsTotally(t *testing.T) {
	want := map[string]forgeapi.RunTrigger{"push": forgeapi.TriggerPush, "schedule": forgeapi.TriggerSchedule, "workflow_dispatch": forgeapi.TriggerManual, "": forgeapi.TriggerUnknown}
	for _, event := range []string{
		"pull_request", "pull_request_target", "pull_request_review", "pull_request_review_comment", "pull_request_sync",
		"pull_request_assign", "pull_request_label", "pull_request_milestone", "pull_request_comment",
		"pull_request_review_approved", "pull_request_review_rejected", "pull_request_review_request",
		"pull_request_approved", "pull_request_rejected",
	} {
		want[event] = forgeapi.TriggerPullRequest
	}
	for _, event := range []string{
		"create", "delete", "fork", "issues", "issue_assign", "issue_label", "issue_milestone", "issue_comment",
		"wiki", "gollum", "repository", "release", "package", "registry_package", "status", "workflow_run", "workflow_job",
		"action_run_failure", "action_run_recover", "action_run_success",
		"workflow_run_blocked", "workflow_run_cancelled", "workflow_run_failure", "workflow_run_running",
		"workflow_run_skipped", "workflow_run_success", "workflow_run_waiting",
		"workflow_job_blocked", "workflow_job_cancelled", "workflow_job_failure", "workflow_job_running",
		"workflow_job_skipped", "workflow_job_success", "workflow_job_waiting",
	} {
		want[event] = forgeapi.TriggerOther
	}
	for event, trigger := range want {
		h := newHarness(t, nil)
		if got := h.client.normalizeRun(&wireRun{Event: event}, testRef()).Trigger; got != trigger {
			t.Errorf("normalizeRun(event %q) = trigger %v, want %v", event, got, trigger)
		}
		if n := h.spy.times("UnknownEnumValue"); n != 0 {
			t.Errorf("normalizeRun(event %q) counted %d unmapped value(s), want 0", event, n)
		}
	}
	h := newHarness(t, nil)
	if got := h.client.normalizeRun(&wireRun{TriggerEvent: "pull_request", Event: "teleport"}, testRef()).Trigger; got != forgeapi.TriggerPullRequest {
		t.Errorf("normalizeRun(trigger_event beside event) = trigger %v, want %v: the workflow's on: key decides", got, forgeapi.TriggerPullRequest)
	}
	if got := h.client.normalizeRun(&wireRun{TriggerEvent: "pull_request_approved", Event: "teleport"}, testRef()).Trigger; got != forgeapi.TriggerPullRequest {
		t.Errorf("normalizeRun(trigger_event %q) = trigger %v, want %v: Forgejo copies the serialized event name into it", "pull_request_approved", got, forgeapi.TriggerPullRequest)
	}
	if got := h.client.normalizeRun(&wireRun{Event: "teleport"}, testRef()).Trigger; got != forgeapi.TriggerUnknown {
		t.Errorf("normalizeRun(an undeclared event) = trigger %v, want %v", got, forgeapi.TriggerUnknown)
	}
	if n := h.spy.times("UnknownEnumValue"); n != 1 {
		t.Errorf("normalizeRun(an undeclared event) counted %d unmapped value(s), want 1", n)
	}
	if !h.logged("teleport") {
		t.Error("normalizeRun(an undeclared event) logged no line naming the value, want one")
	}
}

// sinceRun is one run row of the listing, created at created.
func sinceRun(id int, created time.Time) string {
	return `{"id":` + strconv.Itoa(id) + `,"created_at":"` + created.Format(time.RFC3339) + `"}`
}

// sinceRuns is a run listing body stating total, holding rows.
func sinceRuns(total int, rows ...string) string {
	return `{"total_count":` + strconv.Itoa(total) + `,"workflow_runs":[` + strings.Join(rows, ",") + `]}`
}

// Neither product's run route takes a time filter, so every row of a page is held to
// the bound, and a page still holding a run created at or after it goes on to the next.
func TestASinceBoundHoldsEveryPageItReads(t *testing.T) {
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, map[string]string{runsRoute: sinceRuns(120,
		sinceRun(3, bound.Add(48*time.Hour)), sinceRun(2, bound.Add(-time.Second)), sinceRun(1, bound.Add(24*time.Hour)))})
	page, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithPageBound(3), forgeapi.WithSince(bound))
	if err != nil {
		t.Fatalf("ListRuns(WithSince(%v)) = %v, want the page", bound, err)
	}
	if len(page.Items) != 2 || page.Items[0].Ref.ID != 3 || page.Items[1].Ref.ID != 1 {
		t.Errorf("ListRuns(WithSince(%v)) answered %+v, want runs 3 and 1: every row of the page is held to the bound", bound, page.Items)
	}
	if page.Next == "" || page.Partial != nil {
		t.Errorf("ListRuns(WithSince(%v)) = Next %q, Partial %+v, want the continuation the total states: the page held a run inside the bound", bound, page.Next, page.Partial)
	}
	for _, key := range []string{"created", "since", "created_after"} {
		if got := h.instance.query(runsRoute, key); got != "" {
			t.Errorf("ListRuns(WithSince) sent %s=%q, want no time filter: neither product's route declares one", key, got)
		}
	}
}

// Both products answer a repository's runs newest id first (Gitea orders by the
// repository's run index, co-monotonic with id, models/actions/run_list.go
// FindRunOptions.ToOrders; Forgejo by id), and stamp a run's creation once, at insert
// (models/actions/run.go, Created xorm:"created"), so a page whose every run predates
// the bound by more than the skew a host clock can step back ends the walk complete.
func TestASinceBoundEndsTheWalkAtAPageWhollyOlderThanTheBound(t *testing.T) {
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, map[string]string{runsRoute: sinceRuns(120,
		sinceRun(5, bound.Add(-48*time.Hour)), sinceRun(4, bound.Add(-72*time.Hour)))})
	page, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithPageBound(2), forgeapi.WithSince(bound))
	if err != nil {
		t.Fatalf("ListRuns(WithSince(%v)) over a page wholly before the bound = %v, want the page", bound, err)
	}
	if len(page.Items) != 0 || page.Next != "" || page.Partial != nil {
		t.Errorf("ListRuns(WithSince(%v)) over a page wholly before the bound = %d row(s), Next %q, Partial %+v, want none, no continuation and no partial: no later page holds a newer run", bound, len(page.Items), page.Next, page.Partial)
	}
}

// A run stamped up to a minute before the bound may sit on a page above a newer one
// whose host clock stepped back, so such a page goes on to the next.
func TestASinceBoundWalksOnPastAPageOlderThanTheBoundWithinTheSkew(t *testing.T) {
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := newHarness(t, map[string]string{runsRoute: sinceRuns(3,
		sinceRun(9, bound.Add(-time.Minute)), sinceRun(8, bound.Add(-2*time.Hour)))})
	first, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithPageBound(2), forgeapi.WithSince(bound))
	if err != nil {
		t.Fatalf("ListRuns(WithSince(%v)) = %v, want the page", bound, err)
	}
	if len(first.Items) != 0 || first.Next == "" {
		t.Fatalf("ListRuns(WithSince(%v)) over a page whose newest run is a minute before the bound = %d row(s), Next %q, want none and the continuation", bound, len(first.Items), first.Next)
	}
	h.instance.serve(runsRoute, sinceRuns(3, sinceRun(7, bound.Add(time.Hour))))
	second, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithPageBound(2), forgeapi.WithSince(bound), forgeapi.WithAfter(first.Next))
	if err != nil || len(second.Items) != 1 || second.Items[0].Ref.ID != 7 {
		t.Errorf("ListRuns(WithSince(%v), page 2) = %+v, %v, want run 7, created after the bound", bound, second.Items, err)
	}
}

// A long history ends at the first page past the bound rather than at the stated
// total: two requests for a repository holding thousands of older runs.
func TestASinceBoundOverALongHistoryEndsAtTheFirstPageWhollyOlder(t *testing.T) {
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	page1 := []string{sinceRun(2100, bound.Add(time.Hour))}
	page2 := make([]string, 0, 50)
	for i := range 49 {
		page1 = append(page1, sinceRun(2099-i, bound.Add(-time.Duration(i+1)*time.Hour)))
	}
	for i := range 50 {
		page2 = append(page2, sinceRun(2050-i, bound.Add(-time.Duration(i+50)*time.Hour)))
	}
	h := newHarness(t, map[string]string{runsRoute: sinceRuns(2100, page1...)})
	first, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithPageBound(50), forgeapi.WithSince(bound))
	if err != nil || len(first.Items) != 1 || first.Next == "" {
		t.Fatalf("ListRuns(WithSince(%v), page 1) = %d row(s), Next %q, %v, want run 2100 and the continuation", bound, len(first.Items), first.Next, err)
	}
	h.instance.serve(runsRoute, sinceRuns(2100, page2...))
	second, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithPageBound(50), forgeapi.WithSince(bound), forgeapi.WithAfter(first.Next))
	if err != nil || len(second.Items) != 0 || second.Next != "" || second.Partial != nil {
		t.Errorf("ListRuns(WithSince(%v), page 2) = %d row(s), Next %q, Partial %+v, %v, want the walk ended complete", bound, len(second.Items), second.Next, second.Partial, err)
	}
	if got := len(h.instance.arrived()); got != 2 {
		t.Errorf("ListRuns(WithSince) over 2,100 runs sent %d request(s), want 2: %v", got, h.instance.arrived())
	}
}

// Both products stamp a run's creation at whole seconds (timeutil.TimeStamp), so a run
// stamped in the bound's own second may have been created after it, and is answered.
func TestARunStampedInTheBoundsOwnSecondIsAnswered(t *testing.T) {
	h := newHarness(t, map[string]string{runsRoute: `{"total_count":2,"workflow_runs":[` +
		`{"id":3,"created_at":"2026-01-01T12:00:01Z"},{"id":2,"created_at":"2026-01-01T12:00:00Z"}]}`})
	bound := time.Date(2026, 1, 1, 12, 0, 0, 300_000_000, time.UTC)
	page, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithSince(bound))
	if err != nil || len(page.Items) != 2 {
		t.Errorf("ListRuns(WithSince(%v)) = %+v, %v, want both runs: the one stamped 12:00:00 may postdate the bound", bound, page.Items, err)
	}
}

// Gitea's ActionWorkflowRun carries no created_at before v28: modules/structs/repo_actions.go
// at v1.25.0 through v1.26.4 ends at started_at and completed_at.
const giteaRunWithoutCreatedAt = `{"id":7,"run_number":3,"event":"pull_request","path":"ci.yml@refs/heads/main",` +
	`"status":"completed","conclusion":"success","started_at":"2026-01-01T00:00:00Z","completed_at":"2026-01-01T00:01:00Z"}`

func TestASinceBoundRefusesARunThatCarriesNoCreationTime(t *testing.T) {
	h := newHarness(t, map[string]string{runsRoute: `{"total_count":1,"workflow_runs":[` + giteaRunWithoutCreatedAt + `]}`})
	page, err := h.client.ListRuns(t.Context(), testRef())
	if err != nil || len(page.Items) != 1 || !page.Items[0].CreatedAt.IsZero() {
		t.Fatalf("ListRuns(no bound) over a row with no created_at = %+v, %v, want the row with a zero CreatedAt", page.Items, err)
	}
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = h.client.ListRuns(t.Context(), testRef(), forgeapi.WithSince(bound))
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRunUndated || fe.Kind != forgeapi.KindUpstream {
		t.Errorf("ListRuns(WithSince(%v)) over a row with no created_at = %v, want code %q of kind %v: the bound cannot be held, and an empty page would claim no run", bound, err, forgeapi.CodeRunUndated, forgeapi.KindUpstream)
	}
}

// Only the missing creation time is run_undated: every other malformed run row,
// and a listing with no total to count by, stays validation under a bound.
func TestASinceBoundKeepsEveryOtherMalformedRunAsValidation(t *testing.T) {
	bound := forgeapi.WithSince(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for name, body := range map[string]string{
		"no_id":           `{"total_count":1,"workflow_runs":[{"run_number":9,"created_at":"2026-01-02T00:00:00Z"}]}`,
		"negative_number": `{"total_count":1,"workflow_runs":[{"id":7,"run_number":-1,"created_at":"2026-01-02T00:00:00Z"}]}`,
		"no_total_count":  `{"workflow_runs":[{"id":7,"run_number":3,"created_at":"2026-01-02T00:00:00Z"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{runsRoute: body})
			_, err := h.client.ListRuns(t.Context(), testRef(), bound)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeValidation || fe.Kind != forgeapi.KindUpstream {
				t.Errorf("ListRuns(WithSince) over a run listing with %s = %v, want code %q of kind %v", name, err, forgeapi.CodeValidation, forgeapi.KindUpstream)
			}
		})
	}
}

// RunRef.ID is a run's identity, so a row the product sent without one is refused
// rather than published as run 0.
func TestARunWithNoIdentityIsRefused(t *testing.T) {
	for name, row := range map[string]string{
		"missing": `{"run_number":9,"created_at":"2026-01-01T00:00:00Z"}`,
		"zero":    `{"id":0,"run_number":9,"created_at":"2026-01-01T00:00:00Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{runsRoute: `{"total_count":1,"workflow_runs":[` + row + `]}`})
			_, err := h.client.ListRuns(t.Context(), testRef())
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeValidation || fe.Kind != forgeapi.KindUpstream {
				t.Errorf("ListRuns over a row with an id %s = %v, want code %q of kind %v", name, err, forgeapi.CodeValidation, forgeapi.KindUpstream)
			}
		})
	}
}

func TestASinceBoundIsRefusedOnEveryListButTheRunListing(t *testing.T) {
	h := newHarness(t, nil)
	since := forgeapi.WithSince(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for name, call := range map[string]func() error{
		"ListRepos":    func() error { _, err := h.client.ListRepos(t.Context(), since); return err },
		"ListPRs":      func() error { _, err := h.client.ListPRs(t.Context(), testRef(), since); return err },
		"ListMyPRs":    func() error { _, err := h.client.ListMyPRs(t.Context(), since); return err },
		"ListIssues":   func() error { _, err := h.client.ListIssues(t.Context(), testRef(), since); return err },
		"ListMyIssues": func() error { _, err := h.client.ListMyIssues(t.Context(), since); return err },
		"ListReleases": func() error { _, err := h.client.ListReleases(t.Context(), testRef(), since); return err },
		"ListLabels":   func() error { _, err := h.client.ListLabels(t.Context(), testRef(), since); return err },
	} {
		t.Run(name, func(t *testing.T) {
			var fe *forgeapi.Error
			if err := call(); !asForgeError(err, &fe) || fe.Code != forgeapi.CodeListSinceInvalid {
				t.Errorf("%s(WithSince) = %v, want code %q", name, err, forgeapi.CodeListSinceInvalid)
			}
		})
	}
	if got := len(h.instance.arrived()); got != 0 {
		t.Errorf("the refused lists sent %d request(s), want 0: %v", got, h.instance.arrived())
	}
}

const ownerReposRoute = "GET /api/v1/users/example/repos"

// Both products page the owner's repositories in SQL and then drop the rows the
// caller has no unit access to (routers/api/v1/user/repo.go listUserRepos), so a short
// page can have more behind it and the walk continues from X-Total-Count.
func TestAnOwnerScopedRepositoryWalkContinuesFromTheStatedTotal(t *testing.T) {
	h := newHarness(t, map[string]string{ownerReposRoute: "[" + repoRow + "]"})
	h.instance.mu.Lock()
	h.instance.headers.Set("X-Total-Count", "3")
	h.instance.mu.Unlock()
	page, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("example"), forgeapi.WithPageBound(2))
	if err != nil {
		t.Fatalf("ListRepos(WithOwner) = %v, want the page", err)
	}
	if got := h.instance.arrived(); len(got) != 1 || got[0] != ownerReposRoute {
		t.Errorf("ListRepos(WithOwner) sent %v, want the owner's route alone", got)
	}
	if len(page.Items) != 1 || page.Items[0].Ref.Selector != testSelector {
		t.Fatalf("ListRepos(WithOwner) answered %+v, want the owner's repository", page.Items)
	}
	if page.Next == "" {
		t.Fatal("ListRepos(WithOwner) over a short page of a stated 3 = no Next, want the continuation: 1 page of 2 is under the total")
	}
	if _, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("example"), forgeapi.WithPageBound(2), forgeapi.WithAfter(page.Next)); err != nil {
		t.Fatalf("ListRepos(WithOwner, WithAfter) = %v, want page 2", err)
	}
	if got := h.instance.query(ownerReposRoute, keyPage); got != "2" {
		t.Errorf("ListRepos(WithOwner, WithAfter) sent page %q, want 2", got)
	}
	h.instance.mu.Lock()
	h.instance.headers.Set("X-Total-Count", "2")
	h.instance.mu.Unlock()
	if last, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("example"), forgeapi.WithPageBound(2)); err != nil || last.Next != "" {
		t.Errorf("ListRepos(WithOwner) over a page whose bound reaches the stated 2 = Next %q, %v, want none", last.Next, err)
	}
	h.instance.mu.Lock()
	h.instance.headers.Del("X-Total-Count")
	h.instance.mu.Unlock()
	if _, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("example")); err == nil {
		t.Error("ListRepos(WithOwner) over an answer with no X-Total-Count = nil, want a refusal: the total decides the continuation")
	}
}

func TestAnOwnerTheInstanceDoesNotResolveIsOwnerUnresolved(t *testing.T) {
	h := newRefusingHarness(t, http.StatusNotFound)
	_, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("no-such-owner"))
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeOwnerUnresolved || fe.Kind != forgeapi.KindNotFound || fe.Status != http.StatusNotFound {
		t.Errorf("ListRepos(WithOwner(an owner the instance does not hold)) = %v, want code %q of kind %v at 404", err, forgeapi.CodeOwnerUnresolved, forgeapi.KindNotFound)
	}
}

// The owner code describes an owner the call named, so each owner-scoped route's
// refusal for an unresolved owner carries it under WithOwner and the same status on
// the default scope carries the ordinary cell.
func TestTheOwnerCodeRidesOnlyARequestAnOwnerScoped(t *testing.T) {
	for name, test := range map[string]struct {
		call   func(context.Context, *Client, ...forgeapi.ListOption) error
		status int
		kind   forgeapi.ErrorKind
		code   string
	}{
		"ListRepos": {
			call: func(ctx context.Context, c *Client, opts ...forgeapi.ListOption) error {
				_, err := c.ListRepos(ctx, opts...)
				return err
			},
			status: http.StatusNotFound, kind: forgeapi.KindNotFound, code: forgeapi.CodeRepoOrPRNotVisible,
		},
		"ListMyPRs": {
			call: func(ctx context.Context, c *Client, opts ...forgeapi.ListOption) error {
				_, err := c.ListMyPRs(ctx, opts...)
				return err
			},
			status: http.StatusBadRequest, kind: forgeapi.KindUpstream,
		},
		"ListMyIssues": {
			call: func(ctx context.Context, c *Client, opts ...forgeapi.ListOption) error {
				_, err := c.ListMyIssues(ctx, opts...)
				return err
			},
			status: http.StatusBadRequest, kind: forgeapi.KindUpstream,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var fe *forgeapi.Error
			scoped := test.call(t.Context(), newRefusingHarness(t, test.status).client, forgeapi.WithOwner("no-such-owner"))
			if !asForgeError(scoped, &fe) || fe.Code != forgeapi.CodeOwnerUnresolved || fe.Kind != forgeapi.KindNotFound {
				t.Errorf("%s(WithOwner(an owner the instance does not hold)) over %d = %v, want code %q of kind %v", name, test.status, scoped, forgeapi.CodeOwnerUnresolved, forgeapi.KindNotFound)
			}
			unscoped := test.call(t.Context(), newRefusingHarness(t, test.status).client)
			if !asForgeError(unscoped, &fe) || fe.Code != test.code || fe.Kind != test.kind {
				t.Errorf("%s() over %d = %v, want code %q of kind %v: the call named no owner", name, test.status, unscoped, test.code, test.kind)
			}
		})
	}
}

func TestTheConnectionNamesTheProductItsWitnessNames(t *testing.T) {
	for name, test := range map[string]struct {
		version, header, document string
		want                      forgeapi.Product
	}{
		"the_marker":            {version: "16.0.0+gitea-1.22.0", want: forgeapi.ProductForgejo},
		"a_gitea_header":        {version: "1.27.0", header: headerGiteaVersion, want: forgeapi.ProductGitea},
		"a_forgejo_header":      {version: "16.0.0", header: headerForgejoVersion, want: forgeapi.ProductForgejo},
		"a_gitea_document":      {version: "1.27.0", document: giteaDoc, want: forgeapi.ProductGitea},
		"a_forgejo_document":    {version: "16.0.0", document: forgejoDoc, want: forgeapi.ProductForgejo},
		"the_marker_over_title": {version: "16.0.0+gitea-1.22.0", document: giteaDoc, want: forgeapi.ProductForgejo},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarnessOver(t, detectServer(t, test.version, test.header, test.document), time.Now)
			caps, err := h.client.ConnectionCaps(t.Context())
			if err != nil {
				t.Fatalf("ConnectionCaps = %v, want the capabilities", err)
			}
			if caps.Product != test.want {
				t.Errorf("ConnectionCaps = product %v, want %v", caps.Product, test.want)
			}
		})
	}
}

func TestAnOwnerRowCarriesWhatTheDefaultRowCarries(t *testing.T) {
	h := newHarness(t, map[string]string{ownerReposRoute: "[" + repoRow + "]", "GET /api/v1/user/repos": "[" + repoRow + "]"})
	h.instance.mu.Lock()
	h.instance.headers.Set("X-Total-Count", "1")
	h.instance.mu.Unlock()
	owned, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("example"))
	if err != nil || len(owned.Items) != 1 {
		t.Fatalf("ListRepos(WithOwner) = %d row(s), %v, want one", len(owned.Items), err)
	}
	mine, err := h.client.ListRepos(t.Context())
	if err != nil || len(mine.Items) != 1 {
		t.Fatalf("ListRepos = %d row(s), %v, want one", len(mine.Items), err)
	}
	if got, want := owned.Items[0].Ref, mine.Items[0].Ref; got != want {
		t.Errorf("ListRepos(WithOwner) row Ref = %+v, want %+v: both routes answer the same repository record", got, want)
	}
	if strings.Contains(h.instance.query(ownerReposRoute, "owner"), "example") {
		t.Error("ListRepos(WithOwner) sent the owner as a query parameter, want it in the route")
	}
}
