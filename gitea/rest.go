package gitea

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

// cursorPrefix opens this family's continuation encoding. The continuation is this
// library's own value and a consumer never interprets it, so the encoding is the
// smallest one that resumes a page-numbered list exactly: the page, the limit its
// walk is sent at, which the page number counts in, and the call and page bound
// [transport.PageCall] names.
const cursorPrefix = "p"

// runCursorPrefix opens the run listing's continuation. It is apart from
// [cursorPrefix] because it carries one fact more: the rows the walk has been
// served, which the listing's stated total is read against. So the run listing
// handed another list's could not tell how much of its walk was served, and each
// refusal names the list the continuation belongs to.
const runCursorPrefix = "r"

// maxRunWalk bounds the page and the served count a run continuation is decoded
// with. A walk this library mints stays far below it, and the bound keeps a forged
// value from overflowing the next page's number or the walk's sum. The limit takes
// no bound of its own: it is at most a page bound the caller asked for, which
// [forgeapi.WithPageBound] admits at any positive value, and nothing here does
// arithmetic on it.
const maxRunWalk = 1 << 30

// The two state spellings this product takes and answers, on a pull request, an
// issue and the filter over either.
const (
	stateOpen   = "open"
	stateClosed = "closed"
)

// The two page parameters every paged read of this product carries.
const (
	keyPage  = "page"
	keyLimit = "limit"
)

// checkRepo refuses a reference that is not safe to interpolate into a request
// path, before any request: a derived identifier is consumer-controlled input on
// its way into a URL.
func (c *Client) checkRepo(repo forgeapi.RepoRef) error {
	return forgeapi.ValidateSelector(forgeapi.FamilyGitea, repo.Selector)
}

// unpagedList is the answer a list gives when its page bound admits no page at all:
// nothing is issued, and the marker is the one the page cap carries, with here, the
// continuation naming the page nobody asked for, so the remainder is reachable
// without re-running a call this bound refuses. The caller mints here, because the
// encoding that names a page is the list's own.
//
// It is the literal zero the option publishes. A list that sent one page anyway
// would execute the opposite of what its own doc comment states, and clamping the
// bound up to one is the reading a single configuration door rules out.
func (c *Client) unpagedList[T any](here forgeapi.Cursor) (forgeapi.Page[T], bool) {
	if c.core.Budget().ListPages > 0 {
		return forgeapi.Page[T]{}, false
	}
	return forgeapi.Page[T]{
		Next:    here,
		Partial: c.partial(forgeapi.PartialPaginationCap, 0),
	}, true
}

// readJSON performs one GET and decodes it, which is the shape of every read this
// family makes.
//
// The read is UNCONDITIONAL, as every read this library issues is, and that is a
// consequence of two things settled rather than an omission. A validator only pays
// where the sender can answer a not-modified response from something it holds, and
// this client holds no response body: a decoded list kept per connection would be
// the repository cache the boundary rules out. Nor can the answer be handed on: the
// published return is the value itself, with no member that says "unchanged", so a
// not-modified answer has nowhere to go but into a zero-valued one that silently
// replaces whatever the caller had. Conditional requests are therefore a transport
// LAYER this library does not build, so nothing here sends a validator and a
// not-modified answer arriving anyway is the unexpected status the core maps, which
// reaches this function as the error below.
func (c *Client) readJSON(ctx context.Context, op, path string, query url.Values, out any) error {
	_, err := c.get(ctx, op, path, query, out)
	return err
}

// get is the GET both reads make, answering the response beside the decoded body
// because [Client.readAt] reads the redirect record it carries.
func (c *Client) get(ctx context.Context, op, path string, query url.Values, out any) (*transport.Response, error) {
	resp, err := c.core.Do(ctx, &transport.Request{
		Op: op, Method: http.MethodGet, Path: path, Query: query, Page: pageIn(query),
	})
	if err != nil {
		return resp, err
	}
	if err := transport.Decode(resp.Body, out); err != nil {
		return resp, c.core.FailBody(ctx, op, transport.REST(http.MethodGet), resp.Status)
	}
	return resp, nil
}

