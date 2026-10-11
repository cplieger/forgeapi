package conformance

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// liveMutation is what one mutation needs around its call when it runs live on a
// sandbox. prepare makes what the call writes against, points the subject at it and
// registers the removal of each object as it makes it; made, where the call's own
// answer leaves something to remove or to wait for, registers that from the answer,
// and runs before the removals prepare registered. Every object is named under the
// lane's marks, so what a failed removal leaves is found by the next run's sweep.
//
// fixes is every cell of the call's answer that prepare and the call's own request
// fix on the prepared subject, with the value each must hold: the state the call
// leaves the object in, and the title, body, labels, base and head the lane itself
// sent. The live case asserts each one, since no subject can move it, except a cell
// the product's own entry marks cannot supply, where the table's value holds instead.
type liveMutation struct {
	prepare func(t *testing.T, l *lane, s *subject)
	made    func(t *testing.T, l *lane, s subject, got any)
	fixes   func(s subject) map[string]any
}

// liveMutations holds one entry per mutation the contract declares, which
// TestEveryMutationHasItsLiveSetup holds in both directions.
var liveMutations = map[string]liveMutation{
	"Issues.CreateIssue": {
		prepare: func(_ *testing.T, _ *lane, s *subject) { titled(s) },
		made: func(t *testing.T, l *lane, _ subject, got any) {
			n := got.(forgeapi.Issue).Ref.Number
			l.later(t, "closing the issue the case opened", func(ctx context.Context) error { return l.closeIssue(ctx, n) })
		},
		fixes: func(s subject) map[string]any {
			return map[string]any{
				"State": forgeapi.IssueStateOpen, "Title": s.prefix + issueTitle, "Body": issueBody, "Labels[].Name": s.label,
			}
		},
	},
	"Issues.CloseIssue": {
		prepare: func(t *testing.T, l *lane, s *subject) {
			titled(s)
			n, err := l.openIssue(t.Context(), s.prefix+issueTitle)
			if err != nil {
				t.Fatalf("Setup: opening the issue the case closes: %v", err)
			}
			l.later(t, "closing the issue the setup opened", func(ctx context.Context) error { return l.closeIssue(ctx, n) })
			s.issue = forgeapi.IssueRef{Number: n}
		},
		fixes: func(s subject) map[string]any {
			return map[string]any{"State": forgeapi.IssueStateClosed, "Title": s.prefix + issueTitle, "Body": issueBody}
		},
	},
	"PullRequests.CreatePR": {
		prepare: func(t *testing.T, l *lane, s *subject) {
			titled(s)
			s.target = mustBranch(t, l, laneName("create-pr-base"))
			s.source, _ = mustBranchWithCommit(t, l, laneName("create-pr-head"))
		},
		made: func(t *testing.T, l *lane, _ subject, got any) {
			n := got.(forgeapi.PullRequest).Ref.Number
			l.later(t, "closing the pull request the case opened", func(ctx context.Context) error { return l.closePRIfOpen(ctx, n) })
		},
		fixes: func(s subject) map[string]any {
			cells := openedPullRequest(s, forgeapi.PRStateOpen)
			cells["Labels[].Name"] = s.label
			return cells
		},
	},
	"PullRequests.ClosePR": {
		prepare: func(t *testing.T, l *lane, s *subject) { openPullRequest(t, l, s, "close-pr") },
		fixes:   func(s subject) map[string]any { return openedPullRequest(s, forgeapi.PRStateClosed) },
	},
	"PullRequests.ReopenPR": {
		prepare: func(t *testing.T, l *lane, s *subject) {
			openPullRequest(t, l, s, "reopen-pr")
			if err := l.closePR(t.Context(), s.pr.Number); err != nil {
				t.Fatalf("Setup: closing the pull request the case reopens: %v", err)
			}
		},
		fixes: func(s subject) map[string]any { return openedPullRequest(s, forgeapi.PRStateOpen) },
	},
	"PullRequests.RerunFailedChecks": {
		prepare: prepareRerun,
	},
	"Merges.MergePR": {
		prepare: func(t *testing.T, l *lane, s *subject) {
			openPullRequest(t, l, s, "merge")
			l.settleMergeable(t.Context(), s.pr.Number)
		},
		made: func(t *testing.T, l *lane, s subject, got any) {
			switch got.(forgeapi.MergeOutcome).State {
			case forgeapi.MergeOutcomeAccepted, forgeapi.MergeOutcomeEnqueued, forgeapi.MergeOutcomeInFlight:
				l.later(t, "letting the merge the product accepted land", func(ctx context.Context) error {
					return l.awaitMerged(ctx, s.pr.Number)
				})
			}
		},
		fixes: func(s subject) map[string]any {
			answer := mergeAnswer(s.product)
			return map[string]any{"State": answer.State, "Code": answer.Code}
		},
	},
	"Releases.CreateRelease": {
		prepare: func(t *testing.T, l *lane, s *subject) {
			titled(s)
			trunk, err := l.defaultBranch(t.Context())
			if err != nil {
				t.Fatalf("Setup: reading the sandbox's default branch: %v", err)
			}
			tag := laneName("release")
			s.tag, s.target = tag, trunk
			// The tag is known before the call, so its removal is registered
			// before it: a creation that failed half way leaves nothing behind
			// either.
			l.later(t, "removing the release the case created and its tag", func(ctx context.Context) error { return l.removeRelease(ctx, tag) })
		},
		fixes: func(s subject) map[string]any {
			return map[string]any{
				"TagName": s.tag, "Name": s.prefix + releaseName, "Body": releaseBody, "Draft": false, "Prerelease": true,
			}
		},
	},
}

