package github

import (
	"errors"
	"net/http"
	"testing"

	"github.com/cplieger/forgeapi"
)

// inFlightUUID is the handle a pending asynchronous merge minted, which the 409 a second
// merge request receives names again.
const inFlightUUID = "7d5f825f-0c1e-4a7b-9d3e-5f2a8c6b4e10"

// TestAMergeAlreadyInFlightIsTheInFlightOutcome holds GitHub's documented answer to a
// merge sent while another is pending ("a 409 response returns that request's UUID and
// merge options", REST reference, "Merge a pull request asynchronously"), in the body
// shape the sandbox answered: it is the already-in-flight OUTCOME under
// already_enqueued, never an error.
func TestAMergeAlreadyInFlightIsTheInFlightOutcome(t *testing.T) {
	h := newHarness(t, map[string]string{
		mergeRoute: `{"status":"pending","details":{"message":"A merge request already exists for this pull request.",` +
			`"uuid":"` + inFlightUUID + `","merge_method":"merge","merge_action":"default",` +
			`"expected_head_sha":"` + testHeadSHA + `","bypass_rules":false}}`,
	})
	h.instance.status(mergeRoute, http.StatusConflict)
	got, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA})
	if err != nil {
		t.Fatalf("MergePR against a merge already in flight = %v, want the in-flight outcome and no error", err)
	}
	if got.State != forgeapi.MergeOutcomeInFlight {
		t.Errorf("MergePR against a merge already in flight = state %v, want %v", got.State, forgeapi.MergeOutcomeInFlight)
	}
	if got.Code != forgeapi.CodeAlreadyEnqueued {
		t.Errorf("MergePR against a merge already in flight = code %q, want %q", got.Code, forgeapi.CodeAlreadyEnqueued)
	}
	if got.QueueState != forgeapi.QueueUnknown || got.QueuePosition != forgeapi.QueuePositionUnknown {
		t.Errorf("MergePR against a merge already in flight = queue %v at %d, want %v at %d: the 409 carries no queue key",
			got.QueueState, got.QueuePosition, forgeapi.QueueUnknown, forgeapi.QueuePositionUnknown)
	}
}

// TestAnUndecodableInFlightAnswerIsABodyTheLibraryCannotRead holds the Error contract
// for an answer whose body does not decode (CodeValidation, KindUpstream) on the 409
// the merge route reads as an outcome: only a body that decodes and names no handle is
// the plain conflict.
func TestAnUndecodableInFlightAnswerIsABodyTheLibraryCannotRead(t *testing.T) {
	h := newHarness(t, map[string]string{mergeRoute: `{`})
	h.instance.status(mergeRoute, http.StatusConflict)
	_, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA})
	var fe *forgeapi.Error
	if !errors.As(err, &fe) {
		t.Fatalf("MergePR against a 409 whose body does not decode = %v, want a *forgeapi.Error", err)
	}
	if fe.Kind != forgeapi.KindUpstream || fe.Code != forgeapi.CodeValidation {
		t.Errorf("MergePR against a 409 whose body does not decode = %v / %q, want %v / %q",
			fe.Kind, fe.Code, forgeapi.KindUpstream, forgeapi.CodeValidation)
	}
}

// TestAMergeConflictNamingNoMergeIsAConflictWithNoCause holds the other side of that
// split, which is the body's structure: a 409 on the merge whose body names no pending
// merge is a shape no capture witnesses, so it is the plain Conflict the family answers
// for a 409 anywhere else, and never a stale head, which this route reports as a 400.
func TestAMergeConflictNamingNoMergeIsAConflictWithNoCause(t *testing.T) {
	h := newHarness(t, map[string]string{mergeRoute: `{"message":"Conflict"}`})
	h.instance.status(mergeRoute, http.StatusConflict)
	_, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA})
	var fe *forgeapi.Error
	if !errors.As(err, &fe) {
		t.Fatalf("MergePR against a 409 naming no merge = %v, want a *forgeapi.Error", err)
	}
	if fe.Kind != forgeapi.KindConflict || fe.Code != "" {
		t.Errorf("MergePR against a 409 naming no merge = %v / %q, want %v with no code", fe.Kind, fe.Code, forgeapi.KindConflict)
	}
	if fe.Status != http.StatusConflict {
		t.Errorf("MergePR against a 409 naming no merge = status %d, want %d", fe.Status, http.StatusConflict)
	}
}

// TestEveryMergeAsyncBadRequestNamesNoCause holds the 400 this route answers for every
// cause measured on the sandbox: a draft, a closed pull request and a head pin naming
// another commit all arrive as `{"status":"failed","details":{"message":...}}`, the same
// structure with different human text, so the family answers the code for a refusal
// whose cause upstream did not name rather than reading the message.
func TestEveryMergeAsyncBadRequestNamesNoCause(t *testing.T) {
	for name, message := range map[string]string{
		"draft":           "Pull request is in draft.",
		"closed":          "Pull request is closed.",
		"head_not_pinned": "Pull request head branch was modified.",
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{mergeRoute: `{"status":"failed","details":{"message":"` + message + `"}}`})
			h.instance.status(mergeRoute, http.StatusBadRequest)
			_, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA})
			var fe *forgeapi.Error
			if !errors.As(err, &fe) {
				t.Fatalf("MergePR against the %s 400 = %v, want a *forgeapi.Error", name, err)
			}
			if fe.Kind != forgeapi.KindNotMergeable || fe.Code != forgeapi.CodeNotMergeable {
				t.Errorf("MergePR against the %s 400 = %v / %q, want %v / %q", name, fe.Kind, fe.Code,
					forgeapi.KindNotMergeable, forgeapi.CodeNotMergeable)
			}
		})
	}
}
