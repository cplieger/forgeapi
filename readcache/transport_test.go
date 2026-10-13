package readcache_test

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/readcache"
)

// The tests in this file are written from RFC 9110 section 13.1 (the validators a
// client replays), RFC 9111 sections 4.3.4 and 5.2 (what a 304 updates, and
// no-store), and GitHub's statement that a 304 to an authorized conditional request
// does not count against the primary rate limit
// (docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api).

// server answers its body with an ETag, and 304 to a request naming that ETag.
type server struct {
	header      http.Header
	notModified http.Header
	got         []*http.Request
	body        string
	etag        string
	status      int
	// always304 answers 304 to every request, carrying a validator or not.
	always304 int
	mu        sync.Mutex
}

func newServer(t *testing.T, body string) (*server, string) {
	t.Helper()
	s := &server{body: body, etag: `"v1"`, status: http.StatusOK, header: http.Header{}, notModified: http.Header{}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, r.Clone(context.Background()))
	w.Header().Set("ETag", s.etag)
	if s.always304 > 0 || (s.status == http.StatusOK && slices.Contains(r.Header.Values("If-None-Match"), s.etag)) {
		if s.always304 > 0 {
			s.always304--
		}
		maps.Copy(w.Header(), s.notModified)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	maps.Copy(w.Header(), s.header)
	w.WriteHeader(s.status)
	_, _ = io.WriteString(w, s.body)
}

func (s *server) set(f func(*server)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *server) requests() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got)
}

// lastValidator is the If-None-Match the server's last request carried.
func (s *server) lastValidator() string {
	got := s.requests()
	if len(got) == 0 {
		return ""
	}
	return got[len(got)-1].Header.Get("If-None-Match")
}

func newTransport(t *testing.T, next http.RoundTripper, opts ...readcache.Option) *readcache.Transport {
	t.Helper()
	if next == nil {
		next = http.DefaultTransport
	}
	tr, err := readcache.NewTransport(next, opts...)
	if err != nil {
		t.Fatalf("Setup: NewTransport = %v", err)
	}
	return tr
}

// get sends one request through tr and reads its answer in full.
func get(t *testing.T, tr http.RoundTripper, method, url string, header http.Header) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, nil)
	if err != nil {
		t.Fatalf("Setup: NewRequest = %v", err)
	}
	maps.Copy(req.Header, header)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatalf("%s %s = %v, want an answer", method, url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the answer to %s %s = %v", method, url, err)
	}
	return resp.StatusCode, resp.Header, string(body)
}

// auth is the header a request carrying token sends, none for no token.
func auth(token string) http.Header {
	if token == "" {
		return nil
	}
	return http.Header{"Authorization": {token}}
}

func TestAnUnchangedReadIsServedFromTheHeldAnswer(t *testing.T) {
	s, url := newServer(t, `{"n":1}`)
	tr := newTransport(t, nil)
	for i, want := range []string{"", `"v1"`} {
		status, _, body := get(t, tr, http.MethodGet, url, nil)
		if status != http.StatusOK || body != `{"n":1}` {
			t.Errorf("read %d = %d %q, want 200 and the server's body", i, status, body)
		}
		if got := s.lastValidator(); got != want {
			t.Errorf("read %d carried If-None-Match %q, want %q", i, got, want)
		}
	}
	if got := tr.State(); got.Hits != 1 || got.Misses != 1 || got.Bytes <= 0 {
		t.Errorf("State = %+v, want 1 hit, 1 miss and the answer held", got)
	}
}

func TestEntriesAreSeparatedByCredential(t *testing.T) {
	s, url := newServer(t, `{}`)
	tr := newTransport(t, nil)
	for _, step := range []struct {
		token, want string
	}{
		{token: "Bearer one"},
		{token: "Bearer two"},
		{token: "Bearer one", want: `"v1"`},
		{token: "", want: ""},
	} {
		get(t, tr, http.MethodGet, url, auth(step.token))
		if got := s.lastValidator(); got != step.want {
			t.Errorf("a read as %q carried If-None-Match %q, want %q: an answer one credential read never revalidates for another", step.token, got, step.want)
		}
	}
}

