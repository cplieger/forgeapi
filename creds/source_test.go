package creds_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// The source is the forgeapi.CredentialSource a client runs behind.
var _ forgeapi.CredentialSource = (*creds.Source)(nil)

// The file store is the store every case below runs over.
var _ creds.Store = (*creds.FileStore)(nil)

// The two families' successful refresh answers, in the shape each product's token
// endpoint documents: GitHub states the refresh token's own lifetime, GitLab states
// none.
const (
	githubRotated = `{"access_token":"token-new","token_type":"bearer","scope":"repo","expires_in":28800,"refresh_token":"refresh-new","refresh_token_expires_in":15897600}`
	gitlabRotated = `{"access_token":"token-new","token_type":"Bearer","scope":"repo","expires_in":7200,"refresh_token":"refresh-new","created_at":1790000000}`
)

// refreshArms are the two families whose OAuth credential rotates: the path each
// product's token endpoint answers on, and its successful answer.
var refreshArms = []struct {
	rotated string
	path    string
	family  forgeapi.Family
}{
	{family: forgeapi.FamilyGitHub, path: "/login/oauth/access_token", rotated: githubRotated},
	{family: forgeapi.FamilyGitLab, path: "/oauth/token", rotated: gitlabRotated},
}

func TestSource_hands_over_a_static_token_without_a_request(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, _ := openStore(t)
	save(t, store, "conn", creds.Record{
		Family:     forgeapi.FamilyGitea,
		WebBaseURL: ep.srv.URL,
		Kind:       forgeapi.CredKindStaticPAT,
		Token:      "token-static",
		Account:    "example-user",
	})
	src := source(t, store, "conn", ep)

	got, err := src.Token(t.Context())
	if err != nil || got != "token-static" {
		t.Errorf("Token() = (%q, %v), want (%q, nil)", got, err, "token-static")
	}
	if k := src.Kind(); k != forgeapi.CredKindStaticPAT {
		t.Errorf("Kind() = %v, want %v", k, forgeapi.CredKindStaticPAT)
	}
	if s := src.State(); s != forgeapi.CredValid {
		t.Errorf("State() = %v, want %v", s, forgeapi.CredValid)
	}
}

func TestSource_requires_a_reconnect_for_a_static_token_past_its_expiry(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, _ := openStore(t)
	save(t, store, "conn", creds.Record{
		Family:     forgeapi.FamilyGitLab,
		WebBaseURL: ep.srv.URL,
		Kind:       forgeapi.CredKindStaticPAT,
		Token:      "token-static",
		Expiry:     time.Now().Add(-time.Minute).Truncate(time.Second),
	})
	src := source(t, store, "conn", ep)

	_, err := src.Token(t.Context())
	if code := codeOf(err); code != forgeapi.CodeReconnectRequired {
		t.Errorf("Token() = error %v (code %q), want code %q: a static token is never refreshed", err, code, forgeapi.CodeReconnectRequired)
	}
	if s := src.State(); s != forgeapi.CredReconnectRequired {
		t.Errorf("State() = %v, want %v", s, forgeapi.CredReconnectRequired)
	}
}

func TestSource_hands_over_a_fresh_rotating_token_without_a_request(t *testing.T) {
	for _, arm := range refreshArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			ep := newEndpoint(t, unreachable(t))
			store, _ := openStore(t)
			save(t, store, "conn", rotating(arm.family, ep.srv.URL, 8*time.Hour, 8*time.Hour))
			src := source(t, store, "conn", ep)

			got, err := src.Token(t.Context())
			if err != nil || got != "token-old" {
				t.Errorf("Token() = (%q, %v), want (%q, nil)", got, err, "token-old")
			}
			if k := src.Kind(); k != forgeapi.CredKindRotatingOAuth {
				t.Errorf("Kind() = %v, want %v", k, forgeapi.CredKindRotatingOAuth)
			}
			if s := src.State(); s != forgeapi.CredValid {
				t.Errorf("State() = %v, want %v", s, forgeapi.CredValid)
			}
		})
	}
}

// A lifetime shorter than the lead would make every fresh token due on arrival,
// so the lead is clamped to half the lifetime the record was issued with.
func TestSource_clamps_the_lead_to_half_a_short_lifetime(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, _ := openStore(t)
	// Four minutes of life, all of it left: inside the default five-minute lead,
	// outside half the lifetime.
	save(t, store, "conn", rotating(forgeapi.FamilyGitLab, ep.srv.URL, 4*time.Minute, 4*time.Minute))
	src := source(t, store, "conn", ep)

	if s := src.State(); s != forgeapi.CredValid {
		t.Errorf("State() = %v, want %v: four minutes left of four is outside half the lifetime", s, forgeapi.CredValid)
	}
	if got, err := src.Token(t.Context()); err != nil || got != "token-old" {
		t.Errorf("Token() = (%q, %v), want (%q, nil) with no refresh", got, err, "token-old")
	}
}

