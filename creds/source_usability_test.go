package creds_test

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// unreadablePair is a token endpoint's success answer cut short: the instance
// issued a pair, so the stored one is spent upstream, and the pair it issued cannot
// be read out of what arrived.
const unreadablePair = `{"access_token":"token-new","token_type":"bearer","expires_in":28800,"refresh_tok`

// messageOf is the message a *forgeapi.Error carries, empty for any other error.
func messageOf(err error) string {
	if fe, ok := errors.AsType[*forgeapi.Error](err); ok {
		return fe.Message
	}
	return ""
}

// checkMarkStored holds the store's record, read by another process, to one mark.
func checkMarkStored(t *testing.T, dir string, want creds.Usability) {
	t.Helper()
	if got := load(t, reopen(t, dir), "conn").Usability; got != want {
		t.Errorf("the record another process reads is %s, want %s", markNames[got], markNames[want])
	}
}

// checkDeclinedFor holds one Token call over a marked record to the refusal a mark
// earns: no token, the reconnect code, a message naming the mark, and no request.
func checkDeclinedFor(t *testing.T, src *creds.Source, ep *endpoint, sent int, mark creds.Usability) {
	t.Helper()
	token, err := src.Token(t.Context())
	if code := codeOf(err); code != forgeapi.CodeReconnectRequired || token != "" {
		t.Errorf("Token() over a record marked %s = (%q, %v), want no token and code %q", markNames[mark], token, err, forgeapi.CodeReconnectRequired)
	}
	if names := strings.Contains(strings.ToLower(messageOf(err)), "spent"); names != (mark == markSpent) {
		t.Errorf("Token() over a record marked %s = message %q, want it to name the mark: spent says spent, reconnect required does not", markNames[mark], messageOf(err))
	}
	if s := src.State(); s != forgeapi.CredReconnectRequired {
		t.Errorf("State() over a record marked %s = %v, want %v", markNames[mark], s, forgeapi.CredReconnectRequired)
	}
	if n := len(ep.requests()); n != sent {
		t.Errorf("the token endpoint received %d request(s), want %d: a marked record is never refreshed", n, sent)
	}
}

// A marked record is never handed over and never refreshed, whatever its kind and
// its expiry would decide without the mark.
func TestSource_hands_over_nothing_from_a_marked_record(t *testing.T) {
	records := map[string]func(base string) creds.Record{
		"fresh_rotating": func(base string) creds.Record {
			return rotating(forgeapi.FamilyGitHub, base, 8*time.Hour, 8*time.Hour)
		},
		"due_rotating": func(base string) creds.Record {
			return rotating(forgeapi.FamilyGitLab, base, 8*time.Hour, time.Minute)
		},
		"static": func(base string) creds.Record {
			return creds.Record{Family: forgeapi.FamilyGitea, WebBaseURL: base, Kind: forgeapi.CredKindStaticPAT, Token: "token-static", Account: "example-user"}
		},
	}
	for name, build := range records {
		for _, mark := range []creds.Usability{markSpent, markReconnectRequired} {
			t.Run(name+"_"+strings.ReplaceAll(markNames[mark], " ", "_"), func(t *testing.T) {
				ep := newEndpoint(t, unreachable(t))
				store, _ := openStore(t)
				save(t, store, "conn", withMark(build(ep.srv.URL), mark))

				checkDeclinedFor(t, source(t, store, "conn", ep), ep, 0, mark)
			})
		}
	}
}

// The terminal verdict is written to the record the refresh started from, so it
// outlives the process that reached it.
func TestSource_writes_reconnect_required_to_the_record_a_terminal_answer_ended(t *testing.T) {
	tests := []struct {
		body   string
		family forgeapi.Family
		status int
	}{
		{family: forgeapi.FamilyGitHub, status: http.StatusOK, body: `{"error":"bad_refresh_token"}`},
		{family: forgeapi.FamilyGitLab, status: http.StatusBadRequest, body: `{"error":"invalid_grant"}`},
	}
	for _, tc := range tests {
		t.Run(tc.family.String(), func(t *testing.T) {
			ep := newEndpoint(t, func(int, exchange) (int, string) { return tc.status, tc.body })
			store, dir := openStore(t)
			save(t, store, "conn", rotating(tc.family, ep.srv.URL, 8*time.Hour, time.Minute))

			if _, err := source(t, store, "conn", ep).Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
				t.Errorf("Token() over a terminal answer = error %v, want code %q", err, forgeapi.CodeReconnectRequired)
			}
			checkMarkStored(t, dir, markReconnectRequired)
		})
	}
}

