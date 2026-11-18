package github

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// autoMergeNode is the node id the pull request's REST record names, which is what the
// arm addresses it by.
const autoMergeNode = "PR_kwDOexample"

// autoMergePull is the REST pull-request record the auto-merge read answers, in the
// shape the sandbox answered it: the node id the arm addresses and the mergeable state
// that decides between merging now and arming.
func autoMergePull(state string) string {
	return `{"number":1,"state":"open","title":"t","node_id":"` + autoMergeNode + `","merged":false,` +
		`"mergeable":true,"mergeable_state":"` + state + `","head":{"ref":"feature","sha":"` + testHeadSHA + `"},` +
		`"html_url":"https://forge.example/example/example/pull/1"}`
}

// autoMergeArmed is the arm's answer when GitHub accepts it, as the sandbox answered on
// a pull request whose base requires a check nothing posts.
const autoMergeArmed = `{"data":{"enablePullRequestAutoMerge":{"pullRequest":{"number":1,` +
	`"autoMergeRequest":{"enabledAt":"2026-10-04T20:36:40Z","mergeMethod":"MERGE"}}}}}`

// autoMergeRefused is the arm's answer on a pull request GitHub will not arm, as the
// sandbox answered on one in clean status: a null payload beside an UNPROCESSABLE error.
const autoMergeRefused = `{"data":{"enablePullRequestAutoMerge":null},"errors":[{"type":"UNPROCESSABLE",` +
	`"path":["enablePullRequestAutoMerge"],"locations":[{"line":2,"column":3}],` +
	`"message":"Pull request Pull request is in clean status"}]}`

// armRequest is the part of a posted arm document the contract fixes.
type armRequest struct {
	OperationName string `json:"operationName"`
	Variables     struct {
		PullRequestID   string `json:"pullRequestId"`
		ExpectedHeadOid string `json:"expectedHeadOid"`
		MergeMethod     string `json:"mergeMethod"`
	} `json:"variables"`
}

// postedArm decodes the arm document the instance received.
func postedArm(t *testing.T, h *harness) armRequest {
	t.Helper()
	var got armRequest
	if err := json.Unmarshal([]byte(h.instance.body(documentRoute)), &got); err != nil {
		t.Fatalf("decoding the posted arm document %q: %v", h.instance.body(documentRoute), err)
	}
	return got
}

// TestAutoMerge_arms_a_pull_request_that_cannot_merge_now holds `AutoMerge` to its field
// doc on this product: a pull request whose state is not mergeable now is ARMED with
// enablePullRequestAutoMerge, addressed by the node id the read named, pinned with
// expectedHeadOid, and nothing merges through merge-async. The armed answer is the
// enqueued outcome, the member a merge that completes later and unattended reads as.
func TestAutoMerge_arms_a_pull_request_that_cannot_merge_now(t *testing.T) {
	for _, state := range []string{"blocked", "behind", "dirty", "draft", "unknown"} {
		t.Run(state, func(t *testing.T) {
			h := newHarness(t, map[string]string{pullsRoute: autoMergePull(state), documentRoute: autoMergeArmed})
			got, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{Intent: forgeapi.IntentNoSquash, HeadSHA: testHeadSHA, AutoMerge: true})
			if err != nil {
				t.Fatalf("MergePR(AutoMerge) on a %s pull request = %v, want the armed outcome", state, err)
			}
			if got.State != forgeapi.MergeOutcomeEnqueued || got.Code != "" {
				t.Errorf("MergePR(AutoMerge) on a %s pull request = %v / %q, want %v with no code", state, got.State, got.Code, forgeapi.MergeOutcomeEnqueued)
			}
			want := []string{pullsRoute, documentRoute}
			if arrived := h.instance.arrived(); !slices.Equal(arrived, want) {
				t.Errorf("MergePR(AutoMerge) on a %s pull request sent %v, want %v: the read, then the arm, and no merge", state, arrived, want)
			}
			arm := postedArm(t, h)
			if arm.OperationName != "EnableAutoMerge" || arm.Variables.PullRequestID != autoMergeNode ||
				arm.Variables.ExpectedHeadOid != testHeadSHA || arm.Variables.MergeMethod != "MERGE" {
				t.Errorf("the arm posted %+v, want EnableAutoMerge on %q pinned to %q with MERGE", arm, autoMergeNode, testHeadSHA)
			}
		})
	}
}

