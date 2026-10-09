package gitea

import (
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// Client is one connection to a Gitea-family instance: where it is, what the
// credential is trusted with, and what the budget allows.
//
// It is the concrete type [New] returns, so a consumer declares the narrowest
// role interface it actually uses against it rather than receiving an interface
// this package chose. What it holds is unexported, because the state behind an
// operation is nobody else's contract, including which product of the two
// answered, since a caller gates on a capability rather than on a product name.
type Client struct {
	core    *transport.Conn
	caps    *forgeapi.ConnectionCaps
	swagger *swaggerDoc
	foldAt  map[string]int
	// resolve is the connection's one resolution owner, a capacity-one channel
	// so a caller waits for it under its own context (takeResolve). It is held
	// across the version, swagger and settings READS, not only across the cache
	// checks around them, because the price this family publishes is one read of
	// each per connection rather than one per caller: two first callers that each
	// pass a mutex-protected check before either writes fetch a near-megabyte
	// document twice, which race freedom alone does not prevent.
	resolve       chan struct{}
	version       string
	namedBy       string
	versionStatus int
	// maxItems is the instance's stated max_response_items, zero while the
	// connection holds none.
	maxItems int
	forgejo  bool
	mu       sync.Mutex
}

// New builds a client for one connection.
//
// It performs no I/O. Which of the two products this instance runs, and every
// capability that version decides, is resolved by the accessor that needs it, so
// an instance that is down fails at the operation rather than at construction.
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

// open is New with the governor's clock stated rather than taken, which is the one
// thing a test of the rotation needs and no option may carry: the rolling interval
// is a bound only the clock moves, so a case spanning several windows either waits
// out a real one, making its verdict a race against the machine's load, or drives
// time itself. Every consumer reaches this through New, on the real clock.
func open(conn *forgeapi.Connection, clock func() time.Time, opts ...forgeapi.Option) (*Client, error) {
	settings := forgeapi.Resolve(opts...)
	core, err := transport.Open(conn, &settings, transport.Options{
		Mapper:        mapper,
		Signal:        signal,
		DeriveAPIBase: deriveAPIBase,
		Clock:         clock,
		Family:        forgeapi.FamilyGitea,
	})
	if err != nil {
		return nil, err
	}
	return &Client{core: core, resolve: make(chan struct{}, 1)}, nil
}

// deriveAPIBase is this family's API root under the web base, used where the
// connection names no API base of its own.
func deriveAPIBase(web *url.URL) string {
	return strings.TrimSuffix(web.String(), "/") + "/api/v1"
}

// rememberFoldPosition records the POSITION whose row this connection last folded
// in ONE list, which is what the rotation resumes from when the row the cursor names
// has left that list.
//
// The cursor is the durable half and names a ROW, because that is what survives a
// restart in a consumer's own storage; this is the volatile half and names a place
// in the order one instance served one list in. A row that is merged, closed or
// paged out between two cycles takes the cursor's referent with it, and without this
// the window would restart at the head of the list and re-fold the prefix the
// previous interval already served, which is the starvation the rotation exists to
// prevent.
//
// It is held PER LIST rather than per connection, because a position is an index
// into one order and means nothing in another: a poller watching several
// repositories calls the per-repository list once per repository per cycle on one
// connection, so a single position would be overwritten by the next repository every
// time and each list would resume from wherever a different list left off. Measured
// over four cycles of two four-row repositories under a cap of one fold per
// interval: a position shared by every list locks the two into alternate parities,
// each starving half its rows, and no volatile position at all re-folds each list's
// head forever. The map holds one small entry per list this
// connection has folded in, which is the set of repositories the consumer polls, and
// nothing persists it: a restart is cold here and the durable cursor is what resumes.
func (c *Client) rememberFoldPosition(list string, at int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.foldAt == nil {
		c.foldAt = map[string]int{}
	}
	c.foldAt[list] = at
}

// foldPosition is the position this connection last folded at IN ONE list, and
// whether it has folded anything in that list at all. A list it has not reports
// false, which is the first-run answer, the answer after a restart, and the answer
// for a list this connection is presenting for the first time, where the head of it
// is all there is to start from.
func (c *Client) foldPosition(list string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.foldAt[list]
	return at, ok
}

// BudgetState implements [forgeapi.Governor]. It is the read-only view of this
// connection's governor, and which of its four fields carry a read value depends
// on the PRODUCT rather than the family. Forgejo sends a structured `ratelimit`
// header on every response, so Remaining is its `r` parameter and Reset its `t`
// seconds; Gitea sends no rate-limit header at all, so there Remaining is
// [forgeapi.BudgetRemainingUnknown] and Reset is the zero time. On both, LastCost
// is this library's own per-call price, one per request, with the folded status
// read as the expensive member, since neither product prices a call on the wire.
// The governor's reserve has a signal to hold against on Forgejo and is inert on
// Gitea; what bounds this family's folded-status work on either is the two
// per-interval caps [forgeapi.Budget] publishes, counted by this client over a
// rolling window of its own, and [forgeapi.BudgetState.RotationCursor] is where
// that rotation stands.
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
