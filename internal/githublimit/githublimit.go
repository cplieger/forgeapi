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

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/transport"
)

// secondaryTerm is the phrase GitHub's secondary-limit message carries, whose
// wording past it varies
// (https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api#exceeding-the-rate-limit).
const secondaryTerm = "secondary rate limit"

// Limit is one answer's rate-limit facts, as github.RateLimit documents them:
// github.ReadLimit converts one into the other, so their fields stay alike.
type Limit struct {
	Reset      time.Time
	Pool       string
	RetryAfter time.Duration
	Remaining  int
	Limit      int
	Limited    bool
	Secondary  bool
}

// Read reads one answer's rate-limit facts from its status, header and body,
// the body's JSON message member being the one place the secondary-limit
// message is read.
func Read(status int, header http.Header, body []byte) Limit {
	said := SecondaryMessage(Message(body))
	l := Limit{
		Remaining:  forgeapi.BudgetRemainingUnknown,
		RetryAfter: transport.RetryAfter(header),
		Limited:    isLimited(status, header, said),
		Secondary:  isSecondary(status, header, said),
	}
	remaining, reset, ok := Signal(header)
	if l.Reset = reset; ok {
		l.Remaining = remaining
	}
	l.Pool, l.Limit = Pool(header)
	return l
}

// Message is the message member of a GitHub refusal's JSON body, empty where
// the body carries none or is not JSON. It is GitHub's text, so a caller
// quotes it before showing it.
func Message(body []byte) string {
	var refusal struct {
		Message string `json:"message"`
	}
	if transport.Decode(body, &refusal) != nil {
		return ""
	}
	return refusal.Message
}

// Signal reads the budget signal riding every answer on both of GitHub's
// surfaces: the remaining units of the pool the answer drew on, ok false where
// the answer carries no readable remaining figure, and the instant that window
// renews, as epoch seconds, whatever ok reads; the zero time where the answer
// carries no reset.
//
// It is read from the answer and never from the rate-limit endpoint, which is
// measured to report a full pool with a fresh window while the same
// credential's document budget was demonstrably drawn down.
func Signal(header http.Header) (remaining int, reset time.Time, ok bool) {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Reset")), 10, 64); err == nil {
		reset = time.Unix(seconds, 0).UTC()
	}
	remaining, err := strconv.Atoi(strings.TrimSpace(header.Get("X-RateLimit-Remaining")))
	return remaining, reset, err == nil
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

// SecondaryMessage reports whether a message is the one GitHub answers a
// secondary limit with: it carries [secondaryTerm] in any case.
func SecondaryMessage(message string) bool {
	return strings.Contains(strings.ToLower(message), secondaryTerm)
}

// isLimited reports whether a refusal is GitHub's quota rather than a
// permission: a 429, or a 403 that is secondary or whose remaining figure is
// at zero. said reports that the refusal's message is [SecondaryMessage]'s.
func isLimited(status int, header http.Header, said bool) bool {
	switch status {
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		return quota(header, said)
	}
	return false
}

// isSecondary reports whether a 403 or 429 is a secondary limit: it carries
// Retry-After, or its message is the secondary limit's (said), whatever its
// remaining figure reads.
func isSecondary(status int, header http.Header, said bool) bool {
	if status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return false
	}
	return secondary(header, said)
}

// DocumentLimited is [Read]'s Limited and Secondary for a document answer
// carrying errors and no data, which GitHub answers a limit with at 200 too: a primary
// limit with the remaining figure at zero, a secondary limit with its message
// (https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api#exceeding-the-rate-limit).
func DocumentLimited(status int, header http.Header, said bool) (limited, secondaryLimit bool) {
	if status != http.StatusOK {
		return isLimited(status, header, said), isSecondary(status, header, said)
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