// A merge is pinned to the head the caller read, so a merge pinned to a commit that
// is not the head merges nothing. GitHub's asynchronous merge reads the pin from one
// key of its body and merges the current head when that key is absent, so this is
// the case that fails when the pin rides another key.
func TestLiveAGitHubMergePinnedToAnotherCommitLeavesThePullRequestOpen(t *testing.T) {
	p := spec.GitHub
	l, ok := sandboxLane(p)
	if !ok {
		t.Skipf("no GitHub sandbox is named (%s, %s and %s), so no merge is sent", liveURLVar(p), liveTokenVar(p), liveVar(p, "SANDBOX"))
	}
	s := liveSubject(p)
	openPullRequest(t, l, &s, "merge-pin")
	l.settleMergeable(t.Context(), s.pr.Number)
	other, err := l.githubHead(t.Context(), s.target)
	if err != nil || other == s.ref {
		t.Fatalf("Setup: reading the base %s's head = %q, error %v, want a commit other than the pull request's head %s", s.target, other, err, s.ref)
	}
	base := os.Getenv(liveURLVar(p))
	client, err := guard(func() (any, error) {
		return newClient(p, forgeapi.Connection{WebBaseURL: base}, liveOptions(base, &counting{next: &http.Transport{}}, os.Getenv(liveTokenVar(p)))...)
	})
	if err != nil {
		t.Fatalf("Setup: GitHub client for %s: %v", base, err)
	}
	merges, _ := client.(forgeapi.Merges)
	got, mergeErr := merges.MergePR(t.Context(), s.repo, s.pr, forgeapi.MergeRequest{HeadSHA: other})
	if mergeErr == nil {
		t.Errorf("MergePR of #%d pinned to %s, not its head %s = %+v, want a refusal", s.pr.Number, other, s.ref, got)
		l.later(t, "letting the merge the product accepted land", func(ctx context.Context) error { return l.awaitMerged(ctx, s.pr.Number) })
	} else if fe := (*forgeapi.Error)(nil); !asForgeError(mergeErr, &fe) || fe.Code != forgeapi.CodeNotMergeable {
		// The route answers a pin naming another commit with a 400 whose cause is
		// human text alone, so the refusal a consumer branches on is not_mergeable.
		t.Errorf("MergePR of #%d pinned to %s = %v, want code %q", s.pr.Number, other, mergeErr, forgeapi.CodeNotMergeable)
	}
	// A merge the route accepted runs in the background, so the pull request is read
	// after a bounded wait rather than at once.
	var pr struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
	}
	for range 5 {
		if laneWait(t.Context()) != nil {
			break
		}
	}
	if _, err := l.call(t.Context(), http.MethodGet, l.prPath(s.pr.Number), nil, &pr); err != nil {
		t.Fatalf("reading #%d after the pinned merge: %v", s.pr.Number, err)
	}
	if pr.State != "open" || pr.Merged {
		t.Errorf("#%d after a merge pinned to %s = state %q merged %t, want open and unmerged", s.pr.Number, other, pr.State, pr.Merged)
	}
}

