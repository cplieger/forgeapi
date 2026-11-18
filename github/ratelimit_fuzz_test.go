package github_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cplieger/forgeapi/v3/github"
)

// FuzzReadLimit holds ReadLimit over any body to its contract: a secondary
// limit is a limit, a limit is a 403 or a 429, a body reads alike however its
// JSON is spaced, and a body that is not JSON names no secondary limit.
func FuzzReadLimit(f *testing.F) {
	f.Add(403, "4000", "", `{"message":"You have exceeded a secondary rate limit."}`)
	f.Add(403, "0", "", `{"message":"API rate limit exceeded for user ID 1."}`)
	f.Add(429, "", "60", `{}`)
	f.Add(200, "4999", "", `{"errors":[{"message":"secondary rate limit"}]}`)
	f.Add(403, "4000", "", "<html>secondary rate limit</html>")
	f.Fuzz(func(t *testing.T, status int, remaining, retryAfter, body string) {
		h := http.Header{}
		if remaining != "" {
			h.Set("X-RateLimit-Remaining", remaining)
		}
		if retryAfter != "" {
			h.Set("Retry-After", retryAfter)
		}
		got := github.ReadLimit(status, h, []byte(body))
		if got.Secondary && !got.Limited {
			t.Errorf("ReadLimit(%d, %v, %q) = secondary and not limited", status, h, body)
		}
		if got.Limited && status != http.StatusForbidden && status != http.StatusTooManyRequests {
			t.Errorf("ReadLimit(%d, %v, %q) = limited, want a 403 or 429 alone", status, h, body)
		}
		var spaced bytes.Buffer
		if json.Indent(&spaced, []byte(body), "", "  ") == nil {
			if again := github.ReadLimit(status, h, spaced.Bytes()); again != got {
				t.Errorf("ReadLimit(%q) = %+v, its indented form %+v", body, got, again)
			}
		}
		if h.Get("Retry-After") == "" && github.ReadLimit(status, h, append([]byte("<"), body...)).Secondary {
			t.Errorf("ReadLimit(%q behind a non-JSON prefix) = secondary, want no secondary limit", body)
		}
	})
}
