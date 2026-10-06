package github

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// The routes this family reaches on an instance whose API root is the appliance one,
// which is what a connection naming only a web base derives.
const (
	repoRouteBase  = "GET /api/v3/repos/example/example"
	pullsRoute     = "GET /api/v3/repos/example/example/pulls/1"
	issuesRoute    = "GET /api/v3/repos/example/example/issues"
	labelsRoute    = "GET /api/v3/repos/example/example/labels"
	releasesRoute  = "GET /api/v3/repos/example/example/releases"
	reposRoute     = "GET /api/v3/user/repos"
	runsRoute      = "GET /api/v3/repos/example/example/actions/runs"
	rerunRoute     = "POST /api/v3/repos/example/example/actions/runs/1/rerun-failed-jobs"
	mergeRoute     = "PUT /api/v3/repos/example/example/pulls/1/merge-async"
	statusRoute    = "GET /api/v3/repos/example/example/commits/" + testHeadSHA + "/status"
	checkRunsRoute = "GET /api/v3/repos/example/example/commits/" + testHeadSHA + "/check-runs"
)

// prReadEnvelope is the single-pull-request document's answer, built from the recorded
// repository node so the operation cases read the bytes the product sent.
func prReadEnvelope(t *testing.T) string {
	t.Helper()
	return documentEnvelope(`"repository":` + readCaptures(t).raw(t, "graphql_pr_read"))
}

// prListEnvelope is the list document's answer over one recorded row.
func prListEnvelope(t *testing.T, hasNext bool, cursor string) string {
	t.Helper()
	recorded := readCaptures(t)
	var node struct {
		PullRequest any `json:"pullRequest"`
	}
	recorded.row(t, "graphql_pr_read", &node)
	row := recorded.raw(t, "graphql_search_row")
	page := `{"hasNextPage":` + strconv.FormatBool(hasNext) + `,"endCursor":` + quoteOrNull(cursor) + `}`
	return documentEnvelope(`"repository":{"nameWithOwner":"example/example","viewerPermission":"WRITE",` +
		`"pullRequests":{"totalCount":1,"pageInfo":` + page + `,"nodes":[` + row + `]}}`)
}

// searchEnvelope is a cross-repository document's answer over one recorded row, on
// its last page.
func searchEnvelope(t *testing.T, row string) string {
	t.Helper()
	return documentEnvelope(`"search":{"issueCount":1,"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[` +
		readCaptures(t).raw(t, row) + `]}`)
}

func quoteOrNull(s string) string {
	if s == "" {
		return "null"
	}
	return `"` + s + `"`
}

// TestEveryOperationSpendsThePriceItPublishes drives each operation against an instance
// answering its own routes and holds what left the process to the figure the
// expectation table publishes for this product.
//
// It is the N+1 gate at the family's own grain: a list that folded per row, a read that
// resolved a capability, or a merge that read the pull request first would all answer
// correctly and fail here.
func TestEveryOperationSpendsThePriceItPublishes(t *testing.T) {
	recorded := readCaptures(t)
	routes := map[string]string{
		metaRoute:     `{"installed_version":"3.20.0"}`,
		versionsRoute: servedVersions,
		userRoute:     recorded.raw(t, "rest_user"),
		reposRoute:    `[` + recorded.raw(t, "rest_repo_listing_row") + `]`,
		repoRouteBase: recorded.raw(t, "rest_repo"),
		issuesRoute:   `[` + recorded.raw(t, "rest_issue") + `]`,
		labelsRoute:   `[{"name":"example-label","color":"ededed","description":"Example label."}]`,
		releasesRoute: `[` + recorded.raw(t, "rest_release") + `]`,
		runsRoute:     recorded.raw(t, "rest_workflow_runs"),
		rerunRoute:    `{}`,
		mergeRoute:    recorded.raw(t, "rest_merge_accepted"),
		documentRoute: prReadEnvelope(t),
		"POST /api/v3/repos/example/example/pulls":     recorded.raw(t, "rest_pull"),
		"PATCH /api/v3/repos/example/example/pulls/1":  recorded.raw(t, "rest_pull"),
		"POST /api/v3/repos/example/example/issues":    recorded.raw(t, "rest_issue"),
		"PATCH /api/v3/repos/example/example/issues/1": recorded.raw(t, "rest_issue"),
		"POST /api/v3/repos/example/example/releases":  recorded.raw(t, "rest_release"),
	}
	for _, test := range []struct {
		call func(*harness) error
		name string
		want int
	}{
		{name: "Identity.Whoami", want: 1, call: func(h *harness) error {
			_, err := h.client.Whoami(t.Context())
			return err
		}},
		{name: "Repos.ListRepos", want: 1, call: func(h *harness) error {
			_, err := h.client.ListRepos(t.Context())
			return err
		}},
		{name: "PullRequests.ListPRs", want: 1, call: func(h *harness) error {
			h.instance.serve(documentRoute, prListEnvelope(t, false, ""))
			_, err := h.client.ListPRs(t.Context(), testRef())
			return err
		}},
		{name: "PullRequests.ListMyPRs", want: 1, call: func(h *harness) error {
			h.instance.serve(documentRoute, searchEnvelope(t, "graphql_search_row"))
			_, err := h.client.ListMyPRs(t.Context())
			return err
		}},
		{name: "PullRequests.ListMyPRs_under_an_owner", want: 1, call: func(h *harness) error {
			h.instance.serve(documentRoute, searchEnvelope(t, "graphql_search_row"))
			_, err := h.client.ListMyPRs(t.Context(), forgeapi.WithOwner(testOwner))
			return err
		}},
		{name: "Issues.ListMyIssues", want: 1, call: func(h *harness) error {
			h.instance.serve(documentRoute, searchEnvelope(t, "graphql_issue_search_row"))
			_, err := h.client.ListMyIssues(t.Context())
			return err
		}},
		{name: "Issues.ListMyIssues_under_an_owner", want: 1, call: func(h *harness) error {
			h.instance.serve(documentRoute, searchEnvelope(t, "graphql_issue_search_row"))
			_, err := h.client.ListMyIssues(t.Context(), forgeapi.WithOwner(testOwner))
			return err
		}},
		{name: "Checks.ListRuns", want: 1, call: func(h *harness) error {
			_, err := h.client.ListRuns(t.Context(), testRef())
			return err
		}},
		{name: "PullRequests.ReadPR", want: 1, call: func(h *harness) error {
			_, err := h.client.ReadPR(t.Context(), testRef(), testPR())
			return err
		}},
		{name: "PullRequests.CreatePR", want: 1, call: func(h *harness) error {
			_, err := h.client.CreatePR(t.Context(), testRef(), forgeapi.NewPullRequest{Title: "t", SourceBranch: "a", TargetBranch: "b"})
			return err
		}},
		{name: "PullRequests.ClosePR", want: 1, call: func(h *harness) error {
			_, err := h.client.ClosePR(t.Context(), testRef(), testPR())
			return err
		}},
		{name: "PullRequests.ReopenPR", want: 1, call: func(h *harness) error {
			_, err := h.client.ReopenPR(t.Context(), testRef(), testPR())
			return err
		}},
		{name: "PullRequests.RerunFailedChecks", want: 2, call: func(h *harness) error {
			return h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA)
		}},
		{name: "Merges.MergePR", want: 1, call: func(h *harness) error {
			_, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA})
			return err
		}},
		{name: "Merges.MergeStatus", want: 1, call: func(h *harness) error {
			_, err := h.client.MergeStatus(t.Context(), testRef(), testPR())
			return err
		}},
		{name: "Checks.CommitStatus", want: 1, call: func(h *harness) error {
			h.instance.serve(documentRoute, documentEnvelope(`"repository":{"nameWithOwner":"example/example","object":{"oid":"`+testHeadSHA+`","statusCheckRollup":{"state":"SUCCESS","contexts":{"totalCount":1,"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[{"__typename":"StatusContext","context":"example/build","state":"SUCCESS"}]}}}}`))
			_, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA)
			return err
		}},
		{name: "Issues.ListIssues", want: 1, call: func(h *harness) error {
			_, err := h.client.ListIssues(t.Context(), testRef())
			return err
		}},
		{name: "Issues.CreateIssue", want: 1, call: func(h *harness) error {
			_, err := h.client.CreateIssue(t.Context(), testRef(), forgeapi.NewIssue{Title: "t"})
			return err
		}},
		{name: "Issues.CloseIssue", want: 1, call: func(h *harness) error {
			_, err := h.client.CloseIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 1})
			return err
		}},
		{name: "Capabilities.ConnectionCaps", want: 2, call: func(h *harness) error {
			_, err := h.client.ConnectionCaps(t.Context())
			return err
		}},
		{name: "Capabilities.GrantCaps", want: 1, call: func(h *harness) error {
			_, err := h.client.GrantCaps(t.Context())
			return err
		}},
		{name: "Capabilities.RepoAffordances", want: 1, call: func(h *harness) error {
			_, err := h.client.RepoAffordances(t.Context(), testRef())
			return err
		}},
		{name: "Governor.BudgetState", want: 0, call: func(h *harness) error {
			h.client.BudgetState()
			return nil
		}},
		{name: "Releases.ListReleases", want: 1, call: func(h *harness) error {
			_, err := h.client.ListReleases(t.Context(), testRef())
			return err
		}},
		{name: "Releases.CreateRelease", want: 1, call: func(h *harness) error {
			_, err := h.client.CreateRelease(t.Context(), testRef(), forgeapi.NewRelease{TagName: "v1.0.0"})
			return err
		}},
		{name: "Labels.ListLabels", want: 1, call: func(h *harness) error {
			_, err := h.client.ListLabels(t.Context(), testRef())
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, routes)
			withWitness(h.instance)
			if err := test.call(h); err != nil {
				t.Fatalf("%s = %v, want nil: the requests that arrived were %v", test.name, err, h.instance.arrived())
			}
			if got := h.instance.count(); got != test.want {
				t.Errorf("%s sent %d request(s), want %d: %v", test.name, got, test.want, h.instance.arrived())
			}
			for i, token := range h.instance.credentials() {
				if want := "Bearer " + testToken; token != want {
					t.Errorf("%s request %d carried Authorization %q, want %q: the injected credential on every request, under the bearer scheme", test.name, i+1, token, want)
				}
			}
		})
	}
}

