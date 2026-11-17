package github

import (
	"net/http"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
)

// secondaryMessage is the body GitHub answers a secondary limit with
// (https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api#exceeding-the-rate-limit).
const secondaryMessage = `{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again.","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-secondary-rate-limits"}`

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
// x-ratelimit-reset, and to the zero time where the answer carries none.
func TestErrorResetFromAnswer(t *testing.T) {
	reset := time.Unix(1790000000, 0).UTC()
	for _, test := range []struct {
		header http.Header
		want   time.Time
		name   string
		status int
	}{
		{
			name: "a_primary_limit_carries_its_reset", status: http.StatusForbidden, want: reset,
			header: headerPairs("X-RateLimit-Remaining", "0", "X-RateLimit-Reset", "1790000000"),
		},
		{
			name: "a_refusal_that_is_no_limit_carries_its_reset", status: http.StatusNotFound, want: reset,
			header: headerPairs("X-RateLimit-Remaining", "4000", "X-RateLimit-Reset", "1790000000"),
		},
		{name: "a_429_with_no_figures_has_none", status: http.StatusTooManyRequests},
		{
			name: "a_remaining_figure_with_no_reset_has_none", status: http.StatusForbidden,
			header: headerPairs("X-RateLimit-Remaining", "0"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fe := answered(t, test.status, test.header, `{"message":"refused"}`)
			if !fe.Reset.Equal(test.want) {
				t.Errorf("Error.Reset = %v, want %v", fe.Reset, test.want)
			}
		})
	}
}

// TestErrorSecondary holds Error.Secondary to GitHub's two secondary-limit
// signals, the message and Retry-After, whatever the remaining figure reads; a
// 403 carrying only the message is a rate limit, never a permission refusal.
func TestErrorSecondary(t *testing.T) {
	for _, test := range []struct {
		header    http.Header
		name      string
		body      string
		status    int
		kind      forgeapi.ErrorKind
		secondary bool
	}{
		{
			name: "a_403_carrying_only_the_secondary_message", status: http.StatusForbidden, body: secondaryMessage,
			header: headerPairs("X-RateLimit-Remaining", "4000"), kind: forgeapi.KindRateLimited, secondary: true,
		},
		{
			name: "a_429_carrying_only_the_secondary_message", status: http.StatusTooManyRequests, body: secondaryMessage,
			header: headerPairs("X-RateLimit-Remaining", "4000"), kind: forgeapi.KindRateLimited, secondary: true,
		},
		{
			name: "a_403_with_retry_after", status: http.StatusForbidden,
			header: headerPairs("Retry-After", "60", "X-RateLimit-Remaining", "4000"), kind: forgeapi.KindRateLimited, secondary: true,
		},
		{
			name: "a_429_with_retry_after", status: http.StatusTooManyRequests,
			header: headerPairs("Retry-After", "60"), kind: forgeapi.KindRateLimited, secondary: true,
		},
		{
			name: "a_403_at_remaining_0_with_neither_is_a_primary_limit", status: http.StatusForbidden,
			header: headerPairs("X-RateLimit-Remaining", "0"), kind: forgeapi.KindRateLimited,
		},
		{name: "a_bare_429", status: http.StatusTooManyRequests, kind: forgeapi.KindRateLimited},
		{
			name: "a_bare_403_is_a_refusal", status: http.StatusForbidden,
			header: headerPairs("X-RateLimit-Remaining", "4000"), kind: forgeapi.KindForbidden,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := test.body
			if body == "" {
				body = `{"message":"refused"}`
			}
			fe := answered(t, test.status, test.header, body)
			if fe.Kind != test.kind || fe.Secondary != test.secondary {
				t.Errorf("Error = kind %v, secondary %t, want kind %v, secondary %t", fe.Kind, fe.Secondary, test.kind, test.secondary)
			}
		})
	}
}
