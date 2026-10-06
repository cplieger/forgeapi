package forgeapi_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

func FuzzValidateCursor_accepts_only_what_resumes_a_list(f *testing.F) {
	for _, seed := range []string{
		"", "az", "AZ09", "-_.=:", "v1:Zm9v.YmFy_baz-2=", "a/b", "a+b", "a%2Fb", "a b", "a\x00b", "a\xffb",
		strings.Repeat("a", 512), strings.Repeat("a", 513),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		cursor := forgeapi.Cursor(raw)
		err := forgeapi.ValidateCursor(cursor)
		settings, listErr := forgeapi.ResolveList(forgeapi.WithAfter(cursor))
		if err != nil {
			if problem := localRefusal(err, forgeapi.CodeCursorInvalid); problem != nil {
				t.Fatalf("ValidateCursor(%q): %v", raw, problem)
			}
			if problem := localRefusal(listErr, forgeapi.CodeCursorInvalid); problem != nil {
				t.Fatalf("ResolveList(WithAfter(%q)) beside a ValidateCursor refusal: %v", raw, problem)
			}
			return
		}
		if listErr != nil || settings.After != cursor {
			t.Fatalf("ResolveList(WithAfter(%q)) = (after %q, %v), want the cursor ValidateCursor accepts", raw, settings.After, listErr)
		}
		if len(raw) > 512 {
			t.Fatalf("ValidateCursor(%d bytes) = nil, over the 512-byte cap", len(raw))
		}
		if escaped := url.PathEscape(raw); escaped != raw {
			t.Fatalf("ValidateCursor(%q) = nil, yet it path-escapes to %q", raw, escaped)
		}
	})
}