func TestSource_is_due_inside_the_clamped_lead_of_a_short_lifetime(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, _ := openStore(t)
	// One minute left of four: inside half the lifetime.
	save(t, store, "conn", rotating(forgeapi.FamilyGitLab, ep.srv.URL, 4*time.Minute, time.Minute))
	src := source(t, store, "conn", ep)

	if s := src.State(); s != forgeapi.CredRefreshDue {
		t.Errorf("State() = %v, want %v: one minute left of four is inside half the lifetime", s, forgeapi.CredRefreshDue)
	}
}

func TestSource_takes_the_refresh_lead_from_the_option(t *testing.T) {
	tests := []struct {
		opts []forgeapi.Option
		name string
		left time.Duration
		want forgeapi.CredState
	}{
		// The default lead is five minutes.
		{name: "outside_the_default_lead", left: 6 * time.Minute, want: forgeapi.CredValid},
		{name: "inside_the_default_lead", left: 4 * time.Minute, want: forgeapi.CredRefreshDue},
		{
			name: "inside_a_half_hour_lead", left: 20 * time.Minute, want: forgeapi.CredRefreshDue,
			opts: []forgeapi.Option{forgeapi.WithRefreshLead(30 * time.Minute)},
		},
		{
			name: "outside_a_one_minute_lead", left: 4 * time.Minute, want: forgeapi.CredValid,
			opts: []forgeapi.Option{forgeapi.WithRefreshLead(time.Minute)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ep := newEndpoint(t, unreachable(t))
			store, _ := openStore(t)
			save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, tc.left))
			src := source(t, store, "conn", ep, tc.opts...)

			if s := src.State(); s != tc.want {
				t.Errorf("State() with %v of eight hours left = %v, want %v", tc.left, s, tc.want)
			}
		})
	}
}

func TestSource_rotates_the_whole_record_when_due(t *testing.T) {
	for _, arm := range refreshArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			ep := newEndpoint(t, func(int, exchange) (int, string) { return http.StatusOK, arm.rotated })
			store, _ := openStore(t)
			old := rotating(arm.family, ep.srv.URL, 8*time.Hour, time.Minute)
			save(t, store, "conn", old)
			src := source(t, store, "conn", ep)

			before := time.Now()
			got, err := src.Token(t.Context())
			if err != nil || got != "token-new" {
				t.Fatalf("Token() = (%q, %v), want (%q, nil): the token was inside the lead", got, err, "token-new")
			}
			if s := src.State(); s != forgeapi.CredValid {
				t.Errorf("State() after the rotation = %v, want %v", s, forgeapi.CredValid)
			}
			rec := load(t, store, "conn")
			if rec.Token != "token-new" || rec.RefreshToken != "refresh-new" {
				t.Errorf("stored pair = (%q, %q), want (%q, %q): the old pair is dead upstream once the new one is issued",
					rec.Token, rec.RefreshToken, "token-new", "refresh-new")
			}
			if rec.Family != old.Family || rec.WebBaseURL != old.WebBaseURL || rec.ClientID != old.ClientID ||
				rec.Account != old.Account || rec.Kind != forgeapi.CredKindRotatingOAuth {
				t.Errorf("stored record = %+v, want the family, web base, client id, account and kind of %+v kept", rec, old)
			}
			if !slices.Equal(rec.Scopes, []string{"repo"}) {
				t.Errorf("stored Scopes = %v, want [repo], as the response stated them", rec.Scopes)
			}
			if !near(rec.Issued, before, time.Minute) {
				t.Errorf("stored Issued = %v, want about %v, when the rotation answered", rec.Issued, before)
			}
			if len(ep.requests()) != 1 {
				t.Errorf("the endpoint received %d request(s), want 1", len(ep.requests()))
			}
		})
	}
}