// identityServer answers one ETag to every request, and 304 to one naming it, with
// a body naming what the request's identity reads: a held body served to another
// identity reads as that other identity's.
func identityServer(t *testing.T, identity func(*http.Request) string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"shared"`)
		if r.Header.Get("If-None-Match") == `"shared"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, identity(r))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestARequestsIdentityIsKeyedHoweverItIsWritten(t *testing.T) {
	for _, test := range []struct {
		identity func(*http.Request) string
		set      func(r *http.Request, value string)
		name     string
	}{
		{
			name:     "a_lowercase_authorization",
			identity: func(r *http.Request) string { return r.Header.Get("Authorization") },
			set:      func(r *http.Request, v string) { r.Header["authorization"] = []string{v} },
		},
		{
			name:     "a_lowercase_accept",
			identity: func(r *http.Request) string { return r.Header.Get("Accept") },
			set:      func(r *http.Request, v string) { r.Header["accept"] = []string{v} },
		},
		{
			name:     "another_host",
			identity: func(r *http.Request) string { return r.Host },
			set:      func(r *http.Request, v string) { r.Host = v },
		},
		{
			name:     "another_user_agent",
			identity: func(r *http.Request) string { return r.Header.Get("User-Agent") },
			set:      func(r *http.Request, v string) { r.Header.Set("User-Agent", v) },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			url := identityServer(t, test.identity)
			tr := newTransport(t, nil)
			for _, value := range []string{"one.example", "two.example"} {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
				if err != nil {
					t.Fatal(err)
				}
				test.set(req, value)
				resp, err := tr.RoundTrip(req)
				if err != nil {
					t.Fatalf("the read as %q = %v", value, err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if string(body) != value {
					t.Errorf("the read as %q answered %q, want its own answer", value, body)
				}
			}
		})
	}
}

// A credential next sets is on no request the transport keyed, so an answer to it
// is neither served from nor kept for that key.
func TestACredentialNextSetsIsNeverShared(t *testing.T) {
	for _, test := range []struct {
		report func(*http.Response)
		name   string
	}{
		{name: "an_answer_reporting_its_request", report: func(*http.Response) {}},
		{name: "an_answer_naming_no_request", report: func(resp *http.Response) { resp.Request = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			url := identityServer(t, func(r *http.Request) string { return r.Header.Get("Authorization") })
			var token atomic.Value
			next := roundTrip(func(r *http.Request) (*http.Response, error) {
				r = r.Clone(r.Context())
				r.Header.Set("Authorization", token.Load().(string))
				resp, err := http.DefaultTransport.RoundTrip(r)
				if err == nil {
					test.report(resp)
				}
				return resp, err
			})
			tr := newTransport(t, next)
			for _, value := range []string{"Bearer one", "Bearer two"} {
				token.Store(value)
				if _, _, body := get(t, tr, http.MethodGet, url, nil); body != value {
					t.Errorf("the read next sent as %q answered %q, want its own answer", value, body)
				}
			}
			if got := tr.State(); got.Hits != 0 || got.Bytes != 0 {
				t.Errorf("State = %+v, want no hit and nothing held for a request next rewrote", got)
			}
		})
	}
}

func TestALowercaseNoStoreIsNeverKept(t *testing.T) {
	s, url := newServer(t, `{}`)
	tr := newTransport(t, nil)
	for range 2 {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header["cache-control"] = []string{"no-store"}
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if got := s.lastValidator(); got != "" || tr.State().Bytes != 0 {
		t.Errorf("a lowercase no-store read carried If-None-Match %q and left %d bytes held, want none and nothing", got, tr.State().Bytes)
	}
}

func TestAServedAnswerCarriesNoCodingOverItsHeldBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Encoding", "gzip")
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		zw := gzip.NewWriter(w)
		_, _ = io.WriteString(zw, `{"n":1}`)
		_ = zw.Close()
	}))
	t.Cleanup(srv.Close)
	tr := newTransport(t, nil)
	for i := range 2 {
		status, header, body := get(t, tr, http.MethodGet, srv.URL, nil)
		if status != http.StatusOK || body != `{"n":1}` {
			t.Fatalf("read %d = %d %q, want 200 and the decoded body", i, status, body)
		}
		if got := header.Values("Content-Encoding"); len(got) != 0 {
			t.Errorf("read %d carries Content-Encoding %q over its decoded body, want none", i, got)
		}
	}
	if got := tr.State().Hits; got != 1 {
		t.Errorf("the reads hit %d time(s), want the second served", got)
	}
}