// TestAutoMerge_merges_now_a_pull_request_GitHub_will_not_arm holds the other side:
// GitHub refuses to arm a pull request whose state is mergeable now (CLEAN, HAS_HOOKS,
// UNSTABLE in its MergeStateStatus reference; the refusal captured on CLEAN), so its
// requirements are met and it merges through merge-async with the head pin, as a merge
// not asked to wait does.
func TestAutoMerge_merges_now_a_pull_request_GitHub_will_not_arm(t *testing.T) {
	for _, state := range []string{"clean", "has_hooks", "unstable"} {
		t.Run(state, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				pullsRoute: autoMergePull(state),
				mergeRoute: `{"status":"pending","details":{"uuid":"` + inFlightUUID + `","merge_method":"merge","expected_head_sha":"` + testHeadSHA + `"}}`,
			})
			h.instance.status(mergeRoute, http.StatusAccepted)
			got, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{Intent: forgeapi.IntentNoSquash, HeadSHA: testHeadSHA, AutoMerge: true})
			if err != nil {
				t.Fatalf("MergePR(AutoMerge) on a %s pull request = %v, want the accepted merge", state, err)
			}
			if got.State != forgeapi.MergeOutcomeAccepted {
				t.Errorf("MergePR(AutoMerge) on a %s pull request = %v, want %v", state, got.State, forgeapi.MergeOutcomeAccepted)
			}
			want := []string{pullsRoute, mergeRoute}
			if arrived := h.instance.arrived(); !slices.Equal(arrived, want) {
				t.Errorf("MergePR(AutoMerge) on a %s pull request sent %v, want %v: the read, then the merge, and no arm", state, arrived, want)
			}
			if body := h.instance.body(mergeRoute); !strings.Contains(body, `"sha":"`+testHeadSHA+`"`) {
				t.Errorf("the merge carried %s, want the head pin under sha", body)
			}
		})
	}
}

// TestAutoMerge_arms_with_the_strategy_the_request_names holds the arm's merge method to
// the one a merge now would send, in the arm's own enum spelling.
func TestAutoMerge_arms_with_the_strategy_the_request_names(t *testing.T) {
	for name, tc := range map[string]struct {
		req  forgeapi.MergeRequest
		want string
	}{
		"squash intent":  {forgeapi.MergeRequest{Intent: forgeapi.IntentSquash}, "SQUASH"},
		"rebase":         {forgeapi.MergeRequest{Strategy: "rebase"}, "REBASE"},
		"named strategy": {forgeapi.MergeRequest{Strategy: "merge"}, "MERGE"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{pullsRoute: autoMergePull("blocked"), documentRoute: autoMergeArmed})
			tc.req.HeadSHA, tc.req.AutoMerge = testHeadSHA, true
			if _, err := h.client.MergePR(t.Context(), testRef(), testPR(), tc.req); err != nil {
				t.Fatalf("MergePR(%+v) = %v, want the armed outcome", tc.req, err)
			}
			if got := postedArm(t, h).Variables.MergeMethod; got != tc.want {
				t.Errorf("MergePR(%+v) armed with %q, want %q", tc.req, got, tc.want)
			}
		})
	}
}

