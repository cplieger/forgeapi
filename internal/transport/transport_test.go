package transport

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi"
)

// counterSpy records every counter this library fires, by name, so an assertion names
// the counter rather than a call site.
type counterSpy struct {
	fired map[string]int
	mu    sync.Mutex
}

func newCounterSpy() *counterSpy { return &counterSpy{fired: map[string]int{}} }

func (s *counterSpy) fire(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fired[name]++
}

func (s *counterSpy) times(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fired[name]
}

func (s *counterSpy) timesAny(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for name, fired := range s.fired {
		if strings.HasPrefix(name, prefix) {
			n += fired
		}
	}
	return n
}

func (s *counterSpy) counters() forgeapi.Counters {
	return forgeapi.Counters{
		Retried:                func(_ forgeapi.Family, op string) { s.fire("Retried:" + op) },
		RateLimited:            func(forgeapi.Family) { s.fire("RateLimited") },
		AddressRefusal:         func(_ forgeapi.Family, kind string) { s.fire("AddressRefusal:" + kind) },
		RedirectHopCapExceeded: func(forgeapi.Family) { s.fire("RedirectHopCapExceeded") },
		RedirectPortRefused:    func(forgeapi.Family) { s.fire("RedirectPortRefused") },
		ReadDeferred:           func(forgeapi.Family, string) { s.fire("ReadDeferred") },
	}
}

// testMapper is a stand-in for a family's own mapping, enough to tell a throttle from
// the rest.
func testMapper(_ string, status int, _ http.Header, _ string) (forgeapi.ErrorKind, string) {
	switch {
	case status == http.StatusTooManyRequests:
		return forgeapi.KindRateLimited, ""
	case status >= 500:
		return forgeapi.KindUpstream, ""
	}
	return forgeapi.KindUpstream, ""
}

// testSignal reads the same structured header one in-scope product sends.
func testSignal(header http.Header) (int, time.Time, bool) {
	if header.Get("X-Remaining") == "" {
		return 0, time.Time{}, false
	}
	remaining := 0
	if _, err := fmtSscan(header.Get("X-Remaining"), &remaining); err != nil {
		return 0, time.Time{}, false
	}
	return remaining, time.Now().Add(time.Minute), true
}

func fmtSscan(s string, out *int) (int, error) {
	n := 0
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(s[i]-'0')
	}
	*out = n
	return 1, nil
}

func testOptions() Options {
	return Options{
		Mapper:        testMapper,
		Signal:        testSignal,
		DeriveAPIBase: func(web *url.URL) string { return web.String() + "/api/v1" },
		Family:        forgeapi.FamilyGitea,
	}
}

func settingsFor(opts ...forgeapi.Option) *forgeapi.Settings {
	set := forgeapi.Resolve(opts...)
	return &set
}

// openTestConn builds one connection's core over a server the caller already has, with
// the two per-connection statements a loopback instance needs.
func openTestConn(t *testing.T, conn forgeapi.Connection, extra ...forgeapi.Option) *Conn {
	t.Helper()
	return openConnWith(t, conn, testOptions(), extra...)
}

