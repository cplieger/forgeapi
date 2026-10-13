package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"

	"github.com/cplieger/forgeapi"
)

// The tests in this file are written from RFC 9110 section 13.1 (the two
// validators a client replays), RFC 9111 sections 4.3.4 and 5.2.2.5 (what a
// not-modified answer updates, and no-store), and GitHub's statement that an
// authorized not-modified answer does not count against the primary rate limit
// (docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api).

// account is the body every revalidating server answers with.
type account struct {
	Login string `json:"login"`
}

// origin is a revalidating instance: it answers its current body with an ETag
// naming the version, and 304 to a request carrying that ETag.
type origin struct {
	header      http.Header
	got         []http.Header
	body        string
	etag        string
	modified    string
	status      int
	notModified int
	mu          sync.Mutex
}

func newOrigin(body, etag string) *origin {
	return &origin{body: body, etag: etag, status: http.StatusOK, header: http.Header{}}
}

func (o *origin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.got = append(o.got, r.Header.Clone())
	maps.Copy(w.Header(), o.header)
	if o.etag != "" {
		w.Header().Set("ETag", o.etag)
	}
	if o.modified != "" {
		w.Header().Set("Last-Modified", o.modified)
	}
	w.Header().Set("Content-Type", "application/json")
	conditional := (o.etag != "" && slices.Contains(r.Header.Values("If-None-Match"), o.etag)) ||
		(o.modified != "" && r.Header.Get("If-Modified-Since") == o.modified)
	if o.status == http.StatusOK && (conditional || o.notModified > 0) {
		if o.notModified > 0 {
			o.notModified--
		}
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(o.status)
	_, _ = io.WriteString(w, o.body)
}

func (o *origin) set(f func(*origin)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	f(o)
}

func (o *origin) requests() []http.Header {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]http.Header(nil), o.got...)
}

func (o *origin) last() http.Header {
	got := o.requests()
	if len(got) == 0 {
		return http.Header{}
	}
	return got[len(got)-1]
}

// read is one cacheable read through c, decoded into the account it names.
func read(t *testing.T, c *Conn, req *Request) (account, *Response, error) {
	t.Helper()
	return readOn(t.Context(), c, req)
}

func readOn(ctx context.Context, c *Conn, req *Request) (account, *Response, error) {
	var out account
	if req == nil {
		req = &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}
	}
	req.Into = &out
	resp, err := c.Do(Call(ctx, req.Op), req)
	return out, resp, err
}

func serveOrigin(t *testing.T, o *origin) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(o)
	t.Cleanup(srv.Close)
	return srv
}

