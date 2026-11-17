package github

import (
	"context"
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

// reviewsOf is a latestOpinionatedReviews connection naming total reviewers and
// carrying the given states.
func reviewsOf(total int, states ...string) map[string]any {
	nodes := make([]map[string]any, 0, len(states))
	for _, s := range states {
		nodes = append(nodes, map[string]any{"state": s})
	}
	return map[string]any{"totalCount": total, "nodes": nodes}
}

// The counts are the latest opinionated review per reviewer, which the connection
// already is: APPROVED and CHANGES_REQUESTED counted, any other state not. A
// connection naming more reviewers than it carries, or none at all, answers both
// counts CountUnknown, since a count over a cut list would read as exact.
func TestTheReviewCountsAreTheLatestOpinionatedReviewsAndUnknownOverACutList(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, nil)
	for _, test := range []struct {
		edit             func(row map[string]any)
		name             string
		approvals, asked int
	}{
		{name: "recorded", approvals: 0, asked: 1},
		{
			name: "approvals_and_requests",
			edit: func(row map[string]any) {
				row["latestOpinionatedReviews"] = reviewsOf(4, "APPROVED", "CHANGES_REQUESTED", "APPROVED", "DISMISSED")
			},
			approvals: 2, asked: 1,
		},
		{
			name:      "none",
			edit:      func(row map[string]any) { row["latestOpinionatedReviews"] = reviewsOf(0) },
			approvals: 0, asked: 0,
		},
		{
			name:      "cut",
			edit:      func(row map[string]any) { row["latestOpinionatedReviews"] = reviewsOf(21, "APPROVED") },
			approvals: forgeapi.CountUnknown, asked: forgeapi.CountUnknown,
		},
		{
			name:      "not_selected",
			edit:      func(row map[string]any) { delete(row, "latestOpinionatedReviews") },
			approvals: forgeapi.CountUnknown, asked: forgeapi.CountUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var repo struct {
				PullRequest map[string]any `json:"pullRequest"`
			}
			recorded.row(t, "graphql_pr_read", &repo)
			if test.edit != nil {
				test.edit(repo.PullRequest)
			}
			raw, err := json.Marshal(repo.PullRequest)
			if err != nil {
				t.Fatalf("Setup: re-encoding the row: %v", err)
			}
			var node docPullRequest
			if err := json.Unmarshal(raw, &node); err != nil {
				t.Fatalf("Setup: decoding the row: %v", err)
			}
			got := h.client.normalizeDocPull(&node, testRef()).Action
			if got.Approvals != test.approvals || got.ChangesRequested != test.asked {
				t.Errorf("normalizeDocPull = Approvals %d, ChangesRequested %d, want %d, %d", got.Approvals, got.ChangesRequested, test.approvals, test.asked)
			}
		})
	}
}

// A REST record carries no review, so every REST arm answers both counts unknown.
func TestARESTPullRequestAnswersNoReviewCount(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, nil)
	var row restPull
	recorded.row(t, "rest_pull", &row)
	got := h.client.normalizeRESTPull(&row, testRef()).Action
	if got.Approvals != forgeapi.CountUnknown || got.ChangesRequested != forgeapi.CountUnknown {
		t.Errorf("normalizeRESTPull = Approvals %d, ChangesRequested %d, want both %d", got.Approvals, got.ChangesRequested, forgeapi.CountUnknown)
	}
}

// listWithReviews is the list document's answer over the recorded row with its
// review connection replaced.
func listWithReviews(t *testing.T, reviews map[string]any) string {
	t.Helper()
	var row map[string]any
	readCaptures(t).row(t, "graphql_search_row", &row)
	row["latestOpinionatedReviews"] = reviews
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("Setup: re-encoding the row: %v", err)
	}
	return documentEnvelope(`"repository":{"nameWithOwner":"example/example","viewerPermission":"WRITE",` +
		`"pullRequests":{"totalCount":1,"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[` + string(raw) + `]}}`)
}

