package creds_test

import (
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/creds"
	"github.com/cplieger/go/masked"
)

// mapBackend is a Backend a consumer could write: records in a map, a swap that
// compares with Record.Equal, and hooks a case uses to fail or race one call.
type mapBackend struct {
	recs map[string]creds.Record
	// swapFails answers the next Swap with an error; swapLands stores next first.
	swapFails, swapLands bool
	swaps                []swapCall
	mu                   sync.Mutex
}

type swapCall struct {
	prev, next creds.Record
	swapped    bool
}

var _ creds.Backend = (*mapBackend)(nil)

var errBackendDown = errors.New("the backend is down")

func newMapBackend() *mapBackend { return &mapBackend{recs: map[string]creds.Record{}} }

func (b *mapBackend) Load(key string) (creds.Record, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec, ok := b.recs[key]
	return rec, ok, nil
}

func (b *mapBackend) Keys() ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	keys := make([]string, 0, len(b.recs))
	for key := range b.recs {
		keys = append(keys, key)
	}
	return keys, nil
}

func (b *mapBackend) Put(key string, rec creds.Record) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recs[key] = rec
	return nil
}

func (b *mapBackend) Delete(key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.recs, key)
	return nil
}

func (b *mapBackend) Swap(key string, prev, next creds.Record) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.swapFails {
		b.swapFails = false
		if b.swapLands {
			b.recs[key] = next
		}
		b.swaps = append(b.swaps, swapCall{prev: prev, next: next})
		return false, errBackendDown
	}
	stored, ok := b.recs[key]
	swapped := ok && stored.Equal(&prev)
	if swapped {
		b.recs[key] = next
	}
	b.swaps = append(b.swaps, swapCall{prev: prev, next: next, swapped: swapped})
	return swapped, nil
}

func (b *mapBackend) failNextSwap(lands bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.swapFails, b.swapLands = true, lands
}

func (b *mapBackend) calls() []swapCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.swaps)
}

func TestNewStore_commits_a_rotation_through_the_backends_swap(t *testing.T) {
	ep := newEndpoint(t, func(int, exchange) (int, string) { return http.StatusOK, githubRotated })
	backend := newMapBackend()
	store := creds.NewStore(backend)
	old := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute)
	save(t, store, "conn", old)

	got, err := source(t, store, "conn", ep).Token(t.Context())
	if err != nil || got != "token-new" {
		t.Fatalf("Token() = (%q, %v), want (%q, nil)", got, err, "token-new")
	}
	calls := backend.calls()
	if len(calls) != 1 {
		t.Fatalf("the backend received %d swap(s), want 1", len(calls))
	}
	if field := sameRecord(calls[0].prev, old); field != "" || !calls[0].swapped {
		t.Errorf("Swap(prev) differs from the stored record at %q (swapped %v), want the record the rotation started from", field, calls[0].swapped)
	}
	rec := load(t, store, "conn")
	if rec.Token.Reveal() != "token-new" || rec.RefreshToken.Reveal() != "refresh-new" {
		t.Errorf("stored pair = (%q, %q), want (%q, %q)", rec.Token.Reveal(), rec.RefreshToken.Reveal(), "token-new", "refresh-new")
	}
}

func TestNewStore_keeps_a_record_saved_while_the_rotation_was_in_flight(t *testing.T) {
	backend := newMapBackend()
	store := creds.NewStore(backend)
	var ep *endpoint
	reconnected := creds.Record{Family: forgeapi.FamilyGitHub, Kind: forgeapi.CredKindStaticPAT, Token: masked.NewSecret("token-reconnected")}
	ep = newEndpoint(t, func(int, exchange) (int, string) {
		reconnected.WebBaseURL = ep.srv.URL
		if err := store.Save("conn", reconnected); err != nil {
			t.Errorf("Setup: the reconnect's Save = %v", err)
		}
		return http.StatusOK, githubRotated
	})
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))

	got, err := source(t, store, "conn", ep).Token(t.Context())
	if err != nil || got != "token-reconnected" {
		t.Errorf("Token() = (%q, %v), want (%q, nil): the reconnect is newer than the rotation", got, err, "token-reconnected")
	}
	if calls := backend.calls(); len(calls) != 1 || calls[0].swapped {
		t.Errorf("the backend received swaps %+v, want one that answered false", calls)
	}
	if rec := load(t, store, "conn"); rec.Token.Reveal() != "token-reconnected" {
		t.Errorf("stored Token = %q, want %q", rec.Token.Reveal(), "token-reconnected")
	}
}