func TestARevalidatedReadReplaysTheValidatorTheInstanceSent(t *testing.T) {
	for _, test := range []struct {
		name, etag, modified, header, want string
	}{
		{name: "an_etag", etag: `W/"v1"`, header: "If-None-Match", want: `W/"v1"`},
		{name: "a_last_modified_alone", modified: "Wed, 25 Oct 2023 19:17:59 GMT", header: "If-Modified-Since", want: "Wed, 25 Oct 2023 19:17:59 GMT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo","ignored":"x"}`, test.etag)
			o.modified = test.modified
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
			first, _, err := read(t, c, nil)
			if err != nil {
				t.Fatalf("the first read = %v, want nil", err)
			}
			second, resp, err := read(t, c, nil)
			if err != nil {
				t.Fatalf("the revalidated read = %v, want nil", err)
			}
			if got := o.last().Get(test.header); got != test.want {
				t.Errorf("the second read sent %s %q, want %q: RFC 9110 section 13.1 replays the validator the instance sent", test.header, got, test.want)
			}
			if second != first {
				t.Errorf("the revalidated read = %+v, want %+v: a not-modified answer serves the answer held", second, first)
			}
			if resp.Status != http.StatusOK {
				t.Errorf("the revalidated read = status %d, want 200: a served answer carries the held status", resp.Status)
			}
		})
	}
}

func TestAnAnswerWithNoValidatorIsNotKept(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, "")
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
	for range 2 {
		if _, _, err := read(t, c, nil); err != nil {
			t.Fatalf("a read = %v, want nil", err)
		}
	}
	if got := o.last().Get("If-None-Match") + o.last().Get("If-Modified-Since"); got != "" {
		t.Errorf("the second read carried validator %q, want none: the instance sent none to replay", got)
	}
	if got := c.BudgetState().ReadCache.Bytes; got != 0 {
		t.Errorf("ReadCache.Bytes = %d, want 0: nothing revalidatable was answered", got)
	}
}

func TestAnErrorIsNeverKeptAndForgetsTheHeldAnswer(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
	if _, _, err := read(t, c, nil); err != nil {
		t.Fatalf("Setup: the first read = %v", err)
	}
	o.set(func(o *origin) { o.status = http.StatusNotFound })
	if _, _, err := read(t, c, nil); err == nil {
		t.Fatal("a read the instance answered 404 = nil error, want the refusal: an error is never served from the cache")
	}
	o.set(func(o *origin) { o.status = http.StatusOK })
	if _, _, err := read(t, c, nil); err != nil {
		t.Fatalf("the read after the refusal = %v, want nil", err)
	}
	if got := o.last().Get("If-None-Match"); got != "" {
		t.Errorf("the read after a 404 carried If-None-Match %q, want none: the refusal forgets the held answer", got)
	}
}

// A 304 to a REST GET that carried no validator is a response rule, whether or not
// the read may be stored: the read is sent once more without one, and an answer
// the cache may not hold is not kept.
func TestANotModifiedAnswerToAnUnconditionalReadIsSentOnceMore(t *testing.T) {
	for _, test := range []struct {
		ctx  func(context.Context) context.Context
		name string
		opts []forgeapi.Option
		hdrs []forgeapi.Header
		kept bool
	}{
		{name: "a_cacheable_read", kept: true},
		{name: "a_read_forbidding_storage", hdrs: []forgeapi.Header{{Name: "Cache-Control", Value: "no-store"}}},
		{name: "the_cache_switched_off", opts: []forgeapi.Option{forgeapi.WithReadCache(0)}},
		{name: "a_read_inside_a_write", ctx: func(ctx context.Context) context.Context { return CallWrite(ctx, "MergePR") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			o.notModified = 1
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, test.opts...)
			ctx := Call(t.Context(), "Whoami")
			if test.ctx != nil {
				ctx = test.ctx(t.Context())
			}
			var got account
			_, err := c.Do(ctx, &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user", Headers: test.hdrs, Into: &got})
			if err != nil {
				t.Fatalf("a read answered 304 then 200 = %v, want nil", err)
			}
			if got.Login != "octo" {
				t.Errorf("the read = %+v, want the body the resend answered", got)
			}
			if n := len(o.requests()); n != 2 {
				t.Errorf("the instance saw %d request(s), want 2: a 304 to a read with no validator is resent once without one", n)
			}
			if held := c.BudgetState().ReadCache.Bytes; test.kept != (held > 0) {
				t.Errorf("the cache holds %d bytes after the resend, want kept=%v", held, test.kept)
			}
		})
	}
	t.Run("a_write_answered_304_is_sent_once", func(t *testing.T) {
		o := newOrigin(`{"login":"octo"}`, "")
		o.notModified = 1
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
		var got account
		_, err := c.Do(CallWrite(t.Context(), "UpdateIssue"),
			&Request{Op: "UpdateIssue", Method: http.MethodPatch, Path: "/user", Body: map[string]string{"title": "t"}, Into: &got})
		var fe *forgeapi.Error
		if !asError(err, &fe) || fe.Status != http.StatusNotModified {
			t.Errorf("a write answered 304 = %v, want the unexpected-status refusal carrying 304", err)
		}
		if n := len(o.requests()); n != 1 {
			t.Errorf("the instance saw %d request(s), want 1: a write is never sent again", n)
		}
	})
	t.Run("a_304_listed_as_an_answer_is_still_refused", func(t *testing.T) {
		o := newOrigin(`{"login":"octo"}`, "")
		o.notModified = 5
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
		_, err := c.Do(Call(t.Context(), "Whoami"),
			&Request{Op: "Whoami", Method: http.MethodGet, Path: "/user", Answers: []int{http.StatusNotModified}})
		var fe *forgeapi.Error
		if !asError(err, &fe) || fe.Status != http.StatusNotModified {
			t.Errorf("a read answered 304 twice, 304 among its answers = %v, want the unexpected-status refusal carrying 304", err)
		}
		if n := len(o.requests()); n != 2 {
			t.Errorf("the instance saw %d request(s), want exactly 2", n)
		}
	})
	t.Run("a_refused_304_quotes_its_body", func(t *testing.T) {
		o := newOrigin(`{"login":"octo"}`, "")
		o.notModified = 1
		withBody := answerRewrite(func(resp *http.Response) {
			if resp.StatusCode == http.StatusNotModified {
				resp.Body = io.NopCloser(strings.NewReader(`{"message":"held back"}`))
			}
		})
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, forgeapi.WithAttemptObserver(withBody))
		_, err := c.Do(CallWrite(t.Context(), "UpdateIssue"),
			&Request{Op: "UpdateIssue", Method: http.MethodPatch, Path: "/user", Body: map[string]string{"title": "t"}})
		var fe *forgeapi.Error
		if !asError(err, &fe) || !strings.Contains(fe.Message, "held back") {
			t.Errorf("a write answered 304 with a body = %v, want the refusal quoting that body", err)
		}
	})
	t.Run("a_write_whose_304_fails_to_read", func(t *testing.T) {
		var sent atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			sent.Add(1)
			w.Header().Set("X-Remaining", "17")
			w.WriteHeader(http.StatusNotModified)
		}))
		defer srv.Close()
		unreadable := answerRewrite(func(resp *http.Response) {
			if resp.StatusCode == http.StatusNotModified {
				resp.Body = io.NopCloser(iotest.ErrReader(errors.New("connection reset")))
			}
		})
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithAttemptObserver(unreadable))
		_, err := c.Do(CallWrite(t.Context(), "UpdateIssue"),
			&Request{Op: "UpdateIssue", Method: http.MethodPatch, Path: "/user", Body: map[string]string{"title": "t"}})
		var fe *forgeapi.Error
		if !asError(err, &fe) || fe.Status != 0 || fe.Kind != forgeapi.KindTransient {
			t.Errorf("a write whose 304 body fails to read = %v, want the transport failure, not a refusal carrying 304", err)
		}
		if n := sent.Load(); n != 1 {
			t.Errorf("the instance saw %d request(s), want 1: a write is never sent again", n)
		}
		if got := c.BudgetState().Remaining; got != 17 {
			t.Errorf("Remaining = %d, want 17: the unreadable 304's rate headers still update the budget", got)
		}
	})
	t.Run("the_resend_answers_304_again", func(t *testing.T) {
		o := newOrigin(`{"login":"octo"}`, "")
		o.notModified = 5
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
		_, _, err := read(t, c, nil)
		var fe *forgeapi.Error
		if !asError(err, &fe) || fe.Status != http.StatusNotModified {
			t.Errorf("a read answered 304 twice = %v, want the unexpected-status refusal carrying 304", err)
		}
		if n := len(o.requests()); n != 2 {
			t.Errorf("the instance saw %d request(s), want exactly 2", n)
		}
	})
}

func TestAReadThatIsNotCacheableNeverCarriesAValidator(t *testing.T) {
	for _, test := range []struct {
		name string
		ctx  func(context.Context) context.Context
		req  func(into any) *Request
	}{
		{name: "a_post", req: func(into any) *Request {
			return &Request{Op: "CreateIssue", Method: http.MethodPost, Path: "/user", Body: map[string]string{}, Into: into}
		}},
		{name: "a_document", req: func(into any) *Request {
			return &Request{Op: "ListPRs", Method: http.MethodPost, Path: "/user", Body: map[string]string{}, Document: true, Into: into}
		}},
		{name: "a_read_inside_a_write", ctx: func(ctx context.Context) context.Context { return CallWrite(ctx, "MergePR") }, req: func(into any) *Request {
			return &Request{Op: "MergePR", Method: http.MethodGet, Path: "/user", Into: into}
		}},
		{name: "a_read_naming_no_shape", req: func(any) *Request {
			return &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
			ctx := t.Context()
			if test.ctx != nil {
				ctx = test.ctx(ctx)
			}
			for range 2 {
				var out account
				if _, err := c.Do(ctx, test.req(&out)); err != nil {
					t.Fatalf("a request = %v, want nil", err)
				}
			}
			if got := o.last().Get("If-None-Match"); got != "" {
				t.Errorf("the second request carried If-None-Match %q, want none: only a REST GET outside a write is revalidated", got)
			}
		})
	}
}

// rotating is a credential whose token the test moves.
type rotating struct{ token atomic.Value }

func (r *rotating) Token(context.Context) (string, error) { return r.token.Load().(string), nil }
func (*rotating) Kind() forgeapi.CredKind                 { return forgeapi.CredKindStaticPAT }
func (*rotating) State() forgeapi.CredState               { return forgeapi.CredValid }

func TestTheKeyFoldsTheCredentialAndEveryHeaderThatVariesTheAnswer(t *testing.T) {
	t.Run("a_rotated_token", func(t *testing.T) {
		o := newOrigin(`{"login":"octo"}`, `"v1"`)
		cred := &rotating{}
		cred.token.Store("first-token")
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, forgeapi.WithCredentialSource(cred))
		if _, _, err := read(t, c, nil); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		cred.token.Store("second-token")
		if _, _, err := read(t, c, nil); err != nil {
			t.Fatalf("the read under the new token = %v", err)
		}
		if got := o.last().Get("If-None-Match"); got != "" {
			t.Errorf("the read under another token carried If-None-Match %q, want none: an answer is never revalidated under another credential", got)
		}
	})
	t.Run("two_connections", func(t *testing.T) {
		o := newOrigin(`{"login":"octo"}`, `"v1"`)
		srv := serveOrigin(t, o)
		first := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL})
		second := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL})
		if _, _, err := read(t, first, nil); err != nil {
			t.Fatalf("Setup: %v", err)
		}
		if _, _, err := read(t, second, nil); err != nil {
			t.Fatalf("the second connection's read = %v", err)
		}
		if got := o.last().Get("If-None-Match"); got != "" {
			t.Errorf("another connection's read carried If-None-Match %q, want none: two connections never share entries", got)
		}
	})
	for _, header := range []string{"Accept", "X-GitHub-Api-Version"} {
		t.Run("another_"+strings.ToLower(header), func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
			req := func(value string) *Request {
				return &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user", Headers: []forgeapi.Header{{Name: header, Value: value}}}
			}
			if _, _, err := read(t, c, req("one")); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			if _, _, err := read(t, c, req("two")); err != nil {
				t.Fatalf("the read with another %s = %v", header, err)
			}
			if got := o.last().Get("If-None-Match"); got != "" {
				t.Errorf("a read under another %s carried If-None-Match %q, want none: the header varies the representation", header, got)
			}
			if _, _, err := read(t, c, req("two")); err != nil {
				t.Fatalf("the repeated read = %v", err)
			}
			if got := o.last().Get("If-None-Match"); got != `"v1"` {
				t.Errorf("a repeat under the same %s carried If-None-Match %q, want the held validator", header, got)
			}
		})
	}
}

func TestTheCacheHoldsItsBound(t *testing.T) {
	t.Run("eviction_keeps_the_bound", func(t *testing.T) {
		o := newOrigin(`{"login":"`+strings.Repeat("a", 600)+`"}`, `"v1"`)
		const bound = 8 << 10
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, forgeapi.WithReadCache(bound))
		for i := range 40 {
			if _, _, err := read(t, c, &Request{Op: "Whoami", Method: http.MethodGet, Path: fmt.Sprintf("/user/%d", i)}); err != nil {
				t.Fatalf("read %d = %v", i, err)
			}
			if got := c.BudgetState().ReadCache.Bytes; got > bound {
				t.Fatalf("after read %d, ReadCache.Bytes = %d, want at most %d", i, got, bound)
			}
		}
		if _, _, err := read(t, c, &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user/0"}); err != nil {
			t.Fatal(err)
		}
		if got := o.last().Get("If-None-Match"); got != "" {
			t.Errorf("the least recently used entry was revalidated (%q), want it evicted", got)
		}
		if _, _, err := read(t, c, &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user/39"}); err != nil {
			t.Fatal(err)
		}
		if got := o.last().Get("If-None-Match"); got != `"v1"` {
			t.Errorf("the most recent entry carried If-None-Match %q, want it held", got)
		}
	})
	t.Run("a_read_entry_outlives_one_left_unread", func(t *testing.T) {
		o := newOrigin(`{"login":"`+strings.Repeat("a", 600)+`"}`, `"v1"`)
		const bound = 8 << 10
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, forgeapi.WithReadCache(bound))
		user := func(i int) *Request {
			return &Request{Op: "Whoami", Method: http.MethodGet, Path: fmt.Sprintf("/user/%d", i)}
		}
		if _, _, err := read(t, c, user(0)); err != nil {
			t.Fatal(err)
		}
		held := int(bound / c.BudgetState().ReadCache.Bytes)
		for i := 1; i < held; i++ {
			if _, _, err := read(t, c, user(i)); err != nil {
				t.Fatalf("read %d = %v", i, err)
			}
		}
		if _, _, err := read(t, c, user(0)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := read(t, c, user(held)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := read(t, c, user(0)); err != nil {
			t.Fatal(err)
		}
		if got := o.last().Get("If-None-Match"); got != `"v1"` {
			t.Errorf("the entry read again before the eviction carried If-None-Match %q, want it held", got)
		}
		if _, _, err := read(t, c, user(1)); err != nil {
			t.Fatal(err)
		}
		if got := o.last().Get("If-None-Match"); got != "" {
			t.Errorf("the entry left unread longest was revalidated (%q), want it evicted", got)
		}
	})
	t.Run("an_oversized_entry_is_not_kept", func(t *testing.T) {
		o := newOrigin(`{"login":"`+strings.Repeat("a", 4<<10)+`"}`, `"v1"`)
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, forgeapi.WithReadCache(8<<10))
		for range 2 {
			if _, _, err := read(t, c, nil); err != nil {
				t.Fatal(err)
			}
		}
		if got := o.last().Get("If-None-Match"); got != "" {
			t.Errorf("an entry over a quarter of the bound was revalidated (%q), want it not kept", got)
		}
	})
	t.Run("a_zero_bound_switches_the_cache_off", func(t *testing.T) {
		o := newOrigin(`{"login":"octo"}`, `"v1"`)
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, forgeapi.WithReadCache(0))
		for range 2 {
			if _, _, err := read(t, c, nil); err != nil {
				t.Fatal(err)
			}
		}
		if got := o.last().Get("If-None-Match"); got != "" {
			t.Errorf("WithReadCache(0) sent If-None-Match %q, want every read unconditional", got)
		}
		if got := c.BudgetState().ReadCache; got != (forgeapi.ReadCacheState{}) {
			t.Errorf("WithReadCache(0) reports %+v, want the zero state", got)
		}
	})
	t.Run("a_negative_bound_is_refused", func(t *testing.T) {
		_, err := Open(&forgeapi.Connection{WebBaseURL: "https://forge.example.com"}, settingsFor(
			forgeapi.WithCredentialSource(stubCredential{}), forgeapi.WithReadCache(-1),
		), testOptions())
		var fe *forgeapi.Error
		if !asError(err, &fe) || fe.Code != forgeapi.CodeBudgetInvalid {
			t.Errorf("Open with WithReadCache(-1) = %v, want code %q", err, forgeapi.CodeBudgetInvalid)
		}
	})
	t.Run("close_empties_the_cache", func(t *testing.T) {
		o := newOrigin(`{"login":"octo"}`, `"v1"`)
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
		if _, _, err := read(t, c, nil); err != nil {
			t.Fatal(err)
		}
		c.Close()
		if got := c.BudgetState().ReadCache.Bytes; got != 0 {
			t.Errorf("after Close, ReadCache.Bytes = %d, want 0", got)
		}
	})
}

func TestConcurrentReadsKeepTheCacheConsistent(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL},
		forgeapi.WithReadCache(16<<10), forgeapi.WithReadConcurrency(8))
	var wg sync.WaitGroup
	for worker := range 16 {
		wg.Go(func() {
			for i := range 20 {
				path := "/user"
				if worker%2 == 1 {
					path = fmt.Sprintf("/user/%d", (worker*20+i)%37)
				}
				got, _, err := read(t, c, &Request{Op: "Whoami", Method: http.MethodGet, Path: path})
				if err != nil {
					t.Errorf("a concurrent read = %v", err)
					return
				}
				if got.Login != "octo" {
					t.Errorf("a concurrent read = %+v, want the body the instance holds", got)
					return
				}
			}
		})
	}
	wg.Wait()
	if got := c.BudgetState().ReadCache.Bytes; got > 16<<10 {
		t.Errorf("ReadCache.Bytes = %d, want at most the bound", got)
	}
}

func TestANotModifiedAnswerIsPricedByWhatTheProductBills(t *testing.T) {
	for _, test := range []struct {
		name string
		free bool
		want int
	}{
		{name: "a_product_that_bills_none", free: true, want: 0},
		{name: "a_product_that_bills_one", free: false, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			o.header.Set("X-Remaining", "4000")
			opts := testOptions()
			opts.FreeNotModified = test.free
			c := openConnWith(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, opts)
			if _, _, err := read(t, c, nil); err != nil {
				t.Fatal(err)
			}
			o.set(func(o *origin) { o.header.Set("X-Remaining", "3999") })
			if _, _, err := read(t, c, nil); err != nil {
				t.Fatal(err)
			}
			state := c.BudgetState()
			if state.LastCost != test.want {
				t.Errorf("after a not-modified answer, LastCost = %d, want %d", state.LastCost, test.want)
			}
			if state.Remaining != 3999 {
				t.Errorf("after a not-modified answer, Remaining = %d, want 3999: the 304's own rate headers update the budget", state.Remaining)
			}
			want := forgeapi.ReadCacheState{Hits: 1, Misses: 1, Saved: 0, Bytes: state.ReadCache.Bytes}
			if test.free {
				want.Saved = 1
			}
			if state.ReadCache != want || want.Bytes <= 0 {
				t.Errorf("ReadCache = %+v, want %+v with Bytes above zero", state.ReadCache, want)
			}
		})
	}
}

// A served answer carries the 304's own headers and the held fields it omits, each
// held field updated by every 304 that carries it (RFC 9111 section 4.3.4); a field
// the entry does not hold is never served from it, and a required held field the
// 304 omits sends the read once more.
func TestANotModifiedAnswerUpdatesTheHeldHeaders(t *testing.T) {
	const next = `<https://forge.example/user?page=2>; rel="next"`
	type step struct {
		first, then, later http.Header
		want               map[string]string
		name               string
		requests           int
	}
	tests := []step{
		{
			name: "a_link_the_304_omits", first: http.Header{"Link": {next}}, then: http.Header{},
			want: map[string]string{"Link": next}, requests: 2,
		},
		{
			name: "a_link_one_304_replaced_and_the_next_omits", first: http.Header{"Link": {next}},
			then: http.Header{"Link": {`<https://forge.example/user?page=3>; rel="next"`}}, later: http.Header{},
			want: map[string]string{"Link": `<https://forge.example/user?page=3>; rel="next"`}, requests: 3,
		},
		{
			name: "a_total_the_304_repeats", first: http.Header{"X-Total-Count": {"7"}}, then: http.Header{"X-Total-Count": {"8"}},
			want: map[string]string{"X-Total-Count": "8"}, requests: 2,
		},
		{
			name: "scopes_the_304_omits", first: http.Header{"X-Oauth-Scopes": {"repo"}}, then: http.Header{},
			want: map[string]string{"X-Oauth-Scopes": ""}, requests: 2,
		},
		{
			name: "a_cookie_the_304_omits", first: http.Header{"Set-Cookie": {"session=1"}}, then: http.Header{},
			want: map[string]string{"Set-Cookie": ""}, requests: 2,
		},
		{
			name: "the_rate_headers_of_the_304", first: http.Header{"X-Remaining": {"4000"}}, then: http.Header{"X-Remaining": {"3999"}},
			want: map[string]string{"X-Remaining": "3999"}, requests: 2,
		},
		{
			// GitLab's last page sends an empty X-Next-Page, which holds nothing to require.
			name: "an_empty_next_page_the_304_omits", first: http.Header{"X-Next-Page": {""}}, then: http.Header{},
			want: map[string]string{"X-Next-Page": ""}, requests: 2,
		},
	}
	for _, name := range []string{"X-Next-Page", "X-Total-Count", "X-Gitlab-Meta", "X-Gitea-Version", "X-Forgejo-Version"} {
		tests = append(tests, step{
			name: strings.ToLower(name) + "_the_304_omits", first: http.Header{name: {"7"}}, then: http.Header{},
			want: map[string]string{name: ""}, requests: 3,
		})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			o.header = test.first.Clone()
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
			if _, _, err := read(t, c, nil); err != nil {
				t.Fatal(err)
			}
			o.set(func(o *origin) { o.header = test.then.Clone() })
			got, resp, err := read(t, c, nil)
			if test.later != nil && err == nil {
				o.set(func(o *origin) { o.header = test.later.Clone() })
				got, resp, err = read(t, c, nil)
			}
			if err != nil || got.Login != "octo" {
				t.Fatalf("the second read = %+v, %v, want the account", got, err)
			}
			for name, want := range test.want {
				if v := resp.Header.Get(name); v != want {
					t.Errorf("the served answer's %s = %q, want %q", name, v, want)
				}
			}
			if n := len(o.requests()); n != test.requests {
				t.Errorf("the instance saw %d request(s), want %d", n, test.requests)
			}
		})
	}
}

