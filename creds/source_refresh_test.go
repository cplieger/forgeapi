package creds_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

func TestNewSource_refuses_a_negative_refresh_lead(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, _ := openStore(t)

	_, err := creds.NewSource(store, "conn", ep.conn(), ep.options(forgeapi.WithRefreshLead(-time.Minute))...)
	if code := codeOf(err); code != forgeapi.CodeBudgetInvalid {
		t.Errorf("NewSource(WithRefreshLead(-1m)) = error %v (code %q), want code %q", err, code, forgeapi.CodeBudgetInvalid)
	}
}

func TestSource_reports_refreshing_while_the_refresh_is_in_flight(t *testing.T) {
	g, ep := newGate(t, githubRotated)
	store, _ := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	src := source(t, store, "conn", ep)

	done := make(chan error, 1)
	go func() {
		_, err := src.Token(t.Context())
		done <- err
	}()
	<-g.arrived
	if s := src.State(); s != forgeapi.CredRefreshing {
		t.Errorf("State() with the refresh held in flight = %v, want %v", s, forgeapi.CredRefreshing)
	}
	g.open()
	if err := <-done; err != nil {
		t.Fatalf("Token() = error %v, want the rotated token", err)
	}
	if s := src.State(); s != forgeapi.CredValid {
		t.Errorf("State() after the refresh = %v, want %v", s, forgeapi.CredValid)
	}
}

// The instance spends the old pair the moment it issues the new one, so a caller
// that walks away mid-refresh must not cost the connection its only copy.
func TestSource_keeps_a_rotation_whose_caller_cancelled_mid_flight(t *testing.T) {
	cancelled := make(chan struct{})
	arrived := make(chan struct{})
	ep := newEndpoint(t, func(int, exchange) (int, string) {
		close(arrived)
		<-cancelled
		return http.StatusOK, githubRotated
	})
	store, _ := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	src := source(t, store, "conn", ep)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := src.Token(ctx); err != nil {
			t.Logf("Token() = error %v", err)
		}
	}()
	<-arrived
	cancel()
	close(cancelled)
	<-done

	if rec := load(t, store, "conn"); rec.Token != "token-new" || rec.RefreshToken != "refresh-new" {
		t.Errorf("stored pair = (%q, %q), want the rotated pair the instance issued after the caller cancelled", rec.Token, rec.RefreshToken)
	}
}

func TestSource_sends_no_authorization_with_the_refresh(t *testing.T) {
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
	if auth := reqs[0].header.Get("Authorization"); auth != "" {
		t.Errorf("the refresh carried Authorization %q, want none: the token endpoint identifies the application by its client id", auth)
	}
}

// A zero lead refreshes a token only once it has expired, so one a minute short
// of its expiry is still handed over as it is.
func TestNewSource_takes_a_zero_refresh_lead(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, _ := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))

	src := source(t, store, "conn", ep, forgeapi.WithRefreshLead(0))
	if s := src.State(); s != forgeapi.CredValid {
		t.Errorf("State() of a token a minute from expiry under a zero lead = %v, want %v", s, forgeapi.CredValid)
	}
}

// Half the issued lifetime caps the lead only where the record states a lifetime;
// one issued at its own expiry is refreshed at the configured lead.
func TestSource_refreshes_a_record_stating_no_lifetime_at_the_configured_lead(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, _ := openStore(t)
	rec := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute)
	rec.Issued = rec.Expiry
	save(t, store, "conn", rec)

	if s := source(t, store, "conn", ep).State(); s != forgeapi.CredRefreshDue {
		t.Errorf("State() of a record issued at its expiry, a minute away, under the 5m default lead = %v, want %v", s, forgeapi.CredRefreshDue)
	}
}

// Each refresh outcome is one line on the source's own logger, naming the instance
// and the state reached, at warning level where the refresh reached no valid token.
func TestSource_logs_each_refresh_outcome_to_its_logger(t *testing.T) {
	for name, test := range map[string]struct {
		body   string
		state  string
		level  string
		status int
	}{
		"rotated":        {status: http.StatusOK, body: githubRotated, state: "valid", level: "INFO"},
		"server_failure": {status: http.StatusInternalServerError, body: `{"error":"server_error"}`, state: "refresh_due", level: "WARN"},
	} {
		t.Run(name, func(t *testing.T) {
			ep := newEndpoint(t, func(int, exchange) (int, string) { return test.status, test.body })
			store, _ := openStore(t)
			save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
			var logged bytes.Buffer
			src := source(t, store, "conn", ep, forgeapi.WithLogger(slog.New(slog.NewTextHandler(&logged, nil))))

			if _, err := src.Token(t.Context()); err != nil {
				t.Fatalf("Token() = error %v, want a token", err)
			}
			var line string
			for l := range strings.SplitSeq(logged.String(), "\n") {
				if strings.Contains(l, `msg="forgeapi credential refresh"`) {
					line = l
				}
			}
			host := strings.TrimPrefix(ep.srv.URL, "http://")
			for _, want := range []string{"level=" + test.level + " ", " instance=" + host + " ", " state=" + test.state + " "} {
				if !strings.Contains(line, want) {
					t.Errorf("the refresh line on the source's logger = %q, want it to carry %q", line, want)
				}
			}
		})
	}
}

// A refresh cannot change the scopes, so an answer stating none keeps the record's.
func TestSource_keeps_the_scopes_a_refresh_answer_does_not_state(t *testing.T) {
	const unscoped = `{"access_token":"token-new","token_type":"bearer","expires_in":28800,"refresh_token":"refresh-new","refresh_token_expires_in":15897600}`
	ep := newEndpoint(t, func(int, exchange) (int, string) { return http.StatusOK, unscoped })
	store, _ := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))

	if _, err := source(t, store, "conn", ep).Token(t.Context()); err != nil {
		t.Fatalf("Token() = error %v, want the rotated token", err)
	}
	if got := load(t, store, "conn").Scopes; !slices.Equal(got, []string{"repo"}) {
		t.Errorf("the rotated record's Scopes = %q, want the stored %q", got, []string{"repo"})
	}
}

// A token is valid until the time left to its expiry is inside the refresh lead,
// and exactly the lead left is inside it. The bubble's clock stands still, so the
// time left is the lead to the nanosecond.
func TestSource_reads_a_token_exactly_its_lead_from_expiry_as_due(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	synctest.Test(t, func(t *testing.T) {
		store, _ := openStore(t)
		now := time.Now()
		rec := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Hour)
		rec.Issued, rec.Expiry = now.Add(forgeapi.DefaultRefreshLead-8*time.Hour), now.Add(forgeapi.DefaultRefreshLead)
		save(t, store, "conn", rec)

		if s := source(t, store, "conn", ep).State(); s != forgeapi.CredRefreshDue {
			t.Errorf("State() of a token %v from expiry under the %v default lead = %v, want %v", forgeapi.DefaultRefreshLead, forgeapi.DefaultRefreshLead, s, forgeapi.CredRefreshDue)
		}
	})
}
