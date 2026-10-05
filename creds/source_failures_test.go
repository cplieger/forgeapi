package creds_test

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// Both products spend the old pair the moment they issue the new one, so a rotation
// the store refused leaves the stored token dead upstream: the call fails rather
// than handing that token over as though nothing happened.
func TestSource_fails_a_rotation_it_could_not_store(t *testing.T) {
	store, dir := openStore(t)
	ep := newEndpoint(t, func(int, exchange) (int, string) {
		if err := os.Chmod(dir, 0o750); err != nil {
			t.Errorf("Setup: widening the store's directory: %v", err)
		}
		return http.StatusOK, githubRotated
	})
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("Cleanup: narrowing the store's directory: %v", err)
		}
	})
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	src := source(t, store, "conn", ep)

	token, err := src.Token(t.Context())
	if err == nil || token != "" {
		t.Errorf("Token() with the rotated pair unstorable = (%q, %v), want no token and the store's failure: the stored token was spent by the refresh", token, err)
	}
}

// heldEndpoint answers its first request with status and body once the case
// releases it, reporting the request's arrival.
func heldEndpoint(t *testing.T, status int, body string) (arrived, release chan struct{}, ep *endpoint) {
	t.Helper()
	arrived, release = make(chan struct{}), make(chan struct{})
	ep = newEndpoint(t, func(n int, _ exchange) (int, string) {
		if n == 1 {
			close(arrived)
			<-release
		}
		return status, body
	})
	return arrived, release, ep
}

// A reconnect saved while the old record's refresh was in flight is the
// connection's credential, so an answer about the old refresh token says nothing
// about it, whether the answer is terminal or a failure.
func TestSource_hands_over_a_record_saved_while_its_refresh_failed(t *testing.T) {
	for _, test := range []struct {
		name   string
		body   string
		status int
	}{
		{name: "terminal", status: http.StatusOK, body: `{"error":"bad_refresh_token"}`},
		{name: "transient", status: http.StatusServiceUnavailable, body: `{"error":"temporarily_unavailable"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			arrived, release, ep := heldEndpoint(t, test.status, test.body)
			store, _ := openStore(t)
			save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, -time.Minute))
			o := &outcomes{}
			src := source(t, store, "conn", ep, forgeapi.WithCounters(o.counters()))

			type result struct {
				err   error
				token string
			}
			done := make(chan result, 1)
			go func() {
				token, err := src.Token(t.Context())
				done <- result{token: token, err: err}
			}()
			<-arrived
			reconnected := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, 8*time.Hour)
			reconnected.Token, reconnected.RefreshToken = "token-reconnected", "refresh-reconnected"
			save(t, store, "conn", reconnected)
			close(release)

			got := <-done
			if got.err != nil || got.token != "token-reconnected" {
				t.Errorf("Token() = (%q, %v), want (%q, nil): the reconnect saved during the refresh is the credential", got.token, got.err, "token-reconnected")
			}
			if s := src.State(); s != forgeapi.CredValid {
				t.Errorf("State() = %v, want %v", s, forgeapi.CredValid)
			}
			if seen := o.list(); len(seen) != 0 {
				t.Errorf("RefreshOutcome fired %v, want nothing: the refresh was of a record the connection no longer holds", seen)
			}
		})
	}
}

// A token answer whose lifetime is a zero or negative count is neither of the two
// kinds the response decides, so it is refused rather than read as a static token
// that never needs refreshing.
func TestSource_refuses_a_refresh_answer_whose_lifetime_reads_as_neither_kind(t *testing.T) {
	for name, body := range unreadableTokens {
		t.Run(name, func(t *testing.T) {
			ep := newEndpoint(t, func(int, exchange) (int, string) { return http.StatusOK, body })
			store, _ := openStore(t)
			save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
			src := source(t, store, "conn", ep)

			token, err := src.Token(t.Context())
			if code := codeOf(err); code != forgeapi.CodeValidation || token != "" {
				t.Errorf("Token() over %s = (%q, %v), want no token and code %q", body, token, err, forgeapi.CodeValidation)
			}
			if rec := load(t, store, "conn"); rec.Kind != forgeapi.CredKindRotatingOAuth || rec.Token != "token-old" {
				t.Errorf("stored record = (%v, %q), want the rotating record untouched", rec.Kind, rec.Token)
			}
		})
	}
}

// unreadableTokens are token answers whose lifetimes state neither kind.
var unreadableTokens = map[string]string{
	"zero_expiry":             `{"access_token":"token-new","expires_in":0,"refresh_token":"refresh-new"}`,
	"negative_expiry":         `{"access_token":"token-new","expires_in":-1,"refresh_token":"refresh-new"}`,
	"zero_refresh_expiry":     `{"access_token":"token-new","expires_in":28800,"refresh_token":"refresh-new","refresh_token_expires_in":0}`,
	"refresh_token_no_expiry": `{"access_token":"token-new","refresh_token":"refresh-new"}`,
}

// An endpoint can copy what it was sent into its error member, and that member
// reaches an error a consumer logs or shows, so a member no OAuth document defines
// is not quoted.
func TestSource_does_not_quote_an_error_member_no_document_defines(t *testing.T) {
	ep := newEndpoint(t, func(_ int, x exchange) (int, string) {
		return http.StatusBadRequest, `{"error":"` + x.form.Get("refresh_token") + `"}`
	})
	store, _ := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, -time.Minute))
	src := source(t, store, "conn", ep)

	_, err := src.Token(t.Context())
	if err == nil {
		t.Fatal("Token() = nil error, want the refusal: the token has expired and the refresh answered no token")
	}
	if strings.Contains(err.Error(), "refresh-old") {
		t.Errorf("Token() = error %q, want it free of the refresh token the endpoint echoed", err)
	}
}
