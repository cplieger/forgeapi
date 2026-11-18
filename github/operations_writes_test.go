package github

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

func sentBody(t *testing.T, h *harness, route string) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(h.instance.body(route)), &body); err != nil {
		t.Fatalf("Setup: decoding the body %s received: %v", route, err)
	}
	return body
}

func TestANumberBelowOneIsRefusedBeforeAnyRequest(t *testing.T) {
	zero := forgeapi.PRRef{Number: 0, Sigil: sigil}
	for _, test := range []struct {
		call func(h *harness) error
		name string
	}{
		{name: "ReadPR", call: func(h *harness) error { _, err := h.client.ReadPR(t.Context(), testRef(), zero); return err }},
		{name: "MergeStatus", call: func(h *harness) error { _, err := h.client.MergeStatus(t.Context(), testRef(), zero); return err }},
		{name: "ClosePR", call: func(h *harness) error { _, err := h.client.ClosePR(t.Context(), testRef(), zero); return err }},
		{name: "ReopenPR", call: func(h *harness) error { _, err := h.client.ReopenPR(t.Context(), testRef(), zero); return err }},
		{name: "MergePR", call: func(h *harness) error {
			_, err := h.client.MergePR(t.Context(), testRef(), zero, forgeapi.MergeRequest{Intent: forgeapi.IntentNoSquash, HeadSHA: testHeadSHA})
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
			if sent := h.instance.count(); sent != 0 {
				t.Errorf("%s(number 0) sent %d request(s), want none: %v", test.name, sent, h.instance.arrived())
			}
		})
	}
}

func TestALifecycleTransitionSendsItsTargetStateAndAnswersThePullRequest(t *testing.T) {
	const route = "PATCH /api/v3/repos/example/example/pulls/1"
	for _, test := range []struct {
		call func(h *harness) (forgeapi.PullRequest, error)
		name string
		want string
	}{
		{name: "ClosePR", want: stateClosed, call: func(h *harness) (forgeapi.PullRequest, error) {
			return h.client.ClosePR(t.Context(), testRef(), testPR())
		}},
		{name: "ReopenPR", want: stateOpen, call: func(h *harness) (forgeapi.PullRequest, error) {
			return h.client.ReopenPR(t.Context(), testRef(), testPR())
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{route: readCaptures(t).raw(t, "rest_pull")})
			item, err := test.call(h)
			if err != nil {
				t.Fatalf("%s = %v, want the pull request", test.name, err)
			}
			if got := sentBody(t, h, route)[keyState]; got != test.want {
				t.Errorf("%s sent state %v, want %q", test.name, got, test.want)
			}
			if item.Ref.Number != 1 || item.Title != "Example pull request" {
				t.Errorf("%s = #%d %q, want the recorded #1 \"Example pull request\"", test.name, item.Ref.Number, item.Title)
			}
		})
	}
}

// This product fills the close reason itself, so only the state is sent.
func TestClosingAnIssueSendsTheClosedStateAndAnswersTheIssue(t *testing.T) {
	const route = "PATCH /api/v3/repos/example/example/issues/1"
	h := newHarness(t, map[string]string{route: readCaptures(t).raw(t, "rest_issue")})
	item, err := h.client.CloseIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 1})
	if err != nil {
		t.Fatalf("CloseIssue = %v, want the issue", err)
	}
	if got := sentBody(t, h, route); len(got) != 1 || got[keyState] != stateClosed {
		t.Errorf("CloseIssue sent %v, want the closed state alone", got)
	}
	if item.Ref.Number != 1 || item.State != forgeapi.IssueStateClosed || item.Author != "example-user" {
		t.Errorf("CloseIssue = #%d %v by %q, want the recorded #1 closed by example-user", item.Ref.Number, item.State, item.Author)
	}
	want := []forgeapi.Label{{Name: "example-label", Color: "ededed", Description: "Example label."}}
	if !slices.Equal(item.Labels, want) {
		t.Errorf("CloseIssue = labels %v, want %v", item.Labels, want)
	}
}

// The route reads a null labels key as a value, not as an absence.
func TestAnIssueCreationWithNoLabelsSendsNoLabelsKey(t *testing.T) {
	const route = "POST /api/v3/repos/example/example/issues"
	h := newHarness(t, map[string]string{route: readCaptures(t).raw(t, "rest_issue")})
	item, err := h.client.CreateIssue(t.Context(), testRef(), forgeapi.NewIssue{Title: "t"})
	if err != nil {
		t.Fatalf("CreateIssue = %v, want the issue", err)
	}
	if body := sentBody(t, h, route); body[keyLabels] != nil || len(body) != 2 {
		t.Errorf("CreateIssue with no labels sent %v, want the title and body alone", body)
	}
	if item.Ref.Number != 1 {
		t.Errorf("CreateIssue = #%d, want the recorded #1", item.Ref.Number)
	}
}

// With no target sent, the instance's own default branch decides.
func TestAReleaseCreationSendsItsTargetOnlyWhereOneIsNamed(t *testing.T) {
	const route = "POST /api/v3/repos/example/example/releases"
	for _, test := range []struct {
		want   any
		name   string
		target string
	}{
		{name: "named", target: "main", want: "main"},
		{name: "not_named"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{route: readCaptures(t).raw(t, "rest_release")})
			release, err := h.client.CreateRelease(t.Context(), testRef(), forgeapi.NewRelease{TagName: "v1.0.0", Target: test.target})
			if err != nil {
				t.Fatalf("CreateRelease = %v, want the release", err)
			}
			if got := sentBody(t, h, route)["target_commitish"]; got != test.want {
				t.Errorf("CreateRelease(target %q) sent target_commitish %v, want %v", test.target, got, test.want)
			}
			if release.TagName != "v1.0.0" || release.Name != "Example release" {
				t.Errorf("CreateRelease = %q %q, want the recorded v1.0.0 \"Example release\"", release.TagName, release.Name)
			}
		})
	}
}