// RFC 9110 section 8.8.1: a validator names a representation of one resource, so a
// 304 from wherever a hop leads cannot vouch for an answer another address gave.
func TestARedirectedReadIsNeverServedFromTheCache(t *testing.T) {
	for _, test := range []struct {
		name  string
		first string
	}{
		{name: "a_hop_that_changes_destination", first: "/api/v1/a"},
		{name: "a_resource_that_starts_redirecting", first: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			alpha, beta := newOrigin(`{"login":"alpha"}`, `"shared"`), newOrigin(`{"login":"beta"}`, `"shared"`)
			var hop atomic.Value
			hop.Store(test.first)
			mux := http.NewServeMux()
			mux.Handle("/api/v1/a", alpha)
			mux.Handle("/api/v1/b", beta)
			mux.HandleFunc("/api/v1/old", func(w http.ResponseWriter, r *http.Request) {
				if to, _ := hop.Load().(string); to != "" {
					http.Redirect(w, r, to, http.StatusMovedPermanently)
					return
				}
				alpha.ServeHTTP(w, r)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL})
			req := func() *Request { return &Request{Op: "ListIssues", Method: http.MethodGet, Path: "/old"} }
			if got, _, err := read(t, c, req()); err != nil || got.Login != "alpha" {
				t.Fatalf("Setup: the first read = %+v, %v, want alpha", got, err)
			}
			hop.Store("/api/v1/b")
			for i := range 2 {
				got, resp, err := read(t, c, req())
				if err != nil {
					t.Fatal(err)
				}
				if got.Login != "beta" || !resp.Redirected {
					t.Errorf("read %d through the new hop = %+v, Redirected %v, want beta and the hop witnessed", i+2, got, resp.Redirected)
				}
			}
			for _, h := range beta.requests() {
				if v := h.Get("If-None-Match"); v != "" {
					t.Errorf("a request the hop led to carried If-None-Match %q, want none", v)
				}
			}
			if got := c.BudgetState().ReadCache.Bytes; got != 0 {
				t.Errorf("the cache holds %d bytes after reads that took a hop, want nothing kept", got)
			}
		})
	}
}

