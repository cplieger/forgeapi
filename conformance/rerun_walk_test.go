package conformance

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// runPageMaximum is the page size each product's re-run reads its runs at: the REST
// page maximum on GitHub and GitLab, the stated maximum the Gitea-family setup serves.
var runPageMaximum = map[spec.Product]int{spec.GitHub: 100, spec.GitLab: 100, spec.Gitea: 50, spec.Forgejo: 50}

// runPageKey is the query parameter each product's runs route takes its page size in.
var runPageKey = map[spec.Product]string{spec.GitHub: "per_page", spec.GitLab: "per_page", spec.Gitea: "limit", spec.Forgejo: "limit"}

// runPagesWalked is the most pages a re-run reads, which the table's prices state as
// "up to three pages".
const runPagesWalked = 3

// runRow is one run on head as each product's runs route answers it, failed or passed.
func runRow(p spec.Product, id int, head string, failed bool) map[string]any {
	switch p {
	case spec.GitHub:
		return map[string]any{"id": id, "head_sha": head, "status": "completed", "conclusion": map[bool]string{true: "failure", false: "success"}[failed]}
	case spec.GitLab:
		return map[string]any{"id": id, "sha": head, "status": map[bool]string{true: "failed", false: "success"}[failed], "ref": "main"}
	case spec.Forgejo:
		return map[string]any{"id": id, "commit_sha": head, "status": map[bool]string{true: "failure", false: "success"}[failed]}
	}
	return map[string]any{"id": id, "head_sha": head, "status": "completed", "conclusion": map[bool]string{true: "failure", false: "success"}[failed]}
}

// runPage is one page of the runs route carrying rows, in each product's envelope.
func runPage(t *testing.T, p spec.Product, rows []map[string]any, repeat int) wire {
	t.Helper()
	path := apiRoot(p) + "/repos/" + selector + "/actions/runs"
	body := jsonBody(t, map[string]any{"total_count": 1000, "workflow_runs": rows})
	if p == spec.GitLab {
		path = "/api/v4/projects/" + strings.ReplaceAll(selector, "/", "%2F") + "/pipelines"
		body = jsonBody(t, rows)
	}
	return wire{Method: http.MethodGet, Path: path, Status: http.StatusOK, Body: body, Repeat: repeat}
}

// passingRows is n passed runs on head, numbered from 100.
func passingRows(p spec.Product, n int) []map[string]any {
	rows := make([]map[string]any, n)
	for i := range rows {
		rows[i] = runRow(p, 100+i, headSHA, false)
	}
	return rows
}

// pullHeadRead is each product's read of the pull request's current head.
func pullHeadRead(t *testing.T, p spec.Product, head string) wire {
	t.Helper()
	if p == spec.GitLab {
		return wire{
			Method: http.MethodGet, Path: "/api/v4/projects/" + strings.ReplaceAll(selector, "/", "%2F") + "/merge_requests/1",
			Status: http.StatusOK, Body: jsonBody(t, map[string]any{"iid": prNumber, "sha": head}),
		}
	}
	return wire{
		Method: http.MethodGet, Path: apiRoot(p) + "/repos/" + selector + "/pulls/1",
		Status: http.StatusOK, Body: jsonBody(t, map[string]any{"number": prNumber, "head": map[string]any{"ref": sourceBranch, "sha": head}}),
	}
}

// rerunOver is the re-run of the canonical pull request on p, pinned to head, over the
// recorded case's setup and re-run routes with its runs read replaced by routes, and
// the requests it sent.
func rerunOver(t *testing.T, p spec.Product, head string, routes ...wire) ([]sent, error) {
	t.Helper()
	f, err := loadFixture(p, "PullRequests.RerunFailedChecks")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var kept []wire
	for _, w := range f.Routes {
		if w.Method == http.MethodGet && (strings.HasSuffix(w.Path, "/actions/runs") || strings.HasSuffix(w.Path, "/pipelines")) {
			continue
		}
		kept = append(kept, w)
	}
	srv, rec := newFixtureServer(t, fixture{path: "inline", Routes: append(routes, kept...)})
	client := pagedClient(t, p, srv, forgeapi.WithMutations(true))
	err = client.(forgeapi.PullRequests).RerunFailedChecks(t.Context(), repoRef(p), prRef(p), head)
	return rec.since(0), err
}

// runReads is the runs reads among requests.
func runReads(requests []sent) []sent {
	var out []sent
	for _, s := range requests {
		if s.method == http.MethodGet && (strings.HasSuffix(s.path, "/actions/runs") || strings.HasSuffix(s.path, "/pipelines")) {
			out = append(out, s)
		}
	}
	return out
}

// reRunOf reports whether requests carry a re-run POST addressed to run id.
func reRunOf(requests []sent, id int) bool {
	for _, s := range requests {
		if s.method == http.MethodPost && strings.Contains(s.path, fmt.Sprintf("/%d/", id)) {
			return true
		}
	}
	return false
}

