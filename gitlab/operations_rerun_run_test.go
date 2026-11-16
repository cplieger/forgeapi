package gitlab

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// The re-run of one run retries the pipeline the run's id names, as the pipelines
// documentation's "Retry jobs in a pipeline" gives the route, with no pipeline read
// first.
func TestARunReRunRetriesThatPipeline(t *testing.T) {
	h := newHarness(t, map[string]string{retryRoute: `{"id":77,"status":"pending"}`})
	if err := h.client.RerunFailedRun(t.Context(), testRef(), forgeapi.RunRef{ID: 77}); err != nil {
		t.Fatalf("RerunFailedRun = %v, want nil", err)
	}
	if sent := h.instance.arrived(); !slices.Equal(sent, []string{retryRoute}) {
		t.Errorf("RerunFailedRun sent %v, want [%s]", sent, retryRoute)
	}
}

// A connection whose re-run verdict is short of yes refuses the run's re-run as it
// refuses the pull request's, with no request.
func TestARunReRunIsRefusedWhereTheReRunCapabilityIsShortOfYes(t *testing.T) {
	h := newHarness(t, map[string]string{
		metadataRoute: `{"message":"401 Unauthorized"}`,
		documentRoute: docAcceptanceAnswer,
		retryRoute:    `{}`,
	})
	h.instance.status(metadataRoute, http.StatusUnauthorized)
	h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
	if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
		t.Fatalf("Setup: ConnectionCaps = %v", err)
	}
	before := len(h.instance.arrived())
	err := h.client.RerunFailedRun(t.Context(), testRef(), forgeapi.RunRef{ID: 77})
	var fe *forgeapi.Error
	if !errors.As(err, &fe) || fe.Code != forgeapi.CodeCapabilityUnsupported {
		t.Errorf("RerunFailedRun = %v, want code %q", err, forgeapi.CodeCapabilityUnsupported)
	}
	if sent := h.instance.arrived()[before:]; len(sent) != 0 {
		t.Errorf("RerunFailedRun sent %v, want nothing", sent)
	}
}

// From 19.6 a retry of a pipeline whose retry is in flight answers 409 Conflict, as
// "Retry jobs in a pipeline" documents, which reaches the caller as a conflict.
func TestARunReRunOfAPipelineWhoseRetryIsInFlightIsAConflict(t *testing.T) {
	h := newHarness(t, map[string]string{retryRoute: `{"message":"409 Conflict"}`})
	h.instance.status(retryRoute, http.StatusConflict)
	err := h.client.RerunFailedRun(t.Context(), testRef(), forgeapi.RunRef{ID: 77})
	var fe *forgeapi.Error
	if !errors.As(err, &fe) || fe.Kind != forgeapi.KindConflict {
		t.Errorf("RerunFailedRun on a 409 = %v, want kind %s", err, forgeapi.KindConflict)
	}
}