// TestTheMergeSendsTheHeadPinAndNothingThatDefersIt is the request-shape half of the
// merge, which no output assertion can see: a merge that dropped the pin would land on
// whatever the head is now, and one that sent a deferring action would complete later,
// unattended, on a pull request the user asked to merge now.
func TestTheMergeSendsTheHeadPinAndNothingThatDefersIt(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{mergeRoute: recorded.raw(t, "rest_merge_accepted")})
	if _, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA}); err != nil {
		t.Fatalf("MergePR = %v, want the outcome", err)
	}
	body := h.instance.body(mergeRoute)
	if !strings.Contains(body, `"sha":"`+testHeadSHA+`"`) {
		t.Errorf("the merge body = %s, want the head the caller pinned under sha, the key the route reads: a merge without it merges whatever the head is now", body)
	}
	if strings.Contains(body, "expected_head_sha") {
		t.Errorf("the merge body = %s, want no expected_head_sha: the route does not read that key, so a pin there pins nothing", body)
	}
	if !strings.Contains(body, `"merge_method":"merge"`) {
		t.Errorf("the merge body = %s, want the strategy this product's route takes", body)
	}
	if strings.Contains(body, "merge_action") {
		t.Errorf("the merge body = %s, want no merge action: this product completes an accepted merge on its own, so an action that deferred it would answer a merge nobody asked to wait for", body)
	}
	if strings.Contains(body, "auto_merge") || strings.Contains(body, "merge_when") {
		t.Errorf("the merge body = %s, want nothing that arms a later merge", body)
	}
}

// TestAMergeWithNoHeadPinIsRefusedBeforeAnyRequest holds the pin as a precondition
// rather than as a value: the refusal is local, so nothing reaches the instance.
func TestAMergeWithNoHeadPinIsRefusedBeforeAnyRequest(t *testing.T) {
	h := newHarness(t, nil)
	_, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeMissingSHA {
		t.Fatalf("MergePR with no head = %v, want code %q", err, forgeapi.CodeMissingSHA)
	}
	if got := h.instance.count(); got != 0 {
		t.Errorf("MergePR with no head sent %d request(s), want 0", got)
	}
}

// TestAStrategyOutsideThisFamilysSetIsRefusedLocally holds the closed set the merge
// strategy is checked against before any request, and the three members this product's
// own schema declares.
func TestAStrategyOutsideThisFamilysSetIsRefusedLocally(t *testing.T) {
	h := newHarness(t, nil)
	_, err := h.client.MergePR(t.Context(), testRef(), testPR(),
		forgeapi.MergeRequest{HeadSHA: testHeadSHA, Strategy: "fast-forward-only"})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeStrategyNotAllowed {
		t.Fatalf("MergePR with a foreign strategy = %v, want code %q", err, forgeapi.CodeStrategyNotAllowed)
	}
	if got := h.instance.count(); got != 0 {
		t.Errorf("MergePR with a foreign strategy sent %d request(s), want 0", got)
	}
	for _, method := range mergeMethods {
		if _, err := mergeMethod(forgeapi.MergeRequest{HeadSHA: testHeadSHA, Strategy: method}); err != nil {
			t.Errorf("mergeMethod(%q) = %v, want nil: that member is this product's own", method, err)
		}
	}
}

// TestTheMergesAcceptIsNotReportedAsAMerge holds the outcome mapping on the shape this
// product actually answers: a 202 carrying a pending status is a merge ACCEPTED and
// running, and reporting it merged would tell a user the work is done.
func TestTheMergesAcceptIsNotReportedAsAMerge(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{mergeRoute: recorded.raw(t, "rest_merge_accepted")})
	h.instance.status(mergeRoute, http.StatusAccepted)
	got, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA})
	if err != nil {
		t.Fatalf("MergePR = %v, want the outcome", err)
	}
	if got.State != forgeapi.MergeOutcomeAccepted {
		t.Errorf("MergePR against the recorded accept = %v, want %v", got.State, forgeapi.MergeOutcomeAccepted)
	}
	if got.Code != forgeapi.CodeAlreadyEnqueued {
		t.Errorf("MergePR against the recorded accept = code %q, want %q: an accepted merge leaves the caller no verdict yet",
			got.Code, forgeapi.CodeAlreadyEnqueued)
	}
	if got.QueueState != forgeapi.QueueUnknown {
		t.Errorf("MergePR = queue %v, want %v: the recorded accept carries no queue key of any kind, so MergeStatus is what reads it", got.QueueState, forgeapi.QueueUnknown)
	}
	if got.QueuePosition != forgeapi.QueuePositionUnknown {
		t.Errorf("MergePR = position %d, want %d", got.QueuePosition, forgeapi.QueuePositionUnknown)
	}
}

// TestAnUnreadableMergeAnswerIsNotAMerge holds the totality arm of the outcome mapping:
// a status no table carries is reported unknown and named, because a merge this library
// calls done when it is not is the worst answer available here.
func TestAnUnreadableMergeAnswerIsNotAMerge(t *testing.T) {
	h := newHarness(t, map[string]string{mergeRoute: `{"status":"teleported","details":{"uuid":"x"}}`})
	got, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA})
	if err != nil {
		t.Fatalf("MergePR = %v, want the outcome", err)
	}
	if got.State != forgeapi.MergeOutcomeUnknown {
		t.Errorf("MergePR against an unmapped status = %v, want %v", got.State, forgeapi.MergeOutcomeUnknown)
	}
	if !h.logged("teleported") {
		t.Error("the unmapped merge status is not in the log, want it named")
	}
}

// TestTheReRunSendsTheHeadPinAsTheFilterTheRouteDeclares holds the re-run's request
// shape and its refusal. The pin is the filter, so the retry addresses the run of the
// commit the caller's row was rendered from, and a head no run carries is refused rather
// than retried: a re-run can carry deployment side effects.
func TestTheReRunSendsTheHeadPinAsTheFilterTheRouteDeclares(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		runsRoute:  recorded.raw(t, "rest_workflow_runs"),
		rerunRoute: `{}`,
	})
	if err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA); err != nil {
		t.Fatalf("RerunFailedChecks = %v, want nil", err)
	}
	if got := h.instance.query(runsRoute, "head_sha"); got != testHeadSHA {
		t.Errorf("the resolving read sent head_sha %q, want %q: the pin is the filter this route declares", got, testHeadSHA)
	}
	if arrived := h.instance.arrived(); len(arrived) != 2 || arrived[1] != rerunRoute {
		t.Errorf("RerunFailedChecks reached %v, want the resolving read then the retry of the run it names", arrived)
	}
}

// TestAReRunWhoseHeadHasMovedIsRefused holds the other half of the pin: the instance
// answered runs and none carries the head the caller pinned, so the row the caller was
// looking at is stale and the retry is refused rather than aimed at another commit.
func TestAReRunWhoseHeadHasMovedIsRefused(t *testing.T) {
	h := newHarness(t, map[string]string{
		runsRoute: `{"total_count":1,"workflow_runs":[{"id":7,"head_sha":"0000000000000000000000000000000000000000","status":"completed","conclusion":"failure"}]}`,
	})
	err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA)
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeStaleHead {
		t.Fatalf("RerunFailedChecks against a moved head = %v, want code %q", err, forgeapi.CodeStaleHead)
	}
	if got := h.instance.count(); got != 1 {
		t.Errorf("RerunFailedChecks against a moved head sent %d request(s), want 1: nothing is retried", got)
	}
}

// TestTheCreationsSendTheLabelsEachRouteTakes holds the one request decision the two
// creations differ on, on this product: the issue route takes label NAMES in the
// creation's own body, and the pull-request route declares no labels parameter at all,
// so there they ride a SECOND request against the labels route, which is the arm that
// operation's price states.
//
// A creation that dropped them succeeded and did not do what was asked, on the one
// product where the caller could not tell, which is what makes this a request-shape
// property rather than an output one.
func TestTheCreationsSendTheLabelsEachRouteTakes(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		"POST /api/v3/repos/example/example/issues":          recorded.raw(t, "rest_issue"),
		"POST /api/v3/repos/example/example/pulls":           recorded.raw(t, "rest_pull"),
		"POST /api/v3/repos/example/example/issues/1/labels": `[{"name":"example-label","color":"ededed"}]`,
	})
	if _, err := h.client.CreateIssue(t.Context(), testRef(), forgeapi.NewIssue{Title: "t", Labels: []string{"example-label"}}); err != nil {
		t.Fatalf("CreateIssue = %v, want the issue", err)
	}
	if body := h.instance.body("POST /api/v3/repos/example/example/issues"); !strings.Contains(body, `"labels":["example-label"]`) {
		t.Errorf("the issue creation's body = %s, want the label names this route takes", body)
	}
	before := h.instance.count()
	if _, err := h.client.CreatePR(t.Context(), testRef(), forgeapi.NewPullRequest{Title: "t", Labels: []string{"example-label"}}); err != nil {
		t.Fatalf("CreatePR = %v, want the pull request", err)
	}
	if body := h.instance.body("POST /api/v3/repos/example/example/pulls"); strings.Contains(body, "labels") {
		t.Errorf("the pull-request creation's body = %s, want no labels: this route declares no such parameter, so sending one asks the instance to ignore it", body)
	}
	if got := h.instance.count() - before; got != 2 {
		t.Errorf("CreatePR with labels sent %d request(s), want 2: %v", got, h.instance.arrived())
	}
	if body := h.instance.body("POST /api/v3/repos/example/example/issues/1/labels"); !strings.Contains(body, `"labels":["example-label"]`) {
		t.Errorf("the labels request's body = %s, want the label names this route takes", body)
	}
}

// TestACreationWithNoLabelsSpendsNoSecondRequest holds the other arm of that price: the
// second request is what the labels buy, so a creation that names none pays for none.
func TestACreationWithNoLabelsSpendsNoSecondRequest(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{"POST /api/v3/repos/example/example/pulls": recorded.raw(t, "rest_pull")})
	if _, err := h.client.CreatePR(t.Context(), testRef(), forgeapi.NewPullRequest{Title: "t"}); err != nil {
		t.Fatalf("CreatePR = %v, want the pull request", err)
	}
	if got := h.instance.count(); got != 1 {
		t.Errorf("CreatePR with no labels sent %d request(s), want 1: %v", got, h.instance.arrived())
	}
}

// TestACreationWhoseLabelsFailAnswersTheObjectItCreated holds the window between the two
// requests, which is the one shape on this surface where a non-nil error arrives with an
// answer: the pull request EXISTS, so an error alone would hide an object the caller now
// owns and would have it create a second one on the retry.
func TestACreationWhoseLabelsFailAnswersTheObjectItCreated(t *testing.T) {
	recorded := readCaptures(t)
	const labelsRequest = "POST /api/v3/repos/example/example/issues/1/labels"
	h := newHarness(t, map[string]string{
		"POST /api/v3/repos/example/example/pulls": recorded.raw(t, "rest_pull"),
		labelsRequest: `{"message":"Validation Failed"}`,
	})
	h.instance.status(labelsRequest, http.StatusUnprocessableEntity)
	item, err := h.client.CreatePR(t.Context(), testRef(), forgeapi.NewPullRequest{Title: "t", Labels: []string{"example-label"}})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("CreatePR whose labels failed = %v, want this library's own error naming what was not done", err)
	}
	if item.Ref.Number != 1 {
		t.Errorf("CreatePR whose labels failed = pull request %+v, want the created one beside the error: the creation happened", item.Ref)
	}
	if item.Title == "" || item.WebURL == "" {
		t.Errorf("CreatePR whose labels failed = title %q and page %q, want the created object's own", item.Title, item.WebURL)
	}
	if got := h.instance.count(); got != 2 {
		t.Errorf("CreatePR whose labels failed sent %d request(s), want 2: %v", got, h.instance.arrived())
	}
}

