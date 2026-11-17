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

// TestErrorNamesPool holds Error.Pool to the RateLimit-Name the failing answer
// names, and to none where it names none.
func TestErrorNamesPool(t *testing.T) {
	for _, test := range []struct {
		header http.Header
		name   string
		want   string
	}{
		{
			name: "a_throttle_names_its_pool", want: "throttle_authenticated_api",
			header: headerPairs("RateLimit-Name", "throttle_authenticated_api", "RateLimit-Remaining", "0"),
		},
		{name: "a_throttle_naming_none", header: headerPairs("RateLimit-Remaining", "0")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if fe := answered(t, http.StatusTooManyRequests, test.header, `{"message":"refused"}`); fe.Pool != test.want {
				t.Errorf("Error.Pool = %q, want %q", fe.Pool, test.want)
			}
		})
	}
}

// TestErrorRemainingFromAnswer holds Error.Remaining to the failing answer's
// own RateLimit-Remaining, and to BudgetRemainingUnknown where it carries none.
func TestErrorRemainingFromAnswer(t *testing.T) {
	for _, test := range []struct {
		header http.Header
		name   string
		want   int
	}{
		{name: "a_throttle_carries_its_figure", header: headerPairs("RateLimit-Remaining", "0")},
		{name: "a_throttle_with_no_figure", want: forgeapi.BudgetRemainingUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			if fe := answered(t, http.StatusTooManyRequests, test.header, `{"message":"refused"}`); fe.Remaining != test.want {
				t.Errorf("Error.Remaining = %d, want %d", fe.Remaining, test.want)
			}
		})
	}
}

// TestBudgetStatePools holds BudgetState.Pools to each RateLimit-Name an
// answer reported, with the API-wide throttle less the request an answer
// under a narrower limit also counted against it
// (https://docs.gitlab.com/administration/settings/user_and_ip_rate_limits/#response-headers).
func TestBudgetStatePools(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v4/user": `{"username": "example-user", "name": "Example User",
  "email": "example-user@forge.example", "web_url": "https://forge.example/example-user"}`,
		labelsRoute: `[]`,
	})
	answer := func(name, remaining, limit, reset string) {
		for _, pair := range [][2]string{{"RateLimit-Name", name}, {"RateLimit-Remaining", remaining}, {"RateLimit-Limit", limit}, {"RateLimit-Reset", reset}} {
			h.instance.answerHeader(pair[0], pair[1])
		}
	}
	answer("throttle_authenticated_api", "1990", "2000", "1890000060")
	if _, err := h.client.Whoami(t.Context()); err != nil {
		t.Fatalf("Setup: Whoami = %v", err)
	}
	answer("burst_traffic_per_user_free_plan", "99", "100", "1890000030")
	if _, err := h.client.ListLabels(t.Context(), testRef()); err != nil {
		t.Fatalf("Setup: ListLabels = %v", err)
	}
	got := h.client.BudgetState().Pools
	want := []forgeapi.PoolState{
		{Name: "burst_traffic_per_user_free_plan", Remaining: 99, Limit: 100, Reset: time.Unix(1890000030, 0).UTC()},
		{Name: "throttle_authenticated_api", Remaining: 1989, Limit: 2000, Reset: time.Unix(1890000060, 0).UTC()},
	}
	if len(got) != len(want) {
		t.Fatalf("Pools = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Remaining != want[i].Remaining ||
			got[i].Limit != want[i].Limit || !got[i].Reset.Equal(want[i].Reset) {
			t.Errorf("Pools[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
