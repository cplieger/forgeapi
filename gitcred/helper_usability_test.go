package gitcred_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// withMark is rec carrying one usability mark.
func withMark(rec creds.Record, mark creds.Usability) creds.Record {
	rec.Usability = mark
	return rec
}

// checkNamesReconnecting holds a decline line to the remedy both marks share.
func checkNamesReconnecting(t *testing.T, diag, host string) {
	t.Helper()
	checkOneLine(t, diag)
	if !strings.Contains(strings.ToLower(diag), "reconnect") || !strings.Contains(diag, host) {
		t.Errorf("the decline line %q names no reconnect of %s, want the remedy and the instance it applies to", diag, host)
	}
}

// A mark the server wrote is a verdict reached in another process, and the record's
// token and expiry alone would hand that token to git.
func TestGet_declines_a_marked_record_with_the_marks_own_reason(t *testing.T) {
	tests := []struct {
		reason string
		mark   creds.Usability
		family forgeapi.Family
	}{
		{mark: creds.UsabilitySpent, reason: "spent", family: forgeapi.FamilyGitHub},
		{mark: creds.UsabilityReconnectRequired, reason: "reconnect_required", family: forgeapi.FamilyGitLab},
	}
	for _, tc := range tests {
		t.Run(tc.reason, func(t *testing.T) {
			store, _ := openStore(t)
			save(t, store, "conn", withMark(record(tc.family, forge), tc.mark))
			c := &tally{}

			got := serve(t, helper(store, c), "get", attributes("https", "forge.example"))
			checkDeclined(t, got, c, tc.reason)
			checkNamesReconnecting(t, got.diag, "forge.example")
		})
	}
}

// tokenEndpoint answers every refresh with one status and body.
func tokenEndpoint(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("Setup: writing the token answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The server's refresh machine and the helper read one directory from two stores,
// which is the two processes the deployment runs: what the server's refresh learned
// reaches the helper only through the record.
func TestGet_declines_a_pair_the_servers_refresh_marked(t *testing.T) {
	tests := []struct {
		body   string
		reason string
		status int
	}{
		{reason: "reconnect_required", status: http.StatusOK, body: `{"error":"bad_refresh_token"}`},
		{reason: "spent", status: http.StatusOK, body: `{"access_token":"token-new","token_type":"bearer","expires_in":28800,"refresh_tok`},
	}
	for _, tc := range tests {
		t.Run(tc.reason, func(t *testing.T) {
			srv := tokenEndpoint(t, tc.status, tc.body)
			serverStore, dir := openStore(t)
			rec := record(forgeapi.FamilyGitHub, srv.URL)
			rec.Expiry = rec.Issued.Add(time.Minute)
			rec.Issued = rec.Expiry.Add(-8 * time.Hour)
			save(t, serverStore, "conn", rec)
			src, err := creds.NewSource(serverStore, "conn", forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL},
				forgeapi.WithWireTransport(srv.Client().Transport),
				forgeapi.WithPlaintextHTTP(true),
				forgeapi.WithPrivateAddresses(true),
				forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
			)
			if err != nil {
				t.Fatalf("Setup: NewSource = error %v", err)
			}
			if token, tokenErr := src.Token(t.Context()); tokenErr == nil || token != "" {
				t.Fatalf("Setup: the server's Token() = (%q, %v), want no token: the refresh answer ends this pair", token, tokenErr)
			}
			helperStore, err := creds.OpenFileStore(dir)
			if err != nil {
				t.Fatalf("Setup: the helper's OpenFileStore(%q) = error %v", dir, err)
			}
			base, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatalf("Setup: parsing %q: %v", srv.URL, err)
			}
			c := &tally{}

			got := serve(t, helper(helperStore, c), "get", attributes(base.Scheme, base.Host))
			checkDeclined(t, got, c, tc.reason)
			checkNamesReconnecting(t, got.diag, base.Host)
		})
	}
}

// Only a static token may state no expiry. A rotating record stating none has no
// deadline that would ever decline it, so it is declined as reconnect-required, as
// the server's refresh machine reads it, rather than handed to git forever.
func TestGet_declines_a_rotating_record_without_an_expiry(t *testing.T) {
	store, _ := openStore(t)
	rec := record(forgeapi.FamilyGitHub, forge)
	rec.Expiry = time.Time{}
	save(t, store, "conn", rec)
	c := &tally{}

	got := serve(t, helper(store, c), "get", attributes("https", "forge.example"))
	checkDeclined(t, got, c, "reconnect_required")
}