// A head with more runs than one page holds is walked at the most rows a page serves
// until a failed run turns up, and refused as a list that continues where the last
// page the price allows came back full with none, never as a head with nothing failed.
func TestAReRunWalksTheHeadsRunsAtThePageMaximum(t *testing.T) {
	for _, p := range spec.Products {
		size := runPageMaximum[p]
		t.Run(string(p)+"_a_failed_run_on_page_2", func(t *testing.T) {
			sent, err := rerunOver(t, p, headSHA,
				runPage(t, p, passingRows(p, size), 1),
				runPage(t, p, []map[string]any{runRow(p, 7, headSHA, true)}, 1))
			if err != nil {
				t.Fatalf("RerunFailedChecks on %s = %v, want the re-run of run 7", p, err)
			}
			reads := runReads(sent)
			if len(reads) != 2 {
				t.Fatalf("RerunFailedChecks on %s read the runs %d times, want 2: %s", p, len(reads), describeRequests(sent))
			}
			for i, r := range reads {
				if got := r.query.Get(runPageKey[p]); got != fmt.Sprint(size) {
					t.Errorf("RerunFailedChecks on %s read page %d at %s=%s, want %d", p, i+1, runPageKey[p], got, size)
				}
				if got := r.query.Get("page"); got != fmt.Sprint(i+1) {
					t.Errorf("RerunFailedChecks on %s read page=%s as its read %d, want %d", p, got, i+1, i+1)
				}
			}
			if !reRunOf(sent, 7) {
				t.Errorf("RerunFailedChecks on %s sent %s, want the re-run of run 7", p, describeRequests(sent))
			}
		})
		t.Run(string(p)+"_the_page_cap", func(t *testing.T) {
			sent, err := rerunOver(t, p, headSHA, runPage(t, p, passingRows(p, size), runPagesWalked+1))
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRunPageTruncated {
				t.Errorf("RerunFailedChecks over %d full pages on %s = %v, want code %q", runPagesWalked, p, err, forgeapi.CodeRunPageTruncated)
			}
			if got := len(runReads(sent)); got != runPagesWalked {
				t.Errorf("RerunFailedChecks over full pages on %s read the runs %d times, want %d", p, got, runPagesWalked)
			}
			if w := writes(sent); len(w) != 0 {
				t.Errorf("RerunFailedChecks over full pages on %s wrote %s, want nothing", p, describeRequests(w))
			}
		})
		t.Run(string(p)+"_a_short_page_of_passed_runs", func(t *testing.T) {
			sent, err := rerunOver(t, p, headSHA, runPage(t, p, passingRows(p, 2), 1))
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRunNotFailed {
				t.Errorf("RerunFailedChecks over passed runs on %s = %v, want code %q", p, err, forgeapi.CodeRunNotFailed)
			}
			if w := writes(sent); len(w) != 0 {
				t.Errorf("RerunFailedChecks over passed runs on %s wrote %s, want nothing", p, describeRequests(w))
			}
		})
	}
}

// A re-run with no pin reads the pull request's current head and filters by it, so a
// failed run of another branch the instance answers beside it is never re-run.
func TestAReRunWithNoPinReRunsARunOfThePullRequestsHead(t *testing.T) {
	const other = "0000000000000000000000000000000000000000"
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			sent, err := rerunOver(t, p, "", pullHeadRead(t, p, headSHA),
				runPage(t, p, []map[string]any{runRow(p, 5, other, true), runRow(p, 7, headSHA, true)}, 1))
			if err != nil {
				t.Fatalf("RerunFailedChecks with no pin on %s = %v, want the re-run of run 7", p, err)
			}
			reads := runReads(sent)
			if len(reads) != 1 || reads[0].query.Get(map[bool]string{true: "sha", false: "head_sha"}[p == spec.GitLab]) != headSHA {
				t.Errorf("RerunFailedChecks with no pin on %s read the runs as %v, want one read filtered by the pull request's head", p, reads)
			}
			if reRunOf(sent, 5) || !reRunOf(sent, 7) {
				t.Errorf("RerunFailedChecks with no pin on %s sent %s, want the re-run of run 7 alone", p, describeRequests(sent))
			}
		})
		t.Run(string(p)+"_a_pull_request_with_no_head", func(t *testing.T) {
			sent, err := rerunOver(t, p, "", pullHeadRead(t, p, ""))
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeMissingSHA {
				t.Errorf("RerunFailedChecks on a pull request with no head on %s = %v, want code %q", p, err, forgeapi.CodeMissingSHA)
			}
			if reads := runReads(sent); len(reads) != 0 || len(writes(sent)) != 0 {
				t.Errorf("RerunFailedChecks on a pull request with no head on %s sent %s, want no run read and no write", p, describeRequests(sent))
			}
		})
	}
}