func openConnWith(t *testing.T, conn forgeapi.Connection, family Options, extra ...forgeapi.Option) *Conn {
	t.Helper()
	opts := append([]forgeapi.Option{
		forgeapi.WithWireTransport(&http.Transport{}),
		forgeapi.WithCredentialSource(stubCredential{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(discardLogger()),
	}, extra...)
	c, err := Open(&conn, settingsFor(opts...), family)
	if err != nil {
		t.Fatalf("Setup: Open(%q): %v", conn.WebBaseURL, err)
	}
	t.Cleanup(c.Close)
	return c
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonAnswer(r *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}
}

// TestTheEnvironmentsProxyVariablesAreNeverRead holds the statement that makes a
// connection record the whole truth about where its traffic goes.
//
// net/http's own transport consults six of them by default, so a stack that did not
// overwrite that would route this library's traffic through a proxy nobody stated on
// the connection, invisibly in every connection record. The trap listener is the
// instrument: anything that reaches it fails the test by arriving at all.
func TestTheEnvironmentsProxyVariablesAreNeverRead(t *testing.T) {
	trap := &atomic.Int64{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Setup: listening for the trap: %v", err)
	}
	stop := make(chan struct{})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				close(stop)
				return
			}
			trap.Add(1)
			if closeErr := conn.Close(); closeErr != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("closing the trap listener: %v", err)
		}
		<-stop
	})

	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(name, "http://"+listener.Addr().String())
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	destination := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destination.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	defer srv.Close()

	// No injected wire here: the point is the transport this library ASSEMBLES,
	// which is the value whose proxy the connection record alone decides.
	c, err := Open(&forgeapi.Connection{WebBaseURL: srv.URL}, settingsFor(
		forgeapi.WithCredentialSource(stubCredential{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(discardLogger()),
	), testOptions())
	if err != nil {
		t.Fatalf("Setup: Open(%q): %v", srv.URL, err)
	}
	t.Cleanup(c.Close)
	if _, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
		t.Fatalf("a read with every proxy variable set = %v, want nil", err)
	}
	if got := trap.Load(); got != 0 {
		t.Errorf("the proxy named by the environment saw %d connection(s), want 0: the connection record is the whole truth about where its traffic goes", got)
	}
	if got := destination.Load(); got != 1 {
		t.Errorf("the destination saw %d request(s), want 1", got)
	}
	// The trap alone cannot fail here and saying so is part of the test: Go's own
	// proxy resolution never proxies a LOOPBACK destination, so a stack that did
	// consult the environment would still reach this server directly. What closes
	// the claim is the resolved transport's own proxy function, which is nil
	// exactly where the connection stated none, and that nil is what makes the
	// variables unreachable at every destination rather than at this one.
	if c.wire.Proxy != nil {
		t.Error("the assembled transport carries a proxy function for a connection that stated no proxy, want none: a fallback there is the whole environment reachable from a connection record that shows nothing")
	}
}

// TestAConnectionsOwnProxyIsTheOneThatIsUsed holds the other half of the proxy rule: the
// proxy a connection STATES is dialled, and the destination sees no connection at all,
// because the socket goes to the proxy and the proxy resolves the destination.
func TestAConnectionsOwnProxyIsTheOneThatIsUsed(t *testing.T) {
	proxied := &atomic.Int64{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxied.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the proxy's answer: %v", err)
		}
	}))
	defer proxy.Close()

	destination := &atomic.Int64{}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destination.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the destination's answer: %v", err)
		}
	}))
	defer origin.Close()

	c, err := Open(&forgeapi.Connection{WebBaseURL: origin.URL, Proxy: proxy.URL}, settingsFor(
		forgeapi.WithCredentialSource(stubCredential{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(discardLogger()),
	), testOptions())
	if err != nil {
		t.Fatalf("Setup: Open with a stated proxy: %v", err)
	}
	t.Cleanup(c.Close)
	if _, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
		t.Fatalf("a read over a stated proxy = %v, want nil", err)
	}
	if got := proxied.Load(); got != 1 {
		t.Errorf("the stated proxy saw %d request(s), want 1", got)
	}
	if got := destination.Load(); got != 0 {
		t.Errorf("the destination saw %d request(s), want 0: a proxied connection's socket goes to the proxy", got)
	}
}

