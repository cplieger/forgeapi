package gitea

import (
	"bytes"
	"context"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// The canonical subject these tests address. It is the same placeholder the
// conformance fixtures use, so a failure here and a failure there name one
// repository.
const (
	testOwner    = "example"
	testRepo     = "example"
	testSelector = testOwner + "/" + testRepo
	testToken    = "unit-placeholder"
	testHeadSHA  = "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d"
)

// testCredential is the source every client here is built with, because injection
// is mandatory and a client without one is refused at construction.
type testCredential struct{}

func (testCredential) Token(context.Context) (string, error) { return testToken, nil }
func (testCredential) Kind() forgeapi.CredKind               { return forgeapi.CredKindStaticPAT }
func (testCredential) State() forgeapi.CredState             { return forgeapi.CredValid }

// instance is one test's stand-in for a Gitea-family instance: the requests that
// arrived, the credential each carried, and the bodies of the mutations.
type instance struct {
	server   *httptest.Server
	headers  http.Header
	bodies   map[string]string
	queries  map[string]url.Values
	routes   map[string]string
	requests []string
	tokens   []string
	mu       sync.Mutex
}

// newInstance is an instance with nothing recorded and no answer header set.
func newInstance() *instance {
	return &instance{
		headers: http.Header{},
		bodies:  map[string]string{},
		queries: map[string]url.Values{},
		routes:  map[string]string{},
	}
}

// serve replaces what one route answers, which is how a list that CHANGED between
// two polling cycles is driven: the rows a cycle folded can be merged, closed or
// paged out before the next one, and that is the case a static route map cannot
// express. It takes the instance's own lock, so the handler and the test are not
// racing over the answer.
func (in *instance) serve(route, body string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.routes[route] = body
}

// arrived is every request this instance saw, as method and escaped path, in order.
func (in *instance) arrived() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.requests...)
}

// credentials is the Authorization header of every request this instance saw, so a
// client that stops authenticating is visible rather than merely unproven.
func (in *instance) credentials() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.tokens...)
}

// body is the request body one route received, empty where that route saw none.
func (in *instance) body(route string) string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.bodies[route]
}

// query is one query parameter of the last request that arrived at a route, so a
// filter the operation must send is held rather than assumed.
func (in *instance) query(route, key string) string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.queries[route].Get(key)
}

// answerHeader sets a response header this instance carries on every later answer,
// which is how a product's budget signal is put on the wire.
func (in *instance) answerHeader(name, value string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.headers.Set(name, value)
}

// count is how many requests arrived, which is what a published price is measured
// against.
func (in *instance) count() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return len(in.requests)
}

// stepClock is a clock the test moves itself. Its only reader is the governor's
// rolling interval, so a case that needs the next window says so in one call rather
// than sleeping past a real one and hoping the fold it measures was quicker than the
// window it was measured in.
type stepClock struct {
	at time.Time
	mu sync.Mutex
}

// newStepClock starts at a fixed instant, so nothing a case reports depends on when
// it ran.
func newStepClock() *stepClock {
	return &stepClock{at: time.Date(2026, time.September, 19, 12, 0, 0, 0, time.UTC)}
}