func TestSource_takes_both_expiries_from_the_github_response(t *testing.T) {
	ep := newEndpoint(t, func(int, exchange) (int, string) { return http.StatusOK, githubRotated })
	store, _ := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	src := source(t, store, "conn", ep)

	before := time.Now()
	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("Token() = error %v, want the rotated token", err)
	}
	rec := load(t, store, "conn")
	if want := before.Add(28800 * time.Second); !near(rec.Expiry, want, time.Minute) {
		t.Errorf("stored Expiry = %v, want about %v, from expires_in", rec.Expiry, want)
	}
	if want := before.Add(15897600 * time.Second); !near(rec.RefreshExpiry, want, time.Minute) {
		t.Errorf("stored RefreshExpiry = %v, want about %v, from refresh_token_expires_in", rec.RefreshExpiry, want)
	}
}

func TestSource_takes_the_gitlab_expiry_from_the_response_and_no_refresh_expiry(t *testing.T) {
	ep := newEndpoint(t, func(int, exchange) (int, string) { return http.StatusOK, gitlabRotated })
	store, _ := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitLab, ep.srv.URL, 2*time.Hour, time.Minute))
	src := source(t, store, "conn", ep)

	before := time.Now()
	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("Token() = error %v, want the rotated token", err)
	}
	rec := load(t, store, "conn")
	if want := before.Add(7200 * time.Second); !near(rec.Expiry, want, time.Minute) {
		t.Errorf("stored Expiry = %v, want about %v, from expires_in", rec.Expiry, want)
	}
	if !rec.RefreshExpiry.IsZero() {
		t.Errorf("stored RefreshExpiry = %v, want zero: the response states none", rec.RefreshExpiry)
	}
}

func TestSource_sends_the_github_refresh_with_no_secret_and_no_scope(t *testing.T) {
	ep := newEndpoint(t, func(int, exchange) (int, string) { return http.StatusOK, githubRotated })
	store, _ := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	src := source(t, store, "conn", ep)

	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("Token() = error %v, want the rotated token", err)
	}
	reqs := ep.requests()
	if len(reqs) != 1 {
		t.Fatalf("the endpoint received %d request(s), want 1", len(reqs))
	}
	x := reqs[0]
	checkRefreshRequest(t, x, "/login/oauth/access_token")
	if accept := x.header.Get("Accept"); accept != "application/json" {
		t.Errorf("Accept = %q, want application/json: the endpoint answers a form unless asked for JSON", accept)
	}
}

func TestSource_sends_the_gitlab_refresh_with_no_secret_no_scope_and_no_redirect(t *testing.T) {
	ep := newEndpoint(t, func(int, exchange) (int, string) { return http.StatusOK, gitlabRotated })
	store, _ := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitLab, ep.srv.URL, 2*time.Hour, time.Minute))
	src := source(t, store, "conn", ep)

	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("Token() = error %v, want the rotated token", err)
	}
	reqs := ep.requests()
	if len(reqs) != 1 {
		t.Fatalf("the endpoint received %d request(s), want 1", len(reqs))
	}
	checkRefreshRequest(t, reqs[0], "/oauth/token")
	if reqs[0].form.Has("redirect_uri") || reqs[0].query.Has("redirect_uri") {
		t.Errorf("the refresh sent redirect_uri, want none: only an authorization-code grant carries one")
	}
}

// checkRefreshRequest holds one refresh request to the shape both families share: a
// form POST to the token endpoint naming the client the grant was minted under, the
// refresh grant and the refresh token, with no client secret and no scope anywhere.
func checkRefreshRequest(t *testing.T, x exchange, path string) {
	t.Helper()
	if x.method != http.MethodPost || x.path != path {
		t.Errorf("the refresh was %s %s, want POST %s", x.method, x.path, path)
	}
	if ct := x.header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
	}
	want := map[string]string{"client_id": clientID, "grant_type": "refresh_token", "refresh_token": "refresh-old"}
	for name, value := range want {
		if got := x.form.Get(name); got != value {
			t.Errorf("form %s = %q, want %q", name, got, value)
		}
	}
	for _, name := range []string{"client_secret", "scope"} {
		if x.form.Has(name) || x.query.Has(name) {
			t.Errorf("the refresh sent %s, want none: the library holds no secret and the scopes cannot change", name)
		}
	}
}

