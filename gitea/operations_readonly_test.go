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

// TestALabeledCreationReadsInsideItsWrite holds the label lookup of CreatePR and
// CreateIssue to the write it belongs to: a read-only client refuses before the
// lookup is sent, and a repeated creation never revalidates it, because a read inside
// a write must see the instance as it is now.
func TestALabeledCreationReadsInsideItsWrite(t *testing.T) {
	const createIssue = "POST /api/v1/repos/example/example/issues"
	for _, test := range []struct {
		create func(t *testing.T, h *harness) error
		name   string
		route  string
		answer string
	}{
		{
			name: "CreatePR", route: createPull, answer: pullRow(1),
			create: func(t *testing.T, h *harness) error {
				t.Helper()
				_, err := h.client.CreatePR(t.Context(), testRef(), forgeapi.NewPullRequest{
					Title: "Example", SourceBranch: "feature", TargetBranch: "main", Labels: []string{"example-label"},
				})
				return err
			},
		},
		{
			name: "CreateIssue", route: createIssue, answer: issueBody,
			create: func(t *testing.T, h *harness) error {
				t.Helper()
				_, err := h.client.CreateIssue(t.Context(), testRef(), forgeapi.NewIssue{Title: "Example issue", Labels: []string{"example-label"}})
				return err
			},
		},
	} {
		t.Run(test.name+"_on_a_read_only_client_sends_nothing", func(t *testing.T) {
			h := newHarness(t, map[string]string{test.route: test.answer, labelsRoute: labelBody}, forgeapi.WithMutations(false))
			err := test.create(t, h)
			var fe *forgeapi.Error
			if !errors.As(err, &fe) || fe.Code != forgeapi.CodeMutationsDisabled {
				t.Errorf("%s with labels on a read-only client = %v, want code %q", test.name, err, forgeapi.CodeMutationsDisabled)
			}
			if sent := h.instance.arrived(); len(sent) != 0 {
				t.Errorf("%s with labels on a read-only client sent %v, want nothing", test.name, sent)
			}
		})
		t.Run(test.name+"_repeated_never_revalidates_its_label_lookup", func(t *testing.T) {
			h := newHarness(t, map[string]string{test.route: test.answer, labelsRoute: labelBody})
			h.instance.answerHeader("ETag", `"labels"`)
			for range 2 {
				if err := test.create(t, h); err != nil {
					t.Fatalf("%s = %v, want nil", test.name, err)
				}
			}
			if sent := h.instance.revalidated(); len(sent) != 0 {
				t.Errorf("a repeated %s sent a validator on %v, want none inside a write", test.name, sent)
			}
		})
	}
}