// pageIn is the page a read asks for, so the per-request record carries it. It reads
// the query this family already built rather than taking a second parameter every
// call site would have to keep in step with it.
func pageIn(query url.Values) int {
	page, err := strconv.Atoi(query.Get(keyPage))
	if err != nil {
		return 1
	}
	return page
}

// sendAt performs one mutation under the call's repository and decodes its answer. It
// goes through the transport configured for exactly one attempt, because a replayed
// creation or merge is a duplicate the inter-mutation interval cannot undo.
func (c *Client) sendAt(ctx context.Context, op, method string, a *address, rest string, body, out any) error {
	resp, err := c.send(ctx, op, method, a, rest, body)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := transport.Decode(resp.Body, out); err != nil {
		return c.core.FailBody(ctx, op, transport.REST(method), resp.Status)
	}
	return nil
}

// sendForStatus is sendAt for a route whose answer is its status alone, an empty body
// whose code is the outcome.
func (c *Client) sendForStatus(ctx context.Context, op, method string, a *address, rest string, body any) (int, error) {
	resp, err := c.send(ctx, op, method, a, rest, body)
	if err != nil {
		return 0, err
	}
	return resp.Status, nil
}

// send performs one mutation, naming the successor a refusal met.
func (c *Client) send(ctx context.Context, op, method string, a *address, rest string, body any) (*transport.Response, error) {
	resp, err := c.core.Do(ctx, &transport.Request{
		Op: op, Method: method, Path: a.route(rest), Body: body,
	})
	if err != nil {
		c.nameRefusedSuccessor(a, resp, err)
	}
	return resp, err
}

// listKind is what a list's own route lets a caller select beside the page, which
// decides which list options it reads and which it refuses.
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
	// runList is the run listing: a fixed list whose route states its own
	// total, so its continuation is the run walk's rather than a page number.
	runList
)

// listOptions resolves one list call's options and refuses, before any request, the
// ones its route cannot apply. A state filter is refused with a local code on every
// list but a stateful one, because the endpoints behind the others fix the state,
// and an owner on every list but a scoped one, because the others address one
// repository or the credential's own.
//
// An owner here is ONE name, a user or an organization: neither product nests
// owners, so a path the shared form admits is refused by [forgeapi.ValidateOwner]
// before it reaches a request rather than sent as a name no instance can hold.
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
		if err := forgeapi.ValidateOwner(forgeapi.FamilyGitea, set.Owner); err != nil {
			return set, err
		}
	}
	if kind == statefulList && set.State == forgeapi.ListStateMerged && op == "ListIssues" {
		return set, transport.Local(forgeapi.CodeListStateInvalid,
			"an issue has no merged state")
	}
	return set, nil
}

// listing resolves one page-numbered list call: its options, the walk its
// continuation stands it at, and this product's query for it, every parameter but
// the page and the limit, which the page read sets once the instance's stated
// maximum is held. repo is the repository the call addresses, the zero reference on
// a list no repository addresses.
func (c *Client) listing(op string, repo forgeapi.RepoRef, kind listKind, opts []forgeapi.ListOption) (forgeapi.ListSettings, url.Values, pageWalk, error) {
	set, err := listOptions(op, kind, opts)
	if err != nil {
		return set, nil, pageWalk{}, err
	}
	walk, err := pageWalkAt(c.core.PageCall(op, repo, set), set.After)
	if err != nil {
		return set, nil, pageWalk{}, err
	}
	query := url.Values{}
	switch kind {
	case statefulList:
		query.Set("state", listState(set.State))
	case scopedList:
		query.Set("state", stateOpen)
		scope(query, set)
	}
	return set, query, walk, nil
}

// scope selects a cross-repository list's rows. The default is the boolean both
// products declare on the issue-search route for the rows the credential created,
// which the instance resolves against whoever the request authenticated as; an
// owner replaces it with the owner filter both declare on the same route, so the
// two scopes never ride one request.
func scope(query url.Values, set forgeapi.ListSettings) {
	if set.OwnerSet {
		query.Set("owner", set.Owner)
		return
	}
	query.Set("created", "true")
}