// TestAMalformedProxyIsRefusedRatherThanDialledDirect holds the refusal that keeps a
// stated proxy from failing open.
func TestAMalformedProxyIsRefusedRatherThanDialledDirect(t *testing.T) {
	for _, test := range []struct {
		name  string
		proxy string
		code  string
	}{
		{name: "no_scheme", proxy: "proxy.example.com:3128", code: forgeapi.CodeConnectionInvalid},
		{name: "unparseable", proxy: "http://[::1", code: forgeapi.CodeConnectionInvalid},
		{name: "a_scheme_this_library_does_not_proxy_over", proxy: "ftp://proxy.example.com", code: forgeapi.CodeConnectionInvalid},
		{name: "a_private_address_with_no_opt_in", proxy: "http://127.0.0.1:3128", code: forgeapi.CodePrivateAddressRefused},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn := forgeapi.Connection{WebBaseURL: "https://forge.example.com", Proxy: test.proxy}
			_, err := Open(&conn, settingsFor(
				forgeapi.WithCredentialSource(stubCredential{}),
				forgeapi.WithLogger(discardLogger()),
			), testOptions())
			var fe *forgeapi.Error
			if !asError(err, &fe) {
				t.Fatalf("Open with proxy %q = %v, want a refusal", test.proxy, err)
			}
			if fe.Code != test.code {
				t.Errorf("Open with proxy %q = code %q, want %q", test.proxy, fe.Code, test.code)
			}
		})
	}
}

// TestTheAddressPolicyRefusesAndCountsAtTheSocket holds the containment hook this
// library installs on the transport, which is the position the address verdict is
// taken at.
//
// The counter is the point. The layer beneath logs its own refusals to the default
// sink and exposes no logger option, so a refusal reported only there lands somewhere
// this library does not own and no consumer can see it; the hook is where this library
// counts it for itself. The hook is a value rather than a behaviour reachable through
// a request here, because reaching it through one needs a name that resolves privately
// and so a resolver this test does not own.
func TestTheAddressPolicyRefusesAndCountsAtTheSocket(t *testing.T) {
	spy := newCounterSpy()
	counters := spy.counters()
	policy := addressPolicy(settingsFor(), &counters, forgeapi.FamilyGitea)
	if policy(netip.MustParseAddr("127.0.0.1")) {
		t.Error("the address policy admitted a loopback address, want a refusal: private ranges are denied without the per-connection opt-in")
	}
	if fired := spy.times("AddressRefusal:dial"); fired != 1 {
		t.Errorf("the refused address fired AddressRefusal %d time(s), want 1: the layer beneath logs to a sink this library does not own, so the counter is the only place a consumer sees it", fired)
	}
	if !policy(netip.MustParseAddr("93.184.216.34")) {
		t.Error("the address policy refused a globally routable address, want it admitted")
	}
	opted := addressPolicy(settingsFor(forgeapi.WithPrivateAddresses(true)), &counters, forgeapi.FamilyGitea)
	if !opted(netip.MustParseAddr("127.0.0.1")) {
		t.Error("the address policy refused a loopback address under the explicit opt-in, want it admitted")
	}
}

// TestMutationPacingIsBoundedByTheOperationDeadline holds the pacing wait to the one
// bound that reaches every blocking phase.
//
// The per-operation bound is a context deadline, and a timer waited on alone holds the
// serialization lock past it: the caller then gets a cancellation long after its own
// clock expired, which makes the published deadline a figure rather than a bound.
func TestMutationPacingIsBoundedByTheOperationDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	defer srv.Close()
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL},
		forgeapi.WithMutations(true),
		forgeapi.WithMutationInterval(2*time.Second),
		forgeapi.WithOperationTimeout(50*time.Millisecond),
	)
	mutation := &Request{Op: "CreateIssue", Method: http.MethodPost, Path: "/x", Body: map[string]any{"a": 1}}
	if _, err := c.Do(t.Context(), mutation); err != nil {
		t.Fatalf("the first mutation = %v, want nil", err)
	}
	started := time.Now()
	_, err := c.Do(t.Context(), mutation)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("the second mutation inside the pacing interval = nil, want the deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the second mutation = %v, want the deadline sentinel returned unchanged", err)
	}
	if elapsed > time.Second {
		t.Errorf("the second mutation returned after %v, want inside the 50ms operation bound: the pacing wait selects on the operation's context, so the bound reaches it", elapsed)
	}
}

