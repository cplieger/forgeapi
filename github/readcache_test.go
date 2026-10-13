package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// anonymousCredential answers no token, which the harness uses for anonymous
// access: the request then carries no Authorization header.
type anonymousCredential struct{}

func (anonymousCredential) Token(context.Context) (string, error) { return "", nil }
func (anonymousCredential) Kind() forgeapi.CredKind               { return forgeapi.CredKindStaticPAT }
func (anonymousCredential) State() forgeapi.CredState             { return forgeapi.CredValid }

var labelsRepo = forgeapi.RepoRef{Selector: "o/r"}

func openCached(t *testing.T, srv *httptest.Server, opts ...forgeapi.Option) *Client {
	t.Helper()
	c, err := New(forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL}, append([]forgeapi.Option{
		forgeapi.WithWireTransport(srv.Client().Transport), forgeapi.WithCredentialSource(testCredential{}),
		forgeapi.WithPlaintextHTTP(true), forgeapi.WithPrivateAddresses(true),
	}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestANotModifiedAnswerIsBilledAsSpecStates holds this family's price of a
// not-modified answer to spec.Revalidations, which states it from GitHub's
// best-practices page: a 304 to a request made with an Authorization header does
// not count against the primary rate limit. The page states no exemption for one
// made without it, so this library bills that at 1. The 304's own rate headers
// still move the budget.
func TestANotModifiedAnswerIsBilledAsSpecStates(t *testing.T) {
	price := -1
	for _, r := range spec.Revalidations {
		if r.Product == spec.GitHub {
			price = r.NotModifiedPrice
		}
	}
	if price != 0 {
		t.Fatalf("spec.Revalidations prices a GitHub not-modified answer at %d, want 0", price)
	}
	for _, test := range []struct {
		source    forgeapi.CredentialSource
		name      string
		wantCost  int
		wantSaved int
	}{
		{name: "authorized", source: testCredential{}, wantCost: price, wantSaved: 1},
		{name: "anonymous", source: anonymousCredential{}, wantCost: 1, wantSaved: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			remaining := "4999"
			var notModified int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(headerRequestID, "C0DE:1")
				w.Header().Set("X-RateLimit-Remaining", remaining)
				w.Header().Set("X-RateLimit-Reset", "4102444800")
				w.Header().Set("ETag", `W/"labels"`)
				if r.Header.Get("If-None-Match") == `W/"labels"` {
					notModified++
					w.WriteHeader(http.StatusNotModified)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `[{"name":"bug","color":"ff0000"}]`)
			}))
			defer srv.Close()
			c := openCached(t, srv, forgeapi.WithCredentialSource(test.source))
			first, err := c.ListLabels(t.Context(), labelsRepo)
			if err != nil {
				t.Fatal(err)
			}
			remaining = "4998"
			second, err := c.ListLabels(t.Context(), labelsRepo)
			if err != nil {
				t.Fatal(err)
			}
			state := c.BudgetState()
			if notModified != 1 || state.LastCost != test.wantCost || state.ReadCache.Saved != test.wantSaved {
				t.Errorf("a revalidated ListLabels = %d not-modified answer(s), LastCost %d, Saved %d, want one at %d and %d",
					notModified, state.LastCost, state.ReadCache.Saved, test.wantCost, test.wantSaved)
			}
			if state.Remaining != 4998 {
				t.Errorf("Remaining = %d, want 4998: the not-modified answer's rate headers move the budget", state.Remaining)
			}
			if len(second.Items) != 1 || second.Items[0] != first.Items[0] {
				t.Errorf("the revalidated ListLabels = %+v, want the held answer %+v", second.Items, first.Items)
			}
		})
	}
}

