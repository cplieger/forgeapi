// Package githubtest is GitHub's rate-limit answers as cases a GitHub
// classifier runs: what [github.ReadLimit], or the github client's GraphQL
// document path, reads from each, the kind the github client's error answers it
// with, and the effect a classifier answers its gate with
// (https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api,
// https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api).
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
	// Body is GitHub's own JSON body, a REST error's or a document's envelope,
	// except the case whose body is a proxy's page.
	Body []byte
	// Name names the case, fit for a subtest name.
	Name string
	// Effect is what a GitHub classifier answers its gate with for the answer.
	Effect Effect
	// Read is what [github.ReadLimit] answers; on a Document case, the facts the
	// document path puts on the client's *forgeapi.Error, since ReadLimit reads
	// REST answers alone.
	Read github.RateLimit
	// Status is the answer's HTTP status.
	Status int
	// Kind is what the github client's *forgeapi.Error answers.
	Kind forgeapi.ErrorKind
	// Document reports an answer to a GraphQL document: Body is its envelope.
	Document bool
}

// Effect is a GitHub classifier's whole answer for one case, in forgeapi's
// terms. The zero Effect is OK: no hold and no step toward a disable.
type Effect struct {
	// Reset is the end the classifier passes where Limited: Read.Reset where
	// Read.Remaining is 0, else the zero time.
	Reset time.Time
	// Pool is the pool held where Limited, empty for the whole connection: a
	// secondary limit holds every pool, whatever Read.Pool names.
	Pool string
	// RetryAfter is Read.RetryAfter where Limited, uncapped.
	RetryAfter time.Duration
	// Limited reports that the gate holds. Holding with a zero RetryAfter and
	// a zero Reset is the back-off curve from one minute.
	Limited bool
	// Refused reports that the credential was refused: a step toward a disable.
	Refused bool
	// Attributed reports a Refused credential's own refusal: disabled at once.
	Attributed bool
}

// The bodies GitHub answers each case with.
const (
	secondaryMessage = "You have exceeded a secondary rate limit. Please wait a few minutes before you try again."
	retiredMessage   = "You have triggered an abuse detection mechanism. Please wait a few minutes before you try again."
	primaryMessage   = "API rate limit exceeded for user ID 1."

	secondaryBody = `{"message":"` + secondaryMessage + `",` +
		`"documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-secondary-rate-limits","status":"403"}`
	retiredBody = `{"message":"` + retiredMessage + `"}`
	primaryBody = `{"message":"` + primaryMessage + `",` +
		`"documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-the-primary-rate-limits","status":"403"}`
	tooManyBody      = `{"message":"Too Many Requests","documentation_url":"https://docs.github.com/rest","status":"429"}`
	unauthorizedBody = `{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest","status":"401"}`
	forbiddenBody    = `{"message":"Resource not accessible by personal access token",` +
		`"documentation_url":"https://docs.github.com/rest/actions/workflow-runs","status":"403"}`
	notFoundBody = `{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`
	proxyBody    = "<html><body><h1>403 Forbidden</h1><p>Request refused under this proxy's secondary rate limit policy.</p></body></html>"

	primaryEnvelope   = `{"data":null,"errors":[{"type":"RATE_LIMITED","message":"` + primaryMessage + `"}]}`
	retiredEnvelope   = `{"data":null,"errors":[{"message":"` + retiredMessage + `"}]}`
	secondaryEnvelope = `{"data":null,"errors":[{"message":"` + secondaryMessage + `"}]}`
)

// poolLimit is each pool's whole limit for an authenticated user: requests on
// core, points on graphql.
const poolLimit = 5000

// LimitCases answers GitHub's rate-limit answers as of now, each reset whole
// epoch seconds after now. On REST, at the core pool: a secondary limit by its
// Retry-After, by its message at remaining 0 and alone at remaining 4000, the
// retired abuse-detection wording with Retry-After, a primary limit, any other
// 429, a 401, a 403 that is no limit, a 404, and a proxy's non-JSON page carrying
// the term. On a document, at the graphql pool: a primary limit at 200, secondary
// limits at 200 by Retry-After and by the message, at 403 by the message, and at
// 403 and 429 at remaining 0 with the message in the envelope's errors.
func LimitCases(now time.Time) []LimitCase {
	return append(restCases(now), documentCases(now)...)
}

