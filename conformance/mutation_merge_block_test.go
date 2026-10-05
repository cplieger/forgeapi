package conformance

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// gitlabMergeStatuses are the merge states the GitLab sandbox answered for one merge
// request over its life, each with the older merge_status the same answer carried
// and the block reason a read on the documents answers for a merge request in that
// state: the creation's, the close after the check settled, the reopen, the close
// after that reopen, and the settled read between them.
//
// A read on the documents maps the state as the enumeration discipline maps every
// member: a settled state names its reason, and a state the asynchronous check has
// not left names none.
var gitlabMergeStatuses = []struct {
	detailed string
	older    string
	read     forgeapi.MergeBlockReason
}{
	{detailed: "preparing", older: "checking", read: forgeapi.MergeBlockUnknown},
	{detailed: "not_open", older: "can_be_merged", read: forgeapi.MergeBlockBlocked},
	{detailed: "unchecked", older: "unchecked", read: forgeapi.MergeBlockUnknown},
	{detailed: "checking", older: "checking", read: forgeapi.MergeBlockUnknown},
	{detailed: "mergeable", older: "can_be_merged", read: forgeapi.MergeBlockNone},
}

// gitlabPullRequestMutations are GitLab's mutations whose own answer is a pull
// request.
var gitlabPullRequestMutations = []string{"PullRequests.CreatePR", "PullRequests.ClosePR", "PullRequests.ReopenPR"}

// gitlabPullRequestReads are GitLab's reads on the documents that answer a pull
// request carrying its block reason.
var gitlabPullRequestReads = []string{"PullRequests.ReadPR", "PullRequests.ListPRs"}

// gitlabCrossRepositoryScopes are the routes GitLab's cross-repository list reads,
// the merge requests the credential authored, the default, and every open one under
// a named owner, each with the request the table names for it.
var gitlabCrossRepositoryScopes = []struct {
	request func(spec.Product, string) arm
	name    string
	opts    []forgeapi.ListOption
}{
	{name: "viewer", request: viewerArm},
	{name: "owner", request: ownerArm, opts: []forgeapi.ListOption{forgeapi.WithOwner(owner)}},
}

// withMergeStatus is one fixture body with every merge request it holds carrying
// the state given, in the spelling each transport answers: REST's detailed status
// beside the older field the same answer carried, and the documents' enum member.
// It answers how many merge requests it altered.
func withMergeStatus(t *testing.T, body []byte, detailed, older string) ([]byte, int) {
	t.Helper()
	return withAltered(t, body, func(n map[string]any) int {
		altered := 0
		if _, ok := n["detailed_merge_status"]; ok {
			n["detailed_merge_status"] = detailed
			if _, ok := n["merge_status"]; ok {
				n["merge_status"] = older
			}
			altered++
		}
		if _, ok := n["detailedMergeStatus"]; ok {
			n["detailedMergeStatus"] = strings.ToUpper(detailed)
			altered++
		}
		return altered
	})
}

// withAltered is one fixture body with alter applied to every JSON object it holds,
// at any depth. It answers how many objects alter reported changing, and the body
// unchanged where it changed none.
func withAltered(t *testing.T, body []byte, alter func(map[string]any) int) ([]byte, int) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var answer any
	if err := dec.Decode(&answer); err != nil {
		t.Fatalf("Setup: a fixture body is not JSON: %v", err)
	}
	altered := 0
	var walk func(node any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			altered += alter(n)
			for _, v := range n {
				walk(v)
			}
		case []any:
			for _, v := range n {
				walk(v)
			}
		}
	}
	walk(answer)
	if altered == 0 {
		return body, 0
	}
	out, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("Setup: re-encoding a fixture body: %v", err)
	}
	return out, altered
}

// gitlabPullRequestUnder is the pull request one GitLab operation answers when every
// merge request its fixture serves is in the state given.
func gitlabPullRequestUnder(t *testing.T, method, detailed, older string) forgeapi.PullRequest {
	t.Helper()
	return pullRequestUnder(t, spec.GitLab, method, "state "+detailed, func(body []byte) ([]byte, int) {
		return withMergeStatus(t, body, detailed, older)
	})
}

// pullRequestUnder is the pull request one operation answers on one product when
// every body its fixture serves is altered as given, driven as the offline case
// drives it: the case's own fixture, its prelude run first, and the canonical
// subject. A list answers its first row. what names the alteration in a failure.
func pullRequestUnder(t *testing.T, p spec.Product, method, what string, alter func([]byte) ([]byte, int)) forgeapi.PullRequest {
	t.Helper()
	e := requireEntry(t, p, method)
	op, ok := operationFor(method)
	if !ok {
		t.Fatalf("Setup: the contract declares no case for %s", method)
	}
	f := alteredFixture(t, p, method, alter)
	srv, rec := newFixtureServer(t, f)
	client := pagedClient(t, p, srv)
	s := canonicalSubject(p)
	if prelude := preludeMethod(e, op); prelude != "" {
		runPrelude(t, e, prelude, client, s)
	}
	got, err := guard(func() (any, error) { return op.invoke(t.Context(), client, s) })
	if err != nil {
		t.Fatalf("%s on %s over a pull request in %s = error %v, want the pull request; the requests were %v",
			method, p, what, err, rec.requests())
	}
	if misses := rec.misses(); len(misses) > 0 {
		t.Fatalf("%s on %s reached route(s) its fixture does not answer: %v", method, p, misses)
	}
	switch g := got.(type) {
	case forgeapi.PullRequest:
		return g
	case forgeapi.Page[forgeapi.PullRequest]:
		if len(g.Items) == 0 {
			t.Fatalf("%s on %s answered no row, want the fixture's pull request", method, p)
		}
		return g.Items[0]
	}
	t.Fatalf("%s on %s answered %T, want a pull request or a page of them", method, p, got)
	return forgeapi.PullRequest{}
}

