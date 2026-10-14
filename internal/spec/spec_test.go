package spec

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestEveryProductStatesItsConditionalReads(t *testing.T) {
	for _, product := range Products {
		var rows []Revalidation
		for _, r := range Revalidations {
			if r.Product == product {
				rows = append(rows, r)
			}
		}
		if len(rows) != 1 {
			t.Errorf("Revalidations holds %d row(s) for %s, want 1", len(rows), product)
			continue
		}
		r := rows[0]
		if r.Validators == "" || r.Documents == "" || r.Says == "" || r.NotModifiedPrice < 0 || r.NotModifiedPrice > 1 {
			t.Errorf("the %s row = %+v, want every fact stated and a price of 0 or 1", product, r)
		}
	}
}

// officialSources are the documentation sites and source repositories of the four
// products and of the components their answers come from; a cache fact cites one.
var officialSources = []string{
	"docs.github.com/", "docs.gitlab.com/", "github.com/rack/rack/",
	"docs.gitea.com/", "github.com/go-gitea/gitea/", "forgejo.org/docs/", "codeberg.org/forgejo/forgejo/",
}

var markdownLink = regexp.MustCompile(`\]\((https://[^)\s]+)\)`)

// TestEveryCacheFactCitesAnOfficialSource holds the per-product facts the read cache
// follows to a citation: each conditional-read fact links the product's own
// documentation or source.
func TestEveryCacheFactCitesAnOfficialSource(t *testing.T) {
	check := func(name, fact string) {
		t.Helper()
		links := markdownLink.FindAllStringSubmatch(fact, -1)
		if len(links) == 0 {
			t.Errorf("%s = %q, want it to link an official source", name, fact)
		}
		for _, link := range links {
			u, err := url.Parse(link[1])
			official := err == nil && slices.ContainsFunc(officialSources, func(prefix string) bool {
				return strings.HasPrefix(u.Host+u.Path, prefix)
			})
			if !official {
				t.Errorf("%s links %s, want one of the official sources %v", name, link[1], officialSources)
			}
		}
	}
	for _, r := range Revalidations {
		check(string(r.Product)+" validators", r.Validators)
		check(string(r.Product)+" documents", r.Documents)
		check(string(r.Product)+" not-modified price", r.Says)
	}
}
