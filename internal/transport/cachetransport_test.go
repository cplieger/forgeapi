package transport

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// The key folds the credential as a digest, so nothing the cache holds carries
// the Authorization value a request sent.
func TestTheTransportHoldsNoCredentialInClear(t *testing.T) {
	const credential = "test-key-credential"
	o := newOrigin(`{}`, `"v1"`)
	o.header.Set("Link", "<next>")
	tr := NewCacheTransport(http.DefaultTransport, CacheConfig{MaxBytes: 1 << 20, Held: []string{"Link"}})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, serveOrigin(t, o).URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip = %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	tr.cache.mu.Lock()
	defer tr.cache.mu.Unlock()
	if len(tr.cache.index) != 1 {
		t.Fatalf("the cache holds %d entries, want the one read", len(tr.cache.index))
	}
	for key, el := range tr.cache.index {
		e, _ := el.Value.(*cacheEntry)
		held := key + e.key + e.page.ETag + e.page.LastModified + string(e.page.Body) + fmt.Sprint(e.page.Fields)
		if strings.Contains(held, credential) {
			t.Errorf("the cache holds %q, which carries the credential in clear", held)
		}
	}
}

// A consumer reads a GraphQL document's cost from LastCost after the call, so the
// read cache never revalidates a document or rewrites what it cost.
func TestADocumentsCostIsTheLastCostBesideCachedReads(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	family := testOptions()
	family.FreeNotModified = true
	c := openConnWith(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, family)
	document := func() {
		t.Helper()
		var out account
		req := &Request{Op: "Document", Method: http.MethodPost, Path: "/graphql", Document: true, Body: map[string]string{"query": "{viewer{login}}"}, Into: &out}
		if _, err := c.Do(Call(t.Context(), req.Op), req); err != nil {
			t.Fatalf("the document = %v", err)
		}
	}
	for range 2 {
		if _, _, err := read(t, c, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.BudgetState().ReadCache.Hits; got != 1 {
		t.Fatalf("Setup: the REST reads hit %d time(s), want 1", got)
	}
	document()
	c.Price(7)
	if got := c.BudgetState().LastCost; got != 7 {
		t.Errorf("after a document the family priced at 7, LastCost = %d, want 7", got)
	}
	document()
	if got := c.BudgetState().LastCost; got != 1 {
		t.Errorf("after a repeated document priced by the wire, LastCost = %d, want 1", got)
	}
	if got := o.last().Get("If-None-Match"); got != "" {
		t.Errorf("the repeated document carried If-None-Match %q, want none", got)
	}
}

// An entry is immutable once kept, because a reader holds it past the lock: the
// entry a 304 leaves is a copy.
func TestRefreshingAnEntryLeavesTheKeptOneUnchanged(t *testing.T) {
	held := heldTable{{name: "Link"}}
	kept := &pageEntry{ETag: `"v1"`, Fields: [][]string{{"<old>"}}}
	fresh := kept.refreshed(held, http.Header{"Link": {"<new>"}})
	if !slices.Equal(kept.Fields[0], []string{"<old>"}) || !slices.Equal(fresh.Fields[0], []string{"<new>"}) {
		t.Errorf("after a refresh the kept entry holds %q and the fresh one %q, want <old> and <new>", kept.Fields[0], fresh.Fields[0])
	}
}
