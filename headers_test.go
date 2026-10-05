package forgeapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"slices"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/gitea"
)

// The three writer sets whose UNION is [forgeapi.ReservedHeaders], split by who
// writes each name and by whether this tree can be observed writing it yet.
//
// Nothing derives them from the source, because proving that NO code anywhere in a
// module writes a header is an unbounded claim over Go's syntax. The observation
// below reads an OUTPUT instead, which cannot have a blind spot: a header reaching
// the server is in the arriving set by construction.
var (
	// libraryHeadersOnEveryRequest are the names this library's own request
	// builder writes on every request it makes, the credential injection among
	// them, so deleting that injection moves the observed set.
	libraryHeadersOnEveryRequest = []string{"Accept", "Authorization", "User-Agent"}
	// libraryHeadersOnARequestWithABody is what it adds where the request
	// carries one.
	libraryHeadersOnARequestWithABody = []string{"Content-Type"}
	// libraryHeadersOneFamilySendsUnderItsOwnPrecondition is the API-version pin,
	// which the GitHub family sends and this observation cannot reach: it rides
	// every request on a connection whose instance has ECHOED the version this
	// library pins, and nothing else, so an observation of the first request to a
	// server that has echoed nothing correctly sees none. It is observable, and
	// observed, at that family's own grain, in both arms: github's
	// TestTheVersionPinRidesEveryRequestOnceTheInstanceEchoedIt reads it arriving on
	// the second request and absent from the first, and
	// TestTheVersionPinIsWithheldFromAnInstanceThatNamedAnotherVersion reads it
	// withheld from an instance answering under another version.
	libraryHeadersOneFamilySendsUnderItsOwnPrecondition = []string{"X-GitHub-Api-Version"}
	// libraryHeadersNothingHereEverWrites are the two request validators, reserved
	// and permanently unobservable, which is the unconditional-read ruling's consequence
	// for this list:
	// conditional requests are a transport layer this library does not build, so no
	// read of its own carries one and no family can make it arrive. They stay
	// reserved because a validator a CONSUMER set would be answered with a
	// not-modified status this library maps as an unexpected one, so the header
	// could only ever break the read it was set on.
	libraryHeadersNothingHereEverWrites = []string{
		"If-Modified-Since",
		"If-None-Match",
	}
)

// stackHeadersThatArrive and stackHeadersThatNeverStick are the other half of
// [forgeapi.ReservedHeaders]: what Go's HTTP stack controls itself, whatever the
// request carries. Both are facts about net/http rather than about this module.
//
// That half is THREE PLUS TWO rather than a count of five, and the split is measured
// on go1.27 by the test below. Accept-Encoding, Content-Length and User-Agent ARRIVE
// as header-map entries, so the observation holds them directly. The other three
// never arrive as headers at all, and all three are named because the reason differs
// for one of them: Host and Transfer-Encoding reach a server as r.Host and
// r.TransferEncoding, which the test witnesses positively, while Connection reaches
// it as neither and is reserved by its stated reason alone, a hop-by-hop name the
// stack owns. A caller setting any of the three would be silently ignored rather
// than honoured, which is what [forgeapi.CodeHeaderReserved] exists to refuse.
var (
	stackHeadersThatArrive     = []string{"Accept-Encoding", "Content-Length", "User-Agent"}
	stackHeadersThatNeverStick = []string{"Connection", "Host", "Transfer-Encoding"}
)

// headersBothHalvesWrite is the OVERLAP between the library's names and the stack's,
// because one name is written by both: net/http supplies a default User-Agent on a
// request that carries none, and this library writes its own. So the slices are
// overlapping writer sets whose union is the reserved list, never a partition of it.
//
// It is a variable rather than a remark because canonical() compacts: without an
// assertion of its own the overlap collapses before the union check, and a name
// deleted from one half is then a green run. Measured on the tree that carried only
// the union check: deleting User-Agent from the library's half left the suite green.
var headersBothHalvesWrite = []string{"User-Agent"}