func restCases(now time.Time) []LimitCase {
	soon := time.Unix(now.Add(30*time.Minute).Unix(), 0).UTC()
	later := time.Unix(now.Add(50*time.Minute).Unix(), 0).UTC()
	const pool = "core"
	return []LimitCase{
		{
			Name: "a_secondary_limit_with_retry_after", Status: http.StatusForbidden,
			Header: header(pool, 4000, later, 120), Body: []byte(secondaryBody),
			Read: read(pool, true, true, 4000, later, 2*time.Minute), Kind: forgeapi.KindRateLimited,
			Effect: Effect{Limited: true, RetryAfter: 2 * time.Minute},
		},
		{
			Name: "a_secondary_limit_at_remaining_0", Status: http.StatusForbidden,
			Header: header(pool, 0, soon, 0), Body: []byte(secondaryBody),
			Read: read(pool, true, true, 0, soon, 0), Kind: forgeapi.KindRateLimited,
			Effect: Effect{Limited: true, Reset: soon},
		},
		{
			Name: "a_secondary_limit_by_its_message_alone", Status: http.StatusForbidden,
			Header: header(pool, 4000, later, 0), Body: []byte(secondaryBody),
			Read: read(pool, true, true, 4000, later, 0), Kind: forgeapi.KindRateLimited,
			Effect: Effect{Limited: true},
		},
		{
			Name: "the_retired_wording_with_retry_after", Status: http.StatusForbidden,
			Header: header(pool, 4000, later, 60), Body: []byte(retiredBody),
			Read: read(pool, true, true, 4000, later, time.Minute), Kind: forgeapi.KindRateLimited,
			Effect: Effect{Limited: true, RetryAfter: time.Minute},
		},
		{
			Name: "a_primary_limit", Status: http.StatusForbidden,
			Header: header(pool, 0, soon, 0), Body: []byte(primaryBody),
			Read: read(pool, true, false, 0, soon, 0), Kind: forgeapi.KindRateLimited,
			Effect: Effect{Limited: true, Pool: pool, Reset: soon},
		},
		{
			Name: "any_other_429", Status: http.StatusTooManyRequests,
			Header: header(pool, 4000, later, 0), Body: []byte(tooManyBody),
			Read: read(pool, true, false, 4000, later, 0), Kind: forgeapi.KindRateLimited,
			Effect: Effect{Limited: true},
		},
		{
			Name: "a_401", Status: http.StatusUnauthorized,
			Header: header(pool, 4000, later, 0), Body: []byte(unauthorizedBody),
			Read: read(pool, false, false, 4000, later, 0), Kind: forgeapi.KindUnauthorized,
			Effect: Effect{Refused: true, Attributed: true},
		},
		{
			Name: "a_403_that_is_no_limit", Status: http.StatusForbidden,
			Header: header(pool, 4000, later, 0), Body: []byte(forbiddenBody),
			Read: read(pool, false, false, 4000, later, 0), Kind: forgeapi.KindForbidden,
		},
		{
			Name: "a_404", Status: http.StatusNotFound,
			Header: header(pool, 4000, later, 0), Body: []byte(notFoundBody),
			Read: read(pool, false, false, 4000, later, 0), Kind: forgeapi.KindNotFound,
		},
		{
			Name: "a_proxy_page_carrying_the_term", Status: http.StatusForbidden,
			Header: header(pool, 4000, later, 0), Body: []byte(proxyBody),
			Read: read(pool, false, false, 4000, later, 0), Kind: forgeapi.KindForbidden,
		},
	}
}

func documentCases(now time.Time) []LimitCase {
	soon := time.Unix(now.Add(30*time.Minute).Unix(), 0).UTC()
	later := time.Unix(now.Add(50*time.Minute).Unix(), 0).UTC()
	const pool = "graphql"
	return []LimitCase{
		{
			Name: "a_document_primary_limit_at_200", Status: http.StatusOK, Document: true,
			Header: header(pool, 0, soon, 0), Body: []byte(primaryEnvelope),
			Read: read(pool, true, false, 0, soon, 0), Kind: forgeapi.KindRateLimited,
			Effect: Effect{Limited: true, Pool: pool, Reset: soon},
		},
		{
			Name: "a_document_secondary_limit_at_200_by_retry_after", Status: http.StatusOK, Document: true,
			Header: header(pool, 4000, later, 120), Body: []byte(retiredEnvelope),
			Read: read(pool, true, true, 4000, later, 2*time.Minute), Kind: forgeapi.KindRateLimited,
			Effect: Effect{Limited: true, RetryAfter: 2 * time.Minute},
		},
		{
			Name: "a_document_secondary_limit_at_200_by_its_message", Status: http.StatusOK, Document: true,
			Header: header(pool, 4000, later, 0), Body: []byte(secondaryEnvelope),
			Read: read(pool, true, true, 4000, later, 0), Kind: forgeapi.KindRateLimited,
			Effect: Effect{Limited: true},
		},
		{
			Name: "a_document_secondary_limit_at_403_by_its_message", Status: http.StatusForbidden, Document: true,
			Header: header(pool, 4000, later, 0), Body: []byte(secondaryEnvelope),
			Read: read(pool, true, true, 4000, later, 0), Kind: forgeapi.KindRateLimited,
			Effect: Effect{Limited: true},
		},
		{
			Name: "a_document_secondary_limit_at_403_at_remaining_0", Status: http.StatusForbidden, Document: true,
			Header: header(pool, 0, soon, 0), Body: []byte(secondaryEnvelope),
			Read: read(pool, true, true, 0, soon, 0), Kind: forgeapi.KindRateLimited,
			Effect: Effect{Limited: true, Reset: soon},
		},
		{
			Name: "a_document_secondary_limit_at_429_at_remaining_0", Status: http.StatusTooManyRequests, Document: true,
			Header: header(pool, 0, soon, 0), Body: []byte(secondaryEnvelope),
			Read: read(pool, true, true, 0, soon, 0), Kind: forgeapi.KindRateLimited,
			Effect: Effect{Limited: true, Reset: soon},
		},
	}
}

// header is pool's X-RateLimit-* set at remaining, renewing at reset, with a
// Retry-After of retryAfter seconds where it is above zero.
func header(pool string, remaining int, reset time.Time, retryAfter int) http.Header {
	h := http.Header{}
	h.Set("X-RateLimit-Limit", strconv.Itoa(poolLimit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	h.Set("X-RateLimit-Used", strconv.Itoa(poolLimit-remaining))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
	h.Set("X-RateLimit-Resource", pool)
	if retryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(retryAfter))
	}
	return h
}

// read is what an answer at pool reads as.
func read(pool string, limited, secondary bool, remaining int, reset time.Time, retryAfter time.Duration) github.RateLimit {
	return github.RateLimit{
		Limited: limited, Secondary: secondary, Remaining: remaining, Limit: poolLimit,
		Reset: reset, Pool: pool, RetryAfter: retryAfter,
	}
}