// listState is this product's own spelling of the state filter.
func listState(s forgeapi.ListState) string {
	switch s {
	case forgeapi.ListStateClosed:
		return stateClosed
	case forgeapi.ListStateAll:
		return "all"
	case forgeapi.ListStateMerged, forgeapi.ListStateOpen, forgeapi.ListStateUnknown:
		return stateOpen
	}
	return stateOpen
}

// readPage reads the page a walk stands at on a list no repository addresses.
func (c *Client) readPage(ctx context.Context, op, path string, query url.Values, walk *pageWalk, bound int, out any) error {
	if err := c.pageQuery(ctx, query, walk, bound); err != nil {
		return err
	}
	return c.readJSON(ctx, op, path, query, out)
}

// readPageAt reads the page a walk stands at on a list the call's repository
// addresses.
func (c *Client) readPageAt(ctx context.Context, op string, a *address, rest string, query url.Values, walk *pageWalk, bound int, out any) error {
	if err := c.pageQuery(ctx, query, walk, bound); err != nil {
		return err
	}
	return c.readAt(ctx, op, a, rest, query, out)
}

// pageQuery sets the page a walk stands at on its query, at the limit
// [Client.pageLimit] answers for the call's bound, and fixes that limit on the walk,
// which is what a short page is measured against and what its continuation carries.
func (c *Client) pageQuery(ctx context.Context, query url.Values, walk *pageWalk, bound int) error {
	limit, err := c.pageLimit(ctx, bound)
	if err != nil {
		return err
	}
	if refused := fixLimit(&walk.limit, limit); refused != nil {
		return refused
	}
	query.Set(keyPage, strconv.Itoa(walk.page))
	query.Set(keyLimit, strconv.Itoa(limit))
	return nil
}

// fixLimit holds a walk to the limit its page is about to be sent at. A walk no page
// has fixed takes it. A walk begun at another limit is refused before its page is
// sent, because its page number counts pages of the limit it began at, and the same
// bound answers another limit only on a connection whose instance states another
// maximum than the one the walk began under.
func fixLimit(began *int, limit int) error {
	if *began != 0 && *began != limit {
		return errorf(forgeapi.CodeCursorInvalid,
			"continuation was minted where its pages were sent at a limit of %d and this connection's instance sends %d for the same page bound, so the page it names counts pages of another size; a walk on this connection starts again from the first page",
			*began, limit)
	}
	*began = limit
	return nil
}

// pageWalk is where one call of a page-numbered list stands: the call its
// continuations name, the page it reads, and the limit the walk's pages are sent
// at, zero on a walk no page has fixed yet.
type pageWalk struct {
	call  transport.PageCall
	page  int
	limit int
}

// pageWalkAt reads where a page-numbered list call stands from its continuation, the
// empty one being the first page with no limit fixed. It refuses one this library
// did not mint and one another call minted, rather than resuming from garbage or at
// a row of another walk.
//
// It refuses the run listing's with a message of its own, which is a real case rather
// than a defensive one: this family mints both encodings, so a caller holding a run
// continuation can hand it to another list, and the message names the change.
func pageWalkAt(call transport.PageCall, c forgeapi.Cursor) (pageWalk, error) {
	if c == "" {
		return pageWalk{call: call, page: 1}, nil
	}
	if err := forgeapi.ValidateCursor(c); err != nil {
		return pageWalk{}, err
	}
	if strings.HasPrefix(string(c), runCursorPrefix) {
		return pageWalk{}, transport.Local(forgeapi.CodeCursorInvalid,
			"continuation was minted by this family's run listing and this call reads another list, so it names no position here")
	}
	position, err := call.Resume(c, cursorPrefix, 2)
	if err != nil {
		return pageWalk{}, err
	}
	page, limit := position[0], position[1]
	if page < 1 || limit < 0 || (limit == 0 && page != 1) {
		return pageWalk{}, transport.Local(forgeapi.CodeCursorInvalid, "continuation names no page and the limit its walk is sent at")
	}
	return pageWalk{call: call, page: page, limit: limit}, nil
}

// here is the continuation naming the page this walk stands at, which is the one an
// unpaged answer hands back.
func (w pageWalk) here() forgeapi.Cursor {
	return w.call.Mint(cursorPrefix, w.page, w.limit)
}