// alteredFixture is one operation's own fixture on one product with every body it
// serves altered as given. A fixture the alteration changes nothing in fails the
// case, since what it would drive is then the unaltered answer.
func alteredFixture(t *testing.T, p spec.Product, method string, alter func([]byte) ([]byte, int)) fixture {
	t.Helper()
	f, err := loadFixture(p, method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	f.Routes = slices.Clone(f.Routes)
	altered := 0
	for i := range f.Routes {
		body, n := alter(f.Routes[i].Body)
		f.Routes[i].Body = body
		altered += n
	}
	if altered == 0 {
		t.Fatalf("Setup: %s serves no pull request carrying the fields the alteration sets", f.path)
	}
	return f
}

// gitlabListedPullRequestUnder is the first row GitLab's cross-repository list answers
// under the options given when every merge request its fixture serves is in the state
// given, with the requests the list sent.
func gitlabListedPullRequestUnder(t *testing.T, e spec.Entry, detailed, older string, opts ...forgeapi.ListOption) (forgeapi.PullRequest, []sent) {
	t.Helper()
	f := alteredFixture(t, e.Product, e.Method, func(body []byte) ([]byte, int) {
		return withMergeStatus(t, body, detailed, older)
	})
	got, requests, err := listAgainst(t, e, f, opts...)
	if err != nil {
		t.Fatalf("%s on %s over a merge request in state %s = error %v, want the page; the requests were %s",
			e.Method, e.Product, detailed, err, describeRequests(requests))
	}
	page, ok := got.(forgeapi.Page[forgeapi.PullRequest])
	if !ok {
		t.Fatalf("%s on %s answered %T, want a page of pull requests", e.Method, e.Product, got)
	}
	if len(page.Items) == 0 {
		t.Fatalf("%s on %s answered no row, want the fixture's merge request", e.Method, e.Product)
	}
	return page.Items[0], requests
}

// A GitLab mutation's own answer carries the merge state at the instant of the
// transition, which the transition itself resets and which no caller can rely on, so
// the block reason it answers is unknown whatever state the answer names, a settled
// one included: the published cell then holds on every record, as the creation's,
// the close's and the reopen's rows state it.
func TestAGitLabMutationsOwnAnswerNamesNoBlockReasonWhateverItsMergeState(t *testing.T) {
	for _, method := range gitlabPullRequestMutations {
		for _, st := range gitlabMergeStatuses {
			t.Run(strings.TrimPrefix(method, "PullRequests.")+"_"+st.detailed, func(t *testing.T) {
				pr := gitlabPullRequestUnder(t, method, st.detailed, st.older)
				if pr.Action.MergeBlocked != forgeapi.MergeBlockUnknown {
					t.Errorf("%s on %s answering detailed_merge_status %q, merge_status %q: Action.MergeBlocked = %v, want %v",
						method, spec.GitLab, st.detailed, st.older, pr.Action.MergeBlocked, forgeapi.MergeBlockUnknown)
				}
			})
		}
	}
}

// A GitLab read on the documents keeps mapping the merge state onto the block
// reason: a merge request that is not open is blocked, one whose asynchronous check
// has not settled names no reason, and a mergeable one names none.
func TestAGitLabDocumentReadMapsTheMergeStateItReadsOntoTheBlockReason(t *testing.T) {
	for _, method := range gitlabPullRequestReads {
		for _, st := range gitlabMergeStatuses {
			t.Run(strings.TrimPrefix(method, "PullRequests.")+"_"+st.detailed, func(t *testing.T) {
				pr := gitlabPullRequestUnder(t, method, st.detailed, st.older)
				if pr.Action.MergeBlocked != st.read {
					t.Errorf("%s on %s reading a merge request in state %q: Action.MergeBlocked = %v, want %v",
						method, spec.GitLab, st.detailed, pr.Action.MergeBlocked, st.read)
				}
			})
		}
	}
}

// GitLab's cross-repository list reads REST on every connection, on the viewer's
// route and on an owner's alike, and that route documents a merge status it may not
// have refreshed, which no answer can tell from a fresh one: each row's block reason
// is unknown whatever state the row names, a settled one included, as the degraded
// list's is. The request is held to the scope's own route, so each scope is the one
// read.
func TestAGitLabCrossRepositoryListNamesNoBlockReasonWhateverItsMergeState(t *testing.T) {
	const method = "PullRequests.ListMyPRs"
	for _, scope := range gitlabCrossRepositoryScopes {
		for _, st := range gitlabMergeStatuses {
			t.Run(scope.name+"_"+st.detailed, func(t *testing.T) {
				e := requireEntry(t, spec.GitLab, method)
				pr, requests := gitlabListedPullRequestUnder(t, e, st.detailed, st.older, scope.opts...)
				checkArm(t, e, scope.request(spec.GitLab, method), requests)
				if pr.Action.MergeBlocked != forgeapi.MergeBlockUnknown {
					t.Errorf("%s on %s under the %s scope, a row answering detailed_merge_status %q, merge_status %q: Action.MergeBlocked = %v, want %v",
						method, spec.GitLab, scope.name, st.detailed, st.older, pr.Action.MergeBlocked, forgeapi.MergeBlockUnknown)
				}
			})
		}
	}
}