func (c *stepClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

// step moves the clock on, which is what rolls the governor's interval.
func (c *stepClock) step(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// spy is the counter set a test installs, recording every event by name so an
// assertion names the counter rather than a call site.
type spy struct {
	fired map[string]int
	mu    sync.Mutex
}

func newSpy() *spy { return &spy{fired: map[string]int{}} }

func (s *spy) fire(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fired[name]++
}

func (s *spy) times(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fired[name]
}

// counters is the seam this library publishes, with every field wired to the spy.
func (s *spy) counters() forgeapi.Counters {
	return forgeapi.Counters{
		UnknownEnumValue: func(forgeapi.Family, string) { s.fire("UnknownEnumValue") },
		RateLimited:      func(forgeapi.Family) { s.fire("RateLimited") },
		Retried:          func(forgeapi.Family, string) { s.fire("Retried") },
		PartialResult:    func(_ forgeapi.Family, reason forgeapi.PartialReason) { s.fire("PartialResult:" + reason.String()) },
		AddressRefusal:   func(_ forgeapi.Family, kind string) { s.fire("AddressRefusal:" + kind) },
		ReadDeferred:     func(forgeapi.Family, string) { s.fire("ReadDeferred") },
		EnvelopeError:    func(forgeapi.Family, string) { s.fire("EnvelopeError") },
		SunsetSeen:       func(forgeapi.Family) { s.fire("SunsetSeen") },
		RefreshOutcome:   func(forgeapi.Family, forgeapi.CredState) { s.fire("RefreshOutcome") },
		HelperDecline:    func(string) { s.fire("HelperDecline") },
		CapabilityContradicted: func(forgeapi.Family, forgeapi.Capability) {
			s.fire("CapabilityContradicted")
		},
		RedirectHopCapExceeded:    func(forgeapi.Family) { s.fire("RedirectHopCapExceeded") },
		RedirectPortRefused:       func(forgeapi.Family) { s.fire("RedirectPortRefused") },
		NonDurableCredentialWrite: func(forgeapi.Family) { s.fire("NonDurableCredentialWrite") },
	}
}

// harness is what every test here drives: a client, the instance behind it, the
// counters it fires and the lines it logs.
type harness struct {
	client   *Client
	instance *instance
	spy      *spy
	logs     *bytes.Buffer
}

// newHarness builds a client over an httptest server answering from routes, which
// map "METHOD /path" onto the body to serve. A route no entry matches answers 500
// with a message naming it, so a client reaching an undocumented route fails the
// test rather than reading a body it was not meant to see.
func newHarness(t *testing.T, routes map[string]string, opts ...forgeapi.Option) *harness {
	t.Helper()
	return newClockedHarness(t, time.Now, routes, opts...)
}

// newClockedHarness is newHarness with the governor's clock supplied, for a case
// that spans more than one of its rolling intervals: the interval is a bound only
// time moves, so a case waiting out a real one is a race against however loaded the
// machine is, and a stepped clock makes the same statement deterministically.
func newClockedHarness(t *testing.T, clock func() time.Time, routes map[string]string, opts ...forgeapi.Option) *harness {
	t.Helper()
	in := newInstance()
	maps.Copy(in.routes, routes)
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.EscapedPath()
		body := readAll(t, r)
		in.mu.Lock()
		in.requests = append(in.requests, key)
		in.tokens = append(in.tokens, r.Header.Get("Authorization"))
		in.queries[key] = r.URL.Query()
		if body != "" {
			in.bodies[key] = body
		}
		maps.Copy(w.Header(), in.headers)
		answer, ok := in.routes[key]
		in.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			if _, err := w.Write([]byte(`{"message":"no route for ` + key + `"}`)); err != nil {
				t.Errorf("Setup: writing the miss for %s: %v", key, err)
			}
			return
		}
		if _, err := w.Write([]byte(answer)); err != nil {
			t.Errorf("Setup: writing the answer for %s: %v", key, err)
		}
	}))
	t.Cleanup(in.server.Close)
	return newHarnessOver(t, in, clock, opts...)
}

// newHarnessOver wires a client onto an instance a caller already built, which is the
// half the two constructors share.
func newHarnessOver(t *testing.T, in *instance, clock func() time.Time, opts ...forgeapi.Option) *harness {
	t.Helper()
	h := &harness{instance: in, spy: newSpy(), logs: &bytes.Buffer{}}
	base := []forgeapi.Option{
		forgeapi.WithWireTransport(in.server.Client().Transport),
		forgeapi.WithCredentialSource(testCredential{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithCounters(h.spy.counters()),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))),
	}
	client, err := open(&forgeapi.Connection{WebBaseURL: in.server.URL}, clock, append(base, opts...)...)
	if err != nil {
		t.Fatalf("Setup: New(%q): %v", in.server.URL, err)
	}
	client.maxItems = testMaxItems
	h.client = client
	return h
}

// testMaxItems is the maximum page size every connection here holds, as one whose
// setup read an instance stating the products' shipped max_response_items: what the
// cases here price and route is an operation on a connection already set up, and
// the settings read itself is the connection row's, held by the conformance suite.
const testMaxItems = 50

// newRefusingHarness builds a client over an instance that answers one status to
// every request, which is how an error-mapping cell is driven end to end rather than
// only at the mapper.
func newRefusingHarness(t *testing.T, status int) *harness {
	t.Helper()
	in := newInstance()
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in.mu.Lock()
		in.requests = append(in.requests, r.Method+" "+r.URL.EscapedPath())
		in.tokens = append(in.tokens, r.Header.Get("Authorization"))
		in.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write([]byte(`{"message":"refused"}`)); err != nil {
			t.Errorf("Setup: writing the refusal: %v", err)
		}
	}))
	t.Cleanup(in.server.Close)
	return newHarnessOver(t, in, time.Now)
}

// logged reports whether any line this client emitted carries a substring, which is
// how a totality table's log arm is held: the unknown value has to be NAMED
// somewhere a maintainer reads.
func (h *harness) logged(want string) bool {
	return strings.Contains(h.logs.String(), want)
}

// testPR is the canonical pull-request reference, the number the fixtures answer for.
func testPR() forgeapi.PRRef {
	return forgeapi.PRRef{Number: 1, Sigil: "#"}
}

// testRef is the canonical repository reference, derived rather than allocated.
func testRef() forgeapi.RepoRef {
	ref := forgeapi.RepoRef{Family: forgeapi.FamilyGitea, Selector: testSelector, DisplayPath: testSelector}
	ref.ID = ref.Encode()
	return ref
}

func readAll(t *testing.T, r *http.Request) string {
	t.Helper()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(http.MaxBytesReader(nil, r.Body, 1<<20)); err != nil {
		t.Errorf("Setup: reading the request body of %s %s: %v", r.Method, r.URL.Path, err)
	}
	return buf.String()
}