// TestACreationWhoseLabelsFailMarksTheObjectItCreated holds what the object beside that
// error says about itself: it exists without EVERY label it was asked for, because the
// labels request applies them all or none, and the marker is counted like every other
// partial result this family reports.
func TestACreationWhoseLabelsFailMarksTheObjectItCreated(t *testing.T) {
	recorded := readCaptures(t)
	const labelsRequest = "POST /api/v3/repos/example/example/issues/1/labels"
	h := newHarness(t, map[string]string{
		"POST /api/v3/repos/example/example/pulls": recorded.raw(t, "rest_pull"),
		labelsRequest: `{"message":"Validation Failed"}`,
	})
	h.instance.status(labelsRequest, http.StatusUnprocessableEntity)
	labels := []string{"example-label", "second-label"}
	item, err := h.client.CreatePR(t.Context(), testRef(), forgeapi.NewPullRequest{Title: "t", Labels: labels})
	if err == nil {
		t.Fatal("CreatePR whose labels failed = nil error, want the error naming what was not done")
	}
	want := forgeapi.Partial{Reason: forgeapi.PartialLabelsNotApplied, OmittedAtLeast: len(labels)}
	switch {
	case item.Partial == nil:
		t.Errorf("CreatePR whose labels failed = no marker, want %+v", want)
	case *item.Partial != want:
		t.Errorf("CreatePR whose labels failed = marker %+v, want %+v", *item.Partial, want)
	}
	if got := h.spy.times("PartialResult:labels_not_applied"); got != 1 {
		t.Errorf("CreatePR whose labels failed counted %d labels_not_applied partial(s), want 1", got)
	}
}

// TestACreationWhoseLabelsApplyCarriesNoMarker holds the other arm: the marker says the
// labels are missing, so a creation whose labels request succeeded must not carry it.
func TestACreationWhoseLabelsApplyCarriesNoMarker(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		"POST /api/v3/repos/example/example/pulls":           recorded.raw(t, "rest_pull"),
		"POST /api/v3/repos/example/example/issues/1/labels": `[{"name":"example-label","color":"ededed"}]`,
	})
	item, err := h.client.CreatePR(t.Context(), testRef(), forgeapi.NewPullRequest{Title: "t", Labels: []string{"example-label"}})
	if err != nil {
		t.Fatalf("CreatePR = %v, want the pull request", err)
	}
	if item.Partial != nil {
		t.Errorf("CreatePR whose labels applied = marker %+v, want none", *item.Partial)
	}
}

// TestTheListsFoldIsBoundedAndSaysSo holds the bounded-fold rule on this product, where
// it costs no unknown: a row whose contexts connection continues carries an EXACT
// verdict beside the pagination marker, because the counts describe the whole collection
// and what the bound truncates is the names.
func TestTheListsFoldIsBoundedAndSaysSo(t *testing.T) {
	recorded := readCaptures(t)
	// The recorded row's contexts connection reported no next page, so the one edit
	// this case makes to the recorded bytes is that flag: what is under test is the
	// row a bounded fold TRUNCATED, and the counts beside it are the recording's own.
	truncated := strings.Replace(recorded.raw(t, "graphql_search_row"), `"hasNextPage": false`, `"hasNextPage": true`, 1)
	h := newHarness(t, map[string]string{
		documentRoute: documentEnvelope(`"repository":{"nameWithOwner":"example/example","viewerPermission":"WRITE",` +
			`"pullRequests":{"totalCount":1,"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[` + truncated + `]}}`),
	})
	page, err := h.client.ListPRs(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListPRs = %v, want the page", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListPRs answered %d row(s), want 1", len(page.Items))
	}
	got := page.Items[0]
	if got.Partial == nil || got.Partial.Reason != forgeapi.PartialPaginationCap {
		t.Errorf("ListPRs row 0 = partial %v, want the pagination cap: its contexts connection continues past the page the document asked for", got.Partial)
	}
	if got.Action.Checks != forgeapi.CheckPassing || got.Action.ChecksTotal != 7 {
		t.Errorf("ListPRs row 0 = %v over %d check(s), want a passing verdict over 7: the counts describe the collection, so the bound truncates the names rather than the verdict",
			got.Action.Checks, got.Action.ChecksTotal)
	}
	if got := h.instance.count(); got != 1 {
		t.Errorf("ListPRs sent %d request(s), want 1: following a row's contexts pages is the fan-out the budget refuses", got)
	}
}

// TestTheSingleReadFollowsTheContextsPagesUpToItsBound holds the complete fold and the
// bound on it at once: the caller named one pull request and is owed the names, and the
// published bound is what stops the read following a collection without end.
func TestTheSingleReadFollowsTheContextsPagesUpToItsBound(t *testing.T) {
	page := func(cursor string, hasNext bool, name string) string {
		return documentEnvelope(`"repository":{"nameWithOwner":"example/example","viewerPermission":"WRITE","pullRequest":{` +
			`"number":1,"title":"Example pull request","state":"OPEN","headRefOid":"` + testHeadSHA + `",` +
			`"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","commits":{"nodes":[{"commit":{"oid":"` + testHeadSHA + `",` +
			`"statusCheckRollup":{"state":"SUCCESS","contexts":{"pageInfo":{"hasNextPage":` + strconv.FormatBool(hasNext) +
			`,"endCursor":` + quoteOrNull(cursor) + `},"nodes":[{"__typename":"StatusContext","context":"` + name + `","state":"SUCCESS"}]}}}}]}}}`)
	}
	h := newHarness(t, map[string]string{documentRoute: page("MQ", true, "example/one")})
	reads := 0
	// Each page answers the next, so the read follows until the bound stops it.
	h.instance.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		names := []string{"example/one", "example/two", "example/three", "example/four"}
		hasNext := reads < len(names)
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(page("cursor"+strconv.Itoa(reads), hasNext, names[min(reads, len(names))-1]))); err != nil {
			t.Errorf("Setup: writing page %d: %v", reads, err)
		}
	})
	got, err := h.client.ReadPR(t.Context(), testRef(), testPR())
	if err != nil {
		t.Fatalf("ReadPR = %v, want the pull request", err)
	}
	if reads != forgeapi.DefaultStatusPages {
		t.Errorf("ReadPR followed %d page(s) of the contexts connection, want the published bound of %d", reads, forgeapi.DefaultStatusPages)
	}
	if got.Action.ChecksTotal != reads {
		t.Errorf("ReadPR = %d check(s) folded, want one per page it read (%d)", got.Action.ChecksTotal, reads)
	}
	if got.Action.Checks != forgeapi.CheckPassing {
		t.Errorf("ReadPR = %v, want %v over the pages it did read", got.Action.Checks, forgeapi.CheckPassing)
	}
	if got.Partial == nil {
		t.Fatal("ReadPR stopped at the bound and carries no pagination marker, want one: the marker is the only thing that tells a consumer the context names are short")
	}
	if got.Partial.Reason != forgeapi.PartialPaginationCap {
		t.Errorf("ReadPR at the bound = partial %v, want %v", got.Partial.Reason, forgeapi.PartialPaginationCap)
	}
}

// TestTheMergeStateReadFollowsNoPageOfChecks holds what separates the merge-state read
// from the pull-request read: it returns no checks, so it pays for none, which is what
// makes its price exactly one on a pull request whose collection continues.
func TestTheMergeStateReadFollowsNoPageOfChecks(t *testing.T) {
	envelope := documentEnvelope(`"repository":{"nameWithOwner":"example/example","viewerPermission":"WRITE","pullRequest":{` +
		`"number":1,"url":"https://forge.example/example/example/pull/1","state":"OPEN","merged":false,` +
		`"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","mergeQueueEntry":null,` +
		`"commits":{"nodes":[{"commit":{"oid":"` + testHeadSHA + `","statusCheckRollup":{"state":"SUCCESS","contexts":{` +
		`"pageInfo":{"hasNextPage":true,"endCursor":"MQ"},"nodes":[]}}}}]}}}`)
	h := newHarness(t, map[string]string{documentRoute: envelope})
	got, err := h.client.MergeStatus(t.Context(), testRef(), testPR())
	if err != nil {
		t.Fatalf("MergeStatus = %v, want the status", err)
	}
	if n := h.instance.count(); n != 1 {
		t.Errorf("MergeStatus sent %d request(s), want 1 even where the checks collection continues", n)
	}
	if got.Merged != forgeapi.SupportNo {
		t.Errorf("MergeStatus = merged %v, want %v", got.Merged, forgeapi.SupportNo)
	}
	if got.Queue != forgeapi.QueueNone {
		t.Errorf("MergeStatus = queue %v, want %v: the recorded entry is null", got.Queue, forgeapi.QueueNone)
	}
	if got.WebURL == "" {
		t.Error("MergeStatus carries no web URL, want the page for a person: that is what this read offers in place of the fold")
	}
}

// TestAQueueEntryIsReadWithItsPosition holds the one product whose queue verdict has
// five members and a position, which is why that verdict is its own enumeration rather than a
// three-valued answer.
func TestAQueueEntryIsReadWithItsPosition(t *testing.T) {
	envelope := documentEnvelope(`"repository":{"nameWithOwner":"example/example","pullRequest":{` +
		`"number":1,"url":"https://forge.example/example/example/pull/1","state":"OPEN","merged":false,` +
		`"mergeQueueEntry":{"state":"AWAITING_CHECKS","position":3}}}`)
	h := newHarness(t, map[string]string{documentRoute: envelope})
	got, err := h.client.MergeStatus(t.Context(), testRef(), testPR())
	if err != nil {
		t.Fatalf("MergeStatus = %v, want the status", err)
	}
	if got.Queue != forgeapi.QueueAwaitingChecks {
		t.Errorf("MergeStatus = queue %v, want %v", got.Queue, forgeapi.QueueAwaitingChecks)
	}
	read, err := h.client.ReadPR(t.Context(), testRef(), testPR())
	if err != nil {
		t.Fatalf("ReadPR = %v, want the pull request", err)
	}
	if read.Action.QueuePosition != 3 {
		t.Errorf("ReadPR = queue position %d, want the 3 the entry carries", read.Action.QueuePosition)
	}
}