// Both lists send the reviews variable the option names, true by default, and with
// it false answer both counts unknown even over a body that still carries reviews.
func TestThePullRequestListsGateTheirReviewsOnTheOption(t *testing.T) {
	lists := []struct {
		list func(context.Context, *harness, ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error)
		name string
	}{
		{name: "ListPRs", list: func(ctx context.Context, h *harness, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
			return h.client.ListPRs(ctx, testRef(), opts...)
		}},
		{name: "ListMyPRs", list: func(ctx context.Context, h *harness, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
			return h.client.ListMyPRs(ctx, opts...)
		}},
	}
	routes := map[string]func(*testing.T) string{
		"ListPRs":   func(t *testing.T) string { return prListEnvelope(t, false, "") },
		"ListMyPRs": func(t *testing.T) string { return searchEnvelope(t, "graphql_search_row") },
	}
	for _, list := range lists {
		for _, test := range []struct {
			opts      []forgeapi.ListOption
			name      string
			approvals int
			sent      bool
		}{
			{name: "option_false", opts: []forgeapi.ListOption{forgeapi.WithReviews(false)}, approvals: forgeapi.CountUnknown},
			{name: "default", sent: true, approvals: 2},
		} {
			t.Run(list.name+"_"+test.name, func(t *testing.T) {
				h := newHarness(t, map[string]string{documentRoute: routes[list.name](t)})
				page, err := list.list(t.Context(), h, test.opts...)
				if err != nil {
					t.Fatalf("%s = %v, want the page", list.name, err)
				}
				if got, found := sentReviews(t, h.instance.body(documentRoute)); !found || got != test.sent {
					t.Errorf("%s sent reviews %t (found %t), want %t", list.name, got, found, test.sent)
				}
				if len(page.Items) != 1 {
					t.Fatalf("%s answered %d row(s), want the 1 recorded", list.name, len(page.Items))
				}
				if got := page.Items[0].Action; got.Approvals != test.approvals {
					t.Errorf("%s answered Approvals %d, want %d", list.name, got.Approvals, test.approvals)
				}
			})
		}
	}
}

// A row naming more reviewers than its connection carries is marked with the
// pagination cap, and a list asked not to read reviews marks nothing.
func TestAListRowWithACutReviewListCarriesThePaginationCap(t *testing.T) {
	for _, test := range []struct {
		opts   []forgeapi.ListOption
		name   string
		marked bool
	}{
		{name: "read", marked: true},
		{name: "option_false", opts: []forgeapi.ListOption{forgeapi.WithReviews(false)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: listWithReviews(t, reviewsOf(21, "APPROVED"))})
			page, err := h.client.ListPRs(t.Context(), testRef(), test.opts...)
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("ListPRs = %d row(s), %v, want the 1 recorded", len(page.Items), err)
			}
			row := page.Items[0]
			if row.Action.Approvals != forgeapi.CountUnknown || row.Action.ChangesRequested != forgeapi.CountUnknown {
				t.Errorf("ListPRs answered Approvals %d, ChangesRequested %d, want both %d", row.Action.Approvals, row.Action.ChangesRequested, forgeapi.CountUnknown)
			}
			marked := row.Partial != nil && row.Partial.Reason == forgeapi.PartialPaginationCap
			if marked != test.marked || (!test.marked && row.Partial != nil) {
				t.Errorf("ListPRs row Partial = %v, want the pagination cap %t", row.Partial, test.marked)
			}
		})
	}
}

// The single read selects reviews and the merge-state read, which returns no pull
// request, selects none.
func TestTheSingleReadSelectsReviewsAndTheMergeStateReadDoesNot(t *testing.T) {
	h := newHarness(t, map[string]string{documentRoute: prReadEnvelope(t)})
	pr, err := h.client.ReadPR(t.Context(), testRef(), testPR())
	if err != nil {
		t.Fatalf("ReadPR = %v, want the pull request", err)
	}
	if got, found := sentReviews(t, h.instance.body(documentRoute)); !found || !got {
		t.Errorf("ReadPR sent reviews %t (found %t), want true", got, found)
	}
	if pr.Action.Approvals != 0 || pr.Action.ChangesRequested != 1 {
		t.Errorf("ReadPR answered Approvals %d, ChangesRequested %d, want 0, 1 as recorded", pr.Action.Approvals, pr.Action.ChangesRequested)
	}
	if _, err := h.client.MergeStatus(t.Context(), testRef(), testPR()); err != nil {
		t.Fatalf("MergeStatus = %v, want the status", err)
	}
	if got, found := sentReviews(t, h.instance.body(documentRoute)); !found || got {
		t.Errorf("MergeStatus sent reviews %t (found %t), want false", got, found)
	}
}

// Every document that selects the reviews gates them on one variable, which the
// lists require and the single read defaults to true.
func TestThePullRequestDocumentsGateTheReviewsOnOneVariable(t *testing.T) {
	for _, test := range []struct {
		doc      document
		declares string
	}{
		{doc: prList, declares: "$reviews: Boolean!"},
		{doc: prMine, declares: "$reviews: Boolean!"},
		{doc: prRead, declares: "$reviews: Boolean = true"},
	} {
		t.Run(test.doc.name, func(t *testing.T) {
			want := "latestOpinionatedReviews(first: 20) @include(if: $reviews)"
			for _, w := range []string{test.declares, want} {
				if !strings.Contains(test.doc.text, w) {
					t.Errorf("the %s document does not contain %q", test.doc.name, w)
				}
			}
		})
	}
}
