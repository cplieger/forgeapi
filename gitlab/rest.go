package gitlab

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
// smallest one that resumes a page-numbered list.
const cursorPrefix = "p"

// The page parameters every paged REST read of this product carries.
const (
	keyPage    = "page"
	keyPerPage = "per_page"
)

// headerNextPage is this product's own witness that a paged list continues: the
// page number to ask for next, empty on the last page. It is read INSTEAD of a
// short-page heuristic, because the product states the fact directly and a full
// page is only evidence by inference. Measured on gitlab.com: the projects, issues
// and releases lists all answer it beside x-total-pages.
const headerNextPage = "x-next-page"

// The two state spellings this product answers on a merge request and an issue,
// which are also two of the filter's own members.
const (
	stateOpened = "opened"
	stateClosed = "closed"
	stateMerged = "merged"
	// stateAll is the filter's wildcard, which is a member of the same enumeration
	// and never one row's own state.
	stateAll = "all"
)

// projectPath is the repository selector as this product's API takes it: the full
// namespace path as ONE path segment, with the separators percent-encoded.
//
// The encoding happens HERE, at request time, rather than being stored on the
// reference, because the reference's selector is the canonical one the derived
// identifier is computed from. Measured on gitlab.com, both other spellings answer
// 404: the decoded path reads as extra path segments and a doubly-encoded one
// resolves no project.
func projectPath(repo forgeapi.RepoRef) string {
	return url.PathEscape(repo.Selector)
}

// projectRoute is the escaped path of one project's own subtree.
func projectRoute(repo forgeapi.RepoRef, rest string) string {
	return "/projects/" + projectPath(repo) + rest
}

// checkRepo refuses a reference that is not safe to interpolate into a request
// path, before any request: a derived identifier is consumer-controlled input on
// its way into a URL. The separator bound is this family's own, because its
// selector is a nested namespace path rather than an owner-and-name pair.
func checkRepo(repo forgeapi.RepoRef) error {
	return forgeapi.ValidateSelector(forgeapi.FamilyGitLab, repo.Selector)
}

// unpagedList is the answer a list gives when its page bound admits no page at all:
// nothing is issued, and the marker is the one the page cap carries, with resume
// naming the page nobody asked for so the remainder is reachable without re-running a
// call this bound refuses.
//
// It is the literal zero the option publishes. A list that sent one page anyway
// would execute the opposite of what its own doc comment states, and clamping the
// bound up to one is the reading a single configuration door rules out.
//
// resume is the CALLER's, because this family mints two continuation encodings: the
// page-numbered one its REST lists resume from, and the document's own opaque cursor.
// On the document arm the first page IS the absence of a cursor, so a first call that
// admitted no page answers the marker with an empty continuation, which tells a
// consumer the whole list remains and that a bound is what it owes.
func (c *Client) unpagedList[T any](resume forgeapi.Cursor) (forgeapi.Page[T], bool) {
	if c.core.Budget().ListPages > 0 {
		return forgeapi.Page[T]{}, false
	}
	return forgeapi.Page[T]{
		Next:    resume,
		Partial: c.partial(forgeapi.PartialPaginationCap, 0),
	}, true
}

// readJSON performs one GET and decodes it, answering the response headers beside
// the error because two of this product's facts ride there rather than in the body:
// the continuation for a paged list and the budget signal.
//
// The read is UNCONDITIONAL, as every read this library issues is: nothing above the
// transport holds a response body to answer a not-modified response from, and no
// published return has a member that says unchanged. A not-modified answer arriving
// anyway is the unexpected status the core maps, which reaches this function as the
// error below.
func (c *Client) readJSON(ctx context.Context, op, path string, query url.Values, out any) (http.Header, error) {
	resp, err := c.do(ctx, &transport.Request{
		Op: op, Method: http.MethodGet, EscapedPath: path, Query: query, Page: pageIn(query),
	})
	if err != nil {
		return headerOf(resp), err
	}
	if err := transport.Decode(resp.Body, out); err != nil {
		return resp.Header, c.core.FailBody(ctx, op, transport.REST(http.MethodGet), resp.Status)
	}
	return resp.Header, nil
}

// do sends one request through the core, REST and document alike, and records a
// refusal for a permission or scope the credential lacks, which is where
// [Client.GrantCaps] learns of one at no request of its own.
func (c *Client) do(ctx context.Context, req *transport.Request) (*transport.Response, error) {
	resp, err := c.core.Do(ctx, req)
	var fe *forgeapi.Error
	if asForgeError(err, &fe) && fe.Code == forgeapi.CodeScopeInsufficient {
		c.rememberRefusal(fe)
	}
	return resp, err
}

