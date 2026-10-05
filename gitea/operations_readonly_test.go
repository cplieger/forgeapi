package gitea

import (
	"errors"
	"testing"

	"github.com/cplieger/forgeapi"
)

// TestARerunOnAReadOnlyClientSendsNothing holds WithMutations(false) to the operation
// boundary its doc names for RerunFailedChecks: the read that resolves the run is part
// of the mutating operation, so a read-only client refuses before anything is sent.
func TestARerunOnAReadOnlyClientSendsNothing(t *testing.T) {
	h := newHarness(t, nil, forgeapi.WithMutations(false))
	before := h.instance.count()
	err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA)
	var fe *forgeapi.Error
	if !errors.As(err, &fe) || fe.Code != forgeapi.CodeMutationsDisabled {
		t.Errorf("RerunFailedChecks on a read-only client = %v, want code %q", err, forgeapi.CodeMutationsDisabled)
	}
	if sent := h.instance.count() - before; sent != 0 {
		t.Errorf("RerunFailedChecks on a read-only client sent %d requests, want none", sent)
	}
}
