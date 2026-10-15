package creds_test

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// countingStore is a file store that counts its Load calls.
type countingStore struct {
	*creds.FileStore
	loads atomic.Int64
}

func (c *countingStore) Load(key string) (creds.Record, bool, error) {
	c.loads.Add(1)
	return c.FileStore.Load(key)
}

// staticRecord is a static token record with the given expiry, zero for none.
func staticRecord(base string, expiry time.Time) creds.Record {
	return creds.Record{
		Family: forgeapi.FamilyGitea, WebBaseURL: base, Kind: forgeapi.CredKindStaticPAT,
		Token: "token-static", Expiry: expiry, Account: "example-user",
	}
}

// widen makes every read of the store in dir fail until the case ends.
func widen(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatalf("Setup: widening the store's directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("Setup: narrowing the store's directory: %v", err)
		}
	})
}

func TestSource_StoredToken_reads_the_store_once_per_call(t *testing.T) {
	cases := []struct {
		setup func(t *testing.T, store *creds.FileStore, dir, base string)
		name  string
	}{
		{name: "valid", setup: func(t *testing.T, store *creds.FileStore, _, base string) {
			save(t, store, "conn", rotating(forgeapi.FamilyGitHub, base, 8*time.Hour, 8*time.Hour))
		}},
		{name: "refresh due", setup: func(t *testing.T, store *creds.FileStore, _, base string) {
			save(t, store, "conn", rotating(forgeapi.FamilyGitHub, base, 8*time.Hour, time.Minute))
		}},
		{name: "expired and due", setup: func(t *testing.T, store *creds.FileStore, _, base string) {
			save(t, store, "conn", rotating(forgeapi.FamilyGitHub, base, 8*time.Hour, -time.Minute))
		}},
		{name: "marked", setup: func(t *testing.T, store *creds.FileStore, _, base string) {
			save(t, store, "conn", withMark(rotating(forgeapi.FamilyGitHub, base, 8*time.Hour, 8*time.Hour), markSpent))
		}},
		{name: "no record", setup: func(*testing.T, *creds.FileStore, string, string) {}},
		{name: "unreadable", setup: func(t *testing.T, store *creds.FileStore, dir, base string) {
			save(t, store, "conn", rotating(forgeapi.FamilyGitHub, base, 8*time.Hour, 8*time.Hour))
			widen(t, dir)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep := newEndpoint(t, unreachable(t))
			file, dir := openStore(t)
			tc.setup(t, file, dir, ep.srv.URL)
			store := &countingStore{FileStore: file}
			src := source(t, store, "conn", ep)

			before := store.loads.Load()
			_, _, _ = src.StoredToken()
			if n := store.loads.Load() - before; n != 1 {
				t.Errorf("StoredToken() read the store %d time(s), want 1", n)
			}
		})
	}
}

func TestSource_StoredToken_hands_over_a_token_it_can_hand_over_without_a_request(t *testing.T) {
	cases := []struct {
		rec   func(base string) creds.Record
		name  string
		token string
		state forgeapi.CredState
	}{
		{name: "static", token: "token-static", state: forgeapi.CredValid, rec: func(base string) creds.Record {
			return staticRecord(base, time.Time{})
		}},
		{name: "fresh rotating", token: "token-old", state: forgeapi.CredValid, rec: func(base string) creds.Record {
			return rotating(forgeapi.FamilyGitHub, base, 8*time.Hour, 8*time.Hour)
		}},
		{name: "rotating inside its lead", token: "token-old", state: forgeapi.CredRefreshDue, rec: func(base string) creds.Record {
			return rotating(forgeapi.FamilyGitHub, base, 8*time.Hour, time.Minute)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep := newEndpoint(t, unreachable(t))
			store, _ := openStore(t)
			rec := tc.rec(ep.srv.URL)
			save(t, store, "conn", rec)
			src := source(t, store, "conn", ep)

			token, state, err := src.StoredToken()
			if err != nil || token != tc.token || state != tc.state {
				t.Errorf("StoredToken() = (%q, %v, %v), want (%q, %v, nil)", token, state, err, tc.token, tc.state)
			}
			if field := sameRecord(load(t, store, "conn"), rec); field != "" {
				t.Errorf("the stored record's %s changed, want the record untouched: StoredToken never refreshes", field)
			}
		})
	}
}

