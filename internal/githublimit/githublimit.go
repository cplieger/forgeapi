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

// Pool names the rate-limit pool an answer drew on, core for REST and graphql
// for documents among them, and that pool's whole limit, zero where the answer
// states none
// (https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api#checking-the-status-of-your-rate-limit).
func Pool(header http.Header) (name string, limit int) {
	limit, err := strconv.Atoi(strings.TrimSpace(header.Get("X-RateLimit-Limit")))
	if err != nil || limit < 0 {
		limit = 0
	}
	return strings.TrimSpace(header.Get("X-RateLimit-Resource")), limit
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
		return quota(header, said)
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
	return secondary(header, said)
}

// DocumentLimited is [Limited] and [Secondary] for a document answer carrying
// errors and no data, which GitHub answers a limit with at 200 too: a primary
// limit with the remaining figure at zero, a secondary limit with its message
// (https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api#exceeding-the-rate-limit).
func DocumentLimited(status int, header http.Header, said bool) (limited, isSecondary bool) {
	if status != http.StatusOK {
		return Limited(status, header, said), Secondary(status, header, said)
	}
	return quota(header, said), secondary(header, said)
}

// quota reports a refusal that is a secondary limit or whose remaining figure
// is at zero.
func quota(header http.Header, said bool) bool {
	remaining, _, ok := Signal(header)
	return secondary(header, said) || (ok && remaining <= 0)
}

func secondary(header http.Header, said bool) bool {
	return said || header.Get("Retry-After") != ""
}