func TestASecondMutationWaitsOutOnlyWhatIsLeftOfTheInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wire := roundTripFunc(func(r *http.Request) (*http.Response, error) { return jsonAnswer(r, http.StatusOK, `{}`), nil })
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"},
			forgeapi.WithWireTransport(wire),
			forgeapi.WithMutations(true),
			forgeapi.WithMutationInterval(time.Minute),
			forgeapi.WithOperationTimeout(time.Hour),
		)
		mutation := &Request{Op: "CreateIssue", Method: http.MethodPost, Path: "/x", Body: map[string]any{"a": 1}}
		if _, err := c.Do(t.Context(), mutation); err != nil {
			t.Fatalf("the first mutation = %v, want nil", err)
		}
		time.Sleep(20 * time.Second)
		started := time.Now()
		if _, err := c.Do(t.Context(), mutation); err != nil {
			t.Fatalf("the second mutation = %v, want nil", err)
		}
		if waited := time.Since(started); waited != 40*time.Second {
			t.Errorf("a mutation 20s after the last one under a one-minute interval waited %v, want 40s", waited)
		}
	})
}

// TestARetriedRequestIsCountedAndRecorded holds the retry seam, which is the only
// place this library learns that an attempt was repeated: the counter names the
// operation, and the per-request record carries the attempt the request reached.
func TestARetriedRequestIsCountedAndRecorded(t *testing.T) {
	attempts := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			if _, err := io.WriteString(w, `{"message":"gateway"}`); err != nil {
				t.Errorf("Setup: writing the gateway answer: %v", err)
			}
			return
		}
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	defer srv.Close()
	spy := newCounterSpy()
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL},
		forgeapi.WithCounters(spy.counters()),
		forgeapi.WithRetries(2),
	)
	if _, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
		t.Fatalf("a read whose first attempt answered 502 = %v, want nil after the retry", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("the instance saw %d attempt(s), want 2", got)
	}
	if fired := spy.times("Retried:Whoami"); fired != 1 {
		t.Errorf("the retry fired the Retried counter for Whoami %d time(s), want 1: the counter names the operation the repeated attempt belongs to", fired)
	}
}

// TestATransportRefusalIsNotReportedAsAThrottleFromTheStatusAlone holds the throttle
// rule: a 429 reaches the mapper with its duration rather than arriving as a deadline
// cancellation, which needs a retry predicate that never retries that status.
func TestATransportRefusalIsNotReportedAsAThrottleFromTheStatusAlone(t *testing.T) {
	seen := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		seen.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		if _, err := io.WriteString(w, `{"message":"slow down"}`); err != nil {
			t.Errorf("Setup: writing the throttle: %v", err)
		}
	}))
	defer srv.Close()
	spy := newCounterSpy()
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithCounters(spy.counters()))
	_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("a throttled read = %v, want a *forgeapi.Error", err)
	}
	if fe.Kind != forgeapi.KindRateLimited {
		t.Errorf("the throttle = kind %v, want %v", fe.Kind, forgeapi.KindRateLimited)
	}
	if fe.RetryAfter != 7*time.Second {
		t.Errorf("the throttle = retry after %v, want %v: the duration upstream named is what the caller waits", fe.RetryAfter, 7*time.Second)
	}
	if got := seen.Load(); got != 1 {
		t.Errorf("the instance saw %d attempt(s), want 1: a throttle ends the cycle rather than consuming the retry budget", got)
	}
	if fired := spy.times("RateLimited"); fired != 1 {
		t.Errorf("the throttle fired the RateLimited counter %d time(s), want 1", fired)
	}
}

// TestAConditionalReadIsNotIssuedWithoutSomethingToServeFromIt holds one half of the
// ruling that every read this core issues is unconditional.
//
// The reason is what this library keeps: no response body, so a not-modified answer
// has nothing to serve from and buys a saving the caller cannot spend. The
// instance's own validator is on the wire here, which is what makes the second read
// the measurement: a core that remembered it would repeat the read conditionally and
// this would go red. The other half, what a not-modified answer arriving anyway is
// mapped to, is the case below.
func TestAConditionalReadIsNotIssuedWithoutSomethingToServeFromIt(t *testing.T) {
	var conditional atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
			conditional.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `W/"v1"`)
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	defer srv.Close()
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL})
	for range 2 {
		if _, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
			t.Fatalf("a read = %v, want nil", err)
		}
	}
	if conditional.Load() {
		t.Error("the second read carried a validator, want none: this library holds no response body, so a not-modified answer would have nothing to serve from and the caller would be handed a zero-valued answer")
	}
}

