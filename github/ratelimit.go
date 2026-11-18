package github

import (
	"net/http"
	"time"

	"github.com/cplieger/forgeapi/v3/internal/githublimit"
)

// RateLimit is what one GitHub answer says about its rate limits, read from that
// answer alone: each figure is the answer's own header, on any answer, a
// success's included
// (https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api).
type RateLimit struct {
	// Reset is X-RateLimit-Reset as sent, whatever Remaining reads; the zero
	// time where the answer carries no reset or no readable remaining figure.
	Reset time.Time
	// Pool is X-RateLimit-Resource as sent, a secondary limit's included;
	// empty where absent.
	Pool string
	// RetryAfter is the wait Retry-After asks for, zero where absent.
	RetryAfter time.Duration
	// Remaining is X-RateLimit-Remaining as sent;
	// [forgeapi.BudgetRemainingUnknown] where absent or unreadable.
	Remaining int
	// Limit is X-RateLimit-Limit as sent, zero where absent.
	Limit int
	// Limited reports a rate limit: a 429, or a 403 carrying Retry-After,
	// X-RateLimit-Remaining 0 or the secondary-limit message.
	Limited bool
	// Secondary reports a secondary limit: Limited, and the body's JSON
	// message member names a secondary rate limit or the answer carries
	// Retry-After.
	Secondary bool
}

// ReadLimit reads one GitHub answer's rate-limit facts from its status, its
// header and its body, the one reading the client's [forgeapi.Error] figures
// and the creds package's App mint take. The secondary-limit message is the
// body's JSON message member carrying "secondary rate limit" in any case; the
// phrase anywhere else, or in a body that is not JSON, is none.
func ReadLimit(status int, h http.Header, body []byte) RateLimit {
	return RateLimit(githublimit.Read(status, h, body))
}
