package gitea

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// detectServer answers the two reads detection makes and nothing else: the version
// body, with the header a product would name itself with where the case sets one, and
// the API document the case supplies, which it can withhold entirely as an instance
// with the document switched off does.
func detectServer(t *testing.T, version, header, document string) *instance {
	t.Helper()
	in := newInstance()
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		in.mu.Lock()
		in.requests = append(in.requests, r.Method+" "+path)
		in.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case path == "/api/v1/version":
			if header != "" {
				w.Header().Set(header, "1.27.0")
			}
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
	t.Cleanup(in.server.Close)
	return in
}

// TestTheFamilyIsEstablishedFromAMarkerRatherThanFromAVersionKey holds what detection
// has to prove before this package serves a connection at all.
//
// Every route, every enumeration table and every error mapping below detection is one
// family's, so a server identified on nothing but the presence of a version string is
// a server whose answers are then read as this family's. Any service can answer that
// route with that key. So the release FORM both products report is the first witness
// and one of three product marks is the second: the Gitea marker inside the body,
// which names the product that is not Gitea; a version header, which neither sends
// today and either could restore; or the API document's own TITLE, which names the
// product on both.
//
// Each of the three NAMES a product, and that is what makes it a witness. A document
// any service can serve names nobody, so its presence is not the second witness and
// the last case here is what holds that.
func TestTheFamilyIsEstablishedFromAMarkerRatherThanFromAVersionKey(t *testing.T) {
	for _, test := range []struct {
		name     string
		version  string
		header   string
		document string
		detected bool
	}{
		{
			name:     "the_marker_in_the_version_body_names_the_other_product",
			version:  "16.0.0-dev-753-6bcc6da0+gitea-1.22.0",
			detected: true,
		},
		{
			name:     "a_version_header_names_a_product",
			version:  "1.27.0",
			header:   headerGiteaVersion,
			detected: true,
		},
		{
			name:     "the_api_documents_title_is_the_second_witness_where_nothing_else_names_a_product",
			version:  "1.27.0+dev-954-g1f3981a301",
			document: giteaDoc,
			detected: true,
		},
		{
			name:     "the_other_products_document_names_it_just_as_well",
			version:  "16.0.0-dev-753-6bcc6da0",
			document: forgejoDoc,
			detected: true,
		},
		{
			name:    "a_release_alone_establishes_nothing",
			version: "1.27.0",
		},
		{
			name:     "a_version_that_is_not_a_release_is_no_witness_at_all",
			version:  "not-a-gitea-family-version",
			document: giteaDoc,
		},
		{
			name:     "an_empty_version_body_is_no_witness_either",
			version:  "",
			document: giteaDoc,
		},
		{
			// The finding this case exists for: a release number and a
			// document are two observations of which neither identifies
			// anybody, and a document is a thing any service can serve.
			name:     "a_document_that_names_no_product_is_no_witness",
			version:  "1.27.0",
			document: anonymousDoc,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarnessOver(t, detectServer(t, test.version, test.header, test.document), time.Now)
			caps, err := h.client.ConnectionCaps(t.Context())
			if test.detected {
				if err != nil {
					t.Fatalf("ConnectionCaps = %v, want the capabilities: this instance carries a mark of the family", err)
				}
				if len(caps.Caps) == 0 {
					t.Error("ConnectionCaps answered no capability, want the connection scope's own")
				}
				return
			}
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) {
				t.Fatalf("ConnectionCaps = %+v, %v, want a refusal: nothing here identifies the family", caps, err)
			}
			if fe.Code != forgeapi.CodeFamilyUndetected {
				t.Errorf("ConnectionCaps = code %q, want %q", fe.Code, forgeapi.CodeFamilyUndetected)
			}
			if fe.Status != http.StatusOK {
				t.Errorf("ConnectionCaps = status %d, want %d: this refusal made a request and identified no family, so it carries the real status", fe.Status, http.StatusOK)
			}
			// A refusal that cached capability state would answer the second
			// caller as if the family had been established.
			if _, again := h.client.ConnectionCaps(t.Context()); again == nil {
				t.Error("a second ConnectionCaps = nil, want the same refusal: nothing is cached for a family that was never established")
			}
		})
	}
}

// Setup reads the instance's stated maximum only once the family is established, so
// a server setup refuses as undetected is never asked for its settings: every route
// below detection is one family's, the settings route included.
func TestSetupAsksAServerItRefusesAsUndetectedForNoSettings(t *testing.T) {
	h := newHarnessOver(t, detectServer(t, "1.27.0", "", anonymousDoc), time.Now)
	h.client.maxItems = 0

	_, err := h.client.ConnectionCaps(t.Context())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeFamilyUndetected {
		t.Fatalf("Setup: ConnectionCaps on a server naming no product = %v, want code %q", err, forgeapi.CodeFamilyUndetected)
	}
	if sent := h.instance.arrived(); slices.Contains(sent, "GET /api/v1"+settingsPath) {
		t.Errorf("ConnectionCaps on a server it refused as undetected sent %v, want no settings read: the family was never established", sent)
	}
}

// A list on a connection holding no stated maximum waits for the connection's one
// resolution owner before its first page, and it waits no longer than its own
// context allows: while setup holds the owner across a version read that has not
// answered, a list with a short deadline ends at that deadline with the context's
// own error rather than when setup's read finishes.
func TestListWaitingOnTheResolutionOwner_ends_at_its_own_deadline(t *testing.T) {
	versionAsked := make(chan struct{})
	release := make(chan struct{})
	var asked sync.Once
	in := newInstance()
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == "/api/v1/version" {
			asked.Do(func() { close(versionAsked) })
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		if _, err := w.Write([]byte(`{"message":"not found"}`)); err != nil {
			t.Errorf("Setup: writing the absence of %s: %v", r.URL.EscapedPath(), err)
		}
	}))
	t.Cleanup(in.server.Close)
	t.Cleanup(func() { close(release) })
	h := newHarnessOver(t, in, time.Now)
	h.client.maxItems = 0

	go func() { _, _ = h.client.ConnectionCaps(context.WithoutCancel(t.Context())) }()
	select {
	case <-versionAsked:
	case <-time.After(5 * time.Second):
		t.Fatal("Setup: ConnectionCaps sent no version read within 5s, so no setup holds the resolution owner")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	listed := make(chan error, 1)
	go func() {
		_, err := h.client.ListReleases(ctx, testRef())
		listed <- err
	}()
	select {
	case err := <-listed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("ListReleases with a 50ms deadline behind a setup read that has not answered = error %v, want %v", err, context.DeadlineExceeded)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListReleases with a 50ms deadline behind a setup read that has not answered has not returned after 5s, want it to end at its own deadline")
	}
}
