package github

import (
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// TestAPullRequestNamesTheRepositoryItsHeadLivesIn holds ADR-0104 on this product's
// two arms: the documents' nullable PullRequest.headRepository and the REST record's
// head.repo, which name the addressed repository for a pull request opened inside it,
// the fork for one opened from a fork, and null for a deleted fork.
func TestAPullRequestNamesTheRepositoryItsHeadLivesIn(t *testing.T) {
	c := newHarness(t, nil).client
	repo := testRef()
	for _, test := range []struct {
		name string
		head string
		want forgeapi.RepoRef
	}{
		{"same repository", testSelector, repo},
		{"same repository, other case", strings.ToUpper(testSelector), repo},
		{"fork", "contributor/fork", repoRef("contributor/fork")},
		{"deleted fork", "", forgeapi.RepoRef{}},
	} {
		var doc *docRepoRef
		var rest *restRepoName
		if test.head != "" {
			doc, rest = &docRepoRef{NameWithOwner: test.head}, &restRepoName{FullName: test.head}
		}
		if got := c.normalizeDocPull(&docPullRequest{HeadRepository: doc}, repo).SourceRepo; got != test.want {
			t.Errorf("document, %s: SourceRepo = %+v, want %+v", test.name, got, test.want)
		}
		if got := c.normalizeRESTPull(&restPull{Head: &restRef{Repo: rest}}, repo).SourceRepo; got != test.want {
			t.Errorf("REST, %s: SourceRepo = %+v, want %+v", test.name, got, test.want)
		}
	}
	if got := c.normalizeDocPull(&docPullRequest{HeadRepository: &docRepoRef{NameWithOwner: "contributor/fork"}},
		forgeapi.RepoRef{Family: forgeapi.FamilyGitHub}).SourceRepo; got != repoRef("contributor/fork") {
		t.Errorf("a cross-repository row with no repository of its own: SourceRepo = %+v, want the fork", got)
	}
}
