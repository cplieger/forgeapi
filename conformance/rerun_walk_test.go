package conformance

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// walkedProducts are the products whose re-run walks the head's runs for one that
// failed; GitLab retries the head pipeline it matches to the merge request's head
// instead, held by TestAGitLabReRunRetriesThePipelineGitLabMatchesToTheHead.
var walkedProducts = []spec.Product{spec.GitHub, spec.Gitea, spec.Forgejo}

// runPageMaximum is the page size each product's re-run reads its runs at: the REST
// page maximum on GitHub, the stated maximum the Gitea-family setup serves.
var runPageMaximum = map[spec.Product]int{spec.GitHub: 100, spec.Gitea: 50, spec.Forgejo: 50}

// runPageKey is the query parameter each product's runs route takes its page size in.
var runPageKey = map[spec.Product]string{spec.GitHub: "per_page", spec.Gitea: "limit", spec.Forgejo: "limit"}

// runPagesWalked is the most pages a re-run reads, which the table's prices state as
// "up to three pages".
const runPagesWalked = 3

// runRow is one run on head as each product's runs route answers it, failed or passed.
func runRow(p spec.Product, id int, head string, failed bool) map[string]any {
	switch p {
	case spec.GitHub:
		return map[string]any{"id": id, "head_sha": head, "status": "completed", "conclusion": map[bool]string{true: "failure", false: "success"}[failed]}
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
	return wire{
		Method: http.MethodGet, Path: apiRoot(p) + "/repos/" + selector + "/pulls/1",
		Status: http.StatusOK, Body: jsonBody(t, map[string]any{"number": prNumber, "head": map[string]any{"ref": sourceBranch, "sha": head}}),
	}
}

// rerunOver is the re-run of the canonical pull request on p, pinned to head, over the
// recorded case's setup and re-run routes with each read routes also answers replaced
// by it, and the requests it sent.
func rerunOver(t *testing.T, p spec.Product, head string, routes ...wire) ([]sent, error) {
	t.Helper()
	f, err := loadFixture(p, "PullRequests.RerunFailedChecks")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var kept []wire
	for _, w := range f.Routes {
		replaced := slices.ContainsFunc(routes, func(r wire) bool { return r.Method == w.Method && r.Path == w.Path })
		if w.Method != http.MethodGet || !replaced {
			kept = append(kept, w)
		}
	}
	srv, rec := newFixtureServer(t, fixture{path: "inline", Routes: append(routes, kept...)})
	client := pagedClient(t, p, srv, forgeapi.WithMutations(true))
	err = client.(forgeapi.PullRequests).RerunFailedChecks(t.Context(), repoRef(p), prRef(p), head)
	return rec.since(0), err
}

// runReads is the runs reads among requests, GitLab's pipelines listing included.
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
	for _, p := range walkedProducts {
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

// linked is a failed run on the head as a run triggered by event answers it, with the
// numbers of the pull requests the forge links it to and the repository its head
// came from.
func linked(p spec.Product, id int, event string, pulls []int, headRepo string) map[string]any {
	row := runRow(p, id, headSHA, true)
	links := make([]map[string]any, 0, len(pulls))
	for _, n := range pulls {
		links = append(links, map[string]any{"number": n})
	}
	row["event"], row["trigger_event"] = event, event
	row["pull_requests"] = links
	row["head_repository"] = map[string]any{"full_name": headRepo}
	return row
}

// Choosing which run a re-run targets: on GitHub, Gitea and Forgejo the run re-run is
// a failed or cancelled run on the pull request's current head SHA, whatever its event
// or pull request, because the checks verdict a consumer shows red is that commit's
// rollup or combined status, which counts every run on the commit. A run's pull
// request link is no filter: GitHub answers a fork pull request's run with an empty
// pull_requests array (https://github.com/github/docs/issues/22501).
func TestAReRunTargetsAFailedRunOnThePullRequestsHeadSHA(t *testing.T) {
	const fork = "example-fork/example"
	for _, p := range walkedProducts {
		size := runPageMaximum[p]
		for _, test := range []struct {
			name  string
			pages [][]map[string]any
			rerun int
		}{
			{name: "the_pull_requests_own_run", pages: [][]map[string]any{{linked(p, 7, "pull_request", []int{prNumber}, selector)}}, rerun: 7},
			{name: "another_pull_requests_failed_run_on_the_same_sha_re_run_since_the_commits_verdict_counts_it", pages: [][]map[string]any{{linked(p, 7, "pull_request", []int{prNumber + 1}, selector)}}, rerun: 7},
			{name: "a_fork_pull_requests_run_with_no_pull_request_link", pages: [][]map[string]any{{linked(p, 7, "pull_request", nil, fork)}}, rerun: 7},
			{name: "a_push_run_on_the_head_re_run_since_the_commits_verdict_counts_it", pages: [][]map[string]any{{linked(p, 7, "push", nil, selector)}}, rerun: 7},
			{name: "a_green_run_refused", pages: [][]map[string]any{passingRows(p, 2)}},
			{name: "a_failed_run_past_page_1", pages: [][]map[string]any{passingRows(p, size), {linked(p, 7, "pull_request", []int{prNumber}, selector)}}, rerun: 7},
		} {
			t.Run(string(p)+"_"+test.name, func(t *testing.T) {
				routes := make([]wire, 0, len(test.pages))
				for _, rows := range test.pages {
					routes = append(routes, runPage(t, p, rows, 1))
				}
				sent, err := rerunOver(t, p, headSHA, routes...)
				if test.rerun == 0 {
					var fe *forgeapi.Error
					if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRunNotFailed {
						t.Errorf("RerunFailedChecks over passed runs on %s = %v, want code %q", p, err, forgeapi.CodeRunNotFailed)
					}
					if w := writes(sent); len(w) != 0 {
						t.Errorf("RerunFailedChecks over passed runs on %s wrote %s, want nothing", p, describeRequests(w))
					}
					return
				}
				if err != nil {
					t.Fatalf("RerunFailedChecks on %s = %v, want the re-run of run %d", p, err, test.rerun)
				}
				if got := len(runReads(sent)); got != len(test.pages) {
					t.Errorf("RerunFailedChecks on %s read the runs %d times, want %d", p, got, len(test.pages))
				}
				if w := writes(sent); len(w) != 1 || !reRunOf(w, test.rerun) {
					t.Errorf("RerunFailedChecks on %s wrote %s, want the re-run of run %d alone", p, describeRequests(w), test.rerun)
				}
			})
		}
	}
}

// A re-run with no pin reads the pull request's current head and filters by it, so a
// failed run of another branch the instance answers beside it is never re-run.
func TestAReRunWithNoPinReRunsARunOfThePullRequestsHead(t *testing.T) {
	const other = "0000000000000000000000000000000000000000"
	for _, p := range walkedProducts {
		t.Run(string(p), func(t *testing.T) {
			sent, err := rerunOver(t, p, "", pullHeadRead(t, p, headSHA),
				runPage(t, p, []map[string]any{runRow(p, 5, other, true), runRow(p, 7, headSHA, true)}, 1))
			if err != nil {
				t.Fatalf("RerunFailedChecks with no pin on %s = %v, want the re-run of run 7", p, err)
			}
			reads := runReads(sent)
			if len(reads) != 1 || reads[0].query.Get("head_sha") != headSHA {
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

// gitlabProject is the canonical project as GitLab's routes address it.
var gitlabProject = "/api/v4/projects/" + strings.ReplaceAll(selector, "/", "%2F")

// rerunHeadAnswer is GitLab's RerunHead document answered for the canonical project,
// id 1, whose merge request carries head and the matched head pipeline pipeline, or
// null where pipeline is nil.
func rerunHeadAnswer(t *testing.T, head string, pipeline map[string]any) wire {
	t.Helper()
	mr := map[string]any{"diffHeadSha": head, "headPipeline": pipeline}
	return wire{GraphQL: true, Status: http.StatusOK, Body: jsonBody(t, map[string]any{
		"data": map[string]any{"project": map[string]any{"id": "gid://gitlab/Project/1", "mergeRequest": mr}},
	})}
}

// headPipelineRead is GitLab's REST read of the canonical merge request on head, its
// stored head pipeline answered as pipeline, or null where pipeline is nil.
func headPipelineRead(t *testing.T, head string, pipeline map[string]any) wire {
	t.Helper()
	return wire{
		Method: http.MethodGet, Path: gitlabProject + "/merge_requests/1", Status: http.StatusOK,
		Body: jsonBody(t, map[string]any{"iid": prNumber, "sha": head, "source_project_id": 1, "target_project_id": 1, "head_pipeline": pipeline}),
	}
}

// gitlabRerunOver is the re-run of the canonical merge request on GitLab, pinned to
// pin, after connection setup, over the recorded case with its RerunHead answer
// replaced by resolve, or, on a connection whose setup question is refused, with the
// document routes replaced by that refusal and resolve, the REST merge request read.
func gitlabRerunOver(t *testing.T, pin string, degraded bool, resolve wire) ([]sent, error) {
	t.Helper()
	f, err := loadFixture(spec.GitLab, "PullRequests.RerunFailedChecks")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	refused := wire{GraphQL: true, Status: http.StatusOK, Body: jsonBody(t, map[string]any{
		"errors": []map[string]any{{"message": "Field 'diffHeadSha' doesn't exist on type 'MergeRequest'", "extensions": map[string]any{"code": "undefinedField"}}},
	})}
	var routes []wire
	documents := 0
	for _, w := range f.Routes {
		switch {
		case !w.GraphQL:
			routes = append(routes, w)
		case documents == 0 && degraded:
			routes = append(routes, refused, resolve)
		case documents == 0:
			routes = append(routes, w)
		case !degraded:
			routes = append(routes, resolve)
		}
		if w.GraphQL {
			documents++
		}
	}
	forkRetry := wire{Method: http.MethodPost, Path: "/api/v4/projects/99/pipelines/538/retry", Status: http.StatusCreated, Body: jsonBody(t, map[string]any{})}
	srv, rec := newFixtureServer(t, fixture{path: "inline", Routes: append(routes, forkRetry)})
	client := pagedClient(t, spec.GitLab, srv, forgeapi.WithMutations(true))
	if _, err := client.(forgeapi.Capabilities).ConnectionCaps(t.Context()); err != nil {
		t.Fatalf("Setup: ConnectionCaps = %v", err)
	}
	err = client.(forgeapi.PullRequests).RerunFailedChecks(t.Context(), repoRef(spec.GitLab), prRef(spec.GitLab), pin)
	return rec.since(0), err
}

// GitLab's re-run retries the head pipeline GitLab itself matches to the merge
// request's head, at its own project: GraphQL headPipeline is diff_head_pipeline,
// matched by sha or source_sha (app/graphql/types/merge_request_type.rb), so a merged
// results pipeline of the head answers, on a merge commit, and one of an earlier
// head answers null. The REST merge request answers the stored head pipeline
// unmatched, so where the documents are refused one on another commit is refused.
func TestAGitLabReRunRetriesThePipelineGitLabMatchesToTheHead(t *testing.T) {
	const mergeCommit = "1604b0c46c395822e4e9478777f8e54ac99fe5b9"
	matched := func(project int, status string) map[string]any {
		return map[string]any{"id": "gid://gitlab/Ci::Pipeline/538", "status": status, "project": map[string]any{"id": fmt.Sprintf("gid://gitlab/Project/%d", project)}}
	}
	stored := func(sha, ref, status string) map[string]any {
		return map[string]any{"id": 538, "project_id": 1, "sha": sha, "ref": ref, "status": status}
	}
	ownRetry := gitlabProject + "/pipelines/538/retry"
	for _, test := range []struct {
		name, pin   string
		degraded    bool
		resolve     func(*testing.T) wire
		retry, code string
	}{
		{
			name: "a_merged_results_pipeline_matched_to_the_head", pin: headSHA,
			resolve: func(t *testing.T) wire { return rerunHeadAnswer(t, headSHA, matched(1, "FAILED")) },
			retry:   ownRetry,
		},
		{
			name: "a_fork_pipeline_in_the_forks_project", pin: headSHA,
			resolve: func(t *testing.T) wire { return rerunHeadAnswer(t, headSHA, matched(99, "FAILED")) },
			retry:   "/api/v4/projects/99/pipelines/538/retry",
		},
		{
			name: "an_earlier_heads_merged_results_pipeline_gitlab_answers_null", pin: headSHA,
			resolve: func(t *testing.T) wire { return rerunHeadAnswer(t, headSHA, nil) },
			code:    forgeapi.CodeRunNotFailed,
		},
		{
			name: "a_pin_the_merge_request_moved_past", pin: mergeCommit,
			resolve: func(t *testing.T) wire { return rerunHeadAnswer(t, headSHA, matched(1, "FAILED")) },
			code:    forgeapi.CodeStaleHead,
		},
		{
			name: "a_green_pipeline", pin: headSHA,
			resolve: func(t *testing.T) wire { return rerunHeadAnswer(t, headSHA, matched(1, "SUCCESS")) },
			code:    forgeapi.CodeRunNotFailed,
		},
		{
			name: "documents_refused_a_stored_pipeline_on_the_head", pin: headSHA, degraded: true,
			resolve: func(t *testing.T) wire { return headPipelineRead(t, headSHA, stored(headSHA, sourceBranch, "failed")) },
			retry:   ownRetry,
		},
		{
			name: "documents_refused_a_stored_pipeline_on_another_commit", pin: headSHA, degraded: true,
			resolve: func(t *testing.T) wire {
				return headPipelineRead(t, headSHA, stored(mergeCommit, "refs/merge-requests/1/merge", "failed"))
			},
			code: forgeapi.CodeRunNotFailed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			arrived, err := gitlabRerunOver(t, test.pin, test.degraded, test.resolve(t))
			if reads := runReads(arrived); len(reads) != 0 {
				t.Errorf("RerunFailedChecks on GitLab read the pipelines as %s, want the merge request resolved alone", describeRequests(reads))
			}
			var w []sent
			for _, s := range writes(arrived) {
				if !strings.HasSuffix(s.path, "/graphql") {
					w = append(w, s)
				}
			}
			if test.code != "" {
				var fe *forgeapi.Error
				if !asForgeError(err, &fe) || fe.Code != test.code {
					t.Errorf("RerunFailedChecks on GitLab = %v, want code %q", err, test.code)
				}
				if len(w) != 0 {
					t.Errorf("RerunFailedChecks on GitLab wrote %s, want nothing", describeRequests(w))
				}
				return
			}
			if err != nil {
				t.Fatalf("RerunFailedChecks on GitLab = %v, want the retry of pipeline 538", err)
			}
			if len(w) != 1 || w[0].path != test.retry {
				t.Errorf("RerunFailedChecks on GitLab wrote %s, want POST %s alone: %s", describeRequests(w), test.retry, headPipelineRerun)
			}
		})
	}
}
