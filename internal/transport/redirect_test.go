package transport

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/cplieger/forgeapi"
)

// The two origins a containment case needs. They share one PORT and differ in host,
// because the port allowlist would otherwise refuse the hop before the origin
// comparison this library's own policy exists to make, and Go's default policy gets
// exactly that comparison wrong: its match is the hostname alone.
type origins struct {
	named        *httptest.Server
	other        *httptest.Server
	namedSaw     *record
	otherSaw     *record
	otherAddress string
}

// record is what one origin observed.
type record struct {
	requests []string
	auth     []string
	custom   []string
	bodies   []string
	mu       sync.Mutex
}

func (r *record) see(req *http.Request, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req.Method+" "+req.URL.EscapedPath())
	r.auth = append(r.auth, req.Header.Get("Authorization"))
	r.custom = append(r.custom, req.Header.Get("X-Gateway-Secret"))
	r.bodies = append(r.bodies, body)
}

func (r *record) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func (r *record) last() (auth, custom, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) == 0 {
		return "", "", ""
	}
	i := len(r.requests) - 1
	return r.auth[i], r.custom[i], r.bodies[i]
}

// twoOrigins starts two servers on one port at two loopback hosts. The named one
// answers the redirect the case is about; the other one records whatever reaches it,
// which for a refused hop must be nothing at all.
func twoOrigins(t *testing.T, redirect func(*record) http.Handler) *origins {
	t.Helper()
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Setup: listening on 127.0.0.1: %v", err)
	}
	port := strconv.Itoa(first.Addr().(*net.TCPAddr).Port)
	second, err := net.Listen("tcp", net.JoinHostPort("127.0.0.2", port))
	if err != nil {
		if cerr := first.Close(); cerr != nil {
			t.Errorf("Setup: closing the first listener: %v", cerr)
		}
		t.Skipf("a second loopback host on port %s is unavailable here: %v", port, err)
	}

	o := &origins{namedSaw: &record{}, otherSaw: &record{}, otherAddress: net.JoinHostPort("127.0.0.2", port)}
	o.named = httptest.NewUnstartedServer(redirect(o.namedSaw))
	o.named.Listener = first
	o.named.Start()
	t.Cleanup(o.named.Close)

	o.other = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.otherSaw.see(r, readBody(t, r))
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"login":"other-origin"}`); err != nil {
			t.Errorf("Setup: writing the other origin's answer: %v", err)
		}
	}))
	o.other.Listener = second
	o.other.Start()
	t.Cleanup(o.other.Close)
	return o
}

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		t.Errorf("Setup: reading a request body: %v", err)
	}
	return string(body)
}

// TestCredentialsAndConsumerHeadersAreDroppedOffOrigin holds the drop Go's own
// default policy does not make: its same-host test is the hostname alone, so a
// subdomain change, a scheme downgrade and a port change all forward every sensitive
// header. This policy compares scheme, host and port together.
func TestCredentialsAndConsumerHeadersAreDroppedOffOrigin(t *testing.T) {
	o := twoOrigins(t, func(saw *record) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			saw.see(r, readBody(t, r))
			http.Redirect(w, r, "http://"+addressOfOther(r)+"/api/v1/user", http.StatusFound)
		})
	})
	conn := connWithHeaders(o.named.URL)
	c := openTestConn(t, conn)
	if _, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
		t.Fatalf("a read across an off-origin hop = %v, want nil", err)
	}
	if auth, custom, _ := o.namedSaw.last(); auth == "" || custom == "" {
		t.Errorf("the named instance saw Authorization %q and X-Gateway-Secret %q, want both: a header only reaches the instance the connection names", auth, custom)
	}
	auth, custom, _ := o.otherSaw.last()
	if auth != "" {
		t.Errorf("the other origin saw Authorization %q, want none", auth)
	}
	if custom != "" {
		t.Errorf("the other origin saw X-Gateway-Secret %q, want none: a consumer header is dropped on a differing hop exactly as the credential is", custom)
	}
}

// TestAMutatingRequestIsNotReplayedOffOrigin holds the hop the rewrite refusal cannot
// reach.
//
// A 307 and a 308 carry the method AND the body forward, so following one off origin
// sends the caller's creation, merge or close to a host the consumer never named and
// returns that host's answer as the operation's own. Dropping the credential is not
// enough there: the request itself is the thing that must not travel.
func TestAMutatingRequestIsNotReplayedOffOrigin(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			o := twoOrigins(t, func(saw *record) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					saw.see(r, readBody(t, r))
					http.Redirect(w, r, "http://"+addressOfOther(r)+"/api/v1/repos/example/example/issues", status)
				})
			})
			c := openTestConn(t, connWithHeaders(o.named.URL))
			_, err := c.Do(t.Context(), &Request{
				Op: "CreateIssue", Method: http.MethodPost,
				Path: "/repos/example/example/issues", Body: map[string]any{"title": "t"},
			})
			var fe *forgeapi.Error
			if !asError(err, &fe) {
				t.Fatalf("a mutation across an off-origin %d = %v, want a refusal", status, err)
			}
			if fe.Code != forgeapi.CodeRepoRefStale {
				t.Errorf("the refusal = code %q, want %q", fe.Code, forgeapi.CodeRepoRefStale)
			}
			if o.otherSaw.count() != 0 {
				_, _, body := o.otherSaw.last()
				t.Errorf("the other origin saw %d request(s) carrying %q, want none: the caller's mutation must not be replayed at a host the consumer never named", o.otherSaw.count(), body)
			}
		})
	}
}

// TestARewritableHopIsRefused holds the other half of that rule: net/http downgrades
// a mutation to a bodyless read across these three statuses, so following one would
// answer 2xx from a read that merged nothing and the caller would read that as a
// merge.
func TestARewritableHopIsRefused(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			o := twoOrigins(t, func(saw *record) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					saw.see(r, readBody(t, r))
					http.Redirect(w, r, "http://"+addressOfOther(r)+"/api/v1/repos/example/example/issues", status)
				})
			})
			c := openTestConn(t, connWithHeaders(o.named.URL))
			_, err := c.Do(t.Context(), &Request{
				Op: "CreateIssue", Method: http.MethodPost,
				Path: "/repos/example/example/issues", Body: map[string]any{"title": "t"},
			})
			var fe *forgeapi.Error
			if !asError(err, &fe) {
				t.Fatalf("a mutation across a %d = %v, want a refusal", status, err)
			}
			if fe.Code != forgeapi.CodeRepoRefStale {
				t.Errorf("the refusal = code %q, want %q", fe.Code, forgeapi.CodeRepoRefStale)
			}
		})
	}
}

// A refused hop is the one failure answered beside a response: the answer that asked
// for the hop and the address it would have gone to, which is where a family reads a
// moved repository's successor off a write it did not send again. A failure no
// answer arrived for has no such response.
func TestARefusedHopIsAnsweredBesideItsRefusalWithWhereItWouldHaveGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/api/v1/repos/example/example-moved/issues/1", http.StatusMovedPermanently)
	}))
	defer srv.Close()
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithMutations(true), forgeapi.WithRetries(0))

	resp, err := c.Do(t.Context(), &Request{
		Op: "CloseIssue", Method: http.MethodPatch,
		Path: "/repos/example/example/issues/1", Body: map[string]any{"state": "closed"},
	})

	var fe *forgeapi.Error
	if !asError(err, &fe) || fe.Code != forgeapi.CodeRepoRefStale {
		t.Fatalf("Do(PATCH) across a 301 = error %v, want code %q", err, forgeapi.CodeRepoRefStale)
	}
	if resp == nil {
		t.Fatal("Do(PATCH) across a 301 = no response, want the answer that asked for the refused hop")
	}
	if want := srv.URL + "/api/v1/repos/example/example-moved/issues/1"; resp.Refused != want {
		t.Errorf("Do(PATCH) across a 301 = Refused %q, want %q: the location resolved against the request it answered", resp.Refused, want)
	}
	if resp.Status != http.StatusMovedPermanently {
		t.Errorf("Do(PATCH) across a 301 = Status %d, want %d", resp.Status, http.StatusMovedPermanently)
	}

	srv.Close()
	resp, err = c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	if err == nil || resp != nil {
		t.Errorf("Do(GET) on a closed listener = (%+v, %v), want a failure and no response: no answer arrived", resp, err)
	}
}

// TestEveryRedirectRefusalCarriesItsOwnCode holds the field a consumer branches on.
//
// The policy builds a correct refusal, net/http wraps whatever a policy returns in
// *url.Error, and a mapper that stringified that chain would hand the caller an empty
// code, a transient kind and a retryable true: the four refusal codes the policy owns
// would be unreachable, and a permanent containment refusal would be reported as
// something worth retrying, which is exactly the failure mode having its own code
// exists to avoid.
func TestEveryRedirectRefusalCarriesItsOwnCode(t *testing.T) {
	for _, test := range []struct {
		name     string
		location func(*http.Request) string
		code     string
	}{
		{
			name:     "a_chain_over_the_hop_cap",
			location: func(r *http.Request) string { return "http://" + r.Host + "/api/v1/user" },
			code:     forgeapi.CodeRedirectHopCap,
		},
		{
			name: "a_hop_outside_the_port_allowlist",
			location: func(r *http.Request) string {
				return "http://" + net.JoinHostPort(hostOnly(r.Host), "9") + "/api/v1/user"
			},
			code: forgeapi.CodeRedirectPortRefused,
		},
		{
			name:     "a_hop_with_no_host",
			location: func(*http.Request) string { return "http:///api/v1/user" },
			code:     forgeapi.CodeConnectionInvalid,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, test.location(r), http.StatusFound)
			}))
			defer srv.Close()
			spy := newCounterSpy()
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithCounters(spy.counters()))
			_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
			var fe *forgeapi.Error
			if !asError(err, &fe) {
				t.Fatalf("the refused chain = %v, want a *forgeapi.Error", err)
			}
			if fe.Code != test.code {
				t.Errorf("the refusal = code %q, want %q: a refusal whose code the caller cannot read is one it cannot branch on", fe.Code, test.code)
			}
			if fe.Retryable {
				t.Error("the refusal = retryable true, want false: a containment refusal is permanent, and reporting it as transient tells the caller to retry it")
			}
			if fe.Kind == forgeapi.KindTransient {
				t.Errorf("the refusal = kind %v, want a kind that is not transient", fe.Kind)
			}
			if fe.Op != "Whoami" {
				t.Errorf("the refusal = op %q, want %q: the operation is filled in by the request that made it", fe.Op, "Whoami")
			}
		})
	}
}

// TestADowngradeIsRefusedWhateverThePlaintextStatement holds the one refusal that
// does not depend on the connection's own posture: a downgrade mid-request is not a
// deployment shape anyone chose.
func TestADowngradeIsRefusedWhateverThePlaintextStatement(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/api/v1/user", http.StatusFound)
	}))
	defer srv.Close()
	c, err := Open(&forgeapi.Connection{WebBaseURL: srv.URL}, settingsFor(
		forgeapi.WithWireTransport(srv.Client().Transport),
		forgeapi.WithCredentialSource(stubCredential{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(discardLogger()),
	), testOptions())
	if err != nil {
		t.Fatalf("Setup: Open(%q): %v", srv.URL, err)
	}
	_, err = c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("a downgrade hop = %v, want a refusal", err)
	}
	if fe.Code != forgeapi.CodePlaintextRefused {
		t.Errorf("the refusal = code %q, want %q", fe.Code, forgeapi.CodePlaintextRefused)
	}
}

// addressOfOther is the other loopback host at the port the request arrived on.
func addressOfOther(r *http.Request) string {
	return net.JoinHostPort("127.0.0.2", portOnly(r.Host))
}

func portOnly(hostport string) string {
	_, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return "80"
	}
	return port
}

func hostOnly(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport
	}
	return host
}

// connWithHeaders is a connection carrying one consumer header, so a hop's drop is
// observable on a name this library does not write itself.
func connWithHeaders(base string) forgeapi.Connection {
	return forgeapi.Connection{
		WebBaseURL: base,
		Headers:    []forgeapi.Header{{Name: "X-Gateway-Secret", Value: "gateway-literal"}},
	}
}

// stubCredential is the mandatory credential source, holding a placeholder.
type stubCredential struct{}

func (stubCredential) Token(context.Context) (string, error) { return "transport-placeholder", nil }
func (stubCredential) Kind() forgeapi.CredKind               { return forgeapi.CredKindStaticPAT }
func (stubCredential) State() forgeapi.CredState             { return forgeapi.CredValid }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// asError reports whether err is this library's own error type.
func asError(err error, out **forgeapi.Error) bool {
	fe, ok := err.(*forgeapi.Error)
	if !ok {
		return false
	}
	*out = fe
	return true
}