func TestSource_requires_a_reconnect_after_a_terminal_refresh_answer(t *testing.T) {
	tests := []struct {
		body   string
		family forgeapi.Family
		status int
	}{
		// GitHub answers the refresh error inside a 200, so the body's error
		// member is what decides it.
		{family: forgeapi.FamilyGitHub, status: http.StatusOK, body: `{"error":"bad_refresh_token","error_description":"The refresh token passed is incorrect or expired."}`},
		{family: forgeapi.FamilyGitLab, status: http.StatusBadRequest, body: `{"error":"invalid_grant","error_description":"The provided authorization grant is invalid, expired, revoked."}`},
	}
	for _, tc := range tests {
		t.Run(tc.family.String(), func(t *testing.T) {
			ep := newEndpoint(t, func(int, exchange) (int, string) { return tc.status, tc.body })
			store, _ := openStore(t)
			save(t, store, "conn", rotating(tc.family, ep.srv.URL, 8*time.Hour, time.Minute))
			counted := &outcomes{}
			src := source(t, store, "conn", ep, forgeapi.WithCounters(counted.counters()))

			for call := 1; call <= 2; call++ {
				if _, err := src.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
					t.Errorf("Token() call %d = error %v, want code %q", call, err, forgeapi.CodeReconnectRequired)
				}
			}
			if s := src.State(); s != forgeapi.CredReconnectRequired {
				t.Errorf("State() = %v, want %v", s, forgeapi.CredReconnectRequired)
			}
			if n := len(ep.requests()); n != 1 {
				t.Errorf("the endpoint received %d request(s), want 1: a terminal answer is not retried", n)
			}
			want := []string{tc.family.String() + " " + forgeapi.CredReconnectRequired.String()}
			if got := counted.list(); !slices.Equal(got, want) {
				t.Errorf("RefreshOutcome reported %v, want %v", got, want)
			}
		})
	}
}

func TestSource_requires_a_reconnect_for_a_refresh_token_past_its_own_expiry(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, _ := openStore(t)
	rec := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute)
	rec.RefreshExpiry = time.Now().Add(-time.Hour).Truncate(time.Second)
	save(t, store, "conn", rec)
	counted := &outcomes{}
	src := source(t, store, "conn", ep, forgeapi.WithCounters(counted.counters()))

	if _, err := src.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
		t.Errorf("Token() = error %v, want code %q", err, forgeapi.CodeReconnectRequired)
	}
	if s := src.State(); s != forgeapi.CredReconnectRequired {
		t.Errorf("State() = %v, want %v", s, forgeapi.CredReconnectRequired)
	}
	if got := counted.list(); len(got) != 1 {
		t.Errorf("RefreshOutcome reported %v, want one terminal outcome", got)
	}
}

func TestSource_hands_over_a_rotating_token_with_no_refresh_token_until_it_expires(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, _ := openStore(t)
	rec := rotating(forgeapi.FamilyGitLab, ep.srv.URL, 2*time.Hour, 2*time.Hour)
	rec.RefreshToken, rec.RefreshExpiry = "", time.Time{}
	save(t, store, "conn", rec)
	src := source(t, store, "conn", ep)

	if got, err := src.Token(t.Context()); err != nil || got != "token-old" {
		t.Errorf("Token() = (%q, %v), want (%q, nil)", got, err, "token-old")
	}
}

func TestSource_requires_a_reconnect_for_an_expired_token_with_no_refresh_token(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, _ := openStore(t)
	rec := rotating(forgeapi.FamilyGitLab, ep.srv.URL, 2*time.Hour, -time.Minute)
	rec.RefreshToken, rec.RefreshExpiry = "", time.Time{}
	save(t, store, "conn", rec)
	src := source(t, store, "conn", ep)

	if _, err := src.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
		t.Errorf("Token() = error %v, want code %q", err, forgeapi.CodeReconnectRequired)
	}
	if s := src.State(); s != forgeapi.CredReconnectRequired {
		t.Errorf("State() = %v, want %v", s, forgeapi.CredReconnectRequired)
	}
}

func TestSource_requires_a_reconnect_for_a_key_with_no_record(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, _ := openStore(t)
	src := source(t, store, "never-connected", ep)

	if _, err := src.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
		t.Errorf("Token() = error %v, want code %q", err, forgeapi.CodeReconnectRequired)
	}
	if s := src.State(); s != forgeapi.CredReconnectRequired {
		t.Errorf("State() = %v, want %v", s, forgeapi.CredReconnectRequired)
	}
}

// A zero-valued record must not read as a static token that never needs
// refreshing.
func TestSource_requires_a_reconnect_for_a_record_of_unknown_kind(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, _ := openStore(t)
	save(t, store, "conn", creds.Record{Family: forgeapi.FamilyGitHub, WebBaseURL: ep.srv.URL, Token: "token-old"})
	src := source(t, store, "conn", ep)

	if _, err := src.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
		t.Errorf("Token() = error %v, want code %q", err, forgeapi.CodeReconnectRequired)
	}
	if s := src.State(); s != forgeapi.CredReconnectRequired {
		t.Errorf("State() = %v, want %v", s, forgeapi.CredReconnectRequired)
	}
}

