package gitlab

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
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

func TestAReRunWithNoHeadReRunsTheFirstPipeline(t *testing.T) {
	h := newHarness(t, map[string]string{
		metadataRoute:  metadataBody,
		documentRoute:  docAcceptanceAnswer,
		pipelinesRoute: pipelinesBody,
		retryRoute:     `{}`,
	})
	h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
	if err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), ""); err != nil {
		t.Fatalf("RerunFailedChecks with no head = %v, want the retry", err)
	}
	if got := h.instance.query(pipelinesRoute, "sha"); got != "" {
		t.Errorf("the resolving read sent sha %q, want no filter where the forge reported no head", got)
	}
	if arrived := h.instance.arrived(); !reflect.DeepEqual(arrived[len(arrived)-1:], []string{retryRoute}) {
		t.Errorf("RerunFailedChecks with no head ended on %v, want the retry of the first pipeline, %s", arrived, retryRoute)
	}
}
