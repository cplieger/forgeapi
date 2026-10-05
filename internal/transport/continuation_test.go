package transport

import (
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// pageSettings resolves one list call's options for these cases.
func pageSettings(t *testing.T, opts ...forgeapi.ListOption) forgeapi.ListSettings {
	t.Helper()
	set, err := forgeapi.ResolveList(opts...)
	if err != nil {
		t.Fatalf("Setup: ResolveList(%v) = %v", opts, err)
	}
	return set
}

// pageRepo is one repository reference spelled as a caller wrote it.
func pageRepo(selector string) forgeapi.RepoRef {
	return forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: selector, DisplayPath: selector}
}

// pageConnection is the connection every call below is made on.
const pageConnection = "https://forge.example/api/v1"

// A continuation names its repository and its owner as the derived repository
// identifier folds them, so the same call spelled in another case resumes it.
func TestPageCall_resumes_a_continuation_of_the_same_call_spelled_in_another_case(t *testing.T) {
	for name, pair := range map[string][2]PageCall{
		"repository": {
			newPageCall(forgeapi.FamilyGitHub, pageConnection, "ListIssues", pageRepo("Example/Repo"), pageSettings(t)),
			newPageCall(forgeapi.FamilyGitHub, pageConnection, "ListIssues", pageRepo("example/repo"), pageSettings(t)),
		},
		"owner": {
			newPageCall(forgeapi.FamilyGitHub, pageConnection, "ListMyPRs", forgeapi.RepoRef{}, pageSettings(t, forgeapi.WithOwner("Example-Org"))),
			newPageCall(forgeapi.FamilyGitHub, pageConnection, "ListMyPRs", forgeapi.RepoRef{}, pageSettings(t, forgeapi.WithOwner("example-org"))),
		},
	} {
		t.Run(name, func(t *testing.T) {
			minted := pair[0].Mint("p", 4)
			got, err := pair[1].Resume(minted, "p", 1)
			if err != nil || !slices.Equal(got, []int{4}) {
				t.Errorf("Resume(%q) = %v, %v, want [4]", minted, got, err)
			}
		})
	}
}

// The bound a walk began at rides the refusal, since it is the one bound that
// resumes the walk.
func TestPageCall_refuses_another_bound_naming_the_one_the_walk_began_at(t *testing.T) {
	minted := newPageCall(forgeapi.FamilyGitHub, pageConnection, "ListLabels", pageRepo("example/repo"), pageSettings(t, forgeapi.WithPageBound(7))).Mint("p", 2)
	_, err := newPageCall(forgeapi.FamilyGitHub, pageConnection, "ListLabels", pageRepo("example/repo"), pageSettings(t, forgeapi.WithPageBound(8))).Resume(minted, "p", 1)
	var fe *forgeapi.Error
	if !errors.As(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid || !strings.Contains(fe.Message, "page bound of 7") {
		t.Errorf("Resume(%q) under a bound of 8 = %v, want code %q naming the bound of 7", minted, err, forgeapi.CodeCursorInvalid)
	}
}

// connectionNamed is the connection a continuation names for one API base as a
// connection resolved it.
func connectionNamed(t *testing.T, base string) string {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("Setup: url.Parse(%q) = %v", base, err)
	}
	return connectionOf(u)
}

// Two spellings of one API base name one connection: the host in capitals, the
// scheme's default port spelled out, a trailing slash.
func TestPageCall_names_one_connection_under_every_spelling_of_its_API_base(t *testing.T) {
	want := connectionNamed(t, "https://forge.example/api/v1")
	for _, spelling := range []string{
		"https://FORGE.example/api/v1",
		"https://forge.example:443/api/v1",
		"https://forge.example/api/v1/",
	} {
		if got := connectionNamed(t, spelling); got != want {
			t.Errorf("connectionOf(%q) = %q, want %q, the connection https://forge.example/api/v1 names", spelling, got, want)
		}
	}
	plain := connectionNamed(t, "http://forge.example/api/v1")
	if got := connectionNamed(t, "http://forge.example:80/api/v1"); got != plain {
		t.Errorf("connectionOf(http://forge.example:80/api/v1) = %q, want %q, the connection http://forge.example/api/v1 names", got, plain)
	}
}

