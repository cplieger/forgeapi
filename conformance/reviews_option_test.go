package conformance

import (
	"slices"
	"strconv"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// reviewsLists are the lists that take WithReviews.
var reviewsLists = []string{"PullRequests.ListPRs", "PullRequests.ListMyPRs"}

// TestAReviewsOptionOnAListWithNoReviewCountsIsRefused holds WithReviews to the two
// pull request lists: on every other list it is a misuse at either value, refused
// before any request rather than ignored.
func TestAReviewsOptionOnAListWithNoReviewCountsIsRefused(t *testing.T) {
	for _, method := range slices.Sorted(func(yield func(string) bool) {
		for m := range lists {
			if !slices.Contains(reviewsLists, m) && !yield(m) {
				return
			}
		}
	}) {
		for _, p := range spec.Products {
			for _, read := range []bool{true, false} {
				t.Run(string(p)+"_"+method+"_"+strconv.FormatBool(read), func(t *testing.T) {
					e := requireEntry(t, p, method)
					if pending(e) {
						t.Skip(pendingReason(e))
					}
					_, requests, err := listAgainst(t, e, fixture{Routes: setupRoutes(t, e)}, forgeapi.WithReviews(read))
					checkLocalRefusal(t, e, forgeapi.CodeListReviewsInvalid, err, requests)
				})
			}
		}
	}
}

// TestThePullRequestListsTakeTheReviewsOption holds the other half: both pull
// request lists answer their recorded page under WithReviews at either value.
func TestThePullRequestListsTakeTheReviewsOption(t *testing.T) {
	for _, method := range reviewsLists {
		for _, p := range spec.Products {
			for _, read := range []bool{true, false} {
				e := requireEntry(t, p, method)
				t.Run(caseName(e)+"_"+strconv.FormatBool(read), func(t *testing.T) {
					got, requests, err := listFixture(t, e, forgeapi.WithReviews(read))
					if err != nil {
						t.Fatalf("%s on %s with WithReviews(%t) = error %v, want the page; the requests were %s",
							method, p, read, err, describeRequests(requests))
					}
					if page, ok := got.(forgeapi.Page[forgeapi.PullRequest]); !ok || len(page.Items) == 0 {
						t.Errorf("%s on %s with WithReviews(%t) answered %T with no row, want the fixture's rows", method, p, read, got)
					}
				})
			}
		}
	}
}
