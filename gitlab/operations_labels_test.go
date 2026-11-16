package gitlab

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

func sentList(t *testing.T, h *harness, route, key string) []string {
	t.Helper()
	var body map[string][]string
	if err := json.Unmarshal([]byte(h.instance.body(route)), &body); err != nil {
		t.Fatalf("Setup: decoding the body %s received: %v", route, err)
	}
	return body[key]
}

// The addition reads the project's first label page at the REST maximum and puts
// the names on the merge request the target names.
func TestALabelAdditionOnAMergeRequestSendsItsNames(t *testing.T) {
	const read, write = "GET /api/v4/projects/example%2Fgroup%2Fexample/labels", "PUT /api/v4/projects/example%2Fgroup%2Fexample/merge_requests/4"
	h := newHarness(t, map[string]string{read: `[{"name":"a"}]`, write: `{}`})
	if err := h.client.AddLabels(t.Context(), testRef(), forgeapi.LabelTarget{PR: forgeapi.PRRef{Number: 4}}, []string{"a"}); err != nil {
		t.Fatalf("AddLabels = %v, want nil", err)
	}
	if got := h.instance.query(read, keyPerPage); got != "100" {
		t.Errorf("AddLabels read the labels with per_page %q, want 100, the REST maximum", got)
	}
	if got := sentList(t, h, write, "add_labels"); !slices.Equal(got, []string{"a"}) {
		t.Errorf("AddLabels sent add_labels %v, want [a]", got)
	}
}

// A name with a comma or surrounding space is no label title on this product and
// would split or strip into another name, so it is not sent; a removal left with no
// name sends nothing.
func TestALabelRemovalSendsOnlyNamesThatCanBeTitles(t *testing.T) {
	const route = "PUT /api/v4/projects/example%2Fgroup%2Fexample/issues/1"
	h := newHarness(t, map[string]string{route: `{}`})
	issue := forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}
	if err := h.client.RemoveLabels(t.Context(), testRef(), issue, []string{"a,b", " a", "", "c d", "c d"}); err != nil {
		t.Fatalf("RemoveLabels = %v, want nil", err)
	}
	if got := sentList(t, h, route, "remove_labels"); !slices.Equal(got, []string{"c d"}) {
		t.Errorf("RemoveLabels sent remove_labels %v, want [c d]", got)
	}
	before := h.instance.count()
	if err := h.client.RemoveLabels(t.Context(), testRef(), issue, []string{"x,y", "z "}); err != nil {
		t.Fatalf("RemoveLabels of no title = %v, want nil", err)
	}
	if n := h.instance.count() - before; n != 0 {
		t.Errorf("RemoveLabels of names no title can be sent %d request(s), want none", n)
	}
}
