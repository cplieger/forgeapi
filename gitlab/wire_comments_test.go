package gitlab

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

// The comment count is the record's user-notes count, userNotesCount on a document
// and user_notes_count over REST. The document declares the field nullable, so a
// null, like a record without the key, answers CountUnknown rather than a zero that
// reads as a count.
func TestTheCommentCountIsTheUserNotesCountAndUnknownWithoutIt(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, nil)
	document := func(raw []byte) int {
		var project docProject
		if err := json.Unmarshal(raw, &project); err != nil || project.MergeRequest == nil {
			t.Fatalf("Setup: decoding the read %s = %v, want its merge request", raw, err)
		}
		return h.client.normalizeDocPull(project.MergeRequest, testRef()).Comments
	}
	mergeRequest := func(raw []byte) int {
		var row restMergeRequest
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatalf("Setup: decoding the merge request: %v", err)
		}
		return h.client.normalizeRESTPull(&row, testRef(), restMutated).Comments
	}
	issue := func(raw []byte) int {
		var row restIssue
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatalf("Setup: decoding the issue: %v", err)
		}
		return h.client.normalizeIssue(&row, testRef()).Comments
	}
	node := func(edit func(map[string]any)) func(map[string]any) {
		return func(row map[string]any) { edit(row["mergeRequest"].(map[string]any)) }
	}
	for _, test := range []struct {
		edit  func(map[string]any)
		count func([]byte) int
		name  string
		row   string
		want  int
	}{
		{name: "document_read", row: "graphql_pr_read", count: document, want: 6},
		{
			name: "document_read_null", row: "graphql_pr_read", count: document, want: forgeapi.CountUnknown,
			edit: node(func(r map[string]any) { r["userNotesCount"] = nil }),
		},
		{name: "rest_merge_request", row: "rest_merge_request", count: mergeRequest, want: 4},
		{
			name: "rest_merge_request_without_the_key", row: "rest_merge_request", count: mergeRequest, want: forgeapi.CountUnknown,
			edit: func(r map[string]any) { delete(r, "user_notes_count") },
		},
		{name: "rest_issue", row: "rest_issue", count: issue, want: 7},
		{
			name: "rest_issue_without_the_key", row: "rest_issue", count: issue, want: forgeapi.CountUnknown,
			edit: func(r map[string]any) { delete(r, "user_notes_count") },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.count(recordedWith(t, recorded, test.row, test.edit)); got != test.want {
				t.Errorf("Comments = %d, want %d", got, test.want)
			}
		})
	}
}