// A stored record whose web base is not an origin and a path cannot be refreshed
// over that base by any retry, so its refresh ends as a terminal answer does:
// nothing is sent, the record is marked and Token answers a reconnect.
func TestSource_writes_reconnect_required_to_a_record_whose_base_it_refuses(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, dir := openStore(t)
	base := strings.Replace(ep.srv.URL, "://", "://user@", 1)
	save(t, store, "conn", rotating(forgeapi.FamilyGitLab, base, 8*time.Hour, -time.Minute))

	src := source(t, store, "conn", ep)
	if _, err := src.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
		t.Errorf("Token() over a stored base carrying userinfo = error %v, want code %q", err, forgeapi.CodeReconnectRequired)
	}
	if got := src.State(); got != forgeapi.CredReconnectRequired {
		t.Errorf("State() after a refused stored base = %v, want %v", got, forgeapi.CredReconnectRequired)
	}
	if n := len(ep.requests()); n != 0 {
		t.Errorf("the refresh over a stored base carrying userinfo sent %d request(s), want none", n)
	}
	checkMarkStored(t, dir, markReconnectRequired)
}

// An answer whose pair cannot be read is a rotation the instance made and this
// machine lost, so the stored pair is spent and the record says so.
func TestSource_writes_spent_to_the_record_whose_rotated_pair_it_could_not_read(t *testing.T) {
	for _, arm := range refreshArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			ep := newEndpoint(t, func(int, exchange) (int, string) { return http.StatusOK, unreadablePair })
			store, dir := openStore(t)
			save(t, store, "conn", rotating(arm.family, ep.srv.URL, 8*time.Hour, time.Minute))
			src := source(t, store, "conn", ep)

			if token, err := src.Token(t.Context()); err == nil || token != "" {
				t.Errorf("Token() over an unreadable rotation = (%q, %v), want no token and an error: the stored token was spent by the refresh", token, err)
			}
			checkMarkStored(t, dir, markSpent)
			checkDeclinedFor(t, src, ep, 1, markSpent)
		})
	}
}

// A reader in another process honours a verdict this one reached, which is what a
// verdict held in one process's memory could not do.
func TestSource_honours_a_mark_another_process_wrote(t *testing.T) {
	ep := newEndpoint(t, func(int, exchange) (int, string) { return http.StatusOK, `{"error":"bad_refresh_token"}` })
	store, dir := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	if _, err := source(t, store, "conn", ep).Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
		t.Fatalf("Setup: Token() over a terminal answer = error %v, want code %q", err, forgeapi.CodeReconnectRequired)
	}

	other := source(t, reopen(t, dir), "conn", ep)
	if s := other.State(); s != forgeapi.CredReconnectRequired {
		t.Errorf("State() in another process = %v, want %v", s, forgeapi.CredReconnectRequired)
	}
	checkDeclinedFor(t, other, ep, 1, markReconnectRequired)
}

// The mark is written by compare-and-swap over the record the refresh started from,
// so a reconnect saved while the refresh was in flight is never marked.
func TestSource_marks_nothing_when_a_reconnect_replaced_the_record_its_refresh_ended(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{name: "terminal", status: http.StatusOK, body: `{"error":"bad_refresh_token"}`},
		{name: "unreadable_pair", status: http.StatusOK, body: unreadablePair},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			arrived, release, ep := heldEndpoint(t, tc.status, tc.body)
			store, dir := openStore(t)
			save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
			src := source(t, store, "conn", ep)

			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = src.Token(t.Context())
			}()
			<-arrived
			reconnected := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, 8*time.Hour)
			reconnected.Token, reconnected.RefreshToken = "token-reconnected", "refresh-reconnected"
			save(t, store, "conn", reconnected)
			close(release)
			<-done

			rec := load(t, reopen(t, dir), "conn")
			if rec.Token != "token-reconnected" || rec.Usability != markUnmarked {
				t.Errorf("the stored record = (%q, %s), want (%q, unmarked): the answer was about a pair the connection no longer holds",
					rec.Token, markNames[rec.Usability], "token-reconnected")
			}
		})
	}
}

// failingWrite is a token endpoint that answers a rotation with the rotated answer
// given and, before it does, widens the store's directory, so the write of the
// rotated pair fails; restore narrows it again for the writes after.
func failingWrite(t *testing.T, dir, rotated string) (ep *endpoint, restore func()) {
	t.Helper()
	ep = newEndpoint(t, func(n int, _ exchange) (int, string) {
		if n == 1 {
			if err := os.Chmod(dir, 0o750); err != nil {
				t.Errorf("Setup: widening the store's directory: %v", err)
			}
		}
		return http.StatusOK, rotated
	})
	restore = func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("Setup: narrowing the store's directory: %v", err)
		}
	}
	t.Cleanup(restore)
	return ep, restore
}

