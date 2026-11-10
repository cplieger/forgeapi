package creds_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/creds"
)

// A login is interpolated into the lookup's path, so an accepted one reaches
// GitHub as exactly one path segment spelling it, and a refused one is refused
// before any request without being quoted.
func FuzzInstallationOfUser_stays_one_path_segment(f *testing.F) {
	for _, seed := range []string{"octocat", "octo-cat", "", ".", "..", "a/b", "a%2Fb", "a b", "a?b", "a#b", "%2e%2e", "ü", "a\x00b", "a.b", "~a+b_c"} {
		f.Add(seed)
	}
	rsaKey, err := appRSAKey()
	if err != nil {
		f.Fatalf("Setup: rsa.GenerateKey: %v", err)
	}
	key, err := creds.NewAppKey("123456", rsaKey)
	if err != nil {
		f.Fatalf("Setup: NewAppKey: %v", err)
	}
	f.Fuzz(func(t *testing.T, login string) {
		g := newFakeGitHub(t, func(int, appCall) appReply { return appReply{status: http.StatusOK, body: `{"id":42}`} })
		s, err := creds.NewAppSource(forgeapi.Connection{WebBaseURL: appWeb}, key,
			creds.AppSourceConfig{Installation: creds.InstallationOfUser(login), Grant: readGrant()}, appOptions(g)...)
		if err != nil {
			if codeOf(err) != forgeapi.CodeAppGrantInvalid {
				t.Fatalf("NewAppSource(%q) = %v, want %q", login, err, forgeapi.CodeAppGrantInvalid)
			}
			if len(login) > 3 && strings.Contains(err.Error(), login) {
				t.Fatalf("NewAppSource(%q) = %v, which quotes the login", login, err)
			}
			return
		}
		if _, err := s.Resolve(t.Context()); err != nil {
			t.Fatalf("Resolve() for %q = %v", login, err)
		}
		calls := g.calls()
		if len(calls) != 1 {
			t.Fatalf("Resolve() for %q sent %d requests, want 1", login, len(calls))
		}
		segments := strings.Split(calls[0].escaped, "/")
		if len(segments) != 6 || strings.Join(segments[:4], "/") != apiPrefix+"/users" || segments[5] != "installation" {
			t.Fatalf("Resolve() for %q asked for %q, want %s/users/{login}/installation", login, calls[0].escaped, apiPrefix)
		}
		if got, err := url.PathUnescape(segments[4]); err != nil || got != login || got == "." || got == ".." {
			t.Fatalf("Resolve() for %q sent the segment %q, which spells %q", login, segments[4], got)
		}
	})
}

// A repository's full name is interpolated into the lookup's path, so an
// accepted one reaches GitHub as exactly two path segments spelling it, and a
// refused one is refused before any request without being quoted.
func FuzzInstallationOfRepo_stays_two_path_segments(f *testing.F) {
	for _, seed := range []string{"octo-org/repo", "repo", "/repo", "octo/", "a/b/c", "../x", "x/..", "a%2Fb/c", "a/b?c", "a/b#c", "~a+b/c.d_e", "a/b\x00c", "ü/b"} {
		f.Add(seed)
	}
	rsaKey, err := appRSAKey()
	if err != nil {
		f.Fatalf("Setup: rsa.GenerateKey: %v", err)
	}
	key, err := creds.NewAppKey("123456", rsaKey)
	if err != nil {
		f.Fatalf("Setup: NewAppKey: %v", err)
	}
	f.Fuzz(func(t *testing.T, fullName string) {
		g := newFakeGitHub(t, func(int, appCall) appReply { return appReply{status: http.StatusOK, body: `{"id":42}`} })
		s, err := creds.NewAppSource(forgeapi.Connection{WebBaseURL: appWeb}, key,
			creds.AppSourceConfig{Installation: creds.InstallationOfRepo(fullName), Grant: readGrant()}, appOptions(g)...)
		if err != nil {
			if codeOf(err) != forgeapi.CodeAppGrantInvalid {
				t.Fatalf("NewAppSource(%q) = %v, want %q", fullName, err, forgeapi.CodeAppGrantInvalid)
			}
			if err.Error() != "app_grant_invalid: the installation's repository is not a GitHub owner and name" {
				t.Fatalf("NewAppSource(%q) = %v, want the fixed refusal that quotes no name", fullName, err)
			}
			return
		}
		if _, err := s.Resolve(t.Context()); err != nil {
			t.Fatalf("Resolve() for %q = %v", fullName, err)
		}
		calls := g.calls()
		if len(calls) != 1 {
			t.Fatalf("Resolve() for %q sent %d requests, want 1", fullName, len(calls))
		}
		segments := strings.Split(calls[0].escaped, "/")
		if len(segments) != 7 || strings.Join(segments[:4], "/") != apiPrefix+"/repos" || segments[6] != "installation" {
			t.Fatalf("Resolve() for %q asked for %q, want %s/repos/{owner}/{repo}/installation", fullName, calls[0].escaped, apiPrefix)
		}
		owner, errOwner := url.PathUnescape(segments[4])
		repo, errRepo := url.PathUnescape(segments[5])
		if errOwner != nil || errRepo != nil || owner+"/"+repo != fullName {
			t.Fatalf("Resolve() for %q sent the segments %q and %q", fullName, segments[4], segments[5])
		}
		for _, part := range []string{owner, repo} {
			if part == "." || part == ".." {
				t.Fatalf("Resolve() for %q sent the dot segment %q", fullName, part)
			}
		}
	})
}
