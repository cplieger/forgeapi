package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// TestARevalidatedReadAnswersWhatTheFirstReadAnswered is the projection property of
// the read cache over every read operation on every product: the cache keeps a
// family's decoded projection of a body rather than the body, so a read answered
// from a not-modified exchange must answer exactly what the read that kept it
// answered, at a price the table publishes once a free not-modified answer is
// taken off (spec.Revalidations).
//
// The server serves each case's recorded fixture with an entity tag over the body
// added to every GET answer, and answers 304 to a request replaying it (RFC 9110
// section 13.1.2), whatever product recorded it. A GitHub 304 carries no Link and
// no X-OAuth-Scopes, as spec.Revalidations measures.
func TestARevalidatedReadAnswersWhatTheFirstReadAnswered(t *testing.T) {
	for _, e := range spec.Table {
		op, ok := operationFor(e.Method)
		if !ok || op.mutation || pending(e) || refuses(e) || e.Method == "Governor.BudgetState" {
			continue
		}
		t.Run(caseName(e), func(t *testing.T) {
			runRevalidated(t, e, op)
		})
	}
}

func runRevalidated(t *testing.T, e spec.Entry, op operation) {
	t.Helper()
	f, err := loadFixture(e.Product, e.Method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	srv, rec, notModified := newRevalidatingServer(t, e.Product, f)
	conn := forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL + apiRoot(e.Product)}
	client, err := guard(func() (any, error) { return newClient(e.Product, conn, offlineOptions(srv)...) })
	if err != nil {
		t.Fatalf("Setup: %s client: %v", e.Product, err)
	}
	s := canonicalSubject(e.Product)
	if prelude := preludeMethod(e, op); prelude != "" {
		runPrelude(t, e, prelude, client, s)
	}
	first, err := guard(func() (any, error) { return op.invoke(t.Context(), client, s) })
	if err != nil {
		t.Fatalf("the first read = %v, want nil", err)
	}
	rec.reset()
	before, freeBefore := rec.count(), notModified.Load()
	second, err := guard(func() (any, error) { return op.invoke(t.Context(), client, s) })
	if err != nil {
		t.Fatalf("the revalidated read = %v, want nil", err)
	}
	answered := notModified.Load() - freeBefore
	billed := rec.count() - before
	if notModifiedPrice(e.Product) == 0 {
		billed -= int(answered)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("%s on %s answered\n\t%+v\nafter a not-modified exchange, want what the read that kept it answered\n\t%+v", e.Method, e.Product, second, first)
	}
	items := 1
	if op.items != nil {
		items = op.items(second)
	}
	if low, high := price(&e, items); billed < low || billed > high {
		t.Errorf("%s on %s billed %d request(s) revalidated (%d not-modified), want %d to %d", e.Method, e.Product, billed, answered, low, high)
	}
	if rec.gets() > 0 && answered == 0 && revalidates(&e) {
		t.Errorf("%s on %s sent %d GET(s) a second time and none was answered not-modified, want the held answers revalidated", e.Method, e.Product, rec.gets())
	}
}

// revalidates reports whether the table expects a cell's repeated GETs to be
// revalidated: on a product whose 304 bills nothing, a cell priced from one request
// is read unconditionally (GitHub's identity read, whose scopes its 304 omits).
func revalidates(e *spec.Entry) bool {
	return notModifiedPrice(e.Product) != 0 || e.Requests.Min == 0
}

func notModifiedPrice(p spec.Product) int {
	for _, r := range spec.Revalidations {
		if r.Product == p {
			return r.NotModifiedPrice
		}
	}
	return 1
}

// revalidatingRecorder is the fixture recorder with the GETs since the last reset
// counted.
type revalidatingRecorder struct {
	*recorder
	get atomic.Int64
}

func (r *revalidatingRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spent = make([]int, len(r.spent))
	r.get.Store(0)
}

func (r *revalidatingRecorder) gets() int { return int(r.get.Load()) }

// newRevalidatingServer serves one fixture as newFixtureServer does, with an entity
// tag on every GET answer and a 304 to a request that replays it, carrying the
// headers product's own 304 carries.
func newRevalidatingServer(t *testing.T, product spec.Product, f fixture) (*httptest.Server, *revalidatingRecorder, *atomic.Int64) {
	t.Helper()
	rec := &revalidatingRecorder{recorder: &recorder{spent: make([]int, len(f.Routes))}}
	notModified := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxFixtureBody))
		if err != nil {
			body = nil
		}
		rec.record(sent{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.Query(), body: body})
		if r.Method == http.MethodGet {
			rec.get.Add(1)
		}
		route, ok := rec.take(f, r, body)
		if !ok {
			rec.miss(r.Method, r.URL.EscapedPath())
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"message":"no fixture route for %s %s"}`, r.Method, r.URL.EscapedPath())
			return
		}
		for k, v := range route.Headers {
			w.Header().Set(k, v)
		}
		if r.Method == http.MethodGet && route.Status == http.StatusOK {
			sum := sha256.Sum256(route.Body)
			tag := `W/"` + hex.EncodeToString(sum[:8]) + `"`
			w.Header().Set("ETag", tag)
			if r.Header.Get("If-None-Match") == tag {
				notModified.Add(1)
				if product == spec.GitHub {
					w.Header().Del("Link")
					w.Header().Del("X-OAuth-Scopes")
				}
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.WriteHeader(route.Status)
		if _, err := w.Write(route.Body); err != nil {
			t.Errorf("Setup: writing the fixture response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec, notModified
}
