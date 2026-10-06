package forgeapi_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// An owner reaches a search string whose grammar this library does not own, so an
// accepted one must survive query escaping one segment at a time.
func FuzzValidateOwner_accepts_only_owners_that_escape_unchanged(f *testing.F) {
	for _, seed := range []string{
		"acme", "Example-org_2.dev", "azAZ09", "acme/team/sub", "user:example", "a b", "a`b", "a{b", "a@b", "a[b",
		"a//b", "a/./b", "/a", "a/", "a\x00b", "a\xffb", strings.Repeat("a", 256),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, owner string) {
		pathErr := forgeapi.ValidateOwner(forgeapi.FamilyGitLab, owner)
		nameErr := forgeapi.ValidateOwner(forgeapi.FamilyGitHub, owner)
		_, listErr := forgeapi.ResolveList(forgeapi.WithOwner(owner))
		if (pathErr == nil) != (listErr == nil) {
			t.Fatalf("ValidateOwner(gitlab, %q) = %v but ResolveList(WithOwner) = %v, want one verdict", owner, pathErr, listErr)
		}
		if pathErr != nil {
			if nameErr == nil {
				t.Fatalf("ValidateOwner(github, %q) = nil where the path form refuses it: %v", owner, pathErr)
			}
			if problem := localRefusal(pathErr, forgeapi.CodeListOwnerInvalid); problem != nil {
				t.Fatalf("ValidateOwner(gitlab, %q): %v", owner, problem)
			}
			return
		}
		if len(owner) > 255 {
			t.Fatalf("ValidateOwner(gitlab, %q) = nil for %d bytes, over the 255-byte form", owner, len(owner))
		}
		for segment := range strings.SplitSeq(owner, "/") {
			if segment == "" || segment == "." || segment == ".." || url.QueryEscape(segment) != segment {
				t.Fatalf("ValidateOwner(gitlab, %q) = nil, yet segment %q is empty, a dot segment or changes under query escaping", owner, segment)
			}
		}
		if nameErr == nil && strings.Contains(owner, "/") {
			t.Fatalf("ValidateOwner(github, %q) = nil for a path", owner)
		}
	})
}