// An observer that rewrites a request reaches the wire with a request the key does
// not describe, so neither the credential nor any varying header can be crossed.
func TestAnObserverThatChangesARequestBypassesTheCache(t *testing.T) {
	echo := func(header string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"shared"`)
			w.Header().Set("Content-Type", "application/json")
			if r.Header.Get("If-None-Match") == `"shared"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = io.WriteString(w, `{"login":"`+r.Header.Get(header)+`"}`)
		}
	}
	for _, header := range []string{"Authorization", "Accept"} {
		for _, test := range observerPositions {
			t.Run(test.name+"_"+strings.ToLower(header), func(t *testing.T) {
				var value atomic.Value
				value.Store("one")
				rewrite := func(next http.RoundTripper) http.RoundTripper {
					return roundTripFunc(func(r *http.Request) (*http.Response, error) {
						r = r.Clone(r.Context())
						r.Header.Set(header, value.Load().(string))
						return next.RoundTrip(r)
					})
				}
				srv := httptest.NewServer(echo(header))
				defer srv.Close()
				c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, test.option(rewrite))
				if got, _, err := read(t, c, nil); err != nil || got.Login != "one" {
					t.Fatalf("Setup: the first read = %+v, %v, want one", got, err)
				}
				value.Store("two")
				got, _, err := read(t, c, nil)
				if err != nil {
					t.Fatal(err)
				}
				if got.Login != "two" {
					t.Errorf("the read sent with %s two = %+v, want the instance's answer to two, not the one kept for one", header, got)
				}
			})
		}
	}
	t.Run("an_observer_that_only_reads", func(t *testing.T) {
		o := newOrigin(`{"login":"octo"}`, `"v1"`)
		pass := func(next http.RoundTripper) http.RoundTripper { return next }
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL},
			forgeapi.WithRequestObserver(pass), forgeapi.WithAttemptObserver(pass))
		for range 2 {
			if _, _, err := read(t, c, nil); err != nil {
				t.Fatal(err)
			}
		}
		if got := c.BudgetState().ReadCache.Hits; got != 1 {
			t.Errorf("ReadCache.Hits behind observers that change nothing = %d, want 1", got)
		}
	})
	for _, test := range observerPositions {
		t.Run(test.name+"_host", func(t *testing.T) {
			var host atomic.Value
			host.Store("first.example")
			rewrite := func(next http.RoundTripper) http.RoundTripper {
				return roundTripFunc(func(r *http.Request) (*http.Response, error) {
					r = r.Clone(r.Context())
					r.Host = host.Load().(string)
					return next.RoundTrip(r)
				})
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"shared"`)
				if r.Header.Get("If-None-Match") == `"shared"` {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				_, _ = io.WriteString(w, `{"login":"`+r.Host+`"}`)
			}))
			defer srv.Close()
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, test.option(rewrite))
			if got, _, err := read(t, c, nil); err != nil || got.Login != "first.example" {
				t.Fatalf("Setup: the first read = %+v, %v, want first.example", got, err)
			}
			host.Store("second.example")
			got, _, err := read(t, c, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.Login != "second.example" {
				t.Errorf("the read sent with Host second.example = %+v, want that host's answer, not the one kept for first.example", got)
			}
		})
	}
}

// A request an observer changed leaves without its validators however their lines
// and names are written: an empty first line hides none (RFC 9110 section 5.6.1),
// and net/http sends a field under any name.
func TestAChangedRequestLeavesWithoutAnyValidatorLine(t *testing.T) {
	for _, test := range []struct {
		change func(h http.Header, lines []string)
		name   string
	}{
		{name: "an_empty_first_line", change: func(h http.Header, lines []string) { h["If-None-Match"] = append([]string{""}, lines...) }},
		{name: "a_lowercase_name", change: func(h http.Header, lines []string) {
			h.Del("If-None-Match")
			h["if-none-match"] = lines
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			change := func(next http.RoundTripper) http.RoundTripper {
				return roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if lines := r.Header.Values("If-None-Match"); len(lines) > 0 {
						r = r.Clone(r.Context())
						test.change(r.Header, lines)
					}
					return next.RoundTrip(r)
				})
			}
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, forgeapi.WithAttemptObserver(change))
			for i := range 2 {
				if got, _, err := read(t, c, nil); err != nil || got.Login != "octo" {
					t.Fatalf("read %d = %+v, %v, want the instance's answer", i, got, err)
				}
			}
			got := o.requests()
			if len(got) != 2 {
				t.Errorf("the instance saw %d request(s), want 2: a changed read is one unconditional attempt", len(got))
			}
			for i, header := range got {
				if lines := header.Values("If-None-Match"); slices.ContainsFunc(lines, func(line string) bool { return line != "" }) {
					t.Errorf("wire request %d carried If-None-Match %q, want no validator on a changed request", i, lines)
				}
			}
		})
	}
}

// An answer is for the request it reports: a substitute wire that rewrites the
// keyed request, here its credential, is answered for the request it sent, whether
// its answers report that request or name none.
func TestAWireThatRewritesARequestBypassesTheCache(t *testing.T) {
	wire := &http.Transport{}
	t.Cleanup(wire.CloseIdleConnections)
	for _, test := range []struct {
		report func(*http.Response)
		name   string
	}{
		{name: "an_answer_reporting_its_request", report: func(*http.Response) {}},
		{name: "an_answer_naming_no_request", report: func(resp *http.Response) { resp.Request = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"shared"`)
				w.Header().Set("Content-Type", "application/json")
				if r.Header.Get("If-None-Match") == `"shared"` {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				_, _ = io.WriteString(w, `{"login":"`+r.Header.Get("Authorization")+`"}`)
			}))
			t.Cleanup(srv.Close)
			var token atomic.Value
			rewrite := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				r = r.Clone(r.Context())
				r.Header.Set("Authorization", token.Load().(string))
				resp, err := wire.RoundTrip(r)
				if err == nil {
					test.report(resp)
				}
				return resp, err
			})
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithWireTransport(rewrite))
			for _, value := range []string{"one", "two"} {
				token.Store(value)
				if got, _, err := read(t, c, nil); err != nil || got.Login != value {
					t.Errorf("the read the wire sent as %q = %+v, %v, want its own answer", value, got, err)
				}
			}
			if got := c.BudgetState().ReadCache; got.Hits != 0 || got.Bytes != 0 {
				t.Errorf("ReadCache = %+v, want no hit and nothing held for a request the wire rewrote", got)
			}
		})
	}
}

