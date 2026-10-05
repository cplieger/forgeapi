package gitea

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// address is the repository one call addresses. Both products answer a moved
// repository's old path with a 301 to its new path, so a followed hop names the
// successor at no further request, and every later request of the call addresses it:
// a move costs a call one request however many reads it makes.
type address struct {
	// successor is the repository a followed hop named, nil while none has.
	successor *forgeapi.RepoRef
	// named is the repository the caller passed, the zero reference on a list whose
	// rows each name their own.
	named forgeapi.RepoRef
	// status is what the read that named the successor answered.
	status int
}

// current is the repository the call's next request addresses.
func (a *address) current() forgeapi.RepoRef {
	if a.successor != nil {
		return *a.successor
	}
	return a.named
}

// route is the path of one route under the repository the next request addresses.
func (a *address) route(rest string) string {
	return "/repos/" + a.current().Selector + rest
}

// readAt is [Client.readJSON] for a route under the call's repository. A followed hop
// naming no repository this family addresses is the stale refusal with no successor,
// since rows from an address the caller did not name are not an ordinary answer. A
// successor that refused the read or answered a body that did not decode is the stale
// refusal carrying it; an answer cut before its body arrived leaves no response to
// read the hop from, so it is the transport failure, and a retry names the move.
func (c *Client) readAt(ctx context.Context, op string, a *address, rest string, query url.Values, out any) error {
	resp, err := c.get(ctx, op, a.route(rest), query, out)
	if resp == nil || !resp.Redirected || transport.ContextSentinel(err) != nil {
		return err
	}
	to := c.repoAt(resp.Ended)
	switch {
	case to == nil:
		return c.stale(ctx, op, resp.Status, nil)
	case to.ID == a.current().Encode():
		return err
	case err != nil:
		return c.stale(ctx, op, resp.Status, to)
	}
	a.successor, a.status = to, resp.Status
	return nil
}

// moved is the refusal of a call whose answer has no successor carrier once its
// repository moved, nil while it has not: a silent answer would be a stale bookmark.
func (c *Client) moved(ctx context.Context, op string, a *address) error {
	if a.successor == nil {
		return nil
	}
	return c.stale(ctx, op, a.status, a.successor)
}

// repoAt is the repository an absolute address names on this connection, nil where it
// names none the family's selector rule admits: a redirect's location is upstream
// input on its way into a request path.
func (c *Client) repoAt(address string) *forgeapi.RepoRef {
	rest, ok := c.core.UnderAPI(address)
	if !ok {
		return nil
	}
	tail, ok := strings.CutPrefix(rest, "/repos/")
	if !ok {
		return nil
	}
	owner, tail, _ := strings.Cut(tail, "/")
	name, _, _ := strings.Cut(tail, "/")
	owner, ownerErr := url.PathUnescape(owner)
	name, nameErr := url.PathUnescape(name)
	if ownerErr != nil || nameErr != nil {
		return nil
	}
	selector := owner + "/" + name
	if forgeapi.ValidateSelector(forgeapi.FamilyGitea, selector) != nil {
		return nil
	}
	ref := repoRef(selector)
	return &ref
}

// nameRefusedSuccessor puts the successor a write's refused hop names on the stale
// refusal the transport answered. The write is not sent again: re-issuing it against
// the successor is the caller's decision.
func (c *Client) nameRefusedSuccessor(a *address, resp *transport.Response, err error) {
	var fe *forgeapi.Error
	if resp == nil || resp.Refused == "" || !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefStale {
		return
	}
	if to := c.repoAt(resp.Refused); to != nil && to.ID != a.current().Encode() {
		fe.Successor = to
	}
}

// stale is the refusal of a read whose repository moved, carrying the successor where
// the redirect's location named one; without one the remedy is relisting.
func (c *Client) stale(ctx context.Context, op string, status int, to *forgeapi.RepoRef) error {
	reason := "this repository answered from another address, which on this family is a rename or a transfer, and the redirect's location names no repository this family addresses, so re-point by listing the repositories again"
	if to != nil {
		reason = "this repository answered from another address, which on this family is a rename or a transfer; the successor named here is the repository the redirect's location names, so re-point the reference that was stored"
	}
	fe := c.core.Fail(ctx, op, transport.REST(http.MethodGet), forgeapi.CodeRepoRefStale, forgeapi.KindNotFound, status, reason)
	fe.Successor = to
	return fe
}
