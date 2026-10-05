package conformance

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/cplieger/forgeapi/internal/spec"
)

// TestEveryCaseHasALoadableFixture holds the fixture set to the table's shape and to
// the recording rules: one fixture per case, each naming the capture it was cut
// from and the date that capture was read, each declaring at least one route, and
// each route carrying a body the JSON decoder accepts.
//
// It runs with no client, so it reports a fixture defect on a tree where every
// operation panics, which is the tree this suite was written against.
func TestEveryCaseHasALoadableFixture(t *testing.T) {
	for _, e := range spec.Table {
		t.Run(caseName(e), func(t *testing.T) {
			f, err := loadFixture(e.Product, e.Method)
			if err != nil {
				t.Fatalf("loadFixture(%s, %s) = error %v, want a fixture", e.Product, e.Method, err)
			}
			for i, w := range f.Routes {
				switch {
				case w.GraphQL && (w.Method != "" || w.Path != ""):
					t.Errorf("%s route %d is a document and also names %s %q, want one or the other", f.path, i, w.Method, w.Path)
				case !w.GraphQL && (w.Method == "" || (w.Path == "" && len(w.Paths) == 0)):
					t.Errorf("%s route %d names no method or no path, want both", f.path, i)
				}
				if w.Status < 200 || w.Status > 299 {
					t.Errorf("%s route %d answers %d, want a 2xx: a case asserting the normalized contract is served the successful arm", f.path, i, w.Status)
				}
				if !json.Valid(w.Body) {
					t.Errorf("%s route %d carries a body the JSON decoder refuses", f.path, i)
				}
			}
		})
	}
}

// TestEveryFixtureServesARouteTheTableNames cross-checks the two artefacts that can
// disagree silently: the routes a fixture serves and the route the table publishes.
//
// It holds the WEAKER of the two possible claims, that at least one named route is
// served, because a price stated as a range names both arms and a fixture serves
// one: GitHub's commit status names a document and a two-endpoint REST pair, and
// the fixture serves the document, which is the arm the minimum prices.
func TestEveryFixtureServesARouteTheTableNames(t *testing.T) {
	for _, e := range spec.Table {
		t.Run(caseName(e), func(t *testing.T) {
			named := tablePaths(&e)
			f, err := loadFixture(e.Product, e.Method)
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			var served []string
			for _, w := range f.Routes {
				if w.GraphQL {
					served = append(served, "<a GraphQL document>")
					continue
				}
				served = append(served, w.Path)
				served = append(served, w.Paths...)
			}
			if len(named) == 0 {
				// The table names a document, or names no route at all for an
				// operation detection refuses and one the governor answers from a
				// signal that rode an earlier response.
				return
			}
			if strings.Contains(e.Exercises, "document") && slices.Contains(served, "<a GraphQL document>") {
				// Three rows name a document AND the REST route a connection
				// that refuses the document degrades to. The document is the arm
				// the minimum price buys, so the fixture serves that one.
				return
			}
			if !slices.ContainsFunc(named, func(p string) bool {
				return slices.ContainsFunc(served, func(s string) bool { return pathMatches(s, p) })
			}) {
				t.Errorf("%s on %s serves %v, want at least one of the routes the table names in %q: %v",
					e.Method, e.Product, served, e.Exercises, named)
			}
		})
	}
}

// detectionHeaders is the family-detection header precedence, per product: the
// name a product sends on ordinary traffic, and the names it does not send at all.
// The order is Forgejo's, Gitea's, then GitLab's and GitHub's, and it is
// load-bearing only for the two products that still send one.
//
// This is the suite's own statement of that rule, as every expectation here is,
// and it is what turns a precedence over four names into something a fixture can
// be held to.
var detectionHeaders = map[spec.Product]struct {
	sends  string
	silent []string
}{
	spec.GitHub:  {sends: "X-GitHub-Request-Id"},
	spec.GitLab:  {sends: "X-Gitlab-Meta"},
	spec.Gitea:   {silent: []string{"X-Forgejo-Version", "X-Gitea-Version"}},
	spec.Forgejo: {silent: []string{"X-Forgejo-Version", "X-Gitea-Version"}},
}