// mergeAnswer is what the merge of the lane's prepared pull request answers, a
// mergeable one into a base no queue protects: GitHub's merge is asynchronous, so it
// accepts the merge and runs it in the background, and every other product completes
// it when it answers.
func mergeAnswer(p spec.Product) forgeapi.MergeOutcome {
	if p == spec.GitHub {
		return forgeapi.MergeOutcome{State: forgeapi.MergeOutcomeAccepted, Code: forgeapi.CodeAlreadyEnqueued}
	}
	return forgeapi.MergeOutcome{State: forgeapi.MergeOutcomeMerged}
}

// openedPullRequest is what the lane fixes on a pull request it opened, from the
// head into the base the subject names under the run's title, in the state the
// call leaves it.
func openedPullRequest(s subject, state forgeapi.PRState) map[string]any {
	return map[string]any{
		"State": state, "Title": s.prefix + prTitle, "Body": prBody, "SourceBranch": s.source, "TargetBranch": s.target,
	}
}

// titled points a creation's text at this run: every title starts with the run's
// prefix, and the label applied is the lane's.
func titled(s *subject) {
	s.prefix = laneTitlePrefix()
	s.label = laneLabel(s.product)
}

// laneLabel is the label the lane applies and the seed pull request carries: the
// one the product's LABEL variable names, where it differs from the canonical one.
func laneLabel(p spec.Product) string {
	return env(p, "LABEL", labelName)
}

// mustBranch makes a branch at the default branch's head and registers its removal.
func mustBranch(t *testing.T, l *lane, name string) string {
	t.Helper()
	if err := l.createBranch(t.Context(), name); err != nil {
		t.Fatalf("Setup: creating the branch %s: %v", name, err)
	}
	l.later(t, "deleting the branch "+name, func(ctx context.Context) error { return l.deleteBranch(ctx, name) })
	return name
}

// mustBranchWithCommit makes a branch one commit ahead of the default branch,
// registers its removal, and answers the branch and the commit.
func mustBranchWithCommit(t *testing.T, l *lane, name string) (string, string) {
	t.Helper()
	sha, err := l.createBranchWithCommit(t.Context(), name)
	if err != nil {
		t.Fatalf("Setup: creating the branch %s with a commit: %v", name, err)
	}
	l.later(t, "deleting the branch "+name, func(ctx context.Context) error { return l.deleteBranch(ctx, name) })
	return name, sha
}

// openPullRequest opens a pull request the case acts on, from a branch one commit
// ahead into a base the run made, and points the subject at it and its head.
func openPullRequest(t *testing.T, l *lane, s *subject, kind string) {
	t.Helper()
	titled(s)
	base := mustBranch(t, l, laneName(kind+"-base"))
	head, sha := mustBranchWithCommit(t, l, laneName(kind+"-head"))
	openOn(t, l, s, head, base, sha)
}

// openOn opens the pull request from head into base and registers its closing.
func openOn(t *testing.T, l *lane, s *subject, head, base, sha string) {
	t.Helper()
	n, err := l.openPR(t.Context(), head, base, s.prefix+prTitle)
	if err != nil {
		t.Fatalf("Setup: opening the pull request from %s into %s: %v", head, base, err)
	}
	l.later(t, "closing the pull request the setup opened", func(ctx context.Context) error { return l.closePRIfOpen(ctx, n) })
	s.pr = forgeapi.PRRef{Number: n, Sigil: sigil(s.product)}
	s.ref, s.source, s.target = sha, head, base
}