// TestTheMergeStateReadSpendsOneRequestOnEveryArm holds the exact price its row
// publishes on the arm nothing else drives: the call that meets a refused document. That
// call spends one request, the refusal, and the next call on the connection spends one,
// the REST record, so neither arm exceeds the one this operation promises everywhere.
func TestTheMergeStateReadSpendsOneRequestOnEveryArm(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		documentRoute: `{"errors":[{"type":"UNPROCESSABLE","message":"the schema refuses this document"}],"data":null}`,
		pullsRoute:    recorded.raw(t, "rest_pull"),
	})
	if _, err := h.client.MergeStatus(t.Context(), testRef(), testPR()); err == nil {
		t.Fatal("MergeStatus against a refused document = <nil>, want the refusal")
	}
	if n := h.instance.count(); n != 1 {
		t.Errorf("the discovering call sent %d request(s), want 1: %v", n, h.instance.arrived())
	}
	before := h.instance.count()
	got, err := h.client.MergeStatus(t.Context(), testRef(), testPR())
	if err != nil {
		t.Fatalf("MergeStatus on a degraded connection = %v, want the REST answer", err)
	}
	if n := h.instance.count() - before; n != 1 {
		t.Errorf("the degraded call sent %d request(s), want 1: the REST record alone", n)
	}
	if got.Queue != forgeapi.QueueNone {
		t.Errorf("MergeStatus on the degraded arm = queue %v, want %v: the REST record carries no merge-queue entry", got.Queue, forgeapi.QueueNone)
	}
	if got.Merged != forgeapi.SupportNo {
		t.Errorf("MergeStatus on the degraded arm = merged %v, want %v: the record's own merged flag", got.Merged, forgeapi.SupportNo)
	}
}

// TestADocumentRefusedAtRuntimeIsTheCallsOwnFailureAndTheConnectionsRecord holds the
// degradation arm to the exact prices the rows publish. The call that MEETS the refusal
// spends that one request and answers with the refusal; it does not buy the REST read
// inside itself, because a range on an operation for a property of the CONNECTION makes
// an exact promise impossible. The next call on the same connection takes the REST arm
// from the start and spends one.
func TestADocumentRefusedAtRuntimeIsTheCallsOwnFailureAndTheConnectionsRecord(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		documentRoute: `{"errors":[{"type":"UNPROCESSABLE","message":"the schema refuses this document"}],"data":null}`,
		pullsRoute:    recorded.raw(t, "rest_pull"),
	})
	if _, err := h.client.ReadPR(t.Context(), testRef(), testPR()); err == nil {
		t.Fatal("ReadPR against a refused document = <nil>, want the refusal: the discovery is not this call's to pay for")
	}
	if n := h.instance.count(); n != 1 {
		t.Errorf("the discovering call sent %d request(s), want 1: the refusal alone: %v", n, h.instance.arrived())
	}
	before := h.instance.count()
	got, err := h.client.ReadPR(t.Context(), testRef(), testPR())
	if err != nil {
		t.Fatalf("ReadPR on a degraded connection = %v, want the REST answer", err)
	}
	if n := h.instance.count() - before; n != 1 {
		t.Errorf("the second call spent %d request(s), want 1: the REST read alone", n)
	}
	if got.Action.Checks != forgeapi.CheckUnknown {
		t.Errorf("ReadPR on the degraded arm = %v, want %v: that route carries no rollup for a fold to read", got.Action.Checks, forgeapi.CheckUnknown)
	}
	if arrived := h.instance.arrived(); arrived[len(arrived)-1] != pullsRoute {
		t.Errorf("the second call reached %q, want the REST read: a degraded connection does not retry the document", arrived[len(arrived)-1])
	}
}

// TestTheDegradedReadMapsTheMergeStateItReads holds the REST arm's one READ to the
// mapping the mutations on the same arm do not apply: the record a read fetches is
// the pull request as it stands, so its mergeable flag and its REST merge state name
// the verdict and the reason, here a recorded mergeable, clean pull request.
func TestTheDegradedReadMapsTheMergeStateItReads(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		documentRoute: `{"errors":[{"type":"UNPROCESSABLE","message":"the schema refuses this document"}],"data":null}`,
		pullsRoute:    recorded.raw(t, "rest_pull"),
	})
	if _, err := h.client.ReadPR(t.Context(), testRef(), testPR()); err == nil {
		t.Fatal("Setup: ReadPR against a refused document = <nil>, want the refusal that degrades the connection")
	}
	got, err := h.client.ReadPR(t.Context(), testRef(), testPR())
	if err != nil {
		t.Fatalf("ReadPR on a degraded connection = %v, want the REST answer", err)
	}
	if got.Action.Mergeable != forgeapi.SupportYes {
		t.Errorf("ReadPR on the degraded arm over mergeable true = Action.Mergeable %v, want %v", got.Action.Mergeable, forgeapi.SupportYes)
	}
	if got.Action.MergeBlocked != forgeapi.MergeBlockNone {
		t.Errorf("ReadPR on the degraded arm over mergeable_state clean = Action.MergeBlocked %v, want %v", got.Action.MergeBlocked, forgeapi.MergeBlockNone)
	}
}

// TestTheFoldedStatusDegradesToBothSourcesRatherThanTheCombinedStatusAlone holds the
// measured hazard on the arm that meets it: the combined status answers an empty array
// byte-identically on an Actions repository and on a commit with no CI, so the degraded
// read takes the check runs too and folds what it holds.
func TestTheFoldedStatusDegradesToBothSourcesRatherThanTheCombinedStatusAlone(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		documentRoute:  `{"errors":[{"type":"UNPROCESSABLE","message":"the schema refuses this document"}],"data":null}`,
		statusRoute:    recorded.raw(t, "rest_combined_status_on_an_actions_repository"),
		checkRunsRoute: recorded.raw(t, "rest_check_runs"),
	})
	if _, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA); err == nil {
		t.Fatal("Setup: CommitStatus against a refused document = <nil>, want the refusal that degrades the connection")
	}
	before := h.instance.count()
	got, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA)
	if err != nil {
		t.Fatalf("CommitStatus on a degraded connection = %v, want the REST answer", err)
	}
	if n := h.instance.count() - before; n != 2 {
		t.Errorf("the degraded call sent %d request(s), want 2: the combined status and the check runs, which is what its range prices: %v", n, h.instance.arrived())
	}
	if got.State != forgeapi.CheckPassing || got.Total != 2 {
		t.Errorf("CommitStatus on the degraded arm = %v over %d, want a passing verdict over the 2 check runs: the combined status carried none",
			got.State, got.Total)
	}
	if got.Ref != testHeadSHA {
		t.Errorf("CommitStatus = ref %q, want the commit the rows name", got.Ref)
	}
}

// TestATruncatedFoldedStatusSaysSo holds the positive direction of the folded status's
// pagination marker, which is the direction the sibling case over an empty fold cannot
// reach: a commit whose contexts connection continues at every page is read to the
// published bound and the answer carries the marker.
//
// The verdict stays exact beside it, which is this product's own property rather than a
// concession: the rollup's counts describe the whole collection, so what the bound
// truncates is the context NAMES. That pairing is the whole reason the marker has to be
// there, because nothing else in the answer separates three names out of many from three
// names that were all of them.
func TestATruncatedFoldedStatusSaysSo(t *testing.T) {
	page := func(cursor, name string) string {
		return documentEnvelope(`"repository":{"nameWithOwner":"example/example","object":{"oid":"` + testHeadSHA + `",` +
			`"statusCheckRollup":{"state":"SUCCESS","contexts":{"totalCount":446,` +
			`"pageInfo":{"hasNextPage":true,"endCursor":` + quoteOrNull(cursor) + `},` +
			`"nodes":[{"__typename":"StatusContext","context":"` + name + `","state":"SUCCESS"}]}}}}`)
	}
	h := newHarness(t, map[string]string{documentRoute: page("MQ", "example/one")})
	reads := 0
	// Every page reports a further one, which is the 446-context shape the read's own
	// bound exists for: the fold can never complete, so the bound is what ends it.
	h.instance.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(page("cursor"+strconv.Itoa(reads), "example/"+strconv.Itoa(reads)))); err != nil {
			t.Errorf("Setup: writing page %d: %v", reads, err)
		}
	})
	got, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA)
	if err != nil {
		t.Fatalf("CommitStatus = %v, want the fold", err)
	}
	if reads != forgeapi.DefaultStatusPages {
		t.Errorf("CommitStatus read %d page(s) of the contexts connection, want the published bound of %d", reads, forgeapi.DefaultStatusPages)
	}
	if got.Partial == nil {
		t.Fatal("CommitStatus stopped at the bound and carries no pagination marker, want one: the marker is the only thing that tells a consumer the context names are short")
	}
	if got.Partial.Reason != forgeapi.PartialPaginationCap {
		t.Errorf("CommitStatus at the bound = partial %v, want %v", got.Partial.Reason, forgeapi.PartialPaginationCap)
	}
	if got.State != forgeapi.CheckPassing {
		t.Errorf("CommitStatus at the bound = %v, want %v: the counts describe the whole collection, so the bound truncates the names rather than the verdict", got.State, forgeapi.CheckPassing)
	}
	if len(got.Contexts) != reads {
		t.Errorf("CommitStatus at the bound = %d context name(s), want one per page it read (%d)", len(got.Contexts), reads)
	}
}

// TestAnEmptyDegradedFoldIsUnknownRatherThanGreen holds the same hazard's own worst
// case: both sources empty is a commit with nothing to fold, so the answer is the unknown
// member over a zero total rather than the combined status's own word for it.
func TestAnEmptyDegradedFoldIsUnknownRatherThanGreen(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		documentRoute:  `{"errors":[{"type":"UNPROCESSABLE","message":"refused"}],"data":null}`,
		statusRoute:    recorded.raw(t, "rest_combined_status_on_a_commit_with_no_ci"),
		checkRunsRoute: `{"total_count":0,"check_runs":[]}`,
	})
	if _, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA); err == nil {
		t.Fatal("Setup: CommitStatus against a refused document = <nil>, want the refusal that degrades the connection")
	}
	got, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA)
	if err != nil {
		t.Fatalf("CommitStatus = %v, want the empty answer", err)
	}
	if got.State != forgeapi.CheckUnknown || got.Total != 0 {
		t.Errorf("CommitStatus over nothing = %v at total %d, want %v at 0", got.State, got.Total, forgeapi.CheckUnknown)
	}
	if got.Partial != nil {
		t.Errorf("CommitStatus over nothing = partial %v, want none: nothing was truncated", got.Partial)
	}
	if len(got.Contexts) != 0 {
		t.Errorf("CommitStatus over nothing carries %d context row(s), want none", len(got.Contexts))
	}
}

