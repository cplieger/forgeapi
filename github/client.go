package github

import (
	"sync"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/internal/githubroot"
	"github.com/cplieger/forgeapi/v2/internal/transport"
)

// APIVersionHeader is the header that pins this family's REST API version. It
// rides every REST request of a connection whose instance serves [APIVersion],
// read once at connection setup from the version set the instance publishes, and
// no request of one whose instance does not: an unserved version is refused with
// 400 and no version echo, so the pin is never sent blind. A connection without
// the pin, or whose setup has not run, runs under the instance's default.
// [retiredVersion] reads the answers to pinned requests.
const APIVersionHeader = "X-GitHub-Api-Version"

// APIVersion is the pinned REST API version: the current one, with no end of
// support scheduled. It supersedes 2022-11-28, which closes on 10 March 2028 and
// which this package's field sets were first measured against; this date's
// breaking changes were read against every response field. An instance that
// retires it answers [forgeapi.CodeAPIVersionRetired], so move this constant
// before that date.
const APIVersion = "2026-03-10"

// Client is one GitHub connection: where the instance is, what the credential is
// trusted with, and what the budget allows.
//
// It is the concrete type [New] returns, so a consumer declares the narrowest
// role interface it actually uses against it rather than receiving an interface
// this package chose. What it holds is unexported, because the state behind an
// operation is nobody else's contract.
type Client struct { //nolint:govet // fieldalignment: the field order is this type's own reading order, the connection first and the flags it learns about its instance after, and no value of it is allocated per request
	core *transport.Conn
	// documentURL is where this connection's documents are posted, derived once
	// from the API root: this product serves them beside the REST routes rather
	// than under them.
	documentURL string
	caps        *forgeapi.ConnectionCaps
	grant       *forgeapi.GrantCaps
	// witness records that a response on this connection named this product,
	// which is the cheapest family evidence and rides traffic already sent.
	witness bool
	// pinned records that this instance serves the version this family pins, read
	// once at connection setup. False is the OMIT arm rather than an unresolved
	// one: a connection that sends no pin takes the instance's own default, which
	// is the answer for an appliance below the pinned version and for a connection
	// whose setup has not run.
	pinned bool
	// degraded records that this connection's documents are refused, so the two
	// reads that have a REST arm take it from their next call. It is per
	// CONNECTION rather than per version, and it is written by the call that met
	// the refusal, whose own failure it is.
	degraded bool
	mu       sync.Mutex
}

// New builds a client for one connection. It performs no I/O, so an instance
// that is down fails at the operation, not at construction. It refuses a missing
// credential source ([forgeapi.CodeAnonymousRefused]), a malformed connection
// ([forgeapi.CodeConnectionInvalid]), a reserved extra header
// ([forgeapi.CodeHeaderReserved]), plaintext without [forgeapi.WithPlaintextHTTP]
// ([forgeapi.CodePlaintextRefused]), a private host without [forgeapi.WithPrivateAddresses]
// ([forgeapi.CodePrivateAddressRefused]), a negative budget value
// ([forgeapi.CodeBudgetInvalid]) and a malformed cursor ([forgeapi.CodeCursorInvalid]).
func New(conn forgeapi.Connection, opts ...forgeapi.Option) (*Client, error) { //nolint:gocritic // hugeParam: the connection record is the published signature's own parameter
	return open(&conn, time.Now, opts...)
}

// open is New with the governor's clock stated rather than taken, which is what a
// test of the rolling interval needs and no option may carry. Every consumer
// reaches this through New, on the real clock.
func open(conn *forgeapi.Connection, clock func() time.Time, opts ...forgeapi.Option) (*Client, error) {
	settings := forgeapi.Resolve(opts...)
	// The client is allocated before the transport because the error mapping is
	// ITS: one cell of that mapping turns on whether this connection sends the
	// version pin, which is a fact the connection learns and not one the mapping
	// can read off a response.
	client := &Client{}
	core, err := transport.Open(conn, &settings, transport.Options{
		Mapper:        client.mapper,
		Signal:        signal,
		Pool:          pool,
		DeriveAPIBase: githubroot.API,
		Clock:         clock,
		Family:        forgeapi.FamilyGitHub,
		// A not-modified answer to an authorized conditional request bills
		// nothing here, which the transport checks per request:
		// spec.Revalidations owns the fact.
		FreeNotModified: true,
	})
	if err != nil {
		return nil, err
	}
	client.core = core
	client.documentURL = githubroot.Document(core.APIBase())
	return client, nil
}

// noteResponse records what a response tells this connection for free: that this
// product answered, the family witness, and an announced window closing, counted
// because the remedy is moving the pin in a release. The version an answer came
// under is read per response by the error mapping, not held here.
func (c *Client) noteResponse(resp *transport.Response) {
	if resp == nil || resp.Header == nil {
		return
	}
	if resp.Header.Get(headerSunset) != "" {
		if counters := c.core.Counters(); counters.SunsetSeen != nil {
			counters.SunsetSeen(forgeapi.FamilyGitHub)
		}
	}
	if resp.Header.Get(headerRequestID) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.witness = true
}

// restHeaders is this family's header set for one REST request: the version pin
// where this connection's instance serves it, nothing where it does not. The pin
// is the REST surface's alone, since documents are versioned by their schema, so
// [Client.execute] sets no header on a document request.
func (c *Client) restHeaders() []forgeapi.Header {
	if !c.sendsPin() {
		return nil
	}
	return []forgeapi.Header{{Name: APIVersionHeader, Value: APIVersion}}
}

// holdPin records whether this instance serves the version this family pins, which
// is what every later REST request of this connection carries or omits.
func (c *Client) holdPin(served bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pinned = served
}

// sendsPin reports whether this connection sends the version pin. A connection whose
// setup has not run answers no, which is the same arm an instance that serves no such
// version takes: the request goes out unpinned and the instance answers under its own
// default.
func (c *Client) sendsPin() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pinned
}

// sawWitness reports whether a response on this connection has named this product,
// which is the cheapest family evidence and the reason the connection read can answer
// at no request on a connection that has already been used.
func (c *Client) sawWitness() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.witness
}

// markDegraded records that a document was refused, so the two reads that have a REST
// arm take it from their next call.
func (c *Client) markDegraded() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.degraded = true
}

// isDegraded reports whether this connection has already discovered that its
// documents are refused.
func (c *Client) isDegraded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.degraded
}

// BudgetState implements [forgeapi.Governor]: the USER's primary quota, shared by
// every application acting for the credential, as the last response's rate-limit
// headers report it, never the rate-limit endpoint, measured to report a full pool
// while documents drew it down. REST and documents draw separate windows, and the
// one reported is the last call's. [forgeapi.BudgetState.LastCost] is what the call
// spent: 1 per REST attempt, 0 for an authorized conditional 304, replaced by a
// document's reported cost where a document is its last exchange. No I/O; safe
// concurrently. RotationCursor is empty: a list's folded verdict is exact here.
func (c *Client) BudgetState() forgeapi.BudgetState { return c.core.BudgetState() }

// Close implements [forgeapi.Core]: it releases this client's pool, the idle
// connections of the transport the library built for the connection, at once,
// which a caller that disconnects or re-addresses the connection wants; otherwise
// they close after that transport's idle timeout. A transport supplied through
// [forgeapi.WithWireTransport] stays its author's to release. A call in flight
// keeps its connection until it returns. Close adds no closed state: a call after
// it dials afresh, and a further Close releases that connection too.
func (c *Client) Close() { c.core.Close() }
