package github

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// docsWorkflowRun is the workflow run GitHub's REST reference shows as the example
// response of "List workflow runs for a repository" (API version 2026-03-10),
// trimmed to the keys the row reads: the run's id, its run_number and its event.
const docsWorkflowRun = `{"id":30433642,"name":"Build","head_branch":"main","head_sha":"acb5820ced9479c074f688cc328bf03f341a511d",` +
	`"run_number":562,"event":"push","status":"queued","conclusion":null,` +
	`"html_url":"https://github.com/octo-org/octo-repo/actions/runs/30433642",` +
	`"created_at":"2020-01-22T19:33:08Z","updated_at":"2020-01-22T19:33:08Z"}`

func TestARunCarriesItsIdentityAndWhatStartedIt(t *testing.T) {
	h := newHarness(t, map[string]string{runsRoute: `{"total_count":1,"workflow_runs":[` + docsWorkflowRun + `]}`})
	page, err := h.client.ListRuns(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListRuns = %v, want the page", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListRuns answered %d row(s), want 1", len(page.Items))
	}
	run := page.Items[0]
	if want := (forgeapi.RunRef{ID: 30433642, Number: 562}); run.Ref != want {
		t.Errorf("ListRuns row Ref = %+v, want %+v: id is what the run routes take, run_number its sequence", run.Ref, want)
	}
	if run.Trigger != forgeapi.TriggerPush {
		t.Errorf("ListRuns row Trigger = %v, want %v from event push", run.Trigger, forgeapi.TriggerPush)
	}
}

// TestTheWorkflowRunEventMapsTotally holds the trigger to a table over every event the
// "Events that trigger workflows" page lists, plus dynamic, the event GitHub-managed
// workflows report.
func TestTheWorkflowRunEventMapsTotally(t *testing.T) {
	want := map[string]forgeapi.RunTrigger{
		"push":                        forgeapi.TriggerPush,
		"pull_request":                forgeapi.TriggerPullRequest,
		"pull_request_target":         forgeapi.TriggerPullRequest,
		"pull_request_review":         forgeapi.TriggerPullRequest,
		"pull_request_review_comment": forgeapi.TriggerPullRequest,
		"merge_group":                 forgeapi.TriggerPullRequest,
		"schedule":                    forgeapi.TriggerSchedule,
		"workflow_dispatch":           forgeapi.TriggerManual,
		"repository_dispatch":         forgeapi.TriggerManual,
	}
	for _, other := range []string{
		"branch_protection_rule", "check_run", "check_suite", "create", "delete", "deployment",
		"deployment_status", "discussion", "discussion_comment", "fork", "gollum", "image_version",
		"issue_comment", "issues", "label", "milestone", "page_build", "public", "registry_package",
		"release", "status", "watch", "workflow_call", "workflow_run", "dynamic",
	} {
		want[other] = forgeapi.TriggerOther
	}
	want[""] = forgeapi.TriggerUnknown
	for event, trigger := range want {
		h := newHarness(t, nil)
		run := h.client.normalizeRun(&restWorkflowRun{Event: event}, testRef())
		if run.Trigger != trigger {
			t.Errorf("normalizeRun(event %q) = trigger %v, want %v", event, run.Trigger, trigger)
		}
		if n := h.spy.times("UnknownEnumValue:workflow run event (rest)"); n != 0 {
			t.Errorf("normalizeRun(event %q) counted %d unmapped value(s), want 0: the event is documented", event, n)
		}
	}
	h := newHarness(t, nil)
	if run := h.client.normalizeRun(&restWorkflowRun{Event: "teleport"}, testRef()); run.Trigger != forgeapi.TriggerUnknown {
		t.Errorf("normalizeRun(an undocumented event) = trigger %v, want %v", run.Trigger, forgeapi.TriggerUnknown)
	}
	if n := h.spy.times("UnknownEnumValue:workflow run event (rest)"); n != 1 {
		t.Errorf("normalizeRun(an undocumented event) counted %d unmapped value(s), want 1", n)
	}
	if !h.logged("teleport") {
		t.Error("normalizeRun(an undocumented event) logged no line naming the value, want one")
	}
}