func TestSource_keeps_the_still_valid_token_after_a_transient_refresh_failure(t *testing.T) {
	ep := newEndpoint(t, func(int, exchange) (int, string) {
		return http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}`
	})
	store, _ := openStore(t)
	old := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute)
	save(t, store, "conn", old)
	src := source(t, store, "conn", ep)

	if got, err := src.Token(t.Context()); err != nil || got != "token-old" {
		t.Errorf("Token() = (%q, %v), want (%q, nil): the token has a minute left", got, err, "token-old")
	}
	if s := src.State(); s != forgeapi.CredRefreshDue {
		t.Errorf("State() = %v, want %v", s, forgeapi.CredRefreshDue)
	}
	if field := sameRecord(load(t, store, "conn"), old); field != "" {
		t.Errorf("stored record differs at %s, want it unchanged after a failed refresh", field)
	}
	if len(ep.requests()) == 0 {
		t.Error("the endpoint received no request, want the refresh attempted")
	}
}

func TestSource_fails_a_transient_refresh_failure_once_the_token_has_expired(t *testing.T) {
	ep := newEndpoint(t, func(int, exchange) (int, string) {
		return http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}`
	})
	store, _ := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitLab, ep.srv.URL, 2*time.Hour, -time.Minute))
	src := source(t, store, "conn", ep)

	_, err := src.Token(t.Context())
	if err == nil {
		t.Fatal("Token() = nil error, want a failure: the token has expired and the refresh did not answer")
	}
	if code := codeOf(err); code == forgeapi.CodeReconnectRequired {
		t.Errorf("Token() = code %q, want a transient failure: nothing terminal was answered", code)
	}
	if s := src.State(); s == forgeapi.CredReconnectRequired {
		t.Errorf("State() = %v, want the connection still refreshable", s)
	}
}

// gate holds a refresh in flight until the case releases it, and reports the first
// request's arrival.
type gate struct {
	arrived chan struct{}
	release chan struct{}
	seen    sync.Once
	opened  sync.Once
}

// newGate builds a gate around the endpoint it holds. It is opened at cleanup
// before the endpoint closes, because closing a server waits for the request the
// gate holds.
func newGate(t *testing.T, body string) (*gate, *endpoint) {
	t.Helper()
	g := &gate{arrived: make(chan struct{}), release: make(chan struct{})}
	ep := newEndpoint(t, func(int, exchange) (int, string) {
		g.seen.Do(func() { close(g.arrived) })
		<-g.release
		return http.StatusOK, body
	})
	t.Cleanup(g.open)
	return g, ep
}

func (g *gate) open() { g.opened.Do(func() { close(g.release) }) }

// The refresh owner is the key's in its store, not a source's: a second source over
// the same store waits on the refresh the first one holds, and fails at its own
// deadline rather than sending a refresh of its own.
func TestSource_refreshes_once_for_two_sources_on_one_key(t *testing.T) {
	g, ep := newGate(t, githubRotated)
	store, _ := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	first, second := source(t, store, "conn", ep), source(t, store, "conn", ep)

	owner := make(chan error, 1)
	go func() {
		_, err := first.Token(t.Context())
		owner <- err
	}()
	<-g.arrived

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	waited := make(chan error, 1)
	go func() {
		_, err := second.Token(ctx)
		waited <- err
	}()
	select {
	case err := <-waited:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the second source's Token() = error %v, want its own deadline while the first holds the refresh", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the second source's Token() was still running five seconds after its own deadline, want it to wait on the first source's refresh and fail at the deadline")
	}
	if n := len(ep.requests()); n != 1 {
		t.Errorf("the endpoint received %d refresh request(s) while the first source held the refresh, want 1", n)
	}

	g.open()
	if err := <-owner; err != nil {
		t.Fatalf("the first source's Token() = error %v, want the rotated token", err)
	}
	if token, err := second.Token(t.Context()); err != nil || token != "token-new" {
		t.Errorf("the second source's Token() after the rotation = (%q, %v), want (%q, nil)", token, err, "token-new")
	}
	if n := len(ep.requests()); n != 1 {
		t.Errorf("the endpoint received %d refresh request(s) in all, want 1", n)
	}
}

