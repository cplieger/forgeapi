package gitlab

import (
	"net/http"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// A throttle is not the document's, even where its body is an envelope carrying
// errors: the status maps it, and the connection stays on documents.
func TestAThrottledDocumentCarryingErrorsIsARateLimit(t *testing.T) {
	h := newHarness(t, map[string]string{documentRoute: `{"errors":[{"message":"Retry later"}]}`})
	h.instance.status(documentRoute, http.StatusTooManyRequests)
	_, err := h.client.ReadPR(t.Context(), testRef(), testPR())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindRateLimited || fe.Status != http.StatusTooManyRequests {
		t.Fatalf("ReadPR against a throttled document = %v, want a 429 rate limit", err)
	}
	if h.client.isDegraded() {
		t.Error("a throttled document degraded the connection, want it on documents")
	}
}
