package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// The canonical subject these tests address. The selector is NESTED, which is this
// family's own shape and the one an owner-and-name reading of a path silently gets
// wrong: two separators have to survive as one encoded path segment.
const (
	testSelector = "example/group/example"
	testEncoded  = "example%2Fgroup%2Fexample"
	testToken    = "unit-placeholder"
	testHeadSHA  = "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d"
)

// testCredential is the source every client here is built with, because injection
// is mandatory and a client without one is refused at construction.
type testCredential struct{}

func (testCredential) Token(context.Context) (string, error) { return testToken, nil }
func (testCredential) Kind() forgeapi.CredKind               { return forgeapi.CredKindStaticPAT }
func (testCredential) State() forgeapi.CredState             { return forgeapi.CredValid }

// instance is one test's stand-in for a GitLab instance: the requests that arrived,
// the credential each carried, the bodies of the mutations and the documents that
// were posted.
type instance struct {
	server    *httptest.Server
	headers   http.Header
	bodies    map[string]string
	queries   map[string]url.Values
	routes    map[string]string
	statuses  map[string]int
	requests  []string
	tokens    []string
	documents []string
	mu        sync.Mutex
}

// newInstance is an instance with nothing recorded and no answer header set.
func newInstance() *instance {
	return &instance{
		headers:  http.Header{},
		bodies:   map[string]string{},
		queries:  map[string]url.Values{},
		routes:   map[string]string{},
		statuses: map[string]int{},
	}
}

// serve replaces what one route answers, which is how a connection whose behaviour
// CHANGES between two calls is driven: a document refused once and answered
// afterwards, or a page whose successor arrives on the second read.
func (in *instance) serve(route, body string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.routes[route] = body
}

// status sets the status one route answers, so an error-mapping cell is driven end to
// end rather than only at the mapper.
func (in *instance) status(route string, code int) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.statuses[route] = code
}

// arrived is every request this instance saw, as method and escaped path, in order.
// The ESCAPED path is what the selector assertion needs: a family that sent the
// decoded namespace path would arrive here as extra path segments.
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

// posted is every document body this instance received, which is what a document
// assertion reads.
func (in *instance) posted() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.documents...)
}

// answerHeader sets a response header this instance carries on every later answer,
// which is how the budget signal, the continuation and the family witness are put on
// the wire.
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
		UnknownEnumValue: func(_ forgeapi.Family, field string) { s.fire("UnknownEnumValue:" + field) },
		RateLimited:      func(forgeapi.Family) { s.fire("RateLimited") },
		Retried:          func(forgeapi.Family, string) { s.fire("Retried") },
		PartialResult:    func(_ forgeapi.Family, reason forgeapi.PartialReason) { s.fire("PartialResult:" + reason.String()) },
		AddressRefusal:   func(_ forgeapi.Family, kind string) { s.fire("AddressRefusal:" + kind) },
		ReadDeferred:     func(forgeapi.Family, string) { s.fire("ReadDeferred") },
		EnvelopeError:    func(_ forgeapi.Family, kind string) { s.fire("EnvelopeError:" + kind) },
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

// documentRoute is the key a posted document is recorded under, since this product's
// GraphQL endpoint sits beside the versioned REST root rather than under it.
const documentRoute = "POST /api/graphql"

// newHarness builds a client over an httptest server answering from routes, which map
// "METHOD /escaped-path" onto the body to serve. A route no entry matches answers 500
// with a message naming it, so a client reaching an undocumented route fails the test
// rather than reading a body it was not meant to see.
func newHarness(t *testing.T, routes map[string]string, opts ...forgeapi.Option) *harness {
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
			if key == documentRoute {
				in.documents = append(in.documents, body)
			}
		}
		maps.Copy(w.Header(), in.headers)
		answer, ok := in.routes[key]
		code := in.statuses[key]
		in.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			if _, err := w.Write([]byte(`{"message":"no route for ` + key + `"}`)); err != nil {
				t.Errorf("Setup: writing the miss for %s: %v", key, err)
			}
			return
		}
		if code != 0 {
			w.WriteHeader(code)
		}
		if _, err := w.Write([]byte(answer)); err != nil {
			t.Errorf("Setup: writing the answer for %s: %v", key, err)
		}
	}))
	t.Cleanup(in.server.Close)
	return newHarnessOver(t, in, opts...)
}