// The default intent on this product, which configures no default merge method, is the
// one method the repository's record allows, read before either merge path, and the arm
// takes it in its own enum spelling.
func TestTheDefaultIntentTakesTheOneMethodTheRepositoryAllows(t *testing.T) {
	const record = `{"full_name":"example/example","allow_merge_commit":false,"allow_squash_merge":true,"allow_rebase_merge":false}`
	t.Run("merge_now", func(t *testing.T) {
		h := newHarness(t, map[string]string{repoRouteBase: record, mergeRoute: `{"status":"merged"}`})
		if _, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA}); err != nil {
			t.Fatalf("MergePR at the default intent = %v, want the merge", err)
		}
		if arrived, want := h.instance.arrived(), []string{repoRouteBase, mergeRoute}; !slices.Equal(arrived, want) {
			t.Errorf("MergePR at the default intent sent %v, want %v", arrived, want)
		}
		if got := sentBody(t, h, mergeRoute)["merge_method"]; got != "squash" {
			t.Errorf("MergePR at the default intent sent merge_method %v, want the one allowed, squash", got)
		}
	})
	t.Run("armed", func(t *testing.T) {
		h := newHarness(t, map[string]string{repoRouteBase: record, pullsRoute: autoMergePull("blocked"), documentRoute: autoMergeArmed})
		if _, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA, AutoMerge: true}); err != nil {
			t.Fatalf("MergePR(AutoMerge) at the default intent = %v, want the armed outcome", err)
		}
		if arrived, want := h.instance.arrived(), []string{repoRouteBase, pullsRoute, documentRoute}; !slices.Equal(arrived, want) {
			t.Errorf("MergePR(AutoMerge) at the default intent sent %v, want %v", arrived, want)
		}
		if got := postedArm(t, h).Variables.MergeMethod; got != "SQUASH" {
			t.Errorf("MergePR(AutoMerge) at the default intent armed with %q, want SQUASH", got)
		}
	})
}

// A repository allowing other than one method names no default among them, so the
// default intent is refused after the record read and before any other request,
// whatever the base branch's rules or queue would take.
func TestTheDefaultIntentIsRefusedWhereTheRepositoryAllowsOtherThanOneMethod(t *testing.T) {
	const queued = `[{"type":"merge_queue","parameters":{"merge_method":"SQUASH"}}]`
	for _, tc := range []struct{ name, record, names string }{
		{name: "several_allowed", record: `{"full_name":"example/example","allow_merge_commit":true,"allow_squash_merge":true,"allow_rebase_merge":false}`, names: "merge, squash"},
		{name: "no_flags", record: `{"full_name":"example/example"}`, names: "no allowed merge method"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				repoRouteBase: tc.record, pullsRoute: autoMergePull("clean"), mergeRoute: `{"status":"merged"}`,
				"GET /api/v3/repos/example/example/rules/branches/main": queued,
			})
			_, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA})
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeStrategyRequired || fe.Kind != forgeapi.KindNotMergeable {
				t.Fatalf("MergePR at the default intent with %s = %v, want %q / %v", tc.name, err, forgeapi.CodeStrategyRequired, forgeapi.KindNotMergeable)
			}
			if !strings.Contains(fe.Message, tc.names) {
				t.Errorf("MergePR at the default intent with %s = message %q, want it naming %q", tc.name, fe.Message, tc.names)
			}
			if arrived, want := h.instance.arrived(), []string{repoRouteBase}; !slices.Equal(arrived, want) {
				t.Errorf("MergePR at the default intent with %s sent %v, want the record read alone, %v", tc.name, arrived, want)
			}
		})
	}
}

// TestAMergeNotAskedToWaitSendsTheMergeAlone holds the price of the default: without
// AutoMerge nothing is read first, so a merge stays one request.
func TestAMergeNotAskedToWaitSendsTheMergeAlone(t *testing.T) {
	h := newHarness(t, map[string]string{
		mergeRoute: `{"status":"pending","details":{"uuid":"` + inFlightUUID + `","merge_method":"merge","expected_head_sha":"` + testHeadSHA + `"}}`,
	})
	h.instance.status(mergeRoute, http.StatusAccepted)
	if _, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{Intent: forgeapi.IntentNoSquash, HeadSHA: testHeadSHA}); err != nil {
		t.Fatalf("MergePR = %v, want the accepted merge", err)
	}
	if arrived := h.instance.arrived(); !slices.Equal(arrived, []string{mergeRoute}) {
		t.Errorf("MergePR without AutoMerge sent %v, want %v alone", arrived, []string{mergeRoute})
	}
}

