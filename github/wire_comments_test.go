package github

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// recordedWith is one recorded row after edit, the shape of a record the product
// answers with a key changed or missing.
func recordedWith(t *testing.T, recorded captures, name string, edit func(row map[string]any)) []byte {
	t.Helper()
	var row map[string]any
	recorded.row(t, name, &row)
	if edit != nil {
		edit(row)
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("Setup: re-encoding the recorded %s: %v", name, err)
	}
	return raw
}

// The comment count is the record's own count keys: totalCommentsCount on a
// document's pull request, comments plus review_comments on a REST pull request,
// and comments on an issue, the connection's totalCount on a document's row. A
// record that does not carry one of them answers CountUnknown, never a zero that
// reads as a count.
func TestTheCommentCountIsTheRecordsOwnKeysAndUnknownWithoutThem(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, nil)
	pullRead := func(raw []byte) int {
		var repo docRepository
		if err := json.Unmarshal(raw, &repo); err != nil || repo.PullRequest == nil {
			t.Fatalf("Setup: decoding the read %s = %v, want its pull request", raw, err)
		}
		return h.client.normalizeDocPull(repo.PullRequest, testRef()).Comments
	}
	searchRow := func(raw []byte) int {
		var row docPullRequest
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatalf("Setup: decoding the search row: %v", err)
		}
		return h.client.normalizeDocPull(&row, testRef()).Comments
	}
	restPullCount := func(raw []byte) int {
		var row restPull
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatalf("Setup: decoding the pull request: %v", err)
		}
		return h.client.normalizeRESTPull(&row, testRef()).Comments
	}
	restIssueCount := func(raw []byte) int {
		var row restIssue
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatalf("Setup: decoding the issue: %v", err)
		}
		return h.client.normalizeIssue(&row, testRef()).Comments
	}
	docIssueCount := func(raw []byte) int {
		var row docIssue
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatalf("Setup: decoding the issue row: %v", err)
		}
		return h.client.normalizeDocIssue(&row).Comments
	}
	pull := func(edit func(map[string]any)) func(map[string]any) {
		return func(row map[string]any) { edit(row["pullRequest"].(map[string]any)) }
	}
	for _, test := range []struct {
		edit  func(map[string]any)
		count func([]byte) int
		name  string
		row   string
		want  int
	}{
		{name: "document_read", row: "graphql_pr_read", count: pullRead, want: 8},
		{
			name: "document_read_null", row: "graphql_pr_read", count: pullRead, want: forgeapi.CountUnknown,
			edit: pull(func(r map[string]any) { r["totalCommentsCount"] = nil }),
		},
		{
			name: "document_read_without_the_key", row: "graphql_pr_read", count: pullRead, want: forgeapi.CountUnknown,
			edit: pull(func(r map[string]any) { delete(r, "totalCommentsCount") }),
		},
		{name: "search_row", row: "graphql_search_row", count: searchRow, want: 8},
		{name: "rest_pull", row: "rest_pull", count: restPullCount, want: 3 + 2},
		{
			name: "rest_pull_without_review_comments", row: "rest_pull", count: restPullCount, want: forgeapi.CountUnknown,
			edit: func(r map[string]any) { delete(r, "review_comments") },
		},
		{
			name: "rest_pull_without_comments", row: "rest_pull", count: restPullCount, want: forgeapi.CountUnknown,
			edit: func(r map[string]any) { delete(r, "comments") },
		},
		{name: "rest_issue", row: "rest_issue", count: restIssueCount, want: 2},
		{
			name: "rest_issue_without_comments", row: "rest_issue", count: restIssueCount, want: forgeapi.CountUnknown,
			edit: func(r map[string]any) { delete(r, "comments") },
		},
		{name: "issue_search_row", row: "graphql_issue_search_row", count: docIssueCount, want: 2},
		{
			name: "issue_search_row_without_the_connection", row: "graphql_issue_search_row", count: docIssueCount, want: forgeapi.CountUnknown,
			edit: func(r map[string]any) { delete(r, "comments") },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.count(recordedWith(t, recorded, test.row, test.edit)); got != test.want {
				t.Errorf("Comments = %d, want %d", got, test.want)
			}
		})
	}
}
