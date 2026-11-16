package github

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
)

// indexedList is one pull request or issue list reduced to the flag under test.
type indexedList struct {
	call  func(context.Context, *harness) (bool, *forgeapi.Partial, error)
	name  string
	route func(*testing.T) string
	want  bool
}

var indexedLists = []indexedList{
	{
		name: "ListMyPRs", want: true, route: func(t *testing.T) string { return searchEnvelope(t, "graphql_search_row") },
		call: func(ctx context.Context, h *harness) (bool, *forgeapi.Partial, error) {
			p, err := h.client.ListMyPRs(ctx)
			return p.Indexed, p.Partial, err
		},
	},
	{
		name: "ListMyIssues", want: true, route: func(t *testing.T) string { return searchEnvelope(t, "graphql_issue_search_row") },
		call: func(ctx context.Context, h *harness) (bool, *forgeapi.Partial, error) {
			p, err := h.client.ListMyIssues(ctx)
			return p.Indexed, p.Partial, err
		},
	},
	{
		name: "ListPRs", want: false, route: func(t *testing.T) string { return prListEnvelope(t, false, "") },
		call: func(ctx context.Context, h *harness) (bool, *forgeapi.Partial, error) {
			p, err := h.client.ListPRs(ctx, testRef())
			return p.Indexed, p.Partial, err
		},
	},
}

// TestOnlyTheSearchListsAnswerIndexedPages holds Page.Indexed to the route: the two
// cross-repository lists run the search connection, the repository list does not.
func TestOnlyTheSearchListsAnswerIndexedPages(t *testing.T) {
	for _, test := range indexedLists {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: test.route(t)})
			indexed, _, err := test.call(t.Context(), h)
			if err != nil {
				t.Fatalf("%s = %v, want the page", test.name, err)
			}
			if indexed != test.want {
				t.Errorf("%s = Indexed %t, want %t", test.name, indexed, test.want)
			}
		})
	}
}

// TestASearchListsEarlyPagesAreIndexedToo holds the flag on the pages a search list
// answers without reading a row: one the page budget leaves unread and one the
// governor defers at the mutation reserve, since the flag states the route.
func TestASearchListsEarlyPagesAreIndexedToo(t *testing.T) {
	for _, test := range indexedLists {
		t.Run(test.name+"_unread", func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: test.route(t)}, forgeapi.WithListPages(0))
			indexed, partial, err := test.call(t.Context(), h)
			if err != nil || partial == nil || partial.Reason != forgeapi.PartialPaginationCap {
				t.Fatalf("Setup: %s under a zero page budget = partial %+v, %v, want the page-cap marker", test.name, partial, err)
			}
			if indexed != test.want {
				t.Errorf("%s under a zero page budget = Indexed %t, want %t", test.name, indexed, test.want)
			}
		})
		t.Run(test.name+"_deferred", func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: test.route(t)}, forgeapi.WithMutationReserve(10))
			h.instance.answerHeader("X-Ratelimit-Remaining", "10")
			h.instance.answerHeader("X-Ratelimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
			h.instance.answerHeader("X-Ratelimit-Resource", "graphql")
			if _, _, err := test.call(t.Context(), h); err != nil {
				t.Fatalf("Setup: the first %s = %v, want the page that reads the budget", test.name, err)
			}
			indexed, partial, err := test.call(t.Context(), h)
			if err != nil || partial == nil || partial.Reason != forgeapi.PartialRateLimited {
				t.Fatalf("Setup: %s at the reserve = partial %+v, %v, want the deferral marker", test.name, partial, err)
			}
			if indexed != test.want {
				t.Errorf("%s at the reserve = Indexed %t, want %t", test.name, indexed, test.want)
			}
		})
	}
}