// observerPositions is the two places a consumer's hook sits above the wire.
var observerPositions = []struct {
	option func(func(http.RoundTripper) http.RoundTripper) forgeapi.Option
	name   string
}{
	{name: "request_observer", option: forgeapi.WithRequestObserver},
	{name: "attempt_observer", option: forgeapi.WithAttemptObserver},
}

// A held answer is served only on the wire's own 304, and kept only from the wire's
// own 200: an observer that changes what the instance answered is bypassed.
func TestAnObserverThatChangesAnAnswerBypassesTheCache(t *testing.T) {
	for _, test := range observerPositions {
		t.Run(test.name+"_a_200_made_a_304", func(t *testing.T) {
			o := newOrigin(`{"login":"old"}`, `"old"`)
			var forge atomic.Bool
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, test.option(answerRewrite(func(resp *http.Response) {
				if forge.Load() {
					resp.StatusCode = http.StatusNotModified
				}
			})))
			if _, _, err := read(t, c, nil); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			o.set(func(o *origin) { o.body, o.etag = `{"login":"new"}`, `"new"` })
			forge.Store(true)
			got, _, err := read(t, c, nil)
			if err == nil && got.Login == "old" {
				t.Errorf("a 200 an observer reported as 304 served %+v, want the held answer never served without the instance's 304", got)
			}
			if n := len(o.requests()); n != 3 {
				t.Errorf("the instance saw %d request(s), want 3: a 304 the wire did not give is sent once more, never refused at once", n)
			}
		})
		t.Run(test.name+"_a_404_made_a_200", func(t *testing.T) {
			o := newOrigin(`{"login":"gone"}`, `"v1"`)
			o.status = http.StatusNotFound
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, test.option(answerRewrite(func(resp *http.Response) {
				resp.StatusCode = http.StatusOK
			})))
			if _, _, err := read(t, c, nil); err != nil {
				t.Fatalf("the read the observer reported as 200 = %v, want the observer's answer", err)
			}
			if got := c.BudgetState().ReadCache.Bytes; got != 0 {
				t.Errorf("a 404 an observer reported as 200 left %d bytes held, want nothing kept", got)
			}
		})
		t.Run(test.name+"_a_304_given_a_body", func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			var forge atomic.Bool
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, test.option(answerRewrite(func(resp *http.Response) {
				if forge.Load() && resp.StatusCode == http.StatusNotModified {
					resp.Body = io.NopCloser(strings.NewReader(`{"login":"forged"}`))
				}
			})))
			if _, _, err := read(t, c, nil); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			forge.Store(true)
			got, resp, err := read(t, c, nil)
			if err != nil || resp.Revalidated || got.Login != "octo" {
				t.Errorf("a 304 an observer gave a body = %+v, Revalidated %v, %v, want the held answer never served for an answer the wire did not give, and the resend's own answer", got, resp != nil && resp.Revalidated, err)
			}
			if n := len(o.requests()); n != 3 {
				t.Errorf("the instance saw %d request(s), want 3: that 304 is sent once more without a validator", n)
			}
		})
		t.Run(test.name+"_a_304_given_a_header", func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			var forge atomic.Bool
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, test.option(answerRewrite(func(resp *http.Response) {
				if forge.Load() && resp.StatusCode == http.StatusNotModified {
					resp.Header.Set("X-Forged", "1")
				}
			})))
			if _, _, err := read(t, c, nil); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			forge.Store(true)
			got, resp, err := read(t, c, nil)
			if err != nil || resp.Revalidated || got.Login != "octo" {
				t.Errorf("a 304 an observer gave a header = %+v, Revalidated %v, %v, want the resend's own answer", got, resp != nil && resp.Revalidated, err)
			}
			if n := len(o.requests()); n != 3 {
				t.Errorf("the instance saw %d request(s), want 3: that 304 is sent once more without a validator", n)
			}
		})
		t.Run(test.name+"_another_body", func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			var swap atomic.Bool
			swap.Store(true)
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, test.option(answerRewrite(func(resp *http.Response) {
				if swap.Load() {
					resp.Body = io.NopCloser(strings.NewReader(`{"login":"forged"}`))
				}
			})))
			if got, _, err := read(t, c, nil); err != nil || got.Login != "forged" {
				t.Fatalf("Setup: the read = %+v, %v, want the observer's body", got, err)
			}
			swap.Store(false)
			got, _, err := read(t, c, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.Login != "octo" || o.last().Get("If-None-Match") != "" {
				t.Errorf("the read after a swapped body = %+v with If-None-Match %q, want the instance's answer read unconditionally", got, o.last().Get("If-None-Match"))
			}
		})
	}
}

