package gitea

import (
	"errors"
	"net/http"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// heldRefusal stands in for a consumer's own refusal type, which the call's error
// must reach through errors.As.
type heldRefusal struct{}

func (*heldRefusal) Error() string { return "the pool is held" }

// A refused attempt at either of detection's reads asked the instance nothing, so
// detection answers the refusal unchanged, never an undetected family or a missing
// document.
func TestConnectionCapsAnswersARefusedAttemptUnchanged(t *testing.T) {
	for _, path := range []string{"/api/v1/version", "/swagger.v1.json"} {
		t.Run(path[1:], func(t *testing.T) {
			hook := forgeapi.WithAttemptObserver(func(next http.RoundTripper) http.RoundTripper {
				return roundTrip(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == path {
						return nil, &heldRefusal{}
					}
					return next.RoundTrip(r)
				})
			})
			h := newHarness(t, map[string]string{
				"GET /api/v1/version":  `{"version":"1.22.0"}`,
				"GET /swagger.v1.json": `{"info":{"title":"Gitea API"},"paths":{}}`,
			}, hook)
			_, err := h.client.ConnectionCaps(t.Context())
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeAttemptRefused {
				t.Fatalf("ConnectionCaps with %s refused = %v, want code %q", path, err, forgeapi.CodeAttemptRefused)
			}
			if _, ok := errors.AsType[*heldRefusal](err); !ok {
				t.Errorf("ConnectionCaps with %s refused = %v, which does not reach the hook's own refusal", path, err)
			}
		})
	}
}