func TestAHeldFieldKeepsEachOfItsLines(t *testing.T) {
	s, url := newServer(t, `[]`)
	s.header = http.Header{"Set-Cookie": {"a=1; Path=/", "b=2; Path=/"}}
	tr := newTransport(t, nil, readcache.WithHeldFields("set-cookie"))
	get(t, tr, http.MethodGet, url, nil)
	for i := range 2 {
		_, header, _ := get(t, tr, http.MethodGet, url, nil)
		if got, want := header.Values("Set-Cookie"), []string{"a=1; Path=/", "b=2; Path=/"}; !slices.Equal(got, want) {
			t.Errorf("served read %d's Set-Cookie lines = %q, want %q", i, got, want)
		}
		// A caller may rewrite the header it receives; the held lines stay its own.
		header["Set-Cookie"][0] = "rewritten"
	}
	if got := tr.State().Hits; got != 2 {
		t.Errorf("the reads hit %d time(s), want both repeats served", got)
	}
}

func TestOnlyTheDeclaredFieldsAreHeld(t *testing.T) {
	for _, test := range []struct {
		name        string
		held        []string
		notModified http.Header
		wantLink    string
		wantCustom  string
	}{
		{name: "a_declared_field_the_304_omits_keeps_the_held_value", held: []string{"Link"}, wantLink: "<page2>"},
		{
			name: "a_declared_field_the_304_carries_takes_the_304s_value", held: []string{"Link"},
			notModified: http.Header{"Link": {"<page3>"}}, wantLink: "<page3>",
		},
		{name: "an_undeclared_field_is_never_held"},
		{name: "an_undeclared_field_the_304_carries_is_the_304s", notModified: http.Header{"X-Custom": {"live"}}, wantCustom: "live"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, url := newServer(t, `[]`)
			s.header = http.Header{"Link": {"<page2>"}, "X-Custom": {"held"}}
			s.notModified = test.notModified
			tr := newTransport(t, nil, readcache.WithHeldFields(test.held...))
			get(t, tr, http.MethodGet, url, nil)
			status, header, body := get(t, tr, http.MethodGet, url, nil)
			if status != http.StatusOK || body != `[]` {
				t.Fatalf("the revalidated read = %d %q, want the held answer served", status, body)
			}
			if got := header.Get("Link"); got != test.wantLink {
				t.Errorf("the served Link = %q, want %q", got, test.wantLink)
			}
			if got := header.Get("X-Custom"); got != test.wantCustom {
				t.Errorf("the served X-Custom = %q, want %q", got, test.wantCustom)
			}
		})
	}
}

func TestSavedFollowsWhatTheServerIsDeclaredToBill(t *testing.T) {
	for _, test := range []struct {
		name      string
		token     string
		free      bool
		wantSaved int
	}{
		{name: "free_and_authorized", free: true, token: "Bearer t", wantSaved: 1},
		{name: "billed", free: false, token: "Bearer t"},
		{name: "free_but_anonymous", free: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, url := newServer(t, `{}`)
			tr := newTransport(t, nil, readcache.WithFreeNotModified(test.free))
			for range 2 {
				get(t, tr, http.MethodGet, url, auth(test.token))
			}
			if got := tr.State(); got.Hits != 1 || got.Saved != test.wantSaved {
				t.Errorf("State = %+v, want 1 hit and %d saved", got, test.wantSaved)
			}
		})
	}
}

