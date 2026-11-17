package gitlab

import (
	"net/http"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
)

// headerPairs is a header holding each name and value pair.
func headerPairs(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

// answered is the *forgeapi.Error a ListRuns answered with status, header and
// body earns.
func answered(t *testing.T, status int, header http.Header, body string) *forgeapi.Error {
	t.Helper()
	h := newAnsweringHarness(t, status, header, body)
	_, err := h.client.ListRuns(t.Context(), testRef())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("ListRuns answered %d = %v, want a *forgeapi.Error", status, err)
	}
	return fe
}

// TestErrorResetFromAnswer holds Error.Reset to the failing answer's own
// RateLimit-Reset, and to the zero time where the answer carries none
// (https://docs.gitlab.com/administration/settings/user_and_ip_rate_limits/#response-headers).
func TestErrorResetFromAnswer(t *testing.T) {
	for _, test := range []struct {
		header http.Header
		want   time.Time
		name   string
	}{
		{
			name:   "a_throttle_carries_its_reset",
			header: headerPairs("RateLimit-Remaining", "0", "RateLimit-Reset", "1790000000", "RateLimit-Name", "throttle_authenticated_api"),
			want:   time.Unix(1790000000, 0).UTC(),
		},
		{name: "a_throttle_with_no_figures_has_none"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fe := answered(t, http.StatusTooManyRequests, test.header, `{"message":"Retry later"}`)
			if fe.Kind != forgeapi.KindRateLimited || !fe.Reset.Equal(test.want) {
				t.Errorf("Error = kind %v, reset %v, want kind %v, reset %v", fe.Kind, fe.Reset, forgeapi.KindRateLimited, test.want)
			}
		})
	}
}

// TestErrorSecondary holds Error.Secondary to GitHub's limit alone: a GitLab
// throttle never sets it, whatever its headers and message say.
func TestErrorSecondary(t *testing.T) {
	fe := answered(t, http.StatusTooManyRequests,
		headerPairs("Retry-After", "60", "RateLimit-Remaining", "0", "RateLimit-Name", "throttle_authenticated_api"),
		`{"message":"You have exceeded a secondary rate limit"}`)
	if fe.Kind != forgeapi.KindRateLimited || fe.Secondary {
		t.Errorf("Error = kind %v, secondary %t, want kind %v, secondary false", fe.Kind, fe.Secondary, forgeapi.KindRateLimited)
	}
}
