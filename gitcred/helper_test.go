package gitcred_test

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
	"github.com/cplieger/forgeapi/gitcred"
)

// tally records what the helper's two counters report.
type tally struct {
	declines []string
	erases   []string
	mu       sync.Mutex
}

func (c *tally) counters() forgeapi.Counters {
	return forgeapi.Counters{
		HelperDecline: func(reason string) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.declines = append(c.declines, reason)
		},
		HelperErase: func(family forgeapi.Family) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.erases = append(c.erases, family.String())
		},
	}
}

func (c *tally) declined() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.declines)
}

func (c *tally) erased() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.erases)
}

// forge is the web base every owned record names unless a case names another.
const forge = "https://forge.example"

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

// record is a credential for one family at one web base, valid for hours, with a
// refresh token addressed to that web base.
func record(family forgeapi.Family, base string) creds.Record {
	now := time.Now().Truncate(time.Second)
	return creds.Record{
		Family:        family,
		WebBaseURL:    base,
		Kind:          forgeapi.CredKindRotatingOAuth,
		Token:         "token-current",
		Issued:        now,
		Expiry:        now.Add(8 * time.Hour),
		RefreshToken:  "refresh-current",
		RefreshExpiry: now.Add(30 * 24 * time.Hour),
		ClientID:      "example-client-id",
		Account:       "example-user",
	}
}

func save(t *testing.T, store creds.Store, key string, rec creds.Record) {
	t.Helper()
	if err := store.Save(key, rec); err != nil {
		t.Fatalf("Setup: Save(%q) = error %v", key, err)
	}
}

// attributes is the block git writes to a helper for one remote.
func attributes(protocol, host string, extra ...string) string {
	lines := append([]string{"protocol=" + protocol, "host=" + host, "path=example/example.git"}, extra...)
	return strings.Join(lines, "\n") + "\n\n"
}

// served is one invocation's outcome: the attributes the helper wrote back, the
// diagnostic stream, and the error Serve returned.
type served struct {
	err   error
	attrs map[string]string
	diag  string
}

func serve(t *testing.T, h *gitcred.Helper, action, in string) served {
	t.Helper()
	var out, diag bytes.Buffer
	err := h.Serve(t.Context(), action, strings.NewReader(in), &out, &diag)
	attrs := map[string]string{}
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		if key, value, ok := strings.Cut(scanner.Text(), "="); ok {
			attrs[key] = value
		}
	}
	return served{err: err, attrs: attrs, diag: diag.String()}
}

