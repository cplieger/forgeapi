package github

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// The addition reads the first label page at the most a REST page serves, and sends
// the names the caller gave.
func TestALabelAdditionReadsTheWholeFirstPageAndSendsTheNames(t *testing.T) {
	const read, write = "GET /api/v3/repos/example/example/labels", "POST /api/v3/repos/example/example/issues/3/labels"
	h := newHarness(t, map[string]string{read: `[{"name":"a"},{"name":"b"}]`, write: `[]`})
	if err := h.client.AddLabels(t.Context(), testRef(), forgeapi.LabelTarget{PR: forgeapi.PRRef{Number: 3}}, []string{"b", "a"}); err != nil {
		t.Fatalf("AddLabels = %v, want nil", err)
	}
	if got := h.instance.query(read, keyPerPage); got != "100" {
		t.Errorf("AddLabels read the labels with per_page %q, want 100, the REST maximum", got)
	}
	labels, _ := sentBody(t, h, write)[keyLabels].([]any)
	if !slices.Equal(labels, []any{"b", "a"}) {
		t.Errorf("AddLabels sent labels %v, want [b a]", labels)
	}
}

// A label name is one path segment, so a separator inside it is escaped rather than
// read as another segment, and a name given twice is removed once.
func TestALabelRemovalEscapesTheNameAndRemovesItOnce(t *testing.T) {
	const route = "DELETE /api/v3/repos/example/example/issues/1/labels/area%2Fui%20x"
	h := newHarness(t, map[string]string{route: `[]`})
	if err := h.client.RemoveLabels(t.Context(), testRef(), forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}, []string{"area/ui x", "area/ui x"}); err != nil {
		t.Fatalf("RemoveLabels = %v, want nil", err)
	}
	if got := h.instance.arrived(); !slices.Equal(got, []string{route}) {
		t.Errorf("RemoveLabels sent %v, want the one escaped removal %s", got, route)
	}
}

// An empty name names no label: sent, it would be the collection's DELETE, which
// takes every label off the item.
func TestALabelRemovalSendsNoEmptyName(t *testing.T) {
	const route = "DELETE /api/v3/repos/example/example/issues/1/labels/bug"
	for _, tc := range []struct {
		name  string
		names []string
		want  []string
	}{
		{name: "an empty name alone sends nothing", names: []string{""}, want: nil},
		{name: "an empty name beside a label removes only that label", names: []string{"bug", ""}, want: []string{route}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{route: `[]`})
			if err := h.client.RemoveLabels(t.Context(), testRef(), forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}, tc.names); err != nil {
				t.Fatalf("RemoveLabels(%q) = %v, want nil", tc.names, err)
			}
			if got := h.instance.arrived(); !slices.Equal(got, tc.want) {
				t.Errorf("RemoveLabels(%q) sent %v, want %v", tc.names, got, tc.want)
			}
		})
	}
}

// On a renamed repository the removal follows the method-preserving hop and lands at
// the successor before the call is refused as stale, so the refusal names that label
// among the removed; a hop answered 404 removed nothing.
func TestARemovalOnARenamedRepositoryNamesTheLabelItsHopRemoved(t *testing.T) {
	const moved = "DELETE /api/v3/repositories/7691631/issues/1/labels/bug"
	for _, test := range []struct {
		name    string
		status  int
		removed bool
	}{
		{name: "the_hop_removed_the_label", status: http.StatusOK, removed: true},
		{name: "the_hop_found_no_such_label", status: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := renamedInstance(t, map[string]string{moved: `[]`}, true, http.StatusTemporaryRedirect)
			h.instance.status(moved, test.status)
			err := h.client.RemoveLabels(t.Context(), testRef(), forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}, []string{"bug", "other"})
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefStale {
				t.Fatalf("RemoveLabels on a renamed repository = %v, want code %q", err, forgeapi.CodeRepoRefStale)
			}
			if got := strings.Contains(fe.Message, "removed before this refusal: bug"); got != test.removed {
				t.Errorf("RemoveLabels on a renamed repository = message %q, naming bug removed %v, want %v", fe.Message, got, test.removed)
			}
			if slices.ContainsFunc(h.instance.arrived(), func(r string) bool { return strings.HasSuffix(r, "/labels/other") }) {
				t.Errorf("RemoveLabels sent %v, want nothing after the stale refusal", h.instance.arrived())
			}
		})
	}
}