// TestDetectionHeadersRideTheConnectionRead holds those four names to the two
// artefacts that can carry them: the connection read's own fixture and the route
// the table publishes for it.
//
// A name a product sends is served by that product's fixture, so a family reading
// it offline reads what the instance sent; a name neither Gitea nor Forgejo sends
// is absent from both fixtures, so a family cannot detect that family from a header
// the products do not carry and pass here. Either way the table's own row names the
// header, which is what keeps the rule and its referents in one place.
func TestDetectionHeadersRideTheConnectionRead(t *testing.T) {
	for _, e := range spec.Table {
		if e.Method != "Capabilities.ConnectionCaps" {
			continue
		}
		t.Run(string(e.Product), func(t *testing.T) {
			f, err := loadFixture(e.Product, e.Method)
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			served := map[string]bool{}
			for _, w := range f.Routes {
				for name := range w.Headers {
					served[strings.ToLower(name)] = true
				}
			}
			want := detectionHeaders[e.Product]
			if want.sends != "" {
				if !served[strings.ToLower(want.sends)] {
					t.Errorf("%s serves no %s header, want one: this product sends it on ordinary traffic and detection reads it first",
						f.path, want.sends)
				}
				checkTableNamesHeader(t, e, want.sends)
			}
			for _, name := range want.silent {
				if served[strings.ToLower(name)] {
					t.Errorf("%s serves a %s header, want none: this product does not send it, which is why this family detects from the version body",
						f.path, name)
				}
				checkTableNamesHeader(t, e, name)
			}
		})
	}
}

// checkTableNamesHeader holds the table to naming the header the suite asserts, the
// way the route cross-check holds it to naming the route: a spelling only this
// package carries would publish nothing to a reader of the generated documentation.
func checkTableNamesHeader(t *testing.T, e spec.Entry, name string) {
	t.Helper()
	if !strings.Contains(strings.ToLower(e.Exercises), strings.ToLower(name)) {
		t.Errorf("%s on %s names %q in no part of %q, want the header spelled where the rule over it is published",
			e.Method, e.Product, name, e.Exercises)
	}
}

// tableDocument is the name of the document one table row names, where it names one.
//
// The row spells a document as a NAME rather than as a path, since neither GraphQL
// product has one endpoint path to fix, and that name is the operation name the
// request carries. The row's FIRST document is the one a warm connection sends, which
// is the arm every offline case drives. A qualifier between the name and the noun, as
// the search document's row carries, belongs to the noun rather than to the name.
func tableDocument(e *spec.Entry) (string, bool) {
	fields := strings.Fields(e.Exercises)
	for i, field := range fields {
		if strings.TrimRight(field, ",.;") != "document" {
			continue
		}
		for back := i - 1; back >= 0; back-- {
			name := strings.TrimRight(fields[back], ",.;")
			if name == "search" {
				continue
			}
			if name == "" || !unicode.IsUpper(rune(name[0])) {
				break
			}
			return name, true
		}
	}
	return "", false
}

// tablePaths is every request path one table row names, with the row's own
// placeholders resolved onto the canonical subject. A row that names a document
// rather than a route yields nothing, and so does a row that names no route at all.
func tablePaths(e *spec.Entry) []string {
	var out []string
	for field := range strings.FieldsSeq(e.Exercises) {
		if !strings.HasPrefix(field, "/") {
			continue
		}
		path := strings.TrimRight(field, ",.;")
		if i := strings.IndexByte(path, '?'); i >= 0 {
			path = path[:i]
		}
		out = append(out, resolvePlaceholders(path, e.Product))
	}
	return out
}

// resolvePlaceholders rewrites the table's own path placeholders onto the canonical
// subject, so a table row and a fixture route can be compared as strings. The run
// id becomes the any-segment wildcard, because nothing this library publishes pins
// which run a re-run addresses.
func resolvePlaceholders(path string, p spec.Product) string {
	id := selector
	if p == spec.GitLab {
		id = strings.ReplaceAll(selector, "/", "%2F")
	}
	return strings.NewReplacer(
		"{owner}", owner,
		"{repo}", repository,
		"{number}", "1",
		"{iid}", "1",
		"{id}", id,
		"{sha}", headSHA,
		"{ref}", headSHA,
		"{run}", "{}",
	).Replace(path)
}
