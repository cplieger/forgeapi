package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// cursorPrefix opens this family's continuation encoding for a REST list. The
// continuation is this library's own value and a consumer never interprets it, so
// the encoding is the smallest one that resumes a page-numbered list.
const cursorPrefix = "p"

// The two page parameters every paged REST read of this product carries.
const (
	keyPage    = "page"
	keyPerPage = "per_page"
)

// The two state spellings this product takes and answers, on a pull request, an
// issue and the filter over either.
const (
	stateOpen   = "open"
	stateClosed = "closed"
	stateAll    = "all"
)

// headerLink is this product's own pagination witness: the next page's URL under a
// next relation, absent on the last page.
//
// It is read as a WITNESS rather than as an address, and the continuation this
// family mints is the page NUMBER, because that header is measured to be
// id-addressed on repositories that were never renamed. Resuming from its URL would
// therefore address the repository by an id no selector on this surface pins, and
// comparing its path against the selector, which is the other thing a reader is
// tempted to do with it, would report a rename on page two of every list.
const headerLink = "Link"

// headerVersionSelected is the header that names the REST API version the instance
// answered under. It rides every response of this product, which is what makes the
// version pin's precondition free: a connection that has seen this header naming the
// version this family pins knows the instance carries it, and one that has not sends
// no pin.
const headerVersionSelected = "X-GitHub-Api-Version-Selected"

// headerRequestID is the header this product names itself with, which is the family
// witness here. The reason it can be one is that the NAME is this product's own: a
// release number identifies nobody, and every other service would have to send a
// header called after this product to be mistaken for it. Measured: it rides every
// response including a refusal.
const headerRequestID = "X-GitHub-Request-Id"

// headerSunset is the header upstream announces a closing window with. It is counted
// rather than acted on, because the remedy is moving the pin in a release and a
// library cannot take it at run time.
const headerSunset = "Sunset"

// checkRepo refuses a reference that is not safe to interpolate into a request path,
// before any request: a derived identifier is consumer-controlled input on its way
// into a URL, and this family's selector is an owner and a name, so one separator is
// the bound.
func checkRepo(repo forgeapi.RepoRef) error {
	return forgeapi.ValidateSelector(forgeapi.FamilyGitHub, repo.Selector)
}

// repoRoute is the escaped path of one repository's own subtree.
func repoRoute(repo forgeapi.RepoRef, rest string) string {
	return "/repos/" + repo.Selector + rest
}

// readJSON performs one GET and decodes it, answering the response beside the error
// because two of this product's facts ride there rather than in the body: the
// pagination witness and the record that a redirect was followed, which is how a
// renamed repository is detected.
//
// The read is UNCONDITIONAL, as every read this library issues is: nothing above the
// transport holds a response body to answer a not-modified response from, and no
// published return has a member that says unchanged, so the weak validator this
// product sends on every REST response is irrelevant here. A
// not-modified answer arriving anyway is the unexpected status the core maps.
func (c *Client) readJSON(ctx context.Context, op, path string, query url.Values, out any) (*transport.Response, error) {
	resp, err := c.core.Do(ctx, &transport.Request{
		Op: op, Method: http.MethodGet, Path: path, Query: query, Page: pageIn(query),
		Headers: c.restHeaders(),
	})
	c.noteResponse(resp)
	if err != nil {
		return resp, err
	}
	if err := transport.Decode(resp.Body, out); err != nil {
		return resp, c.core.FailBody(ctx, op, transport.REST(http.MethodGet), resp.Status)
	}
	return resp, nil
}

// rename is what one call learned about the address it was given: the successor a
// followed hop led to, resolved at the ONE further read the price states however many
// reads the call itself makes.
//
// read is what holds it to one. A call that reads twice meets the same hop twice, and
// the second meeting has nothing left to learn.
type rename struct {
	to   *forgeapi.RepoRef
	read bool
}

// readRepoJSON is readJSON for a REPOSITORY-addressed route whose own answer declares
// no successor carrier: a hop the transport followed is the rename witness, and here
// it becomes the stale code carrying the successor the further read resolved.
func (c *Client) readRepoJSON(ctx context.Context, op, path string, query url.Values, out any) error {
	var moved rename
	resp, err := c.readMovedJSON(ctx, op, path, query, out, &moved)
	if err != nil {
		return err
	}
	if moved.to != nil {
		return c.stale(ctx, op, http.MethodGet, resp.Status, moved.to)
	}
	return nil
}