func TestACreationAnsweringNoNumberSpendsNoLabelsRequest(t *testing.T) {
	h := newHarness(t, map[string]string{
		"POST /api/v3/repos/example/example/pulls": `{"number":0,"state":"open","title":"t"}`,
	})
	item, err := h.client.CreatePR(t.Context(), testRef(), forgeapi.NewPullRequest{Title: "t", Labels: []string{"example-label"}})
	if err == nil || item.Partial == nil || item.Partial.Reason != forgeapi.PartialLabelsNotApplied {
		t.Errorf("CreatePR answering number 0 = partial %+v, %v, want the labels reported not applied", item.Partial, err)
	}
	if got := h.instance.arrived(); len(got) != 1 {
		t.Errorf("CreatePR answering number 0 sent %v, want the creation alone", got)
	}
}

// With no pin the re-run reads the pull request's current head and filters by it, so
// a failed run of another head the listing answers is never the one re-run.
func TestAReRunWithNoHeadReRunsAFailedRunOfThePullRequestsHead(t *testing.T) {
	const pull = "GET /api/v3/repos/example/example/pulls/1"
	runs := `{"total_count":2,"workflow_runs":[` +
		`{"id":9,"head_sha":"0000000000000000000000000000000000000000","status":"completed","conclusion":"failure"},` +
		`{"id":1,"head_sha":"` + testHeadSHA + `","status":"completed","conclusion":"failure"}]}`
	t.Run("a_head", func(t *testing.T) {
		h := newHarness(t, map[string]string{pull: `{"number":1,"head":{"ref":"feature","sha":"` + testHeadSHA + `"}}`, runsRoute: runs, rerunRoute: `{}`})
		if err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), ""); err != nil {
			t.Fatalf("RerunFailedChecks(no head) = %v, want the re-run sent", err)
		}
		if got := h.instance.arrived(); !slices.Equal(got, []string{pull, runsRoute, rerunRoute}) {
			t.Errorf("RerunFailedChecks(no head) sent %v, want the pull request read, the run read and the re-run of run 1", got)
		}
		if got := h.instance.query(runsRoute, "head_sha"); got != testHeadSHA {
			t.Errorf("RerunFailedChecks(no head) sent head_sha=%q, want the pull request's head %q", got, testHeadSHA)
		}
	})
	t.Run("no_head", func(t *testing.T) {
		h := newHarness(t, map[string]string{pull: `{"number":1,"head":{"ref":"feature","sha":""}}`, runsRoute: runs, rerunRoute: `{}`})
		err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), "")
		var fe *forgeapi.Error
		if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeMissingSHA {
			t.Errorf("RerunFailedChecks on a pull request with no head = %v, want code %q", err, forgeapi.CodeMissingSHA)
		}
		if got := h.instance.arrived(); !slices.Equal(got, []string{pull}) {
			t.Errorf("RerunFailedChecks on a pull request with no head sent %v, want the pull request read alone", got)
		}
	})
}

// A head carries a run per workflow, so the re-run addresses the first run on it that
// failed or was cancelled, and a head whose runs all passed or are still running is
// refused before any write: a re-run can carry deployment side effects.
func TestAReRunAddressesARunOnTheHeadThatFailed(t *testing.T) {
	row := func(id, status, conclusion string) string {
		return `{"id":` + id + `,"head_sha":"` + testHeadSHA + `","status":"` + status + `","conclusion":"` + conclusion + `","path":".github/workflows/w` + id + `.yml"}`
	}
	runs := func(rows ...string) string {
		return `{"total_count":` + strconv.Itoa(len(rows)) + `,"workflow_runs":[` + strings.Join(rows, ",") + `]}`
	}
	for _, test := range []struct {
		name  string
		runs  string
		rerun string
		code  string
	}{
		{name: "a_passed_run_beside_a_failed_one", runs: runs(row("4", "completed", "success"), row("1", "completed", "failure")), rerun: "1"},
		{name: "a_cancelled_run", runs: runs(row("1", "completed", "cancelled")), rerun: "1"},
		{name: "only_passed_and_running_runs", runs: runs(row("4", "completed", "success"), row("5", "in_progress", "")), code: forgeapi.CodeRunNotFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			post := "POST /api/v3/repos/example/example/actions/runs/" + test.rerun + "/rerun-failed-jobs"
			h := newHarness(t, map[string]string{runsRoute: test.runs, post: `{}`})
			err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA)
			if test.code != "" {
				var fe *forgeapi.Error
				if !asForgeError(err, &fe) || fe.Code != test.code || fe.Kind != forgeapi.KindConflict {
					t.Errorf("RerunFailedChecks = %v, want code %q on KindConflict", err, test.code)
				}
				if got := h.instance.arrived(); !slices.Equal(got, []string{runsRoute}) {
					t.Errorf("RerunFailedChecks sent %v, want the run read alone", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("RerunFailedChecks = %v, want nil", err)
			}
			if got := h.instance.arrived(); !slices.Equal(got, []string{runsRoute, post}) {
				t.Errorf("RerunFailedChecks sent %v, want the run read and %s", got, post)
			}
		})
	}
}