// prepareRerun opens a pull request whose head the sandbox's own CI ran on, and
// waits a bounded time for that CI to finish with its failing job, which is what a
// re-run of the failed checks acts on. The product's RUN_WAIT variable sets the
// bound; zero says the instance runs no CI at all, which is the lane's own
// throwaway instances, so the case skips with that reason rather than waiting.
func prepareRerun(t *testing.T, l *lane, s *subject) {
	t.Helper()
	name := liveVar(s.product, "RUN_WAIT")
	raw := os.Getenv(name)
	if raw == "" {
		raw = laneRunWaitDefault
	}
	bound, err := time.ParseDuration(raw)
	if err != nil || bound < 0 {
		t.Fatalf("Setup: %s = %q, want a duration such as %s, or 0 for an instance that runs no CI", name, raw, laneRunWaitDefault)
	}
	if bound == 0 {
		t.Skipf("%s is 0, so this instance runs no CI: no run on a pull request's head ever finishes there, and a re-run of failed checks needs one that finished with a failed job", name)
	}
	titled(s)
	base := mustBranch(t, l, laneName("rerun-base"))
	head, sha := mustBranchWithCommit(t, l, laneCIName("rerun"))
	openOn(t, l, s, head, base, sha)
	v, err := l.waitForRuns(t.Context(), sha, bound)
	switch {
	case err != nil:
		t.Fatalf("Setup: reading the CI runs on %s: %v", sha, err)
	case v.refused:
		t.Skipf("the instance failed every CI run on %s before any job started (%s), so there is no failed job to re-run: gitlab.com answers this way, with the failure reason that the user is not verified, for an account whose identity it has not verified", sha, v.summary)
	case !v.finished:
		t.Skipf("the sandbox's own CI on %s had not finished within %s (%s), so there is no failed job to re-run: an instance whose runners take none of its jobs answers this way", sha, bound, v.summary)
	case !v.failed:
		t.Fatalf("Setup: the sandbox's own CI on %s finished with %s and no failing run, want its failing job: the sandbox's CI configuration yields one passing and one failing job on a push to a forgeapi-ci-* branch", sha, v.summary)
	}
	t.Logf("the sandbox's own CI on %s finished with %s", sha, v.summary)
}

// TestEveryMutationHasItsLiveSetup holds the live setups to the contract in both
// directions: a mutation with no setup would run live against a subject nothing
// made, writing to the canonical branches and numbers of whatever the sandbox
// holds, and a setup with no mutation is one nothing runs.
func TestEveryMutationHasItsLiveSetup(t *testing.T) {
	declared := map[string]bool{}
	for _, op := range operations {
		if !op.mutation {
			continue
		}
		declared[op.method] = true
		if _, ok := liveMutations[op.method]; !ok {
			t.Errorf("%s is a mutation with no live setup, want one that makes what it writes against in the sandbox", op.method)
		}
	}
	for method := range liveMutations {
		if !declared[method] {
			t.Errorf("liveMutations carries %q, which the contract declares no mutation for", method)
		}
	}
}

// A fixed cell is found by its path, so a path no case declares would hold nothing
// while reading as held: every cell a mutation's setup or the permanent read subject
// fixes is one its own case declares, and the seed fixes cells of reads alone, the
// only cases that read it.
func TestEveryCellTheLaneFixesIsOneItsCaseDeclares(t *testing.T) {
	s := canonicalSubject(spec.Gitea)
	fixing := map[string]func(subject) map[string]any{}
	for method, live := range liveMutations {
		if live.fixes != nil {
			fixing[method] = live.fixes
		}
	}
	for method, fixes := range seedFixes {
		if op, ok := operationFor(method); ok && op.mutation {
			t.Errorf("seedFixes carries %s, a mutation, want reads alone: no mutation case addresses the seed", method)
		}
		fixing[method] = fixes
	}
	for method, fixes := range fixing {
		op, ok := operationFor(method)
		if !ok {
			t.Errorf("%s fixes cells, and the contract declares no case for it", method)
			continue
		}
		declared := map[string]bool{}
		for _, c := range op.expect(s.product, op.zero) {
			declared[c.path] = true
		}
		for path := range fixes(s) {
			if !declared[path] {
				t.Errorf("%s fixes %q, a cell its case declares none of, want one of %v", method, path, slices.Sorted(maps.Keys(declared)))
			}
		}
	}
}

