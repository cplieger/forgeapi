package github

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// The re-run of one run posts the failed-jobs re-run the REST description gives
// re-run-workflow-failed-jobs, addressed by the run's id, with no run read first.
func TestARunReRunPostsTheFailedJobsRouteOfThatRun(t *testing.T) {
	const route = "POST /api/v3/repos/example/example/actions/runs/42/rerun-failed-jobs"
	h := newHarness(t, map[string]string{route: `{}`})
	if err := h.client.RerunFailedRun(t.Context(), testRef(), forgeapi.RunRef{ID: 42, Number: 7}); err != nil {
		t.Fatalf("RerunFailedRun = %v, want nil", err)
	}
	if sent := h.instance.arrived(); !slices.Equal(sent, []string{route}) {
		t.Errorf("RerunFailedRun sent %v, want [%s]", sent, route)
	}
}

func TestARunReRunOnAReadOnlyClientSendsNothing(t *testing.T) {
	h := newHarness(t, nil, forgeapi.WithMutations(false))
	err := h.client.RerunFailedRun(t.Context(), testRef(), forgeapi.RunRef{ID: 42})
	var fe *forgeapi.Error
	if !errors.As(err, &fe) || fe.Code != forgeapi.CodeMutationsDisabled {
		t.Errorf("RerunFailedRun on a read-only client = %v, want code %q", err, forgeapi.CodeMutationsDisabled)
	}
	if sent := h.instance.arrived(); len(sent) != 0 {
		t.Errorf("RerunFailedRun on a read-only client sent %v, want nothing", sent)
	}
}

// A run's re-run against a renamed repository refuses with the successor the
// id-addressed read names, as every write of this family does.
func TestARunReRunOfARenamedRepositoryRefusesWithTheSuccessor(t *testing.T) {
	h := renamedInstance(t, map[string]string{
		"POST /api/v3/repositories/7691631/actions/runs/42/rerun-failed-jobs": `{}`,
	}, true, http.StatusTemporaryRedirect)
	err := h.client.RerunFailedRun(t.Context(), testRef(), forgeapi.RunRef{ID: 42})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefStale {
		t.Fatalf("RerunFailedRun against a renamed repository = %v, want code %q", err, forgeapi.CodeRepoRefStale)
	}
	if fe.Successor == nil || fe.Successor.Selector != "example/renamed" {
		t.Errorf("RerunFailedRun against a renamed repository = successor %+v, want example/renamed", fe.Successor)
	}
}
