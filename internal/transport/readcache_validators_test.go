package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

var errCut = errors.New("connection cut")

// RFC 9111 section 4.3.4: a 304 whose validator names another representation must
// not update or serve the held one. It is read again in full, and that answer is
// what later 304s confirm.
func TestANotModifiedAnswerNamingAnotherRepresentationIsReadInFull(t *testing.T) {
	const (
		dayOne = "Wed, 25 Oct 2023 19:17:59 GMT"
		dayTwo = "Thu, 26 Oct 2023 08:00:00 GMT"
	)
	for _, test := range []struct {
		change func(*origin)
		name   string
		header string
		want   string
	}{
		{name: "another_etag", header: "If-None-Match", want: `"v2"`, change: func(o *origin) { o.etag = `"v2"` }},
		{name: "another_last_modified", header: "If-Modified-Since", want: dayTwo, change: func(o *origin) {
			o.modified = dayTwo
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, "")
			if test.header == "If-None-Match" {
				o.etag = `"v1"`
			} else {
				o.modified = dayOne
			}
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
			if _, _, err := read(t, c, nil); err != nil {
				t.Fatalf("Setup: the first read = %v", err)
			}
			o.set(func(o *origin) {
				o.body, o.notModified = `{"login":"new"}`, 1
				test.change(o)
			})
			if got, _, err := read(t, c, nil); err != nil || got.Login != "new" {
				t.Errorf("a read answered 304 naming another representation = %+v, %v, want the instance's new answer", got, err)
			}
			got, _, err := read(t, c, nil)
			if err != nil || got.Login != "new" {
				t.Errorf("the next read, confirmed by a 304 = %+v, %v, want the new answer, not the one held before", got, err)
			}
			if sent := o.last().Get(test.header); sent != test.want {
				t.Errorf("the next read sent %s %q, want %q", test.header, sent, test.want)
			}
		})
	}
}

// RFC 9110 section 8.8.3: an ETag is one entity-tag. A wildcard or a list replayed
// as If-None-Match would match representations the held answer is not, and a
// Last-Modified is replayed only as the HTTP date it must be.
func TestOnlyAWellFormedValidatorIsReplayed(t *testing.T) {
	const date = "Wed, 25 Oct 2023 19:17:59 GMT"
	for _, test := range []struct {
		name, etag, modified string
		wantTag, wantDate    string
	}{
		{name: "a_strong_etag", etag: `"v1"`, wantTag: `"v1"`},
		{name: "a_weak_etag", etag: `W/"v1"`, wantTag: `W/"v1"`},
		{name: "an_empty_etag", etag: `""`, wantTag: `""`},
		{name: "a_wildcard", etag: `*`},
		{name: "a_list", etag: `"v1", "v2"`},
		{name: "an_unquoted_tag", etag: `v1`},
		{name: "trailing_data", etag: `"v1"x`},
		{name: "a_lowercase_weak_prefix", etag: `w/"v1"`},
		{name: "a_bad_etag_beside_a_date", etag: `*`, modified: date, wantDate: date},
		{name: "a_date", modified: date, wantDate: date},
		{name: "not_a_date", modified: "yesterday"},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, test.etag)
			o.modified = test.modified
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
			if _, _, err := read(t, c, nil); err != nil {
				t.Fatalf("Setup: the first read = %v", err)
			}
			o.set(func(o *origin) { o.body = `{"login":"new"}` })
			got, _, err := read(t, c, nil)
			if err != nil {
				t.Fatal(err)
			}
			sent := o.last()
			if tag := sent.Get("If-None-Match"); tag != test.wantTag {
				t.Errorf("the second read sent If-None-Match %q, want %q", tag, test.wantTag)
			}
			if date := sent.Get("If-Modified-Since"); date != test.wantDate {
				t.Errorf("the second read sent If-Modified-Since %q, want %q", date, test.wantDate)
			}
			if test.wantTag == "" && test.wantDate == "" && got.Login != "new" {
				t.Errorf("the second read = %+v, want the instance's new answer: nothing valid was held to confirm", got)
			}
		})
	}
}