// An expired token whose refresh is due is still refresh-due by State's reading, but
// it is no credential to hand over, and renewing it is Token's job.
func TestSource_StoredToken_fails_an_expired_due_token_without_refreshing_it(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, _ := openStore(t)
	rec := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, -time.Minute)
	save(t, store, "conn", rec)
	src := source(t, store, "conn", ep)

	token, state, err := src.StoredToken()
	if token != "" || state != forgeapi.CredRefreshDue {
		t.Errorf("StoredToken() = (%q, %v, _), want (\"\", %v, _)", token, state, forgeapi.CredRefreshDue)
	}
	fe, ok := errors.AsType[*forgeapi.Error](err)
	if !ok || fe.Kind != forgeapi.KindTransient || !fe.Retryable || fe.Code != "" || fe.Family != forgeapi.FamilyGitHub {
		t.Errorf("StoredToken() = error %#v, want a retryable transient *forgeapi.Error with no code, for GitHub", err)
	}
	if field := sameRecord(load(t, store, "conn"), rec); field != "" {
		t.Errorf("the stored record's %s changed, want the record untouched: StoredToken never refreshes", field)
	}
}

// A refresh another call holds in flight is no reason to wait: the stored token is
// answered at once, as refreshing.
func TestSource_StoredToken_answers_without_waiting_on_a_refresh_in_flight(t *testing.T) {
	g, ep := newGate(t, githubRotated)
	store, _ := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	refresher, reader := source(t, store, "conn", ep), source(t, store, "conn", ep)

	refreshed := make(chan error, 1)
	go func() {
		_, err := refresher.Token(t.Context())
		refreshed <- err
	}()
	<-g.arrived

	type answer struct {
		err   error
		token string
		state forgeapi.CredState
	}
	answered := make(chan answer, 1)
	go func() {
		token, state, err := reader.StoredToken()
		answered <- answer{token: token, state: state, err: err}
	}()
	select {
	case a := <-answered:
		if a.err != nil || a.token != "token-old" || a.state != forgeapi.CredRefreshing {
			t.Errorf("StoredToken() with a refresh in flight = (%q, %v, %v), want (%q, %v, nil)", a.token, a.state, a.err, "token-old", forgeapi.CredRefreshing)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("StoredToken() with a refresh in flight had not answered after 5s, want it answered without waiting")
	}
	g.open()
	if err := <-refreshed; err != nil {
		t.Errorf("Setup: Token() = error %v, want the rotated token", err)
	}
}

// While this process holds a rotated pair its store could not take, the stored token
// is the one that pair replaced, so it is not handed over, even where the record it
// sits in would otherwise read as valid or as needing a reconnect. The pair stays
// held for the next Token call to store.
func TestSource_StoredToken_fails_while_a_rotated_pair_waits_to_be_stored(t *testing.T) {
	cases := []struct {
		name   string
		lapsed bool
	}{
		{name: "refresh token live"},
		{name: "refresh token past its own expiry", lapsed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file, dir := openStore(t)
			ep, restore := failingWrite(t, dir, githubRotated)
			base := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute)
			if tc.lapsed {
				base.RefreshExpiry = time.Now().Truncate(time.Second).Add(2 * time.Second)
			}
			save(t, file, "conn", base)
			store := &countingStore{FileStore: file}
			src := source(t, store, "conn", ep)
			if token, err := src.Token(t.Context()); err == nil || token != "" {
				t.Fatalf("Setup: Token() with the rotated pair unstorable = (%q, %v), want no token and the store's failure", token, err)
			}
			restore()
			if tc.lapsed {
				time.Sleep(time.Until(base.RefreshExpiry) + 100*time.Millisecond)
			}

			before := store.loads.Load()
			token, state, err := src.StoredToken()
			if n := store.loads.Load() - before; n != 1 {
				t.Errorf("StoredToken() read the store %d time(s), want 1", n)
			}
			if token != "" || state != forgeapi.CredRefreshDue {
				t.Errorf("StoredToken() with a rotated pair pending = (%q, %v, _), want (\"\", %v, _)", token, state, forgeapi.CredRefreshDue)
			}
			fe, ok := errors.AsType[*forgeapi.Error](err)
			if !ok || fe.Kind != forgeapi.KindTransient || !fe.Retryable || fe.Code != "" || fe.Family != forgeapi.FamilyGitHub {
				t.Errorf("StoredToken() with a rotated pair pending = error %#v, want a retryable transient *forgeapi.Error with no code, for GitHub", err)
			}
			if field := sameRecord(load(t, reopen(t, dir), "conn"), base); field != "" {
				t.Errorf("the stored record's %s changed, want the record untouched: StoredToken never writes", field)
			}

			if token, err := src.Token(t.Context()); err != nil || token != "token-new" {
				t.Errorf("Token() after StoredToken = (%q, %v), want (%q, nil): the pair stays held for Token to store", token, err, "token-new")
			}
			if n := len(ep.requests()); n != 1 {
				t.Errorf("the token endpoint received %d request(s), want 1: nothing refreshes while the pair is held", n)
			}
		})
	}
}