// answerRewrite is an observer that changes the answer its inner transport gave.
func answerRewrite(change func(*http.Response)) func(http.RoundTripper) http.RoundTripper {
	return func(next http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			resp, err := next.RoundTrip(r)
			if err == nil {
				change(resp)
			}
			return resp, err
		})
	}
}

func TestAResentNotModifiedAnswerStillReportsItsBudget(t *testing.T) {
	for _, test := range []struct {
		name       string
		opts       []forgeapi.Option
		resendFail bool
	}{
		{name: "a_cacheable_read"},
		{name: "an_uncached_read_whose_resend_fails", opts: []forgeapi.Option{forgeapi.WithReadCache(0)}, resendFail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var sent atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if sent.Add(1) == 1 {
					w.Header().Set("X-Remaining", "41")
					w.WriteHeader(http.StatusNotModified)
					return
				}
				if test.resendFail {
					panic(http.ErrAbortHandler)
				}
				w.Header().Set("ETag", `"v1"`)
				_, _ = io.WriteString(w, `{"login":"octo"}`)
			}))
			defer srv.Close()
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, test.opts...)
			if _, _, err := read(t, c, nil); (err != nil) != test.resendFail {
				t.Fatalf("the read = %v, want failed=%v", err, test.resendFail)
			}
			if got := sent.Load(); got != 2 && (!test.resendFail || got < 2) {
				t.Errorf("the instance saw %d request(s), want 2, or more where the failed resend is retried", got)
			}
			if got := c.BudgetState().Remaining; got != 41 {
				t.Errorf("Remaining = %d, want 41: the discarded 304's rate headers update the budget", got)
			}
		})
	}
	t.Run("a_cacheable_read_whose_forged_304_resend_fails", func(t *testing.T) {
		var sent atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if sent.Add(1) == 1 {
				w.Header().Set("X-Remaining", "41")
				w.WriteHeader(http.StatusNotModified)
				return
			}
			panic(http.ErrAbortHandler)
		}))
		defer srv.Close()
		forged := answerRewrite(func(resp *http.Response) {
			if resp.StatusCode == http.StatusNotModified {
				resp.Header.Set("X-Remaining", "7")
			}
		})
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithAttemptObserver(forged))
		if _, _, err := read(t, c, nil); err == nil {
			t.Fatal("the read whose resend fails = nil, want the resend's failure")
		}
		if got := c.BudgetState().Remaining; got == 7 {
			t.Errorf("Remaining = %d, the value an observer wrote over the 304: a cached read's budget takes only rate headers the wire vouches for", got)
		}
	})
}