func TestNewStore_holds_a_pair_the_backend_refused_and_stores_it_next(t *testing.T) {
	tests := []struct {
		name  string
		state forgeapi.CredState
		lands bool
	}{
		{name: "a_swap_that_stored_nothing", state: forgeapi.CredRefreshDue},
		{name: "a_swap_whose_write_landed_before_its_error", state: forgeapi.CredValid, lands: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ep := newEndpoint(t, func(int, exchange) (int, string) { return http.StatusOK, githubRotated })
			backend := newMapBackend()
			store := creds.NewStore(backend)
			save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
			src := source(t, store, "conn", ep)
			backend.failNextSwap(tc.lands)

			if _, err := src.Token(t.Context()); !errors.Is(err, errBackendDown) {
				t.Fatalf("Token() = error %v, want the backend's own error", err)
			}
			if s := src.State(); s != tc.state {
				t.Errorf("State() after the failed swap = %v, want %v", s, tc.state)
			}
			got, err := src.Token(t.Context())
			if err != nil || got != "token-new" {
				t.Errorf("the next Token() = (%q, %v), want (%q, nil), the held pair", got, err, "token-new")
			}
			if n := len(ep.requests()); n != 1 {
				t.Errorf("the endpoint received %d request(s), want 1: the held pair is stored, not refreshed again", n)
			}
			if rec := load(t, store, "conn"); rec.Token.Reveal() != "token-new" || rec.RefreshToken.Reveal() != "refresh-new" {
				t.Errorf("stored pair = (%q, %q), want (%q, %q)", rec.Token.Reveal(), rec.RefreshToken.Reveal(), "token-new", "refresh-new")
			}
		})
	}
}

func TestNewStore_answers_what_its_backend_holds(t *testing.T) {
	backend := newMapBackend()
	store := creds.NewStore(backend)
	rec := creds.Record{Family: forgeapi.FamilyGitLab, WebBaseURL: "https://gitlab.example", Kind: forgeapi.CredKindStaticPAT, Token: masked.NewSecret("token-static")}
	save(t, store, "one", rec)
	save(t, store, "two", rec)

	if got, ok, err := backend.Load("one"); err != nil || !ok || got.Token.Reveal() != "token-static" {
		t.Errorf("the backend's Load(one) after Save = (%+v, %v, %v), want the saved record", got, ok, err)
	}
	keys, err := store.Keys()
	slices.Sort(keys)
	if err != nil || !slices.Equal(keys, []string{"one", "two"}) {
		t.Errorf("Keys() = (%v, %v), want ([one two], nil)", keys, err)
	}
	if err := store.Delete("one"); err != nil {
		t.Fatalf("Delete(one) = %v, want nil", err)
	}
	if _, ok, err := store.Load("one"); err != nil || ok {
		t.Errorf("Load(one) after Delete = (_, %v, %v), want (_, false, nil)", ok, err)
	}
	if err := store.Delete("absent"); err != nil {
		t.Errorf("Delete(absent) = %v, want nil", err)
	}
}

func TestRecord_Equal_compares_every_field(t *testing.T) {
	base := rotating(forgeapi.FamilyGitHub, "https://github.example", 8*time.Hour, time.Hour)
	base.Scopes = []string{"repo", "read:org"}
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatalf("Setup: LoadLocation: %v", err)
	}
	tests := []struct {
		change func(r *creds.Record)
		name   string
		want   bool
	}{
		{name: "the_same_record", change: func(*creds.Record) {}, want: true},
		{name: "times_in_another_zone", want: true, change: func(r *creds.Record) {
			r.Issued, r.Expiry, r.RefreshExpiry = r.Issued.In(paris), r.Expiry.In(paris), r.RefreshExpiry.In(paris)
		}},
		{name: "family", change: func(r *creds.Record) { r.Family = forgeapi.FamilyGitLab }},
		{name: "web_base_url", change: func(r *creds.Record) { r.WebBaseURL = "https://other.example" }},
		{name: "kind", change: func(r *creds.Record) { r.Kind = forgeapi.CredKindStaticPAT }},
		{name: "token", change: func(r *creds.Record) { r.Token = masked.NewSecret("token-other") }},
		{name: "issued", change: func(r *creds.Record) { r.Issued = r.Issued.Add(time.Second) }},
		{name: "expiry", change: func(r *creds.Record) { r.Expiry = r.Expiry.Add(time.Second) }},
		{name: "refresh_token", change: func(r *creds.Record) { r.RefreshToken = masked.NewSecret("refresh-other") }},
		{name: "refresh_expiry", change: func(r *creds.Record) { r.RefreshExpiry = r.RefreshExpiry.Add(time.Second) }},
		{name: "client_id", change: func(r *creds.Record) { r.ClientID = "other-client-id" }},
		{name: "scopes_in_another_order", change: func(r *creds.Record) { r.Scopes = []string{"read:org", "repo"} }},
		{name: "scopes_one_fewer", change: func(r *creds.Record) { r.Scopes = []string{"repo"} }},
		{name: "account", change: func(r *creds.Record) { r.Account = "other-user" }},
		{name: "usability", change: func(r *creds.Record) { r.Usability = creds.UsabilitySpent }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			other := base
			other.Scopes = slices.Clone(base.Scopes)
			tc.change(&other)
			if got := base.Equal(&other); got != tc.want {
				t.Errorf("Equal(record with %s changed) = %v, want %v", tc.name, got, tc.want)
			}
			if got := other.Equal(&base); got != tc.want {
				t.Errorf("Equal from the changed side = %v, want %v", got, tc.want)
			}
		})
	}
}