// newHarnessOver wires a client onto an instance a caller already built.
func newHarnessOver(t *testing.T, in *instance, opts ...forgeapi.Option) *harness {
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
	client, err := open(&forgeapi.Connection{WebBaseURL: in.server.URL}, time.Now, append(base, opts...)...)
	if err != nil {
		t.Fatalf("Setup: New(%q): %v", in.server.URL, err)
	}
	h.client = client
	return h
}

// newRefusingHarness builds a client over an instance that answers one status to every
// request, which is how an error-mapping cell is driven end to end.
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
	return newHarnessOver(t, in)
}

// logged reports whether any line this client emitted carries a substring, which is
// how a totality table's log arm is held: the unknown value has to be NAMED somewhere
// a maintainer reads.
func (h *harness) logged(want string) bool {
	return strings.Contains(h.logs.String(), want)
}

// line is the FIRST line this client emitted carrying a substring, so a case can
// hold the attributes of one exchange's record rather than of the whole buffer: a
// containment check over every line passes when two lines each carry half of what
// one line had to say.
func (h *harness) line(want string) (string, bool) {
	for line := range strings.Lines(h.logs.String()) {
		if strings.Contains(line, want) {
			return strings.TrimRight(line, "\n"), true
		}
	}
	return "", false
}

// testRef is the canonical repository reference, derived rather than allocated.
func testRef() forgeapi.RepoRef {
	ref := forgeapi.RepoRef{Family: forgeapi.FamilyGitLab, Selector: testSelector, DisplayPath: testSelector}
	ref.ID = ref.Encode()
	return ref
}

// testPR is the canonical merge-request reference, sigil included.
func testPR() forgeapi.PRRef { return forgeapi.PRRef{Number: 1, Sigil: sigil} }

func readAll(t *testing.T, r *http.Request) string {
	t.Helper()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(http.MaxBytesReader(nil, r.Body, 1<<20)); err != nil {
		t.Errorf("Setup: reading the request body of %s %s: %v", r.Method, r.URL.Path, err)
	}
	return buf.String()
}

// captures is the recorded bodies under testdata, which are the SPECIFICATION side of
// every wire-shape assertion here: a shape written out in this file would hold the
// wire types against a copy of themselves, so a tag that disagrees with what the
// product actually sends would move nothing and fail nothing.
type captures struct {
	Provenance string `json:"//"`
	Rows       map[string]json.RawMessage
}

// readCaptures loads that fixture and holds it to naming the recording it was cut
// from and the date that recording was read.
func readCaptures(t *testing.T) captures {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "captures.json"))
	if err != nil {
		t.Fatalf("Setup: reading the recorded bodies: %v", err)
	}
	var rows map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("Setup: decoding the recorded bodies: %v", err)
	}
	var provenance string
	if err := json.Unmarshal(rows["//"], &provenance); err != nil {
		t.Fatalf("Setup: the recorded bodies carry no provenance line: %v", err)
	}
	if !strings.Contains(provenance, "read 20") {
		t.Fatalf("Setup: the recorded bodies' provenance %q names no read date", provenance)
	}
	delete(rows, "//")
	if len(rows) == 0 {
		t.Fatal("Setup: the recorded bodies carry no row, which is what every wire assertion here reads")
	}
	return captures{Provenance: provenance, Rows: rows}
}

// row is one recorded body, decoded into the wire type that reads it on the instance.
func (c captures) row(t *testing.T, name string, out any) {
	t.Helper()
	raw, ok := c.Rows[name]
	if !ok {
		t.Fatalf("Setup: the recorded bodies carry no %q row", name)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decoding the recorded %s into %T = %v, want nil: this family's wire type disagrees with the bytes the product sent", name, out, err)
	}
}

// restCursor is the continuation a REST list call with opts mints for page on
// client's connection, which is the one a call naming that page hands back.
func restCursor(t *testing.T, client *Client, op string, repo forgeapi.RepoRef, page int, opts ...forgeapi.ListOption) forgeapi.Cursor {
	t.Helper()
	return restWalk{call: listCall(t, client, op, repo, opts...), page: page}.here()
}

// listCall is the call a list with opts makes on client's connection, which every
// continuation it mints names.
func listCall(t *testing.T, client *Client, op string, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) transport.PageCall {
	t.Helper()
	set, err := forgeapi.ResolveList(opts...)
	if err != nil {
		t.Fatalf("Setup: ResolveList() = %v", err)
	}
	return client.core.PageCall(op, repo, set)
}
