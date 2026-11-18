package github

import (
	"net/http"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
)

// TestEveryFailureReachedOnAnAnswerCarriesItsFigures holds each kind of failure this
// family builds after an answer, whatever its status, to that answer's own pool,
// remaining figure and reset, so a remaining figure of zero only ever states a spent
// budget.
func TestEveryFailureReachedOnAnAnswerCarriesItsFigures(t *testing.T) {
	for _, test := range []struct {
		call   func(h *harness) error
		routes map[string]string
		status map[string]int
		name   string
	}{
		{
			name:   "a_merge_conflict_naming_no_merge",
			routes: map[string]string{mergeRoute: `{"message":"Conflict"}`},
			status: map[string]int{mergeRoute: http.StatusConflict},
			call: func(h *harness) error {
				_, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{Intent: forgeapi.IntentNoSquash, HeadSHA: testHeadSHA})
				return err
			},
		},
		{
			name:   "a_merge_answer_that_does_not_decode",
			routes: map[string]string{mergeRoute: `{`},
			status: map[string]int{mergeRoute: http.StatusConflict},
			call: func(h *harness) error {
				_, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{Intent: forgeapi.IntentNoSquash, HeadSHA: testHeadSHA})
				return err
			},
		},
		{
			name:   "a_document_resolving_no_repository",
			routes: map[string]string{documentRoute: `{"data":{"rateLimit":{"cost":1},"repository":null}}`},
			call: func(h *harness) error {
				_, err := h.client.ReadPR(t.Context(), testRef(), testPR())
				return err
			},
		},
		{
			name:   "a_refused_document",
			routes: map[string]string{documentRoute: `{"errors":[{"type":"UNPROCESSABLE","message":"Field 'teleported' doesn't exist"}]}`},
			call: func(h *harness) error {
				_, err := h.client.ReadPR(t.Context(), testRef(), testPR())
				return err
			},
		},
		{
			name:   "a_refused_connection_read_no_answer_witnessed",
			routes: map[string]string{metaRoute: `{"message":"Forbidden"}`},
			status: map[string]int{metaRoute: http.StatusForbidden},
			call: func(h *harness) error {
				_, err := h.client.ConnectionCaps(t.Context())
				return err
			},
		},
		{
			name:   "a_connection_whose_family_no_answer_witnessed",
			routes: map[string]string{metaRoute: `{}`},
			call: func(h *harness) error {
				_, err := h.client.ConnectionCaps(t.Context())
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, test.routes)
			for route, status := range test.status {
				h.instance.status(route, status)
			}
			h.instance.answerHeader("X-RateLimit-Remaining", "4321")
			h.instance.answerHeader("X-RateLimit-Resource", "core")
			h.instance.answerHeader("X-RateLimit-Reset", "1790000000")
			err := test.call(h)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Status == 0 {
				t.Fatalf("the call = %v, want a *forgeapi.Error carrying the answer's status", err)
			}
			if fe.Remaining != 4321 || fe.Pool != "core" || !fe.Reset.Equal(time.Unix(1790000000, 0)) {
				t.Errorf("the call = remaining %d pool %q reset %v, want the answer's 4321, core and its reset", fe.Remaining, fe.Pool, fe.Reset)
			}
		})
	}
}
