package gitea

import (
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

const (
	issueRoute = "GET /api/v1/repos/example/example/issues/1"
	patchIssue = "PATCH /api/v1/repos/example/example/issues/1"
)

func TestReadingAnIssueAnswersTheRecord(t *testing.T) {
	h := newHarness(t, map[string]string{issueRoute: issueBody})
	issue, err := h.client.ReadIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 1})
	if err != nil {
		t.Fatalf("ReadIssue = %v, want the issue", err)
	}
	if issue.Ref.Number != 1 || issue.State != forgeapi.IssueStateOpen || issue.Title != "Example issue" {
		t.Errorf("ReadIssue = #%d %v %q, want #1 open %q", issue.Ref.Number, issue.State, issue.Title, "Example issue")
	}
	if got := h.instance.arrived(); len(got) != 1 || got[0] != issueRoute {
		t.Errorf("ReadIssue sent %v, want the one read %s", got, issueRoute)
	}
}

// Recorded on gitea.com 2026-11-16: the issue route answers a pull request's number
// with its record, pull_request set to the PullRequestMeta the swagger names.
func TestReadingAPullRequestsNumberAsAnIssueIsNotFound(t *testing.T) {
	pull := strings.TrimSuffix(issueBody, "}") + `,"pull_request":{"merged":false,"merged_at":null,"draft":false}}`
	h := newHarness(t, map[string]string{issueRoute: pull})
	_, err := h.client.ReadIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 1})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindNotFound || fe.Code != "" {
		t.Fatalf("ReadIssue of a pull request's number = %v, want KindNotFound with no code", err)
	}
	if !strings.Contains(fe.Message, "pull request") {
		t.Errorf("ReadIssue of a pull request's number = message %q, want it to say the number is a pull request", fe.Message)
	}
}

// EditIssueOption.state takes open, and the route answers 201 with the record.
func TestReopeningAnIssueSendsTheOpenState(t *testing.T) {
	h := newHarness(t, map[string]string{patchIssue: issueBody})
	issue, err := h.client.ReopenIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 1})
	if err != nil {
		t.Fatalf("ReopenIssue = %v, want the issue", err)
	}
	if got := sentBody(t, h, patchIssue); len(got) != 1 || got["state"] != stateOpen {
		t.Errorf("ReopenIssue sent %v, want the open state alone", got)
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
