package families_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/families"
	"github.com/cplieger/forgeapi/gitea"
	"github.com/cplieger/forgeapi/github"
	"github.com/cplieger/forgeapi/gitlab"
)

// connTracker is the last state of every connection an instance accepted, which is
// what shows whether a client released its pool: an idle keep-alive connection a
// client closes reaches StateClosed on the server's side.
type connTracker struct {
	state map[net.Conn]http.ConnState
	mu    sync.Mutex
}

func (ct *connTracker) record(c net.Conn, s http.ConnState) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.state[c] = s
}

// accepted is the number of connections the instance has accepted: a client whose
// pool was released dials a new one for its next request, a kept one reuses its own.
func (ct *connTracker) accepted() int {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return len(ct.state)
}

func (ct *connTracker) open() int {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	n := 0
	for _, s := range ct.state {
		if s != http.StateClosed && s != http.StateHijacked {
			n++
		}
	}
	return n
}

// settlesAt waits a bounded time for the instance to hold want connections open and
// answers the count it last saw.
func (ct *connTracker) settlesAt(want int) int {
	deadline := time.Now().Add(3 * time.Second)
	for {
		n := ct.open()
		if n == want || time.Now().After(deadline) {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// trackedInstance is one product answering as [newProductInstance]'s does, with every
// connection's state recorded. hold, where set, runs before each answer.
func trackedInstance(t *testing.T, product detectedProduct, hold func(*http.Request)) (*httptest.Server, *connTracker) {
	t.Helper()
	own := connectionRead(t, product.dir)
	foreign := foreignReads(t)[product.dir]
	ct := &connTracker{state: map[net.Conn]http.ConnState{}}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20)); err != nil {
			t.Errorf("Setup: reading the request body: %v", err)
		}
		if hold != nil {
			hold(r)
		}
		path := r.URL.EscapedPath()
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
	srv.Config.ConnState = ct.record
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, ct
}

// ownPoolOptions build a client over the library's own transport, whose pool is the
// one under test, with the two statements a loopback plaintext instance needs.
func ownPoolOptions() []forgeapi.Option {
	return []forgeapi.Option{
		forgeapi.WithCredentialSource(openCredential{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
}

// A caller never holds the clients of the families detection asked and rejected, so
// Open releases them itself: on a Gitea instance the GitLab and GitHub candidates'
// connections close, and the answered client's stays open for its next call.
func TestOpenReleasesTheClientsOfTheFamiliesItRejects(t *testing.T) {
	srv, conns := trackedInstance(t, detectedProducts["Gitea"], nil)
	_, family, err := families.Open(t.Context(), forgeapi.Connection{WebBaseURL: srv.URL}, ownPoolOptions()...)
	if err != nil || family != forgeapi.FamilyGitea {
		t.Fatalf("families.Open on a Gitea instance = (%v, %v), want the Gitea family", family, err)
	}
	if n := conns.settlesAt(1); n != 1 {
		t.Errorf("families.Open on a Gitea instance left %d connections open, want 1, the answered client's", n)
	}
}

// The client Open answers keeps its pool: its next request reuses the connection
// detection left idle rather than dialing a new one.
func TestOpenKeepsThePoolOfTheClientItAnswers(t *testing.T) {
	srv, conns := trackedInstance(t, detectedProducts["Gitea"], nil)
	core, _, err := families.Open(t.Context(), forgeapi.Connection{WebBaseURL: srv.URL}, ownPoolOptions()...)
	if err != nil {
		t.Fatalf("Setup: families.Open on a Gitea instance = %v", err)
	}
	if n := conns.settlesAt(1); n != 1 {
		t.Fatalf("Setup: families.Open on a Gitea instance left %d connections open, want 1", n)
	}
	before := conns.accepted()
	_, _ = core.Whoami(t.Context())
	if after := conns.accepted(); after != before {
		t.Errorf("Whoami on the client families.Open answered dialed %d new connection(s), want 0: its pool was released", after-before)
	}
}

// bodyCloseHook calls after, once, when the body of a response to path closes, which
// is the point a read has answered and its connection is back in the pool.
func bodyCloseHook(path string, after func()) func(http.RoundTripper) http.RoundTripper {
	return func(next http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			resp, err := next.RoundTrip(r)
			if err == nil && r.URL.EscapedPath() == path {
				resp.Body = &hookedBody{ReadCloser: resp.Body, after: after}
			}
			return resp, err
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type hookedBody struct {
	io.ReadCloser
	after func()
}

func (b *hookedBody) Close() error {
	err := b.ReadCloser.Close()
	b.after()
	return err
}

// The caller's context ending after a family's read has answered still answers its
// sentinel and no client, and that family's client, whose connection is idle in its
// pool rather than torn down with a cancelled request, is released as well.
func TestOpenReleasesTheClientWhoseReadAnsweredBeforeTheCallersContextEnded(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv, conns := trackedInstance(t, detectedProducts["Gitea"], nil)
	opts := append(ownPoolOptions(), forgeapi.WithRequestObserver(bodyCloseHook("/api/v3/meta", cancel)))
	core, _, err := families.Open(ctx, forgeapi.Connection{WebBaseURL: srv.URL}, opts...)
	if !errors.Is(err, context.Canceled) || core != nil {
		t.Fatalf("families.Open with the context ended after GitHub's read answered = (%v, %v), want no client and context.Canceled", core, err)
	}
	if n := conns.settlesAt(0); n != 0 {
		t.Errorf("families.Open whose context ended after GitHub's read answered left %d connections open, want 0", n)
	}
}

// The caller's context ending mid-detection answers its sentinel and no client, so
// every client Open built by then is released before it returns.
func TestOpenReleasesEveryClientItBuiltWhenTheCallersContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv, conns := trackedInstance(t, detectedProducts["Gitea"], func(r *http.Request) {
		if r.URL.EscapedPath() == "/api/v3/meta" {
			cancel()
			<-r.Context().Done()
		}
	})
	core, _, err := families.Open(ctx, forgeapi.Connection{WebBaseURL: srv.URL}, ownPoolOptions()...)
	if !errors.Is(err, context.Canceled) || core != nil {
		t.Fatalf("families.Open with the context ended during GitHub's read = (%v, %v), want no client and context.Canceled", core, err)
	}
	if n := conns.settlesAt(0); n != 0 {
		t.Errorf("families.Open whose context ended left %d connections open, want 0", n)
	}
}

// familyClients are the three family constructors, each paired with the product its
// connection read is recorded on.
var familyClients = []struct {
	build   func(forgeapi.Connection, ...forgeapi.Option) (forgeapi.Core, error)
	product string
}{
	{product: "GitLab", build: func(c forgeapi.Connection, o ...forgeapi.Option) (forgeapi.Core, error) { return gitlab.New(c, o...) }},
	{product: "GitHub", build: func(c forgeapi.Connection, o ...forgeapi.Option) (forgeapi.Core, error) { return github.New(c, o...) }},
	{product: "Gitea", build: func(c forgeapi.Connection, o ...forgeapi.Option) (forgeapi.Core, error) { return gitea.New(c, o...) }},
}

// Close is a client's disconnect obligation: it releases the connection's pool, so the
// keep-alive connection a read left idle closes.
func TestCloseReleasesTheClientsPool(t *testing.T) {
	for _, family := range familyClients {
		t.Run(family.product, func(t *testing.T) {
			srv, conns := trackedInstance(t, detectedProducts[family.product], nil)
			client, err := family.build(forgeapi.Connection{WebBaseURL: srv.URL}, ownPoolOptions()...)
			if err != nil {
				t.Fatalf("Setup: %s's client: %v", family.product, err)
			}
			if _, err := client.ConnectionCaps(t.Context()); err != nil {
				t.Fatalf("Setup: %s's connection read = %v, want the capabilities", family.product, err)
			}
			if n := conns.settlesAt(1); n != 1 {
				t.Fatalf("Setup: %s's read left %d connections open, want its one idle connection", family.product, n)
			}
			client.Close()
			if n := conns.settlesAt(0); n != 0 {
				t.Errorf("%s Close left %d connections open, want 0", family.product, n)
			}
		})
	}
}

// Close adds no closed state: a call after it dials afresh, and a further Close
// releases that connection as well.
func TestACallAfterCloseDialsAfresh(t *testing.T) {
	for _, family := range familyClients {
		t.Run(family.product, func(t *testing.T) {
			srv, conns := trackedInstance(t, detectedProducts[family.product], nil)
			client, err := family.build(forgeapi.Connection{WebBaseURL: srv.URL}, ownPoolOptions()...)
			if err != nil {
				t.Fatalf("Setup: %s's client: %v", family.product, err)
			}
			client.Close()
			if _, err := client.ConnectionCaps(t.Context()); err != nil {
				t.Fatalf("%s ConnectionCaps after Close = %v, want the capabilities", family.product, err)
			}
			client.Close()
			if n := conns.settlesAt(0); n != 0 {
				t.Errorf("%s second Close left %d connections open, want 0", family.product, n)
			}
		})
	}
}

// The client Open answers is a Core, and Core carries Close, so a caller that
// detected its family releases what it holds without knowing which family that is.
func TestTheClientOpenAnswersReleasesItsPoolWithClose(t *testing.T) {
	srv, conns := trackedInstance(t, detectedProducts["Gitea"], nil)
	core, _, err := families.Open(t.Context(), forgeapi.Connection{WebBaseURL: srv.URL}, ownPoolOptions()...)
	if err != nil {
		t.Fatalf("Setup: families.Open on a Gitea instance = %v", err)
	}
	core.Close()
	if n := conns.settlesAt(0); n != 0 {
		t.Errorf("Close on the client families.Open answered left %d connections open, want 0", n)
	}
}
