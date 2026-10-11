package creds

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

const custodyRotated = `{"access_token":"token-new","token_type":"bearer","scope":"repo","expires_in":28800,"refresh_token":"refresh-new","refresh_token_expires_in":15897600}`

// hookedStore is a FileStore whose Load and acquire call a hook once they
// return, which is where a case pauses one call while others run.
type hookedStore struct {
	*FileStore
	afterLoad    func(found bool)
	afterAcquire func(r *rotation)
}

func (h *hookedStore) Load(key string) (Record, bool, error) {
	rec, ok, err := h.FileStore.Load(key)
	if h.afterLoad != nil {
		h.afterLoad(ok)
	}
	return rec, ok, err
}

func (h *hookedStore) acquire(key string) *rotation {
	r := h.FileStore.acquire(key)
	if h.afterAcquire != nil {
		h.afterAcquire(r)
	}
	return r
}

// pauseOnce answers a hook that blocks its first call until release runs, and
// closes reached when that call arrives.
func pauseOnce() (pause func(), reached <-chan struct{}, release func()) {
	arrived, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	pause = func() {
		first := false
		once.Do(func() { first = true })
		if first {
			close(arrived)
			<-resume
		}
	}
	return pause, arrived, sync.OnceFunc(func() { close(resume) })
}

type custodyFixture struct {
	store     *hookedStore
	dir       string
	exchanges *atomic.Int64
	srv       *httptest.Server
	// failWrite widens the directory while the next exchange answers, so the
	// rotation's write fails; restore narrows it again.
	failWrite atomic.Bool
	// onExchange runs inside each exchange before it answers.
	onExchange atomic.Pointer[func()]
}

