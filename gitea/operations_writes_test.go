package gitea

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

const (
	pullRoute     = "GET /api/v1/repos/example/example/pulls/1"
	patchPull     = "PATCH /api/v1/repos/example/example/pulls/1"
	createPull    = "POST /api/v1/repos/example/example/pulls"
	labelsRoute   = "GET /api/v1/repos/example/example/labels"
	createRelease = "POST /api/v1/repos/example/example/releases"
)

func sentBody(t *testing.T, h *harness, route string) map[string]any {
	t.Helper()
	body := map[string]any{}
	if err := json.Unmarshal([]byte(h.instance.body(route)), &body); err != nil {
		t.Fatalf("Setup: decoding the body %s received, %q: %v", route, h.instance.body(route), err)
	}
	return body
}

func TestANumberBelowOneIsRefusedBeforeAnyRequest(t *testing.T) {
	zero := forgeapi.PRRef{Number: 0, Sigil: "#"}
	for _, test := range []struct {
		call func(h *harness) error
		name string
	}{
		{name: "ReadPR", call: func(h *harness) error { _, err := h.client.ReadPR(t.Context(), testRef(), zero); return err }},
		{name: "MergeStatus", call: func(h *harness) error { _, err := h.client.MergeStatus(t.Context(), testRef(), zero); return err }},
		{name: "ClosePR", call: func(h *harness) error { _, err := h.client.ClosePR(t.Context(), testRef(), zero); return err }},
		{name: "ReopenPR", call: func(h *harness) error { _, err := h.client.ReopenPR(t.Context(), testRef(), zero); return err }},
		{name: "MergePR", call: func(h *harness) error {
			_, err := h.client.MergePR(t.Context(), testRef(), zero, forgeapi.MergeRequest{HeadSHA: testHeadSHA})
			return err
		}},
		{name: "CloseIssue", call: func(h *harness) error {
			_, err := h.client.CloseIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 0})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, nil)
			err := test.call(h)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefInvalid {
				t.Errorf("%s(number 0) = %v, want code %q", test.name, err, forgeapi.CodeRepoRefInvalid)
			}
			if sent := h.instance.arrived(); len(sent) != 0 {
				t.Errorf("%s(number 0) sent %v, want nothing", test.name, sent)
			}
		})
	}
}

func TestClosingOrReopeningAPullRequestSendsTheStateAskedFor(t *testing.T) {
	for _, test := range []struct {
		call  func(h *harness) (forgeapi.PullRequest, error)
		name  string
		state string
	}{
		{name: "ClosePR", state: stateClosed, call: func(h *harness) (forgeapi.PullRequest, error) {
			return h.client.ClosePR(t.Context(), testRef(), testPR())
		}},
		{name: "ReopenPR", state: stateOpen, call: func(h *harness) (forgeapi.PullRequest, error) {
			return h.client.ReopenPR(t.Context(), testRef(), testPR())
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{patchPull: pullRow(1)})
			got, err := test.call(h)
			if err != nil {
				t.Fatalf("%s = %v, want nil", test.name, err)
			}
			if state := sentBody(t, h, patchPull)["state"]; state != test.state {
				t.Errorf("%s sent state %v, want %q", test.name, state, test.state)
			}
			if got.Ref.Number != 1 || got.Title != "Row 1" {
				t.Errorf("%s = pull request %d %q, want 1 %q: the answer is the record the instance returned", test.name, got.Ref.Number, got.Title, "Row 1")
			}
		})
	}
}

func TestCreatePRSendsTheRequestedPullRequest(t *testing.T) {
	req := forgeapi.NewPullRequest{Title: "Example", Body: "Example body.", SourceBranch: "feature", TargetBranch: "main"}
	t.Run("without_labels", func(t *testing.T) {
		h := newHarness(t, map[string]string{createPull: pullRow(1)})
		got, err := h.client.CreatePR(t.Context(), testRef(), req)
		if err != nil {
			t.Fatalf("CreatePR = %v, want nil", err)
		}
		want := map[string]any{"title": "Example", "body": "Example body.", "head": "feature", "base": "main"}
		if body := sentBody(t, h, createPull); !maps.Equal(body, want) {
			t.Errorf("CreatePR sent %v, want %v: no labels field where none were named", body, want)
		}
		if got.Ref.Number != 1 || got.Action.Mergeable != forgeapi.SupportUnknown {
			t.Errorf("CreatePR = number %d, mergeable %v, want 1 and %v", got.Ref.Number, got.Action.Mergeable, forgeapi.SupportUnknown)
		}
	})
	t.Run("with_labels_as_a_draft", func(t *testing.T) {
		h := newHarness(t, map[string]string{createPull: pullRow(1), labelsRoute: labelBody})
		draft := req
		draft.Labels, draft.Draft = []string{"example-label"}, true
		if _, err := h.client.CreatePR(t.Context(), testRef(), draft); err != nil {
			t.Fatalf("CreatePR = %v, want nil", err)
		}
		body := sentBody(t, h, createPull)
		if labels, ok := body["labels"].([]any); !ok || !slices.Equal(labels, []any{float64(7)}) {
			t.Errorf("CreatePR sent labels %v, want [7]", body["labels"])
		}
		if body["title"] != "WIP: Example" {
			t.Errorf("CreatePR as a draft sent title %v, want %q", body["title"], "WIP: Example")
		}
	})
}

