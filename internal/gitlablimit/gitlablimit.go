// Package gitlablimit reads GitLab's rate-limit figures off an answer, for every
// package that reaches a GitLab instance: the gitlab client and the creds
// package's OAuth endpoints, whose answers carry them too
// (https://docs.gitlab.com/administration/settings/user_and_ip_rate_limits/#response-headers).
package gitlablimit

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Signal reads this product's budget signal, which rides every answer, a
// refusal included: the remaining figure of the throttle the request counted
// against, ok false where the answer carries no readable one, and the instant
// its window renews, as epoch seconds, whatever ok reads; the zero time where
// the answer carries no reset.
func Signal(header http.Header) (remaining int, reset time.Time, ok bool) {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(header.Get("RateLimit-Reset")), 10, 64); err == nil {
		reset = time.Unix(seconds, 0).UTC()
	}
	remaining, err := strconv.Atoi(strings.TrimSpace(header.Get("RateLimit-Remaining")))
	return remaining, reset, err == nil
}

// Pool names the throttle an answer reports and its whole limit, the most
// restrictive one the request counted against: on gitlab.com
// throttle_authenticated_api, 2000 a minute, for most routes and
// burst_traffic_per_user_free_plan, 100 a minute, for the project listings
// (measured 2026-10-20).
func Pool(header http.Header) (name string, limit int) {
	limit, err := strconv.Atoi(strings.TrimSpace(header.Get("RateLimit-Limit")))
	if err != nil || limit < 0 {
		limit = 0
	}
	return strings.TrimSpace(header.Get("RateLimit-Name")), limit
}