// probeCredential is the credential source the observation drives the client with.
// Injection is mandatory, and the token is what the arriving Authorization header is
// built from, so a builder that stops injecting it moves the observed set.
type probeCredential struct{}

func (probeCredential) Token(context.Context) (string, error) { return "observation-placeholder", nil }
func (probeCredential) Kind() forgeapi.CredKind               { return forgeapi.CredKindStaticPAT }
func (probeCredential) State() forgeapi.CredState             { return forgeapi.CredValid }

// TestReservedHeadersHoldTheHeadersThatArrive drives real requests through THIS
// LIBRARY's own request builder to an httptest server and holds
// [forgeapi.ReservedHeaders] to the header set that ARRIVES, which is the one
// instrument that can hold both halves of that list at once.
//
// What it proves, stated as a union rather than as a partition because one header is
// written by both halves. The reserved list is exactly the union of the writer sets
// this file names, with no third name and none missing; the overlap between them is
// exactly headersBothHalvesWrite, which is what holds each half's membership once
// canonical() has compacted the union. The set that arrives at the instance is
// exactly the library's own names for that request shape plus the stack's, so
// deleting the credential injection, the accept header or the consumer-header loop
// from the builder fails this test rather than passing quietly. Two of the three
// names that never arrive as headers reach the server through their own fields,
// which is what makes "a caller setting this would be ignored" a measured claim.
//
// What it still cannot prove, and why that is a smaller gap than it was: the
// reserved names no request here carries are classified by the builder that would
// write them rather than by an arrival. They are named in two variables, and the
// split is the whole of what is left open. The API-version pin's is
// libraryHeadersOneFamilySendsUnderItsOwnPrecondition, and that variable's comment
// names the two cases in the github package that observe it arriving and being
// withheld: it cannot arrive HERE, because its precondition is a version echo this
// server never sends. The two validators' is
// libraryHeadersNothingHereEverWrites, which never empties, because conditional
// requests are a layer this library does not build; their membership rests
// on the refusal being the right answer to a consumer-set validator rather than on
// any request of ours.
func TestReservedHeadersHoldTheHeadersThatArrive(t *testing.T) {
	reserved := canonical(forgeapi.ReservedHeaders())
	union := canonical(slices.Concat(
		libraryHeadersOnEveryRequest,
		libraryHeadersOnARequestWithABody,
		libraryHeadersOneFamilySendsUnderItsOwnPrecondition,
		libraryHeadersNothingHereEverWrites,
		stackHeadersThatArrive,
		stackHeadersThatNeverStick,
	))
	if !slices.Equal(reserved, union) {
		t.Errorf("ReservedHeaders() = %v, want %v: the reserved list is the UNION of the writer sets this file names, every entry written by this library or by Go's HTTP stack or by both", reserved, union)
	}
	if overlap, want := headersInBothHalves(), canonical(headersBothHalvesWrite); !slices.Equal(overlap, want) {
		t.Errorf("the names this library and the stack both write = %v, want %v: the union above compacts, so without this a name leaving one half changes nothing there", overlap, want)
	}

	for _, test := range []struct {
		name string
		call func(context.Context, *gitea.Client) error
		// wantArriving is the canonical header set the server must see, in
		// full: an extra name is a header nothing on the reserved list
		// accounts for, and a missing one is a name the builder stopped
		// sending. Both cases DERIVE it from the slices above rather than
		// restating them beside themselves.
		wantArriving []string
	}{
		{
			name: "a_read_carries_no_body",
			call: func(ctx context.Context, c *gitea.Client) error {
				_, err := c.Whoami(ctx)
				return err
			},
			wantArriving: slices.Concat(
				libraryHeadersOnEveryRequest,
				headersWithout(stackHeadersThatArrive, "Content-Length"),
			),
		},
		{
			name: "a_mutation_carries_one",
			call: func(ctx context.Context, c *gitea.Client) error {
				_, err := c.CreateIssue(ctx, repoRefForObservation(), forgeapi.NewIssue{Title: "t"})
				return err
			},
			wantArriving: slices.Concat(
				libraryHeadersOnEveryRequest,
				libraryHeadersOnARequestWithABody,
				stackHeadersThatArrive,
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var (
				arrived  []string
				host     string
				transfer []string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				names := make([]string, 0, len(r.Header))
				for name := range r.Header {
					names = append(names, name)
				}
				arrived = canonical(names)
				host = r.Host
				transfer = r.TransferEncoding
				w.Header().Set("Content-Type", "application/json")
				if _, err := io.WriteString(w, `{"login":"observed"}`); err != nil {
					t.Errorf("Setup: writing the response: %v", err)
				}
			}))
			defer srv.Close()

			client, err := gitea.New(forgeapi.Connection{WebBaseURL: srv.URL},
				forgeapi.WithWireTransport(srv.Client().Transport),
				forgeapi.WithCredentialSource(probeCredential{}),
				forgeapi.WithPlaintextHTTP(true),
				forgeapi.WithPrivateAddresses(true),
				forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
			)
			if err != nil {
				t.Fatalf("Setup: gitea.New(%q): %v", srv.URL, err)
			}
			if err := test.call(t.Context(), client); err != nil {
				t.Fatalf("Setup: driving one request through the library's own builder: %v", err)
			}

			if !slices.Equal(arrived, canonical(test.wantArriving)) {
				t.Errorf("headers arriving at the instance = %v, want %v: this is the derived set the reserved list is held to, so a difference is either a name to add to that list or a builder that stopped writing one", arrived, canonical(test.wantArriving))
			}
			for _, name := range arrived {
				if !slices.Contains(reserved, name) {
					t.Errorf("header %q arrives at the instance and ReservedHeaders() does not hold it: a consumer could set it through Connection.Headers and have it silently overridden, which is what the denylist exists to refuse", name)
				}
			}
			for _, name := range stackHeadersThatNeverStick {
				if key := textproto.CanonicalMIMEHeaderKey(name); slices.Contains(arrived, key) {
					t.Errorf("header %q arrives as a header-map entry, want it not to: it is on the reserved list because the stack consumes it rather than sending it, and a name that does arrive is held by the equality above instead", name)
				}
			}
			if host == "" {
				t.Error("r.Host is empty, want the request's authority: Host is on the reserved list because it reaches a server through this field rather than as a header, and a caller setting the header would be ignored")
			}
			if slices.Contains(transfer, "chunked") {
				t.Errorf("r.TransferEncoding = %v, want no chunked entry: this library holds a request body rather than streaming it, so the length is always known", transfer)
			}
		})
	}
}

