package github

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

func everyRequest(*http.Request) bool { return true }

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

// A refused attempt sent nothing, so no witness can arrive: detection answers the
// refusal unchanged rather than an undetected family.
func TestConnectionCapsAnswersARefusedAttemptUnchanged(t *testing.T) {
	h := newHarness(t, map[string]string{metaRoute: `{}`, versionsRoute: servedVersions}, refuseFirst(1, everyRequest))
	withWitness(h.instance)
	_, err := h.client.ConnectionCaps(t.Context())
	checkRefusedAttempt(t, "ConnectionCaps with its metadata read refused", err)
	if n := h.instance.count(); n != 0 {
		t.Errorf("ConnectionCaps with its metadata read refused sent %d request(s), want 0", n)
	}
}

// A refused version-set read asked the instance nothing, so setup answers the refusal
// and holds no verdict; the next call asks again and holds the pin the set states.
func TestConnectionCapsAnswersARefusedVersionsReadAndAsksAgain(t *testing.T) {
	h := newHarness(t, map[string]string{metaRoute: `{}`, versionsRoute: servedVersions},
		refuseFirst(1, func(r *http.Request) bool { return r.URL.Path == "/api/v3"+versionsPath }))
	withWitness(h.instance)
	_, err := h.client.ConnectionCaps(t.Context())
	checkRefusedAttempt(t, "ConnectionCaps with its version-set read refused", err)
	if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
		t.Fatalf("ConnectionCaps after the refusal = %v, want the capabilities", err)
	}
	if got := h.instance.arrived(); !slices.Contains(got, versionsRoute) {
		t.Errorf("ConnectionCaps after the refusal reached %v, want the version set read again", got)
	}
	if !h.client.sendsPin() {
		t.Error("the connection holds no pin after the version set answered the pinned version, want it held")
	}
}

// A refused document attempt is no refusal of the document, so the connection stays
// on documents: the next read is one document request.
func TestARefusedDocumentAttemptDoesNotDegradeTheConnection(t *testing.T) {
	h := newHarness(t, map[string]string{documentRoute: prReadEnvelope(t)},
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

// A connection at its mutation reserve has the version-set read deferred: setup
// answers the deferral rather than holding no pin for the connection's life.
func TestConnectionCapsAnswersADeferredVersionsRead(t *testing.T) {
	h := newHarness(t, map[string]string{metaRoute: `{}`, versionsRoute: servedVersions})
	withWitness(h.instance)
	for _, pair := range [][2]string{{"X-RateLimit-Resource", "core"}, {"X-RateLimit-Remaining", "10"}, {"X-RateLimit-Limit", "5000"}, {"X-RateLimit-Reset", "4102444800"}} {
		h.instance.answerHeader(pair[0], pair[1])
	}
	_, err := h.client.ConnectionCaps(t.Context())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindRateLimited || fe.Status != 0 || fe.Pool != "core" {
		t.Fatalf("ConnectionCaps with its pool at the reserve = %v, want the governor's deferral of the core pool", err)
	}
	if got := h.instance.arrived(); slices.Contains(got, versionsRoute) {
		t.Errorf("ConnectionCaps at the reserve reached %v, want the version set held back", got)
	}
}

// A metadata read deferred before any witness arrived asked nothing either, so the
// deferral is answered, never an undetected family.
func TestConnectionCapsAnswersADeferredMetadataRead(t *testing.T) {
	h := newHarness(t, map[string]string{metaRoute: `{}`})
	for _, pair := range [][2]string{{"X-RateLimit-Resource", "core"}, {"X-RateLimit-Remaining", "10"}, {"X-RateLimit-Limit", "5000"}, {"X-RateLimit-Reset", "4102444800"}} {
		h.instance.answerHeader(pair[0], pair[1])
	}
	var fe *forgeapi.Error
	if _, err := h.client.ConnectionCaps(t.Context()); !asForgeError(err, &fe) || fe.Code != forgeapi.CodeFamilyUndetected {
		t.Fatalf("Setup: ConnectionCaps over an answer with no witness = %v, want code %q", err, forgeapi.CodeFamilyUndetected)
	}
	_, err := h.client.ConnectionCaps(t.Context())
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindRateLimited || fe.Status != 0 || fe.Code != "" {
		t.Errorf("ConnectionCaps with its pool at the reserve = %v, want the governor's deferral", err)
	}
}
