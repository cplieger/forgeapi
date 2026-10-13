package gitlab

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi/internal/spec"
)

// TestANotModifiedAnswerIsBilledAsSpecStates holds this family's price of a
// not-modified answer to spec.Revalidations, which states from GitLab's rate-limit
// documentation and a measurement that a 304 is billed like any request: the held
// answer serves the call, and nothing is counted saved.
func TestANotModifiedAnswerIsBilledAsSpecStates(t *testing.T) {
	price := -1
	for _, r := range spec.Revalidations {
		if r.Product == spec.GitLab {
			price = r.NotModifiedPrice
		}
	}
	if price < 0 {
		t.Fatal("spec.Revalidations states no GitLab row")
	}
	in := newInstance()
	var notModified atomic.Int64
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `W/"user"`)
		if r.Header.Get("If-None-Match") == `W/"user"` {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"username":"octo"}`)
	}))
	t.Cleanup(in.server.Close)
	h := newHarnessOver(t, in)
	first, err := h.client.Whoami(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.client.Whoami(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state := h.client.BudgetState()
	wantSaved := 0
	if price == 0 {
		wantSaved = 1
	}
	if notModified.Load() != 1 || state.ReadCache.Hits != 1 {
		t.Fatalf("Setup: the second Whoami = %d not-modified answer(s), %d hit(s), want one served", notModified.Load(), state.ReadCache.Hits)
	}
	if state.LastCost != price || state.ReadCache.Saved != wantSaved {
		t.Errorf("a revalidated Whoami = LastCost %d, Saved %d, want %d and %d", state.LastCost, state.ReadCache.Saved, price, wantSaved)
	}
	if second.Login != first.Login || second.Login != "octo" {
		t.Errorf("the revalidated Whoami = %+v, want the held answer %+v", second, first)
	}
}
