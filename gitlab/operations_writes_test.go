package gitlab

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

const (
	createPRRoute      = "POST /api/v4/projects/" + testEncoded + "/merge_requests"
	pullStateRoute     = "PUT /api/v4/projects/" + testEncoded + "/merge_requests/1"
	createIssueRoute   = "POST /api/v4/projects/" + testEncoded + "/issues"
	issueStateRoute    = "PUT /api/v4/projects/" + testEncoded + "/issues/1"
	createReleaseRoute = "POST /api/v4/projects/" + testEncoded + "/releases"
)

func sentBody(t *testing.T, h *harness, route string) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(h.instance.body(route)), &body); err != nil {
		t.Fatalf("Setup: decoding the body %s received (%q): %v", route, h.instance.body(route), err)
	}
	return body
}

func TestACreationSendsLabelsOnlyWhereTheCallerNamedThem(t *testing.T) {
	for _, test := range []struct {
		create func(h *harness, labels []string) (string, error)
		name   string
		route  string
		answer string
		want   string
	}{
		{
			name: "CreatePR", route: createPRRoute, answer: restMergeRequestBody, want: "Example pull request",
			create: func(h *harness, labels []string) (string, error) {
				pr, err := h.client.CreatePR(t.Context(), testRef(), forgeapi.NewPullRequest{
					Title: "Example pull request", SourceBranch: "example-feature", TargetBranch: "main", Labels: labels,
				})
				return pr.Title, err
			},
		},
		{
			name: "CreateIssue", route: createIssueRoute, answer: issueBody, want: "Example issue",
			create: func(h *harness, labels []string) (string, error) {
				issue, err := h.client.CreateIssue(t.Context(), testRef(), forgeapi.NewIssue{Title: "Example issue", Labels: labels})
				return issue.Title, err
			},
		},
	} {
		for name, labels := range map[string][]string{"with_labels": {"example-label"}, "without_labels": nil} {
			t.Run(test.name+"_"+name, func(t *testing.T) {
				h := newHarness(t, map[string]string{test.route: test.answer})
				title, err := test.create(h, labels)
				if err != nil {
					t.Fatalf("%s = %v, want the created record", test.name, err)
				}
				if title != test.want {
					t.Errorf("%s = title %q, want %q from the instance's answer", test.name, title, test.want)
				}
				sent, named := sentBody(t, h, test.route)[keyLabels]
				if named != (labels != nil) {
					t.Errorf("%s with labels %v sent a labels key %v (%v), want %v", test.name, labels, named, sent, labels != nil)
				}
			})
		}
	}
}

func TestALifecycleTransitionSendsItsStateEventAndAnswersTheRecord(t *testing.T) {
	closedPR := strings.Replace(restMergeRequestBody, `"state": "opened"`, `"state": "closed"`, 1)
	closedIssue := strings.Replace(issueBody, `"state": "opened"`, `"state": "closed"`, 1)
	t.Run("ClosePR", func(t *testing.T) {
		h := newHarness(t, map[string]string{pullStateRoute: closedPR})
		pr, err := h.client.ClosePR(t.Context(), testRef(), testPR())
		if err != nil {
			t.Fatalf("ClosePR = %v, want the pull request", err)
		}
		if pr.State != forgeapi.PRStateClosed {
			t.Errorf("ClosePR = state %v, want %v", pr.State, forgeapi.PRStateClosed)
		}
		if got := sentBody(t, h, pullStateRoute)[keyStateEvent]; got != "close" {
			t.Errorf("ClosePR sent state event %v, want close", got)
		}
	})
	t.Run("CloseIssue", func(t *testing.T) {
		h := newHarness(t, map[string]string{issueStateRoute: closedIssue})
		issue, err := h.client.CloseIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 1})
		if err != nil {
			t.Fatalf("CloseIssue = %v, want the issue", err)
		}
		if issue.State != forgeapi.IssueStateClosed || issue.Title != "Example issue" {
			t.Errorf("CloseIssue = state %v title %q, want %v and the instance's title", issue.State, issue.Title, forgeapi.IssueStateClosed)
		}
		if got := sentBody(t, h, issueStateRoute)[keyStateEvent]; got != "close" {
			t.Errorf("CloseIssue sent state event %v, want close", got)
		}
	})
}

