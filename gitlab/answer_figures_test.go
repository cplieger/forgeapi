package gitlab

import (
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
)

// A failure this family reaches on an answer that refused nothing carries that
// answer's own figures, as a refusal's does.
func TestAFailureReachedOnAnAnswerCarriesItsFigures(t *testing.T) {
	h := newHarness(t, map[string]string{metadataRoute: `{"version":"17.5.0"}`})
	h.instance.answerHeader("RateLimit-Name", "throttle_authenticated_api")
	h.instance.answerHeader("RateLimit-Remaining", "1234")
	h.instance.answerHeader("RateLimit-Reset", "1790000000")
	_, err := h.client.ConnectionCaps(t.Context())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeFamilyUndetected || fe.Status != 200 {
		t.Fatalf("ConnectionCaps over a metadata answer with no witness = %v, want code %q at 200", err, forgeapi.CodeFamilyUndetected)
	}
	if fe.Remaining != 1234 || fe.Pool != "throttle_authenticated_api" || !fe.Reset.Equal(time.Unix(1790000000, 0)) {
		t.Errorf("ConnectionCaps = remaining %d pool %q reset %v, want the answer's own", fe.Remaining, fe.Pool, fe.Reset)
	}
}
