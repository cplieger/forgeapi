package families_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/families"
	"github.com/cplieger/forgeapi/gitea"
	"github.com/cplieger/forgeapi/github"
	"github.com/cplieger/forgeapi/gitlab"
)

// recordedAnswer is one answer a product gave, in the shape the detection fixture
// and the conformance fixtures record it: a JSON body, or a text one.
type recordedAnswer struct {
	Headers map[string]string `json:"headers"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Text    string            `json:"text"`
	Body    json.RawMessage   `json:"body"`
	Paths   []string          `json:"paths"`
	Status  int               `json:"status"`
	GraphQL bool              `json:"graphql"`
}

// payload is the bytes the answer carries.
func (a recordedAnswer) payload() []byte {
	if a.Text != "" {
		return []byte(a.Text)
	}
	return a.Body
}

// detectedProduct is one of the products the factory detects: the directory
// its fixtures are recorded under, the family it is, and the root its API answers
// under on a connection that names its web base alone.
type detectedProduct struct {
	dir     string
	root    string
	family  forgeapi.Family
	maximum int
}

// detectedProducts are the products the factory detects, each with the most
// requests detection costs on it: its own family's connection read plus the first
// read of every family asked before it. A GitHub instance a loopback address names
// is an Enterprise Server, whose REST root sits under the web base.
var detectedProducts = map[string]detectedProduct{
	"GitLab":  {dir: "gitlab", family: forgeapi.FamilyGitLab, maximum: 2},
	"GitHub":  {dir: "github", root: "/api/v3", family: forgeapi.FamilyGitHub, maximum: 3},
	"Gitea":   {dir: "gitea", family: forgeapi.FamilyGitea, maximum: 5},
	"Forgejo": {dir: "forgejo", family: forgeapi.FamilyGitea, maximum: 5},
}

// foreignReads are the answers each product gave, read anonymously, to the
// connection reads the factory sends ahead of that product's own family.
func foreignReads(t *testing.T) map[string][]recordedAnswer {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "detect.json"))
	if err != nil {
		t.Fatalf("Setup: reading the detection fixture: %v", err)
	}
	var fixture struct {
		Products map[string][]recordedAnswer `json:"products"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("Setup: the detection fixture is not the recorded shape: %v", err)
	}
	return fixture.Products
}

