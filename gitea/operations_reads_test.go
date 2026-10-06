package gitea

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

const searchIssueRow = `{"number":4,"state":"open","title":"Example issue","user":{"login":"example-user"},` +
	`"labels":[{"name":"example-label","color":"ededed","description":"Example label."}],` +
	`"repository":{"id":1,"name":"example","owner":"example","full_name":"example/example"}}`

func TestBothIssueListsAnswerTheRowsTheirRouteServed(t *testing.T) {
	for _, test := range []struct {
		call func(h *harness) (forgeapi.Page[forgeapi.Issue], error)
		name string
	}{
		{name: "ListIssues", call: func(h *harness) (forgeapi.Page[forgeapi.Issue], error) {
			return h.client.ListIssues(t.Context(), testRef())
		}},
		{name: "ListMyIssues", call: func(h *harness) (forgeapi.Page[forgeapi.Issue], error) {
			return h.client.ListMyIssues(t.Context())
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				issuesRoute:                       "[" + searchIssueRow + "]",
				"GET /api/v1/repos/issues/search": "[" + searchIssueRow + "]",
			})
			page, err := test.call(h)
			if err != nil {
				t.Fatalf("%s = %v, want nil", test.name, err)
			}
			if len(page.Items) != 1 {
				t.Fatalf("%s returned %d row(s), want 1", test.name, len(page.Items))
			}
			got := page.Items[0]
			if got.Ref.Number != 4 || got.Repo.Selector != testSelector {
				t.Errorf("%s = issue %d in %q, want 4 in %q", test.name, got.Ref.Number, got.Repo.Selector, testSelector)
			}
			want := forgeapi.Label{Name: "example-label", Color: "ededed", Description: "Example label."}
			if len(got.Labels) != 1 || got.Labels[0] != want {
				t.Errorf("%s = labels %+v, want [%+v]", test.name, got.Labels, want)
			}
		})
	}
}

func TestListLabelsAnswersTheRepositorysLabels(t *testing.T) {
	h := newHarness(t, map[string]string{labelsRoute: labelBody})
	page, err := h.client.ListLabels(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListLabels = %v, want nil", err)
	}
	want := forgeapi.Label{Name: "example-label", Color: "ededed", Description: "Example label."}
	if len(page.Items) != 1 || page.Items[0] != want {
		t.Errorf("ListLabels = %+v, want [%+v]", page.Items, want)
	}
}

func TestMergeStatusAnswersThePullRequestsMergedState(t *testing.T) {
	h := newHarness(t, map[string]string{
		pullRoute: `{"number":1,"merged":true,"html_url":"https://forge.example/example/example/pulls/1"}`,
	})
	got, err := h.client.MergeStatus(t.Context(), testRef(), testPR())
	if err != nil {
		t.Fatalf("MergeStatus = %v, want nil", err)
	}
	if got.Merged != forgeapi.SupportYes || got.Queue != forgeapi.QueueNone || got.WebURL != "https://forge.example/example/example/pulls/1" {
		t.Errorf("MergeStatus = %+v, want merged, no queue, and the record's page", got)
	}
}

func TestACommitStatusOnABranchNamesTheCommitTheInstanceResolved(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/example/example/commits/main/status": combined("success", 1, statusRow("build", "success")),
	})
	got, err := h.client.CommitStatus(t.Context(), testRef(), "main")
	if err != nil {
		t.Fatalf("CommitStatus(%q) = %v, want nil", "main", err)
	}
	if got.Ref != testHeadSHA {
		t.Errorf("CommitStatus(%q) = ref %q, want %q, the commit the instance answered for", "main", got.Ref, testHeadSHA)
	}
}

func TestAThrottleAfterAFoldLeavesTheFoldedRowItsVerdict(t *testing.T) {
	rows := "[" + pullRow(1) + "," + pullRow(2) + "]"
	var folds atomic.Int32
	in := newInstance()
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		answer := rows
		if strings.HasSuffix(r.URL.EscapedPath(), "/status") {
			if folds.Add(1) > 1 {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(http.StatusTooManyRequests)
				answer = `{"message":"slow down"}`
			} else {
				answer = statusOK
			}
		}
		if _, err := w.Write([]byte(answer)); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(in.server.Close)
	h := newHarnessOver(t, in, time.Now)
	page, err := h.client.ListPRs(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListPRs = %v, want the rows", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("ListPRs returned %d row(s), want 2", len(page.Items))
	}
	if first := page.Items[0]; first.Action.Checks != forgeapi.CheckPassing || first.Partial != nil {
		t.Errorf("the folded row = verdict %v, partial %v, want %v and no marker", first.Action.Checks, first.Partial, forgeapi.CheckPassing)
	}
	if second := page.Items[1]; second.Partial == nil || second.Partial.Reason != forgeapi.PartialRateLimited {
		t.Errorf("the throttled row = partial %v, want the rate-limited reason", second.Partial)
	}
}
