package gitea

import (
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// TestAPullRequestNamesTheRepositoryItsHeadLivesIn holds ADR-0104 on this family: the
// swagger's PullRequest.head is a PRBranchInfo whose repo names the repository the
// head branch lives in, the addressed one for a pull request opened inside it, a
// fork's for one opened from a fork, and null for a deleted fork (measured on
// codeberg.org, where such a row carries repo_id -1). Sameness is the answer's own:
// a list that followed a rename keeps Repo the addressed name while head and base
// both name the repository's current one, and that pull request is still opened
// inside its repository.
func TestAPullRequestNamesTheRepositoryItsHeadLivesIn(t *testing.T) {
	c := newHarness(t, nil).client
	repo := testRef()
	renamed := &wireRepo{FullName: "example/renamed"}
	for _, test := range []struct {
		name       string
		head, base *wireRepo
		want       forgeapi.RepoRef
	}{
		{name: "same repository", head: &wireRepo{FullName: testSelector}, want: repo},
		{name: "same repository, other case", head: &wireRepo{FullName: strings.ToUpper(testSelector)}, want: repo},
		{name: "same repository, renamed since it was addressed", head: renamed, base: renamed, want: repo},
		{name: "same repository, renamed, other case", head: &wireRepo{FullName: "Example/Renamed"}, base: renamed, want: repo},
		{name: "fork", head: &wireRepo{FullName: "contributor/fork"}, want: repoRef("contributor/fork")},
		{name: "fork of a renamed repository", head: &wireRepo{FullName: "contributor/fork"}, base: renamed, want: repoRef("contributor/fork")},
		{name: "deleted fork", base: renamed, want: forgeapi.RepoRef{}},
	} {
		p := &wirePull{Head: wireBranch{Ref: "feature", Repo: test.head}, Base: wireBranch{Ref: "main", Repo: test.base}}
		if got := c.normalizePull(p, repo).SourceRepo; got != test.want {
			t.Errorf("%s: SourceRepo = %+v, want %+v", test.name, got, test.want)
		}
	}
}