// helper builds the credential helper over one store, counting into c.
func helper(store creds.Store, c *tally) *gitcred.Helper {
	return gitcred.New(store,
		forgeapi.WithCounters(c.counters()),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
}

// checkDeclined holds one get to a decline: no credential handed over, no claim
// that later helpers must not be asked, and the reason counted.
func checkDeclined(t *testing.T, got served, c *tally, reason string) {
	t.Helper()
	if pw, ok := got.attrs["password"]; ok {
		t.Errorf("get answered password=%q, want a decline", pw)
	}
	if q, ok := got.attrs["quit"]; ok {
		t.Errorf("get answered quit=%q, want none: a decline leaves later helpers and prompting to work", q)
	}
	if d := c.declined(); !slices.Equal(d, []string{reason}) {
		t.Errorf("HelperDecline reported %v, want [%s]", d, reason)
	}
	if got.err != nil {
		t.Errorf("Serve(get) declining for %s = error %v, want nil: a decline is not an error", reason, got.err)
	}
}

// checkOneLine holds a decline's diagnostic to the one line a user sees.
func checkOneLine(t *testing.T, diag string) {
	t.Helper()
	if diag == "" || strings.Count(strings.TrimSuffix(diag, "\n"), "\n") != 0 {
		t.Errorf("the diagnostic stream carried %q, want exactly one line naming why", diag)
	}
}

func TestGet_answers_an_owned_origin_with_the_familys_username(t *testing.T) {
	tests := []struct {
		username string
		family   forgeapi.Family
	}{
		{family: forgeapi.FamilyGitHub, username: "x-access-token"},
		{family: forgeapi.FamilyGitLab, username: "oauth2"},
		{family: forgeapi.FamilyGitea, username: "example-user"},
	}
	for _, tc := range tests {
		t.Run(tc.family.String(), func(t *testing.T) {
			store, _ := openStore(t)
			save(t, store, "conn", record(tc.family, forge))
			c := &tally{}

			got := serve(t, helper(store, c), "get", attributes("https", "forge.example"))
			if got.err != nil {
				t.Errorf("Serve(get) = error %v, want nil on an answered get", got.err)
			}
			if got.attrs["username"] != tc.username || got.attrs["password"] != "token-current" {
				t.Errorf("get = username %q password %q, want username %q password %q",
					got.attrs["username"], got.attrs["password"], tc.username, "token-current")
			}
			if got.diag != "" {
				t.Errorf("the diagnostic stream carried %q, want nothing on an answered get", got.diag)
			}
		})
	}
}

func TestGet_matches_the_origin_by_scheme_host_and_effective_port(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		protocol string
		host     string
		owned    bool
	}{
		{name: "host_case", base: forge, protocol: "https", host: "FORGE.Example", owned: true},
		{name: "default_port_spelled", base: forge, protocol: "https", host: "forge.example:443", owned: true},
		{name: "port_on_both", base: "https://forge.example:8443", protocol: "https", host: "forge.example:8443", owned: true},
		{name: "port_on_the_record_only", base: "https://forge.example:8443", protocol: "https", host: "forge.example"},
		{name: "port_on_the_remote_only", base: forge, protocol: "https", host: "forge.example:8443"},
		{name: "other_scheme", base: forge, protocol: "http", host: "forge.example"},
		{name: "other_host", base: forge, protocol: "https", host: "other.example"},
		{name: "subdomain", base: forge, protocol: "https", host: "git.forge.example"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := openStore(t)
			save(t, store, "conn", record(forgeapi.FamilyGitLab, tc.base))
			c := &tally{}

			got := serve(t, helper(store, c), "get", attributes(tc.protocol, tc.host))
			if tc.owned {
				if got.attrs["password"] != "token-current" {
					t.Errorf("get for %s://%s against %s = %v, want the token", tc.protocol, tc.host, tc.base, got.attrs)
				}
				return
			}
			checkDeclined(t, got, c, "unowned_origin")
		})
	}
}

// Another helper may own an origin this one does not, so declining it says nothing.
func TestGet_declines_an_unowned_origin_silently(t *testing.T) {
	store, _ := openStore(t)
	save(t, store, "conn", record(forgeapi.FamilyGitHub, forge))
	c := &tally{}

	got := serve(t, helper(store, c), "get", attributes("https", "elsewhere.example"))
	checkDeclined(t, got, c, "unowned_origin")
	if got.diag != "" {
		t.Errorf("the diagnostic stream carried %q, want nothing for an origin another helper may own", got.diag)
	}
}

// refreshTrap is a token endpoint at the record's web base that fails the case if
// anything reaches it, because the helper never refreshes.
func refreshTrap(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the helper sent %s %s, want no request: refresh is the server's", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGet_declines_an_expired_token_without_refreshing_it(t *testing.T) {
	trap := refreshTrap(t)
	store, _ := openStore(t)
	rec := record(forgeapi.FamilyGitLab, trap.URL)
	rec.Issued, rec.Expiry = rec.Issued.Add(-3*time.Hour), rec.Issued.Add(-time.Hour)
	save(t, store, "conn", rec)
	c := &tally{}

	got := serve(t, helper(store, c), "get", attributes("http", strings.TrimPrefix(trap.URL, "http://")))
	checkDeclined(t, got, c, "expired")
	checkOneLine(t, got.diag)
	after, ok, err := store.Load("conn")
	if err != nil || !ok || after.Token != rec.Token || after.RefreshToken != rec.RefreshToken {
		t.Errorf("the stored pair after the decline = (%q, %q, %v, %v), want it unchanged", after.Token, after.RefreshToken, ok, err)
	}
}