func TestAReleaseIsCutFromTheRefItNamesOrTheDefault(t *testing.T) {
	for _, test := range []struct {
		want   any
		name   string
		target string
	}{
		{name: "a_named_target", target: "main", want: "main"},
		{name: "no_target"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{createReleaseRoute: releaseBody})
			release, err := h.client.CreateRelease(t.Context(), testRef(), forgeapi.NewRelease{TagName: "v1.0.0", Target: test.target})
			if err != nil {
				t.Fatalf("CreateRelease = %v, want the release", err)
			}
			if release.TagName != "v1.0.0" || release.Name != "Example release" {
				t.Errorf("CreateRelease = tag %q name %q, want the instance's answer", release.TagName, release.Name)
			}
			if got := sentBody(t, h, createReleaseRoute)["ref"]; got != test.want {
				t.Errorf("CreateRelease with target %q sent ref %v, want %v", test.target, got, test.want)
			}
		})
	}
}

func TestAMergeSendsTheSquashFlagItsIntentNames(t *testing.T) {
	for _, test := range []struct {
		name   string
		intent forgeapi.MergeIntent
		squash bool
	}{
		{name: "squash", intent: forgeapi.IntentSquash, squash: true},
		{name: "no_squash", intent: forgeapi.IntentNoSquash},
		{name: "the_repository_default", intent: forgeapi.IntentDefault},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{mergeRoute: mergedBody})
			if _, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{
				HeadSHA: testHeadSHA, Intent: test.intent,
			}); err != nil {
				t.Fatalf("MergePR = %v, want the outcome", err)
			}
			if got := sentBody(t, h, mergeRoute)["squash"]; got != test.squash {
				t.Errorf("MergePR with intent %v sent squash %v, want %v", test.intent, got, test.squash)
			}
		})
	}
}

// headPipelineRequest is a merge request on head whose head pipeline is pipeline,
// at a target project the addressed one's id names, or none where target is 0.
func headPipelineRequest(head string, target int, pipeline string) string {
	body := `{"iid": 1, "sha": "` + head + `", "source_project_id": 1`
	if target != 0 {
		body += `, "target_project_id": ` + strconv.Itoa(target)
	}
	return body + `, "head_pipeline": ` + pipeline + `}`
}

// headPipeline is one head pipeline as the merge request read answers it, with no
// project id where project is 0.
func headPipeline(project int, sha, ref, status string) string {
	body := `{"id": 77`
	if project != 0 {
		body += `, "project_id": ` + strconv.Itoa(project)
	}
	return body + `, "sha": "` + sha + `", "ref": "` + ref + `", "status": "` + status + `"}`
}

