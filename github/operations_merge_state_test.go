package github

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
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

// TestThePullRequestListsGateTheirMergeStateOnTheOption holds both pull-request lists
// to the option: false sends the variable false and answers unknown rows even from a
// body that still carries MERGEABLE and CLEAN, and the default sends it true and
// answers the recorded merge state.
func TestThePullRequestListsGateTheirMergeStateOnTheOption(t *testing.T) {
	lists := []struct {
		list  func(context.Context, *harness, ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error)
		route func(*testing.T) string
		name  string
	}{
		{
			name:  "ListPRs",
			route: func(t *testing.T) string { return prListEnvelope(t, false, "") },
			list: func(ctx context.Context, h *harness, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
				return h.client.ListPRs(ctx, testRef(), opts...)
			},
		},
		{
			name:  "ListMyPRs",
			route: func(t *testing.T) string { return searchEnvelope(t, "graphql_search_row") },
			list: func(ctx context.Context, h *harness, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
				return h.client.ListMyPRs(ctx, opts...)
			},
		},
	}
	for _, list := range lists {
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
			t.Run(list.name+"_"+test.name, func(t *testing.T) {
				h := newHarness(t, map[string]string{documentRoute: list.route(t)})
				page, err := list.list(t.Context(), h, test.opts...)
				if err != nil {
					t.Fatalf("%s = %v, want the page", list.name, err)
				}
				if got, found := sentMergeState(t, h.instance.body(documentRoute)); !found || got != test.sent {
					t.Errorf("%s sent mergeState %t (found %t), want %t", list.name, got, found, test.sent)
				}
				if len(page.Items) != 1 {
					t.Fatalf("%s answered %d row(s), want the 1 recorded", list.name, len(page.Items))
				}
				got := page.Items[0].Action
				if got.Mergeable != test.mergeable || got.MergeBlocked != test.blocked {
					t.Errorf("%s answered Mergeable %v, MergeBlocked %v, want %v, %v",
						list.name, got.Mergeable, got.MergeBlocked, test.mergeable, test.blocked)
				}
			})
		}
	}
}

// TestThePullRequestDocumentsGateBothMergeFieldsOnOneVariable holds the request the
// option changes: both merge fields sit behind the variable in every pull-request
// document, the lists require it, and the single read defaults it to true.
func TestThePullRequestDocumentsGateBothMergeFieldsOnOneVariable(t *testing.T) {
	for _, test := range []struct {
		doc      document
		declares string
	}{
		{doc: prList, declares: "$mergeState: Boolean!"},
		{doc: prMine, declares: "$mergeState: Boolean!"},
		{doc: prRead, declares: "$mergeState: Boolean = true"},
	} {
		t.Run(test.doc.name, func(t *testing.T) {
			for _, want := range []string{test.declares, "mergeable @include(if: $mergeState)", "mergeStateStatus @include(if: $mergeState)"} {
				if !strings.Contains(test.doc.text, want) {
					t.Errorf("the %s document does not contain %q", test.doc.name, want)
				}
			}
		})
	}
}

// TestTheSingleReadSendsNoMergeStateVariable holds the single read to its default:
// its document declares the variable true, so its callers send nothing new.
func TestTheSingleReadSendsNoMergeStateVariable(t *testing.T) {
	h := newHarness(t, map[string]string{documentRoute: prReadEnvelope(t)})
	if _, err := h.client.ReadPR(t.Context(), testRef(), testPR()); err != nil {
		t.Fatalf("ReadPR = %v, want the pull request", err)
	}
	if got, found := sentMergeState(t, h.instance.body(documentRoute)); found {
		t.Errorf("ReadPR sent mergeState %t, want no such variable", got)
	}
}