// A token inside the refresh lead is still a valid token, and the helper hands it
// over rather than refreshing it.
func TestGet_hands_over_a_due_but_unexpired_token_without_refreshing_it(t *testing.T) {
	trap := refreshTrap(t)
	store, _ := openStore(t)
	rec := record(forgeapi.FamilyGitHub, trap.URL)
	rec.Issued, rec.Expiry = rec.Issued.Add(-8*time.Hour+time.Minute), rec.Issued.Add(time.Minute)
	save(t, store, "conn", rec)
	c := &tally{}

	got := serve(t, helper(store, c), "get", attributes("http", strings.TrimPrefix(trap.URL, "http://")))
	if got.attrs["password"] != "token-current" {
		t.Errorf("get = %v, want the still-valid token", got.attrs)
	}
}

func TestGet_declines_a_connection_that_requires_a_reconnect(t *testing.T) {
	store, _ := openStore(t)
	rec := record(forgeapi.FamilyGitHub, forge)
	rec.Kind = forgeapi.CredKindUnknown
	save(t, store, "conn", rec)
	c := &tally{}

	got := serve(t, helper(store, c), "get", attributes("https", "forge.example"))
	checkDeclined(t, got, c, "reconnect_required")
	checkOneLine(t, got.diag)
}

// Gitea expects the account name as the username, so a record holding none has no
// credential git can use.
func TestGet_declines_a_gitea_record_with_no_account(t *testing.T) {
	store, _ := openStore(t)
	rec := record(forgeapi.FamilyGitea, forge)
	rec.Account = ""
	save(t, store, "conn", rec)
	c := &tally{}

	got := serve(t, helper(store, c), "get", attributes("https", "forge.example"))
	checkDeclined(t, got, c, "no_account")
	checkOneLine(t, got.diag)
}

func TestGet_declines_when_the_store_cannot_be_read(t *testing.T) {
	store, dir := openStore(t)
	save(t, store, "conn", record(forgeapi.FamilyGitHub, forge))
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte("{not a record"), 0o600); err != nil {
		t.Fatalf("Setup: corrupting the store = error %v", err)
	}
	c := &tally{}

	got := serve(t, helper(store, c), "get", attributes("https", "forge.example"))
	checkDeclined(t, got, c, "store_unreadable")
}

// The store is the consumer's, so a credential git offers back is not kept.
func TestStore_is_ignored(t *testing.T) {
	store, _ := openStore(t)
	rec := record(forgeapi.FamilyGitHub, forge)
	save(t, store, "conn", rec)
	c := &tally{}

	got := serve(t, helper(store, c), "store",
		attributes("https", "forge.example", "username=someone", "password=token-from-git"))
	if got.err != nil {
		t.Errorf("store = error %v, want nil", got.err)
	}
	if len(got.attrs) != 0 {
		t.Errorf("store wrote %v back, want nothing", got.attrs)
	}
	after, ok, err := store.Load("conn")
	if err != nil || !ok || after.Token != rec.Token {
		t.Errorf("the stored token after store = (%q, %v, %v), want %q unchanged", after.Token, ok, err, rec.Token)
	}
	keys, err := store.Keys()
	if err != nil || !slices.Equal(keys, []string{"conn"}) {
		t.Errorf("Keys() after store = (%v, %v), want ([conn], nil)", keys, err)
	}
}

