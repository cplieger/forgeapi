package conformance

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// The table's fields for what the pull request lists answer under
// WithReviews(false), on a cell whose default answer carries the review counts.
const (
	rowApprovalsUnderOption = "Items[].Action.Approvals, under WithReviews(false)"
	rowChangesUnderOption   = "Items[].Action.ChangesRequested, under WithReviews(false)"
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

// With reviews left unread, every pull request list answers both counts unknown
// and sends nothing that reads a review: no review route on a REST product, and the
// documents' reviews variable false. A cell whose default answer reads the counts
// states the option's answer in its own conditional row, and a cell that already
// cannot supply them carries none.
func TestThePullRequestListsReadNoReviewWithTheOptionOff(t *testing.T) {
	fields := []struct {
		field, underOption string
	}{
		{field: "Items[].Action.Approvals", underOption: rowApprovalsUnderOption},
		{field: "Items[].Action.ChangesRequested", underOption: rowChangesUnderOption},
	}
	for _, method := range reviewsLists {
		for _, p := range spec.Products {
			e := requireEntry(t, p, method)
			t.Run(caseName(e), func(t *testing.T) {
				got, requests, err := listFixture(t, e, forgeapi.WithReviews(false))
				if err != nil {
					t.Fatalf("%s on %s with WithReviews(false) = error %v, want the page; the requests were %s",
						method, p, err, describeRequests(requests))
				}
				page, ok := got.(forgeapi.Page[forgeapi.PullRequest])
				if !ok || len(page.Items) == 0 {
					t.Fatalf("%s on %s with WithReviews(false) answered %T with no row, want the fixture's rows", method, p, got)
				}
				for i, row := range page.Items {
					if row.Action.Approvals != forgeapi.CountUnknown || row.Action.ChangesRequested != forgeapi.CountUnknown {
						t.Errorf("%s on %s with WithReviews(false): row %d answered Approvals %d, ChangesRequested %d, want both %d",
							method, p, i, row.Action.Approvals, row.Action.ChangesRequested, forgeapi.CountUnknown)
					}
				}
				for _, r := range requests {
					if strings.HasSuffix(r.path, "/reviews") {
						t.Errorf("%s on %s with WithReviews(false) sent %s, want no review read", method, p, r)
					}
					if reviews, ok := documentVariable(r.body, "reviews"); ok && reviews != false {
						t.Errorf("%s on %s with WithReviews(false) sent the document variable reviews %v, want false", method, p, reviews)
					}
				}
				for _, f := range fields {
					d, stated := departureFor(&e, f.underOption)
					switch already := cannotSupply(&e, f.field); {
					case already && stated:
						t.Errorf("%s on %s carries %q, want none: its default answer already cannot supply %s", method, p, f.underOption, f.field)
					case !already && !stated:
						t.Errorf("%s on %s carries no %q row, want one: its default answer reads %s", method, p, f.underOption, f.field)
					case stated && d.Kind != spec.CannotSupply:
						t.Errorf("%s on %s states %q as %s, want %s", method, p, f.underOption, d.Kind, spec.CannotSupply)
					}
				}
			})
		}
	}
}

// cannotSupply reports that a cell's default answer cannot supply a field, where a
// differs-in-shape row still answers it.
func cannotSupply(e *spec.Entry, field string) bool {
	d, ok := departureFor(e, field)
	return ok && d.Kind == spec.CannotSupply
}

// documentVariable is one variable a GraphQL request body sends, and false where
// the body is not a document or does not send it.
func documentVariable(body []byte, name string) (any, bool) {
	var doc struct {
		Variables map[string]any `json:"variables"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return nil, false
	}
	v, ok := doc.Variables[name]
	return v, ok
}
