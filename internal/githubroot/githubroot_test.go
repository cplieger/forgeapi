package githubroot

import (
	"net/url"
	"testing"
)

// TestTheDocumentEndpointIsDerivedPerProduct holds the two addressings this family
// meets. The hosted instance serves its API on a host of its own, so the document sits
// under that host; an appliance serves a versioned REST root under its own web base and
// the document endpoint sits BESIDE that root rather than under it, so appending would
// address a path no instance serves.
func TestTheDocumentEndpointIsDerivedPerProduct(t *testing.T) {
	for _, test := range []struct {
		web  string
		api  string
		want string
	}{
		{web: "https://github.com", api: "https://api.github.com", want: "https://api.github.com/graphql"},
		{web: "https://www.github.com", api: "https://api.github.com", want: "https://api.github.com/graphql"},
		{web: "https://forge.example", api: "https://forge.example/api/v3", want: "https://forge.example/api/graphql"},
		{web: "http://127.0.0.1:8080", api: "http://127.0.0.1:8080/api/v3", want: "http://127.0.0.1:8080/api/graphql"},
	} {
		t.Run(test.web, func(t *testing.T) {
			web, err := url.Parse(test.web)
			if err != nil {
				t.Fatalf("Setup: parsing %q: %v", test.web, err)
			}
			if got := API(web); got != test.api {
				t.Errorf("API(%q) = %q, want %q", test.web, got, test.api)
			}
			api, err := url.Parse(test.api)
			if err != nil {
				t.Fatalf("Setup: parsing %q: %v", test.api, err)
			}
			if got := Document(api); got != test.want {
				t.Errorf("Document(%q) = %q, want %q", test.api, got, test.want)
			}
		})
	}
}
