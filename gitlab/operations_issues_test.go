package gitlab

import (
	"net/http"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

const readIssueRoute = "GET /api/v4/projects/" + testEncoded + "/issues/1"

func TestReadingAnIssueAnswersTheRecord(t *testing.T) {
	h := newHarness(t, map[string]string{readIssueRoute: issueBody})
	issue, err := h.client.ReadIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 1})
	if err != nil {
		t.Fatalf("ReadIssue = %v, want the issue", err)
	}
	if issue.Ref.Number != 1 || issue.State != forgeapi.IssueStateOpen || issue.Title != "Example issue" {
		t.Errorf("ReadIssue = #%d %v %q, want #1 open %q", issue.Ref.Number, issue.State, issue.Title, "Example issue")
	}
	if got := h.instance.arrived(); len(got) != 1 || got[0] != readIssueRoute {
		t.Errorf("ReadIssue sent %v, want the one read %s", got, readIssueRoute)
	}
}

// issues.md "Update an issue": state_event reopen reopens the issue.
func TestReopeningAnIssueSendsTheReopenEvent(t *testing.T) {
	h := newHarness(t, map[string]string{issueStateRoute: issueBody})
	issue, err := h.client.ReopenIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 1})
	if err != nil {
		t.Fatalf("ReopenIssue = %v, want the issue", err)
	}
	if got := sentBody(t, h, issueStateRoute); len(got) != 1 || got[keyStateEvent] != "reopen" {
		t.Errorf("ReopenIssue sent %v, want the reopen state event alone", got)
	}
	if issue.State != forgeapi.IssueStateOpen {
		t.Errorf("ReopenIssue = state %v, want %v", issue.State, forgeapi.IssueStateOpen)
	}
}

func TestAnIssueNumberBelowOneIsRefusedBeforeAnyRequest(t *testing.T) {
	zero := forgeapi.IssueRef{Number: 0}
	for _, test := range []struct {
		call func(h *harness) error
		name string
	}{
		{name: "ReadIssue", call: func(h *harness) error { _, err := h.client.ReadIssue(t.Context(), testRef(), zero); return err }},
		{name: "ReopenIssue", call: func(h *harness) error { _, err := h.client.ReopenIssue(t.Context(), testRef(), zero); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, nil)
			err := test.call(h)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefInvalid {
				t.Errorf("%s(number 0) = %v, want code %q", test.name, err, forgeapi.CodeRepoRefInvalid)
			}
			if sent := h.instance.arrived(); len(sent) != 0 {
				t.Errorf("%s(number 0) sent %v, want nothing", test.name, sent)
			}
		})
	}
}

// The route numbers merge requests apart, so a 404 is the union answer.
func TestReadingAnIssueTheProjectLacksIsNotVisible(t *testing.T) {
	h := newHarness(t, map[string]string{readIssueRoute: `{"message":"404 Not found"}`})
	h.instance.status(readIssueRoute, http.StatusNotFound)
	_, err := h.client.ReadIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 1})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindNotFound || fe.Code != forgeapi.CodeRepoOrPRNotVisible {
		t.Errorf("ReadIssue of a missing issue = %v, want KindNotFound with %q", err, forgeapi.CodeRepoOrPRNotVisible)
	}
}