// RFC 9111 section 5.2.1.5: a request carrying no-store forbids keeping any part of
// its exchange.
func TestARequestForbiddingStorageIsNeverKept(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	c := openTestConn(t, forgeapi.Connection{
		WebBaseURL: serveOrigin(t, o).URL,
		Headers:    []forgeapi.Header{{Name: "Cache-Control", Value: "max-age=0, no-store"}},
	})
	for range 2 {
		if _, _, err := read(t, c, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := o.last().Get("If-None-Match"); got != "" {
		t.Errorf("a no-store request was sent with If-None-Match %q, want none", got)
	}
	if state := c.BudgetState().ReadCache; state != (forgeapi.ReadCacheState{}) {
		t.Errorf("ReadCache = %+v after no-store reads, want nothing held or counted", state)
	}
}

// A conditional request made by the cache is the one GitHub exempts; the
// unconditional resend after a 304 is billed like any request.
func TestOnlyAConditionalNotModifiedAnswerIsFree(t *testing.T) {
	for _, test := range []struct {
		setup  func(*origin)
		name   string
		want   int
		primed bool
	}{
		{name: "a_304_to_a_request_carrying_no_validator", want: 2, setup: func(o *origin) { o.notModified = 1 }},
		{name: "a_304_naming_another_representation", want: 1, primed: true, setup: func(o *origin) {
			o.etag, o.notModified = `"v2"`, 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			opts := testOptions()
			opts.FreeNotModified = true
			c := openConnWith(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, opts)
			if test.primed {
				if _, _, err := read(t, c, nil); err != nil {
					t.Fatalf("Setup: the first read = %v", err)
				}
			}
			o.set(test.setup)
			if _, _, err := read(t, c, nil); err != nil {
				t.Fatal(err)
			}
			state := c.BudgetState()
			if state.LastCost != test.want {
				t.Errorf("LastCost = %d, want %d: only a 304 to a request carrying a validator is free", state.LastCost, test.want)
			}
			if state.ReadCache.Saved != 0 || state.ReadCache.Hits != 0 {
				t.Errorf("ReadCache = %+v, want no hit and nothing saved: no held answer served the call", state.ReadCache)
			}
		})
	}
}

// Hits and Misses count each cacheable call the instance answered in full once:
// a hit where the held answer served it, a miss on every other outcome, an error
// status included. A call the wire never answered counts neither.
func TestEveryAnsweredCacheableReadCountsOnce(t *testing.T) {
	for _, test := range []struct {
		change func(*origin)
		name   string
		want   forgeapi.ReadCacheState
		abort  bool
	}{
		{name: "a_404", want: forgeapi.ReadCacheState{Misses: 2}, change: func(o *origin) { o.status = http.StatusNotFound }},
		{name: "a_500", want: forgeapi.ReadCacheState{Misses: 2}, change: func(o *origin) {
			o.status = http.StatusInternalServerError
		}},
		{name: "a_304_served", want: forgeapi.ReadCacheState{Hits: 1, Misses: 1}, change: func(*origin) {}},
		{name: "a_transport_failure", want: forgeapi.ReadCacheState{Misses: 1}, abort: true, change: func(*origin) {}},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			var abort atomic.Bool
			srv := serveOrigin(t, o)
			cut := func(next http.RoundTripper) http.RoundTripper {
				return roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if abort.Load() {
						return nil, errCut
					}
					return next.RoundTrip(r)
				})
			}
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithRetries(0), forgeapi.WithAttemptObserver(cut))
			if _, _, err := read(t, c, nil); err != nil {
				t.Fatalf("Setup: the first read = %v", err)
			}
			o.set(test.change)
			abort.Store(test.abort)
			_, _, _ = read(t, c, nil)
			got := c.BudgetState().ReadCache
			got.Bytes = 0
			if got != test.want {
				t.Errorf("ReadCache = %+v, want %+v", got, test.want)
			}
		})
	}
}

// gatedAccount is an account whose decode takes a while once slow is set.
type gatedAccount struct {
	slow *atomic.Bool
	account
}

//deadset:ignore DS1103 -- encoding/json calls it through json.Unmarshaler when the read decodes into a *gatedAccount.
func (a *gatedAccount) UnmarshalJSON(b []byte) error {
	if a.slow.Load() {
		time.Sleep(300 * time.Millisecond)
	}
	return json.Unmarshal(b, &a.account)
}

// A held answer served on a 304 whose call then fails did not answer the call, so
// it counts as a miss, not a hit.
func TestAServedReadThatFailsIsNoHit(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL},
		forgeapi.WithOperationTimeout(200*time.Millisecond), forgeapi.WithRetries(0))
	var slow atomic.Bool
	get := func() error {
		_, err := c.Do(Call(t.Context(), "Whoami"), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user", Into: &gatedAccount{slow: &slow}})
		return err
	}
	if err := get(); err != nil {
		t.Fatalf("Setup: the first read = %v", err)
	}
	slow.Store(true)
	if err := get(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Setup: a served read whose decode outlasts its bound = %v, want context.DeadlineExceeded", err)
	}
	got := c.BudgetState().ReadCache
	if got.Hits != 0 || got.Misses != 2 {
		t.Errorf("ReadCache = %+v, want Hits 0 and Misses 2: the failed call was not answered from the cache", got)
	}
}
