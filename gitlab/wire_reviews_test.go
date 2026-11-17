package gitlab

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// sentReviews is the reviews variable one posted document carried, and false for
// found when the document sent none.
func sentReviews(t *testing.T, body string) (value, found bool) {
	t.Helper()
	var posted struct {
		Variables map[string]json.RawMessage `json:"variables"`
	}
	if err := json.Unmarshal([]byte(body), &posted); err != nil {
		t.Fatalf("Setup: decoding the posted document %s: %v", body, err)
	}
	raw, found := posted.Variables["reviews"]
	if !found {
		return false, false
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("Setup: the posted reviews variable %s is not a boolean: %v", raw, err)
	}
	return value, true
}

// reviewersOf is a reviewers connection holding the given review states, with a
// further page where more is true.
func reviewersOf(more bool, states ...string) map[string]any {
	nodes := make([]map[string]any, 0, len(states))
	for _, s := range states {
		nodes = append(nodes, map[string]any{"mergeRequestInteraction": map[string]any{"reviewState": s}})
	}
	return map[string]any{"pageInfo": map[string]any{"hasNextPage": more}, "nodes": nodes}
}

// Approvals is approvedBy's count and ChangesRequested the reviewers whose state is
// REQUESTED_CHANGES. A reviewers connection with a further page leaves
// ChangesRequested alone unknown and marks the row with the pagination cap; a row
// that does not carry a key answers that count unknown.
func TestTheReviewCountsAreTheApprovalCountAndTheReviewersRequestingChanges(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, nil)
	node := func(edit func(map[string]any)) func(map[string]any) {
		return func(row map[string]any) { edit(row["mergeRequest"].(map[string]any)) }
	}
	for _, test := range []struct {
		edit             func(map[string]any)
		name             string
		approvals, asked int
		cut              bool
	}{
		{name: "recorded", approvals: 0, asked: 1},
		{
			name: "several",
			edit: node(func(mr map[string]any) {
				mr["approvedBy"] = map[string]any{"count": 3}
				mr["reviewers"] = reviewersOf(false, "APPROVED", "REQUESTED_CHANGES", "REQUESTED_CHANGES", "REVIEWED", "UNREVIEWED")
			}),
			approvals: 3, asked: 2,
		},
		{
			name: "reviewers_continue",
			edit: node(func(mr map[string]any) {
				mr["approvedBy"] = map[string]any{"count": 1}
				mr["reviewers"] = reviewersOf(true, "REQUESTED_CHANGES")
			}),
			approvals: 1, asked: forgeapi.CountUnknown, cut: true,
		},
		{
			name: "no_interaction",
			edit: node(func(mr map[string]any) {
				mr["reviewers"] = map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": []any{map[string]any{"mergeRequestInteraction": nil}}}
			}),
			approvals: 0, asked: 0,
		},
		{
			name: "not_selected",
			edit: node(func(mr map[string]any) {
				delete(mr, "approvedBy")
				delete(mr, "reviewers")
			}),
			approvals: forgeapi.CountUnknown, asked: forgeapi.CountUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var project docProject
			if err := json.Unmarshal(recordedWith(t, recorded, "graphql_pr_read", test.edit), &project); err != nil || project.MergeRequest == nil {
				t.Fatalf("Setup: decoding the read = %v, want its merge request", err)
			}
			got := h.client.normalizeDocPull(project.MergeRequest, testRef())
			if got.Action.Approvals != test.approvals || got.Action.ChangesRequested != test.asked {
				t.Errorf("normalizeDocPull = Approvals %d, ChangesRequested %d, want %d, %d",
					got.Action.Approvals, got.Action.ChangesRequested, test.approvals, test.asked)
			}
			if cut := got.Partial != nil && got.Partial.Reason == forgeapi.PartialPaginationCap; cut != test.cut {
				t.Errorf("normalizeDocPull Partial = %v, want the pagination cap %t", got.Partial, test.cut)
			}
		})
	}
}

// A REST merge request carries no approval count and no reviewer state, so every
// REST arm answers both counts unknown.
func TestARESTMergeRequestAnswersNoReviewCount(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, nil)
	var row restMergeRequest
	recorded.row(t, "rest_merge_request", &row)
	for _, path := range []restPath{restListed, restMutated, restDegraded} {
		got := h.client.normalizeRESTPull(&row, testRef(), path).Action
		if got.Approvals != forgeapi.CountUnknown || got.ChangesRequested != forgeapi.CountUnknown {
			t.Errorf("normalizeRESTPull(path %d) = Approvals %d, ChangesRequested %d, want both %d",
				path, got.Approvals, got.ChangesRequested, forgeapi.CountUnknown)
		}
	}
}

// The list document sends the reviews variable the option names, true by default,
// and with it false answers both counts unknown even over a body that carries them.
func TestTheDocumentListGatesItsReviewsOnTheOption(t *testing.T) {
	var project map[string]any
	readCaptures(t).row(t, "graphql_pr_read", &project)
	node, err := json.Marshal(project["mergeRequest"])
	if err != nil {
		t.Fatalf("Setup: re-encoding the recorded merge request: %v", err)
	}
	body := `{"data": {"queryComplexity": {"score": 128, "limit": 200}, "project": {"fullPath": "example/example",` +
		`"userPermissions": {"pushCode": true, "readMergeRequest": true}, "mergeRequests": {` +
		`"pageInfo": {"hasNextPage": false, "endCursor": null}, "nodes": [` + string(node) + `]}}}}`
	for _, test := range []struct {
		opts             []forgeapi.ListOption
		name             string
		approvals, asked int
		sent             bool
	}{
		{name: "option_false", opts: []forgeapi.ListOption{forgeapi.WithReviews(false)}, approvals: forgeapi.CountUnknown, asked: forgeapi.CountUnknown},
		{name: "default", sent: true, approvals: 0, asked: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: body})
			page, err := h.client.ListPRs(t.Context(), testRef(), test.opts...)
			if err != nil {
				t.Fatalf("ListPRs = %v, want the page", err)
			}
			if got, found := sentReviews(t, h.instance.body(documentRoute)); !found || got != test.sent {
				t.Errorf("ListPRs sent reviews %t (found %t), want %t", got, found, test.sent)
			}
			if len(page.Items) != 1 {
				t.Fatalf("ListPRs answered %d row(s), want the 1 recorded", len(page.Items))
			}
			if got := page.Items[0].Action; got.Approvals != test.approvals || got.ChangesRequested != test.asked {
				t.Errorf("ListPRs answered Approvals %d, ChangesRequested %d, want %d, %d", got.Approvals, got.ChangesRequested, test.approvals, test.asked)
			}
		})
	}
}

// TestTheReviewFieldsAreGatedWhereAnOptionReachesThem holds the requests WithReviews
// changes: the list document and the single read gate the review fields on their
// required variable, while the schema probe selects them unconditionally.
func TestTheReviewFieldsAreGatedWhereAnOptionReachesThem(t *testing.T) {
	for _, doc := range []document{prList, prRead} {
		for _, want := range []string{"$reviews: Boolean!", "approvedBy @include(if: $reviews) { count }", "reviewers(first: 100) @include(if: $reviews) {"} {
			if !strings.Contains(doc.text, want) {
				t.Errorf("the %s document does not contain %q", doc.name, want)
			}
		}
	}
	for _, want := range []string{"approvedBy { count }", "reviewers(first: 100) {", "mergeRequestInteraction { reviewState }"} {
		if !strings.Contains(docAcceptance.text, want) {
			t.Errorf("the %s document does not select %q", docAcceptance.name, want)
		}
	}
}