// connectionRead is one product's own answer to its family's connection read, from
// the conformance fixture that case is held to.
func connectionRead(t *testing.T, dir string) []recordedAnswer {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "conformance", "testdata", dir, "Capabilities.ConnectionCaps.json"))
	if err != nil {
		t.Fatalf("Setup: reading %s's connection read: %v", dir, err)
	}
	var fixture struct {
		Routes []recordedAnswer `json:"routes"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("Setup: %s's connection read is not the recorded shape: %v", dir, err)
	}
	return fixture.Routes
}

// productInstance is one product as the factory meets it: its own connection read
// answered as recorded, the foreign reads answered as that product answered them,
// and any other route answered as the product answers a route it does not serve,
// which is how it answered the first foreign read recorded for it. It records the
// method and path of every request.
type productInstance struct {
	srv  *httptest.Server
	seen []string
	mu   sync.Mutex
}

func (pi *productInstance) requests() []string {
	pi.mu.Lock()
	defer pi.mu.Unlock()
	return slices.Clone(pi.seen)
}

func newProductInstance(t *testing.T, product detectedProduct, foreign []recordedAnswer) *productInstance {
	t.Helper()
	own := connectionRead(t, product.dir)
	pi := &productInstance{}
	pi.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20)); err != nil {
			t.Errorf("Setup: reading the request body: %v", err)
		}
		path := r.URL.EscapedPath()
		pi.mu.Lock()
		pi.seen = append(pi.seen, r.Method+" "+path)
		pi.mu.Unlock()
		answer, ok := ownAnswer(own, product.root, r.Method, path)
		if !ok {
			answer, ok = foreignAnswer(foreign, r.Method, path)
		}
		if !ok {
			answer = recordedAnswer{Status: http.StatusNotFound, Text: "Not found.\n"}
		}
		for k, v := range answer.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(answer.Status)
		if _, err := w.Write(answer.payload()); err != nil {
			t.Errorf("Setup: writing %s %s: %v", r.Method, path, err)
		}
	}))
	t.Cleanup(pi.srv.Close)
	return pi
}

// ownAnswer is the product's recorded answer to one request of its own family's
// connection read: a route by its method and its path under the product's API
// root, and a document by its method alone.
func ownAnswer(own []recordedAnswer, root, method, path string) (recordedAnswer, bool) {
	for _, a := range own {
		if a.GraphQL {
			if method == http.MethodPost {
				return a, true
			}
			continue
		}
		if a.Method == method && slices.ContainsFunc(append([]string{a.Path}, a.Paths...), func(p string) bool { return p != "" && root+p == path }) {
			return a, true
		}
	}
	return recordedAnswer{}, false
}

// foreignAnswer is the product's recorded answer to a foreign read by its method
// and path, and otherwise the first foreign answer recorded for it, which is how
// it answers a route it does not serve.
func foreignAnswer(foreign []recordedAnswer, method, path string) (recordedAnswer, bool) {
	for _, a := range foreign {
		if a.Method == method && a.Path == path {
			return a, true
		}
	}
	if len(foreign) == 0 {
		return recordedAnswer{}, false
	}
	return foreign[0], true
}

// openOptions are what every factory call here passes: the instance's own
// transport, the two per-connection statements a loopback plaintext server needs,
// the mandatory credential and a silent logger.
func openOptions(srv *httptest.Server) []forgeapi.Option {
	return []forgeapi.Option{
		forgeapi.WithWireTransport(srv.Client().Transport),
		forgeapi.WithCredentialSource(openCredential{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
}

// ownCost is what one product's own family sends for its connection read on a
// fresh instance of the product, built with that family's constructor.
func ownCost(t *testing.T, name string, product detectedProduct, foreign []recordedAnswer) int {
	t.Helper()
	pi := newProductInstance(t, product, foreign)
	conn := forgeapi.Connection{WebBaseURL: pi.srv.URL}
	var client forgeapi.Capabilities
	var err error
	switch product.family {
	case forgeapi.FamilyGitHub:
		client, err = github.New(conn, openOptions(pi.srv)...)
	case forgeapi.FamilyGitLab:
		client, err = gitlab.New(conn, openOptions(pi.srv)...)
	default:
		client, err = gitea.New(conn, openOptions(pi.srv)...)
	}
	if err != nil {
		t.Fatalf("Setup: %s's own client: %v", name, err)
	}
	if _, err := client.ConnectionCaps(t.Context()); err != nil {
		t.Fatalf("Setup: %s's own connection read on its recorded answer = error %v, want the capabilities", name, err)
	}
	return len(pi.requests())
}

// The factory asks GitLab's connection read, then GitHub's, then the Gitea
// family's, and answers the first family whose read establishes it, with that
// family's client and its connection capabilities already held. So each product is
// detected from its own captured answer, after one read of every family asked
// before it, and the caller's next connection read costs nothing.
func TestOpenDetectsEachProductFromItsCapturedAnswer(t *testing.T) {
	foreign := foreignReads(t)
	for name, product := range detectedProducts {
		t.Run(name, func(t *testing.T) {
			pi := newProductInstance(t, product, foreign[product.dir])
			core, family, err := families.Open(t.Context(), forgeapi.Connection{WebBaseURL: pi.srv.URL}, openOptions(pi.srv)...)
			if err != nil {
				t.Fatalf("families.Open on %s = error %v, want its family: %v", name, err, pi.requests())
			}
			if family != product.family || core == nil {
				t.Errorf("families.Open on %s = (%T, %v), want a client and %v", name, core, family, product.family)
			}
			sent := pi.requests()
			var asked []string
			for _, a := range foreign[product.dir] {
				asked = append(asked, a.Method+" "+a.Path)
			}
			if len(sent) < len(asked) || !slices.Equal(sent[:len(asked)], asked) {
				t.Errorf("families.Open on %s sent %v, want it to begin %v: one read of each family asked before this product's own, in order", name, sent, asked)
			}
			if want := len(asked) + ownCost(t, name, product, foreign[product.dir]); len(sent) != want || len(sent) > product.maximum {
				t.Errorf("families.Open on %s sent %d request(s) %v, want %d, at most %d: its own family's connection read and the reads ahead of it", name, len(sent), sent, want, product.maximum)
			}
			if core == nil {
				return
			}
			before := len(pi.requests())
			if _, err := core.ConnectionCaps(t.Context()); err != nil {
				t.Errorf("ConnectionCaps on the client families.Open answered for %s = error %v, want the capabilities it holds", name, err)
			}
			if after := pi.requests(); len(after) != before {
				t.Errorf("ConnectionCaps on the client families.Open answered for %s sent %v, want nothing: detection already read them", name, after[before:])
			}
		})
	}
}

// An instance that establishes no family is refused by name, after every family's
// read in the factory's order, carrying the status of the last answer it received.
func TestOpenRefusesAnInstanceThatMatchesNoFamilyAfterAskingEach(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.EscapedPath())
		mu.Unlock()
		w.Header().Set("Content-Type", "text/plain;charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		if _, err := io.WriteString(w, "Not found.\n"); err != nil {
			t.Errorf("Setup: writing the absence: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	core, family, err := families.Open(t.Context(), forgeapi.Connection{WebBaseURL: srv.URL}, openOptions(srv)...)
	if core != nil || family != forgeapi.FamilyUnknown {
		t.Errorf("families.Open on an instance of no family = (%T, %v), want no client and %v", core, family, forgeapi.FamilyUnknown)
	}
	fe, ok := errors.AsType[*forgeapi.Error](err)
	switch {
	case !ok:
		t.Fatalf("families.Open on an instance of no family = error %v, want a *forgeapi.Error", err)
	case fe.Code != forgeapi.CodeFamilyUndetected || fe.Family != forgeapi.FamilyUnknown:
		t.Errorf("families.Open on an instance of no family = (code %q, family %v), want (%q, %v)", fe.Code, fe.Family, forgeapi.CodeFamilyUndetected, forgeapi.FamilyUnknown)
	case fe.Status != http.StatusNotFound:
		t.Errorf("families.Open on an instance of no family = status %d, want %d, the last answer's", fe.Status, http.StatusNotFound)
	}
	want := []string{"GET /api/v4/metadata", "GET /api/v3/meta", "GET /api/v1/version"}
	mu.Lock()
	defer mu.Unlock()
	var firsts []string
	for _, req := range seen {
		if slices.Contains(want, req) && !slices.Contains(firsts, req) {
			firsts = append(firsts, req)
		}
	}
	if !slices.Equal(firsts, want) {
		t.Errorf("families.Open on an instance of no family sent %v, want each family's first read in the order %v", seen, want)
	}
}

// A context that ends during detection ends it with the context's own sentinel,
// whichever family's read it ended in, rather than reading the cut read as that
// family's absence and asking the next.
func TestOpenAnswersTheContextsSentinelWhereDetectionIsCancelled(t *testing.T) {
	reads := map[string]string{
		"in_GitLabs_read":                    "/api/v4/metadata",
		"in_GitHubs_read":                    "/api/v3/meta",
		"in_the_Gitea_familys_version_read":  "/api/v1/version",
		"in_the_Gitea_familys_document_read": "/swagger.v1.json",
	}
	for name, cutAt := range reads {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var mu sync.Mutex
			var seen []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := r.URL.EscapedPath()
				mu.Lock()
				seen = append(seen, path)
				mu.Unlock()
				switch path {
				case cutAt:
					cancel()
					select {
					case <-r.Context().Done():
					case <-time.After(10 * time.Second):
						t.Errorf("Setup: the request to %s outlived its cancelled context by ten seconds", path)
					}
					return
				case "/api/v1/version":
					w.Header().Set("Content-Type", "application/json")
					if _, err := io.WriteString(w, `{"version":"1.27.0"}`); err != nil {
						t.Errorf("Setup: writing the version: %v", err)
					}
					return
				}
				w.Header().Set("Content-Type", "text/plain;charset=utf-8")
				w.WriteHeader(http.StatusNotFound)
				if _, err := io.WriteString(w, "Not found.\n"); err != nil {
					t.Errorf("Setup: writing the absence: %v", err)
				}
			}))
			t.Cleanup(srv.Close)

			core, family, err := families.Open(ctx, forgeapi.Connection{WebBaseURL: srv.URL}, openOptions(srv)...)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("families.Open cancelled at %s = error %v, want %v", cutAt, err, context.Canceled)
			}
			if _, folded := errors.AsType[*forgeapi.Error](err); folded {
				t.Errorf("families.Open cancelled at %s = %v, a *forgeapi.Error, want the context's sentinel unchanged", cutAt, err)
			}
			if core != nil || family != forgeapi.FamilyUnknown {
				t.Errorf("families.Open cancelled at %s = (%T, %v), want no client and %v", cutAt, core, family, forgeapi.FamilyUnknown)
			}
			mu.Lock()
			defer mu.Unlock()
			if i := slices.Index(seen, cutAt); i < 0 || i != len(seen)-1 {
				t.Errorf("families.Open cancelled at %s sent %v, want that read last: nothing is asked once the context ended", cutAt, seen)
			}
		})
	}
}

// Every family is built from the one connection the caller named, so a connection
// a constructor refuses is refused before any request, with that refusal.
func TestOpenAnswersAConstructorsRefusalBeforeAnyRequest(t *testing.T) {
	reserved := forgeapi.ReservedHeaders()
	if len(reserved) == 0 {
		t.Fatal("Setup: ReservedHeaders() names no header")
	}
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.EscapedPath())
		mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	conn := forgeapi.Connection{WebBaseURL: srv.URL, Headers: []forgeapi.Header{{Name: reserved[0], Value: "example"}}}

	core, family, err := families.Open(t.Context(), conn, openOptions(srv)...)
	if core != nil || family != forgeapi.FamilyUnknown {
		t.Errorf("families.Open over a refused connection = (%T, %v), want no client and %v", core, family, forgeapi.FamilyUnknown)
	}
	if fe, ok := errors.AsType[*forgeapi.Error](err); !ok || fe.Code != forgeapi.CodeHeaderReserved {
		t.Errorf("families.Open over a connection carrying %s = error %v, want code %q: the constructor's refusal", reserved[0], err, forgeapi.CodeHeaderReserved)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 0 {
		t.Errorf("families.Open over a refused connection sent %v, want nothing", seen)
	}
}

// TestTheDetectionFixtureRecordsEveryProductsForeignReads holds the recorded
// answers to the order the factory asks in: every product records one answer per
// family asked before its own, and none answers another family's witness header.
func TestTheDetectionFixtureRecordsEveryProductsForeignReads(t *testing.T) {
	foreign := foreignReads(t)
	ahead := map[forgeapi.Family][]string{
		forgeapi.FamilyGitLab: nil,
		forgeapi.FamilyGitHub: {"GET /api/v4/metadata"},
		forgeapi.FamilyGitea:  {"GET /api/v4/metadata", "GET /api/v3/meta"},
	}
	witness := map[string]string{"GET /api/v4/metadata": "X-Gitlab-Meta", "GET /api/v3/meta": "X-Github-Request-Id"}
	for name, product := range detectedProducts {
		var got []string
		for _, a := range foreign[product.dir] {
			got = append(got, a.Method+" "+a.Path)
			for header := range a.Headers {
				if strings.EqualFold(header, witness[a.Method+" "+a.Path]) {
					t.Errorf("%s's recorded answer to %s %s carries %s, the witness of the family that read asks for", name, a.Method, a.Path, header)
				}
			}
		}
		if !slices.Equal(got, ahead[product.family]) {
			t.Errorf("the detection fixture records %v for %s, want %v: one answer per family asked before %v", got, name, ahead[product.family], product.family)
		}
	}
}
