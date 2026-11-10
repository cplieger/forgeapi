package creds_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/creds"
)

// appWeb is the GitHub Enterprise Server the App tests address, whose REST
// root is /api/v3 under it.
const appWeb = "https://github.example"

// apiPrefix is the REST root's path under appWeb.
const apiPrefix = "/api/v3"

// appCall is one request the fake GitHub received, as the assertions read it.
type appCall struct {
	header  http.Header
	query   url.Values
	method  string
	path    string
	escaped string
	body    []byte
}

// appReply is what the fake GitHub answers one request with: after delay,
// the status, or a dropped connection where drop is set. A request whose
// context ends during the delay fails with the context's error, as a real
// connection does.
type appReply struct {
	header http.Header
	body   string
	status int
	delay  time.Duration
	drop   bool
}

// fakeGitHub answers in process, so a test runs inside a synctest bubble: it
// records every request and answers each with what reply returns for it.
type fakeGitHub struct {
	t     *testing.T
	reply func(n int, c appCall) appReply
	seen  []appCall
	mu    sync.Mutex
}

func newFakeGitHub(t *testing.T, reply func(n int, c appCall) appReply) *fakeGitHub {
	return &fakeGitHub{t: t, reply: reply}
}

func (g *fakeGitHub) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			g.t.Errorf("Setup: reading the request body: %v", err)
		}
		body = raw
	}
	c := appCall{header: r.Header.Clone(), query: r.URL.Query(), method: r.Method, path: r.URL.Path, escaped: r.URL.EscapedPath(), body: body}
	g.mu.Lock()
	g.seen = append(g.seen, c)
	n := len(g.seen)
	g.mu.Unlock()
	rep := g.reply(n, c)
	timer := time.NewTimer(rep.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
	if rep.drop {
		return nil, io.ErrUnexpectedEOF
	}
	header := http.Header{"Content-Type": {"application/json"}}
	maps.Copy(header, rep.header)
	return &http.Response{
		StatusCode: rep.status, Header: header, Body: io.NopCloser(strings.NewReader(rep.body)),
		ContentLength: int64(len(rep.body)), Request: r,
	}, nil
}

// calls is what the fake has received so far.
func (g *fakeGitHub) calls() []appCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.seen)
}

// mints is how many token requests the fake has received.
func (g *fakeGitHub) mints() int {
	n := 0
	for _, c := range g.calls() {
		if c.method == http.MethodPost && strings.HasSuffix(c.path, "/access_tokens") {
			n++
		}
	}
	return n
}

// installationPath is the token endpoint of installation id.
func installationPath(id int64) string {
	return fmt.Sprintf("%s/app/installations/%d/access_tokens", apiPrefix, id)
}

// minted is GitHub's answer to a token request: token n, expiring life after
// the fake's clock reads it, in whole seconds as GitHub states it.
func minted(n int, life time.Duration) appReply {
	expiry := time.Now().Add(life).UTC().Truncate(time.Second).Format(time.RFC3339)
	return appReply{status: http.StatusCreated, body: fmt.Sprintf(`{"token":"ghs_token%d","expires_at":%q}`, n, expiry)}
}

// refusal is a GitHub refusal answering message, with header.
func refusal(status int, message string, header http.Header) appReply {
	body, _ := json.Marshal(map[string]string{"message": message})
	return appReply{status: status, body: string(body), header: header}
}

// mintsAt answers every token request for installation id with a fresh
// token lasting life, and fails the test on any other request.
func mintsAt(t *testing.T, id int64, life time.Duration) func(int, appCall) appReply {
	return func(n int, c appCall) appReply {
		if c.method != http.MethodPost || c.path != installationPath(id) {
			t.Errorf("%s %s reached GitHub, want only the token request of installation %d", c.method, c.path, id)
			return refusal(http.StatusInternalServerError, "unexpected", nil)
		}
		return minted(n, life)
	}
}

// appKey is the App key every source test signs with.
func appKey(t *testing.T) *creds.AppKey {
	t.Helper()
	key, err := creds.NewAppKey("Iv23liAbCdEf012345", appPrivateKey(t))
	if err != nil {
		t.Fatalf("Setup: NewAppKey: %v", err)
	}
	return key
}

// readGrant is a narrowed grant the tests ask for.
func readGrant() creds.AppGrant {
	return creds.AppGrant{Permissions: map[string]string{"metadata": "read"}}
}

// appOptions are what every source test passes: the fake as the wire, a
// silent logger and whatever the case adds.
func appOptions(g *fakeGitHub, extra ...forgeapi.Option) []forgeapi.Option {
	return append([]forgeapi.Option{
		forgeapi.WithWireTransport(g),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	}, extra...)
}

// appSource is a source for in, under readGrant, minting through g.
func appSource(t *testing.T, g *fakeGitHub, in creds.Installation, extra ...forgeapi.Option) *creds.AppSource {
	t.Helper()
	s, err := creds.NewAppSource(forgeapi.Connection{WebBaseURL: appWeb}, appKey(t),
		creds.AppSourceConfig{Installation: in, Grant: readGrant()}, appOptions(g, extra...)...)
	if err != nil {
		t.Fatalf("Setup: NewAppSource: %v", err)
	}
	return s
}

// forgeError is err as a *forgeapi.Error, failing the test where it is not one.
func forgeError(t *testing.T, err error) *forgeapi.Error {
	t.Helper()
	fe, ok := err.(*forgeapi.Error)
	if !ok {
		t.Fatalf("error = %T %v, want a *forgeapi.Error", err, err)
	}
	return fe
}
