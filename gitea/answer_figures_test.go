package gitea

import (
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
)

// A failure this family reaches on an answer that refused nothing carries that
// answer's own figures, as a refusal's does.
func TestAFailureReachedOnAnAnswerCarriesItsFigures(t *testing.T) {
	h := newHarness(t, map[string]string{"GET /api/v1/version": `{"version":"not a release"}`})
	h.instance.answerHeader("RateLimit", `"baseline";r=42;t=60`)
	before := time.Now()
	_, err := h.client.ConnectionCaps(t.Context())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeFamilyUndetected || fe.Status != 200 {
		t.Fatalf("ConnectionCaps over a version body with no release = %v, want code %q at 200", err, forgeapi.CodeFamilyUndetected)
	}
	if fe.Remaining != 42 || fe.Reset.Before(before.Add(time.Minute)) {
		t.Errorf("ConnectionCaps = remaining %d reset %v, want the answer's 42 and a reset a minute on", fe.Remaining, fe.Reset)
	}
}
