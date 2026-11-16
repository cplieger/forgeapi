package gitea

import (
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// recordedReviews is a review list as gitea.com answered it for one pull request
// anonymously, read 2026-11-16, trimmed to the keys the counts read: two reviewers,
// each approving once.
const recordedReviews = `[{"id":385983,"user":{"id":1},"state":"APPROVED","dismissed":false,"stale":false},` +
	`{"id":386006,"user":{"id":2021},"state":"APPROVED","dismissed":false,"stale":false}]`

// review is one review row of the shape both products' API documents give the
// route.
func review(id, user int, state string, dismissed, stale bool) string {
	return `{"id":` + itoa(id) + `,"user":{"id":` + itoa(user) + `},"state":"` + state +
		`","dismissed":` + boolText(dismissed) + `,"stale":` + boolText(stale) + `}`
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// fullReviewPage is a page of exactly the page size the review read asks for.
func fullReviewPage() string {
	rows := make([]string, 0, testMaxItems)
	for i := range testMaxItems {
		rows = append(rows, review(i+1, i+1, "APPROVED", false, false))
	}
	return "[" + strings.Join(rows, ",") + "]"
}

// Per reviewer, the latest review that approves or requests changes and is neither
// dismissed nor stale decides, the latest being the greatest id: a later comment or
// review request changes nothing, and a dismissed or stale opinion counts for none.
func TestTheReviewCountsAreEachReviewersLatestStandingOpinion(t *testing.T) {
	for _, test := range []struct {
		name             string
		rows             []string
		approvals, asked int
	}{
		{name: "none", approvals: 0, asked: 0},
		{name: "recorded", rows: []string{recordedReviews[1 : len(recordedReviews)-1]}, approvals: 2, asked: 0},
		{
			name:      "a_later_request_replaces_an_approval",
			rows:      []string{review(1, 7, "APPROVED", false, false), review(2, 7, "REQUEST_CHANGES", false, false)},
			approvals: 0, asked: 1,
		},
		{
			name:      "the_greatest_id_wins_whatever_the_order",
			rows:      []string{review(9, 7, "APPROVED", false, false), review(3, 7, "REQUEST_CHANGES", false, false)},
			approvals: 1, asked: 0,
		},
		{
			name:      "a_later_comment_changes_nothing",
			rows:      []string{review(1, 7, "REQUEST_CHANGES", false, false), review(2, 7, "COMMENT", false, false), review(3, 7, "REQUEST_REVIEW", false, false)},
			approvals: 0, asked: 1,
		},
		{
			name:      "a_dismissed_or_stale_opinion_counts_for_none",
			rows:      []string{review(1, 7, "APPROVED", false, false), review(2, 7, "REQUEST_CHANGES", true, false), review(3, 8, "APPROVED", false, true)},
			approvals: 1, asked: 0,
		},
		{name: "pending_counts_for_none", rows: []string{review(1, 7, "PENDING", false, false)}, approvals: 0, asked: 0},
		{name: "no_user_names_no_reviewer", rows: []string{`{"id":1,"user":null,"state":"APPROVED"}`}, approvals: 0, asked: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				"GET /api/v1/repos/example/example/pulls/1": pullRow(1),
				statusPath:                   statusOK,
				reviewsPath(testSelector, 1): "[" + strings.Join(test.rows, ",") + "]",
			})
			pr, err := h.client.ReadPR(t.Context(), testRef(), testPR())
			if err != nil {
				t.Fatalf("ReadPR = %v, want the pull request", err)
			}
			if pr.Action.Approvals != test.approvals || pr.Action.ChangesRequested != test.asked {
				t.Errorf("ReadPR = Approvals %d, ChangesRequested %d, want %d, %d", pr.Action.Approvals, pr.Action.ChangesRequested, test.approvals, test.asked)
			}
		})
	}
}