// The runs route filters created with the search syntax at whole seconds, and GitHub
// stamps a run's creation at whole seconds, so the bound is read at its second: a run
// stamped in the bound's own second may postdate it and is answered.
func TestASinceBoundIsSentAsTheCreatedFilterAndHeldAtWholeSeconds(t *testing.T) {
	h := newHarness(t, map[string]string{runsRoute: `{"total_count":3,"workflow_runs":[` +
		`{"id":3,"created_at":"2026-01-01T00:00:01Z"},{"id":2,"created_at":"2026-01-01T00:00:00Z"},{"id":1,"created_at":"2025-12-31T23:59:59Z"}]}`})
	bound := time.Date(2026, 1, 1, 0, 0, 0, 500_000_000, time.UTC)
	page, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithSince(bound))
	if err != nil {
		t.Fatalf("ListRuns(WithSince(%v)) = %v, want the page", bound, err)
	}
	if got, want := h.instance.query(runsRoute, "created"), ">=2026-01-01T00:00:00+00:00"; got != want {
		t.Errorf("ListRuns(WithSince(%v)) sent created %q, want %q", bound, got, want)
	}
	if len(page.Items) != 2 || page.Items[0].Ref.ID != 3 || page.Items[1].Ref.ID != 2 {
		t.Errorf("ListRuns(WithSince(%v)) answered %+v, want runs 3 and 2: run 1 is stamped before the bound's second", bound, page.Items)
	}
	if page.Partial != nil {
		t.Errorf("ListRuns(WithSince(%v)) = Partial %+v, want none: the walk was served its whole total", bound, page.Partial)
	}
}

