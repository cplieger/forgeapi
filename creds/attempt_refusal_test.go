package creds_test

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/creds"
)

// errAttemptRefused is the consumer's own refusal a refusing hook answers.
var errAttemptRefused = errors.New("the consumer's gate refused the attempt")

// refusingHook is an attempt hook that refuses every attempt while on is set
// and passes the rest on.
type refusingHook struct{ on atomic.Bool }

func (h *refusingHook) option() forgeapi.Option {
	return forgeapi.WithAttemptObserver(func(next http.RoundTripper) http.RoundTripper {
		return roundTrip(func(r *http.Request) (*http.Response, error) {
			if h.on.Load() {
				return nil, errAttemptRefused
			}
			return next.RoundTrip(r)
		})
	})
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A re-probe the consumer's hook refuses sent nothing, so it moves no standing:
// the next call sends the re-probe, and only the answered requests are counted
// as outcomes.
func TestAppSource_a_refused_attempt_spends_no_reprobe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, func(n int, _ appCall) appReply {
			if n == 1 {
				return refusal(http.StatusUnauthorized, "Bad credentials", nil)
			}
			return minted(n, time.Hour)
		})
		var o outcomes
		var hook refusingHook
		s := appSource(t, g, creds.InstallationByID(42), forgeapi.WithCounters(o.counters()), hook.option())
		if _, err := s.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
			t.Fatalf("Setup: Token() = %v, want %q", err, forgeapi.CodeReconnectRequired)
		}
		time.Sleep(5 * time.Minute)
		hook.on.Store(true)
		if _, err := s.Token(t.Context()); !errors.Is(err, errAttemptRefused) || g.mints() != 1 {
			t.Fatalf("Token() refused by the hook = %v after %d token requests, want the hook's refusal after 1", err, g.mints())
		}
		hook.on.Store(false)
		if _, err := s.Token(t.Context()); err != nil || g.mints() != 2 {
			t.Errorf("Token() after the refusal = %v after %d token requests, want the re-probe sent and a token", err, g.mints())
		}
		want := []string{"github reconnect_required", "github valid"}
		if got := o.list(); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("RefreshOutcome = %v, want %v: the refused re-probe is no outcome", got, want)
		}
	})
}

// A refresh the consumer's hook refuses sent nothing, so it records no outcome:
// a live token is still handed over, and an expired one answers the hook's
// refusal.
func TestStoredSource_a_refused_refresh_records_no_outcome(t *testing.T) {
	for _, tc := range []struct {
		err   error
		name  string
		token string
		left  time.Duration
	}{
		{name: "a_live_token", left: time.Minute, token: "token-old"},
		{name: "an_expired_token", left: -time.Minute, err: errAttemptRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ep := newEndpoint(t, unreachable(t))
			store, _ := openStore(t)
			save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, tc.left))
			counted := &outcomes{}
			var hook refusingHook
			hook.on.Store(true)
			src := source(t, store, "conn", ep, forgeapi.WithCounters(counted.counters()), hook.option())
			if token, err := src.Token(t.Context()); token != tc.token || !errors.Is(err, tc.err) {
				t.Errorf("Token() = %q, %v, want %q, %v", token, err, tc.token, tc.err)
			}
			if got := counted.list(); len(got) != 0 {
				t.Errorf("RefreshOutcome reported %v, want none: the refused refresh sent nothing", got)
			}
		})
	}
}