func TestCreateReleaseSendsTheRequestedRelease(t *testing.T) {
	h := newHarness(t, map[string]string{
		createRelease: `{"tag_name":"v1.0.0","name":"One","html_url":"https://forge.example/example/example/releases/v1.0.0","prerelease":true}`,
	})
	got, err := h.client.CreateRelease(t.Context(), testRef(), forgeapi.NewRelease{
		TagName: "v1.0.0", Name: "One", Body: "Notes.", Target: "main", Prerelease: true,
	})
	if err != nil {
		t.Fatalf("CreateRelease = %v, want nil", err)
	}
	want := map[string]any{"tag_name": "v1.0.0", "name": "One", "body": "Notes.", "target_commitish": "main", "draft": false, "prerelease": true}
	if body := sentBody(t, h, createRelease); !maps.Equal(body, want) {
		t.Errorf("CreateRelease sent %v, want %v", body, want)
	}
	if got.TagName != "v1.0.0" || !got.Prerelease || got.WebURL == "" {
		t.Errorf("CreateRelease = %+v, want the record the instance returned", got)
	}
}

func TestAMergeSendsTheStyleItsIntentNames(t *testing.T) {
	for _, test := range []struct {
		name   string
		intent forgeapi.MergeIntent
		want   string
	}{
		{name: "squash", intent: forgeapi.IntentSquash, want: styleSquash},
		{name: "default", intent: forgeapi.IntentDefault, want: styleMerge},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, form := mergeAnswering(t, http.StatusOK)
			if _, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{Intent: test.intent, HeadSHA: testHeadSHA}); err != nil {
				t.Fatalf("MergePR = %v, want nil", err)
			}
			if got := (*form)["Do"]; got != test.want {
				t.Errorf("MergePR with intent %s sent Do=%v, want %q", test.name, got, test.want)
			}
		})
	}
}

func rerunRoutes(rows string) map[string]string {
	return map[string]string{
		"GET /api/v1/version":  `{"version":"1.27.0+dev"}`,
		"GET /swagger.v1.json": `{"info":{"title":"Gitea API"},"paths":{"/repos/{owner}/{repo}/actions/runs/{run}/rerun-failed-jobs":{"post":{"operationId":"rerun"}}}}`,
		runsRoute:              `{"total_count":2,"workflow_runs":[` + rows + `]}`,
		"POST /api/v1/repos/example/example/actions/runs/3/rerun-failed-jobs": "",
		"POST /api/v1/repos/example/example/actions/runs/9/rerun-failed-jobs": "",
	}
}

func TestARerunReRunsTheRunItsHeadPinNames(t *testing.T) {
	const other = "0000000000000000000000000000000000000000"
	rows := `{"id":3,"head_sha":"` + other + `"},{"id":9,"head_sha":"` + testHeadSHA + `"}`
	for _, test := range []struct {
		name    string
		head    string
		rerun   string
		queried string
	}{
		{name: "a_pinned_head", head: testHeadSHA, rerun: "9", queried: testHeadSHA},
		{name: "no_pin", rerun: "3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, rerunRoutes(rows))
			if err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), test.head); err != nil {
				t.Fatalf("RerunFailedChecks(%q) = %v, want nil", test.head, err)
			}
			want := "POST /api/v1/repos/example/example/actions/runs/" + test.rerun + "/rerun-failed-jobs"
			if sent := h.instance.arrived(); !slices.Contains(sent, want) {
				t.Errorf("RerunFailedChecks(%q) sent %v, want %s", test.head, sent, want)
			}
			if got := h.instance.query(runsRoute, "head_sha"); got != test.queried {
				t.Errorf("RerunFailedChecks(%q) asked the run listing for head_sha=%q, want %q", test.head, got, test.queried)
			}
		})
	}
}

func TestARerunRefusesAHeadItCannotActOn(t *testing.T) {
	t.Run("a_head_no_run_carries", func(t *testing.T) {
		h := newHarness(t, rerunRoutes(`{"id":3,"head_sha":"0000000000000000000000000000000000000000"}`))
		err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA)
		var fe *forgeapi.Error
		if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeStaleHead {
			t.Errorf("RerunFailedChecks for a head no run carries = %v, want code %q", err, forgeapi.CodeStaleHead)
		}
		if sent := strings.Join(h.instance.arrived(), " "); strings.Contains(sent, "POST") {
			t.Errorf("RerunFailedChecks for a head no run carries sent %s, want no re-run", sent)
		}
	})
	t.Run("a_head_that_is_not_a_reference", func(t *testing.T) {
		h := newHarness(t, rerunRoutes(`{"id":3,"head_sha":"0000000000000000000000000000000000000000"}`))
		err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), "not a ref")
		var fe *forgeapi.Error
		if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRefInvalid {
			t.Errorf("RerunFailedChecks(%q) = %v, want code %q", "not a ref", err, forgeapi.CodeRefInvalid)
		}
		if sent := h.instance.arrived(); len(sent) != 0 {
			t.Errorf("RerunFailedChecks(%q) sent %v, want nothing", "not a ref", sent)
		}
	})
}
