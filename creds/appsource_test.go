package creds_test

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/creds"
	"github.com/cplieger/forgeapi/v2/github"
)

// unreachableGitHub answers a case in which no request may leave.
func unreachableGitHub(t *testing.T) *fakeGitHub {
	return newFakeGitHub(t, func(_ int, c appCall) appReply {
		t.Errorf("%s %s reached GitHub, want no request", c.method, c.path)
		return refusal(http.StatusInternalServerError, "unexpected", nil)
	})
}

func TestNewAppSource_refuses_what_it_cannot_send(t *testing.T) {
	g := unreachableGitHub(t)
	key := appKey(t)
	full := creds.FullGrant()
	fullWithPermissions, fullWithRepositories := full, full
	fullWithPermissions.Permissions = map[string]string{"metadata": "read"}
	fullWithRepositories.Repositories = []string{"repo"}
	many := make([]string, 501)
	for i := range many {
		many[i] = fmt.Sprintf("repo%d", i)
	}
	web := forgeapi.Connection{WebBaseURL: appWeb}
	byID := creds.InstallationByID(42)
	for _, tc := range []struct {
		name string
		conn forgeapi.Connection
		key  *creds.AppKey
		cfg  creds.AppSourceConfig
		opts []forgeapi.Option
		code string
	}{
		{"no key", web, nil, creds.AppSourceConfig{Installation: byID, Grant: readGrant()}, nil, forgeapi.CodeAppKeyInvalid},
		{"the zero Installation", web, key, creds.AppSourceConfig{Grant: readGrant()}, nil, forgeapi.CodeAppGrantInvalid},
		{"an installation ID of zero", web, key, creds.AppSourceConfig{Installation: creds.InstallationByID(0), Grant: readGrant()}, nil, forgeapi.CodeAppGrantInvalid},
		{"an empty user login", web, key, creds.AppSourceConfig{Installation: creds.InstallationOfUser(""), Grant: readGrant()}, nil, forgeapi.CodeAppGrantInvalid},
		{"a user login carrying a separator", web, key, creds.AppSourceConfig{Installation: creds.InstallationOfUser("octo/cat"), Grant: readGrant()}, nil, forgeapi.CodeAppGrantInvalid},
		{"an organization login of dot-dot", web, key, creds.AppSourceConfig{Installation: creds.InstallationOfOrg(".."), Grant: readGrant()}, nil, forgeapi.CodeAppGrantInvalid},
		{"an organization login carrying a space", web, key, creds.AppSourceConfig{Installation: creds.InstallationOfOrg("octo cat"), Grant: readGrant()}, nil, forgeapi.CodeAppGrantInvalid},
		{"a repository naming no owner", web, key, creds.AppSourceConfig{Installation: creds.InstallationOfRepo("repo"), Grant: readGrant()}, nil, forgeapi.CodeAppGrantInvalid},
		{"a repository naming no name", web, key, creds.AppSourceConfig{Installation: creds.InstallationOfRepo("octo/"), Grant: readGrant()}, nil, forgeapi.CodeAppGrantInvalid},
		{"an owner holding a separator", web, key, creds.AppSourceConfig{Installation: creds.InstallationOfRepo("octo/team/repo"), Grant: readGrant()}, nil, forgeapi.CodeAppGrantInvalid},
		{"the zero grant", web, key, creds.AppSourceConfig{Installation: byID}, nil, forgeapi.CodeAppGrantInvalid},
		{"repositories with no permission", web, key, creds.AppSourceConfig{Installation: byID, Grant: creds.AppGrant{Repositories: []string{"repo"}}}, nil, forgeapi.CodeAppGrantInvalid},
		{"the full grant with permissions filled in", web, key, creds.AppSourceConfig{Installation: byID, Grant: fullWithPermissions}, nil, forgeapi.CodeAppGrantInvalid},
		{"the full grant with repositories filled in", web, key, creds.AppSourceConfig{Installation: byID, Grant: fullWithRepositories}, nil, forgeapi.CodeAppGrantInvalid},
		{"an unknown level", web, key, creds.AppSourceConfig{Installation: byID, Grant: creds.AppGrant{Permissions: map[string]string{"contents": "maintain"}}}, nil, forgeapi.CodeAppGrantInvalid},
		{"a permission name outside GitHub's form", web, key, creds.AppSourceConfig{Installation: byID, Grant: creds.AppGrant{Permissions: map[string]string{"Contents": "read"}}}, nil, forgeapi.CodeAppGrantInvalid},
		{"501 repositories", web, key, creds.AppSourceConfig{Installation: byID, Grant: creds.AppGrant{Permissions: map[string]string{"metadata": "read"}, Repositories: many}}, nil, forgeapi.CodeAppGrantInvalid},
		{"a repository carrying its owner", web, key, creds.AppSourceConfig{Installation: byID, Grant: creds.AppGrant{Permissions: map[string]string{"metadata": "read"}, Repositories: []string{"octo/repo"}}}, nil, forgeapi.CodeAppGrantInvalid},
		{"a plaintext web base under WithPlaintextHTTP", forgeapi.Connection{WebBaseURL: "http://github.example"}, key, creds.AppSourceConfig{Installation: byID, Grant: readGrant()}, []forgeapi.Option{forgeapi.WithPlaintextHTTP(true)}, forgeapi.CodePlaintextRefused},
		{"a plaintext API base under WithPlaintextHTTP", forgeapi.Connection{WebBaseURL: appWeb, APIBaseURL: "HTTP://api.github.example"}, key, creds.AppSourceConfig{Installation: byID, Grant: readGrant()}, []forgeapi.Option{forgeapi.WithPlaintextHTTP(true)}, forgeapi.CodePlaintextRefused},
		{"a negative lead", web, key, creds.AppSourceConfig{Installation: byID, Grant: readGrant()}, []forgeapi.Option{forgeapi.WithRefreshLead(-time.Second)}, forgeapi.CodeBudgetInvalid},
		{"no web base", forgeapi.Connection{}, key, creds.AppSourceConfig{Installation: byID, Grant: readGrant()}, nil, forgeapi.CodeConnectionInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := creds.NewAppSource(tc.conn, tc.key, tc.cfg, appOptions(g, tc.opts...)...)
			if s != nil || codeOf(err) != tc.code {
				t.Fatalf("NewAppSource = %v, %v, want no source and code %q", s, err, tc.code)
			}
			// The connection opted in, so the refusal names the App source's
			// own reason rather than a missing opt-in.
			if tc.code == forgeapi.CodePlaintextRefused && !strings.Contains(err.Error(), "App JWT") {
				t.Errorf("NewAppSource = %v, want the refusal to name the App JWT", err)
			}
		})
	}
}

