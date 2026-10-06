package forgeapi_test

import (
	"encoding/hex"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// A repository's identifier has one spelling: an id is accepted only where it is the
// encoding of the reference it decodes to, so a consumer keying on the segment it
// received never holds two keys for one repository.
func TestDecodeRepoRef_refuses_an_id_that_is_not_its_references_encoding(t *testing.T) {
	for _, test := range []struct {
		name string
		id   string
	}{
		{name: "upper_case_hex", id: "v1.6F776E65722F7265706F"},
		{name: "mixed_case_hex", id: "v1.6f776E65722f7265706f"},
		{name: "hex_of_a_selector_carrying_upper_case", id: forgeapi.RepoIDPrefix + hex.EncodeToString([]byte("Owner/Repo"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, family := range []forgeapi.Family{forgeapi.FamilyGitHub, forgeapi.FamilyGitLab, forgeapi.FamilyGitea} {
				ref, err := forgeapi.DecodeRepoRef(test.id, family)
				var fe *forgeapi.Error
				if !errors.As(err, &fe) || fe.Code != forgeapi.CodeRepoRefInvalid {
					t.Errorf("DecodeRepoRef(%q, %v) = (%+v, %v), want %q", test.id, family, ref, err, forgeapi.CodeRepoRefInvalid)
				}
			}
		})
	}
}

// The decoded selector is capped at 512 bytes before it reaches a URL, and the cap
// bounds the work a refused id costs: an id whose hex would decode past it is refused
// before it is decoded, so a long id allocates nothing in proportion to its length.
func TestDecodeRepoRef_refuses_an_over_cap_id_before_decoding_it(t *testing.T) {
	atCap := forgeapi.RepoIDPrefix + hex.EncodeToString([]byte("o/"+strings.Repeat("a", 510)))
	if _, err := forgeapi.DecodeRepoRef(atCap, forgeapi.FamilyGitHub); err != nil {
		t.Errorf("DecodeRepoRef(a 512-byte selector) = %v, want it decoded", err)
	}
	for name, id := range map[string]string{
		"one_byte_over": forgeapi.RepoIDPrefix + strings.Repeat("61", 513),
		"a_mebibyte":    forgeapi.RepoIDPrefix + strings.Repeat("61", 1<<20),
	} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := forgeapi.DecodeRepoRef(id, forgeapi.FamilyGitHub)
		runtime.ReadMemStats(&after)
		var fe *forgeapi.Error
		if !errors.As(err, &fe) || fe.Code != forgeapi.CodeRepoRefInvalid {
			t.Errorf("DecodeRepoRef(%s) = %v, want %q", name, err, forgeapi.CodeRepoRefInvalid)
		}
		if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<10 {
			t.Errorf("DecodeRepoRef(%s) allocated %d bytes refusing it, want under 64 KiB: the cap is checked after the decode", name, grew)
		}
	}
}

func TestRepoRefEncode_folds_ascii_upper_case_alone(t *testing.T) {
	for _, test := range []struct {
		name     string
		selector string
		folded   string
	}{
		{name: "letter_bounds", selector: "AZ/az", folded: "az/az"},
		{name: "bytes_beside_the_upper_range", selector: "@[/`{", folded: "@[/`{"},
		{name: "non_ascii", selector: "Ünï/Code", folded: "Ünï/code"},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := forgeapi.RepoIDPrefix + hex.EncodeToString([]byte(test.folded))
			if got := (forgeapi.RepoRef{Selector: test.selector}).Encode(); got != want {
				t.Errorf("RepoRef{Selector: %q}.Encode() = %q, want %q", test.selector, got, want)
			}
		})
	}
}

func TestValidateRef_accepts_a_ref_in_the_path_class(t *testing.T) {
	for name, ref := range map[string]string{
		"lower_bounds":    "az",
		"upper_bounds":    "AZ",
		"digit_bounds":    "09",
		"punctuation":     "-_.+~",
		"branch_path":     "feature/x-1.2",
		"sha":             "0123456789abcdef0123456789abcdef01234567",
		"at_the_byte_cap": strings.Repeat("a", 512),
	} {
		t.Run(name, func(t *testing.T) {
			if err := forgeapi.ValidateRef(ref); err != nil {
				t.Errorf("ValidateRef(%q) = %v, want nil", ref, err)
			}
		})
	}
}

func TestValidateRef_refuses_a_ref_outside_the_path_form(t *testing.T) {
	for name, ref := range map[string]string{
		"empty":            "",
		"over_the_cap":     strings.Repeat("a", 513),
		"backtick":         "a`b",
		"open_brace":       "a{b",
		"at_sign":          "a@b",
		"open_bracket":     "a[b",
		"colon":            "a:b",
		"space":            "a b",
		"percent_escape":   "a%2Fb",
		"backslash":        `a\b`,
		"query":            "a?b",
		"nul":              "a\x00b",
		"high_byte":        "a\xffb",
		"dot_dot_segment":  "../../../user",
		"empty_segment":    "a//b",
		"trailing_slash":   "main/",
		"leading_slash":    "/main",
		"lone_dot_segment": "a/./b",
	} {
		t.Run(name, func(t *testing.T) {
			if problem := localRefusal(forgeapi.ValidateRef(ref), forgeapi.CodeRefInvalid); problem != nil {
				t.Errorf("ValidateRef(%q): %v", ref, problem)
			}
		})
	}
}

// A value outside the members renders as the zero member rather than as a number.
func TestFamily_String_renders_the_wire_spelling(t *testing.T) {
	for _, test := range []struct {
		name   string
		want   string
		family forgeapi.Family
	}{
		{name: "zero", family: forgeapi.FamilyUnknown, want: "unknown"},
		{name: "github", family: forgeapi.FamilyGitHub, want: "github"},
		{name: "gitlab", family: forgeapi.FamilyGitLab, want: "gitlab"},
		{name: "last_member", family: forgeapi.FamilyGitea, want: "gitea"},
		{name: "past_the_last_member", family: forgeapi.FamilyGitea + 1, want: "unknown"},
		{name: "negative", family: -1, want: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.family.String(); got != test.want {
				t.Errorf("Family(%d).String() = %q, want %q", int(test.family), got, test.want)
			}
		})
	}
}

// The canonical spelling decodes, and the reference it answers carries that spelling
// as its ID and the lowercased selector, so ID equals Encode on every reference.
func TestDecodeRepoRef_answers_the_canonical_encoding_as_its_id(t *testing.T) {
	for _, test := range []struct {
		selector string
		want     string
		family   forgeapi.Family
	}{
		{family: forgeapi.FamilyGitHub, selector: "Owner/Repo", want: "owner/repo"},
		{family: forgeapi.FamilyGitLab, selector: "Group/Sub/Project", want: "group/sub/project"},
		{family: forgeapi.FamilyGitea, selector: "Owner/Repo", want: "owner/repo"},
	} {
		t.Run(test.family.String(), func(t *testing.T) {
			id := forgeapi.RepoRef{Family: test.family, Selector: test.selector}.Encode()
			got, err := forgeapi.DecodeRepoRef(id, test.family)
			if err != nil {
				t.Fatalf("DecodeRepoRef(%q, %v) = %v, want the reference", id, test.family, err)
			}
			if got.ID != id || got.Encode() != id || got.Selector != test.want {
				t.Errorf("DecodeRepoRef(%q, %v) = ID %q, Encode %q, selector %q; want ID and Encode %q and selector %q",
					id, test.family, got.ID, got.Encode(), got.Selector, id, test.want)
			}
		})
	}
}