// RunRef.ID is a run's identity, so a row the route sent without one is refused rather
// than published as run 0.
func TestARunWithNoIdentityIsRefused(t *testing.T) {
	for name, row := range map[string]string{
		"missing": `{"run_number":9,"created_at":"2026-01-01T00:00:00Z"}`,
		"zero":    `{"id":0,"run_number":9,"created_at":"2026-01-01T00:00:00Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{runsRoute: `{"total_count":1,"workflow_runs":[` + row + `]}`})
			_, err := h.client.ListRuns(t.Context(), testRef())
			var fe *forgeapi.Error
			if !errors.As(err, &fe) || fe.Code != forgeapi.CodeValidation || fe.Kind != forgeapi.KindUpstream {
				t.Errorf("ListRuns over a row with an id %s = %v, want code %q of kind %v", name, err, forgeapi.CodeValidation, forgeapi.KindUpstream)
			}
		})
	}
}

// Every workflow-run row carries created_at, so under a since bound a row without one
// is malformed, refused rather than dropped as older and answered as a page holding no
// run.
func TestABoundedRunWithNoCreationTimeIsRefused(t *testing.T) {
	h := newHarness(t, map[string]string{runsRoute: `{"total_count":1,"workflow_runs":[{"id":1}]}`})
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	page, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithSince(bound))
	var fe *forgeapi.Error
	if !errors.As(err, &fe) || fe.Code != forgeapi.CodeValidation || fe.Kind != forgeapi.KindUpstream {
		t.Errorf("ListRuns(WithSince(%v)) over a row with no created_at = %+v, %v, want code %q of kind %v", bound, page, err, forgeapi.CodeValidation, forgeapi.KindUpstream)
	}
}

// A workflow run from a fork waiting for a maintainer's approval answers status
// completed with conclusion action_required and has run no job (read on public
// repositories with status=action_required, 2026-10-06), the state Gitea answers as
// waiting and Forgejo as blocked. On a check run the same conclusion is an app asking
// a person to act on a finished check, which GitHub groups with failure and timeout.
func TestAnApprovalWaitIsPendingOnARunAndFailingOnACheckRun(t *testing.T) {
	h := newHarness(t, nil)
	if run := h.client.normalizeRun(&restWorkflowRun{ID: 1, Status: "completed", Conclusion: "action_required"}, testRef()); run.State != forgeapi.CheckPending {
		t.Errorf("normalizeRun(completed, action_required) = state %v, want %v: the run waits for approval", run.State, forgeapi.CheckPending)
	}
	if got := h.client.runState("check run (rest)", "completed", "action_required"); got != forgeapi.CheckFailing {
		t.Errorf("runState(check run, completed, action_required) = %v, want %v", got, forgeapi.CheckFailing)
	}
}

func TestAnUnboundedRunListingSendsNoCreatedFilter(t *testing.T) {
	h := newHarness(t, map[string]string{runsRoute: `{"total_count":1,"workflow_runs":[{"id":1}]}`})
	if _, err := h.client.ListRuns(t.Context(), testRef()); err != nil {
		t.Fatalf("ListRuns = %v, want the page", err)
	}
	if _, sent := h.instance.queries[runsRoute]["created"]; sent {
		t.Errorf("ListRuns with no bound sent created %q, want none", h.instance.query(runsRoute, "created"))
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
	if got := h.instance.count(); got != 0 {
		t.Errorf("the refused lists sent %d request(s), want 0: %v", got, h.instance.arrived())
	}
}

// docsRepositoryOwner is one page of repositoryOwner.repositories as the public
// GraphQL schema types it: a nullable owner, a connection with pageInfo, and the
// Repository fields the row reads.
const docsRepositoryOwner = `"repositoryOwner":{"repositories":{"pageInfo":{"hasNextPage":true,"endCursor":"Y3Vyc29yOjE="},"nodes":[` +
	`{"nameWithOwner":"octo-org/octo-repo","description":"An example","url":"https://github.com/octo-org/octo-repo",` +
	`"isPrivate":true,"isArchived":true,"isFork":false,"updatedAt":"2026-01-02T00:00:00Z","defaultBranchRef":{"name":"main"},` +
	`"hasIssuesEnabled":true,"viewerPermission":"WRITE","mergeCommitAllowed":true,"squashMergeAllowed":false,"rebaseMergeAllowed":true}]}}`

func TestAnOwnerScopedRepositoryListingReadsTheOwnersRepositories(t *testing.T) {
	h := newHarness(t, map[string]string{documentRoute: documentEnvelope(docsRepositoryOwner)})
	page, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("octo-org"), forgeapi.WithPageBound(30))
	if err != nil {
		t.Fatalf("ListRepos(WithOwner) = %v, want the page", err)
	}
	if got := h.instance.count(); got != 1 {
		t.Errorf("ListRepos(WithOwner) sent %d request(s), want 1: %v", got, h.instance.arrived())
	}
	body := h.instance.body(documentRoute)
	for _, want := range []string{`"operationName":"RepoOwned"`, `"owner":"octo-org"`, `"first":30`, "ownerAffiliations: [OWNER]", "repositoryOwner(login: $owner)"} {
		if !strings.Contains(body, want) {
			t.Errorf("ListRepos(WithOwner) posted %s, want it to carry %s", body, want)
		}
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListRepos(WithOwner) answered %d row(s), want 1", len(page.Items))
	}
	row := page.Items[0]
	checks := []struct {
		field     string
		got, want any
	}{
		{"Ref", row.Ref, repoRef("octo-org/octo-repo")},
		{"Description", row.Description, "An example"},
		{"WebURL", row.WebURL, "https://github.com/octo-org/octo-repo"},
		{"CloneURL", row.CloneURL, "https://github.com/octo-org/octo-repo.git"},
		{"UpdatedAt", row.UpdatedAt, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{"Private", row.Private, true},
		{"Archived", row.Archived, true},
		{"Fork", row.Fork, false},
		{"DefaultBranch", row.Affordances.DefaultBranch, "main"},
		{"HasIssues", row.Affordances.HasIssues, forgeapi.SupportYes},
		{"CanPush", row.Affordances.CanPush, forgeapi.SupportYes},
		{"MergeTrain", row.Affordances.MergeTrain, forgeapi.SupportUnknown},
		{"MergeStrategies", strings.Join(row.Affordances.MergeStrategies, ","), "merge,rebase"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("ListRepos(WithOwner) row %s = %v, want %v", c.field, c.got, c.want)
		}
	}
	if page.Next == "" {
		t.Error("ListRepos(WithOwner) = no Next, want the continuation hasNextPage names")
	}
	h.instance.serve(documentRoute, documentEnvelope(strings.Replace(docsRepositoryOwner, `"hasNextPage":true`, `"hasNextPage":false`, 1)))
	if _, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("octo-org"), forgeapi.WithPageBound(30), forgeapi.WithAfter(page.Next)); err != nil {
		t.Fatalf("ListRepos(WithOwner, WithAfter(Next)) = %v, want the next page", err)
	}
	if body := h.instance.body(documentRoute); !strings.Contains(body, `"after":"Y3Vyc29yOjE="`) {
		t.Errorf("ListRepos(WithAfter(Next)) posted %s, want the end cursor the first page named", body)
	}
}

func TestTheViewerPermissionDecidesWhetherAnOwnedRowCanPush(t *testing.T) {
	for permission, want := range map[string]forgeapi.Support{
		`"ADMIN"`: forgeapi.SupportYes, `"MAINTAIN"`: forgeapi.SupportYes, `"WRITE"`: forgeapi.SupportYes,
		`"TRIAGE"`: forgeapi.SupportNo, `"TRIAGE_PLUS"`: forgeapi.SupportNo, `"READ"`: forgeapi.SupportNo, "null": forgeapi.SupportUnknown,
	} {
		h := newHarness(t, map[string]string{documentRoute: documentEnvelope(strings.Replace(docsRepositoryOwner, `"WRITE"`, permission, 1))})
		page, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("octo-org"))
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("ListRepos(WithOwner, viewerPermission %s) = %d row(s), %v, want one", permission, len(page.Items), err)
		}
		if got := page.Items[0].Affordances.CanPush; got != want {
			t.Errorf("ListRepos(WithOwner, viewerPermission %s) = CanPush %v, want %v", permission, got, want)
		}
	}
}

// An absent owner is a null repositoryOwner with no error on it, or a NOT_FOUND error
// placed on that field, with or without the rest of the data beside it.
func TestAnOwnerTheInstanceDoesNotResolveIsOwnerUnresolved(t *testing.T) {
	notFound := `"errors":[{"type":"NOT_FOUND","path":["repositoryOwner"],"message":"Could not resolve to a RepositoryOwner with the login of 'no-such-owner'."}]`
	for name, envelope := range map[string]string{
		"null_without_error":     documentEnvelope(`"repositoryOwner":null`),
		"null_beside_not_found":  strings.TrimSuffix(documentEnvelope(`"repositoryOwner":null`), "}") + "," + notFound + "}",
		"not_found_without_data": "{" + notFound + "}",
		"null_beside_an_error_elsewhere": strings.TrimSuffix(documentEnvelope(`"repositoryOwner":null`), "}") +
			`,"errors":[{"type":"MAX_NODE_LIMIT_EXCEEDED","path":["rateLimit"],"message":"budget field unavailable"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: envelope})
			_, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("no-such-owner"))
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeOwnerUnresolved || fe.Kind != forgeapi.KindNotFound || fe.Status != http.StatusOK {
				t.Fatalf("ListRepos(an owner answered by %s) = %v, want code %q of kind %v at status 200", envelope, err, forgeapi.CodeOwnerUnresolved, forgeapi.KindNotFound)
			}
			if h.client.isDegraded() {
				t.Error("the connection is degraded after an unresolved owner, want it not to be: the document answered")
			}
		})
	}
}

