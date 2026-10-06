package creds_test

import (
	"context"
	"net/http"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// unstoredRotation runs one refresh whose rotated pair the store could not take, on
// one family, and answers the record the rotation started from, the source, its
// endpoint, the store's directory and restore, which lets writes succeed again.
func unstoredRotation(t *testing.T, family forgeapi.Family, rotated string) (base creds.Record, src *creds.Source, ep *endpoint, dir string, restore func()) {
	t.Helper()
	store, dir := openStore(t)
	ep, restore = failingWrite(t, dir, rotated)
	base = rotating(family, ep.srv.URL, 8*time.Hour, time.Minute)
	save(t, store, "conn", base)
	src = source(t, store, "conn", ep)
	if token, err := src.Token(t.Context()); err == nil || token != "" {
		t.Fatalf("Setup: Token() with the rotated pair unstorable = (%q, %v), want no token and the store's failure", token, err)
	}
	return base, src, ep, dir, restore
}

// The rotated pair is live upstream and in this process's hands after its write
// failed, so the next call stores it before anything else and hands it over: no
// further refresh, no reconnect, and no mark on the stored record.
func TestSource_stores_the_rotated_pair_on_the_next_call_after_a_failed_rotation_write(t *testing.T) {
	for _, arm := range refreshArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			_, src, ep, dir, restore := unstoredRotation(t, arm.family, arm.rotated)
			restore()

			token, err := src.Token(t.Context())
			if err != nil || token != "token-new" {
				t.Errorf("Token() once writes succeed = (%q, %v), want (%q, nil): the rotated pair the failed write left is the credential", token, err, "token-new")
			}
			if n := len(ep.requests()); n != 1 {
				t.Errorf("the token endpoint received %d request(s), want 1: the pending pair is stored, not refreshed again", n)
			}
			rec := load(t, reopen(t, dir), "conn")
			if rec.Token != "token-new" || rec.RefreshToken != "refresh-new" || rec.Usability != markUnmarked {
				t.Errorf("the record another process reads = (%q, %q, %s), want (%q, %q, unmarked): a live pair is held, so nothing is spent",
					rec.Token, rec.RefreshToken, markNames[rec.Usability], "token-new", "refresh-new")
			}
		})
	}
}

// While a rotated pair waits to be stored the connection needs work from the
// library, not from a human, which is what a non-durable write already reports. That
// holds whatever the record the rotation started from now reads as on its own: here
// its refresh token's expiry passes while the pair is pending, which alone would read
// as a reconnect, and the pending pair is still the credential.
func TestSource_reads_a_pending_rotated_pair_as_refresh_due(t *testing.T) {
	store, dir := openStore(t)
	ep, restore := failingWrite(t, dir, githubRotated)
	base := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute)
	base.RefreshExpiry = time.Now().Truncate(time.Second).Add(2 * time.Second)
	save(t, store, "conn", base)
	src := source(t, store, "conn", ep)
	if token, err := src.Token(t.Context()); err == nil || token != "" {
		t.Fatalf("Setup: Token() with the rotated pair unstorable = (%q, %v), want no token and the store's failure", token, err)
	}
	restore()
	time.Sleep(time.Until(base.RefreshExpiry) + 100*time.Millisecond)

	if s := src.State(); s != forgeapi.CredRefreshDue {
		t.Errorf("State() with a rotated pair pending, past the old refresh token's expiry = %v, want %v: the next call stores the pair", s, forgeapi.CredRefreshDue)
	}
}

// A second write that fails fails its own call and keeps the pair pending, so the
// write after it still stores the pair, and no call refreshes a pair already spent.
func TestSource_keeps_the_rotated_pair_pending_through_a_second_failed_write(t *testing.T) {
	_, src, ep, dir, restore := unstoredRotation(t, forgeapi.FamilyGitHub, githubRotated)

	if token, err := src.Token(t.Context()); err == nil || token != "" {
		t.Errorf("Token() with the store still refusing writes = (%q, %v), want no token and the store's failure", token, err)
	}
	restore()
	if rec := load(t, reopen(t, dir), "conn"); rec.Token != "token-old" || rec.Usability != markUnmarked {
		t.Errorf("the record another process reads after two failed writes = (%q, %s), want (%q, unmarked): nothing reached the store",
			rec.Token, markNames[rec.Usability], "token-old")
	}

	token, err := src.Token(t.Context())
	if err != nil || token != "token-new" {
		t.Errorf("Token() once writes succeed = (%q, %v), want (%q, nil): the pair stayed pending through the second failure", token, err, "token-new")
	}
	if n := len(ep.requests()); n != 1 {
		t.Errorf("the token endpoint received %d request(s), want 1", n)
	}
}