// statusRecorder is an attempt observer wrapped around the wire: it sees each
// request that reaches the server, and the status the server answered.
type statusRecorder struct {
	next http.RoundTripper
	got  []int
	mu   sync.Mutex
}

func (o *statusRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := o.next.RoundTrip(r)
	if err == nil {
		o.mu.Lock()
		o.got = append(o.got, resp.StatusCode)
		o.mu.Unlock()
	}
	return resp, err
}

func TestAnObserverBeneathSeesEveryWireAttemptAsTheServerAnswered(t *testing.T) {
	for _, test := range []struct {
		name      string
		always304 int
		want      []int
	}{
		{name: "a_revalidated_read", want: []int{http.StatusOK, http.StatusNotModified}},
		{name: "a_validatorless_304_resent", always304: 1, want: []int{http.StatusNotModified, http.StatusOK, http.StatusNotModified}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, url := newServer(t, `{}`)
			s.always304 = test.always304
			observer := &statusRecorder{next: http.DefaultTransport}
			tr := newTransport(t, observer)
			for i := range 2 {
				if status, _, body := get(t, tr, http.MethodGet, url, nil); status != http.StatusOK || body != `{}` {
					t.Errorf("read %d = %d %q, want 200 and the server's body", i, status, body)
				}
			}
			if !slices.Equal(observer.got, test.want) || len(observer.got) != len(s.requests()) {
				t.Errorf("the observer saw %v over %d request(s), want %v, one per request", observer.got, len(s.requests()), test.want)
			}
		})
	}
}

func TestOnlyAGetIsCached(t *testing.T) {
	s, url := newServer(t, `{}`)
	tr := newTransport(t, nil)
	for range 2 {
		get(t, tr, http.MethodPost, url, nil)
	}
	if got := s.lastValidator(); got != "" {
		t.Errorf("a repeated POST carried If-None-Match %q, want none", got)
	}
	if got := tr.State(); got != (forgeapi.ReadCacheState{}) {
		t.Errorf("State after two POSTs = %+v, want nothing counted or held", got)
	}
}

func TestAnAnswerNotKeptForgetsTheHeldOne(t *testing.T) {
	for _, test := range []struct {
		name   string
		second func(*server)
	}{
		{name: "an_error", second: func(s *server) { s.status = http.StatusInternalServerError }},
		{name: "a_no_store_answer", second: func(s *server) { s.etag = `"v2"`; s.header.Set("Cache-Control", "no-store") }},
		{name: "an_answer_with_no_validator", second: func(s *server) { s.etag = "" }},
		{name: "an_encoded_body", second: func(s *server) { s.etag = `"v2"`; s.header.Set("Content-Encoding", "br") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, url := newServer(t, `{}`)
			tr := newTransport(t, nil)
			get(t, tr, http.MethodGet, url, nil)
			s.set(test.second)
			get(t, tr, http.MethodGet, url, nil)
			if got := tr.State().Bytes; got != 0 {
				t.Errorf("after %s the cache holds %d bytes, want none", test.name, got)
			}
			s.set(func(s *server) { s.status, s.etag = http.StatusOK, `"v1"` })
			get(t, tr, http.MethodGet, url, nil)
			if got := s.lastValidator(); got != "" {
				t.Errorf("the read after %s carried If-None-Match %q, want none", test.name, got)
			}
		})
	}
}

