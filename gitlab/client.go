package gitlab

import (
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// Client is one GitLab connection: where the instance is, what the credential is
// trusted with, and what the budget allows.
//
// It is the concrete type [New] returns, so a consumer declares the narrowest
// role interface it actually uses against it rather than receiving an interface
// this package chose. What it holds is unexported, because the state behind an
// operation is nobody else's contract.
type Client struct {
	core  *transport.Conn
	caps  *forgeapi.ConnectionCaps
	grant *forgeapi.Evidence
	// refused is the evidence of the latest request on this connection the
	// instance refused for a permission or scope the credential lacks, nil while
	// none has been. It outranks the permission pair, because this product checks
	// the token's own permission before the owner's access and the pair reports
	// only the owner's.
	refused  *forgeapi.Evidence
	settings forgeapi.Settings
	// grantPush is what the last document reported for the project permission
	// that decides whether a caller can ask for a merge-status recalculation. It
	// is held beside grant because the evidence names the source and this names
	// the answer.
	grantPush forgeapi.Support
	// degraded records that this connection's documents are refused, so every
	// document read on it goes to REST instead. It is per CONNECTION rather than
	// per version, and it is normally written by the bounded schema question
	// connection setup asks, which is what lets every operation publish an exact
	// price. A document refused at runtime after that question was answered writes
	// it too, and the call that met the refusal reports it rather than answering
	// from REST, so no call spends a discovery its row does not price.
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
	core, err := transport.Open(conn, &settings, transport.Options{
		Mapper:        mapper,
		Signal:        signal,
		DeriveAPIBase: deriveAPIBase,
		Clock:         clock,
		Family:        forgeapi.FamilyGitLab,
	})
	if err != nil {
		return nil, err
	}
	return &Client{core: core, settings: settings, grantPush: forgeapi.SupportUnknown}, nil
}

// deriveAPIBase is this product's API root under the web base, used where the
// connection names no API base of its own.
func deriveAPIBase(web *url.URL) string {
	return strings.TrimSuffix(web.String(), "/") + "/api/v4"
}

// markDegraded records that a document was refused at runtime, so every later
// document read on this connection goes to REST.
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

// rememberGrant records what a document reported for the project permissions,
// which is where [Client.GrantCaps] answers from.
//
// It is recorded by the documents rather than fetched by the accessor because the
// accessor names no repository: its signature is credential-scoped, so there is no
// project path and no merge-request number for it to send a document with, and the
// permissions ride every document a read of this product already sends. That is
// what prices the accessor at zero requests.
func (c *Client) rememberGrant(push, read bool) {
	detail := "project permissions on the last document: pushCode " + boolWord(push) +
		", readMergeRequest " + boolWord(read)
	// A credential the instance reports as unable to push reads UNKNOWN rather than
	// no, which is the difference between "this instance lacks the feature" and "this
	// credential may be shown a merge status nothing refreshed". A no here would have
	// a consumer disable the control on an instance where it works.
	support := forgeapi.SupportUnknown
	if push {
		support = forgeapi.SupportYes
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.grantPush = support
	c.grant = &forgeapi.Evidence{Source: forgeapi.EvidenceResponseBody, Detail: detail}
}

// rememberRefusal records a request the instance refused for a permission or scope
// the credential lacks, naming the operation and the instance's own text of what is
// missing, which arrives sanitized and bounded on the error.
func (c *Client) rememberRefusal(fe *forgeapi.Error) {
	ev := &forgeapi.Evidence{
		Source: forgeapi.EvidenceResponseBody,
		Detail: fe.Op + " was refused for a permission the credential lacks: " + fe.Message,
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refused = ev
}

// heldGrant is the recorded grant and its evidence, with the third return false
// where nothing on this connection has reported one yet. A scope refusal answers
// unknown with its own evidence, ahead of any permission pair.
func (c *Client) heldGrant() (forgeapi.Support, forgeapi.Evidence, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refused != nil {
		return forgeapi.SupportUnknown, *c.refused, true
	}
	if c.grant == nil {
		return forgeapi.SupportUnknown, forgeapi.Evidence{}, false
	}
	return c.grantPush, *c.grant, true
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// BudgetState implements [forgeapi.Governor]. It is the read-only view of this
// connection's governor: the remaining budget the instance last reported, when
// it renews, and what the last call cost. This product reports remaining and
// reset through its rate-limit headers and prices a document by the complexity
// the endpoint reports, which is what LastCost carries after a document read.
// The headers report the credential's own quota rather than this client's share
// of it, so it moves when another application spends and the governor's reserve
// is held against that shared pool.
//
// [forgeapi.BudgetState.RotationCursor] is empty on this family and nothing
// rotates: the folded verdict here is one scalar selected by a document the list
// already sends, so no row costs a read of its own and there is no per-interval
// cap for a rotation to spread.
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