// TestTheVersionPinIsResolvedAtSetupAndRidesOnlyWhereItIsServed holds both arms of the
// pin, which is the whole decision: the version set connection setup read is what says
// which instance this is, and the pin rides every later REST request of the one that
// serves it and no request of the one that does not.
//
// The echo cannot decide it, which is why the set is read: the instance on both arms
// here names the OLDER version it serves, the way a real one answers a request that
// pinned nothing, so a connection waiting for that echo to name the pinned version
// would wait forever and run entirely under the version this pin supersedes. And an
// instance that serves no such version answers 400 to every request that names it, so
// a pin sent blind is not the safe direction either.
func TestTheVersionPinIsResolvedAtSetupAndRidesOnlyWhereItIsServed(t *testing.T) {
	for _, test := range []struct {
		name    string
		served  string
		wantPin string
	}{
		{name: "the_instance_serves_the_pinned_version", served: servedVersions, wantPin: APIVersion},
		{name: "the_instance_serves_only_the_version_it_supersedes", served: unservedVersions, wantPin: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorded := readCaptures(t)
			h := newHarness(t, map[string]string{
				metaRoute:     `{"installed_version":"3.20.0"}`,
				versionsRoute: test.served,
				userRoute:     recorded.raw(t, "rest_user"),
				reposRoute:    `[` + recorded.raw(t, "rest_repo_listing_row") + `]`,
			})
			withWitness(h.instance)
			h.instance.answerHeader(headerVersionSelected, "2022-11-28")
			if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
				t.Fatalf("Setup: ConnectionCaps = %v", err)
			}
			before := h.instance.count()
			if _, err := h.client.Whoami(t.Context()); err != nil {
				t.Fatalf("Setup: Whoami = %v", err)
			}
			if _, err := h.client.ListRepos(t.Context()); err != nil {
				t.Fatalf("Setup: ListRepos = %v", err)
			}
			pins := h.instance.versionPins()[before:]
			if len(pins) != 2 {
				t.Fatalf("two calls after setup sent %d request(s), want 2: %v", len(pins), h.instance.arrived())
			}
			for i, pin := range pins {
				if pin != test.wantPin {
					t.Errorf("request %d after setup carried the pin %q, want %q: the version set this instance published is %s",
						i+1, pin, test.wantPin, test.served)
				}
			}
		})
	}
}

// TestAConnectionWhoseSetupHasNotRunSendsNoPin is the third state, and it is the same
// arm as the appliance: a connection that never resolved the version set runs under the
// instance's own default rather than naming a version the instance may refuse.
func TestAConnectionWhoseSetupHasNotRunSendsNoPin(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{userRoute: recorded.raw(t, "rest_user")})
	if _, err := h.client.Whoami(t.Context()); err != nil {
		t.Fatalf("Setup: Whoami = %v", err)
	}
	pins := h.instance.versionPins()
	if len(pins) != 1 {
		t.Fatalf("one call sent %d request(s), want 1", len(pins))
	}
	if pins[0] != "" {
		t.Errorf("a connection whose setup has not run carried the pin %q, want none: what it would name is unresolved", pins[0])
	}
}

// TestTheVersionPinRidesRESTAndNeverADocument holds the pin to the surface the date
// version names: every REST request carries it and no document does, because the
// version this family pins is a REST API version and the document surface is versioned
// by its schema instead.
func TestTheVersionPinRidesRESTAndNeverADocument(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		metaRoute:     `{"installed_version":"3.20.0"}`,
		versionsRoute: servedVersions,
		userRoute:     recorded.raw(t, "rest_user"),
		reposRoute:    `[` + recorded.raw(t, "rest_repo_listing_row") + `]`,
		documentRoute: documentEnvelope(`"repository":{"nameWithOwner":"example/example","viewerPermission":"WRITE",` +
			`"pullRequests":{"totalCount":1,"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[` +
			recorded.raw(t, "graphql_pr_read") + `]}}`),
	})
	withWitness(h.instance)
	if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
		t.Fatalf("Setup: ConnectionCaps = %v", err)
	}
	before := h.instance.count()
	if _, err := h.client.Whoami(t.Context()); err != nil {
		t.Fatalf("Setup: Whoami = %v", err)
	}
	if _, err := h.client.ListPRs(t.Context(), testRef()); err != nil {
		t.Fatalf("Setup: ListPRs = %v", err)
	}
	if _, err := h.client.ListRepos(t.Context()); err != nil {
		t.Fatalf("Setup: ListRepos = %v", err)
	}
	arrived := h.instance.arrived()[before:]
	pins := h.instance.versionPins()[before:]
	if len(pins) != 3 || len(arrived) != 3 {
		t.Fatalf("three calls after setup sent %v with pins %v, want three requests", arrived, pins)
	}
	if arrived[1] != documentRoute {
		t.Fatalf("request 2 reached %q, want the document route %q", arrived[1], documentRoute)
	}
	if pins[0] != APIVersion {
		t.Errorf("the REST request before the document carried the pin %q, want %q", pins[0], APIVersion)
	}
	if pins[1] != "" {
		t.Errorf("the document request carried the pin %q, want none: that version names a REST API version", pins[1])
	}
	if pins[2] != APIVersion {
		t.Errorf("the REST request after the document carried the pin %q, want %q", pins[2], APIVersion)
	}
}

// TestAnOrdinaryRefusalUnderThePinnedVersionIsNotTheVersionsFault is the consequence
// arm of the pin, and it is the one a consumer reads: the code that tells it to take a
// new release of this library must arrive only when the version this library named is
// what the instance refused.
//
// The three shapes are measured on the live product. An instance serving the pin names
// it in the echo on every answer including a refusal, so a 400 carrying that echo is an
// ordinary refusal and keeps the bare upstream kind. A version outside the instance's
// set is refused before one is selected, so that refusal carries no echo. And the third
// row is the one the pin's own resolution makes reachable: a connection whose instance
// serves no such version sends no pin, so its answers name the instance's default on
// every response including an ordinary refusal, and a mapping that read the echo there
// would hand the retirement remedy to every 400 on that connection.
func TestAnOrdinaryRefusalUnderThePinnedVersionIsNotTheVersionsFault(t *testing.T) {
	for _, test := range []struct {
		name     string
		served   string
		echo     string
		wantCode string
	}{
		{
			name:   "the_instance_answered_under_the_pinned_version",
			served: servedVersions, echo: APIVersion, wantCode: "",
		},
		{
			name:   "the_instance_refused_the_version_the_request_named",
			served: servedVersions, echo: "", wantCode: forgeapi.CodeAPIVersionRetired,
		},
		{
			name:   "no_pin_rode_the_request_so_the_refusal_is_not_the_versions",
			served: unservedVersions, echo: "2022-11-28", wantCode: "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				metaRoute:     `{"installed_version":"3.20.0"}`,
				versionsRoute: test.served,
				userRoute:     `{"message":"Bad Request"}`,
			})
			withWitness(h.instance)
			if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
				t.Fatalf("Setup: ConnectionCaps = %v", err)
			}
			if test.echo != "" {
				h.instance.answerHeader(headerVersionSelected, test.echo)
			}
			h.instance.status(userRoute, http.StatusBadRequest)
			_, err := h.client.Whoami(t.Context())
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) {
				t.Fatalf("Whoami = %v, want this library's own error", err)
			}
			if fe.Code != test.wantCode {
				t.Errorf("a 400 with the version echo %q on an instance publishing %s = code %q, want %q",
					test.echo, test.served, fe.Code, test.wantCode)
			}
		})
	}
}

// TestAnAnnouncedWindowClosingIsCounted holds the one thing a library can do about a
// dated pin at run time, which is to make it visible: the remedy is moving the constant
// in a release, so the header is counted rather than acted on.
func TestAnAnnouncedWindowClosingIsCounted(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{userRoute: recorded.raw(t, "rest_user")})
	h.instance.answerHeader(headerSunset, "Wed, 10 Mar 2028 00:00:00 GMT")
	if _, err := h.client.Whoami(t.Context()); err != nil {
		t.Fatalf("Whoami = %v, want the account", err)
	}
	if n := h.spy.times("SunsetSeen"); n != 1 {
		t.Errorf("the sunset counter fired %d time(s), want 1", n)
	}
}

// TestAPagedListResumesFromTheLinkHeaderWitness holds this product's own pagination
// witness, read for the PRESENCE of a next relation rather than for the URL it carries:
// that URL is measured to be id-addressed on repositories never renamed, so resuming
// from it would address a repository by an id no published route pins.
func TestAPagedListResumesFromTheLinkHeaderWitness(t *testing.T) {
	h := newHarness(t, map[string]string{labelsRoute: `[{"name":"example-label","color":"ededed"}]`})
	h.instance.answerHeaders(labelsRoute, http.Header{
		"Link": []string{`<https://forge.example/repositories/1246315859/labels?per_page=100&page=2>; rel="next", <…&page=5>; rel="last"`},
	})
	page, err := h.client.ListLabels(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListLabels = %v, want the page", err)
	}
	if want := restCursor(t, h.client, "ListLabels", testRef(), 2); page.Next != want {
		t.Errorf("ListLabels = next %q, want %q, the page after this one: the link header is the witness and the continuation is this library's own", page.Next, want)
	}
	if err := forgeapi.ValidateCursor(page.Next); err != nil {
		t.Errorf("ValidateCursor(%q) = %v, want nil", page.Next, err)
	}
}

// TestAnIdAddressedLinkHeaderIsNoRename is the false positive the rename witness has to
// avoid, and it is measured rather than hypothetical: this product answers a
// repo-addressed list with an id-addressed link header on a repository that was never
// renamed, so a final-path comparison would report a rename on page two of every list.
func TestAnIdAddressedLinkHeaderIsNoRename(t *testing.T) {
	h := newHarness(t, map[string]string{labelsRoute: `[{"name":"example-label"}]`})
	h.instance.answerHeaders(labelsRoute, http.Header{
		"Link": []string{`<https://forge.example/repositories/1246315859/labels?page=2>; rel="next"`},
	})
	page, err := h.client.ListLabels(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListLabels against an id-addressed link header = %v, want the page: that header is not a rename", err)
	}
	if page.Successor != nil {
		t.Errorf("ListLabels = successor %v, want none: nothing redirected", page.Successor)
	}
}