// TestANotModifiedAnswerIsMappedAsTheUnexpectedStatusItIs holds the other half of
// that ruling.
//
// With every read unconditional, a 304 is a status no request of ours asked for, so
// it is mapped like any other unmapped status, which is the upstream kind carrying
// the real status. The arm this closes is a success return whose body is empty: a
// caller handed one decodes nothing and publishes the zero value over whatever it
// had, with no error anywhere to say so.
func TestANotModifiedAnswerIsMappedAsTheUnexpectedStatusItIs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL})
	_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("a read the instance answered 304 = %v, want a *forgeapi.Error: a nil error there hands the caller an empty answer to decode", err)
	}
	if fe.Kind != forgeapi.KindUpstream {
		t.Errorf("the not-modified answer = kind %v, want %v: nothing asked for it, so it is unmapped and never guessed into another kind", fe.Kind, forgeapi.KindUpstream)
	}
	if fe.Status != http.StatusNotModified {
		t.Errorf("the not-modified answer = status %d, want %d: the status is the real one always", fe.Status, http.StatusNotModified)
	}
}

// TestTheHeaderDenylistIsRefusedAtConnectTime holds the refusal an operator relies on:
// a name something beneath the consumer writes is refused by NAME rather than dropped,
// because an operator whose setting was ignored believes something false about their
// traffic.
func TestTheHeaderDenylistIsRefusedAtConnectTime(t *testing.T) {
	for _, name := range forgeapi.ReservedHeaders() {
		for _, spelling := range []string{name, mixedCase(name)} {
			conn := forgeapi.Connection{
				WebBaseURL: "https://forge.example.com",
				Headers:    []forgeapi.Header{{Name: spelling, Value: "v"}},
			}
			_, err := Open(&conn, settingsFor(
				forgeapi.WithCredentialSource(stubCredential{}),
				forgeapi.WithLogger(discardLogger()),
			), testOptions())
			var fe *forgeapi.Error
			if !asError(err, &fe) {
				t.Errorf("Open with header %q = %v, want a refusal", spelling, err)
				continue
			}
			if fe.Code != forgeapi.CodeHeaderReserved {
				t.Errorf("Open with header %q = code %q, want %q", spelling, fe.Code, forgeapi.CodeHeaderReserved)
			}
		}
	}
}

// RFC 9110 section 5.5: a field value is visible bytes, spaces and tabs; any other
// control byte splits a header or smuggles a second one.
func TestAHeaderValueIsRefusedOnlyForAControlByte(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   string
		refused bool
	}{
		{name: "a_space", value: "gateway literal"},
		{name: "a_tab", value: "gateway\tliteral"},
		{name: "the_last_visible_byte", value: "gateway~"},
		{name: "a_carriage_return", value: "gateway\rliteral", refused: true},
		{name: "a_line_feed", value: "gateway\nliteral", refused: true},
		{name: "a_nul", value: "gateway\x00literal", refused: true},
		{name: "the_last_control_byte_below_space", value: "gateway\x1fliteral", refused: true},
		{name: "a_delete", value: "gateway\x7fliteral", refused: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn := forgeapi.Connection{
				WebBaseURL: "https://forge.example.com",
				Headers:    []forgeapi.Header{{Name: "X-Gateway-Secret", Value: test.value}},
			}
			_, err := Open(&conn, settingsFor(
				forgeapi.WithCredentialSource(stubCredential{}),
				forgeapi.WithLogger(discardLogger()),
			), testOptions())
			if !test.refused {
				if err != nil {
					t.Errorf("Open with header value %q = %v, want it accepted", test.value, err)
				}
				return
			}
			var fe *forgeapi.Error
			if !asError(err, &fe) || fe.Code != forgeapi.CodeConnectionInvalid {
				t.Errorf("Open with header value %q = %v, want code %q", test.value, err, forgeapi.CodeConnectionInvalid)
			}
		})
	}
}

