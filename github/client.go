package github

import (
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// APIVersionHeader is the header that pins this family's REST API version. It rides
// every REST request of a connection whose instance SERVES [APIVersion], and no
// request of a connection whose instance does not.
//
// Which of the two a connection is, is resolved once at connection setup from the
// version set the instance publishes, and priced on the connection's own row. The
// echo cannot resolve it, which is why the set is read: this product names the
// version a response answered UNDER, and an unpinned request is answered under the
// older version the instance serves, so a connection waiting for that echo to name
// the pin waits forever and every request runs under the version this pin
// supersedes.
//
// What an instance outside this version's reach answers is measured rather than
// assumed, and it is why the set is read rather than the pin sent blind: a version
// the instance does not serve is refused with 400, its body naming this header and
// listing the versions served, and that refusal carries NO echo, because the request
// is refused before any version is selected. An ordinary refusal does carry one,
// measured on a 404 and on a 422. Those two readings are what [retiredVersion] holds
// the answer to, on the requests that carried the pin.
//
// A connection whose instance serves no such version therefore runs under that
// instance's own default rather than having every request refused, and so does one
// whose setup has not run. That default is the version this package's field sets
// were first measured against, and the breaking-changes reading for the pinned date
// moved no field this library reads.
const APIVersionHeader = "X-GitHub-Api-Version"

// APIVersion is the pinned REST API version: the current one, with no end of
// support scheduled. It supersedes
// 2022-11-28, the older of the two versions this product serves, which closes on
// 10 March 2028; that version is what the field sets in this package were first
// measured against, and this product's breaking-changes list for this date was read
// against every response field before any fixture was captured.
//
// The pin is a dated obligation, not a fixed input: an instance announces an
// approaching close through headers this library counts, and an instance that
// RETIRES this version while a connection is pinned to it stops answering under it,
// which this family's error mapping reads off the echo as
// [forgeapi.CodeAPIVersionRetired]. The obligation is to move this constant before
// that date, which is why it is one named constant with one test row behind it.
const APIVersion = "2026-03-10"

// dotcomHost is this product's hosted instance, whose API and web bases are two
// DIFFERENT hosts. Every other instance is an appliance, whose API root sits under
// its own web base, so the two derivations are one branch on this name.
const dotcomHost = "github.com"

// dotcomAPIBase is the hosted instance's API root, which is the addressing the
// expectation table spells this product's routes in: no path prefix at all.
const dotcomAPIBase = "https://api.github.com"

// applianceAPIPath and applianceDocumentPath are an appliance's two roots under its
// own web base. The REST root carries a version segment and the document endpoint
// sits BESIDE it rather than under it, which is why the document URL is derived by
// replacing that segment rather than by appending to it.
const (
	applianceAPIPath      = "/api/v3"
	applianceDocumentPath = "/api/graphql"
)

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

// New builds a client for one connection.
//
// It performs no I/O. Every capability this instance's version decides is
// resolved by the accessor that needs it, so an instance that is down fails at
// the operation rather than at construction, and a consumer can build its
// clients before it can reach anything.
//
// What it does refuse at entry is a client it cannot run honestly, and every
// refusal carries a code of its own, because a consumer names the remedy for the
// refusal it got rather than showing a generic warning: no credential source,
// which is [forgeapi.CodeAnonymousRefused] rather than a client that sends
// anonymous requests; no web base URL, a half-present certificate pair, a
// [forgeapi.Connection.Proxy] URL net/url refuses or carrying no scheme this
// library proxies over, or a malformed [forgeapi.Header] entry, which is
// [forgeapi.CodeConnectionInvalid]; an
// extra header whose name this library or the HTTP stack writes itself, which is
// [forgeapi.CodeHeaderReserved] naming it; a plaintext URL without
// [forgeapi.WithPlaintextHTTP], which is [forgeapi.CodePlaintextRefused]; a
// private-range or single-label host without [forgeapi.WithPrivateAddresses],
// which is [forgeapi.CodePrivateAddressRefused]; a negative value passed to any
// budget option, [forgeapi.WithRetries] and [forgeapi.WithMutationReserve]
// included, which is [forgeapi.CodeBudgetInvalid]; and a
// [forgeapi.RotationCursor] that is not well formed, which is
// [forgeapi.CodeCursorInvalid].
//
//nolint:gocritic // hugeParam: the connection record is the published signature's own parameter
func New(conn forgeapi.Connection, opts ...forgeapi.Option) (*Client, error) {
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
		DeriveAPIBase: deriveAPIBase,
		Clock:         clock,
		Family:        forgeapi.FamilyGitHub,
	})
	if err != nil {
		return nil, err
	}
	client.core = core
	client.documentURL = documentURL(core.APIBase())
	return client, nil
}