func TestARequestForbiddingStorageIsNeverRevalidated(t *testing.T) {
	s, url := newServer(t, `{}`)
	tr := newTransport(t, nil)
	for range 2 {
		get(t, tr, http.MethodGet, url, http.Header{"Cache-Control": {"no-store"}})
	}
	if got := s.lastValidator(); got != "" {
		t.Errorf("a no-store read carried If-None-Match %q, want none", got)
	}
	if got := tr.State(); got.Bytes != 0 {
		t.Errorf("after no-store reads the cache holds %d bytes, want none", got.Bytes)
	}
}

func TestAnUnvouchedNotModifiedIsSentOnceMore(t *testing.T) {
	for _, test := range []struct {
		name       string
		header     http.Header
		opts       []readcache.Option
		always304  int
		wantStatus int
		wantSent   int
	}{
		{name: "a_304_to_no_validator", always304: 1, wantStatus: http.StatusOK, wantSent: 2},
		{name: "a_second_304", always304: 2, wantStatus: http.StatusNotModified, wantSent: 2},
		{name: "with_the_cache_off", always304: 1, opts: []readcache.Option{readcache.WithMaxBytes(0)}, wantStatus: http.StatusOK, wantSent: 2},
		{name: "on_a_no_store_read", always304: 1, header: http.Header{"Cache-Control": {"no-store"}}, wantStatus: http.StatusOK, wantSent: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, url := newServer(t, `{}`)
			s.always304 = test.always304
			tr := newTransport(t, nil, test.opts...)
			status, _, _ := get(t, tr, http.MethodGet, url, test.header)
			if status != test.wantStatus || len(s.requests()) != test.wantSent {
				t.Errorf("the read = %d over %d request(s), want %d over %d", status, len(s.requests()), test.wantStatus, test.wantSent)
			}
			if got := s.requests()[len(s.requests())-1].Header.Get("If-None-Match"); got != "" {
				t.Errorf("the resend carried If-None-Match %q, want none", got)
			}
		})
	}
}

// A conditional request is one with a validator line carrying text, whatever its
// other lines: an empty list element names nothing (RFC 9110 section 5.6.1).
func TestACallersOwnConditionalRequestPassesThrough(t *testing.T) {
	for _, test := range []struct {
		header http.Header
		name   string
		held   bool
	}{
		{name: "one_validator", header: http.Header{"If-None-Match": {`"v1"`}}},
		{name: "a_validator_after_an_empty_line", header: http.Header{"If-None-Match": {"", `"v1"`}}},
		{name: "a_validator_after_an_empty_line_over_a_held_answer", header: http.Header{"If-None-Match": {"", `"v1"`}}, held: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, url := newServer(t, `{}`)
			tr := newTransport(t, nil)
			if test.held {
				get(t, tr, http.MethodGet, url, nil)
			}
			before, sent := tr.State(), len(s.requests())
			status, _, _ := get(t, tr, http.MethodGet, url, test.header)
			got := s.requests()
			if status != http.StatusNotModified || len(got) != sent+1 {
				t.Fatalf("the caller's conditional read = %d over %d request(s), want its own 304 over one", status, len(got)-sent)
			}
			if lines := got[sent].Header.Values("If-None-Match"); !slices.Equal(lines, test.header.Values("If-None-Match")) {
				t.Errorf("the caller's conditional read reached the server with If-None-Match %q, want its own %q", lines, test.header.Values("If-None-Match"))
			}
			if after := tr.State(); after != before {
				t.Errorf("State after the caller's own conditional read = %+v, want %+v: nothing counted or changed", after, before)
			}
		})
	}
}

func TestAnAnswerOverItsShareOfTheBoundIsNotKept(t *testing.T) {
	big := strings.Repeat("x", 4096)
	s, url := newServer(t, big)
	tr := newTransport(t, nil, readcache.WithMaxBytes(8192))
	for i := range 2 {
		if _, _, body := get(t, tr, http.MethodGet, url, nil); body != big {
			t.Errorf("read %d answered %d bytes, want the server's %d", i, len(body), len(big))
		}
	}
	if got := s.lastValidator(); got != "" {
		t.Errorf("the read after an answer over a quarter of the bound carried If-None-Match %q, want none", got)
	}
	if got := tr.State().Bytes; got != 0 {
		t.Errorf("the cache holds %d bytes, want none", got)
	}
}

