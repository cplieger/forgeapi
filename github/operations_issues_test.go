package github

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

func TestReadingAnIssueAnswersTheRecordedIssue(t *testing.T) {
	const route = "GET /api/v3/repos/example/example/issues/1"
	h := newHarness(t, map[string]string{route: readCaptures(t).raw(t, "rest_issue")})
	item, err := h.client.ReadIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 1})
	if err != nil {
		t.Fatalf("ReadIssue = %v, want the issue", err)
	}
	if item.Ref.Number != 1 || item.Repo != testRef() || item.State != forgeapi.IssueStateClosed || item.Comments != 2 {
		t.Errorf("ReadIssue = #%d in %s %v with %d comment(s), want the recorded #1 in example/example closed with 2", item.Ref.Number, item.Repo.Selector, item.State, item.Comments)
	}
	if got := h.instance.arrived(); len(got) != 1 || got[0] != route {
		t.Errorf("ReadIssue sent %v, want the one read %s", got, route)
	}
}

// The issues route answers a pull request's number with its record, marked by the
// pull_request key the API description names, and this operation reads issues only.
func TestReadingAPullRequestsNumberAsAnIssueIsNotFound(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v3/repos/example/example/issues/2": readCaptures(t).raw(t, "rest_issue_that_is_a_pull_request"),
	})
	_, err := h.client.ReadIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 2})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindNotFound || fe.Code != "" {
		t.Fatalf("ReadIssue of a pull request's number = %v, want KindNotFound with no code", err)
	}
	if !strings.Contains(fe.Message, "pull request") {
		t.Errorf("ReadIssue of a pull request's number = message %q, want it to say the number is a pull request", fe.Message)
	}
}

// Measured 2026-11-16: a transferred issue's old address answers 301 with a
// name-addressed Location, /repos/{owner}/{repo}/issues/{number} where it now lives.
func TestReadingATransferredIssueAnswersItWhereItNowLives(t *testing.T) {
	in := newInstance()
	issue := strings.Replace(readCaptures(t).raw(t, "rest_issue"), `"number": 1`, `"number": 7`, 1)
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in.mu.Lock()
		in.requests = append(in.requests, r.Method+" "+r.URL.EscapedPath())
		in.mu.Unlock()
		switch r.URL.EscapedPath() {
		case "/api/v3/repos/example/example/issues/1":
			http.Redirect(w, r, "/api/v3/repos/example/elsewhere/issues/7", http.StatusMovedPermanently)
		case "/api/v3/repos/example/elsewhere/issues/7":
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write([]byte(issue)); err != nil {
				t.Errorf("Setup: writing the issue: %v", err)
			}
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(in.server.Close)
	h := newHarnessOver(t, in)

	item, err := h.client.ReadIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 1})
	if err != nil {
		t.Fatalf("ReadIssue of a transferred issue = %v, want it answered where it now lives", err)
	}
	if item.Repo != repoRef("example/elsewhere") || item.Ref.Number != 7 {
		t.Errorf("ReadIssue of a transferred issue = #%d in %+v, want #7 in example/elsewhere", item.Ref.Number, item.Repo)
	}
	if got := h.instance.arrived(); len(got) != 2 {
		t.Errorf("ReadIssue of a transferred issue sent %v, want the read and its hop alone: the location names the repository", got)
	}
}

// A renamed repository's hop is id-addressed, so it is no transfer: the read
// resolves the successor and, its answer carrying none, refuses with it.
func TestReadingAnIssueOfARenamedRepositoryRefusesWithItsSuccessor(t *testing.T) {
	h := renamedInstance(t, map[string]string{
		"GET /api/v3/repositories/7691631/issues/1": readCaptures(t).raw(t, "rest_issue"),
	}, true, http.StatusMovedPermanently)
	_, err := h.client.ReadIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 1})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefStale {
		t.Fatalf("ReadIssue on a renamed repository = %v, want code %q", err, forgeapi.CodeRepoRefStale)
	}
	if fe.Successor == nil || fe.Successor.Selector != "example/renamed" {
		t.Errorf("ReadIssue on a renamed repository = successor %v, want example/renamed", fe.Successor)
	}
	if got := h.instance.arrived(); len(got) != 3 {
		t.Errorf("ReadIssue on a renamed repository sent %v, want the read, its hop and the resolving read", got)
	}
}

func TestReopeningAnIssueSendsTheOpenStateAndAnswersTheIssue(t *testing.T) {
	const route = "PATCH /api/v3/repos/example/example/issues/1"
	open := strings.Replace(readCaptures(t).raw(t, "rest_issue"), `"state": "closed"`, `"state": "open"`, 1)
	h := newHarness(t, map[string]string{route: open})
	item, err := h.client.ReopenIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 1})
	if err != nil {
		t.Fatalf("ReopenIssue = %v, want the issue", err)
	}
	if got := sentBody(t, h, route); len(got) != 1 || got[keyState] != "open" {
		t.Errorf("ReopenIssue sent %v, want the open state alone", got)
	}
	if item.Ref.Number != 1 || item.State != forgeapi.IssueStateOpen {
		t.Errorf("ReopenIssue = #%d %v, want #1 open", item.Ref.Number, item.State)
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
			if sent := h.instance.count(); sent != 0 {
				t.Errorf("%s(number 0) sent %d request(s), want none: %v", test.name, sent, h.instance.arrived())
			}
		})
	}
}
