package conformance

import (
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// TestOnlyGitHubsSearchListsAreIndexed holds Page.Indexed to the route: GitHub serves
// its two cross-repository lists from the search index, and every other pull
// request and issue list on every product reads the forge's database.
func TestOnlyGitHubsSearchListsAreIndexed(t *testing.T) {
	for _, method := range []string{"PullRequests.ListMyPRs", "Issues.ListMyIssues", "PullRequests.ListPRs", "Issues.ListIssues"} {
		for _, p := range spec.Products {
			e := requireEntry(t, p, method)
			t.Run(caseName(e), func(t *testing.T) {
				want := p == spec.GitHub && (method == "PullRequests.ListMyPRs" || method == "Issues.ListMyIssues")
				got, requests, err := listFixture(t, e)
				if err != nil {
					t.Fatalf("%s on %s = error %v, want the page; the requests were %s", method, p, err, describeRequests(requests))
				}
				var indexed bool
				switch page := got.(type) {
				case forgeapi.Page[forgeapi.PullRequest]:
					indexed = page.Indexed
				case forgeapi.Page[forgeapi.Issue]:
					indexed = page.Indexed
				default:
					t.Fatalf("%s on %s answered %T, want a page of pull requests or issues", method, p, got)
				}
				if indexed != want {
					t.Errorf("%s on %s = Indexed %t, want %t", method, p, indexed, want)
				}
			})
		}
	}
}