// A consumer meters a document call by BudgetState().LastCost, so the cache never
// replaces the cost the endpoint reported, a REST 304 billed at 0 just before it
// included.
func TestADocumentCallAfterARevalidatedReadReportsTheDocumentsCost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerRequestID, "C0DE:1")
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = io.WriteString(w, `{"data":{"rateLimit":{"cost":3,"limit":5000,"remaining":4990},"repository":{"nameWithOwner":"example/example"}}}`)
			return
		}
		w.Header().Set("ETag", `W/"labels"`)
		if r.Header.Get("If-None-Match") == `W/"labels"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, `[]`)
	}))
	defer srv.Close()
	c := openCached(t, srv)
	for range 2 {
		if _, err := c.ListLabels(t.Context(), labelsRepo); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.BudgetState().LastCost; got != 0 {
		t.Fatalf("Setup: LastCost after an authorized 304 = %d, want 0", got)
	}
	var payload docPayload
	if _, err := c.execute(t.Context(), "ListPRs", prList, map[string]any{}, &payload); err != nil {
		t.Fatal(err)
	}
	if got := c.BudgetState().LastCost; got != 3 {
		t.Errorf("LastCost after a document charged 3 = %d, want 3", got)
	}
}

// githubLike answers as GitHub was measured to: an ETag over the body alone (two
// answers with one body and different Link headers carried one ETag), and a 304
// carrying neither Link nor X-OAuth-Scopes (spec.Revalidations).
type githubLike struct {
	pages  map[string]string
	link   map[string]string
	scopes string
	sent   []http.Header
	mu     sync.Mutex
}

func (g *githubLike) set(f func(*githubLike)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f(g)
}

func (g *githubLike) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sent = append(g.sent, r.Header.Clone())
	w.Header().Set(headerRequestID, "C0DE:1")
	w.Header().Set("X-RateLimit-Remaining", "4000")
	w.Header().Set("X-RateLimit-Reset", "4102444800")
	body := `{"login":"octo"}`
	page := r.URL.Query().Get(keyPage)
	if strings.HasSuffix(r.URL.Path, "/labels") {
		body = g.pages[page]
	}
	sum := sha256.Sum256([]byte(body))
	tag := `W/"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", tag)
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if l := g.link[page]; l != "" && strings.HasSuffix(r.URL.Path, "/labels") {
		w.Header().Set(headerLink, l)
	}
	w.Header().Set("X-OAuth-Scopes", g.scopes)
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

const (
	labelA = `{"name":"a","color":"ffffff"}`
	labelB = `{"name":"b","color":"ffffff"}`
	labelC = `{"name":"c","color":"ffffff"}`
)

// A revalidated list page carries the held 200's Link, because GitHub's 304 omits it
// and its ETag does not cover it. A row added beyond a full, unchanged page changes
// neither, so the served page must still continue, as a fresh read does, and a walk
// on the cached client reaches the new row.
func TestARevalidatedFullPageReachesRowsAddedBeyondIt(t *testing.T) {
	g := &githubLike{pages: map[string]string{"1": "[" + labelA + "," + labelB + "]", "2": "[]"}, link: map[string]string{}, scopes: "repo"}
	srv := httptest.NewServer(g)
	defer srv.Close()
	cached := openCached(t, srv)
	first, err := cached.ListLabels(t.Context(), labelsRepo, forgeapi.WithPageBound(2))
	if err != nil || first.Next != "" {
		t.Fatalf("Setup: the first read = next %q, %v, want the whole list in one page", first.Next, err)
	}
	g.set(func(g *githubLike) {
		g.pages["2"] = "[" + labelC + "]"
		g.link["1"] = "<" + srv.URL + `/repos/o/r/labels?per_page=2&page=2>; rel="next"`
	})
	served, err := cached.ListLabels(t.Context(), labelsRepo, forgeapi.WithPageBound(2))
	if err != nil {
		t.Fatal(err)
	}
	if hits := cached.BudgetState().ReadCache.Hits; hits != 1 {
		t.Fatalf("Setup: the second read = %d hit(s), want page 1 served from the held answer", hits)
	}
	if served.Next == "" {
		t.Fatalf("a served full page = no continuation, want one: the row added beyond it is unreachable")
	}
	rest, err := cached.ListLabels(t.Context(), labelsRepo, forgeapi.WithPageBound(2), forgeapi.WithAfter(served.Next))
	if err != nil {
		t.Fatal(err)
	}
	if len(rest.Items) != 1 || rest.Items[0].Name != "c" || rest.Next != "" {
		t.Errorf("the page after the served one = %+v, next %q, want the added label alone", rest.Items, rest.Next)
	}
}