func newCustodyFixture(t *testing.T) *custodyFixture {
	t.Helper()
	f := &custodyFixture{dir: filepath.Join(t.TempDir(), "credentials"), exchanges: &atomic.Int64{}}
	fs, err := OpenFileStore(f.dir)
	if err != nil {
		t.Fatalf("Setup: OpenFileStore: %v", err)
	}
	f.store = &hookedStore{FileStore: fs}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.exchanges.Add(1)
		if hook := f.onExchange.Load(); hook != nil {
			(*hook)()
		}
		if f.failWrite.Load() {
			if err := os.Chmod(f.dir, 0o750); err != nil {
				t.Errorf("Setup: widening the store's directory: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, custodyRotated); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *custodyFixture) restore(t *testing.T) {
	t.Helper()
	if err := os.Chmod(f.dir, 0o700); err != nil {
		t.Fatalf("Setup: narrowing the store's directory: %v", err)
	}
}

// due is a rotating record inside its refresh lead, so a token call refreshes it.
func (f *custodyFixture) due() Record {
	now := time.Now().Truncate(time.Second)
	return Record{
		Family: forgeapi.FamilyGitHub, WebBaseURL: f.srv.URL, Kind: forgeapi.CredKindRotatingOAuth,
		Token: "token-old", Issued: now.Add(-8 * time.Hour), Expiry: now.Add(time.Minute),
		RefreshToken: "refresh-old", RefreshExpiry: now.Add(30 * 24 * time.Hour), ClientID: "example-client-id",
	}
}

// live is a static token, which a token call hands over without a refresh.
func (f *custodyFixture) live() Record {
	return Record{Family: forgeapi.FamilyGitHub, WebBaseURL: f.srv.URL, Kind: forgeapi.CredKindStaticPAT, Token: "token-static"}
}

func (f *custodyFixture) save(t *testing.T, rec Record) {
	t.Helper()
	if err := f.store.Save("conn", rec); err != nil {
		t.Fatalf("Setup: Save: %v", err)
	}
}

func (f *custodyFixture) remove(t *testing.T) {
	t.Helper()
	if err := f.store.Delete("conn"); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}
}

func (f *custodyFixture) source(t *testing.T) *Source {
	t.Helper()
	src, err := NewSource(f.store, "conn", forgeapi.Connection{WebBaseURL: f.srv.URL},
		forgeapi.WithWireTransport(f.srv.Client().Transport),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err != nil {
		t.Fatalf("Setup: NewSource: %v", err)
	}
	return src
}

func (f *custodyFixture) custody() int {
	f.store.custodyMu.Lock()
	defer f.store.custodyMu.Unlock()
	return len(f.store.custody)
}

// Custody is created only by a token call and freed with its record, so a key per
// sign-in does not grow it and a state read writes none.
func TestFileStore_FreesAKeysCustodyWithItsRecord(t *testing.T) {
	t.Run("a_state_read_on_a_live_record_creates_none", func(t *testing.T) {
		f := newCustodyFixture(t)
		f.save(t, f.due())
		if got := f.source(t).State(); got != forgeapi.CredRefreshDue {
			t.Fatalf("State() = %v, want refresh-due", got)
		}
		if n := f.custody(); n != 0 {
			t.Errorf("custody holds %d key(s) after a state read, want 0", n)
		}
	})
	t.Run("a_saved_used_and_deleted_key_leaves_none", func(t *testing.T) {
		f := newCustodyFixture(t)
		f.save(t, f.live())
		if _, err := f.source(t).Token(t.Context()); err != nil {
			t.Fatalf("Token() = %v, want the static token", err)
		}
		f.remove(t)
		if n := f.custody(); n != 0 {
			t.Errorf("custody holds %d key(s) after the record was deleted, want 0", n)
		}
	})
	t.Run("a_state_read_paused_across_a_delete_and_reads_of_the_deleted_key_add_none", func(t *testing.T) {
		f := newCustodyFixture(t)
		f.save(t, f.live())
		src := f.source(t)
		pause, reached, release := pauseOnce()
		f.store.afterLoad = func(bool) { pause() }
		done := make(chan struct{})
		go func() {
			defer close(done)
			src.State()
		}()
		<-reached
		f.remove(t)
		release()
		<-done
		f.store.afterLoad = nil
		src.State()
		if _, err := src.Token(t.Context()); err == nil {
			t.Error("Token() on a deleted key = nil error, want reconnect-required")
		}
		if n := f.custody(); n != 0 {
			t.Errorf("custody holds %d key(s), want 0", n)
		}
	})
	t.Run("a_token_call_paused_before_it_owns_the_key_shares_one_rotation_with_a_later_one", func(t *testing.T) {
		f := newCustodyFixture(t)
		f.save(t, f.due())
		src := f.source(t)
		pause, reached, release := pauseOnce()
		var mu sync.Mutex
		var seen []*rotation
		f.store.afterAcquire = func(r *rotation) {
			mu.Lock()
			seen = append(seen, r)
			mu.Unlock()
			pause()
		}
		done := make(chan error, 1)
		go func() {
			_, err := src.Token(t.Context())
			done <- err
		}()
		<-reached
		f.remove(t)
		f.save(t, f.due())
		if _, err := src.Token(t.Context()); err != nil {
			t.Fatalf("the second Token() = %v, want the rotated token", err)
		}
		release()
		if err := <-done; err != nil {
			t.Fatalf("the paused Token() = %v, want the rotated token", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != 2 || seen[0] != seen[1] {
			t.Errorf("the two token calls held %d rotation(s) %v, want one shared: two owners of one key refresh it twice", len(seen), seen)
		}
		if n := f.exchanges.Load(); n != 1 {
			t.Errorf("the token server received %d exchange(s), want 1", n)
		}
	})
	t.Run("a_delete_during_a_refresh_keeps_the_custody_until_the_refresh_returns", func(t *testing.T) {
		f := newCustodyFixture(t)
		f.save(t, f.due())
		src := f.source(t)
		hold := make(chan struct{})
		inFlight := make(chan struct{})
		var once sync.Once
		hook := func() {
			once.Do(func() { close(inFlight) })
			<-hold
		}
		f.onExchange.Store(&hook)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = src.Token(t.Context())
		}()
		<-inFlight
		f.remove(t)
		if n := f.custody(); n != 1 {
			t.Errorf("custody holds %d key(s) while the refresh is in flight, want 1", n)
		}
		close(hold)
		<-done
		if n := f.custody(); n != 0 {
			t.Errorf("custody holds %d key(s) after the refresh returned, want 0", n)
		}
	})
	t.Run("a_token_call_that_found_no_record_keeps_a_later_refreshs_held_pair", func(t *testing.T) {
		f := newCustodyFixture(t)
		src := f.source(t)
		pause, reached, release := pauseOnce()
		f.store.afterLoad = func(found bool) {
			if !found {
				pause()
			}
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = src.Token(t.Context())
		}()
		<-reached
		f.save(t, f.due())
		f.failWrite.Store(true)
		if _, err := src.Token(t.Context()); err == nil {
			t.Fatal("Setup: the refresh whose write failed = nil error, want the store's failure")
		}
		f.failWrite.Store(false)
		f.restore(t)
		release()
		<-done
		token, err := src.Token(t.Context())
		if err != nil || token != "token-new" {
			t.Errorf("the next Token() = (%q, %v), want the held pair's token-new", token, err)
		}
		if n := f.exchanges.Load(); n != 1 {
			t.Errorf("the token server received %d exchange(s), want 1: the held pair is stored, not refreshed again", n)
		}
	})
}
