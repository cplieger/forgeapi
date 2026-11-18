package gitlab

import (
	"errors"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// heldRefusal stands in for a consumer's own refusal type, which the call's error
// must reach through errors.As.
type heldRefusal struct{}

func (*heldRefusal) Error() string { return "the pool is held" }

// refuseFirst is a hook refusing the first n attempts whose request matches, passing
// every other attempt on.
func refuseFirst(n int32, matches func(*http.Request) bool) forgeapi.Option {
	var refused atomic.Int32
	return forgeapi.WithAttemptObserver(func(next http.RoundTripper) http.RoundTripper {
		return roundTrip(func(r *http.Request) (*http.Response, error) {
			if matches(r) && refused.Add(1) <= n {
				return nil, &heldRefusal{}
			}
			return next.RoundTrip(r)
		})
	})
}

func checkRefusedAttempt(t *testing.T, what string, err error) {
	t.Helper()
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeAttemptRefused {
		t.Fatalf("%s = %v, want code %q", what, err, forgeapi.CodeAttemptRefused)
	}
	if _, ok := errors.AsType[*heldRefusal](err); !ok {
		t.Errorf("%s = %v, which does not reach the hook's own refusal through errors.As", what, err)
	}
}

// A refused attempt sent nothing, so no metadata header can arrive: detection
// answers the refusal unchanged rather than an undetected family.
func TestConnectionCapsAnswersARefusedAttemptUnchanged(t *testing.T) {
	h := newHarness(t, map[string]string{metadataRoute: `{"version":"17.5.0"}`},
		refuseFirst(1, func(*http.Request) bool { return true }))
	_, err := h.client.ConnectionCaps(t.Context())
	checkRefusedAttempt(t, "ConnectionCaps with its metadata read refused", err)
	if n := h.instance.count(); n != 0 {
		t.Errorf("ConnectionCaps with its metadata read refused sent %d request(s), want 0", n)
	}
}

// A refused document attempt is no refusal of the document, so the connection stays
// on documents: the next read is one document request.
func TestARefusedDocumentAttemptDoesNotDegradeTheConnection(t *testing.T) {
	h := newHarness(t, map[string]string{documentRoute: docRead},
		refuseFirst(1, func(r *http.Request) bool { return r.Method == http.MethodPost }))
	_, err := h.client.ReadPR(t.Context(), testRef(), testPR())
	checkRefusedAttempt(t, "ReadPR with its document attempt refused", err)
	if h.client.isDegraded() {
		t.Error("a refused document attempt degraded the connection, want it on documents")
	}
	if _, err := h.client.ReadPR(t.Context(), testRef(), testPR()); err != nil {
		t.Fatalf("ReadPR after the refusal = %v, want the document's answer", err)
	}
	if got := h.instance.arrived(); !slices.Equal(got, []string{documentRoute}) {
		t.Errorf("ReadPR after the refusal reached %v, want the one document request", got)
	}
}

// A connection read the governor deferred sent nothing either, so detection answers
// the deferral unchanged rather than an undetected family.
func TestConnectionCapsAnswersADeferredReadUnchanged(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v4/user": `{"username": "example-user", "name": "Example User"}`,
		metadataRoute:      `{"version":"17.5.0"}`,
	})
	for _, pair := range [][2]string{{"RateLimit-Name", "throttle_authenticated_api"}, {"RateLimit-Remaining", "10"}, {"RateLimit-Limit", "2000"}, {"RateLimit-Reset", "4102444800"}} {
		h.instance.answerHeader(pair[0], pair[1])
	}
	if _, err := h.client.Whoami(t.Context()); err != nil {
		t.Fatalf("Setup: Whoami = %v", err)
	}
	_, err := h.client.ConnectionCaps(t.Context())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindRateLimited || fe.Status != 0 || fe.Code != "" {
		t.Fatalf("ConnectionCaps with its pool at the reserve = %v, want the governor's deferral", err)
	}
	if fe.Pool != "throttle_authenticated_api" || fe.Remaining != 10 {
		t.Errorf("ConnectionCaps' deferral = pool %q remaining %d, want the held pool's", fe.Pool, fe.Remaining)
	}
}