// Another scheme, host, port or root is another connection, so a call on it names
// another call and refuses the continuation the first minted.
func TestPageCall_refuses_a_continuation_minted_on_another_connection(t *testing.T) {
	minted := newPageCall(forgeapi.FamilyGitea, connectionNamed(t, "https://forge.example/api/v1"), "ListLabels", pageRepo("example/repo"), pageSettings(t)).Mint("p", 2)
	for _, other := range []string{
		"http://forge.example/api/v1",
		"https://other.example/api/v1",
		"https://forge.example:8443/api/v1",
		"https://forge.example/second/api/v1",
	} {
		call := newPageCall(forgeapi.FamilyGitea, connectionNamed(t, other), "ListLabels", pageRepo("example/repo"), pageSettings(t))
		_, err := call.Resume(minted, "p", 1)
		var fe *forgeapi.Error
		if !errors.As(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
			t.Errorf("Resume(%q) on %s = %v, want code %q", minted, other, err, forgeapi.CodeCursorInvalid)
		}
	}
}

// A call names its family beside its connection, so a client of another family built
// for the same API base refuses the continuation, although both spell the repository
// and the page the same way.
func TestPageCall_refuses_a_continuation_another_family_minted_on_its_connection(t *testing.T) {
	minted := newPageCall(forgeapi.FamilyGitHub, pageConnection, "ListLabels", pageRepo("example/repo"), pageSettings(t)).Mint("p", 2)
	_, err := newPageCall(forgeapi.FamilyGitLab, pageConnection, "ListLabels", pageRepo("example/repo"), pageSettings(t)).Resume(minted, "p", 1)
	var fe *forgeapi.Error
	if !errors.As(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
		t.Errorf("Resume(%q) on a %s call = %v, want code %q", minted, forgeapi.FamilyGitLab, err, forgeapi.CodeCursorInvalid)
	}
}

// positionMinted is a position continuation the list below mints at a bound of 7.
func positionMinted(t *testing.T) forgeapi.Cursor {
	t.Helper()
	return newPageCall(forgeapi.FamilyGitLab, pageConnection, "ListPRs", pageRepo("example/repo"), pageSettings(t, forgeapi.WithPageBound(7))).MintPosition("g", "Y3Vyc29y")
}

// A position continuation carries no bound, since an upstream position resumes at any
// page size, so the call that minted it resumes it under another bound.
func TestPageCall_resumes_a_position_under_any_bound(t *testing.T) {
	minted := positionMinted(t)
	resumer := newPageCall(forgeapi.FamilyGitLab, pageConnection, "ListPRs", pageRepo("example/repo"), pageSettings(t, forgeapi.WithPageBound(1)))
	if got, err := resumer.ResumePosition(minted, "g", 1); err != nil || !slices.Equal(got, []string{"Y3Vyc29y"}) {
		t.Errorf("ResumePosition(%q) under a bound of 1 = %v, %v, want [Y3Vyc29y]", minted, got, err)
	}
}

// A position continuation names its call, so the same list on another repository
// refuses it.
func TestPageCall_refuses_a_position_another_call_minted(t *testing.T) {
	minted := positionMinted(t)
	other := newPageCall(forgeapi.FamilyGitLab, pageConnection, "ListPRs", pageRepo("example/other"), pageSettings(t, forgeapi.WithPageBound(7)))
	_, err := other.ResumePosition(minted, "g", 1)
	var fe *forgeapi.Error
	if !errors.As(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
		t.Errorf("ResumePosition(%q) on another repository = %v, want code %q", minted, err, forgeapi.CodeCursorInvalid)
	}
}