// deriveAPIBase is this product's API root under the web base, used where the
// connection names no API base of its own.
//
// The hosted instance serves its API on a host of its OWN rather than under the web
// base, which is why this is a branch rather than a suffix: the routes the
// expectation table spells carry no prefix because that host's root is the prefix.
// Every other instance is an appliance, whose documented root is a version segment
// under its own web base.
func deriveAPIBase(web *url.URL) string {
	if isDotcom(web) {
		return dotcomAPIBase
	}
	return strings.TrimSuffix(web.String(), "/") + applianceAPIPath
}

// isDotcom reports whether a URL addresses the hosted instance, by host rather than
// by anything a response carries, because the answer decides where the first request
// goes.
func isDotcom(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	return host == dotcomHost || host == "www."+dotcomHost || host == "api."+dotcomHost
}

// documentURL is where this connection posts its documents, derived from the API root
// the connection resolved: beside the REST routes on an appliance, whose root carries
// a version segment the document endpoint does not, and under the API host on the
// hosted instance.
func documentURL(api *url.URL) string {
	base := strings.TrimSuffix(api.String(), "/")
	if trimmed, ok := strings.CutSuffix(base, applianceAPIPath); ok {
		return trimmed + applianceDocumentPath
	}
	return base + "/graphql"
}

// noteResponse records what a response tells this connection about its instance, for
// the two facts that ride traffic already sent and therefore cost nothing: that this
// product answered at all, which is the family witness; and an announced window
// closing, which is counted because the remedy is moving the pin in a release and a
// library cannot take it at run time.
//
// The version an answer came under is NOT among them. It is read per response by the
// error mapping rather than held, because the pin rides every request, so the echo
// carries no per-connection fact left to learn.
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

// restHeaders is this family's own header set for one REST request, which is the
// version pin where this connection's instance serves it and nothing where it does
// not.
//
// The pin is the REST surface's obligation alone: the date version names a REST API
// version, the header naming the version an instance answered under rides REST
// responses, and this product's document surface is versioned by its schema and its
// deprecation window instead. So a document request carries no header from this
// family, which is why [Client.execute] sets none rather than calling this.
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

// BudgetState implements [forgeapi.Governor]. It is the read-only view of this
// connection's governor: the remaining budget the instance last reported, when
// it renews, and what the last call cost. This product reports the remaining
// budget and the reset through its rate-limit headers on both surfaces, and a
// document's cost through the rate-limit object every query document selects.
// What it reports is the USER's primary quota, drawn on by every
// application acting for that credential, so the remaining budget falls while
// this client sends nothing and the governor's reserve is held against that
// shared pool. Both costs were measured: a document is 1 whatever it selects, at
// every node count from 0 to 2440, and a REST call is 1 unit of the core
// resource, read as a decrement of one per request across twelve consecutive
// calls. So [forgeapi.BudgetState.LastCost] is 1 on either surface. The two are
// separate windows, so what is reported is the window the last call drew on.
//
// The figure is read from the signal riding the response just received and NEVER
// from this product's rate-limit endpoint, which is measured to report a full pool
// with a fresh window on three separate reads while the same credential's document
// budget was demonstrably drawn down.
//
// [forgeapi.BudgetState.RotationCursor] is empty on this family and nothing
// rotates: a list's folded verdict is exact from the first page of a connection the
// list document already sends, so no row costs a read of its own and there is no
// per-interval cap for a rotation to spread.
//
// It performs no I/O and is safe to call concurrently with operations; it is
// never written from outside. See [forgeapi.BudgetState].
func (c *Client) BudgetState() forgeapi.BudgetState { return c.core.BudgetState() }

// Close implements [forgeapi.Core]: it releases this client's pool, the idle
// connections of the transport the library built for the connection, at once,
// which a caller that disconnects or re-addresses the connection wants; otherwise
// they close after that transport's idle timeout. A transport supplied through
// [forgeapi.WithWireTransport] stays its author's to release. A call in flight
// keeps its connection until it returns. Close adds no closed state: a call after
// it dials afresh, and a further Close releases that connection too.
func (c *Client) Close() { c.core.Close() }
