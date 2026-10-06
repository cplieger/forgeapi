package forgeapi_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// pathSafeProblem describes why an accepted path value would not reach a request
// path as the segments it reads as, or returns "" when it would.
func pathSafeProblem(value string) string {
	if value == "" || len(value) > 512 {
		return "its length is outside 1 to 512 bytes"
	}
	for segment := range strings.SplitSeq(value, "/") {
		switch {
		case segment == "", segment == ".", segment == "..":
			return "it carries an empty, dot or dot-dot segment"
		case url.PathEscape(segment) != segment:
			return "a segment changes under path escaping"
		}
	}
	return ""
}

func FuzzValidateRef_accepts_only_values_that_interpolate_unchanged(f *testing.F) {
	for _, seed := range []string{
		"main", "feature/x-1.2", "AZ/az/09", "-_.+~", "../../../user", "a//b", "/main", "main/",
		"a:b", "a b", "a%2Fb", "a?b", "a#b", `a\b`, "a\x00b", "a\xffb", "Ünï", strings.Repeat("a", 513),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, ref string) {
		err := forgeapi.ValidateRef(ref)
		if err != nil {
			if problem := localRefusal(err, forgeapi.CodeRefInvalid); problem != nil {
				t.Fatalf("ValidateRef(%q): %v", ref, problem)
			}
			return
		}
		if problem := pathSafeProblem(ref); problem != "" {
			t.Fatalf("ValidateRef(%q) = nil, yet %s", ref, problem)
		}
	})
}

func FuzzRepoRef_decodes_its_own_encoding(f *testing.F) {
	for _, seed := range []string{"owner/repo", "Owner/Repo", "AZ/az", "Group/Sub/Project", "a/b/c", "o/../r", "o:r", "Ünï/r"} {
		f.Add(seed, uint8(forgeapi.FamilyGitHub))
		f.Add(seed, uint8(forgeapi.FamilyGitLab))
	}
	f.Fuzz(func(t *testing.T, selector string, raw uint8) {
		family := forgeapi.Family(raw % 4)
		if forgeapi.ValidateSelector(family, selector) != nil {
			return
		}
		if problem := pathSafeProblem(selector); problem != "" {
			t.Fatalf("ValidateSelector(%v, %q) = nil, yet %s", family, selector, problem)
		}
		id := forgeapi.RepoRef{Family: family, Selector: selector}.Encode()
		got, err := forgeapi.DecodeRepoRef(id, family)
		if err != nil {
			t.Fatalf("DecodeRepoRef(Encode of %q, %v) = %v, want the reference", selector, family, err)
		}
		if want := strings.ToLower(selector); got.Selector != want || got.ID != id || got.Family != family {
			t.Fatalf("DecodeRepoRef(Encode of %q, %v) = %+v, want selector %q, ID %q and the family", selector, family, got, want, id)
		}
	})
}

func FuzzDecodeRepoRef_accepts_only_canonical_ids(f *testing.F) {
	for _, seed := range []string{
		"v1.6f776e65722f7265706f", "v1.6F776E65722F7265706F", "v1.4f776e65722f5265706f", "v1.2e2e2f72",
		"v2.6f2f72", "v1.", "v1.zz", "6f2f72",
	} {
		f.Add(seed, uint8(forgeapi.FamilyGitHub))
	}
	f.Fuzz(func(t *testing.T, id string, raw uint8) {
		family := forgeapi.Family(raw % 4)
		got, err := forgeapi.DecodeRepoRef(id, family)
		if err != nil {
			if problem := localRefusal(err, forgeapi.CodeRepoRefInvalid); problem != nil {
				t.Fatalf("DecodeRepoRef(%q, %v): %v", id, family, problem)
			}
			return
		}
		if got.ID != id || got.Encode() != id || got.Family != family {
			t.Fatalf("DecodeRepoRef(%q, %v) = %+v, want it to carry the id it re-encodes to", id, family, got)
		}
		if err := forgeapi.ValidateSelector(family, got.Selector); err != nil {
			t.Fatalf("DecodeRepoRef(%q, %v) answered selector %q, which ValidateSelector refuses: %v", id, family, got.Selector, err)
		}
	})
}