// GraphQL nulls a nullable field whose resolver failed and reports the failure beside
// the data, so a null owner carrying any other error is that failure, not an absence.
func TestAnOwnerWhoseResolverFailedIsNotOwnerUnresolved(t *testing.T) {
	envelope := strings.TrimSuffix(documentEnvelope(`"repositoryOwner":null`), "}") +
		`,"errors":[{"type":"FORBIDDEN","path":["repositoryOwner"],"message":"Resource not accessible by integration"}]}`
	h := newHarness(t, map[string]string{documentRoute: envelope})
	_, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("octo-org"))
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code == forgeapi.CodeOwnerUnresolved || fe.Kind != forgeapi.KindUpstream || fe.Status != http.StatusOK {
		t.Fatalf("ListRepos(an owner nulled beside a FORBIDDEN error) = %v, want a failure of kind %v at status 200 that is not %q", err, forgeapi.KindUpstream, forgeapi.CodeOwnerUnresolved)
	}
	if !strings.Contains(fe.Message, "Resource not accessible by integration") {
		t.Errorf("ListRepos(an owner nulled beside a FORBIDDEN error) message = %q, want the instance's error quoted", fe.Message)
	}
}

func TestAPathOwnerOnTheRepositoryListingIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	_, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("octo-org/team"))
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeListOwnerInvalid {
		t.Errorf("ListRepos(WithOwner(a path)) = %v, want code %q", err, forgeapi.CodeListOwnerInvalid)
	}
	if got := h.instance.count(); got != 0 {
		t.Errorf("ListRepos(WithOwner(a path)) sent %d request(s), want 0", got)
	}
}

func TestTheConnectionNamesItsProduct(t *testing.T) {
	h := newHarness(t, map[string]string{metaRoute: `{"installed_version":"3.20.0"}`, versionsRoute: servedVersions})
	withWitness(h.instance)
	caps, err := h.client.ConnectionCaps(t.Context())
	if err != nil {
		t.Fatalf("ConnectionCaps = %v, want the capabilities", err)
	}
	if caps.Product != forgeapi.ProductGitHub {
		t.Errorf("ConnectionCaps = product %v, want %v", caps.Product, forgeapi.ProductGitHub)
	}
}
