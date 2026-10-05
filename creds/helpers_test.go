package creds_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// exchange is one request the token endpoint received, as the assertions read it.
type exchange struct {
	header http.Header
	form   url.Values
	query  url.Values
	method string
	path   string
}

// endpoint is a fake of a forge's OAuth endpoints: it records every request and
// answers each with what the case's answer function returns for it.
type endpoint struct {
	srv  *httptest.Server
	seen []exchange
	mu   sync.Mutex
}

// answer is what the fake endpoint sends for the n-th request it received.
type answer func(n int, x exchange) (status int, body string)

func newEndpoint(t *testing.T, reply answer) *endpoint {
	t.Helper()
	ep := &endpoint{}
	ep.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Errorf("Setup: reading the request body: %v", err)
		}
		form := url.Values{}
		if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
			form, err = url.ParseQuery(string(raw))
			if err != nil {
				t.Errorf("Setup: the request body %q is not a form: %v", raw, err)
			}
		}
		x := exchange{header: r.Header.Clone(), form: form, query: r.URL.Query(), method: r.Method, path: r.URL.Path}
		ep.mu.Lock()
		ep.seen = append(ep.seen, x)
		n := len(ep.seen)
		ep.mu.Unlock()
		status, body := reply(n, x)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(ep.srv.Close)
	return ep
}

// requests is what the endpoint has received so far.
func (ep *endpoint) requests() []exchange {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	return slices.Clone(ep.seen)
}

// conn is the connection whose web base the endpoint serves.
func (ep *endpoint) conn() forgeapi.Connection {
	return forgeapi.Connection{WebBaseURL: ep.srv.URL, APIBaseURL: ep.srv.URL}
}

// options are what every case passes: the endpoint's own transport, the two
// per-connection statements a loopback plaintext server needs, a silent logger and
// whatever the case adds.
func (ep *endpoint) options(extra ...forgeapi.Option) []forgeapi.Option {
	return append([]forgeapi.Option{
		forgeapi.WithWireTransport(ep.srv.Client().Transport),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	}, extra...)
}

// unreachable is an answer function for a case in which no request may leave.
func unreachable(t *testing.T) answer {
	return func(_ int, x exchange) (int, string) {
		t.Errorf("%s %s reached the token endpoint, want no request", x.method, x.path)
		return http.StatusInternalServerError, `{"error":"server_error"}`
	}
}

// openStore opens a file store in a fresh private directory of the test's own.
func openStore(t *testing.T) (*creds.FileStore, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "credentials")
	store, err := creds.OpenFileStore(dir)
	if err != nil {
		t.Fatalf("Setup: OpenFileStore(%q) = error %v", dir, err)
	}
	return store, dir
}

// clientID is the consumer's OAuth application, which every rotating record names.
const clientID = "example-client-id"

// rotating is a rotating record of one lifetime with left of it remaining: issued
// at now+left-lifetime and expiring at now+left, with a refresh token good for a
// month. Its times are whole seconds, so a store that keeps no finer grain still
// answers the record it was given.
func rotating(family forgeapi.Family, base string, lifetime, left time.Duration) creds.Record {
	now := time.Now().Truncate(time.Second)
	return creds.Record{
		Family:        family,
		WebBaseURL:    base,
		Kind:          forgeapi.CredKindRotatingOAuth,
		Token:         "token-old",
		Issued:        now.Add(left - lifetime),
		Expiry:        now.Add(left),
		RefreshToken:  "refresh-old",
		RefreshExpiry: now.Add(30 * 24 * time.Hour),
		ClientID:      clientID,
		Scopes:        []string{"repo"},
		Account:       "example-user",
	}
}

// save stores one record under one key, failing the case's setup otherwise.
func save(t *testing.T, store creds.Store, key string, rec creds.Record) {
	t.Helper()
	if err := store.Save(key, rec); err != nil {
		t.Fatalf("Setup: Save(%q) = error %v", key, err)
	}
}

// load reads one record back, failing the case where it is absent.
func load(t *testing.T, store creds.Store, key string) creds.Record {
	t.Helper()
	rec, ok, err := store.Load(key)
	if err != nil || !ok {
		t.Fatalf("Load(%q) = (_, %v, %v), want the record", key, ok, err)
	}
	return rec
}

// source builds the refresh machine over one stored key.
func source(t *testing.T, store creds.Store, key string, ep *endpoint, extra ...forgeapi.Option) *creds.Source {
	t.Helper()
	src, err := creds.NewSource(store, key, ep.conn(), ep.options(extra...)...)
	if err != nil {
		t.Fatalf("Setup: NewSource(%q) = error %v", key, err)
	}
	return src
}

// near reports whether got is within slack of want, for a time the code under test
// takes from its own clock.
func near(got, want time.Time, slack time.Duration) bool {
	d := got.Sub(want)
	return d <= slack && d >= -slack
}

// codeOf is the code a *forgeapi.Error carries, empty for any other error.
func codeOf(err error) string {
	if fe, ok := errors.AsType[*forgeapi.Error](err); ok {
		return fe.Code
	}
	return ""
}

// outcomes records every terminal refresh outcome the counters report.
type outcomes struct {
	seen []string
	mu   sync.Mutex
}

func (o *outcomes) counters() forgeapi.Counters {
	return forgeapi.Counters{RefreshOutcome: func(family forgeapi.Family, terminal forgeapi.CredState) {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.seen = append(o.seen, family.String()+" "+terminal.String())
	}}
}

func (o *outcomes) list() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.seen)
}

// sameRecord reports the first field two records disagree on, comparing times as
// instants, and empty where they agree.
func sameRecord(got, want creds.Record) string {
	switch {
	case got.Family != want.Family:
		return "Family"
	case got.WebBaseURL != want.WebBaseURL:
		return "WebBaseURL"
	case got.Kind != want.Kind:
		return "Kind"
	case got.Token != want.Token:
		return "Token"
	case !got.Issued.Equal(want.Issued):
		return "Issued"
	case !got.Expiry.Equal(want.Expiry):
		return "Expiry"
	case got.RefreshToken != want.RefreshToken:
		return "RefreshToken"
	case !got.RefreshExpiry.Equal(want.RefreshExpiry):
		return "RefreshExpiry"
	case got.ClientID != want.ClientID:
		return "ClientID"
	case !slices.Equal(got.Scopes, want.Scopes):
		return "Scopes"
	case got.Account != want.Account:
		return "Account"
	}
	return ""
}
