package gitea

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/internal/spec"
)

// TestTheReadCacheFollowsEachProductsRevalidationRow holds both products of this
// family to their spec.Revalidations row on a connection whose setup detected the
// product. As measured, neither sends a validator on its JSON API, so no read is
// sent conditional and none is served. An instance that sent one anyway would be
// revalidated, its 304 billed at the row's price.
func TestTheReadCacheFollowsEachProductsRevalidationRow(t *testing.T) {
	for _, test := range []struct {
		product spec.Product
		version string
		header  string
	}{
		{product: spec.Gitea, version: "1.22.0", header: headerGiteaVersion},
		{product: spec.Forgejo, version: "16.0.0+gitea-1.22.0"},
	} {
		price := -1
		for _, r := range spec.Revalidations {
			if r.Product == test.product {
				price = r.NotModifiedPrice
			}
		}
		for _, validator := range []bool{false, true} {
			name := string(test.product) + "_as_measured"
			if validator {
				name = string(test.product) + "_sending_a_validator_anyway"
			}
			t.Run(name, func(t *testing.T) {
				if price < 0 {
					t.Fatalf("spec.Revalidations states no %s row", test.product)
				}
				in := newInstance()
				var conditional, notModified atomic.Int64
				in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/api/v1/version" {
						if test.header != "" {
							w.Header().Set(test.header, test.version)
						}
						_, _ = io.WriteString(w, `{"version":"`+test.version+`"}`)
						return
					}
					if r.Header.Get("If-None-Match") != "" {
						conditional.Add(1)
					}
					if validator {
						w.Header().Set("ETag", `W/"user"`)
						if r.Header.Get("If-None-Match") == `W/"user"` {
							notModified.Add(1)
							w.WriteHeader(http.StatusNotModified)
							return
						}
					}
					_, _ = io.WriteString(w, `{"login":"octo"}`)
				}))
				t.Cleanup(in.server.Close)
				h := newHarnessOver(t, in, time.Now)
				if err := h.client.resolveVersion(t.Context()); err != nil {
					t.Fatalf("Setup: detecting the product = %v", err)
				}
				if h.client.forgejo != (test.product == spec.Forgejo) {
					t.Fatalf("Setup: the connection detected forgejo=%v, want %s", h.client.forgejo, test.product)
				}
				first, err := h.client.Whoami(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				second, err := h.client.Whoami(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				state := h.client.BudgetState()
				if second.Login != first.Login || second.Login != "octo" {
					t.Errorf("the second Whoami = %+v, want %+v", second, first)
				}
				if !validator {
					if conditional.Load() != 0 || state.ReadCache.Hits != 0 || state.ReadCache.Saved != 0 {
						t.Errorf("with no validator sent = %d conditional read(s), %d hit(s), %d saved, want the cache inert",
							conditional.Load(), state.ReadCache.Hits, state.ReadCache.Saved)
					}
					return
				}
				if notModified.Load() != 1 || state.ReadCache.Hits != 1 {
					t.Fatalf("Setup: the second Whoami = %d not-modified answer(s), %d hit(s), want one served", notModified.Load(), state.ReadCache.Hits)
				}
				wantSaved := 0
				if price == 0 {
					wantSaved = 1
				}
				if state.LastCost != price || state.ReadCache.Saved != wantSaved {
					t.Errorf("a revalidated Whoami = LastCost %d, Saved %d, want %d and %d", state.LastCost, state.ReadCache.Saved, price, wantSaved)
				}
			})
		}
	}
}
