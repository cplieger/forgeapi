package github

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// armedPull is the pull request's REST record with an auto-merge armed, in the shape
// of the auto-merge schema this product's API description gives.
const armedPull = `{"number":1,"state":"open","node_id":"` + autoMergeNode + `",` +
	`"auto_merge":{"enabled_by":{"login":"example-user"},"merge_method":"merge","commit_title":"t","commit_message":"b"}}`

// unarmedPull is the same record with nothing armed: auto_merge null.
const unarmedPull = `{"number":1,"state":"open","node_id":"` + autoMergeNode + `","auto_merge":null}`

// disarmed is the disarm's answer per this product's GraphQL schema: the payload's
// pull request, its auto-merge request null once disarmed.
const disarmed = `{"data":{"disablePullRequestAutoMerge":{"pullRequest":{"number":1,"autoMergeRequest":null}}}}`

// disarmRequest is the part of a posted disarm document the contract fixes.
type disarmRequest struct {
	OperationName string `json:"operationName"`
	Variables     struct {
		PullRequestID string `json:"pullRequestId"`
	} `json:"variables"`
}

func TestACancelOfAnArmedMergeSendsTheDisarmByTheRecordsNodeID(t *testing.T) {
	h := newHarness(t, map[string]string{pullsRoute: armedPull, documentRoute: disarmed})
	if err := h.client.CancelAutoMerge(t.Context(), testRef(), testPR()); err != nil {
		t.Fatalf("CancelAutoMerge of an armed merge = %v, want nil", err)
	}
	if got, want := h.instance.arrived(), []string{pullsRoute, documentRoute}; !slices.Equal(got, want) {
		t.Errorf("CancelAutoMerge of an armed merge sent %v, want %v: the read, then the disarm", got, want)
	}
	var posted disarmRequest
	if err := json.Unmarshal([]byte(h.instance.body(documentRoute)), &posted); err != nil {
		t.Fatalf("decoding the posted disarm %q: %v", h.instance.body(documentRoute), err)
	}
	if posted.OperationName != "DisableAutoMerge" || posted.Variables.PullRequestID != autoMergeNode {
		t.Errorf("the disarm posted %+v, want DisableAutoMerge on %q", posted, autoMergeNode)
	}
}

// GitHub's description does not say what the disarm answers on a pull request with
// nothing armed, so a record whose auto_merge is null ends the call with no disarm.
func TestACancelWithNothingArmedSendsNoDisarm(t *testing.T) {
	h := newHarness(t, map[string]string{pullsRoute: unarmedPull, documentRoute: disarmed})
	if err := h.client.CancelAutoMerge(t.Context(), testRef(), testPR()); err != nil {
		t.Fatalf("CancelAutoMerge with nothing armed = %v, want nil", err)
	}
	if got, want := h.instance.arrived(), []string{pullsRoute}; !slices.Equal(got, want) {
		t.Errorf("CancelAutoMerge with nothing armed sent %v, want %v alone", got, want)
	}
}

// A disarm the forge refuses, a null payload beside an error, is upstream's refusal
// with its message quoted. It is the cancel's refusal and not the document's failure,
// so the connection keeps its documents.
func TestADisarmGitHubRefusesStaysUpstream(t *testing.T) {
	refused := `{"data":{"disablePullRequestAutoMerge":null},"errors":[{"type":"UNPROCESSABLE",` +
		`"path":["disablePullRequestAutoMerge"],"message":"Pull request is in an unexpected state"}]}`
	h := newHarness(t, map[string]string{pullsRoute: armedPull, documentRoute: refused})
	err := h.client.CancelAutoMerge(t.Context(), testRef(), testPR())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindUpstream || fe.Code != "" {
		t.Fatalf("CancelAutoMerge with the disarm refused = %v, want KindUpstream with no code", err)
	}
	if !strings.Contains(fe.Message, "unexpected state") {
		t.Errorf("CancelAutoMerge with the disarm refused = message %q, want the instance's reason quoted", fe.Message)
	}
	if h.client.isDegraded() {
		t.Error("a refused disarm degraded the connection, want its documents kept")
	}
}

// A disarm answered with the auto-merge request still on the pull request did not
// disarm it, so it is no success.
func TestADisarmAnsweredStillArmedIsNoSuccess(t *testing.T) {
	stillArmed := `{"data":{"disablePullRequestAutoMerge":{"pullRequest":{"number":1,"autoMergeRequest":{"enabledAt":"2026-10-04T20:36:40Z"}}}}}`
	h := newHarness(t, map[string]string{pullsRoute: armedPull, documentRoute: stillArmed})
	err := h.client.CancelAutoMerge(t.Context(), testRef(), testPR())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindUpstream {
		t.Errorf("CancelAutoMerge answered still armed = %v, want KindUpstream", err)
	}
}

// A payload naming no pull request still says the mutation ran, so the call answers
// nil and counts the shape as one this family does not map.
func TestADisarmAnsweredWithNoPullRequestIsCounted(t *testing.T) {
	h := newHarness(t, map[string]string{pullsRoute: armedPull, documentRoute: `{"data":{"disablePullRequestAutoMerge":{"pullRequest":null}}}`})
	if err := h.client.CancelAutoMerge(t.Context(), testRef(), testPR()); err != nil {
		t.Fatalf("CancelAutoMerge answered with no pull request = %v, want nil", err)
	}
	if !h.logged("no pull request on the disarm's payload") {
		t.Error("the disarm answered with no pull request is not in the log, want it named")
	}
}

// The read is the cancel's own, so a client that refuses mutations sends nothing,
// and a pull-request number that is not positive is refused before any request.
func TestACancelThatMayNotBeSentSendsNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		pr   forgeapi.PRRef
		code string
		opts []forgeapi.Option
	}{
		"read-only client": {pr: testPR(), code: forgeapi.CodeMutationsDisabled, opts: []forgeapi.Option{forgeapi.WithMutations(false)}},
		"number zero":      {pr: forgeapi.PRRef{}, code: forgeapi.CodeRepoRefInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{pullsRoute: armedPull, documentRoute: disarmed}, tc.opts...)
			err := h.client.CancelAutoMerge(t.Context(), testRef(), tc.pr)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != tc.code {
				t.Errorf("CancelAutoMerge (%s) = %v, want code %q", name, err, tc.code)
			}
			if arrived := h.instance.arrived(); len(arrived) != 0 {
				t.Errorf("CancelAutoMerge (%s) sent %v, want nothing", name, arrived)
			}
		})
	}
}