func TestNewAppSource_accepts_a_half_hour_lead(t *testing.T) {
	g := unreachableGitHub(t)
	s := appSource(t, g, creds.InstallationByID(42), forgeapi.WithRefreshLead(30*time.Minute))
	if got := s.Kind(); got != forgeapi.CredKindAppInstallation {
		t.Errorf("Kind() = %v, want %v", got, forgeapi.CredKindAppInstallation)
	}
	if got := s.State(); got != forgeapi.CredRefreshDue {
		t.Errorf("State() before any mint = %v, want %v", got, forgeapi.CredRefreshDue)
	}
}

// The first Token mints, every call outside the default 5-minute lead answers
// the cached token, and the first call inside it mints again.
func TestAppSource_Token_hands_out_the_cached_token_until_the_lead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, mintsAt(t, 42, time.Hour))
		s := appSource(t, g, creds.InstallationByID(42))

		first, err := s.Token(t.Context())
		if err != nil || first != "ghs_token1" {
			t.Fatalf("Token() = %q, %v, want ghs_token1", first, err)
		}
		if got := s.State(); got != forgeapi.CredValid {
			t.Errorf("State() after a mint = %v, want %v", got, forgeapi.CredValid)
		}
		time.Sleep(55*time.Minute - time.Second)
		if got, err := s.Token(t.Context()); err != nil || got != "ghs_token1" || g.mints() != 1 {
			t.Errorf("Token() a second outside the lead = %q, %v after %d mints, want the cached ghs_token1 after 1", got, err, g.mints())
		}
		time.Sleep(time.Second)
		if got := s.State(); got != forgeapi.CredRefreshDue {
			t.Errorf("State() inside the lead = %v, want %v", got, forgeapi.CredRefreshDue)
		}
		if got, err := s.Token(t.Context()); err != nil || got != "ghs_token2" || g.mints() != 2 {
			t.Errorf("Token() inside the lead = %q, %v after %d mints, want ghs_token2 after 2", got, err, g.mints())
		}
	})
}