// One page of reviews is read at the stated maximum. A page that came back full may
// continue, so both counts stay unknown and the row carries the pagination cap.
func TestAFullReviewPageAnswersUnknownWithThePaginationCap(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/example/example/pulls": "[" + pullRow(1) + "]",
		statusPath:                   statusOK,
		reviewsPath(testSelector, 1): fullReviewPage(),
	})
	page, err := h.client.ListPRs(t.Context(), testRef())
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ListPRs = %d row(s), %v, want 1", len(page.Items), err)
	}
	row := page.Items[0]
	if row.Action.Approvals != forgeapi.CountUnknown || row.Action.ChangesRequested != forgeapi.CountUnknown {
		t.Errorf("ListPRs answered Approvals %d, ChangesRequested %d, want both %d", row.Action.Approvals, row.Action.ChangesRequested, forgeapi.CountUnknown)
	}
	if row.Partial == nil || row.Partial.Reason != forgeapi.PartialPaginationCap {
		t.Errorf("ListPRs row Partial = %v, want the pagination cap", row.Partial)
	}
	if got := h.instance.query(reviewsPath(testSelector, 1), "limit"); got != itoa(testMaxItems) {
		t.Errorf("the review read asked limit=%q, want %d, the stated maximum", got, testMaxItems)
	}
	if got := h.instance.query(reviewsPath(testSelector, 1), "page"); got != "1" {
		t.Errorf("the review read asked page=%q, want 1", got)
	}
}

// A list reads each row's reviews beside its fold by default, and under
// WithReviews(false) reads none and answers both counts unknown.
func TestTheListReadsEachRowsReviewsUnlessTheOptionSaysNot(t *testing.T) {
	for _, test := range []struct {
		opts      []forgeapi.ListOption
		name      string
		approvals int
		requests  int
	}{
		{name: "default", approvals: 2, requests: 3},
		{name: "option_false", opts: []forgeapi.ListOption{forgeapi.WithReviews(false)}, approvals: forgeapi.CountUnknown, requests: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				"GET /api/v1/repos/example/example/pulls": "[" + pullRow(1) + "]",
				statusPath:                   statusOK,
				reviewsPath(testSelector, 1): recordedReviews,
			})
			page, err := h.client.ListPRs(t.Context(), testRef(), test.opts...)
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("ListPRs = %d row(s), %v, want 1", len(page.Items), err)
			}
			if got := page.Items[0].Action.Approvals; got != test.approvals {
				t.Errorf("ListPRs answered Approvals %d, want %d", got, test.approvals)
			}
			if got := h.instance.count(); got != test.requests {
				t.Errorf("ListPRs sent %d request(s) %v, want %d", got, h.instance.arrived(), test.requests)
			}
		})
	}
}

// A row the per-interval cap does not admit reads neither its status nor its
// reviews, and carries the budget marker with both counts unknown.
func TestARowTheIntervalDoesNotAdmitReadsNoReview(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/example/example/pulls": "[" + pullRow(1) + "]",
		statusPath:                   statusOK,
		reviewsPath(testSelector, 1): recordedReviews,
	}, forgeapi.WithStatusReadsPerInterval(0))
	page, err := h.client.ListPRs(t.Context(), testRef())
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ListPRs = %d row(s), %v, want 1", len(page.Items), err)
	}
	row := page.Items[0]
	if row.Action.Approvals != forgeapi.CountUnknown || row.Action.ChangesRequested != forgeapi.CountUnknown {
		t.Errorf("ListPRs answered Approvals %d, ChangesRequested %d, want both %d", row.Action.Approvals, row.Action.ChangesRequested, forgeapi.CountUnknown)
	}
	if row.Partial == nil || row.Partial.Reason != forgeapi.PartialBudget {
		t.Errorf("ListPRs row Partial = %v, want the budget marker", row.Partial)
	}
	if got := h.instance.count(); got != 1 {
		t.Errorf("ListPRs sent %d request(s) %v, want the list alone", got, h.instance.arrived())
	}
}

// A row with no head has no status to fold, and its reviews are still read.
func TestARowWithNoHeadStillReadsItsReviews(t *testing.T) {
	row := strings.Replace(pullRow(1), testHeadSHA, "", 1)
	if row == pullRow(1) {
		t.Fatal("Setup: the pull row carries no head to remove")
	}
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/example/example/pulls": "[" + row + "]",
		reviewsPath(testSelector, 1):              recordedReviews,
	})
	page, err := h.client.ListPRs(t.Context(), testRef())
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ListPRs = %d row(s), %v, want 1", len(page.Items), err)
	}
	if got := page.Items[0].Action.Approvals; got != 2 {
		t.Errorf("ListPRs answered Approvals %d, want 2", got)
	}
}
