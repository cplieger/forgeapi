package conformance

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// clientAt is one connection to an instance this suite stood up, at the web base
// and the API base given, built with the family's constructor as every offline
// case's client is.
func clientAt(t *testing.T, p spec.Product, srv *httptest.Server, web, api string) any {
	t.Helper()
	client, err := guard(func() (any, error) {
		return newClient(p, forgeapi.Connection{WebBaseURL: web, APIBaseURL: api}, offlineOptions(srv)...)
	})
	if err != nil {
		t.Fatalf("Setup: %s client at %s: %v", p, api, err)
	}
	return client
}

// A continuation names the connection that minted it, so the same list on another
// instance of the same product, whose listing is another collection although the
// repository, the state, the scope and the bound are spelled the same, refuses it.
func TestAPageNumberedContinuationOnAnotherInstanceOfTheProductIsRefused(t *testing.T) {
	for _, p := range spec.Products {
		for _, e := range pageNumberedLists(t, p) {
			t.Run(caseName(e), func(t *testing.T) {
				s := canonicalSubject(p)
				began := newPagedInstance(t, p, mintedRows, 50)
				next := mintContinuation(t, e, pagedClient(t, p, began.serve(t)), began, s)

				elsewhere := newPagedInstance(t, p, mintedRows, 50)
				_, window, err := callList(t, e, pagedClient(t, p, elsewhere.serve(t)), elsewhere.rec, s,
					forgeapi.WithPageBound(mintBound), forgeapi.WithAfter(next))
				checkCursorRefused(t, e.Method+" on another "+string(p)+" instance", err, elsewhere.listRequests(window))
			})
		}
	}
}

// Gitea and Forgejo are products of one family, so a continuation minted on an
// instance of one names the family the other's client is, and only the connection
// tells the listings apart.
func TestAPageNumberedContinuationHandedBetweenTheGiteaFamilysProductsIsRefused(t *testing.T) {
	for _, minter := range giteaFamilyProducts {
		for _, resumer := range giteaFamilyProducts {
			if minter == resumer {
				continue
			}
			resumable := map[string]spec.Entry{}
			for _, e := range pageNumberedLists(t, resumer) {
				resumable[e.Method] = e
			}
			for _, minted := range pageNumberedLists(t, minter) {
				other, ok := resumable[minted.Method]
				if !ok {
					continue
				}
				t.Run(caseName(minted)+"_to_"+string(resumer), func(t *testing.T) {
					began := newPagedInstance(t, minter, mintedRows, 50)
					next := mintContinuation(t, minted, pagedClient(t, minter, began.serve(t)), began, canonicalSubject(minter))

					elsewhere := newPagedInstance(t, resumer, mintedRows, 50)
					_, window, err := callList(t, other, pagedClient(t, resumer, elsewhere.serve(t)), elsewhere.rec, canonicalSubject(resumer),
						forgeapi.WithPageBound(mintBound), forgeapi.WithAfter(next))
					checkCursorRefused(t, other.Method+" on "+string(resumer)+" handed the continuation "+string(minter)+" minted", err, elsewhere.listRequests(window))
				})
			}
		}
	}
}

// relativeRoots are two roots one host serves two instances under.
var relativeRoots = [2]string{"/first", "/second"}

// twoRootsOneHost serves one paged instance under each of the two relative roots of
// one host, which share a scheme, a host and a port.
func twoRootsOneHost(t *testing.T, pi *pagedInstance) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for _, root := range relativeRoots {
		mux.Handle(root+"/", http.StripPrefix(root, pi.handler(t)))
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// A connection is its API base, path included, so two instances one host serves
// under two relative roots are two connections, and a continuation minted under one
// root is refused under the other while it resumes under its own.
func TestAPageNumberedContinuationUnderAnotherRelativeRootOfOneHostIsRefused(t *testing.T) {
	for _, p := range spec.Products {
		for _, e := range pageNumberedLists(t, p) {
			t.Run(caseName(e), func(t *testing.T) {
				s := canonicalSubject(p)
				pi := newPagedInstance(t, p, mintedRows, 50)
				srv := twoRootsOneHost(t, pi)
				at := func(root string) any {
					return clientAt(t, p, srv, srv.URL+root, srv.URL+root+apiRoot(p))
				}
				next := mintContinuation(t, e, at(relativeRoots[0]), pi, s)

				_, window, err := callList(t, e, at(relativeRoots[1]), pi.rec, s, forgeapi.WithPageBound(mintBound), forgeapi.WithAfter(next))
				checkCursorRefused(t, e.Method+" on "+string(p)+" under "+relativeRoots[1]+", minted under "+relativeRoots[0], err, pi.listRequests(window))

				_, window, err = callList(t, e, at(relativeRoots[0]), pi.rec, s, forgeapi.WithPageBound(mintBound), forgeapi.WithAfter(next))
				pages := pi.listRequests(window)
				if err != nil || len(pages) == 0 || pages[0].query.Get("page") != "2" {
					t.Errorf("%s on %s under %s, resuming its own continuation = error %v, sent [%s], want the second page", e.Method, p, relativeRoots[0], err, describeRequests(window))
				}
			})
		}
	}
}

// Two spellings of one API base name one connection, its scheme folded and a
// trailing slash trimmed, so a continuation resumes on a client built for the same
// instance under another spelling, which is what lets it survive a restart.
func TestAPageNumberedContinuationResumesOnAnotherSpellingOfItsConnection(t *testing.T) {
	spellings := map[string]func(base string) string{
		"trailing_slash": func(base string) string { return base + "/" },
		"scheme_in_capitals": func(base string) string {
			return "HTTP" + strings.TrimPrefix(base, "http")
		},
	}
	for _, p := range spec.Products {
		for _, e := range pageNumberedLists(t, p) {
			for name, spell := range spellings {
				t.Run(caseName(e)+"_"+name, func(t *testing.T) {
					s := canonicalSubject(p)
					pi := newPagedInstance(t, p, mintedRows, 50)
					srv := pi.serve(t)
					next := mintContinuation(t, e, pagedClient(t, p, srv), pi, s)

					respelled := clientAt(t, p, srv, spell(srv.URL), spell(srv.URL+apiRoot(p)))
					_, window, err := callList(t, e, respelled, pi.rec, s, forgeapi.WithPageBound(mintBound), forgeapi.WithAfter(next))
					pages := pi.listRequests(window)
					if err != nil || len(pages) == 0 || pages[0].query.Get("page") != "2" {
						t.Errorf("%s on %s at %s, resuming a continuation minted at %s = error %v, sent [%s], want the second page", e.Method, p, spell(srv.URL+apiRoot(p)), srv.URL+apiRoot(p), err, describeRequests(window))
					}
				})
			}
		}
	}
}
