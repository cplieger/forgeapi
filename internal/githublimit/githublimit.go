// Package githublimit reads GitHub's rate-limit answers, for every package
// that reaches GitHub's API: the github client and the creds package's App
// mint
// (https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api).
package githublimit

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Signal reads the budget signal riding every answer on both of GitHub's
// surfaces: the remaining units of the pool the answer drew on and the instant
// that window renews, as epoch seconds; ok is false where the answer carries no
// remaining figure. The reset is the zero time where the answer carries none.
//
// It is read from the answer and never from the rate-limit endpoint, which is
// measured to report a full pool with a fresh window while the same
// credential's document budget was demonstrably drawn down.
func Signal(header http.Header) (remaining int, reset time.Time, ok bool) {
	raw := header.Get("X-RateLimit-Remaining")
	if raw == "" {
		return 0, time.Time{}, false
	}
	remaining, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, time.Time{}, false
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Reset")), 10, 64)
	if err != nil {
		return remaining, time.Time{}, true
	}
	return remaining, time.Unix(seconds, 0).UTC(), true
}

// SecondaryMessage reports whether a refusal's message member is the one
// GitHub answers a secondary limit with, whose wording past that phrase varies.
func SecondaryMessage(message string) bool {
	return strings.Contains(strings.ToLower(message), "secondary rate limit")
}

// Limited reports whether a refusal is GitHub's quota rather than a
// permission: a 429, or a 403 that is [Secondary] or whose remaining figure is
// at zero. said reports that the refusal's message is [SecondaryMessage]'s.
func Limited(status int, header http.Header, said bool) bool {
	switch status {
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		remaining, _, ok := Signal(header)
		return Secondary(status, header, said) || (ok && remaining <= 0)
	}
	return false
}

// Secondary reports whether a 403 or 429 is a secondary limit: it carries
// Retry-After, or its message is the secondary limit's (said), whatever its
// remaining figure reads.
func Secondary(status int, header http.Header, said bool) bool {
	if status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return false
	}
	return said || header.Get("Retry-After") != ""
}