// renamedInstance is an instance whose repository MOVED: every request under the
// selector the caller holds answers a redirect to the id-addressed location this product
// sends, the id-addressed routes answer from moved, and the id-addressed repository
// record is what the successor's canonical name is read from.
//
// successor says whether that record answers at all, which is the difference between the
// rename a consumer can act on and the one whose remedy is listing the repositories
// again. A moved route answers the status [instance.status] sets for it, 200 otherwise.
func renamedInstance(t *testing.T, moved map[string]string, successor bool, status int) *harness {
	t.Helper()
	in := newInstance()
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		in.mu.Lock()
		in.requests = append(in.requests, r.Method+" "+path)
		in.tokens = append(in.tokens, r.Header.Get("Authorization"))
		in.pins = append(in.pins, r.Header.Get(APIVersionHeader))
		if body := readAll(t, r); body != "" {
			in.bodies[r.Method+" "+path] = body
		}
		code, coded := in.statuses[r.Method+" "+path]
		in.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if after, ok := strings.CutPrefix(path, "/api/v3/repos/example/example"); ok {
			http.Redirect(w, r, "/api/v3/repositories/7691631"+after, status)
			return
		}
		if path == "/api/v3/repositories/7691631" {
			if !successor {
				w.WriteHeader(http.StatusNotFound)
				if _, err := w.Write([]byte(`{"message":"Not Found"}`)); err != nil {
					t.Errorf("Setup: writing the refusal: %v", err)
				}
				return
			}
			if _, err := w.Write([]byte(`{"full_name":"example/renamed"}`)); err != nil {
				t.Errorf("Setup: writing the successor record: %v", err)
			}
			return
		}
		body, ok := moved[r.Method+" "+path]
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			if _, err := w.Write([]byte(`{"message":"no route for ` + path + `"}`)); err != nil {
				t.Errorf("Setup: writing the miss: %v", err)
			}
			return
		}
		if coded {
			w.WriteHeader(code)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(in.server.Close)
	return newHarnessOver(t, in)
}

// TestARenameFillsEveryCarrierTheSurfaceDeclares holds the rename on all four carriers
// at once, which is what stops the field being a half-feature a reader infers a mechanism
// from: the three answers that declare a successor return the successor's rows and name
// it, and the calls that declare none refuse under the stale code carrying the same
// value.
//
// The redirect is the witness this product needs: it answers a renamed repository with a
// hop the follow-and-revalidate policy would otherwise take silently, leaving a stale
// reference working until the day the old address stopped redirecting. What it does not
// carry is a canonical selector, because the location is id-addressed, so each arm here
// spends the one further read the price states and every arm spends it once.
//
// The request counts are what say once. Each read arrives twice at the instance, the
// address the caller named and the hop it led to, and the resolving read is the one
// beyond that: so the folded-status arm, whose degraded path makes TWO reads, spends
// five rather than six.
func TestARenameFillsEveryCarrierTheSurfaceDeclares(t *testing.T) {
	const wantSuccessor = "example/renamed"
	rollup := `{"state":"SUCCESS","statuses":[{"context":"example/build","state":"success"}],"sha":"` + testHeadSHA + `"}`
	moved := map[string]string{
		"GET /api/v3/repositories/7691631/actions/runs":                           readCaptures(t).raw(t, "rest_workflow_runs_listing"),
		"GET /api/v3/repositories/7691631/labels":                                 `[{"name":"example-label"}]`,
		"GET /api/v3/repositories/7691631/pulls/1":                                `{"number":1,"state":"open","title":"t","merged":false,"html_url":"https://forge.example/example/renamed/pull/1"}`,
		"GET /api/v3/repositories/7691631/commits/" + testHeadSHA + "/status":     rollup,
		"GET /api/v3/repositories/7691631/commits/" + testHeadSHA + "/check-runs": `{"total_count":0,"check_runs":[]}`,
		"GET /api/v3/repositories/7691631":                                        `{"full_name":"example/renamed"}`,
	}
	for _, test := range []struct {
		call  func(*harness) (*forgeapi.RepoRef, error)
		name  string
		spent int
	}{
		{
			name: "Page_carries_it_and_the_rows_are_the_successors", spent: 3,
			call: func(h *harness) (*forgeapi.RepoRef, error) {
				page, err := h.client.ListLabels(t.Context(), testRef())
				return page.Successor, err
			},
		},
		{
			name: "the_run_listings_Page_carries_it_beside_the_rows", spent: 3,
			call: func(h *harness) (*forgeapi.RepoRef, error) {
				page, err := h.client.ListRuns(t.Context(), testRef())
				if err == nil && len(page.Items) != 2 {
					return nil, errors.New("ListRuns of a renamed repository answered " + strconv.Itoa(len(page.Items)) + " row(s), want the successor's 2")
				}
				return page.Successor, err
			},
		},
		{
			name: "the_reruns_error_carries_it_and_nothing_is_retried", spent: 3,
			call: func(h *harness) (*forgeapi.RepoRef, error) {
				err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA)
				var fe *forgeapi.Error
				if !asForgeError(err, &fe) {
					return nil, err
				}
				if fe.Code != forgeapi.CodeRepoRefStale {
					return nil, fe
				}
				return fe.Successor, nil
			},
		},
		{
			name: "MergeStatus_carries_it_on_the_degraded_arm", spent: 3,
			call: func(h *harness) (*forgeapi.RepoRef, error) {
				h.client.markDegraded()
				status, err := h.client.MergeStatus(t.Context(), testRef(), testPR())
				return status.Successor, err
			},
		},
		{
			name: "CommitChecks_carries_it_over_both_of_its_reads", spent: 5,
			call: func(h *harness) (*forgeapi.RepoRef, error) {
				h.client.markDegraded()
				checks, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA)
				return checks.Successor, err
			},
		},
		{
			name: "the_error_carries_it_where_the_answer_declares_none", spent: 3,
			call: func(h *harness) (*forgeapi.RepoRef, error) {
				_, err := h.client.RepoAffordances(t.Context(), testRef())
				var fe *forgeapi.Error
				if !asForgeError(err, &fe) {
					return nil, err
				}
				if fe.Code != forgeapi.CodeRepoRefStale {
					return nil, fe
				}
				return fe.Successor, nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := renamedInstance(t, moved, true, http.StatusMovedPermanently)
			got, err := test.call(h)
			if err != nil {
				t.Fatalf("%s = %v, want the rename reported rather than refused: %v", test.name, err, h.instance.arrived())
			}
			if got == nil {
				t.Fatalf("%s = no successor, want %q: the location is id-addressed and one read resolves its canonical name; the requests were %v",
					test.name, wantSuccessor, h.instance.arrived())
			}
			if got.Selector != wantSuccessor {
				t.Errorf("%s = successor %q, want %q", test.name, got.Selector, wantSuccessor)
			}
			if got.ID == "" || got.Family != forgeapi.FamilyGitHub {
				t.Errorf("%s = successor %+v, want a reference a consumer can store", test.name, got)
			}
			if spent := h.instance.count(); spent != test.spent {
				t.Errorf("%s sent %d request(s), want %d: the successor is resolved once however many reads the call makes: %v",
					test.name, spent, test.spent, h.instance.arrived())
			}
		})
	}
}

// TestARenameWhoseSuccessorCannotBeResolvedIsTheStaleCodeWithNone holds the other arm of
// every carrier: the move was detected and the read that resolves its canonical name did
// not answer, so no carrier gets a value it would be wrong about and the caller gets the
// reconnect-and-relist remedy instead.
func TestARenameWhoseSuccessorCannotBeResolvedIsTheStaleCodeWithNone(t *testing.T) {
	h := renamedInstance(t, map[string]string{
		"GET /api/v3/repositories/7691631/labels": `[{"name":"example-label"}]`,
	}, false, http.StatusMovedPermanently)
	page, err := h.client.ListLabels(t.Context(), testRef())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefStale {
		t.Fatalf("ListLabels against a rename whose successor is invisible = %v, want code %q", err, forgeapi.CodeRepoRefStale)
	}
	if fe.Successor != nil {
		t.Errorf("the refusal carries the successor %v, want none: the read that would name it was refused", fe.Successor)
	}
	if page.Successor != nil {
		t.Errorf("the page carries the successor %v, want none: this call answers the refusal rather than rows", page.Successor)
	}
	if !strings.Contains(fe.Message, "listing the repositories again") {
		t.Errorf("the refusal reads %q, want the remedy a consumer can act on", fe.Message)
	}
}

// TestARenameWhoseReadFailsAtTheSuccessorStillNamesTheSuccessor holds the witness on the
// failure arm: the followed hop is the rename once the read it led to answers,
// so a read the successor refuses, or answers with a body that does not decode, names
// the move as the stale code carrying the successor the one resolving read names, with
// that answer's own status, rather than answering that failure with the move unreported.
// The call spends the rename arm's price and no more.
func TestARenameWhoseReadFailsAtTheSuccessorStillNamesTheSuccessor(t *testing.T) {
	const labels = "GET /api/v3/repositories/7691631/labels"
	const pull = "GET /api/v3/repositories/7691631/pulls/1"
	for _, test := range []struct {
		read   func(*harness) error
		route  string
		body   string
		name   string
		status int
	}{
		{
			name: "a_list_the_successor_refuses", route: labels, body: `{"message":"Not Found"}`, status: http.StatusNotFound,
			read: func(h *harness) error { _, err := h.client.ListLabels(t.Context(), testRef()); return err },
		},
		{
			name: "a_list_whose_successor_answers_no_decodable_body", route: labels, body: `not a label list`, status: http.StatusOK,
			read: func(h *harness) error { _, err := h.client.ListLabels(t.Context(), testRef()); return err },
		},
		{
			name: "a_carrierless_read_the_successor_refuses", route: pull, body: `{"message":"Not Found"}`, status: http.StatusNotFound,
			read: func(h *harness) error {
				h.client.markDegraded()
				_, err := h.client.ReadPR(t.Context(), testRef(), testPR())
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := renamedInstance(t, map[string]string{test.route: test.body}, true, http.StatusMovedPermanently)
			h.instance.status(test.route, test.status)

			err := test.read(h)

			var fe *forgeapi.Error
			switch {
			case !asForgeError(err, &fe):
				t.Fatalf("%s = error %v, want a *forgeapi.Error: %v", test.name, err, h.instance.arrived())
			case fe.Code != forgeapi.CodeRepoRefStale:
				t.Errorf("%s = code %q (status %d), want %q: the hop was followed, so the repository moved", test.name, fe.Code, fe.Status, forgeapi.CodeRepoRefStale)
			case fe.Successor == nil || fe.Successor.Selector != "example/renamed":
				t.Errorf("%s = successor %v, want example/renamed", test.name, fe.Successor)
			case fe.Status != test.status:
				t.Errorf("%s = status %d, want %d, the successor's own answer", test.name, fe.Status, test.status)
			}
			if spent := h.instance.count(); spent != 3 {
				t.Errorf("%s sent %d request(s), want 3, the read, its hop and the one resolving read: %v", test.name, spent, h.instance.arrived())
			}
		})
	}
}