func TestAnAnswerForbiddingStorageIsNotKept(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	o.header.Set("Cache-Control", "private, no-store")
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
	for range 2 {
		if _, _, err := read(t, c, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := o.last().Get("If-None-Match"); got != "" {
		t.Errorf("a no-store answer was revalidated (%q), want it not kept (RFC 9111 section 5.2.2.5)", got)
	}
}

func TestAnOverlongValidatorIsNotReplayed(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"`+strings.Repeat("v", 2<<10)+`"`)
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
	for range 2 {
		if _, _, err := read(t, c, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := o.last().Get("If-None-Match"); got != "" {
		t.Errorf("a 2 KiB validator was replayed, want it refused")
	}
}

// An entry is charged every byte the cache holds for it, spare capacity included,
// so the bound holds the memory the entries own, not only their bodies' lengths.
func TestAnEntryIsChargedEverythingItHolds(t *testing.T) {
	const key = "page-key"
	body := make(json.RawMessage, 2, 4096)
	copy(body, "{}")
	p := &pageEntry{ETag: `"v1"`, Fields: familyFields.hold(http.Header{"Link": {"next"}}), Body: body}
	if got, model := pageCost(key, p), int64(len(key)+cap(body)+len(`"v1"`)+len("next")); got < model {
		t.Errorf("a page whose body holds %d bytes is charged %d, want at least %d", cap(body), got, model)
	}
	if got := pageCost(key, &pageEntry{Fields: [][]string{{"next"}}}) - pageCost(key, &pageEntry{Fields: [][]string{{""}}}); got != int64(len("next")) {
		t.Errorf("a held line of %d bytes adds %d to the charge of an empty one, want exactly its length", len("next"), got)
	}
	slots := &pageEntry{ETag: p.ETag, Fields: heldTable(make([]heldField, 4096)).hold(http.Header{})}
	want := int64(len(slots.Fields)) * int64(reflect.TypeFor[[]string]().Size())
	if got := pageCost(key, slots) - pageCost(key, &pageEntry{ETag: p.ETag}); got < want {
		t.Errorf("%d empty held field slots add %d to the charge, want at least the %d bytes their storage takes", len(slots.Fields), got, want)
	}
	lines := &pageEntry{ETag: p.ETag, Fields: [][]string{make([]string, 4096)}}
	want = int64(len(lines.Fields[0])) * int64(reflect.TypeFor[string]().Size())
	if got := pageCost(key, lines) - pageCost(key, &pageEntry{ETag: p.ETag, Fields: [][]string{nil}}); got < want {
		t.Errorf("a held field of %d empty lines adds %d to the charge, want at least the %d bytes their storage takes", len(lines.Fields[0]), got, want)
	}
}

// A field the cache does not keep must not stay reachable through a kept one:
// Header.Clone backs every field with one shared array, so a kept Link would hold
// a dropped cookie, or a value a 304 replaced, that the bound never charged.
func TestADroppedHeaderIsNotHeldPastTheBound(t *testing.T) {
	const bound, entries = 32 << 20, 48
	big := strings.Repeat("c", 1<<20)
	heap := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	for _, test := range []struct {
		name     string
		first    http.Header
		replaced http.Header
	}{
		{name: "a_cookie_on_the_answer", first: http.Header{"Set-Cookie": {"session=" + big}}},
		{name: "a_value_a_not_modified_answer_replaced", first: http.Header{"X-Big": {big}}, replaced: http.Header{"X-Big": {"small"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{}`, `"v1"`)
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, forgeapi.WithReadCache(bound))
			header := func(h http.Header) {
				o.set(func(o *origin) {
					o.header = h.Clone()
					o.header.Set("Link", `<https://forge.example/user?page=2>; rel="next"`)
				})
			}
			before := heap()
			for i := range entries {
				req := func() *Request {
					return &Request{Op: "Whoami", Method: http.MethodGet, Path: fmt.Sprintf("/user/%d", i)}
				}
				header(test.first)
				if _, _, err := read(t, c, req()); err != nil {
					t.Fatalf("read %d = %v", i, err)
				}
				if test.replaced != nil {
					header(test.replaced)
					if _, _, err := read(t, c, req()); err != nil {
						t.Fatalf("revalidation %d = %v", i, err)
					}
				}
			}
			held := int64(heap()) - int64(before)
			charged := c.BudgetState().ReadCache.Bytes
			if limit := charged + 4<<20; held > limit {
				t.Errorf("%d entries charged %d bytes hold %d on the heap, want at most %d", entries, charged, held, limit)
			}
			runtime.KeepAlive(c)
		})
	}
}

