// Package githubtest is GitHub's rate-limit answers as cases a GitHub
// classifier runs: what [github.ReadLimit] reads from each, the kind the github
// client's error answers it with, and what a classifier's hold covers
// (https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api).
package githubtest

import (
	"net/http"
	"strconv"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/github"
)

// LimitCase is one answer GitHub gives and what each reader makes of it.
type LimitCase struct {
	// Header is the full X-RateLimit-* set a real answer carries (limit,
	// remaining, used, reset and resource), with Retry-After where the case
	// has one.
	Header http.Header
	// Body is GitHub's own JSON error body, except the case whose body is a
	// proxy's page.
	Body []byte
	// Name names the case, fit for a subtest name.
	Name string
	// Hold is what a GitHub classifier hands its gate for the answer.
	Hold Hold
	// Read is what [github.ReadLimit] answers.
	Read github.RateLimit
	// Status is the answer's HTTP status.
	Status int
	// Kind is what the github client's *forgeapi.Error answers.
	Kind forgeapi.ErrorKind
}

// Hold is what a GitHub classifier holds for one answer, in forgeapi's terms.
// The zero Hold holds nothing.
type Hold struct {
	// Reset is the end the classifier passes: Read.Reset where Read.Remaining
	// is 0, else the zero time.
	Reset time.Time
	// Pool is the pool held, empty for the whole connection: a secondary limit
	// holds every pool, whatever Read.Pool names.
	Pool string
	// RetryAfter is the wait the answer asked for, zero where it asked none.
	RetryAfter time.Duration
	// Limited reports that the classifier holds. Holding with a zero
	// RetryAfter and a zero Reset is the back-off curve from one minute.
	Limited bool
}

// The bodies GitHub answers each case with.
const (
	secondaryBody = `{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again.",` +
		`"documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-secondary-rate-limits","status":"403"}`
	retiredBody = `{"message":"You have triggered an abuse detection mechanism. Please wait a few minutes before you try again."}`
	primaryBody = `{"message":"API rate limit exceeded for user ID 1.",` +
		`"documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-the-primary-rate-limits","status":"403"}`
	tooManyBody      = `{"message":"Too Many Requests","documentation_url":"https://docs.github.com/rest","status":"429"}`
	unauthorizedBody = `{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest","status":"401"}`
	forbiddenBody    = `{"message":"Resource not accessible by personal access token",` +
		`"documentation_url":"https://docs.github.com/rest/actions/workflow-runs","status":"403"}`
	notFoundBody = `{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`
	proxyBody    = "<html><body><h1>403 Forbidden</h1><p>Request refused under this proxy's secondary rate limit policy.</p></body></html>"
)

// coreLimit is the core pool's whole limit for an authenticated user.
const coreLimit = 5000

// LimitCases answers GitHub's rate-limit answers as of now: a secondary limit
// by its Retry-After, by its message at remaining 0 and by its message alone
// at remaining 4000, the retired abuse-detection wording carrying
// Retry-After, a primary limit, any other 429, a 401, a 403 that is no limit,
// a 404, and a proxy's non-JSON page carrying the term, which is no limit.
// Each reset is whole epoch seconds after now.
func LimitCases(now time.Time) []LimitCase {
	soon := time.Unix(now.Add(30*time.Minute).Unix(), 0).UTC()
	later := time.Unix(now.Add(50*time.Minute).Unix(), 0).UTC()
	return []LimitCase{
		{
			Name: "a_secondary_limit_with_retry_after", Status: http.StatusForbidden,
			Header: header(4000, later, 120), Body: []byte(secondaryBody),
			Read: read(true, true, 4000, later, 2*time.Minute), Kind: forgeapi.KindRateLimited,
			Hold: Hold{Limited: true, RetryAfter: 2 * time.Minute},
		},
		{
			Name: "a_secondary_limit_at_remaining_0", Status: http.StatusForbidden,
			Header: header(0, soon, 0), Body: []byte(secondaryBody),
			Read: read(true, true, 0, soon, 0), Kind: forgeapi.KindRateLimited,
			Hold: Hold{Limited: true, Reset: soon},
		},
		{
			Name: "a_secondary_limit_by_its_message_alone", Status: http.StatusForbidden,
			Header: header(4000, later, 0), Body: []byte(secondaryBody),
			Read: read(true, true, 4000, later, 0), Kind: forgeapi.KindRateLimited,
			Hold: Hold{Limited: true},
		},
		{
			Name: "the_retired_wording_with_retry_after", Status: http.StatusForbidden,
			Header: header(4000, later, 60), Body: []byte(retiredBody),
			Read: read(true, true, 4000, later, time.Minute), Kind: forgeapi.KindRateLimited,
			Hold: Hold{Limited: true, RetryAfter: time.Minute},
		},
		{
			Name: "a_primary_limit", Status: http.StatusForbidden,
			Header: header(0, soon, 0), Body: []byte(primaryBody),
			Read: read(true, false, 0, soon, 0), Kind: forgeapi.KindRateLimited,
			Hold: Hold{Limited: true, Pool: "core", Reset: soon},
		},
		{
			Name: "any_other_429", Status: http.StatusTooManyRequests,
			Header: header(4000, later, 0), Body: []byte(tooManyBody),
			Read: read(true, false, 4000, later, 0), Kind: forgeapi.KindRateLimited,
			Hold: Hold{Limited: true},
		},
		{
			Name: "a_401", Status: http.StatusUnauthorized,
			Header: header(4000, later, 0), Body: []byte(unauthorizedBody),
			Read: read(false, false, 4000, later, 0), Kind: forgeapi.KindUnauthorized,
		},
		{
			Name: "a_403_that_is_no_limit", Status: http.StatusForbidden,
			Header: header(4000, later, 0), Body: []byte(forbiddenBody),
			Read: read(false, false, 4000, later, 0), Kind: forgeapi.KindForbidden,
		},
		{
			Name: "a_404", Status: http.StatusNotFound,
			Header: header(4000, later, 0), Body: []byte(notFoundBody),
			Read: read(false, false, 4000, later, 0), Kind: forgeapi.KindNotFound,
		},
		{
			Name: "a_proxy_page_carrying_the_term", Status: http.StatusForbidden,
			Header: header(4000, later, 0), Body: []byte(proxyBody),
			Read: read(false, false, 4000, later, 0), Kind: forgeapi.KindForbidden,
		},
	}
}

// header is the core pool's X-RateLimit-* set at remaining, renewing at reset,
// with a Retry-After of retryAfter seconds where it is above zero.
func header(remaining int, reset time.Time, retryAfter int) http.Header {
	h := http.Header{}
	h.Set("X-RateLimit-Limit", strconv.Itoa(coreLimit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	h.Set("X-RateLimit-Used", strconv.Itoa(coreLimit-remaining))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
	h.Set("X-RateLimit-Resource", "core")
	if retryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(retryAfter))
	}
	return h
}

// read is what [github.ReadLimit] answers for a core-pool answer.
func read(limited, secondary bool, remaining int, reset time.Time, retryAfter time.Duration) github.RateLimit {
	return github.RateLimit{
		Limited: limited, Secondary: secondary, Remaining: remaining, Limit: coreLimit,
		Reset: reset, Pool: "core", RetryAfter: retryAfter,
	}
}
