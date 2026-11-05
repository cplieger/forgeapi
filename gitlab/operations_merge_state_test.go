package gitlab

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v2"
)

// sentMergeState is the mergeState variable one posted document carried, and false
// for found when the document sent none.
func sentMergeState(t *testing.T, body string) (value, found bool) {
	t.Helper()
	var posted struct {
		Variables map[string]json.RawMessage `json:"variables"`
	}
	if err := json.Unmarshal([]byte(body), &posted); err != nil {
		t.Fatalf("Setup: decoding the posted document %s: %v", body, err)
	}
	raw, found := posted.Variables["mergeState"]
	if !found {
		return false, false
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("Setup: the posted mergeState variable %s is not a boolean: %v", raw, err)
	}
	return value, true
}

// TestTheDocumentListGatesItsMergeStateOnTheOption holds the document arm of ListPRs
// to the option: false sends the variable false and answers unknown rows even from a
// body that still carries mergeable true and MERGEABLE, and the default sends it true
// and answers the recorded merge state.
func TestTheDocumentListGatesItsMergeStateOnTheOption(t *testing.T) {
	for _, test := range []struct {
		opts      []forgeapi.ListOption
		name      string
		mergeable forgeapi.Support
		blocked   forgeapi.MergeBlockReason
		sent      bool
	}{
		{
			name: "option_false", opts: []forgeapi.ListOption{forgeapi.WithMergeState(false)},
			mergeable: forgeapi.SupportUnknown, blocked: forgeapi.MergeBlockUnknown,
		},
		{name: "default", sent: true, mergeable: forgeapi.SupportYes, blocked: forgeapi.MergeBlockNone},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: docList})
			page, err := h.client.ListPRs(t.Context(), testRef(), test.opts...)
			if err != nil {
				t.Fatalf("ListPRs = %v, want the page", err)
			}
			if got, found := sentMergeState(t, h.instance.body(documentRoute)); !found || got != test.sent {
				t.Errorf("ListPRs sent mergeState %t (found %t), want %t", got, found, test.sent)
			}
			if len(page.Items) != 1 {
				t.Fatalf("ListPRs answered %d row(s), want the 1 recorded", len(page.Items))
			}
			got := page.Items[0].Action
			if got.Mergeable != test.mergeable || got.MergeBlocked != test.blocked {
				t.Errorf("ListPRs answered Mergeable %v, MergeBlocked %v, want %v, %v",
					got.Mergeable, got.MergeBlocked, test.mergeable, test.blocked)
			}
		})
	}
}

// TestOnlyTheListDocumentGatesTheMergeFields holds the request the option changes:
// the list document gates both merge fields on its required variable, while the
// single read and the schema probe select them unconditionally.
func TestOnlyTheListDocumentGatesTheMergeFields(t *testing.T) {
	for _, want := range []string{"$mergeState: Boolean!", "mergeable @include(if: $mergeState)", "detailedMergeStatus @include(if: $mergeState)"} {
		if !strings.Contains(prList.text, want) {
			t.Errorf("the PRList document does not contain %q", want)
		}
	}
	for _, doc := range []document{prRead, docAcceptance} {
		t.Run(doc.name, func(t *testing.T) {
			if strings.Contains(doc.text, "mergeState") || strings.Contains(doc.text, "@include") {
				t.Errorf("the %s document names the merge state gate, want both fields selected unconditionally", doc.name)
			}
			for _, want := range []string{"\n      mergeable\n", "\n      detailedMergeStatus\n"} {
				if !strings.Contains(doc.text, want) {
					t.Errorf("the %s document does not select %q", doc.name, strings.TrimSpace(want))
				}
			}
		})
	}
}

// TestTheSingleReadSendsNoMergeStateVariable holds the single read, which fills
// GitLab merge state for a caller that listed without it, to posting no mergeState
// variable.
func TestTheSingleReadSendsNoMergeStateVariable(t *testing.T) {
	h := newHarness(t, map[string]string{documentRoute: docRead})
	if _, err := h.client.ReadPR(t.Context(), testRef(), testPR()); err != nil {
		t.Fatalf("ReadPR = %v, want the merge request", err)
	}
	if got, found := sentMergeState(t, h.instance.body(documentRoute)); found {
		t.Errorf("ReadPR sent mergeState %t, want no such variable", got)
	}
}

// TestTheRESTListsAcceptTheMergeStateOption holds the two lists whose REST rows
// already answer unknown merge state, the cross-repository list on every connection
// and the per-repository list on a connection whose documents were refused: they take
// the option at either value and their rows stay unknown.
func TestTheRESTListsAcceptTheMergeStateOption(t *testing.T) {
	for _, read := range []bool{true, false} {
		opt := forgeapi.WithMergeState(read)
		t.Run("ListMyPRs_"+strconv.FormatBool(read), func(t *testing.T) {
			h := newHarness(t, map[string]string{myMergeRoute: "[" + restMergeRequestBody + "]"})
			page, err := h.client.ListMyPRs(t.Context(), opt)
			assertUnknownMergeState(t, "ListMyPRs", read, page, err)
		})
		t.Run("degraded_ListPRs_"+strconv.FormatBool(read), func(t *testing.T) {
			h := newHarness(t, map[string]string{
				metadataRoute: metadataBody,
				documentRoute: docTooComplex,
				listRoute:     "[" + restMergeRequestBody + "]",
			})
			h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
			if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
				t.Fatalf("Setup: ConnectionCaps = %v, want the capabilities", err)
			}
			if !h.client.isDegraded() {
				t.Fatal("Setup: the refused schema question left the connection undegraded")
			}
			page, err := h.client.ListPRs(t.Context(), testRef(), opt)
			assertUnknownMergeState(t, "ListPRs", read, page, err)
		})
	}
}

func assertUnknownMergeState(t *testing.T, op string, read bool, page forgeapi.Page[forgeapi.PullRequest], err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s(WithMergeState(%t)) = %v, want the page", op, read, err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("%s(WithMergeState(%t)) answered %d row(s), want 1", op, read, len(page.Items))
	}
	got := page.Items[0].Action
	if got.Mergeable != forgeapi.SupportUnknown || got.MergeBlocked != forgeapi.MergeBlockUnknown {
		t.Errorf("%s(WithMergeState(%t)) answered Mergeable %v, MergeBlocked %v, want unknown, unknown",
			op, read, got.Mergeable, got.MergeBlocked)
	}
}