// A substitute wire may answer header names and values that are substrings of a
// larger string, so whatever an entry keeps must own its bytes, or the bound
// charges a few bytes of a string that holds a megabyte.
func TestAKeptEntryOwnsItsBytes(t *testing.T) {
	const bound, entries = 32 << 20, 48
	heap := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	for _, test := range []struct {
		answer func(h http.Header, big string)
		name   string
	}{
		{name: "a_header_value", answer: func(h http.Header, big string) { h.Set("X-Small", big[:8]) }},
		{name: "a_header_name", answer: func(h http.Header, big string) { h[("X-Small" + big)[:7]] = []string{"small"} }},
		{name: "a_held_field", answer: func(h http.Header, big string) { h.Set("Link", big[:8]) }},
		{name: "the_etag", answer: func(h http.Header, big string) { h.Set("ETag", (`"v1"` + big)[:4]) }},
		{name: "the_last_modified", answer: func(h http.Header, big string) {
			const date = "Mon, 05 Oct 2026 10:00:00 GMT"
			h.Del("ETag")
			h.Set("Last-Modified", (date + big)[:len(date)])
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				resp := jsonAnswer(r, http.StatusOK, `{}`)
				resp.Header.Set("ETag", `"v1"`)
				test.answer(resp.Header, strings.Repeat("c", 1<<20))
				return resp, nil
			})
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"},
				forgeapi.WithWireTransport(wire), forgeapi.WithReadCache(bound))
			before := heap()
			for i := range entries {
				if _, _, err := read(t, c, &Request{Op: "Whoami", Method: http.MethodGet, Path: fmt.Sprintf("/user/%d", i)}); err != nil {
					t.Fatalf("read %d = %v", i, err)
				}
			}
			held := int64(heap()) - int64(before)
			charged := c.BudgetState().ReadCache.Bytes
			if charged <= 0 {
				t.Fatalf("nothing was kept, want %d entries held", entries)
			}
			if limit := charged + 4<<20; held > limit {
				t.Errorf("%d entries charged %d bytes hold %d on the heap, want at most %d", entries, charged, held, limit)
			}
			runtime.KeepAlive(c)
		})
	}
}

// RFC 9111 section 5.2.2.5: no-store on a not-modified answer withdraws permission to
// keep the response, so the held answer serves this call and is then forgotten.
func TestANotModifiedAnswerForbiddingStorageForgetsTheHeldAnswer(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL})
	if _, _, err := read(t, c, nil); err != nil {
		t.Fatal(err)
	}
	o.set(func(o *origin) { o.header.Set("Cache-Control", "no-store") })
	got, resp, err := read(t, c, nil)
	if err != nil || got.Login != "octo" || resp.Status != http.StatusOK {
		t.Fatalf("the not-modified read = %+v, %v, want the held answer served", got, err)
	}
	if held := c.BudgetState().ReadCache.Bytes; held != 0 {
		t.Errorf("after a no-store 304 the cache holds %d bytes, want none", held)
	}
	if _, _, err := read(t, c, nil); err != nil {
		t.Fatal(err)
	}
	if got := o.last().Get("If-None-Match"); got != "" {
		t.Errorf("the read after a no-store 304 carried If-None-Match %q, want none", got)
	}
}

func TestTheRecordNamesWhatTheCacheDid(t *testing.T) {
	o := newOrigin(`{"login":"octo"}`, `"v1"`)
	var buf bytes.Buffer
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, forgeapi.WithLogger(recordingLogger(&buf)))
	for _, want := range []struct {
		cache  string
		status float64
	}{{cache: "miss", status: 200}, {cache: "hit", status: 304}} {
		buf.Reset()
		if _, _, err := read(t, c, nil); err != nil {
			t.Fatal(err)
		}
		line := onlyRecord(t, &buf)
		if line["cache"] != want.cache || line["status"] != want.status {
			t.Errorf("the record = cache %v status %v, want %s %v: the status is the one the instance answered", line["cache"], line["status"], want.cache, want.status)
		}
	}
	buf.Reset()
	if _, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
		t.Fatal(err)
	}
	if got := onlyRecord(t, &buf)["cache"]; got != "off" {
		t.Errorf("the record of a read naming no shape = cache %v, want off", got)
	}
	cut := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	defer cut.Close()
	buf.Reset()
	failing := openTestConn(t, forgeapi.Connection{WebBaseURL: cut.URL}, forgeapi.WithLogger(recordingLogger(&buf)), forgeapi.WithRetries(0))
	if _, _, err := read(t, failing, nil); err == nil {
		t.Fatal("a read whose connection was cut = nil, want the transport failure")
	}
	if got := onlyRecord(t, &buf)["cache"]; got != "off" {
		t.Errorf("the record of a read no answer completed = cache %v, want off: the cache counts it neither way", got)
	}
}

// A consumer meters requests with an attempt observer, so it sees every wire attempt,
// a 304 as the 304 the instance sent, though the caller gets the held body.
func TestTheAttemptObserverSeesEveryWireAttemptAsItWasAnswered(t *testing.T) {
	statuses := func(got *[]int, mu *sync.Mutex) func(http.RoundTripper) http.RoundTripper {
		return answerRewrite(func(resp *http.Response) {
			mu.Lock()
			defer mu.Unlock()
			*got = append(*got, resp.StatusCode)
		})
	}
	for _, test := range []struct {
		name        string
		notModified int
		want        []int
	}{
		{name: "a_revalidated_read", want: []int{http.StatusOK, http.StatusNotModified}},
		{name: "a_validatorless_304_resent", notModified: 1, want: []int{http.StatusNotModified, http.StatusOK, http.StatusNotModified}},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := newOrigin(`{"login":"octo"}`, `"v1"`)
			o.notModified = test.notModified
			var got []int
			var mu sync.Mutex
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: serveOrigin(t, o).URL}, forgeapi.WithAttemptObserver(statuses(&got, &mu)))
			for range 2 {
				if answer, _, err := read(t, c, nil); err != nil || answer.Login != "octo" {
					t.Fatalf("a read = %+v, %v, want the instance's body", answer, err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(got, test.want) || len(got) != len(o.requests()) {
				t.Errorf("the attempt observer saw %v over %d wire request(s), want %v, one per request", got, len(o.requests()), test.want)
			}
		})
	}
}