// headerOf is the headers of a response that came back with a failure, which the
// caller still reads: this product answers its own metadata header on a refusal as
// well as on a success, and that header is what establishes the family.
func headerOf(resp *transport.Response) http.Header {
	if resp == nil {
		return nil
	}
	return resp.Header
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
// transport configured for exactly one attempt, because a replayed creation or
// merge is a duplicate the inter-mutation interval cannot undo.
func (c *Client) sendJSON(ctx context.Context, op, method, path string, body, out any) error {
	resp, err := c.do(ctx, &transport.Request{
		Op: op, Method: method, EscapedPath: path, Body: body,
	})
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
	// to what the credential created or to one group's projects.
	scopedList
)

// listing resolves one list call's options. A state filter is refused with a local
// code on every list but a stateful one, because the endpoints behind the others fix
// the state, and an owner on every list but a scoped one, because the others address
// one project or the credential's own.
//
// An owner here may be a PATH, because it names a group and a group nests: the shared
// form the root checks already admits the separator, and [scopedRoute] sends it as
// this product's encoded group id.
//
// It resolves the options and NOT the continuation, which is the caller's: this family
// mints two encodings, the page-numbered one its REST lists resume from and the list
// document's own opaque cursor, and a shared reading of one would refuse the other.
func listing(op string, kind listKind, opts []forgeapi.ListOption) (forgeapi.ListSettings, error) {
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
	if kind == statefulList && set.State == forgeapi.ListStateMerged && op == opListIssues {
		return set, transport.Local(forgeapi.CodeListStateInvalid,
			"an issue has no merged state")
	}
	return set, nil
}

// restQuery turns one REST list call's resolved options into this product's page
// query, and answers the walk its continuation stands it at beside it, so a caller can
// mint the continuation for the next page. repo is the repository the call
// addresses, the zero reference on a list no repository addresses. A scoped list
// sends the open state its construction fixes.
func (c *Client) restQuery(op string, repo forgeapi.RepoRef, set forgeapi.ListSettings, kind listKind) (url.Values, restWalk, error) {
	walk, err := restWalkAt(c.core.PageCall(op, repo, set), set.After)
	if err != nil {
		return nil, restWalk{}, err
	}
	query := url.Values{
		keyPage:    {strconv.Itoa(walk.page)},
		keyPerPage: {strconv.Itoa(set.PageBound)},
	}
	switch kind {
	case statefulList:
		query.Set("state", listState(set.State))
	case scopedList:
		query.Set("state", stateOpened)
	}
	return query, walk, nil
}

// scopedRoute is the route a cross-repository list reads for its scope: the
// instance-wide collection narrowed to what the credential created, or under an
// owner that group's own, which spans its subgroups. The group is its full path as
// ONE encoded segment, as a project is. A user's namespace has no such route, so it
// answers 404 as an absent group does, which the mapper reads as the unresolved
// owner.
func scopedRoute(set forgeapi.ListSettings, query url.Values, collection string) string {
	if set.OwnerSet {
		return "/groups/" + url.PathEscape(set.Owner) + "/" + collection
	}
	query.Set("scope", "created_by_me")
	return "/" + collection
}

// listState is this product's own spelling of the state filter, which is the same
// vocabulary its item state uses plus the wildcard.
func listState(s forgeapi.ListState) string {
	switch s {
	case forgeapi.ListStateClosed:
		return stateClosed
	case forgeapi.ListStateMerged:
		return stateMerged
	case forgeapi.ListStateAll:
		return stateAll
	case forgeapi.ListStateOpen, forgeapi.ListStateUnknown:
		return stateOpened
	}
	return stateOpened
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
	if strings.HasPrefix(string(c), documentPrefix) {
		return restWalk{}, transport.Local(forgeapi.CodeCursorInvalid,
			"continuation was minted by this connection's list document and this call reads REST, so it names no position here")
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

// next is the continuation for the page the next-page header names, empty where the
// list is complete.
func (w restWalk) next(header http.Header) forgeapi.Cursor {
	page, ok := nextPage(header)
	if !ok {
		return ""
	}
	return w.call.Mint(cursorPrefix, page)
}

// nextPage is the page after one response's, and whether there is one. The witness is
// this product's own next-page header rather than a short page: the header states the
// fact, and a page that came back full is only evidence of a remainder by inference.
func nextPage(header http.Header) (int, bool) {
	next := strings.TrimSpace(header.Get(headerNextPage))
	if next == "" {
		return 0, false
	}
	page, err := strconv.Atoi(next)
	if err != nil || page < 1 {
		return 0, false
	}
	return page, true
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
		counters.PartialResult(forgeapi.FamilyGitLab, reason)
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

// asForgeError reports whether err is this library's own error type, which is one
// of the three answers every operation can give.
func asForgeError(err error, out **forgeapi.Error) bool {
	fe, ok := err.(*forgeapi.Error)
	if !ok {
		return false
	}
	*out = fe
	return true
}