// The pending pair belongs to the record the rotation started from, compared whole,
// mark included, so once something marks that record the pair is dropped, the mark
// stands and nothing is handed over.
func TestSource_drops_a_pending_pair_whose_base_record_was_marked(t *testing.T) {
	base, src, ep, dir, restore := unstoredRotation(t, forgeapi.FamilyGitHub, githubRotated)
	restore()
	save(t, reopen(t, dir), "conn", withMark(base, markReconnectRequired))

	checkDeclinedFor(t, src, ep, 1, markReconnectRequired)
	if rec := load(t, reopen(t, dir), "conn"); rec.Token != "token-old" || rec.Usability != markReconnectRequired {
		t.Errorf("the stored record = (%q, %s), want (%q, reconnect required): the pending pair is not written over a record it does not belong to",
			rec.Token, markNames[rec.Usability], "token-old")
	}
}

// waitWatch is a caller's context that reports the first time the call asks for its
// Done channel, which is when that call waits for the key's refresh owner: the
// machine consults a caller's context to bound that wait and, on a key holding no
// pending pair, nowhere before it.
type waitWatch struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (w *waitWatch) Done() <-chan struct{} {
	w.once.Do(func() { close(w.waiting) })
	return w.Context.Done()
}

// A call already waiting for the key's owner when the owner's rotation write fails
// is the next call for the key: once it owns the key it stores the pair the owner
// holds rather than refreshing with the refresh token that pair replaced, which the
// product answers as spent and which would cost the connection a reconnect.
func TestSource_a_call_waiting_behind_a_failed_rotation_write_stores_the_pair(t *testing.T) {
	store, dir := openStore(t)
	arrived, release := make(chan struct{}), make(chan struct{})
	ep := newEndpoint(t, func(n int, _ exchange) (int, string) {
		if n == 1 {
			close(arrived)
			<-release
			if err := os.Chmod(dir, 0o750); err != nil {
				t.Errorf("Setup: widening the store's directory: %v", err)
			}
		}
		return http.StatusOK, githubRotated
	})
	var released sync.Once
	open := func() { released.Do(func() { close(release) }) }
	t.Cleanup(open)
	restore := func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("Setup: narrowing the store's directory: %v", err)
		}
	}
	t.Cleanup(restore)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	// The owner reports its failed write's outcome after holding the pair and before
	// it gives the key up, so writes succeed again for the call waiting behind it.
	narrowed := forgeapi.Counters{RefreshOutcome: func(forgeapi.Family, forgeapi.CredState) { restore() }}
	src := source(t, store, "conn", ep, forgeapi.WithCounters(narrowed))

	owner := make(chan error, 1)
	go func() {
		_, err := src.Token(t.Context())
		owner <- err
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("Setup: the owner's refresh reached no endpoint within 5s")
	}
	watch := &waitWatch{Context: t.Context(), waiting: make(chan struct{})}
	type result struct {
		err   error
		token string
	}
	waiter := make(chan result, 1)
	go func() {
		token, err := src.Token(watch)
		waiter <- result{token: token, err: err}
	}()
	select {
	case <-watch.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("Setup: the second call did not wait for the key's owner within 5s")
	}
	open()

	select {
	case err := <-owner:
		if err == nil {
			t.Errorf("the owner's Token() with its rotated pair unstorable = nil error, want the store's failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the owner's Token() has not returned 5s after its endpoint answered")
	}
	select {
	case got := <-waiter:
		if got.err != nil || got.token != "token-new" {
			t.Errorf("Token() waiting behind the failed write = (%q, %v), want (%q, nil): it stores the pair the owner holds", got.token, got.err, "token-new")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Token() waiting behind the failed write has not returned 5s after the owner did")
	}
	if n := len(ep.requests()); n != 1 {
		t.Errorf("the token endpoint received %d request(s), want 1: the waiting call stores the pair, it does not refresh with the token that pair replaced", n)
	}
	rec := load(t, reopen(t, dir), "conn")
	if rec.Token != "token-new" || rec.RefreshToken != "refresh-new" || rec.Usability != markUnmarked {
		t.Errorf("the stored record = (%q, %q, %s), want (%q, %q, unmarked)",
			rec.Token, rec.RefreshToken, markNames[rec.Usability], "token-new", "refresh-new")
	}
}

// Storing the held pair is the refresh reaching a valid token, so it is counted as
// one, after the failed write's own outcome.
func TestSource_counts_the_held_pair_it_stores_as_a_valid_outcome(t *testing.T) {
	store, dir := openStore(t)
	ep, restore := failingWrite(t, dir, githubRotated)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	o := &outcomes{}
	src := source(t, store, "conn", ep, forgeapi.WithCounters(o.counters()))
	if _, err := src.Token(t.Context()); err == nil {
		t.Fatal("Setup: Token() with the rotated pair unstorable = nil error, want the store's failure")
	}
	restore()

	if token, err := src.Token(t.Context()); err != nil || token != "token-new" {
		t.Fatalf("Token() once writes succeed = (%q, %v), want (%q, nil)", token, err, "token-new")
	}
	if got, want := o.list(), []string{"github refresh_due", "github valid"}; !slices.Equal(got, want) {
		t.Errorf("RefreshOutcome reported %q, want %q", got, want)
	}
}
