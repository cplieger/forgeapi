package gitea

import (
	"net/http"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
)

// answered is the *forgeapi.Error a ListRuns answered 429 with a RateLimit
// header of raw, none where raw is empty, earns.
func answered(t *testing.T, raw string) *forgeapi.Error {
	t.Helper()
	header := http.Header{}
	if raw != "" {
		header.Set("RateLimit", raw)
	}
	h := newAnsweringHarness(t, http.StatusTooManyRequests, header, `{"message":"refused"}`)
	_, err := h.client.ListRuns(t.Context(), testRef())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("ListRuns answered 429 = %v, want a *forgeapi.Error", err)
	}
	return fe
}

// TestErrorResetFromAnswer holds Error.Reset to the window Forgejo's RateLimit
// header states in its t parameter, seconds from the answer
// (https://datatracker.ietf.org/doc/draft-ietf-httpapi-ratelimit-headers/), and to
// the zero time where the answer states none: Gitea sends no header at all.
func TestErrorResetFromAnswer(t *testing.T) {
	t.Run("forgejo_states_its_window", func(t *testing.T) {
		before := time.Now()
		fe := answered(t, `"baseline";r=0;t=60`)
		after := time.Now()
		if fe.Reset.Before(before.Add(time.Minute)) || fe.Reset.After(after.Add(time.Minute)) {
			t.Errorf("Error.Reset = %v, want a minute after the answer, between %v and %v", fe.Reset, before.Add(time.Minute), after.Add(time.Minute))
		}
	})
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "a_remaining_figure_with_no_window_has_none", raw: `"baseline";r=0`},
		{name: "gitea_sends_no_header"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if fe := answered(t, test.raw); !fe.Reset.IsZero() {
				t.Errorf("Error.Reset = %v, want the zero time", fe.Reset)
			}
		})
	}
}

// TestErrorSecondary holds Error.Secondary to GitHub's limit alone: neither
// product of this family sets it.
func TestErrorSecondary(t *testing.T) {
	if fe := answered(t, `"baseline";r=0;t=60`); fe.Kind != forgeapi.KindRateLimited || fe.Secondary {
		t.Errorf("Error = kind %v, secondary %t, want kind %v, secondary false", fe.Kind, fe.Secondary, forgeapi.KindRateLimited)
	}
}

// TestErrorRemainingFromAnswer holds Error.Remaining to the r parameter of
// Forgejo's RateLimit header, and to BudgetRemainingUnknown where the answer
// carries none, as Gitea's never does. Neither product names a pool.
func TestErrorRemainingFromAnswer(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want int
	}{
		{name: "forgejo_states_its_figure", raw: `"baseline";r=3;t=60`, want: 3},
		{name: "gitea_sends_no_header", want: forgeapi.BudgetRemainingUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			if fe := answered(t, test.raw); fe.Remaining != test.want || fe.Pool != "" {
				t.Errorf("Error = remaining %d, pool %q, want remaining %d, no pool", fe.Remaining, fe.Pool, test.want)
			}
		})
	}
}

// TestAForbiddenAnswerAtZeroIsARateLimit holds a 403 to the one budget reading
// the signal takes: Forgejo's RateLimit header at r=0 is a rate limit.
func TestAForbiddenAnswerAtZeroIsARateLimit(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want forgeapi.ErrorKind
	}{
		{name: "at_zero", raw: `"baseline";r=0;t=60`, want: forgeapi.KindRateLimited},
		{name: "above_zero", raw: `"baseline";r=3;t=60`, want: forgeapi.KindForbidden},
		{name: "no_header", want: forgeapi.KindForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := http.Header{}
			if test.raw != "" {
				header.Set("RateLimit", test.raw)
			}
			if kind, _ := mapped("ListRuns", http.StatusForbidden, header); kind != test.want {
				t.Errorf("mapper(ListRuns, 403, %v) = %v, want %v", header, kind, test.want)
			}
		})
	}
}