// Git erases after any authentication failure, and one failed push must not take
// the connection and its views down with it.
func TestErase_keeps_the_connection_and_is_counted(t *testing.T) {
	store, _ := openStore(t)
	rec := record(forgeapi.FamilyGitLab, forge)
	save(t, store, "conn", rec)
	c := &tally{}

	serve(t, helper(store, c), "erase",
		attributes("https", "forge.example", "username=oauth2", "password=token-current"))
	after, ok, err := store.Load("conn")
	if err != nil || !ok || after.Token != rec.Token || after.RefreshToken != rec.RefreshToken {
		t.Errorf("the stored pair after erase = (%q, %q, %v, %v), want it kept", after.Token, after.RefreshToken, ok, err)
	}
	if e := c.erased(); !slices.Equal(e, []string{forgeapi.FamilyGitLab.String()}) {
		t.Errorf("HelperErase reported %v, want [%s]", e, forgeapi.FamilyGitLab)
	}
}

// Git's protocol asks a helper to ignore an action it does not know, which is what
// lets git add one later.
func TestServe_ignores_an_action_it_does_not_know(t *testing.T) {
	store, _ := openStore(t)
	save(t, store, "conn", record(forgeapi.FamilyGitHub, forge))
	c := &tally{}

	got := serve(t, helper(store, c), "an-action-git-may-add", attributes("https", "forge.example"))
	if got.err != nil || len(got.attrs) != 0 {
		t.Errorf("an unknown action = (%v, %v), want (no attributes, nil)", got.attrs, got.err)
	}
}

// A helper is a separate process reading the store the server rotates, so every
// read sees one whole record: a token some rotation issued, or a decline of a token
// that expired, and never a fragment of two.
func TestGet_reads_one_whole_record_while_the_server_rotates_it(t *testing.T) {
	var mu sync.Mutex
	issued := map[string]bool{"token-current": true}
	n := 0
	ep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n++
		token := "token-rotated-" + strings.Repeat("x", n)
		issued[token] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+token+`","token_type":"bearer","expires_in":2,"refresh_token":"refresh-`+token+`","refresh_token_expires_in":15897600}`)
	}))
	t.Cleanup(ep.Close)
	server, dir := openStore(t)
	rec := record(forgeapi.FamilyGitHub, ep.URL)
	rec.Issued, rec.Expiry = rec.Issued.Add(-2*time.Second), rec.Issued.Add(time.Second)
	save(t, server, "conn", rec)
	src, err := creds.NewSource(server, "conn", forgeapi.Connection{WebBaseURL: ep.URL, APIBaseURL: ep.URL},
		forgeapi.WithWireTransport(ep.Client().Transport),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err != nil {
		t.Fatalf("Setup: NewSource = error %v", err)
	}
	reader, err := creds.OpenFileStore(dir)
	if err != nil {
		t.Fatalf("Setup: OpenFileStore for the helper = error %v", err)
	}

	stop := time.Now().Add(3 * time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for time.Now().Before(stop) {
			if _, err := src.Token(t.Context()); err != nil {
				t.Errorf("the server's Token() = error %v, want a rotated token", err)
				return
			}
		}
	}()
	var torn string
	for torn == "" && time.Now().Before(stop) {
		c := &tally{}
		got := serve(t, helper(reader, c), "get", attributes("http", strings.TrimPrefix(ep.URL, "http://")))
		mu.Lock()
		known := issued[got.attrs["password"]]
		mu.Unlock()
		switch pw, answered := got.attrs["password"]; {
		case answered && !known:
			torn = "get answered password " + pw + ", which no rotation issued"
		case !answered && !slices.Equal(c.declined(), []string{"expired"}):
			torn = "get declined for " + strings.Join(c.declined(), ", ") + ", want only a token that expired between rotations"
		}
	}
	<-done
	if torn != "" {
		t.Error(torn)
	}
	mu.Lock()
	defer mu.Unlock()
	if n == 0 {
		t.Error("the server rotated nothing, so the race this case holds never ran")
	}
}