func TestSource_fails_a_waiter_at_its_own_deadline(t *testing.T) {
	g, ep := newGate(t, githubRotated)
	store, _ := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	src := source(t, store, "conn", ep)

	owner := make(chan error, 1)
	go func() {
		_, err := src.Token(t.Context())
		owner <- err
	}()
	<-g.arrived

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	waited := make(chan error, 1)
	go func() {
		_, err := src.Token(ctx)
		waited <- err
	}()
	select {
	case err := <-waited:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the waiter's Token() = error %v, want its own deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the waiter's Token() was still waiting five seconds after its own deadline, want it to fail at the deadline")
	}
	g.open()
	if err := <-owner; err != nil {
		t.Errorf("the owner's Token() = error %v, want the rotated token", err)
	}
}

// A consumer that reconnects while a refresh is in flight saved a newer record, and
// the rotation that started from the older one must not overwrite it.
func TestSource_does_not_overwrite_a_record_saved_during_the_refresh(t *testing.T) {
	store, _ := openStore(t)
	saved := make(chan error, 1)
	landed := make(chan struct{})
	var ep *endpoint
	ep = newEndpoint(t, func(int, exchange) (int, string) {
		fresh := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, 8*time.Hour)
		fresh.Token, fresh.RefreshToken = "token-reconnected", "refresh-reconnected"
		go func() {
			saved <- store.Save("conn", fresh)
			close(landed)
		}()
		// The reconnect is given its chance to land before the rotation answers,
		// and the rotation answers anyway if the store holds it back.
		select {
		case <-landed:
		case <-time.After(time.Second):
		}
		return http.StatusOK, githubRotated
	})
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	src := source(t, store, "conn", ep)

	if _, err := src.Token(t.Context()); err != nil {
		t.Logf("Token() = error %v", err)
	}
	select {
	case err := <-saved:
		if err != nil {
			t.Fatalf("Setup: the reconnect's Save = error %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reconnect's Save had not returned five seconds after the refresh did")
	}
	if rec := load(t, store, "conn"); rec.Token != "token-reconnected" || rec.RefreshToken != "refresh-reconnected" {
		t.Errorf("stored pair = (%q, %q), want the reconnected pair: the rotation started from a record that was replaced",
			rec.Token, rec.RefreshToken)
	}
}

func TestSource_persists_a_rotation_for_the_next_store_opened_on_the_directory(t *testing.T) {
	ep := newEndpoint(t, func(int, exchange) (int, string) { return http.StatusOK, githubRotated })
	store, dir := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	src := source(t, store, "conn", ep)
	if _, err := src.Token(t.Context()); err != nil {
		t.Fatalf("Token() = error %v, want the rotated token", err)
	}

	reopened, err := creds.OpenFileStore(dir)
	if err != nil {
		t.Fatalf("OpenFileStore(%q) again = error %v", dir, err)
	}
	if rec := load(t, reopened, "conn"); rec.Token != "token-new" || rec.RefreshToken != "refresh-new" {
		t.Errorf("the reopened store holds (%q, %q), want the rotated pair", rec.Token, rec.RefreshToken)
	}
}

// A rotating credential is an OAuth grant with an expiry, so a rotating record that
// states none is not a token that never expires: no deadline would ever make it due
// or expired, and the token would be handed over forever. It reads as
// reconnect-required, the reading every record outside the two kinds gets, whether
// or not it carries a refresh token, and nothing is sent to renew it.
func TestSource_reads_a_rotating_record_without_an_expiry_as_reconnect_required(t *testing.T) {
	for name, refresh := range map[string]string{"with_a_refresh_token": "refresh-old", "without_one": ""} {
		t.Run(name, func(t *testing.T) {
			ep := newEndpoint(t, unreachable(t))
			store, _ := openStore(t)
			rec := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, 8*time.Hour)
			rec.Expiry, rec.RefreshToken = time.Time{}, refresh
			save(t, store, "conn", rec)
			src := source(t, store, "conn", ep)

			token, err := src.Token(t.Context())
			if code := codeOf(err); token != "" || code != forgeapi.CodeReconnectRequired {
				t.Errorf("Token() over a rotating record without an expiry = (%q, %v), want no token and code %q", token, err, forgeapi.CodeReconnectRequired)
			}
			if s := src.State(); s != forgeapi.CredReconnectRequired {
				t.Errorf("State() over a rotating record without an expiry = %v, want %v", s, forgeapi.CredReconnectRequired)
			}
			if n := len(ep.requests()); n != 0 {
				t.Errorf("the token endpoint received %d request(s), want none", n)
			}
		})
	}
}