// TestASingleReadOfARenamedRepositoryRefusesWithTheSuccessor is the carrier-less read of
// the pair that shares the degraded record: the pull request answers no successor field,
// so the value rides the error, which is the carrier the architecture names as the
// general remedy.
func TestASingleReadOfARenamedRepositoryRefusesWithTheSuccessor(t *testing.T) {
	h := renamedInstance(t, map[string]string{
		"GET /api/v3/repositories/7691631/pulls/1": `{"number":1,"state":"open","title":"t","merged":false}`,
	}, true, http.StatusMovedPermanently)
	h.client.markDegraded()
	_, err := h.client.ReadPR(t.Context(), testRef(), testPR())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefStale {
		t.Fatalf("ReadPR against a renamed repository = %v, want code %q", err, forgeapi.CodeRepoRefStale)
	}
	if fe.Successor == nil || fe.Successor.Selector != "example/renamed" {
		t.Errorf("the refusal carries the successor %v, want example/renamed", fe.Successor)
	}
}

// TestAMutationOfARenamedRepositoryRefusesWithTheSuccessor holds the mutation arm, which
// is refused rather than answered by decision: re-issuing a creation or a merge against
// the successor is the caller's choice, and what this library owes it is the successor to
// make it with.
//
// The two statuses are two different hazards and only one of them can name a successor.
// A hop that PRESERVES the method reaches this family, so the location is in hand and the
// further read resolves the name. A hop net/http would rewrite into a bodyless read is
// refused by the transport's own policy before this family sees anything, because a merge
// against a renamed repository would otherwise answer success from a read that merged
// nothing, and there the refusal is all there is.
func TestAMutationOfARenamedRepositoryRefusesWithTheSuccessor(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        int
		wantSuccessor string
	}{
		{name: "a_hop_that_preserves_the_method", status: http.StatusTemporaryRedirect, wantSuccessor: "example/renamed"},
		{name: "a_hop_net_http_would_rewrite_into_a_read", status: http.StatusMovedPermanently, wantSuccessor: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := renamedInstance(t, map[string]string{
				"POST /api/v3/repositories/7691631/issues": `{"number":1,"state":"open","title":"t"}`,
			}, true, test.status)
			_, err := h.client.CreateIssue(t.Context(), testRef(), forgeapi.NewIssue{Title: "t"})
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefStale {
				t.Fatalf("CreateIssue against a renamed repository = %v, want code %q", err, forgeapi.CodeRepoRefStale)
			}
			got := ""
			if fe.Successor != nil {
				got = fe.Successor.Selector
			}
			if got != test.wantSuccessor {
				t.Errorf("CreateIssue against a %d = successor %q, want %q", test.status, got, test.wantSuccessor)
			}
		})
	}
}

// TestTheIssuesListDropsTheRowsThatArePullRequests holds the one filter this product's
// issue list needs: that route's population is issues and pull requests both, and every
// pull-request row carries a key naming its own pull request.
func TestTheIssuesListDropsTheRowsThatArePullRequests(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		issuesRoute: `[` + recorded.raw(t, "rest_issue") + `,` + recorded.raw(t, "rest_issue_that_is_a_pull_request") + `]`,
	})
	page, err := h.client.ListIssues(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListIssues = %v, want the page", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListIssues answered %d row(s), want 1: the other row of the recorded page is a pull request", len(page.Items))
	}
	if got := page.Items[0].Ref.Number; got != 1 {
		t.Errorf("ListIssues answered issue %d, want the issue rather than the pull request", got)
	}
}

// TestTheBudgetIsReadFromTheResponseAndNeverFromTheRateLimitEndpoint holds the budget's
// source at the family's grain: the figure comes from the signal riding the answer, and no
// operation of this family reaches the endpoint that reports a full pool with a fresh
// window while the same credential's document budget is drawn down.
func TestTheBudgetIsReadFromTheResponseAndNeverFromTheRateLimitEndpoint(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{reposRoute: `[` + recorded.raw(t, "rest_repo_listing_row") + `]`})
	h.instance.answerHeaders(reposRoute, http.Header{
		"X-Ratelimit-Remaining": []string{"4990"},
		"X-Ratelimit-Reset":     []string{"1790000000"},
		"X-Ratelimit-Resource":  []string{"core"},
	})
	if got := h.client.BudgetState().Remaining; got != forgeapi.BudgetRemainingUnknown {
		t.Errorf("BudgetState before any call = %d, want %d: a connection that has sent nothing has no figure", got, forgeapi.BudgetRemainingUnknown)
	}
	if _, err := h.client.ListRepos(t.Context()); err != nil {
		t.Fatalf("Setup: ListRepos = %v", err)
	}
	got := h.client.BudgetState()
	if got.Remaining != 4990 {
		t.Errorf("BudgetState = remaining %d, want the 4990 the answer's own header carried", got.Remaining)
	}
	if got.Reset.IsZero() {
		t.Error("BudgetState carries no reset instant, want the one the header names")
	}
	if got.LastCost != 1 {
		t.Errorf("BudgetState = last cost %d, want 1: a REST call is one unit of this product's core resource", got.LastCost)
	}
	if got.RotationCursor != "" {
		t.Errorf("BudgetState = rotation cursor %q, want empty: nothing on this family spends a per-row fold budget, so there is no position to keep", got.RotationCursor)
	}
	for _, arrived := range h.instance.arrived() {
		if strings.Contains(arrived, "rate_limit") {
			t.Errorf("an operation reached %q, want no read of the rate-limit endpoint: it is measured to report a full pool while the budget is gone", arrived)
		}
	}
}

// TestAListWhoseBoundAdmitsNoPageIssuesNothing holds the literal zero the page-bound
// option publishes: a list that sent one page anyway would do the opposite of what its
// own option says, and the continuation names the page nobody asked for so the remainder
// stays reachable.
func TestAListWhoseBoundAdmitsNoPageIssuesNothing(t *testing.T) {
	h := newHarness(t, nil, forgeapi.WithListPages(0))
	page, err := h.client.ListLabels(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListLabels with a zero page bound = %v, want the empty page", err)
	}
	if got := h.instance.count(); got != 0 {
		t.Errorf("ListLabels with a zero page bound sent %d request(s), want 0", got)
	}
	if page.Partial == nil || page.Partial.Reason != forgeapi.PartialPaginationCap {
		t.Errorf("ListLabels with a zero page bound = partial %v, want the pagination cap", page.Partial)
	}
	if page.Next == "" {
		t.Error("ListLabels with a zero page bound carries no continuation, want the page nobody asked for")
	}
}

// TestACrossRepositoryRowCarriesItsOwnRepository holds the addressing that makes those
// rows actionable, which is the whole reason the search document selects a repository per
// row: the call names none.
func TestACrossRepositoryRowCarriesItsOwnRepository(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		documentRoute: documentEnvelope(`"search":{"issueCount":1,"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[` +
			recorded.raw(t, "graphql_search_row") + `]}`),
	})
	page, err := h.client.ListMyPRs(t.Context())
	if err != nil {
		t.Fatalf("ListMyPRs = %v, want the page", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListMyPRs answered %d row(s), want 1", len(page.Items))
	}
	row := page.Items[0]
	if row.Repo.Selector != testSelector {
		t.Errorf("ListMyPRs row 0 = repository %q, want %q: the row carries its own", row.Repo.Selector, testSelector)
	}
	if row.Repo.ID != testRef().ID {
		t.Errorf("ListMyPRs row 0 = id %q, want the derived %q", row.Repo.ID, testRef().ID)
	}
	if got := h.instance.count(); got != 1 {
		t.Errorf("ListMyPRs sent %d request(s), want 1: the price moves with the page rather than with how many repositories the credential can reach", got)
	}
}

// TestACrossRepositoryIssueRowCarriesItsOwnRepository holds the issue twin of the row
// above to the same addressing and the same price, and to the search it sends: issue
// rows, under the scope it was asked for.
func TestACrossRepositoryIssueRowCarriesItsOwnRepository(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
		opts []forgeapi.ListOption
	}{
		{name: "the_credentials_own", want: "type:issue author:@me state:open"},
		{name: "under_an_owner", want: "type:issue user:example-org state:open", opts: []forgeapi.ListOption{forgeapi.WithOwner("example-org")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: searchEnvelope(t, "graphql_issue_search_row")})
			page, err := h.client.ListMyIssues(t.Context(), test.opts...)
			if err != nil {
				t.Fatalf("ListMyIssues = %v, want the page", err)
			}
			if len(page.Items) != 1 {
				t.Fatalf("ListMyIssues answered %d row(s), want 1", len(page.Items))
			}
			if row := page.Items[0]; row.Repo != testRef() {
				t.Errorf("ListMyIssues row 0 = repository %+v, want %+v: the row carries its own", row.Repo, testRef())
			}
			if got := h.instance.count(); got != 1 {
				t.Errorf("ListMyIssues sent %d request(s), want 1", got)
			}
			body := h.instance.body(documentRoute)
			if !strings.Contains(body, `"operationName":"IssueMine"`) {
				t.Errorf("ListMyIssues posted %s, want the IssueMine document", body)
			}
			if !strings.Contains(body, `"q":"`+test.want+`"`) {
				t.Errorf("ListMyIssues posted %s, want the search %q", body, test.want)
			}
		})
	}
}

// TestACrossRepositoryPageResumesFromTheSearchCursor holds both cross-repository
// lists to the continuation their search connection names: a page whose connection
// reports a next page hands over a continuation carrying its end cursor, and the call that resumes from it
// sends that cursor back as the document's own position. A list that dropped it would
// answer every page as the first.
func TestACrossRepositoryPageResumesFromTheSearchCursor(t *testing.T) {
	const cursor = "Y3Vyc29yOjIw"
	for _, test := range []struct {
		list func(*harness, ...forgeapi.ListOption) (forgeapi.Cursor, error)
		name string
		row  string
	}{
		{name: "PullRequests.ListMyPRs", row: "graphql_search_row", list: func(h *harness, opts ...forgeapi.ListOption) (forgeapi.Cursor, error) {
			page, err := h.client.ListMyPRs(t.Context(), opts...)
			return page.Next, err
		}},
		{name: "Issues.ListMyIssues", row: "graphql_issue_search_row", list: func(h *harness, opts ...forgeapi.ListOption) (forgeapi.Cursor, error) {
			page, err := h.client.ListMyIssues(t.Context(), opts...)
			return page.Next, err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				documentRoute: documentEnvelope(`"search":{"issueCount":2,"pageInfo":{"hasNextPage":true,"endCursor":"` + cursor + `"},"nodes":[` +
					readCaptures(t).raw(t, test.row) + `]}`),
			})
			next, err := test.list(h)
			if err != nil {
				t.Fatalf("%s = %v, want the first page", test.name, err)
			}
			if next == "" || forgeapi.ValidateCursor(next) != nil {
				t.Fatalf("%s = next %q, want a continuation this library's own validator admits: the connection reported a next page", test.name, next)
			}
			if _, err := test.list(h, forgeapi.WithAfter(next)); err != nil {
				t.Fatalf("%s(WithAfter(%q)) = %v, want the next page", test.name, next, err)
			}
			if body := h.instance.body(documentRoute); !strings.Contains(body, `"after":"`+cursor+`"`) {
				t.Errorf("%s(WithAfter(%q)) posted %s, want the variable after carrying %q", test.name, next, body, cursor)
			}
		})
	}
}