// readMovedJSON is readJSON for a repository-addressed route on a call whose ANSWER
// declares a successor carrier: where the read followed a hop it resolves the
// successor once and hands it back for the caller to publish, so the rows the
// successor answered are returned rather than refused. A read the successor refused
// or answered undecodably is the stale refusal carrying that successor, and so is a
// move whose successor could NOT be resolved, carrying none: a carrier left nil beside
// rows from an address the caller did not name would say the read was ordinary. A cut
// answer leaves no response to read the hop from and is the transport failure.
func (c *Client) readMovedJSON(ctx context.Context, op, path string, query url.Values, out any, moved *rename) (*transport.Response, error) {
	resp, err := c.readJSON(ctx, op, path, query, out)
	if resp == nil || !resp.Redirected || transport.ContextSentinel(err) != nil {
		return resp, err
	}
	if !moved.read {
		moved.read = true
		moved.to = c.successorOf(ctx, op, resp.Ended)
	}
	if moved.to == nil || err != nil {
		return resp, c.stale(ctx, op, http.MethodGet, resp.Status, moved.to)
	}
	return resp, nil
}

// successorOf is the repository a followed hop led to, resolved at one read against
// the id-addressed location the hop ended on, and nil where that read could not
// answer.
//
// It is nil rather than a failure by decision, and the decision is the caller's
// remedy: the refusal the caller then answers with is the reconnect-and-relist one,
// which is what a consumer can act on where a successor it cannot see is not. Both
// arms of the nil are real, an address naming no repository id and a read the
// credential cannot make, and neither tells the caller anything it could use.
func (c *Client) successorOf(ctx context.Context, op, address string) *forgeapi.RepoRef {
	id := repoIDIn(address)
	if id == "" {
		return nil
	}
	var record restRepo
	if _, err := c.readJSON(ctx, op, "/repositories/"+id, nil, &record); err != nil {
		return nil
	}
	if record.FullName == "" {
		return nil
	}
	ref := repoRef(record.FullName)
	return &ref
}

// repoIDIn is the repository id an id-addressed address names, empty where the
// address is not one. The id is held to digits, because it is on its way back into a
// request path.
func repoIDIn(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}
	segments := strings.Split(u.EscapedPath(), "/")
	for i, segment := range segments {
		if segment != "repositories" || i+1 >= len(segments) {
			continue
		}
		id := segments[i+1]
		if id == "" || strings.TrimLeft(id, "0123456789") != "" {
			return ""
		}
		return id
	}
	return ""
}

// stale is the refusal a repository-addressed call answers when the transport followed
// a redirect to reach its answer, which on this product is what a rename or a transfer
// looks like.
//
// It carries the successor where one resolved, which is the value a consumer re-points
// its stored reference to, and none where the further read could not answer, which is
// the case whose remedy is listing the repositories again.
func (c *Client) stale(ctx context.Context, op, method string, status int, to *forgeapi.RepoRef) error {
	reason := "this repository answered from another address, which on this product is a rename or a transfer; the location it sends is id-addressed and the read that resolves its canonical name did not answer, so re-point by listing the repositories again"
	if to != nil {
		reason = "this repository answered from another address, which on this product is a rename or a transfer; the successor named here is its canonical selector, so re-point the reference that was stored"
	}
	fe := c.core.Fail(ctx, op, transport.REST(method), forgeapi.CodeRepoRefStale, forgeapi.KindNotFound, status, reason)
	fe.Successor = to
	return fe
}

// pageIn is the page a read asks for, so the per-request record carries it.
func pageIn(query url.Values) int {
	page, err := strconv.Atoi(query.Get(keyPage))
	if err != nil {
		return 1
	}
	return page
}

// sendJSON performs one mutation and decodes its answer. It goes through the
// transport configured for exactly one attempt, because a replayed creation or merge
// is a duplicate the inter-mutation interval cannot undo.
//
// A mutation that met a redirect is refused rather than answered, with the successor
// named where the further read resolved one: the transport declines to follow the
// three statuses net/http rewrites into a bodyless read, so a hop reaching here at all
// is one that preserved the method, and re-issuing against the successor is the
// caller's decision rather than this library's.
func (c *Client) sendJSON(ctx context.Context, op, method, path string, body, out any) error {
	resp, err := c.exchange(ctx, &transport.Request{Op: op, Method: method, Path: path, Body: body})
	if err != nil || out == nil {
		return err
	}
	if err := transport.Decode(resp.Body, out); err != nil {
		return c.core.FailBody(ctx, op, transport.REST(method), resp.Status)
	}
	return nil
}

