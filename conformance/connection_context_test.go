package conformance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// Every operation answers nil, a *forgeapi.Error or a context sentinel returned
// unchanged, and the connection read is no exception: a read its own context ended
// answers that context's sentinel, whatever header the cut answer carried or
// lacked, rather than reading the cut as the family's absence.
func TestAConnectionReadEndedByItsContextAnswersTheContextsOwnSentinel(t *testing.T) {
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				cancel()
				select {
				case <-r.Context().Done():
				case <-time.After(10 * time.Second):
					t.Errorf("Setup: the request to %s outlived its cancelled context by ten seconds", r.URL.EscapedPath())
				}
			}))
			t.Cleanup(srv.Close)
			caps, ok := pagedClient(t, p, srv).(forgeapi.Capabilities)
			if !ok {
				t.Fatal(roleAbsent("Capabilities"))
			}

			_, err := guard(func() (any, error) { return caps.ConnectionCaps(ctx) })
			if !errors.Is(err, context.Canceled) {
				t.Errorf("ConnectionCaps on %s cancelled during its read = error %v, want %v", p, err, context.Canceled)
			}
			if fe, folded := errors.AsType[*forgeapi.Error](err); folded {
				t.Errorf("ConnectionCaps on %s cancelled during its read = code %q, want the context's sentinel unchanged", p, fe.Code)
			}
		})
	}
}
