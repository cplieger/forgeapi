package forgeapi_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/gitea"
)

// countingTransport counts the exchanges one hook position sees.
type countingTransport struct {
	next  http.RoundTripper
	count *atomic.Int64
}

func (c countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.count.Add(1)
	return c.next.RoundTrip(r)
}

// TestTheTwoObserversSitOnOppositeSidesOfTheRetryLayer holds the whole contract the
// two hooks have, which is their POSITION: the attempt hook sees every exchange that
// reached the wire and the request hook sees one call per logical request, so a
// connection that retried once shows two against one. Nothing else separates them, so
// a wiring swap between the two is invisible without this.
func TestTheTwoObserversSitOnOppositeSidesOfTheRetryLayer(t *testing.T) {
	var served atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if served.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			if _, err := io.WriteString(w, `{"message":"unavailable"}`); err != nil {
				t.Errorf("Setup: writing the first answer: %v", err)
			}
			return
		}
		if _, err := io.WriteString(w, `{"login":"observed"}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	defer srv.Close()

	var requests, attempts atomic.Int64
	client, err := gitea.New(forgeapi.Connection{WebBaseURL: srv.URL},
		forgeapi.WithWireTransport(srv.Client().Transport),
		forgeapi.WithCredentialSource(probeCredential{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		forgeapi.WithRequestObserver(func(next http.RoundTripper) http.RoundTripper {
			return countingTransport{next: next, count: &requests}
		}),
		forgeapi.WithAttemptObserver(func(next http.RoundTripper) http.RoundTripper {
			return countingTransport{next: next, count: &attempts}
		}),
	)
	if err != nil {
		t.Fatalf("Setup: gitea.New(%q): %v", srv.URL, err)
	}
	if _, err := client.Whoami(t.Context()); err != nil {
		t.Fatalf("Whoami over one retried request = %v, want the answer", err)
	}
	if got := served.Load(); got != 2 {
		t.Fatalf("the instance served %d request(s), want 2: the unavailable answer and the one that followed it", got)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("the attempt observer saw %d exchange(s), want 2: it is installed INSIDE the retry layer, so it sees one per attempt", got)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("the request observer saw %d exchange(s), want 1: it is installed OUTSIDE the retry layer, so it sees one per logical request", got)
	}
}

// TestARepositoryIdentifierRoundTripsAndAMalformedOneIsRefused holds the published
// decoder against the published encoder, and holds its refusal to the code it
// documents: a derived identifier is consumer-controlled input on its way into a
// request path, so a decode that let a bad one through would let it reach a URL.
func TestARepositoryIdentifierRoundTripsAndAMalformedOneIsRefused(t *testing.T) {
	ref := forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "example/example"}
	got, err := forgeapi.DecodeRepoRef(ref.Encode(), forgeapi.FamilyGitHub)
	if err != nil {
		t.Fatalf("DecodeRepoRef(%q, %q) = %v, want the reference", ref.Encode(), forgeapi.FamilyGitHub, err)
	}
	if got.Selector != ref.Selector || got.Family != forgeapi.FamilyGitHub || got.ID != ref.Encode() {
		t.Errorf("DecodeRepoRef(RepoRef.Encode()) = %+v, want the selector %q on %q with the identifier it decoded",
			got, ref.Selector, forgeapi.FamilyGitHub)
	}
	if got.DisplayPath != ref.Selector {
		t.Errorf("DecodeRepoRef(%q).DisplayPath = %q, want %q", ref.Encode(), got.DisplayPath, ref.Selector)
	}
	for _, id := range []string{
		"example/example",
		forgeapi.RepoIDPrefix + "zz",
		forgeapi.RepoIDPrefix + "2e2e",
		forgeapi.RepoIDPrefix + "612f622f63",
	} {
		_, err := forgeapi.DecodeRepoRef(id, forgeapi.FamilyGitHub)
		var fe *forgeapi.Error
		if !errors.As(err, &fe) || fe.Code != forgeapi.CodeRepoRefInvalid {
			t.Errorf("DecodeRepoRef(%q, %q) = %v, want %q", id, forgeapi.FamilyGitHub, err, forgeapi.CodeRepoRefInvalid)
		}
	}
}
