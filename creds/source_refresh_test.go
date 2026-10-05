package creds_test

import (
	"context"
	"net/http"
	"testing"
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