func TestAZeroBoundSwitchesTheCacheOff(t *testing.T) {
	s, url := newServer(t, `{}`)
	tr := newTransport(t, nil, readcache.WithMaxBytes(0))
	for range 2 {
		get(t, tr, http.MethodGet, url, nil)
	}
	if got := s.lastValidator(); got != "" {
		t.Errorf("a read with the cache off carried If-None-Match %q, want none", got)
	}
	if got := tr.State(); got != (forgeapi.ReadCacheState{}) {
		t.Errorf("State with the cache off = %+v, want nothing counted or held", got)
	}
}

func TestANotModifiedNamingAnotherRepresentationIsReadInFull(t *testing.T) {
	s, url := newServer(t, `{"n":1}`)
	tr := newTransport(t, nil)
	get(t, tr, http.MethodGet, url, nil)
	s.set(func(s *server) {
		s.body, s.always304 = `{"n":2}`, 1
		s.notModified = http.Header{"Etag": {`"v2"`}}
	})
	status, _, body := get(t, tr, http.MethodGet, url, nil)
	if status != http.StatusOK || body != `{"n":2}` {
		t.Errorf("a read answered 304 for another ETag = %d %q, want the server's current body", status, body)
	}
	if got := len(s.requests()); got != 3 {
		t.Errorf("the server saw %d requests, want 3: the 304 naming another representation is sent once more", got)
	}
}