// The token request carries the App JWT as a bearer and the grant's fields.
func TestAppSource_Token_request_carries_the_App_JWT_and_the_grant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, mintsAt(t, 42, time.Hour))
		grant := creds.AppGrant{Permissions: map[string]string{"metadata": "read", "administration": "read"}, Repositories: []string{"one", "two"}}
		s, err := creds.NewAppSource(forgeapi.Connection{WebBaseURL: appWeb}, appKey(t),
			creds.AppSourceConfig{Installation: creds.InstallationByID(42), Grant: grant}, appOptions(g)...)
		if err != nil {
			t.Fatalf("Setup: NewAppSource: %v", err)
		}
		grant.Permissions["contents"] = "write"
		grant.Repositories[0] = "changed"

		if _, err := s.Token(t.Context()); err != nil {
			t.Fatalf("Token() = %v", err)
		}
		calls := g.calls()
		if len(calls) != 1 {
			t.Fatalf("GitHub received %d requests, want 1", len(calls))
		}
		jwt, ok := strings.CutPrefix(calls[0].header.Get("Authorization"), "Bearer ")
		if !ok {
			t.Fatalf("Authorization = %q, want a bearer", calls[0].header.Get("Authorization"))
		}
		_, claims, _ := jwtParts(t, jwt)
		var c struct {
			Iss string `json:"iss"`
		}
		if err := json.Unmarshal(claims, &c); err != nil || c.Iss != "Iv23liAbCdEf012345" {
			t.Errorf("the bearer's claims = %s, want the App's issuer", claims)
		}
		var body map[string]any
		if err := json.Unmarshal(calls[0].body, &body); err != nil {
			t.Fatalf("the request body %q is not JSON: %v", calls[0].body, err)
		}
		want := `{"permissions":{"administration":"read","metadata":"read"},"repositories":["one","two"]}`
		if got, _ := json.Marshal(body); string(got) != want {
			t.Errorf("the request body = %s, want %s, unchanged by the caller's later edits", got, want)
		}
	})
}

func TestAppSource_FullGrant_sends_no_body(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, mintsAt(t, 42, time.Hour))
		s, err := creds.NewAppSource(forgeapi.Connection{WebBaseURL: appWeb}, appKey(t),
			creds.AppSourceConfig{Installation: creds.InstallationByID(42), Grant: creds.FullGrant()}, appOptions(g)...)
		if err != nil {
			t.Fatalf("Setup: NewAppSource: %v", err)
		}
		if _, err := s.Token(t.Context()); err != nil {
			t.Fatalf("Token() = %v", err)
		}
		if calls := g.calls(); len(calls) != 1 || len(calls[0].body) != 0 {
			t.Errorf("the full grant's token request = %+v, want one request with no body, so neither permissions nor repositories", calls)
		}
	})
}

// The lead in force is the option's or half the token's lifetime, whichever
// is smaller: a 29-minute lead hands a 40-minute token out for 20 minutes.
func TestAppSource_lead_is_clamped_to_half_the_lifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, mintsAt(t, 42, 40*time.Minute))
		s := appSource(t, g, creds.InstallationByID(42), forgeapi.WithRefreshLead(29*time.Minute))
		if _, err := s.Token(t.Context()); err != nil {
			t.Fatalf("Token() = %v", err)
		}
		time.Sleep(20*time.Minute - time.Second)
		if got, _ := s.Token(t.Context()); got != "ghs_token1" {
			t.Errorf("Token() 20 minutes less a second in = %q, want the cached ghs_token1", got)
		}
		time.Sleep(time.Second)
		if got, _ := s.Token(t.Context()); got != "ghs_token2" {
			t.Errorf("Token() 20 minutes in = %q, want a fresh ghs_token2", got)
		}
	})
}

// Callers arriving while a mint is in flight wait for it and share its token.
func TestAppSource_Token_concurrent_callers_share_one_mint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		g := newFakeGitHub(t, func(n int, _ appCall) appReply {
			<-release
			return minted(n, time.Hour)
		})
		s := appSource(t, g, creds.InstallationByID(42))
		const callers = 5
		got := make([]string, callers)
		var wg sync.WaitGroup
		for i := range callers {
			wg.Go(func() {
				token, err := s.Token(t.Context())
				if err != nil {
					t.Errorf("Token() = %v", err)
				}
				got[i] = token
			})
		}
		synctest.Wait()
		if state := s.State(); state != forgeapi.CredRefreshing {
			t.Errorf("State() during the mint = %v, want %v", state, forgeapi.CredRefreshing)
		}
		close(release)
		wg.Wait()
		for i, token := range got {
			if token != "ghs_token1" {
				t.Errorf("caller %d got %q, want the one mint's ghs_token1", i, token)
			}
		}
		if n := g.mints(); n != 1 {
			t.Errorf("GitHub received %d token requests, want 1", n)
		}
	})
}