// A reconnect saved after the failed write is a new record, so the rotated pair the
// key's custody holds, which belongs to the record the rotation started from, never
// reaches it.
func TestSource_keeps_a_reconnect_saved_after_a_failed_rotation_write_unmarked(t *testing.T) {
	store, dir := openStore(t)
	ep, restore := failingWrite(t, dir, githubRotated)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	src := source(t, store, "conn", ep)
	if token, err := src.Token(t.Context()); err == nil || token != "" {
		t.Fatalf("Setup: Token() with the rotated pair unstorable = (%q, %v), want no token and the store's failure", token, err)
	}
	restore()
	reconnected := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, 8*time.Hour)
	reconnected.Token, reconnected.RefreshToken = "token-reconnected", "refresh-reconnected"
	save(t, store, "conn", reconnected)

	token, err := src.Token(t.Context())
	if err != nil || token != "token-reconnected" {
		t.Errorf("Token() after the reconnect = (%q, %v), want (%q, nil): a reconnect saves a new, unmarked record", token, err, "token-reconnected")
	}
	rec := load(t, reopen(t, dir), "conn")
	if rec.Token != "token-reconnected" || rec.Usability != markUnmarked {
		t.Errorf("the stored record = (%q, %s), want (%q, unmarked)", rec.Token, markNames[rec.Usability], "token-reconnected")
	}
}

// Every reader derives a refresh token's own expiry from the record, so reaching it
// writes nothing.
func TestSource_writes_no_mark_for_a_refresh_token_past_its_own_expiry(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))
	store, dir := openStore(t)
	rec := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, -time.Minute)
	rec.RefreshExpiry = time.Now().Add(-time.Hour).Truncate(time.Second)
	save(t, store, "conn", rec)

	if _, err := source(t, store, "conn", ep).Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
		t.Errorf("Token() = error %v, want code %q", err, forgeapi.CodeReconnectRequired)
	}
	checkMarkStored(t, dir, markUnmarked)
}

// A mark the store could not take when the refresh ended stays owed, and the next
// call whose write succeeds writes it to the record.
func TestSource_writes_a_mark_the_store_could_not_take_on_the_next_call(t *testing.T) {
	store, dir := openStore(t)
	ep := newEndpoint(t, func(n int, _ exchange) (int, string) {
		if n == 1 {
			if err := os.Chmod(dir, 0o750); err != nil {
				t.Errorf("Setup: widening the store's directory: %v", err)
			}
		}
		return http.StatusOK, `{"error":"bad_refresh_token"}`
	})
	restore := func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("Setup: narrowing the store's directory: %v", err)
		}
	}
	t.Cleanup(restore)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute))
	src := source(t, store, "conn", ep)
	if _, err := src.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
		t.Fatalf("Setup: Token() over a terminal answer = error %v, want code %q", err, forgeapi.CodeReconnectRequired)
	}
	restore()

	if _, err := src.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
		t.Errorf("Token() once writes succeed = error %v, want code %q", err, forgeapi.CodeReconnectRequired)
	}
	checkMarkStored(t, dir, markReconnectRequired)
}

// Once written, a mark is settled: a record saved after it, even an identical copy of
// the one it marked, is never marked by this process.
func TestSource_never_marks_a_record_saved_after_its_mark_was_written(t *testing.T) {
	ep := newEndpoint(t, func(int, exchange) (int, string) { return http.StatusOK, `{"error":"bad_refresh_token"}` })
	store, dir := openStore(t)
	rec := rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, time.Minute)
	save(t, store, "conn", rec)
	src := source(t, store, "conn", ep)
	if _, err := src.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
		t.Fatalf("Setup: Token() over a terminal answer = error %v, want code %q", err, forgeapi.CodeReconnectRequired)
	}
	checkMarkStored(t, dir, markReconnectRequired)

	save(t, store, "conn", rec)
	if _, err := src.Token(t.Context()); codeOf(err) != forgeapi.CodeReconnectRequired {
		t.Errorf("Token() over a re-saved copy of the marked record = error %v, want code %q", err, forgeapi.CodeReconnectRequired)
	}
	checkMarkStored(t, dir, markUnmarked)
}