// exchange issues one REST request with this connection's REST headers and answers
// its response, a redirect the write was refused on answered as the stale reference.
func (c *Client) exchange(ctx context.Context, req *transport.Request) (*transport.Response, error) {
	req.Headers = c.restHeaders()
	resp, err := c.core.Do(ctx, req)
	c.noteResponse(resp)
	if err != nil {
		return nil, err
	}
	if resp.Redirected {
		return nil, c.stale(ctx, req.Op, req.Method, resp.Status, c.successorOf(ctx, req.Op, resp.Ended))
	}
	return resp, nil
}

// listKind is what a list's own route or document lets a caller select beside the
// page, which decides which list options it reads and which it refuses.
type listKind int

const (
	// fixedList is a list whose route fixes what it answers: no state to
	// filter and no owner to scope by.
	fixedList listKind = iota
	// statefulList is a per-repository list with a state to filter.
	statefulList
	// scopedList is a cross-repository list: open by construction, and scoped
	// to what the credential authored or to one owner's repositories.
	scopedList
)

// listOptions resolves one list call's options and refuses, before any request, the
// ones its route or document cannot apply: a state filter on a list that fixes its
// state, and an owner on a list that is not scoped. An owner is ONE login, because
// the search grammar's owner qualifier takes a single account, so a path the shared
// form admits is refused by [forgeapi.ValidateOwner] rather than sent as a qualifier
// no account matches.
func listOptions(op string, kind listKind, opts []forgeapi.ListOption) (forgeapi.ListSettings, error) {
	set, err := forgeapi.ResolveList(opts...)
	if err != nil {
		return set, err
	}
	if set.StateSet && kind != statefulList {
		return set, transport.Local(forgeapi.CodeListStateInvalid,
			op+" lists rows whose state the endpoint fixes, so a state filter cannot apply")
	}
	if set.OwnerSet && kind != scopedList {
		return set, transport.Local(forgeapi.CodeListOwnerInvalid,
			op+" lists rows no owner scopes, so an owner cannot apply")
	}
	if set.OwnerSet {
		if err := forgeapi.ValidateOwner(forgeapi.FamilyGitHub, set.Owner); err != nil {
			return set, err
		}
	}
	if kind == statefulList && set.State == forgeapi.ListStateMerged && op == opListIssues {
		return set, transport.Local(forgeapi.CodeListStateInvalid, "an issue has no merged state")
	}
	return set, nil
}

// listing resolves one REST list call's options, the walk its continuation stands it
// at, and this product's page query for it, sending the state filter on a stateful
// list alone. repo is the repository the call addresses, the zero reference on a
// list no repository addresses.
func (c *Client) listing(op string, repo forgeapi.RepoRef, kind listKind, opts []forgeapi.ListOption) (url.Values, restWalk, error) {
	set, err := listOptions(op, kind, opts)
	if err != nil {
		return nil, restWalk{}, err
	}
	walk, err := restWalkAt(c.core.PageCall(op, repo, set), set.After)
	if err != nil {
		return nil, restWalk{}, err
	}
	query := url.Values{
		keyPage:    {strconv.Itoa(walk.page)},
		keyPerPage: {strconv.Itoa(set.PageBound)},
	}
	if kind == statefulList {
		query.Set("state", listState(set.State))
	}
	return query, walk, nil
}

// listState is this product's own spelling of the state filter, which is the same
// vocabulary its item state uses plus the wildcard. The merged member has no filter
// of its own here, so it reads the closed one and the merged rows arrive inside it,
// which is what this product's own list does.
func listState(s forgeapi.ListState) string {
	switch s {
	case forgeapi.ListStateClosed, forgeapi.ListStateMerged:
		return stateClosed
	case forgeapi.ListStateAll:
		return stateAll
	case forgeapi.ListStateOpen, forgeapi.ListStateUnknown:
		return stateOpen
	}
	return stateOpen
}

// restWalk is where one call of a REST list stands: the call its continuations name
// and the page it reads.
type restWalk struct {
	call transport.PageCall
	page int
}

