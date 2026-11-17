package gitea

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// pullWithCounts is one pull-request record carrying the two count keys both
// products' API documents declare on a PullRequest, comments and review_comments.
const pullWithCounts = `{"number":1,"state":"open","title":"Example pull request","user":{"login":"example-user"},` +
	`"labels":[],"comments":2,"review_comments":1,` +
	`"head":{"ref":"example-feature","sha":"` + testHeadSHA + `"},"base":{"ref":"main"}}`

// The comment count of a pull-request record is comments plus review_comments, of an
// issue its comments, and a record without one of them answers CountUnknown rather
// than a zero that reads as a count.
func TestTheCommentCountIsTheRecordsOwnKeysAndUnknownWithoutThem(t *testing.T) {
	h := newHarness(t, nil)
	for _, test := range []struct {
		count func(t *testing.T, body string) int
		name  string
		body  string
		want  int
	}{
		{name: "pull_request", count: pullComments(h), body: pullWithCounts, want: 2 + 1},
		{
			name: "pull_request_without_review_comments", count: pullComments(h),
			body: strings.Replace(pullWithCounts, `"review_comments":1,`, "", 1), want: forgeapi.CountUnknown,
		},
		{
			name: "pull_request_without_comments", count: pullComments(h),
			body: strings.Replace(pullWithCounts, `"comments":2,`, "", 1), want: forgeapi.CountUnknown,
		},
		{
			name: "issue", count: issueComments(h),
			body: strings.Replace(issueBody, `"state":"open",`, `"state":"open","comments":5,`, 1), want: 5,
		},
		{name: "issue_without_comments", count: issueComments(h), body: issueBody, want: forgeapi.CountUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.count(t, test.body); got != test.want {
				t.Errorf("Comments of %s = %d, want %d", test.body, got, test.want)
			}
		})
	}
}

func pullComments(h *harness) func(t *testing.T, body string) int {
	return func(t *testing.T, body string) int {
		t.Helper()
		var p wirePull
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatalf("Setup: decoding %s: %v", body, err)
		}
		return h.client.normalizePull(&p, testRef()).Comments
	}
}

func issueComments(h *harness) func(t *testing.T, body string) int {
	return func(t *testing.T, body string) int {
		t.Helper()
		var i wireIssue
		if err := json.Unmarshal([]byte(body), &i); err != nil {
			t.Fatalf("Setup: decoding %s: %v", body, err)
		}
		return h.client.normalizeIssue(&i, testRef()).Comments
	}
}

// A cross-repository row is the issue record, which carries comments and no
// review_comments key, so its count is comments alone rather than unknown.
func TestACrossRepositoryRowCountsItsCommentsAlone(t *testing.T) {
	row := strings.Replace(searchRow(false), `"comments":0,`, `"comments":4,`, 1)
	if row == searchRow(false) {
		t.Fatal("Setup: the search row carries no comments key to set")
	}
	h := newHarness(t, map[string]string{"GET /api/v1/repos/issues/search": "[" + row + "]"})
	page, err := h.client.ListMyPRs(t.Context())
	if err != nil {
		t.Fatalf("ListMyPRs = %v, want the page", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListMyPRs answered %d row(s), want 1", len(page.Items))
	}
	if got := page.Items[0].Comments; got != 4 {
		t.Errorf("ListMyPRs row Comments = %d, want 4: the row's comments key", got)
	}
}
