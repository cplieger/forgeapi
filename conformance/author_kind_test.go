package conformance

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// The paths the author's kind is asserted at, on a list row and on a single answer.
const (
	rowAuthorKind = "Items[].AuthorKind"
	authorKind    = "AuthorKind"
)

// authorKindKey is the key and person's value each product's published shape types
// an author under, by the object that holds the login: GitHub's document Actor
// (__typename) and REST user (type), GitLab's user (bot) and Gitea's user (type).
// The recorded fixtures were trimmed of them. Forgejo's user carries none, and
// neither does GitLab's issue author, measured 2026-10-11, so those rows are served
// as recorded.
func authorKindKey(p spec.Product, method, holder string) (string, any, bool) {
	switch {
	case p == spec.GitHub && holder == "author":
		return "__typename", "User", true
	case p == spec.GitHub && holder == "user", p == spec.Gitea && holder == "user":
		return "type", "User", true
	case p == spec.GitLab && holder == "author" && strings.HasPrefix(method, "PullRequests."):
		return "bot", false, true
	}
	return "", nil, false
}

// typeAuthors adds the product's author type to every object in v that names the
// fixture's author.
func typeAuthors(v any, p spec.Product, method string) {
	switch node := v.(type) {
	case map[string]any:
		for key, child := range node {
			if obj, ok := child.(map[string]any); ok && (obj["login"] == author || obj["username"] == author) {
				if k, val, ok := authorKindKey(p, method, key); ok {
					obj[k] = val
				}
			}
			typeAuthors(child, p, method)
		}
	case []any:
		for _, child := range node {
			typeAuthors(child, p, method)
		}
	}
}

// TestEveryProductsAuthorCarriesItsKind holds each pull request and issue answer to
// the author kind its product's shape states: a person's account where the author
// object types it, and the unknown the table names where it does not.
func TestEveryProductsAuthorCarriesItsKind(t *testing.T) {
	for _, e := range spec.Table {
		if !strings.HasPrefix(e.Method, "PullRequests.") && !strings.HasPrefix(e.Method, "Issues.") {
			continue
		}
		op, ok := operationFor(e.Method)
		if !ok || !strings.Contains(strings.Join(op.rowArms, ","), "AuthorKind") {
			continue
		}
		t.Run(caseName(e), func(t *testing.T) {
			if pending(e) {
				t.Skip(pendingReason(e))
			}
			f, err := loadFixture(e.Product, e.Method)
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			for i := range f.Routes {
				var body any
				d := json.NewDecoder(bytes.NewReader(f.Routes[i].Body))
				d.UseNumber()
				if d.Decode(&body) != nil {
					continue
				}
				typeAuthors(body, e.Product, e.Method)
				if f.Routes[i].Body, err = json.Marshal(body); err != nil {
					t.Fatalf("Setup: encoding %s: %v", f.path, err)
				}
			}
			srv, _ := newFixtureServer(t, f)
			conn := forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL + apiRoot(e.Product)}
			client, err := guard(func() (any, error) { return newClient(e.Product, conn, offlineOptions(srv)...) })
			if err != nil {
				t.Fatalf("Setup: %s client: %v", e.Product, err)
			}
			s := canonicalSubject(e.Product)
			if prelude := preludeMethod(e, op); prelude != "" {
				runPrelude(t, e, prelude, client, s)
			}
			got, err := guard(func() (any, error) { return op.invoke(t.Context(), client, s) })
			if err != nil {
				t.Fatalf("%s on %s = error %v, want its answer", e.Method, e.Product, err)
			}
			var c check
			switch g := got.(type) {
			case forgeapi.Page[forgeapi.PullRequest]:
				c = eq(rowAuthorKind, first(g.Items).AuthorKind, forgeapi.AccountKindUser)
			case forgeapi.Page[forgeapi.Issue]:
				c = eq(rowAuthorKind, first(g.Items).AuthorKind, forgeapi.AccountKindUser)
			case forgeapi.PullRequest:
				c = eq(authorKind, g.AuthorKind, forgeapi.AccountKindUser)
			case forgeapi.Issue:
				c = eq(authorKind, g.AuthorKind, forgeapi.AccountKindUser)
			default:
				t.Fatalf("%s on %s answered %T, which this case reads no author of", e.Method, e.Product, got)
			}
			failures, _, _ := cellFailures(e, []check{c})
			for _, f := range failures {
				t.Error(f)
			}
		})
	}
}
