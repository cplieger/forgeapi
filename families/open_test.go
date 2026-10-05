package families_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/families"
)

// giteaDocument is the API document of an instance of the family, trimmed to the field
// detection reads it for: the title, which is the product mark. It is the title
// gitea.com serves.
const giteaDocument = `{"info":{"title":"Gitea API"},"paths":{}}`

// openCredential is the source every client here is built with, because injection is
// mandatory and a client without one is refused before any detection runs.
type openCredential struct{}

func (openCredential) Token(context.Context) (string, error) { return "open-placeholder", nil }
func (openCredential) Kind() forgeapi.CredKind               { return forgeapi.CredKindStaticPAT }
func (openCredential) State() forgeapi.CredState             { return forgeapi.CredValid }

// serverAnswering is an instance answering the two reads detection makes, with the
// version body the case names and the API document it supplies, withheld entirely
// where that document is empty.
func serverAnswering(t *testing.T, version, document string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch path := r.URL.EscapedPath(); {
		case path == "/api/v1/version":
			if _, err := w.Write([]byte(`{"version":"` + version + `"}`)); err != nil {
				t.Errorf("Setup: writing the version body: %v", err)
			}
		case path == "/swagger.v1.json" && document != "":
			if _, err := w.Write([]byte(document)); err != nil {
				t.Errorf("Setup: writing the document: %v", err)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			if _, err := w.Write([]byte(`{"message":"not found"}`)); err != nil {
				t.Errorf("Setup: writing the absence of %s: %v", path, err)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestOpenRefusesAServerThatIdentifiesNoFamily holds the factory's own contract at the
// one place a consumer meets it.
//
// Open answers a family, and everything a caller then does with the client it returns
// is addressed, mapped and enumerated per family. So a server whose family cannot be
// established is a named refusal a connect dialog can act on, never a guess: a service
// answering the version route with any string is not evidence of this family, and the
// positive arm is what keeps the refusal from being a client that never opens.
func TestOpenRefusesAServerThatIdentifiesNoFamily(t *testing.T) {
	for _, test := range []struct {
		name     string
		version  string
		document string
		want     forgeapi.Family
	}{
		{
			name:     "an_instance_of_the_family",
			version:  "1.27.0+dev-954-g1f3981a301",
			document: giteaDocument,
			want:     forgeapi.FamilyGitea,
		},
		{
			// Each negative arm is refused by ONE of the two witnesses, so
			// each is sensitive to that witness alone: this one serves a
			// document naming the product, and is refused on the version
			// body's form.
			name:     "a_service_answering_an_arbitrary_version",
			version:  "not-a-gitea-family-version",
			document: giteaDocument,
			want:     forgeapi.FamilyUnknown,
		},
		{
			// And this one answers the release form, so only the second
			// witness can refuse it.
			name:    "a_service_answering_a_release_and_nothing_else",
			version: "1.27.0",
			want:    forgeapi.FamilyUnknown,
		},
		{
			// A document ANY service can serve is not the second witness
			// either: two observations of which neither names a product
			// identify nobody.
			name:     "a_service_answering_a_document_that_names_no_product",
			version:  "1.27.0",
			document: `{"paths":{}}`,
			want:     forgeapi.FamilyUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := serverAnswering(t, test.version, test.document)
			core, family, err := families.Open(t.Context(), forgeapi.Connection{WebBaseURL: srv.URL},
				forgeapi.WithWireTransport(srv.Client().Transport),
				forgeapi.WithCredentialSource(openCredential{}),
				forgeapi.WithPlaintextHTTP(true),
				forgeapi.WithPrivateAddresses(true),
				forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
			)
			if family != test.want {
				t.Errorf("families.Open = family %v, want %v", family, test.want)
			}
			if test.want != forgeapi.FamilyUnknown {
				if err != nil {
					t.Fatalf("families.Open = %v, want a Core", err)
				}
				if core == nil {
					t.Error("families.Open answered no Core, want one")
				}
				return
			}
			if core != nil {
				t.Errorf("families.Open answered %T beside the refusal, want nothing: a client for a family nobody established would address, map and enumerate as that family", core)
			}
			var fe *forgeapi.Error
			if !errors.As(err, &fe) {
				t.Fatalf("families.Open = %v, want a *forgeapi.Error", err)
			}
			if fe.Code != forgeapi.CodeFamilyUndetected {
				t.Errorf("families.Open = code %q, want %q: a consumer's connect dialog names the remedy from the code rather than from a message", fe.Code, forgeapi.CodeFamilyUndetected)
			}
			if fe.Family != forgeapi.FamilyUnknown {
				t.Errorf("families.Open = family %v on the error, want %v", fe.Family, forgeapi.FamilyUnknown)
			}
			if fe.Status != http.StatusOK {
				t.Errorf("families.Open = status %d, want %d: this refusal made a request and identified no family, so it carries the real status", fe.Status, http.StatusOK)
			}
		})
	}
}
