package conformance

import (
	"testing"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/internal/spec"
)

// The table's fields for what the pull request lists answer under
// WithMergeState(false), on a cell whose default answer carries merge state.
const (
	rowMergeableUnderOption    = "Items[].Action.Mergeable, under WithMergeState(false)"
	rowMergeBlockedUnderOption = "Items[].Action.MergeBlocked, under WithMergeState(false)"
)

// mergeStateLists are the lists that take WithMergeState.
var mergeStateLists = []string{"PullRequests.ListPRs", "PullRequests.ListMyPRs"}

// With merge state left unread, every pull request list answers each row's merge
// state unknown over the recorded body, which still carries it. A cell whose default
// answer reads that field states the option's answer in its own conditional row,
// and a cell that already cannot supply it carries no such row.
func TestThePullRequestListsAnswerUnknownMergeStateWithTheOptionOff(t *testing.T) {
	fields := []struct {
		field, underOption string
	}{
		{field: "Items[].Action.Mergeable", underOption: rowMergeableUnderOption},
		{field: "Items[].Action.MergeBlocked", underOption: rowMergeBlockedUnderOption},
	}
	for _, method := range mergeStateLists {
		for _, p := range spec.Products {
			e := requireEntry(t, p, method)
			t.Run(caseName(e), func(t *testing.T) {
				got, requests, err := listFixture(t, e, forgeapi.WithMergeState(false))
				if err != nil {
					t.Fatalf("%s on %s with WithMergeState(false) = error %v, want the page; the requests were %s",
						method, p, err, describeRequests(requests))
				}
				page, ok := got.(forgeapi.Page[forgeapi.PullRequest])
				if !ok || len(page.Items) == 0 {
					t.Fatalf("%s on %s with WithMergeState(false) answered %T with no row, want the fixture's rows", method, p, got)
				}
				for i, row := range page.Items {
					if row.Action.Mergeable != forgeapi.SupportUnknown || row.Action.MergeBlocked != forgeapi.MergeBlockUnknown {
						t.Errorf("%s on %s with WithMergeState(false): row %d answered Mergeable %v, MergeBlocked %v, want %v, %v",
							method, p, i, row.Action.Mergeable, row.Action.MergeBlocked, forgeapi.SupportUnknown, forgeapi.MergeBlockUnknown)
					}
				}
				for _, f := range fields {
					_, already := departureFor(&e, f.field)
					d, stated := departureFor(&e, f.underOption)
					switch {
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