// TestAConsumerHeaderArrivesBesideTheLibrarysOwn holds the other half of the
// denylist's purpose: a name the list does NOT hold passes through to the instance
// the connection names, which is what makes the list a denylist rather than an
// allowlist.
func TestAConsumerHeaderArrivesBesideTheLibrarysOwn(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"login":"observed"}`); err != nil {
			t.Errorf("Setup: writing the response: %v", err)
		}
	}))
	defer srv.Close()

	conn := forgeapi.Connection{
		WebBaseURL: srv.URL,
		Headers:    []forgeapi.Header{{Name: "X-Gateway-Secret", Value: "gateway-literal"}},
	}
	client, err := gitea.New(conn,
		forgeapi.WithWireTransport(srv.Client().Transport),
		forgeapi.WithCredentialSource(probeCredential{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err != nil {
		t.Fatalf("Setup: gitea.New(%q): %v", srv.URL, err)
	}
	if _, err := client.Whoami(t.Context()); err != nil {
		t.Fatalf("Setup: Whoami: %v", err)
	}
	if want := "gateway-literal"; got.Get("X-Gateway-Secret") != want {
		t.Errorf("X-Gateway-Secret arriving at the instance = %q, want %q: a name the denylist does not hold passes through", got.Get("X-Gateway-Secret"), want)
	}
	if got.Get("Authorization") == "" {
		t.Error("Authorization arriving at the instance is empty, want the credential: the consumer's headers are applied after it and must not displace it")
	}
}

// TestAConnectionMutatedAfterConstructionDoesNotChangeWhatIsSent holds the header
// list to being COPIED rather than referenced, the way the CA and key material is: a
// caller keeps the backing array of the slice it passed, and an entry rewritten there
// after the constructor validated it would otherwise reach the wire under a name the
// constructor refuses, applied after the credential and so able to replace it.
func TestAConnectionMutatedAfterConstructionDoesNotChangeWhatIsSent(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"login":"observed"}`); err != nil {
			t.Errorf("Setup: writing the response: %v", err)
		}
	}))
	defer srv.Close()

	headers := []forgeapi.Header{{Name: "X-Allowed", Value: "allowed"}}
	client, err := gitea.New(forgeapi.Connection{WebBaseURL: srv.URL, Headers: headers},
		forgeapi.WithWireTransport(srv.Client().Transport),
		forgeapi.WithCredentialSource(probeCredential{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err != nil {
		t.Fatalf("Setup: gitea.New(%q): %v", srv.URL, err)
	}
	headers[0] = forgeapi.Header{Name: "Authorization", Value: "token replacement"}
	if _, err := client.Whoami(t.Context()); err != nil {
		t.Fatalf("Setup: Whoami: %v", err)
	}
	if got.Get("Authorization") == "token replacement" {
		t.Error("Authorization arriving at the instance is the value written into the caller's slice after construction, want the credential's own: the connection's header list is validated once, so it is copied")
	}
	if got.Get("X-Allowed") != "allowed" {
		t.Errorf("X-Allowed arriving at the instance = %q, want %q: the copy is taken at construction", got.Get("X-Allowed"), "allowed")
	}
}

// repoRefForObservation is a reference the selector validator accepts, so the
// mutation under observation reaches the request builder rather than a local
// refusal.
func repoRefForObservation() forgeapi.RepoRef {
	ref := forgeapi.RepoRef{Family: forgeapi.FamilyGitea, Selector: "example/example", DisplayPath: "example/example"}
	ref.ID = ref.Encode()
	return ref
}

// headersInBothHalves returns the canonical names this library's own slices share
// with the stack's, which is the overlap the union check compacts away.
func headersInBothHalves() []string {
	library := canonical(slices.Concat(
		libraryHeadersOnEveryRequest,
		libraryHeadersOnARequestWithABody,
		libraryHeadersOneFamilySendsUnderItsOwnPrecondition,
	))
	stack := canonical(slices.Concat(stackHeadersThatArrive, stackHeadersThatNeverStick))
	var both []string
	for _, name := range library {
		if slices.Contains(stack, name) {
			both = append(both, name)
		}
	}
	return both
}

// headersWithout returns names with one member dropped, compared in canonical case,
// so an expectation can be derived from a slice above rather than restated: a read
// carries no body, so the one stack name a body produces is not in its set.
func headersWithout(names []string, drop string) []string {
	key := textproto.CanonicalMIMEHeaderKey(drop)
	return slices.DeleteFunc(slices.Clone(names), func(name string) bool {
		return textproto.CanonicalMIMEHeaderKey(name) == key
	})
}

// canonical returns names in canonical MIME header case, sorted and deduplicated,
// which is the form HTTP compares them in and the form a set comparison needs.
func canonical(names []string) []string {
	keys := make([]string, 0, len(names))
	for _, name := range names {
		keys = append(keys, textproto.CanonicalMIMEHeaderKey(name))
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}
