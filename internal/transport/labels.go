package transport

import (
	"errors"
	"strings"

	"github.com/cplieger/forgeapi/v3"
)

// DistinctNames is names with every empty name and every repeat after the first
// dropped, in order, so a removal sends one request per label however often the
// caller named it. An empty name names no label, and on a route that takes the name
// as its last segment it would address the collection: GitHub's DELETE there
// removes every label the item carries.
func DistinctNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// AfterRemovals is err, a refusal a label removal met after it had removed the
// labels named in removed, with its Message naming them: those removals stay, and
// [forgeapi.Labels.RemoveLabels] says the refusal names them. A context sentinel
// carries no message and is returned as it is.
func AfterRemovals(err error, removed []string) error {
	fe, ok := errors.AsType[*forgeapi.Error](err)
	if !ok || len(removed) == 0 {
		return err
	}
	note := "removed before this refusal: " + strings.Join(removed, ", ")
	if fe.Message == "" {
		fe.Message = note
	} else {
		fe.Message += "; " + note
	}
	return err
}