// The re-run retries the merge request's head pipeline, the one its checks verdict
// reads: on merged results it ran on a merge commit of the head, on a fork's merge
// request in the fork's project, and neither is a row of the project's pipelines on
// the head. Every refusal is sent before any write, and no pipeline listing is read.
func TestTheReRunRetriesTheMergeRequestsHeadPipeline(t *testing.T) {
	const other = "0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f60718293"
	const forkRetryRoute = "POST /api/v4/projects/99/pipelines/77/retry"
	const targetRetryRoute = "POST /api/v4/projects/1/pipelines/77/retry"
	failedOnHead := headPipeline(1, testHeadSHA, "example-feature", "failed")
	for _, test := range []struct {
		name, pin, request, retry, code string
		kind                            forgeapi.ErrorKind
	}{
		{name: "a_failed_branch_pipeline_on_the_pin", pin: testHeadSHA, request: headPipelineRequest(testHeadSHA, 1, failedOnHead), retry: retryRoute},
		{name: "no_pin_takes_the_merge_requests_head", request: headPipelineRequest(testHeadSHA, 1, failedOnHead), retry: retryRoute},
		{name: "a_canceled_pipeline", pin: testHeadSHA, request: headPipelineRequest(testHeadSHA, 1, headPipeline(1, testHeadSHA, "example-feature", "canceled")), retry: retryRoute},
		{name: "a_merged_results_pipeline_on_a_merge_commit", pin: testHeadSHA, request: headPipelineRequest(testHeadSHA, 1, headPipeline(1, other, "refs/merge-requests/1/merge", "failed")), retry: retryRoute},
		{name: "a_merge_train_pipeline", pin: testHeadSHA, request: headPipelineRequest(testHeadSHA, 1, headPipeline(1, other, "refs/merge-requests/1/train", "failed")), retry: retryRoute},
		{name: "a_fork_pipeline_in_the_forks_project", pin: testHeadSHA, request: headPipelineRequest(testHeadSHA, 1, headPipeline(99, testHeadSHA, "example-feature", "failed")), retry: forkRetryRoute},
		{name: "a_pipeline_naming_no_project", pin: testHeadSHA, request: headPipelineRequest(testHeadSHA, 1, headPipeline(0, testHeadSHA, "example-feature", "failed")), retry: retryRoute},
		{name: "a_merge_request_naming_no_target_project", pin: testHeadSHA, request: headPipelineRequest(testHeadSHA, 0, failedOnHead), retry: targetRetryRoute},
		{name: "a_pin_the_merge_request_moved_past", pin: other, request: headPipelineRequest(testHeadSHA, 1, failedOnHead), code: forgeapi.CodeStaleHead, kind: forgeapi.KindConflict},
		{name: "a_head_pipeline_that_passed", pin: testHeadSHA, request: headPipelineRequest(testHeadSHA, 1, headPipeline(1, testHeadSHA, "example-feature", "success")), code: forgeapi.CodeRunNotFailed, kind: forgeapi.KindConflict},
		{name: "a_running_head_pipeline", pin: testHeadSHA, request: headPipelineRequest(testHeadSHA, 1, headPipeline(1, testHeadSHA, "example-feature", "running")), code: forgeapi.CodeRunNotFailed, kind: forgeapi.KindConflict},
		{name: "no_head_pipeline", pin: testHeadSHA, request: headPipelineRequest(testHeadSHA, 1, "null"), code: forgeapi.CodeRunNotFailed, kind: forgeapi.KindConflict},
		{name: "a_head_pipeline_of_an_earlier_commit", pin: testHeadSHA, request: headPipelineRequest(testHeadSHA, 1, headPipeline(1, other, "example-feature", "failed")), code: forgeapi.CodeRunNotFailed, kind: forgeapi.KindConflict},
		{name: "an_earlier_commits_pipeline_on_the_head_ref", pin: testHeadSHA, request: headPipelineRequest(testHeadSHA, 1, headPipeline(1, other, "refs/merge-requests/1/head", "failed")), code: forgeapi.CodeRunNotFailed, kind: forgeapi.KindConflict},
		{name: "another_merge_requests_merge_ref", pin: testHeadSHA, request: headPipelineRequest(testHeadSHA, 1, headPipeline(1, other, "refs/merge-requests/2/merge", "failed")), code: forgeapi.CodeRunNotFailed, kind: forgeapi.KindConflict},
		{name: "a_merge_request_with_no_head", request: headPipelineRequest("", 1, failedOnHead), code: forgeapi.CodeMissingSHA, kind: forgeapi.KindUpstream},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				metadataRoute:    metadataBody,
				documentRoute:    docAcceptanceAnswer,
				readRoute:        test.request,
				pipelinesRoute:   pipelinesBody,
				retryRoute:       `{}`,
				forkRetryRoute:   `{}`,
				targetRetryRoute: `{}`,
			})
			h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
			if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
				t.Fatalf("Setup: ConnectionCaps = %v", err)
			}
			before := h.instance.count()
			err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), test.pin)
			sent := h.instance.arrived()[before:]
			if test.code == "" {
				if err != nil {
					t.Fatalf("RerunFailedChecks = %v, want the retry", err)
				}
				if want := []string{readRoute, test.retry}; !slices.Equal(sent, want) {
					t.Errorf("RerunFailedChecks sent %v, want %v", sent, want)
				}
				return
			}
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != test.code || fe.Kind != test.kind {
				t.Errorf("RerunFailedChecks = %v, want code %q on %v", err, test.code, test.kind)
			}
			if want := []string{readRoute}; !slices.Equal(sent, want) {
				t.Errorf("RerunFailedChecks sent %v, want %v alone and no retry", sent, want)
			}
		})
	}
}