// TestTheRunListingReadsEachWorkflowRun holds the run listing to the route the re-run
// resolves its run on and to the row it publishes: the page bound as the page size,
// no head filter, one request, each run normalized from its own record, and the link
// header's next relation as the continuation.
func TestTheRunListingReadsEachWorkflowRun(t *testing.T) {
	h := newHarness(t, map[string]string{runsRoute: readCaptures(t).raw(t, "rest_workflow_runs_listing")})
	h.instance.answerHeaders(runsRoute, http.Header{
		headerLink: []string{`<https://forge.example/repositories/7691631/actions/runs?per_page=30&page=2>; rel="next"`},
	})
	page, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithPageBound(30))
	if err != nil {
		t.Fatalf("ListRuns = %v, want the page", err)
	}
	if got := h.instance.count(); got != 1 {
		t.Errorf("ListRuns sent %d request(s), want 1: %v", got, h.instance.arrived())
	}
	if got := h.instance.query(runsRoute, keyPerPage); got != "30" {
		t.Errorf("ListRuns sent per_page %q, want the page bound 30", got)
	}
	if got := h.instance.query(runsRoute, keyPage); got != "1" {
		t.Errorf("ListRuns sent page %q, want 1 on a first page", got)
	}
	if got := h.instance.query(runsRoute, "head_sha"); got != "" {
		t.Errorf("ListRuns sent head_sha %q, want none: the listing answers every run of the repository", got)
	}
	want := []forgeapi.Run{
		{
			Repo: testRef(), Name: "example", Branch: "example-feature", HeadSHA: testHeadSHA,
			WebURL:    "https://forge.example/example/example/actions/runs/1",
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			State: forgeapi.CheckFailing,
		},
		{
			Repo: testRef(), Name: "example-security", Branch: "example-feature", HeadSHA: testHeadSHA,
			WebURL:    "https://forge.example/example/example/actions/runs/2",
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			State: forgeapi.CheckPassing,
		},
	}
	if len(page.Items) != len(want) {
		t.Fatalf("ListRuns answered %d row(s), want %d", len(page.Items), len(want))
	}
	for i := range want {
		if page.Items[i] != want[i] {
			t.Errorf("ListRuns row %d = %+v, want %+v", i, page.Items[i], want[i])
		}
	}
	if want := restCursor(t, h.client, "ListRuns", testRef(), 2, forgeapi.WithPageBound(30)); page.Next != want {
		t.Errorf("ListRuns = next %q, want %q from the link header's next relation", page.Next, want)
	}
	if page.Successor != nil {
		t.Errorf("ListRuns = successor %+v, want none: the route answered from the address it was given", page.Successor)
	}
}

// TestTheWorkflowRunVocabularyFoldsTotally holds the run's verdict to the vocabulary
// the runs route's own status filter documents, every member of which is a check
// run's status or conclusion: a run still in flight is pending whatever its status
// member, a completed one is its conclusion's verdict, and a value outside the table
// is unknown, counted and named.
func TestTheWorkflowRunVocabularyFoldsTotally(t *testing.T) {
	for _, test := range []struct {
		status     string
		conclusion string
		want       forgeapi.CheckState
	}{
		{status: "queued", want: forgeapi.CheckPending},
		{status: "in_progress", want: forgeapi.CheckPending},
		{status: "requested", want: forgeapi.CheckPending},
		{status: "waiting", want: forgeapi.CheckPending},
		{status: "pending", want: forgeapi.CheckPending},
		{status: "completed", conclusion: "success", want: forgeapi.CheckPassing},
		{status: "completed", conclusion: "failure", want: forgeapi.CheckFailing},
		{status: "completed", conclusion: "timed_out", want: forgeapi.CheckFailing},
		{status: "completed", conclusion: "startup_failure", want: forgeapi.CheckFailing},
		{status: "completed", conclusion: "action_required", want: forgeapi.CheckFailing},
		{status: "completed", conclusion: "neutral", want: forgeapi.CheckNeutral},
		{status: "completed", conclusion: "skipped", want: forgeapi.CheckNeutral},
		{status: "completed", conclusion: "cancelled", want: forgeapi.CheckNeutral},
		{status: "completed", conclusion: "stale", want: forgeapi.CheckNeutral},
	} {
		h := newHarness(t, nil)
		run := h.client.normalizeRun(&restWorkflowRun{Status: test.status, Conclusion: test.conclusion}, testRef())
		if run.State != test.want {
			t.Errorf("normalizeRun(status %q, conclusion %q) = state %v, want %v", test.status, test.conclusion, run.State, test.want)
		}
		if n := h.spy.times("UnknownEnumValue:workflow run (rest) status") + h.spy.times("UnknownEnumValue:workflow run (rest) conclusion"); n != 0 {
			t.Errorf("normalizeRun(status %q, conclusion %q) counted %d unmapped value(s), want 0: the member is documented", test.status, test.conclusion, n)
		}
	}
	h := newHarness(t, nil)
	run := h.client.normalizeRun(&restWorkflowRun{Status: "completed", Conclusion: "teleported"}, testRef())
	if run.State != forgeapi.CheckUnknown {
		t.Errorf("normalizeRun(an undocumented conclusion) = state %v, want %v", run.State, forgeapi.CheckUnknown)
	}
	if n := h.spy.times("UnknownEnumValue:workflow run (rest) conclusion"); n != 1 {
		t.Errorf("normalizeRun(an undocumented conclusion) counted %d unmapped value(s), want 1", n)
	}
	if !h.logged("teleported") {
		t.Error("normalizeRun(an undocumented conclusion) logged no line naming the value, want one")
	}
}

// TestADocumentThatResolvedNoRepositoryIsSeparatedFromOneWithNoPullRequest holds the two
// absence answers the envelope separates at no further request, which one REST status
// could not tell apart.
func TestADocumentThatResolvedNoRepositoryIsSeparatedFromOneWithNoPullRequest(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "a_null_repository_is_invisible",
			body: documentEnvelope(`"repository":null`),
			want: forgeapi.CodeRepoOrPRNotVisible,
		},
		{
			name: "a_resolved_repository_with_no_pull_request_is_a_number_that_does_not_exist",
			body: documentEnvelope(`"repository":{"nameWithOwner":"example/example","pullRequest":null}`),
			want: forgeapi.CodePRNotFound,
		},
		{
			name: "an_envelope_answering_neither_data_nor_errors_resolves_nothing",
			body: `{"data":null}`,
			want: forgeapi.CodeRepoOrPRNotVisible,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: test.body})
			_, err := h.client.ReadPR(t.Context(), testRef(), testPR())
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != test.want {
				t.Fatalf("ReadPR = %v, want code %q", err, test.want)
			}
			if h.client.isDegraded() {
				t.Error("the connection is degraded after an ABSENCE, want it not to be: nothing about the document was refused")
			}
		})
	}
}

// TestADocumentRenamedUnderTheSelectorIsMarkedFromTheCanonicalName holds the rename
// witness on the arm that has no redirect to record: the selection resolves a renamed
// repository silently at 200, so the canonical name the document answers is what marks
// the move, and it costs no further read.
func TestADocumentRenamedUnderTheSelectorIsMarkedFromTheCanonicalName(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		documentRoute: documentEnvelope(`"repository":{"nameWithOwner":"moved/elsewhere","viewerPermission":"READ",` +
			`"pullRequests":{"totalCount":1,"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[` +
			recorded.raw(t, "graphql_search_row") + `]}}`),
	})
	page, err := h.client.ListPRs(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListPRs = %v, want the page", err)
	}
	if page.Successor == nil || page.Successor.Selector != "moved/elsewhere" {
		t.Fatalf("ListPRs = successor %v, want the canonical name the document answered", page.Successor)
	}
	if page.Successor.ID != repoRef("moved/elsewhere").ID {
		t.Errorf("the successor's id = %q, want the derived one", page.Successor.ID)
	}
	if got := h.instance.count(); got != 1 {
		t.Errorf("ListPRs sent %d request(s), want 1: on this arm the successor costs no read of its own", got)
	}
}

// TestEveryOperationRefusesAReferenceItCannotInterpolate holds the one check that runs
// before any request on every repository-addressed operation: a derived identifier is
// consumer-controlled input on its way into a URL.
func TestEveryOperationRefusesAReferenceItCannotInterpolate(t *testing.T) {
	bad := forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "example/../etc"}
	h := newHarness(t, nil)
	for name, call := range map[string]func() error{
		"ListPRs":  func() error { _, err := h.client.ListPRs(t.Context(), bad); return err },
		"ReadPR":   func() error { _, err := h.client.ReadPR(t.Context(), bad, testPR()); return err },
		"CreatePR": func() error { _, err := h.client.CreatePR(t.Context(), bad, forgeapi.NewPullRequest{}); return err },
		"ClosePR":  func() error { _, err := h.client.ClosePR(t.Context(), bad, testPR()); return err },
		"MergePR": func() error {
			_, err := h.client.MergePR(t.Context(), bad, testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA})
			return err
		},
		"MergeStatus": func() error { _, err := h.client.MergeStatus(t.Context(), bad, testPR()); return err },
		"CommitStatus": func() error {
			_, err := h.client.CommitStatus(t.Context(), bad, testHeadSHA)
			return err
		},
		"ListIssues":  func() error { _, err := h.client.ListIssues(t.Context(), bad); return err },
		"CreateIssue": func() error { _, err := h.client.CreateIssue(t.Context(), bad, forgeapi.NewIssue{}); return err },
		"CloseIssue": func() error {
			_, err := h.client.CloseIssue(t.Context(), bad, forgeapi.IssueRef{Number: 1})
			return err
		},
		"ListReleases":    func() error { _, err := h.client.ListReleases(t.Context(), bad); return err },
		"CreateRelease":   func() error { _, err := h.client.CreateRelease(t.Context(), bad, forgeapi.NewRelease{}); return err },
		"ListLabels":      func() error { _, err := h.client.ListLabels(t.Context(), bad); return err },
		"RepoAffordances": func() error { _, err := h.client.RepoAffordances(t.Context(), bad); return err },
		"RerunFailedChecks": func() error {
			return h.client.RerunFailedChecks(t.Context(), bad, testPR(), testHeadSHA)
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefInvalid {
				t.Errorf("%s with a dot-dot selector = %v, want code %q", name, err, forgeapi.CodeRepoRefInvalid)
			}
		})
	}
	if got := h.instance.count(); got != 0 {
		t.Errorf("the refused references sent %d request(s), want 0", got)
	}
}
