package creds_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/creds"
)

// lookupThenMint answers the selector's lookup at lookup with body, and every
// token request for installation 42 with a fresh token.
func lookupThenMint(t *testing.T, lookup, body string) func(int, appCall) appReply {
	return func(n int, c appCall) appReply {
		switch {
		case c.method == http.MethodGet && c.path == lookup:
			return appReply{status: http.StatusOK, body: body}
		case c.method == http.MethodPost && c.path == installationPath(42):
			return minted(n, time.Hour)
		}
		t.Errorf("%s %s reached GitHub, want only %s and the token request", c.method, c.path, lookup)
		return refusal(http.StatusInternalServerError, "unexpected", nil)
	}
}

// Each selector is looked up at its own endpoint, once, whichever of Resolve,
// Token and Mint comes first.
func TestAppSource_Resolve_looks_each_selector_up_once(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     creds.Installation
		lookup string
		body   string
	}{
		{"of a user", creds.InstallationOfUser("octocat"), apiPrefix + "/users/octocat/installation", `{"id":42}`},
		{"of an organization", creds.InstallationOfOrg("octo-org"), apiPrefix + "/orgs/octo-org/installation", `{"id":42}`},
		{"of a repository", creds.InstallationOfRepo("octo-org/repo"), apiPrefix + "/repos/octo-org/repo/installation", `{"id":42}`},
		{"the only one", creds.OnlyInstallation(), apiPrefix + "/app/installations", `[{"id":42,"account":{"login":"octo-org"}}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				g := newFakeGitHub(t, lookupThenMint(t, tc.lookup, tc.body))
				s := appSource(t, g, tc.in)
				if id, err := s.Resolve(t.Context()); err != nil || id != 42 {
					t.Errorf("Resolve() = %d, %v, want 42", id, err)
				}
				if _, err := s.Token(t.Context()); err != nil {
					t.Errorf("Token() = %v", err)
				}
				if _, err := s.Mint(t.Context()); err != nil {
					t.Errorf("Mint() = %v", err)
				}
				if id, err := s.Resolve(t.Context()); err != nil || id != 42 {
					t.Errorf("Resolve() again = %d, %v, want 42", id, err)
				}
				lookups := 0
				for _, c := range g.calls() {
					if c.method == http.MethodGet {
						lookups++
					}
				}
				if lookups != 1 || g.mints() != 2 {
					t.Errorf("GitHub received %d lookups and %d token requests, want 1 and 2", lookups, g.mints())
				}
			})
		})
	}
}

func TestAppSource_Resolve_by_ID_sends_nothing(t *testing.T) {
	s := appSource(t, unreachableGitHub(t), creds.InstallationByID(42))
	if id, err := s.Resolve(t.Context()); err != nil || id != 42 {
		t.Errorf("Resolve() = %d, %v, want 42 from memory", id, err)
	}
}

// installationPage is a list of n installations, org1 (1) first.
func installationPage(n int) string {
	entries := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		entries = append(entries, fmt.Sprintf(`{"id":%d,"account":{"login":"org%d"}}`, i, i))
	}
	return "[" + strings.Join(entries, ",") + "]"
}

// OnlyInstallation needs exactly one: none, or more than one named by account
// and ID, needs a human and reads as reconnect-required.
func TestAppSource_OnlyInstallation_refuses_anything_but_one(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want []string
	}{
		{"none", `[]`, []string{"no installation"}},
		{"two", `[{"id":7,"account":{"login":"octo-org"}},{"id":9,"account":{"login":"octocat"}}]`, []string{"has 2 installations", "octo-org (7)", "octocat (9)"}},
		{"a full page", installationPage(100), []string{"at least 100 installations", "first page", "org1 (1)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGitHub(t, lookupThenMint(t, apiPrefix+"/app/installations", tc.body))
			s := appSource(t, g, creds.OnlyInstallation())
			_, err := s.Token(t.Context())
			fe := forgeError(t, err)
			if fe.Code != forgeapi.CodeReconnectRequired {
				t.Errorf("Token() code = %q, want %q", fe.Code, forgeapi.CodeReconnectRequired)
			}
			for _, part := range tc.want {
				if !strings.Contains(fe.Message, part) {
					t.Errorf("Token() message = %q, want it to name %q", fe.Message, part)
				}
			}
			if got := s.State(); got != forgeapi.CredReconnectRequired {
				t.Errorf("State() = %v, want %v", got, forgeapi.CredReconnectRequired)
			}
			if g.mints() != 0 {
				t.Errorf("GitHub received %d token requests, want none", g.mints())
			}
		})
	}
}

// A 404 from the token endpoint is a gone installation. The re-probe five
// minutes later looks a repository's installation up again and finds the
// reinstall's new ID; an ID selector keeps its own and asks for it again.
func TestAppSource_404_forgets_the_installation_ID_but_an_ID_selector(t *testing.T) {
	t.Run("of a repository", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			lookups := 0
			g := newFakeGitHub(t, func(n int, c appCall) appReply {
				switch {
				case c.method == http.MethodGet:
					lookups++
					if lookups == 1 {
						return appReply{status: http.StatusOK, body: `{"id":42}`}
					}
					return appReply{status: http.StatusOK, body: `{"id":99}`}
				case c.path == installationPath(42):
					return refusal(http.StatusNotFound, "Not Found", nil)
				case c.path == installationPath(99):
					return minted(n, time.Hour)
				}
				t.Errorf("%s %s reached GitHub", c.method, c.path)
				return refusal(http.StatusInternalServerError, "unexpected", nil)
			})
			s := appSource(t, g, creds.InstallationOfRepo("octo-org/repo"))
			if _, err := s.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
				t.Fatalf("Token() against a gone installation = %v, want %q", err, forgeapi.CodeReconnectRequired)
			}
			time.Sleep(5 * time.Minute)
			if got, err := s.Token(t.Context()); err != nil || got == "" {
				t.Errorf("Token() at the re-probe = %q, %v, want the reinstall's token", got, err)
			}
			if id, err := s.Resolve(t.Context()); err != nil || id != 99 {
				t.Errorf("Resolve() after the reinstall = %d, %v, want 99", id, err)
			}
			if got := s.State(); got != forgeapi.CredValid {
				t.Errorf("State() after the re-probe succeeded = %v, want %v", got, forgeapi.CredValid)
			}
		})
	})
	t.Run("by ID", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			g := newFakeGitHub(t, func(n int, c appCall) appReply {
				if c.path != installationPath(42) {
					t.Errorf("%s %s reached GitHub, want only installation 42's token request", c.method, c.path)
				}
				if n == 1 {
					return refusal(http.StatusNotFound, "Not Found", nil)
				}
				return minted(n, time.Hour)
			})
			s := appSource(t, g, creds.InstallationByID(42))
			if _, err := s.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
				t.Fatalf("Token() against a gone installation = %v, want %q", err, forgeapi.CodeReconnectRequired)
			}
			time.Sleep(5 * time.Minute)
			if _, err := s.Token(t.Context()); err != nil {
				t.Errorf("Token() at the re-probe = %v", err)
			}
			if id, err := s.Resolve(t.Context()); err != nil || id != 42 {
				t.Errorf("Resolve() = %d, %v, want the selector's own 42", id, err)
			}
		})
	})
}

// A lookup GitHub fails with a 5xx or a lost connection is retried as a token
// request is, whether Token or Resolve sent it: the budget's retries bound
// every lookup one call sends, and the caller reads GitHub's answer, under a
// short deadline too.
func TestAppSource_a_failed_lookup_is_retried_like_a_mint(t *testing.T) {
	lookup := apiPrefix + "/repos/octo-org/repo/installation"
	repo := creds.InstallationOfRepo("octo-org/repo")
	calls := []struct {
		call func(*creds.AppSource, context.Context) error
		name string
	}{
		{name: "Token", call: func(s *creds.AppSource, ctx context.Context) error { _, err := s.Token(ctx); return err }},
		{name: "Resolve", call: func(s *creds.AppSource, ctx context.Context) error { _, err := s.Resolve(ctx); return err }},
	}
	transient := forgeapi.DefaultRetries + 1
	for _, tc := range []struct {
		name    string
		reply   appReply
		timeout time.Duration
		retries int
		status  int
		lookups int
	}{
		{"a 500", refusal(http.StatusInternalServerError, "oops", nil), forgeapi.DefaultOperationTimeout, forgeapi.DefaultRetries, http.StatusInternalServerError, transient},
		{"a 502", refusal(http.StatusBadGateway, "bad gateway", nil), forgeapi.DefaultOperationTimeout, forgeapi.DefaultRetries, http.StatusBadGateway, transient},
		{"a 503", refusal(http.StatusServiceUnavailable, "unavailable", nil), forgeapi.DefaultOperationTimeout, forgeapi.DefaultRetries, http.StatusServiceUnavailable, transient},
		{"a dropped connection", appReply{drop: true}, forgeapi.DefaultOperationTimeout, forgeapi.DefaultRetries, 0, transient},
		// Pauses of 1 and 2 seconds fit 5 seconds; the third, of 4, does not.
		{"a 502 under four retries and a deadline of 5 seconds", refusal(http.StatusBadGateway, "bad gateway", nil), 5 * time.Second, 4, http.StatusBadGateway, 3},
	} {
		for _, c := range calls {
			t.Run(c.name+" after "+tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					g := newFakeGitHub(t, func(_ int, call appCall) appReply {
						if call.method != http.MethodGet || call.path != lookup {
							t.Errorf("%s %s reached GitHub, want only the lookup", call.method, call.path)
						}
						return tc.reply
					})
					s := appSource(t, g, repo, forgeapi.WithRetries(tc.retries), forgeapi.WithOperationTimeout(tc.timeout))
					started := time.Now()
					err := c.call(s, t.Context())
					if took := time.Since(started); took > tc.timeout {
						t.Errorf("%s() took %v, want it inside the %v deadline", c.name, took, tc.timeout)
					}
					if fe := forgeError(t, err); fe.Status != tc.status || fe.Kind != forgeapi.KindTransient {
						t.Errorf("%s() = %+v, want GitHub's transient answer, status %d", c.name, fe, tc.status)
					}
					if n := len(g.calls()); n != tc.lookups {
						t.Errorf("GitHub received %d lookups, want %d", n, tc.lookups)
					}
				})
			})
		}
	}
}

// A lookup answered with no usable ID is malformed: worth another attempt,
// and never a human's to repair.
func TestAppSource_Resolve_malformed_lookup_is_transient(t *testing.T) {
	g := newFakeGitHub(t, func(_ int, _ appCall) appReply {
		return appReply{status: http.StatusOK, body: `{"id":0}`}
	})
	s := appSource(t, g, creds.InstallationOfUser("octocat"))
	_, err := s.Resolve(t.Context())
	if fe := forgeError(t, err); fe.Kind != forgeapi.KindTransient || !fe.Retryable {
		t.Errorf("Resolve() = %v, want a retryable transient failure", err)
	}
	if got := s.State(); got != forgeapi.CredRefreshDue {
		t.Errorf("State() = %v, want %v", got, forgeapi.CredRefreshDue)
	}
}