// TestAZeroConcurrencyLimitIsRefusedAtConstruction holds the two budget knobs whose
// zero is refused rather than installed, against the doc comments that publish that.
//
// A concurrency limit of zero admits no request, so a client carrying one could run
// no operation at all. Handing it back would make every call fail for a reason the
// caller has to work out, where the named code at construction says which option did
// it; and with one configuration door the zero cannot be read as a request for the
// default. These two options have no other caller in the tree, which is what makes
// the pairing between each clause and this refusal worth pinning.
func TestAZeroConcurrencyLimitIsRefusedAtConstruction(t *testing.T) {
	for _, test := range []struct {
		name   string
		option forgeapi.Option
	}{
		{name: "a_zero_read_limit", option: forgeapi.WithReadConcurrency(0)},
		{name: "a_zero_mutation_limit", option: forgeapi.WithMutationConcurrency(0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn := forgeapi.Connection{WebBaseURL: "https://forge.example.com"}
			core, err := Open(&conn, settingsFor(
				forgeapi.WithCredentialSource(stubCredential{}),
				forgeapi.WithLogger(discardLogger()),
				test.option,
			), testOptions())
			if core != nil {
				t.Errorf("Open answered a core beside the refusal, want nothing: a client that admits no request has no working operation on it")
			}
			var fe *forgeapi.Error
			if !asError(err, &fe) {
				t.Fatalf("Open = %v, want a refusal: a zero concurrency limit admits no request", err)
			}
			if fe.Code != forgeapi.CodeBudgetInvalid {
				t.Errorf("Open = code %q, want %q: the option's own doc names this code, and a consumer names the remedy from it", fe.Code, forgeapi.CodeBudgetInvalid)
			}
			if fe.Status != 0 {
				t.Errorf("Open = status %d, want 0: nothing was sent, so there is no status to carry", fe.Status)
			}
		})
	}
}

// Every dial lands on serve over an in-memory pipe; call it inside a synctest bubble,
// where the pipe's waits advance the bubble's clock.
func openPipedConn(t *testing.T, web string, serve func(net.Conn), extra ...forgeapi.Option) *Conn {
	t.Helper()
	opts := append([]forgeapi.Option{
		forgeapi.WithCredentialSource(stubCredential{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithLogger(discardLogger()),
	}, extra...)
	conn := forgeapi.Connection{WebBaseURL: web}
	c, err := Open(&conn, settingsFor(opts...), testOptions())
	if err != nil {
		t.Fatalf("Setup: Open(%q): %v", web, err)
	}
	c.wire.DialContext = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go serve(server)
		return client, nil
	}
	t.Cleanup(c.Close)
	return c
}

func drain(server net.Conn) {
	defer server.Close()
	_, _ = io.Copy(io.Discard, server)
}

func TestAnInstanceStallingOnePhaseFailsTheRequestAtThatPhasesBound(t *testing.T) {
	for _, test := range []struct {
		name  string
		web   string
		serve func(net.Conn)
		want  time.Duration
	}{
		{name: "a_handshake_never_answered", web: "https://forge.example", serve: drain, want: 10 * time.Second},
		{
			name: "a_request_never_answered",
			web:  "http://forge.example",
			serve: func(server net.Conn) {
				defer server.Close()
				if _, err := http.ReadRequest(bufio.NewReader(server)); err != nil {
					return
				}
				_, _ = io.Copy(io.Discard, server)
			},
			want: 15 * time.Second,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := openPipedConn(t, test.web, test.serve,
					forgeapi.WithRetries(0),
					forgeapi.WithOperationTimeout(time.Hour),
				)
				started := time.Now()
				if _, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err == nil {
					t.Fatalf("Do against a stalled instance = nil error, want the phase's failure")
				}
				if waited := time.Since(started); waited != test.want {
					t.Errorf("Do against a stalled instance failed after %v, want %v under a one-hour operation deadline", waited, test.want)
				}
			})
		})
	}
}

