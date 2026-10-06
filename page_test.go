package forgeapi_test

import (
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

func TestValidateCursor_accepts_a_cursor_in_this_librarys_encoding(t *testing.T) {
	for name, cursor := range map[string]forgeapi.Cursor{
		"empty":           "",
		"lower_bounds":    "az",
		"upper_bounds":    "AZ",
		"digit_bounds":    "09",
		"punctuation":     "-_.=:",
		"mixed":           "v1:Zm9v.YmFy_baz-2=",
		"at_the_byte_cap": forgeapi.Cursor(strings.Repeat("a", 512)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := forgeapi.ValidateCursor(cursor); err != nil {
				t.Errorf("ValidateCursor(%q) = %v, want nil", cursor, err)
			}
		})
	}
}

func TestValidateCursor_refuses_a_byte_outside_this_librarys_encoding(t *testing.T) {
	for name, cursor := range map[string]forgeapi.Cursor{
		"backtick":       "a`b",
		"open_brace":     "a{b",
		"at_sign":        "a@b",
		"open_bracket":   "a[b",
		"slash":          "a/b",
		"plus":           "a+b",
		"percent_escape": "a%2Fb",
		"space":          "a b",
		"nul":            "a\x00b",
		"high_byte":      "a\xffb",
		"over_the_cap":   forgeapi.Cursor(strings.Repeat("a", 513)),
	} {
		t.Run(name, func(t *testing.T) {
			if problem := localRefusal(forgeapi.ValidateCursor(cursor), forgeapi.CodeCursorInvalid); problem != nil {
				t.Errorf("ValidateCursor(%q): %v", cursor, problem)
			}
		})
	}
}