// Mint always mints and answers the token masked with its expiry, and leaves
// the cached token in place.
func TestAppSource_Mint_never_replaces_the_cached_token(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, mintsAt(t, 42, time.Hour))
		s := appSource(t, g, creds.InstallationByID(42))
		if _, err := s.Token(t.Context()); err != nil {
			t.Fatalf("Token() = %v", err)
		}
		tok, err := s.Mint(t.Context())
		if err != nil {
			t.Fatalf("Mint() = %v", err)
		}
		if got := tok.Token.Reveal(); got != "ghs_token2" {
			t.Errorf("Mint().Token.Reveal() = %q, want ghs_token2", got)
		}
		if want := time.Now().Add(time.Hour); !tok.Expiry.Equal(want) {
			t.Errorf("Mint().Expiry = %v, want %v", tok.Expiry, want)
		}
		if got, _ := s.Token(t.Context()); got != "ghs_token1" || g.mints() != 2 {
			t.Errorf("Token() after Mint = %q after %d mints, want the cached ghs_token1 after 2", got, g.mints())
		}
	})
}

// An App source refuses plain HTTP, so its end-to-end path runs over TLS with
// the server's certificate as the connection's CA.
func TestAppSource_mints_over_TLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/42/access_tokens" {
			t.Errorf("%s %s reached GitHub, want the token request", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":"ghs_tls","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	t.Cleanup(srv.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	s, err := creds.NewAppSource(forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL, CABytes: ca}, appKey(t),
		creds.AppSourceConfig{Installation: creds.InstallationByID(42), Grant: readGrant()},
		forgeapi.WithPrivateAddresses(true), forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("Setup: NewAppSource: %v", err)
	}
	if got, err := s.Token(t.Context()); err != nil || got != "ghs_tls" {
		t.Errorf("Token() over TLS = %q, %v, want ghs_tls", got, err)
	}
}

// A token request mints a credential and changes no forge object, so a
// read-only posture, which refuses every mutation, still mints.
func TestAppSource_mints_under_a_read_only_posture(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, mintsAt(t, 42, time.Hour))
		s := appSource(t, g, creds.InstallationByID(42), forgeapi.WithMutations(false))
		if got, err := s.Token(t.Context()); err != nil || got != "ghs_token1" {
			t.Errorf("Token() under WithMutations(false) = %q, %v, want ghs_token1", got, err)
		}
	})
}

// A client asks its source for a token on its own request's context, which a
// write marks as one. The mark is the client's: a read-only source still
// mints for a client that permits the write.
func TestAppSource_mints_for_a_clients_write_under_a_read_only_posture(t *testing.T) {
	pull := apiPrefix + "/repos/octo-org/repo/pulls/1"
	g := newFakeGitHub(t, func(n int, c appCall) appReply {
		switch {
		case c.method == http.MethodPost && c.path == installationPath(42):
			return minted(n, time.Hour)
		case c.method == http.MethodPatch && c.path == pull:
			return appReply{status: http.StatusOK, body: `{"number":1,"state":"closed"}`}
		}
		t.Errorf("%s %s reached GitHub, want only the token request and the pull request's update", c.method, c.path)
		return refusal(http.StatusInternalServerError, "unexpected", nil)
	})
	s := appSource(t, g, creds.InstallationByID(42), forgeapi.WithMutations(false))
	client, err := github.New(forgeapi.Connection{WebBaseURL: appWeb}, appOptions(g, forgeapi.WithCredentialSource(s))...)
	if err != nil {
		t.Fatalf("Setup: github.New: %v", err)
	}
	t.Cleanup(client.Close)
	repo := forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: "octo-org/repo"}
	if _, err := client.ClosePR(t.Context(), repo, forgeapi.PRRef{Number: 1}); err != nil {
		t.Errorf("ClosePR() with a read-only App source = %v, want the write sent", err)
	}
	if n := g.mints(); n != 1 {
		t.Errorf("GitHub received %d token requests, want 1", n)
	}
	if calls := g.calls(); len(calls) != 2 || calls[1].method != http.MethodPatch {
		t.Errorf("GitHub received %d requests, want the token request then the update", len(calls))
	}
}
