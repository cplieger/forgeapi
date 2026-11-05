package gitea

import (
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v2"
)

// TestThePullListAnswersUnknownMergeStateUnderTheOption holds ListPRs to the option:
// a /pulls row carrying mergeable true answers unknown with the option false, and the
// recorded flag under the default.
func TestThePullListAnswersUnknownMergeStateUnderTheOption(t *testing.T) {
	row := strings.Replace(pullRow(1), `"state":"open",`, `"state":"open","mergeable":true,`, 1)
	if row == pullRow(1) {
		t.Fatal("Setup: the pull row carries no state to place the mergeable flag beside")
	}
	for _, test := range []struct {
		opts      []forgeapi.ListOption
		name      string
		mergeable forgeapi.Support
	}{
		{name: "option_false", opts: []forgeapi.ListOption{forgeapi.WithMergeState(false)}, mergeable: forgeapi.SupportUnknown},
		{name: "default", mergeable: forgeapi.SupportYes},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				"GET /api/v1/repos/example/example/pulls": "[" + row + "]",
				statusPath: statusOK,
			})
			page, err := h.client.ListPRs(t.Context(), testRef(), test.opts...)
			if err != nil {
				t.Fatalf("ListPRs = %v, want the page", err)
			}
			if len(page.Items) != 1 {
				t.Fatalf("ListPRs answered %d row(s), want 1", len(page.Items))
			}
			got := page.Items[0].Action
			if got.Mergeable != test.mergeable || got.MergeBlocked != forgeapi.MergeBlockUnknown {
				t.Errorf("ListPRs answered Mergeable %v, MergeBlocked %v, want %v, %v",
					got.Mergeable, got.MergeBlocked, test.mergeable, forgeapi.MergeBlockUnknown)
			}
		})
	}
}

// TestTheCrossRepositoryListAcceptsTheMergeStateOption holds ListMyPRs, whose issue
// search rows carry no mergeable key, to taking the option at either value with its
// rows unknown. The option-false row carries a mergeable flag anyway, which the option
// answers unknown whatever the body carries.
func TestTheCrossRepositoryListAcceptsTheMergeStateOption(t *testing.T) {
	flagged := strings.Replace(searchRow(false), `"state":"open",`, `"state":"open","mergeable":true,`, 1)
	if flagged == searchRow(false) {
		t.Fatal("Setup: the search row carries no state to place the mergeable flag beside")
	}
	for _, read := range []bool{true, false} {
		t.Run(strconv.FormatBool(read), func(t *testing.T) {
			row := searchRow(false)
			if !read {
				row = flagged
			}
			h := newHarness(t, map[string]string{
				"GET /api/v1/repos/issues/search": "[" + row + "]",
			})
			page, err := h.client.ListMyPRs(t.Context(), forgeapi.WithMergeState(read))
			if err != nil {
				t.Fatalf("ListMyPRs(WithMergeState(%t)) = %v, want the page", read, err)
			}
			if len(page.Items) != 1 {
				t.Fatalf("ListMyPRs(WithMergeState(%t)) answered %d row(s), want 1", read, len(page.Items))
			}
			got := page.Items[0].Action
			if got.Mergeable != forgeapi.SupportUnknown || got.MergeBlocked != forgeapi.MergeBlockUnknown {
				t.Errorf("ListMyPRs(WithMergeState(%t)) answered Mergeable %v, MergeBlocked %v, want unknown, unknown",
					read, got.Mergeable, got.MergeBlocked)
			}
		})
	}
}
