package github

import (
	"slices"
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