// The marks are the only thing that lets a sweep remove an object, so a name
// without one at its start, a sandbox's own branch above all, is never removed.
func TestTheSweepRemovesOnlyWhatCarriesTheLanesMark(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{name: laneMark + "20261001-120000-create-pr-base", want: true},
		{name: laneCIMark + "20261001-120000-rerun", want: true},
		{name: laneMark + "20261001-120000 Example issue", want: true},
		{name: "main", want: false},
		{name: "forgeapi-ci-probe", want: false},
		{name: "forgeapi-a", want: false},
		{name: "Seed issue " + laneMark, want: false},
		{name: "x" + laneMark + "branch", want: false},
		{name: "", want: false},
	} {
		if got := laneOwned(tc.name); got != tc.want {
			t.Errorf("laneOwned(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A listing differing in any kind of object is a sandbox not left as found, and two
// listings holding the same objects in another order are the same sandbox.
func TestTheLaneComparesTheSandboxBeforeAndAfterByKind(t *testing.T) {
	before := laneState{
		pulls:    []laneItem{{number: 3, title: "Seed pull request"}},
		issues:   []laneItem{{number: 4, title: "Seed issue"}},
		branches: []string{"main", "seed"},
		tags:     []string{"v0.1.0"},
		releases: []laneRelease{{tag: "v0.1.0", id: 9}},
	}
	same := laneState{
		pulls:    []laneItem{{number: 3, title: "Seed pull request"}},
		issues:   []laneItem{{number: 4, title: "Seed issue"}},
		branches: []string{"seed", "main"},
		tags:     []string{"v0.1.0"},
		releases: []laneRelease{{tag: "v0.1.0", id: 9}},
	}
	if diff := laneDiff(before, same); len(diff) != 0 {
		t.Errorf("laneDiff of one sandbox listed in two orders = %q, want none", diff)
	}
	for name, after := range map[string]laneState{
		"open pull request": {pulls: append(slices.Clone(before.pulls), laneItem{number: 5, title: laneMark + "x Example pull request"}), issues: before.issues, branches: before.branches, tags: before.tags, releases: before.releases},
		"open issue":        {pulls: before.pulls, issues: nil, branches: before.branches, tags: before.tags, releases: before.releases},
		"branch":            {pulls: before.pulls, issues: before.issues, branches: append(slices.Clone(before.branches), laneMark+"x-base"), tags: before.tags, releases: before.releases},
		"tag":               {pulls: before.pulls, issues: before.issues, branches: before.branches, tags: append(slices.Clone(before.tags), laneMark+"x-release"), releases: before.releases},
		"release":           {pulls: before.pulls, issues: before.issues, branches: before.branches, tags: before.tags, releases: nil},
	} {
		if diff := laneDiff(before, after); len(diff) != 1 {
			t.Errorf("laneDiff with one %s changed = %q, want exactly one difference", name, diff)
		}
	}
}

// The plaintext and private-address statements are made for a throwaway instance
// on the same machine and for nothing else, so a live run pointed at any other
// instance keeps the library's refusals.
func TestTheLiveLaneStatesPlaintextOnlyForALoopbackInstance(t *testing.T) {
	for base, want := range map[string]bool{
		"http://127.0.0.1:3000":        true,
		"http://127.0.0.2:3000/":       true,
		"http://[::1]:3000":            true,
		"http://localhost:3000":        true,
		"https://127.0.0.1:3000":       false,
		"http://10.0.0.1:3000":         false,
		"http://forge.example":         false,
		"http://127.0.0.1.nip.io:3000": false,
		"https://gitlab.com":           false,
		"":                             false,
	} {
		if got := loopbackPlaintext(base); got != want {
			t.Errorf("loopbackPlaintext(%q) = %v, want %v", base, got, want)
		}
	}
}

// The run's names all start with a mark and carry the run, so a run's objects are
// both found by the next sweep and told apart from another run's.
func TestEveryNameTheRunGivesCarriesTheLanesMarkAndTheRun(t *testing.T) {
	for _, got := range []string{laneName("create-pr-base"), laneCIName("rerun"), laneTitlePrefix() + issueTitle} {
		if !laneOwned(got) {
			t.Errorf("%q carries no lane mark at its start, want one: the next run's sweep could not find it", got)
		}
		if !strings.HasPrefix(got, laneMark+laneRun()) && !strings.HasPrefix(got, laneCIMark+laneRun()) {
			t.Errorf("%q does not carry the run %q after its mark, want it: two runs' objects would share one name", got, laneRun())
		}
	}
}

// sandboxIssues is a Gitea-family sandbox holding issues and nothing else, enough
// for the lane to list it and to close what it finds.
type sandboxIssues struct {
	open map[int]string
	mu   sync.Mutex
}

func (sb *sandboxIssues) add(n int, title string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.open[n] = title
}

func (sb *sandboxIssues) isOpen(n int) bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	_, ok := sb.open[n]
	return ok
}

func (sb *sandboxIssues) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	root := "/api/v1/repos/" + selector
	w.Header().Set("Content-Type", "application/json")
	switch rest := strings.TrimPrefix(r.URL.Path, root); {
	case r.Method == http.MethodGet && rest == "/issues":
		rows := []map[string]any{}
		for n, title := range sb.open {
			rows = append(rows, map[string]any{"number": n, "title": title})
		}
		if err := json.NewEncoder(w).Encode(rows); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case r.Method == http.MethodGet:
		_, _ = w.Write([]byte("[]"))
	case r.Method == http.MethodPatch && strings.HasPrefix(rest, "/issues/"):
		n, err := strconv.Atoi(strings.TrimPrefix(rest, "/issues/"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		delete(sb.open, n)
		_, _ = w.Write([]byte(`{}`))
	default:
		http.NotFound(w, r)
	}
}

// A write the instance committed while its answer was lost registered no removal,
// yet what it made carries the lane's marks, so the run that made it removes it
// before it ends rather than leaving the sandbox dirty until the next run. The run
// still fails, naming what it left; and an object without the marks is never
// removed, so a sandbox that still differs after the sweep fails naming that too.
func TestTheLaneRemovesWhatALostAnswerLeftWithinTheRun(t *testing.T) {
	for _, test := range []struct {
		name    string
		title   string
		removed bool
		names   string
	}{
		{name: "carrying_the_lanes_mark", title: laneMark + laneRun() + " Example issue", removed: true, names: "was removed"},
		{name: "carrying_no_mark", title: "Someone's own issue", removed: false, names: "still differs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sb := &sandboxIssues{open: map[int]string{1: "Seed issue"}}
			srv := httptest.NewServer(sb)
			t.Cleanup(srv.Close)
			l := newLane(spec.Gitea, srv.URL, "lane-placeholder", selector)
			before, err := l.state(t.Context())
			if err != nil {
				t.Fatalf("Setup: listing the sandbox: %v", err)
			}
			sb.add(2, test.title)

			_, err = l.restore(t.Context(), before)
			if err == nil || !strings.Contains(err.Error(), "appeared") || !strings.Contains(err.Error(), test.names) {
				t.Errorf("restore with %q left open = %v, want a failure naming what appeared and saying it %s", test.title, err, test.names)
			}
			if gone := !sb.isOpen(2); gone != test.removed {
				t.Errorf("restore with %q left open: removed = %v, want %v", test.title, gone, test.removed)
			}
			if !sb.isOpen(1) {
				t.Errorf("restore closed the sandbox's own issue, want it left open")
			}
		})
	}
}