func TestAReadWhoseContextEndsKeepsNothing(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var validators []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		validators = append(validators, r.Header.Get("If-None-Match"))
		first := len(validators) == 1
		mu.Unlock()
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"n":`)
		if first {
			w.(http.Flusher).Flush()
			close(started)
			<-release
		}
		_, _ = io.WriteString(w, `1}`)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	tr := newTransport(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a read whose context ended = %v, want context.Canceled", err)
	}
	get(t, tr, http.MethodGet, srv.URL, nil)
	mu.Lock()
	defer mu.Unlock()
	if got := validators[len(validators)-1]; got != "" {
		t.Errorf("the read after a cancelled one carried If-None-Match %q, want none", got)
	}
}

func TestNewTransportRefusesWhatCannotRun(t *testing.T) {
	for _, test := range []struct {
		name string
		next http.RoundTripper
		code string
		opts []readcache.Option
	}{
		{name: "no_round_tripper", code: forgeapi.CodeConnectionInvalid},
		{name: "a_negative_bound", next: http.DefaultTransport, opts: []readcache.Option{readcache.WithMaxBytes(-1)}, code: forgeapi.CodeBudgetInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			tr, err := readcache.NewTransport(test.next, test.opts...)
			fe, ok := errors.AsType[*forgeapi.Error](err)
			if tr != nil || !ok || fe.Code != test.code {
				t.Errorf("NewTransport = %v, %v, want nil and %s", tr, err, test.code)
			}
		})
	}
}

func TestTheLeastRecentlyUsedEntryIsEvictedFirst(t *testing.T) {
	s, url := newServer(t, `{}`)
	probe := newTransport(t, nil)
	get(t, probe, http.MethodGet, url+"/a", nil)
	entry := probe.State().Bytes
	tr := newTransport(t, nil, readcache.WithMaxBytes(4*entry+entry/2))
	for _, path := range []string{"/a", "/b", "/c", "/d", "/a", "/e"} {
		get(t, tr, http.MethodGet, url+path, nil)
	}
	for _, test := range []struct {
		path, want string
	}{
		{path: "/b", want: ""},
		{path: "/a", want: `"v1"`},
		{path: "/e", want: `"v1"`},
	} {
		get(t, tr, http.MethodGet, url+test.path, nil)
		if got := s.lastValidator(); got != test.want {
			t.Errorf("a read of %s after a fifth entry carried If-None-Match %q, want %q: the least recently used entry goes first", test.path, got, test.want)
		}
	}
}

// endingBody ends its read's context as it answers the last of its bytes.
type endingBody struct {
	io.Reader
	cancel context.CancelFunc
}

func (b *endingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		b.cancel()
	}
	return n, err
}

func (*endingBody) Close() error { return nil }

func TestAReadWhoseContextEndsAsItsAnswerIsReadKeepsNothing(t *testing.T) {
	type cancelKey struct{}
	for _, test := range []struct {
		name   string
		status int
		sent   int
	}{
		{name: "a_200", status: http.StatusOK, sent: 1},
		{name: "a_304_twice", status: http.StatusNotModified, sent: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var validators []string
			wire := roundTrip(func(r *http.Request) (*http.Response, error) {
				validators = append(validators, r.Header.Get("If-None-Match"))
				cancel, _ := r.Context().Value(cancelKey{}).(context.CancelFunc)
				return &http.Response{
					StatusCode: test.status, Header: http.Header{"Etag": {`"v1"`}},
					Body: &endingBody{Reader: strings.NewReader(`{}`), cancel: cancel}, Request: r,
				}, nil
			})
			tr := newTransport(t, wire)
			for range 2 {
				ctx, cancel := context.WithCancel(t.Context())
				t.Cleanup(cancel)
				ctx = context.WithValue(ctx, cancelKey{}, cancel)
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://forge.example/x", nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := tr.RoundTrip(req)
				if err == nil {
					resp.Body.Close()
				}
				if !errors.Is(err, context.Canceled) {
					t.Errorf("a read whose context ended with its answer = %v, want context.Canceled", err)
				}
			}
			if len(validators) != 2*test.sent || slices.ContainsFunc(validators, func(v string) bool { return v != "" }) || tr.State().Bytes != 0 {
				t.Errorf("the reads carried %q and the cache holds %d bytes, want %d request(s) with no validator and nothing held", validators, tr.State().Bytes, 2*test.sent)
			}
		})
	}
}

// A wire attempt that fails once the request's context has ended answers the
// context's error, as an answer read after it ended does, and keeps nothing.
func TestAWireFailureAfterTheContextEndedAnswersTheContextsError(t *testing.T) {
	type cancelKey struct{}
	wire := roundTrip(func(r *http.Request) (*http.Response, error) {
		if cancel, ok := r.Context().Value(cancelKey{}).(context.CancelFunc); ok {
			cancel()
			return nil, errors.New("wire failed")
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Etag": {`"v1"`}},
			Body: io.NopCloser(strings.NewReader(`{}`)), Request: r,
		}, nil
	})
	tr := newTransport(t, wire)
	if _, _, body := get(t, tr, http.MethodGet, "http://forge.example/x", nil); body != `{}` || tr.State().Bytes == 0 {
		t.Fatalf("Setup: the first read answered %q holding %d bytes, want its answer kept", body, tr.State().Bytes)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(context.WithValue(ctx, cancelKey{}, cancel), http.MethodGet, "http://forge.example/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.RoundTrip(req)
	if err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a wire failure after the context ended = %v, want context.Canceled", err)
	}
	if got := tr.State().Bytes; got != 0 {
		t.Errorf("the cache holds %d bytes after the failed read, want the key forgotten", got)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// idleCloser is a round tripper that counts the calls to close its idle connections.
type idleCloser struct {
	http.RoundTripper
	closed int
}

func (c *idleCloser) CloseIdleConnections() { c.closed++ }

func TestClosingTheClientsIdleConnectionsReachesNext(t *testing.T) {
	next := &idleCloser{RoundTripper: http.DefaultTransport}
	(&http.Client{Transport: newTransport(t, next)}).CloseIdleConnections()
	if next.closed != 1 {
		t.Errorf("next's idle connections were closed %d time(s), want 1", next.closed)
	}
}