func TestSource_StoredToken_requires_a_reconnect_where_State_does(t *testing.T) {
	cases := []struct {
		rec   func(base string) *creds.Record
		name  string
		spent bool
	}{
		{name: "no record", rec: func(string) *creds.Record { return nil }},
		{name: "static past its expiry", rec: func(base string) *creds.Record {
			rec := staticRecord(base, time.Now().Add(-time.Hour).Truncate(time.Second))
			return &rec
		}},
		{name: "marked spent", spent: true, rec: func(base string) *creds.Record {
			rec := withMark(rotating(forgeapi.FamilyGitHub, base, 8*time.Hour, 8*time.Hour), markSpent)
			return &rec
		}},
		{name: "marked reconnect required", rec: func(base string) *creds.Record {
			rec := withMark(rotating(forgeapi.FamilyGitHub, base, 8*time.Hour, 8*time.Hour), markReconnectRequired)
			return &rec
		}},
		{name: "refresh token past its own expiry", rec: func(base string) *creds.Record {
			rec := rotating(forgeapi.FamilyGitHub, base, 8*time.Hour, time.Minute)
			rec.RefreshExpiry = time.Now().Add(-time.Hour).Truncate(time.Second)
			return &rec
		}},
		{name: "rotating with no expiry", rec: func(base string) *creds.Record {
			rec := rotating(forgeapi.FamilyGitHub, base, 8*time.Hour, 8*time.Hour)
			rec.Expiry = time.Time{}
			return &rec
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep := newEndpoint(t, unreachable(t))
			store, _ := openStore(t)
			if rec := tc.rec(ep.srv.URL); rec != nil {
				save(t, store, "conn", *rec)
			}
			counted := &outcomes{}
			src := source(t, store, "conn", ep, forgeapi.WithCounters(counted.counters()))

			token, state, err := src.StoredToken()
			if token != "" || state != forgeapi.CredReconnectRequired || codeOf(err) != forgeapi.CodeReconnectRequired {
				t.Errorf("StoredToken() = (%q, %v, %v), want (\"\", %v, code %q)", token, state, err, forgeapi.CredReconnectRequired, forgeapi.CodeReconnectRequired)
			}
			if names := strings.Contains(strings.ToLower(messageOf(err)), "spent"); names != tc.spent {
				t.Errorf("StoredToken() = message %q, want it to name spent only for a spent record", messageOf(err))
			}
			if got := counted.list(); len(got) != 0 {
				t.Errorf("RefreshOutcome reported %v, want none: StoredToken records no outcome", got)
			}
		})
	}
}

// The terminal answer's mark could not be written, so only this process's custody
// knows it: StoredToken honours it and leaves it owed rather than writing it.
func TestSource_StoredToken_honours_a_mark_still_owed_to_the_store(t *testing.T) {
	store, dir := openStore(t)
	ep := newEndpoint(t, func(n int, _ exchange) (int, string) {
		if n == 1 {
			if err := os.Chmod(dir, 0o750); err != nil {
				t.Errorf("Setup: widening the store's directory: %v", err)
			}
		}
		return http.StatusOK, `{"error":"bad_refresh_token"}`
	})
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("Setup: narrowing the store's directory: %v", err)
		}
	})
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	src := source(t, store, "conn", ep)
	if _, err := src.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
		t.Fatalf("Setup: Token() over a terminal answer = error %v, want code %q", err, forgeapi.CodeReconnectRequired)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("Setup: narrowing the store's directory: %v", err)
	}

	token, state, err := src.StoredToken()
	if token != "" || state != forgeapi.CredReconnectRequired || codeOf(err) != forgeapi.CodeReconnectRequired {
		t.Errorf("StoredToken() with the mark owed = (%q, %v, %v), want (\"\", %v, code %q)", token, state, err, forgeapi.CredReconnectRequired, forgeapi.CodeReconnectRequired)
	}
	checkMarkStored(t, dir, markUnmarked)
}

func TestSource_StoredToken_fails_with_the_stores_error_where_it_cannot_be_read(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, dir := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, 8*time.Hour))
	src := source(t, store, "conn", ep)
	widen(t, dir)
	_, _, want := store.Load("conn")
	if want == nil {
		t.Fatal("Setup: Load() over a widened directory = no error, want the store's refusal")
	}

	token, state, err := src.StoredToken()
	if token != "" || state != forgeapi.CredUnknown || err == nil || err.Error() != want.Error() {
		t.Errorf("StoredToken() = (%q, %v, %v), want (\"\", %v, %v)", token, state, err, forgeapi.CredUnknown, want)
	}
}