// A page short of its size holds every row the list has beyond its start, so a row
// added after it changes its body, and a served short page ends the list.
func TestARevalidatedShortPageEndsTheList(t *testing.T) {
	g := &githubLike{pages: map[string]string{"1": "[" + labelA + "]"}, link: map[string]string{}, scopes: "repo"}
	srv := httptest.NewServer(g)
	defer srv.Close()
	cached := openCached(t, srv)
	for range 2 {
		page, err := cached.ListLabels(t.Context(), labelsRepo, forgeapi.WithPageBound(2))
		if err != nil || page.Next != "" {
			t.Fatalf("a short page = next %q, %v, want the list ended", page.Next, err)
		}
	}
	if hits := cached.BudgetState().ReadCache.Hits; hits != 1 {
		t.Errorf("the second read = %d hit(s), want the short page served from the held answer", hits)
	}
}

// GitHub's 304 to GET /user carries no X-OAuth-Scopes, and the scopes of a classic
// token can change while its /user body does not. Whoami therefore reads them fresh
// on every call, sent with no validator.
func TestWhoamiReportsTheScopesTheTokenHoldsNow(t *testing.T) {
	g := &githubLike{scopes: "repo"}
	srv := httptest.NewServer(g)
	defer srv.Close()
	cached := openCached(t, srv)
	if _, err := cached.Whoami(t.Context()); err != nil {
		t.Fatal(err)
	}
	g.set(func(g *githubLike) { g.scopes = "repo, workflow" })
	got, err := cached.Whoami(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Scopes) != 2 || got.Scopes[1] != "workflow" {
		t.Errorf("Whoami after the token's scopes changed = %v, want [repo workflow]", got.Scopes)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, h := range g.sent {
		if v := h.Get("If-None-Match"); v != "" {
			t.Errorf("identity read %d carried If-None-Match %q, want none", i+1, v)
		}
	}
}

// TestTheSuccessorReadOfARedirectedMutationIsNeverRevalidated holds a write's reads
// out of the cache. A mutation that met a method-preserving redirect resolves the
// renamed repository's successor with a read, and that read belongs to the write: it
// carries no validator even where an earlier read holds the same record, so it adds
// no hit.
func TestTheSuccessorReadOfARedirectedMutationIsNeverRevalidated(t *testing.T) {
	const successor = "/api/v3/repositories/7691631"
	var mu sync.Mutex
	var validators []string
	in := newInstance()
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		if after, ok := strings.CutPrefix(path, "/api/v3/repos/example/example"); ok {
			status := http.StatusMovedPermanently
			if r.Method != http.MethodGet {
				status = http.StatusTemporaryRedirect
			}
			http.Redirect(w, r, successor+after, status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		answer := `{"number":1,"state":"open","title":"t","merged":false}`
		if path == successor {
			mu.Lock()
			validators = append(validators, r.Header.Get("If-None-Match"))
			mu.Unlock()
			w.Header().Set("ETag", `W/"successor"`)
			if r.Header.Get("If-None-Match") == `W/"successor"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			answer = `{"full_name":"example/renamed"}`
		}
		if _, err := io.WriteString(w, answer); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(in.server.Close)
	h := newHarnessOver(t, in)
	h.client.markDegraded()
	if _, err := h.client.ReadPR(t.Context(), testRef(), testPR()); err == nil {
		t.Fatal("Setup: ReadPR against a renamed repository = nil, want the stale reference")
	}
	if held := h.client.BudgetState().ReadCache.Bytes; held == 0 {
		t.Fatal("Setup: the read's successor record was not held, so nothing could be revalidated")
	}

	_, err := h.client.CreateIssue(t.Context(), testRef(), forgeapi.NewIssue{Title: "t"})

	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Successor == nil || fe.Successor.Selector != "example/renamed" {
		t.Fatalf("CreateIssue against a renamed repository = %v, want the stale reference naming example/renamed", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(validators) != 2 || validators[1] != "" {
		t.Errorf("the successor reads carried If-None-Match %q, want the write's read to carry none", validators)
	}
	if hits := h.client.BudgetState().ReadCache.Hits; hits != 0 {
		t.Errorf("ReadCache.Hits after the write = %d, want 0", hits)
	}
}
