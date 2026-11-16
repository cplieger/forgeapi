package github

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/creds"
)

// attempt is one request an attempt observer saw, with what forgeapi.Mutates read.
type attempt struct {
	route   string
	mutates bool
}

// attempts is an attempt observer recording every request it passes on.
type attempts struct {
	seen []attempt
	mu   sync.Mutex
}

func (a *attempts) observe(next http.RoundTripper) http.RoundTripper {
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		a.mu.Lock()
		a.seen = append(a.seen, attempt{route: r.Method + " " + r.URL.EscapedPath(), mutates: forgeapi.Mutates(r)})
		a.mu.Unlock()
		return next.RoundTrip(r)
	})
}

func (a *attempts) all() []attempt {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.seen)
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// GitHub sends every document as a POST, and a search page is a read.
func TestASearchPageIsNotMarkedAWrite(t *testing.T) {
	seen := &attempts{}
	h := newHarness(t, map[string]string{documentRoute: searchAnswer(0, nil, false)}, forgeapi.WithAttemptObserver(seen.observe))
	if _, err := h.client.ListMyPRs(t.Context()); err != nil {
		t.Fatalf("ListMyPRs = %v, want the page", err)
	}
	if got, want := seen.all(), []attempt{{route: documentRoute}}; !slices.Equal(got, want) {
		t.Errorf("the attempts of a search page = %v, want %v", got, want)
	}
}

func TestACreationWithLabelsMarksBothItsWrites(t *testing.T) {
	recorded := readCaptures(t)
	const create, labels = "POST /api/v3/repos/example/example/pulls", "POST /api/v3/repos/example/example/issues/1/labels"
	seen := &attempts{}
	h := newHarness(t, map[string]string{
		create: recorded.raw(t, "rest_pull"),
		labels: `[{"name":"example-label","color":"ededed"}]`,
	}, forgeapi.WithAttemptObserver(seen.observe))
	if _, err := h.client.CreatePR(t.Context(), testRef(), forgeapi.NewPullRequest{Title: "t", Labels: []string{"example-label"}}); err != nil {
		t.Fatalf("CreatePR = %v, want the pull request", err)
	}
	if got, want := seen.all(), []attempt{{route: create, mutates: true}, {route: labels, mutates: true}}; !slices.Equal(got, want) {
		t.Errorf("the attempts of CreatePR with a label = %v, want %v", got, want)
	}
}

// An App installation mints its token on the write's first request, and the mint
// changes no forge object, so only the write is marked. The App source refuses
// plaintext, so the instance answers in process behind an https address.
func TestAnAppTokenMintedForAWriteIsNotMarked(t *testing.T) {
	const mint, closed = "POST /api/v3/app/installations/42/access_tokens", "PATCH /api/v3/repos/example/example/pulls/1"
	answers := map[string]string{
		closed: readCaptures(t).raw(t, "rest_pull"),
		mint:   `{"token":"ghs_placeholder","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`,
	}
	instance := roundTrip(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "application/json")
		route := r.Method + " " + r.URL.EscapedPath()
		body, ok := answers[route]
		switch {
		case !ok:
			rec.WriteHeader(http.StatusInternalServerError)
			body = `{"message":"no route for ` + route + `"}`
		case route == mint:
			rec.WriteHeader(http.StatusCreated)
		}
		if _, err := rec.WriteString(body); err != nil {
			t.Errorf("Setup: writing the answer for %s: %v", route, err)
		}
		resp := rec.Result()
		resp.Request = r
		return resp, nil
	})
	seen := &attempts{}
	wire := []forgeapi.Option{forgeapi.WithWireTransport(instance), forgeapi.WithAttemptObserver(seen.observe)}
	signer, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Setup: generating the App key: %v", err)
	}
	key, err := creds.NewAppKey("example-app", signer)
	if err != nil {
		t.Fatalf("Setup: NewAppKey: %v", err)
	}
	conn := forgeapi.Connection{WebBaseURL: "https://github.example"}
	src, err := creds.NewAppSource(conn, key, creds.AppSourceConfig{
		Installation: creds.InstallationByID(42),
		Grant:        creds.AppGrant{Permissions: map[string]string{"pull_requests": "write"}},
	}, wire...)
	if err != nil {
		t.Fatalf("Setup: NewAppSource: %v", err)
	}
	client, err := New(conn, append(wire, forgeapi.WithCredentialSource(src))...)
	if err != nil {
		t.Fatalf("Setup: New: %v", err)
	}
	t.Cleanup(client.Close)
	if _, err := client.ClosePR(t.Context(), testRef(), testPR()); err != nil {
		t.Fatalf("ClosePR under an App installation = %v, want the pull request", err)
	}
	if got, want := seen.all(), []attempt{{route: mint}, {route: closed, mutates: true}}; !slices.Equal(got, want) {
		t.Errorf("the attempts of ClosePR under an App installation = %v, want %v", got, want)
	}
}