// restWalkAt reads where a REST list call stands from its continuation, the empty one
// being the first page, refusing one this library did not mint and one another call
// minted rather than resuming from garbage or at a row of another walk.
func restWalkAt(call transport.PageCall, c forgeapi.Cursor) (restWalk, error) {
	if c == "" {
		return restWalk{call: call, page: 1}, nil
	}
	if err := forgeapi.ValidateCursor(c); err != nil {
		return restWalk{}, err
	}
	if strings.HasPrefix(string(c), documentPrefix) || strings.HasPrefix(string(c), searchPrefix) {
		return restWalk{}, transport.Local(forgeapi.CodeCursorInvalid,
			"continuation was minted by one of this family's documents and this call reads REST, so it names no position here")
	}
	position, err := call.Resume(c, cursorPrefix, 1)
	if err != nil {
		return restWalk{}, err
	}
	if position[0] < 1 {
		return restWalk{}, transport.Local(forgeapi.CodeCursorInvalid, "continuation names no page")
	}
	return restWalk{call: call, page: position[0]}, nil
}

// here is the continuation naming the page this walk reads, which is what a list
// whose bound admitted no page hands back so the remainder stays reachable.
func (w restWalk) here() forgeapi.Cursor {
	return w.call.Mint(cursorPrefix, w.page)
}

// next is the continuation for the page after this one, empty where the list is
// complete. The witness is this product's own link header, read for the presence of a
// next relation rather than for the URL it carries.
func (w restWalk) next(header http.Header) forgeapi.Cursor {
	if !hasNextLink(header) {
		return ""
	}
	return w.call.Mint(cursorPrefix, w.page+1)
}

// hasNextLink reports whether one response's link header offers a next page.
func hasNextLink(header http.Header) bool {
	for _, value := range header.Values(headerLink) {
		for field := range strings.SplitSeq(value, ",") {
			if strings.Contains(field, `rel="next"`) {
				return true
			}
		}
	}
	return false
}

// unpagedList is the answer a list gives when its page bound admits no page at all:
// nothing is issued, and the marker is the one the page cap carries, with resume
// naming the page nobody asked for so the remainder is reachable without re-running a
// call this bound refuses.
//
// It is the literal zero the option publishes. A list that sent one page anyway would
// execute the opposite of what its own doc comment states, and clamping the bound up
// to one is the reading a single configuration door rules out.
func (c *Client) unpagedList[T any](resume forgeapi.Cursor) (forgeapi.Page[T], bool) {
	if c.core.Budget().ListPages > 0 {
		return forgeapi.Page[T]{}, false
	}
	return forgeapi.Page[T]{
		Next:    resume,
		Partial: c.partial(forgeapi.PartialPaginationCap, 0),
	}, true
}

// pagePartial is the response-level marker for a list that reached its page cap. It
// always arrives with a continuation, so a consumer fetches the next page with that
// cursor rather than re-running the same capped call.
func (c *Client) pagePartial(rows int, next forgeapi.Cursor, pages int) *forgeapi.Partial {
	if next == "" || pages < c.core.Budget().ListPages {
		return nil
	}
	return c.partial(forgeapi.PartialPaginationCap, rows)
}

// partial mints one partial marker and counts it, which is the one place this family
// reports an incomplete result: the counter is what separates a partial the governor
// chose from one upstream imposed, and a marker minted without it is a partial no
// consumer's metrics can see.
func (c *Client) partial(reason forgeapi.PartialReason, fetched int) *forgeapi.Partial {
	if counters := c.core.Counters(); counters.PartialResult != nil {
		counters.PartialResult(forgeapi.FamilyGitHub, reason)
	}
	return &forgeapi.Partial{Reason: reason, Fetched: fetched, OmittedAtLeast: 1}
}

// deferredPage turns the governor's deferral into the empty page that carries the
// rate-limited reason. A read held back to keep the mutation reserve is not a
// failure: nothing was issued, so the list has no rows and says why, and a consumer
// tells it from an upstream throttle by the remaining figure the governor reports.
func (c *Client) deferredPage[T any](err error) (forgeapi.Page[T], bool) {
	if !transport.IsDeferred(err) {
		return forgeapi.Page[T]{}, false
	}
	return forgeapi.Page[T]{Partial: c.partial(forgeapi.PartialRateLimited, 0)}, true
}

// errorf is the local shape for a refusal this family reaches with a formatted
// reason.
func errorf(code, format string, args ...any) *forgeapi.Error {
	return transport.Local(code, fmt.Sprintf(format, args...))
}

// asForgeError reports whether err is this library's own error type, which is one of
// the three answers every operation can give.
func asForgeError(err error, out **forgeapi.Error) bool {
	fe, ok := err.(*forgeapi.Error)
	if !ok {
		return false
	}
	*out = fe
	return true
}