// TestAnArmGitHubRefusesIsTheForgesRefusal holds the arm's refusal to what it is: the
// forge declining this merge in human text, which is the not-mergeable code for a
// refusal whose cause upstream did not name. It is the merge's refusal and not the
// document's failure, so the connection keeps its documents.
func TestAnArmGitHubRefusesIsTheForgesRefusal(t *testing.T) {
	h := newHarness(t, map[string]string{pullsRoute: autoMergePull("unknown"), documentRoute: autoMergeRefused})
	_, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{Intent: forgeapi.IntentNoSquash, HeadSHA: testHeadSHA, AutoMerge: true})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("MergePR(AutoMerge) with the arm refused = %v, want a *forgeapi.Error", err)
	}
	if fe.Kind != forgeapi.KindNotMergeable || fe.Code != forgeapi.CodeNotMergeable {
		t.Errorf("MergePR(AutoMerge) with the arm refused = %v / %q, want %v / %q", fe.Kind, fe.Code, forgeapi.KindNotMergeable, forgeapi.CodeNotMergeable)
	}
	if !strings.Contains(fe.Message, "clean status") {
		t.Errorf("MergePR(AutoMerge) with the arm refused = message %q, want the instance's reason quoted", fe.Message)
	}
	if h.client.isDegraded() {
		t.Error("a refused arm degraded the connection, want its documents kept: the refusal is the merge's, not the document's")
	}
}

// TestAnArmOnAReadOnlyClientIsRefusedBeforeItIsSent holds the arm to the mutation gate:
// it changes the pull request as a merge does, so a client that refuses mutations sends
// no arm.
func TestAnArmOnAReadOnlyClientIsRefusedBeforeItIsSent(t *testing.T) {
	h := newHarness(t, map[string]string{pullsRoute: autoMergePull("blocked"), documentRoute: autoMergeArmed}, forgeapi.WithMutations(false))
	_, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{Intent: forgeapi.IntentNoSquash, HeadSHA: testHeadSHA, AutoMerge: true})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeMutationsDisabled {
		t.Errorf("MergePR(AutoMerge) on a read-only client = %v, want code %q", err, forgeapi.CodeMutationsDisabled)
	}
	if arrived := h.instance.arrived(); len(arrived) != 0 {
		t.Errorf("MergePR(AutoMerge) on a read-only client sent %v, want nothing: the read is the merge's own", arrived)
	}
}

// TestAMergeAskedToWaitIsAdmittedUnderTheMutationReserve holds the reserve to its
// purpose: it is held back from reads for the writes a user is about to make, so the
// read a merge asked to wait makes before its arm is the merge's and is admitted with
// the budget at the reserve, as the merge not asked to wait is.
func TestAMergeAskedToWaitIsAdmittedUnderTheMutationReserve(t *testing.T) {
	h := newHarness(t, map[string]string{pullsRoute: autoMergePull("blocked"), documentRoute: autoMergeArmed})
	h.instance.answerHeader("X-Ratelimit-Remaining", "10")
	h.instance.answerHeader("X-Ratelimit-Reset", "1790000000")
	h.instance.answerHeader("X-Ratelimit-Resource", "core")
	for attempt := range 2 {
		got, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{Intent: forgeapi.IntentNoSquash, HeadSHA: testHeadSHA, AutoMerge: true})
		if err != nil {
			t.Fatalf("MergePR(AutoMerge) #%d with the budget at the reserve = %v, want the merge armed", attempt+1, err)
		}
		if got.State != forgeapi.MergeOutcomeEnqueued {
			t.Errorf("MergePR(AutoMerge) #%d with the budget at the reserve = %v, want %v", attempt+1, got.State, forgeapi.MergeOutcomeEnqueued)
		}
	}
}