// A redirect's location is upstream input, so only an address on the API root's own
// origin and beneath its own path names a route on the connection: another host,
// scheme or port is another instance, and a path beside the root is not this
// instance's API.
func TestUnderAPI_names_a_route_only_beneath_the_API_root_on_its_own_origin(t *testing.T) {
	for _, test := range []struct {
		name    string
		web     string
		address string
		want    string
		ok      bool
	}{
		{name: "beneath_the_root", web: "http://forge.example:3000/first", address: "http://forge.example:3000/first/api/v1/repos/o/n/labels?page=1", want: "/repos/o/n/labels", ok: true},
		{name: "the_host_in_capitals", web: "http://forge.example:3000/first", address: "http://FORGE.example:3000/first/api/v1/repos/o/n", want: "/repos/o/n", ok: true},
		{name: "the_default_port_spelled_out", web: "https://forge.example", address: "https://forge.example:443/api/v1/repos/o/n", want: "/repos/o/n", ok: true},
		{name: "an_escaped_segment_kept_escaped", web: "https://forge.example", address: "https://forge.example/api/v1/repos/ex%20ample/n", want: "/repos/ex%20ample/n", ok: true},
		{name: "another_host", web: "http://forge.example:3000/first", address: "http://other.example:3000/first/api/v1/repos/o/n"},
		{name: "another_scheme", web: "http://forge.example:3000/first", address: "https://forge.example:3000/first/api/v1/repos/o/n"},
		{name: "another_port", web: "http://forge.example:3000/first", address: "http://forge.example:3001/first/api/v1/repos/o/n"},
		{name: "beside_the_root", web: "http://forge.example:3000/first", address: "http://forge.example:3000/api/v1/repos/o/n"},
		{name: "a_longer_segment_sharing_the_roots_prefix", web: "http://forge.example:3000/first", address: "http://forge.example:3000/first/api/v10/repos/o/n"},
		{name: "a_relative_address", web: "http://forge.example:3000/first", address: "/first/api/v1/repos/o/n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: test.web})
			got, ok := c.UnderAPI(test.address)
			if got != test.want || ok != test.ok {
				t.Errorf("UnderAPI(%q) on %q = (%q, %v), want (%q, %v)", test.address, c.APIBase(), got, ok, test.want, test.ok)
			}
		})
	}
}

func TestParseOriginAndPath_answers_only_an_origin_and_a_path(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{name: "an_origin_and_a_path", raw: "https://forge.example:8443/first", ok: true},
		{name: "userinfo", raw: "https://user@forge.example/first"},
		{name: "a_query", raw: "https://forge.example/first?tab=1"},
		{name: "an_empty_query", raw: "https://forge.example/first?"},
		{name: "an_empty_fragment", raw: "https://forge.example/first#"},
		{name: "unparseable", raw: "https://[::1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			u, ok := ParseOriginAndPath(test.raw)
			if ok != test.ok {
				t.Fatalf("ParseOriginAndPath(%q) = ok %v, want %v", test.raw, ok, test.ok)
			}
			if ok && u.String() != test.raw {
				t.Errorf("ParseOriginAndPath(%q) = %q, want the base unchanged", test.raw, u)
			}
		})
	}
}

// mixedCase spells a header name in a case no canonical form uses, because names are
// compared case-insensitively as HTTP does.
func mixedCase(name string) string {
	out := []byte(name)
	for i := range out {
		if out[i] >= 'a' && out[i] <= 'z' {
			out[i] -= 'a' - 'A'
		} else if out[i] >= 'A' && out[i] <= 'Z' {
			out[i] += 'a' - 'A'
		}
	}
	return string(out)
}