// next is the continuation for the page after one that served rows, empty where the
// list is complete. The witness is a page SHORT of the limit it was sent at: this
// product sends no next link a read can rely on, and a page that came back full is
// the only evidence that a remainder exists, which holds because the limit is never
// above the instance's stated maximum.
func (w pageWalk) next(rows int) forgeapi.Cursor {
	if rows < w.limit {
		return ""
	}
	return pageWalk{call: w.call, page: w.page + 1, limit: w.limit}.here()
}

// runWalk is where one page of the run listing stands in its walk: the call its
// continuations name, the page it reads, the rows the pages before it were served,
// and the limit every page of the walk is sent at, zero on a walk no page has fixed
// yet.
type runWalk struct {
	call   transport.PageCall
	page   int
	served int
	limit  int
}

// runWalkAt reads where a run-listing call stands from its continuation, the empty
// one being the first page with nothing served and no limit fixed. It refuses the
// page-numbered encoding, for the reason [runCursorPrefix] states, one another call
// minted, and a page, count or limit that is not one this library minted.
func runWalkAt(call transport.PageCall, c forgeapi.Cursor) (runWalk, error) {
	if c == "" {
		return runWalk{call: call, page: 1}, nil
	}
	if err := forgeapi.ValidateCursor(c); err != nil {
		return runWalk{}, err
	}
	if !strings.HasPrefix(string(c), runCursorPrefix) {
		return runWalk{}, transport.Local(forgeapi.CodeCursorInvalid,
			"continuation was minted by another of this family's lists and this call reads a repository's run listing, so it names no position here")
	}
	position, err := call.Resume(c, runCursorPrefix, 3)
	if err != nil {
		return runWalk{}, err
	}
	page, served, limit := position[0], position[1], position[2]
	if page < 1 || page > maxRunWalk || served < 0 || served > maxRunWalk || limit < 0 ||
		(limit == 0 && (page != 1 || served != 0)) {
		return runWalk{}, transport.Local(forgeapi.CodeCursorInvalid, "continuation names no page of a run walk, the rows it was served and its limit")
	}
	return runWalk{call: call, page: page, served: served, limit: limit}, nil
}

// here is the continuation naming the page this walk stands at, which is the one an
// unpaged answer hands back.
func (w runWalk) here() forgeapi.Cursor {
	return w.call.Mint(runCursorPrefix, w.page, w.served, w.limit)
}

// next is the continuation after a page that served rows against the listing's
// stated total, empty where the walk has been served the whole total or the page
// served none.
//
// The total decides it and a short page does not: an instance serves at most its
// own maximum page size whatever limit is asked for, so a page short of the limit
// while the total states more is a clamp rather than the end, and a full page that
// serves the total is the end rather than a page with a remainder behind it. An
// empty page is the end whatever the total states, because no further page of the
// walk can serve what this one did not, and continuing would spend a request a page
// for nothing.
func (w runWalk) next(rows, total int) forgeapi.Cursor {
	served := w.served + rows
	if rows == 0 || served >= total {
		return ""
	}
	return runWalk{call: w.call, page: w.page + 1, served: served, limit: w.limit}.here()
}

// pagePartial is the response-level marker for a list that reached its page cap.
// It always arrives with a continuation, so a consumer fetches the next page with
// that cursor rather than re-running the same capped call.
func (c *Client) pagePartial(rows int, next forgeapi.Cursor, pages int) *forgeapi.Partial {
	if next == "" || pages < c.core.Budget().ListPages {
		return nil
	}
	return c.partial(forgeapi.PartialPaginationCap, rows)
}

// partial mints one partial marker and counts it, which is the one place this
// family reports an incomplete result: the counter is what separates a partial the
// governor chose from one upstream imposed, and a marker minted without it is a
// partial no consumer's metrics can see.
func (c *Client) partial(reason forgeapi.PartialReason, fetched int) *forgeapi.Partial {
	if counters := c.core.Counters(); counters.PartialResult != nil {
		counters.PartialResult(forgeapi.FamilyGitea, reason)
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
