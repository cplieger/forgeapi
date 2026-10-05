package github

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// maxStatusPageItems is the page size the degraded folded-status reads ask for, which
// is this product's own maximum response items: those reads' own pages are what the
// fold is computed over, so a smaller page spends more requests for the same verdict.
const maxStatusPageItems = 100

// maxRunPageItems is the page size the re-run's resolving read asks for. It is small
// because that read wants ONE run, the one carrying the head the caller pinned, and
// upstream applies the pin as a declared filter.
const maxRunPageItems = 20

// The request-body fields several mutations share, and the two document variables every
// repository-addressed document of this product takes.
const (
	keyState  = "state"
	keyTitle  = "title"
	keyBody   = "body"
	keyLabels = "labels"
	keyDraft  = "draft"
	varOwner  = "owner"
	varName   = "name"
)

// Whoami implements [forgeapi.Identity]. This product reports the credential's
// granted scopes in a response HEADER rather than in the account body, which
// was measured on a classic OAuth token, so a grant capability rests on
// evidence rather than on inference. A fine-grained token is documented to report
// an empty set there and was not measured, so on that credential kind the same
// capability degrades to unknown rather than to none. The account body's own
// email is the PUBLIC profile one and is null where the profile carries none.
func (c *Client) Whoami(ctx context.Context) (forgeapi.Account, error) {
	const op = "Whoami"
	ctx = c.core.Call(ctx, op)
	user, scopes, err := c.readUser(ctx, op)
	if err != nil {
		return forgeapi.Account{}, err
	}
	// The scope header this read already carried is what the grant capability
	// answers from, so recording it here is what prices that accessor at nothing
	// on a connection whose identity has been read.
	c.heldGrant(scopes)
	return forgeapi.Account{
		Login:  user.Login,
		Name:   user.Name,
		Email:  user.Email,
		WebURL: user.WebURL,
		Scopes: scopes,
	}, nil
}

// ListRepos implements [forgeapi.Repos].
//
// It asks for the repositories the CREDENTIAL can reach, which is what this product's
// own identity-scoped route answers, and it sends no affiliation or visibility filter
// of its own: the operation publishes the credential's repositories, and a filter
// would answer a narrower question than the one a consumer asked.
//
// Each row carries its own affordances, which is what keeps a listing from needing one
// accessor call per row. What a row cannot carry is the merge strategies: measured,
// this product's minimal repository representation omits all three flags, so a listed
// repository answers an empty set with its evidence saying why and a caller that needs
// them pays [Client.RepoAffordances].
func (c *Client) ListRepos(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Repository], error) {
	const op = "ListRepos"
	ctx = c.core.Call(ctx, op)
	query, walk, err := c.listing(op, forgeapi.RepoRef{}, fixedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Repository]{}, err
	}
	if bounded, ok := c.unpagedList[forgeapi.Repository](walk.here()); ok {
		return bounded, nil
	}
	var rows []restRepo
	resp, err := c.readJSON(ctx, op, "/user/repos", query, &rows)
	if err != nil {
		if deferred, ok := c.deferredPage[forgeapi.Repository](err); ok {
			return deferred, nil
		}
		return forgeapi.Page[forgeapi.Repository]{}, err
	}
	items := make([]forgeapi.Repository, 0, len(rows))
	for i := range rows {
		items = append(items, c.normalizeRepo(&rows[i]))
	}
	next := walk.next(resp.Header)
	return forgeapi.Page[forgeapi.Repository]{
		Items:   items,
		Next:    next,
		Partial: c.pagePartial(len(rows), next, walk.page),
	}, nil
}

// ListPRs implements [forgeapi.PullRequests].
//
// It reads the list document rather than the REST list, which returns neither
// mergeability field at all: the document carries the check rollup, the merge
// state, the head commit, the auto-merge request and the merge-queue entry in one
// call, measured at ONE rate-limit point for 20 pull-request rows and at three for
// 100. nodeCount tracks what the document REQUESTS rather than what came back, and
// moves without the cost: at 100 rows it read 2,300 with a labels page of 20 and
// 10,300 with the labels page of 100 this document selects, at three points both.
//
// Each row's check fold is BOUNDED to the first page of that row's contexts
// connection, which is what keeps the list at one request, and on this product that
// bound costs NO unknown: the connection's per-state counts describe the WHOLE
// collection, measured byte-identical across three pages of a 446-context collection,
// so the verdict and the six counts are exact from the first page and what the bound
// truncates is the check NAMES. A row whose connection reports a next page therefore
// carries a real verdict beside [forgeapi.PartialPaginationCap], which is what says
// the names are short rather than the verdict.
//
// It asks for at most [prListPageCeiling] rows a page whatever the page bound asks,
// for the reason that constant states, and a page of that size cost two points on
// the repository it was measured on. A page the ceiling shortened is an ordinary
// page: it carries the document's continuation and no partial marker, so the
// ceiling costs requests and never rows.
//
// This is the one read of this family with no REST arm, and that is its published
// price rather than an omission: the REST list carries no rollup and no merge state,
// so a fall-back would answer a different question, and a call that fell back inside
// itself would spend two requests where this row publishes one. A document refused
// here is this call's failure and marks the connection, so the two reads that do have
// a REST arm take it from their next call.
func (c *Client) ListPRs(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	const op = "ListPRs"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	owner, name, err := ownerName(repo)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	set, err := listOptions(op, statefulList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	call := c.core.PageCall(op, repo, set)
	after, err := decodeAfter(call, set.After)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	if bounded, ok := c.unpagedList[forgeapi.PullRequest](set.After); ok {
		return bounded, nil
	}
	var payload docPayload
	result, err := c.execute(ctx, op, prList, map[string]any{
		varOwner: owner,
		varName:  name,
		"first":  min(set.PageBound, prListPageCeiling),
		"after":  nullableString(after),
		"states": documentStates(set.State),
	}, &payload)
	if err != nil {
		if deferred, ok := c.deferredPage[forgeapi.PullRequest](err); ok {
			return deferred, nil
		}
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	if payload.Repository == nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, c.core.Fail(ctx, op, transport.Document(http.MethodPost),
			forgeapi.CodeRepoOrPRNotVisible, forgeapi.KindNotFound, http.StatusOK,
			"the document resolved no repository for this selector")
	}
	rows := payload.Repository.PullRequests
	if rows == nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, c.core.FailBody(ctx, op, transport.Document(http.MethodPost), http.StatusOK)
	}
	canonical, successor := c.documentRepo(payload.Repository, repo)
	items := make([]forgeapi.PullRequest, 0, len(rows.Nodes))
	for i := range rows.Nodes {
		items = append(items, c.rowWithFold(&rows.Nodes[i], canonical))
	}
	page := forgeapi.Page[forgeapi.PullRequest]{Items: items, Next: documentNext(call, rows.PageInfo), Successor: successor}
	if result.Partial {
		page.Partial = c.partial(forgeapi.PartialGraphQLPartial, len(items))
	}
	return page, nil
}

// rowWithFold is one list row, with the bounded fold's own marker where the row's
// contexts connection extends past the page the document asked for. The verdict is
// exact either way, which is why the marker rides the row rather than blanking it.
func (c *Client) rowWithFold(node *docPullRequest, repo forgeapi.RepoRef) forgeapi.PullRequest {
	row := c.normalizeDocPull(node, repo)
	if fold := c.foldRollup(rollupOf(node)); fold.truncated {
		row.Partial = c.partial(forgeapi.PartialPaginationCap, fold.total)
	}
	return row
}

// documentRepo is the repository a document's rows belong to, taken from the canonical
// name the selection returns rather than from the selector asked for, and the successor
// where the two differ.
//
// That comparison is what marks a rename on this arm: the repository selection resolves
// a renamed repository silently at HTTP 200, so there is no redirect to record, and the
// canonical name the document answers IS the successor's own selector, which is the one
// place on this product where a successor costs no further read.
func (c *Client) documentRepo(repo *docRepository, asked forgeapi.RepoRef) (canonical forgeapi.RepoRef, successor *forgeapi.RepoRef) {
	if repo.NameWithOwner == "" || repo.NameWithOwner == asked.Selector {
		return asked, nil
	}
	moved := repoRef(repo.NameWithOwner)
	return moved, &moved
}

// ListMyPRs implements [forgeapi.PullRequests].
//
// This product answers it through a SEARCH document of its own, whose rows carry
// their own repository. Under [forgeapi.WithOwner] the owner qualifier replaces the
// author keyword and resolves an organization as it resolves a user; an owner the
// index holds no account for answers an empty page, measured, so this family cannot
// answer [forgeapi.CodeOwnerUnresolved] without a resolving read the price does not
// carry.
//
// It asks for at most [prSearchPageCeiling] rows a page whatever the page bound asks,
// for the reason that constant states, and a page of that size cost one point under
// both owners measured, so the cost moves with the page, never with the repository
// count. The search answers no result past its 1,000th, so a walk ends there with no
// continuation, and the page that ends it carries [forgeapi.PartialResultWindow]
// wherever the search's own total states more than the walk was served.
func (c *Client) ListMyPRs(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	const op = "ListMyPRs"
	ctx = c.core.Call(ctx, op)
	set, err := listOptions(op, scopedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	call := c.core.PageCall(op, forgeapi.RepoRef{}, set)
	after, served, err := decodeSearchAfter(set.After, call)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	if bounded, ok := c.unpagedList[forgeapi.PullRequest](set.After); ok {
		return bounded, nil
	}
	query := searchQuery(searchPulls, set)
	set.PageBound = min(set.PageBound, prSearchPageCeiling)
	var payload docPayload
	result, err := c.execute(ctx, op, prMine, searchVariables(query, set, after), &payload)
	if err != nil {
		if deferred, ok := c.deferredPage[forgeapi.PullRequest](err); ok {
			return deferred, nil
		}
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	if payload.Search == nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, c.core.FailBody(ctx, op, transport.Document(http.MethodPost), http.StatusOK)
	}
	rows := payload.Search
	nodes, err := c.searchRows(ctx, op, rows.Nodes, (*docSearchPull).typename, typePullRequest, rows.IssueCount, result.Partial)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	items := make([]forgeapi.PullRequest, 0, len(nodes))
	for _, node := range nodes {
		items = append(items, c.rowWithFold(&node.docPullRequest, rowRepo(node.Repository)))
	}
	return c.searchPage(items, rows.PageInfo, searchWalk{call: call, served: served}, rows.IssueCount, result.Partial), nil
}

// The union member type names the two search documents select their rows by.
const (
	typePullRequest = "PullRequest"
	typeIssue       = "Issue"
)

func (n *docSearchPull) typename() string  { return n.Typename }
func (n *docSearchIssue) typename() string { return n.Typename }

// searchRows is one search page's nodes of the type its document selects, or the
// refusal of a page that is not one a search can answer.
//
// The total is the search's own count of its results, so one below zero counts
// nothing and is refused as the malformed answer it is, never read as a complete
// page. A node is a nullable member of a union: a null one, or a member of another
// type, is a row the document did not answer. Beside the envelope errors that null a
// node it is omitted, and the page carries the document's partial marker; with no
// error to account for it, the page is malformed and is refused.
func (c *Client) searchRows[N any](ctx context.Context, op string, nodes []*N, typeOf func(*N) string, want string, total int, partial bool) ([]*N, error) {
	if total < 0 {
		return nil, c.core.Fail(ctx, op, transport.Document(http.MethodPost), forgeapi.CodeValidation, forgeapi.KindUpstream, http.StatusOK,
			"the search answered an issueCount below zero, which counts nothing")
	}
	rows := make([]*N, 0, len(nodes))
	for _, node := range nodes {
		if node != nil && typeOf(node) == want {
			rows = append(rows, node)
			continue
		}
		if !partial {
			return nil, c.core.Fail(ctx, op, transport.Document(http.MethodPost), forgeapi.CodeValidation, forgeapi.KindUpstream, http.StatusOK,
				"the search answered a row that is not a "+want+" with no error beside it to say why")
		}
	}
	return rows, nil
}

// searchWalk is where one page of a cross-repository search stands in its walk: the
// call its continuations name and the rows the pages before this one were served.
type searchWalk struct {
	call   transport.PageCall
	served int
}

// searchPage is one page of a cross-repository search: its rows, the continuation
// carrying what the walk has been served, and the marker of the page that ends a walk.
//
// A page whose document answered errors beside its data carries the document's own
// marker, because that says this page's rows are in question, which outranks a
// statement about rows beyond it. Otherwise a page minting no continuation while the
// search's stated total is more than the walk was served carries the result window,
// counting the whole walk through the continuation rather than one page.
func (c *Client) searchPage[T any](items []T, info docPageInfo, walk searchWalk, total int, documentPartial bool) forgeapi.Page[T] {
	served := walk.served + len(items)
	page := forgeapi.Page[T]{Items: items, Next: searchNext(info, walk.call, served)}
	switch {
	case documentPartial:
		page.Partial = c.partial(forgeapi.PartialGraphQLPartial, len(items))
	case page.Next == "" && total > served:
		page.Partial = c.partial(forgeapi.PartialResultWindow, served)
		page.Partial.OmittedAtLeast = total - served
	}
	return page
}

// rowRepo is one cross-repository row's own repository, which the search documents
// select per row because the call addresses none. A row whose selection answered no
// repository carries the family alone, so a consumer can tell a row it cannot act on
// from one it can.
func rowRepo(repo *docRepoRef) forgeapi.RepoRef {
	if repo == nil || repo.NameWithOwner == "" {
		return forgeapi.RepoRef{Family: forgeapi.FamilyGitHub}
	}
	return repoRef(repo.NameWithOwner)
}

// documentNext is the continuation one page of a document connection mints for call,
// empty on its last page.
func documentNext(call transport.PageCall, info docPageInfo) forgeapi.Cursor {
	if !info.HasNextPage {
		return ""
	}
	return encodeAfter(call, info.EndCursor)
}

// ListMyIssues implements [forgeapi.Issues].
//
// It is the issue twin of [Client.ListMyPRs]: the same search connection, scoped the
// same way under either scope, priced the same way and ended the same way at the
// search's window, through a document of its own whose rows are issues and carry their
// own repository. It asks for the page bound as given, since a page of 100 of its rows
// answered under an owner where the pull-request search's did not. An owner the
// search index holds no account for answers an empty page with no error here too,
// measured.
func (c *Client) ListMyIssues(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Issue], error) {
	const op = "ListMyIssues"
	ctx = c.core.Call(ctx, op)
	set, err := listOptions(op, scopedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	call := c.core.PageCall(op, forgeapi.RepoRef{}, set)
	after, served, err := decodeSearchAfter(set.After, call)
	if err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	if bounded, ok := c.unpagedList[forgeapi.Issue](set.After); ok {
		return bounded, nil
	}
	query := searchQuery(searchIssues, set)
	var payload docIssuePayload
	result, err := c.execute(ctx, op, issueMine, searchVariables(query, set, after), &payload)
	if err != nil {
		if deferred, ok := c.deferredPage[forgeapi.Issue](err); ok {
			return deferred, nil
		}
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	if payload.Search == nil {
		return forgeapi.Page[forgeapi.Issue]{}, c.core.FailBody(ctx, op, transport.Document(http.MethodPost), http.StatusOK)
	}
	rows := payload.Search
	nodes, err := c.searchRows(ctx, op, rows.Nodes, (*docSearchIssue).typename, typeIssue, rows.IssueCount, result.Partial)
	if err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	items := make([]forgeapi.Issue, 0, len(nodes))
	for _, node := range nodes {
		items = append(items, c.normalizeDocIssue(&node.docIssue))
	}
	return c.searchPage(items, rows.PageInfo, searchWalk{call: call, served: served}, rows.IssueCount, result.Partial), nil
}

// ReadPR implements [forgeapi.PullRequests].
//
// It reads the single-pull-request document, whose field set is its own: the
// merge state is the slow field on a large repository, so a read must not pay a
// list's cost and a list must not pay a read's.
//
// It is one to three requests: the document, plus a further page of the contexts
// connection wherever that connection has one, because the fold on a
// single-pull-request read is COMPLETE and bounded by
// [forgeapi.Budget.StatusPages]. The caller named one pull request and is owed a
// verdict, so this is the path that pays for one; a row of [Client.ListPRs] is
// not. Measured on a 446-context pull request, that bound is reached on a live
// repository and leaves the names short with the marker saying so.
//
// The degraded REST path spends ONE, plus the one further read that resolves the
// successor where that record answered from another address, and it is the arm a
// connection takes once it
// has already discovered this instance refusing the document: no page of a fold
// follows it, because REST carries no checks rollup to follow and
// [forgeapi.ActionState.Checks] is [forgeapi.CheckUnknown] for that connection. The
// call that MEETS the refusal fails with it rather than falling back inside itself,
// so a discovery is never a second request charged to whichever operation happened
// to arrive first.
//
// Its two absence answers are separated by evidence rather than by status, and
// the evidence is free here because the document already decodes it: a resolved
// repository with no pull request is [forgeapi.CodePRNotFound], a null repository
// is [forgeapi.CodeRepoOrPRNotVisible], and the degraded REST path reports the
// union because there the status is all there is.
func (c *Client) ReadPR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	const op = "ReadPR"
	ctx = c.core.Call(ctx, op)
	read, err := c.readPull(ctx, op, repo, pr, true)
	if err != nil {
		return forgeapi.PullRequest{}, err
	}
	if read.successor != nil {
		return forgeapi.PullRequest{}, c.stale(ctx, op, http.MethodGet, http.StatusOK, read.successor)
	}
	return read.item, nil
}

// pullRead is one single-pull-request read's answer: the normalized pull request, the
// merged verdict beside it, which the document carries as a boolean of its own, and
// the successor where the read reached its answer from another address.
//
// The successor is handed back rather than acted on here, because the two operations
// this read serves declare different carriers for it: one publishes it on its own
// answer and the other has nowhere to put it and refuses instead.
type pullRead struct {
	successor *forgeapi.RepoRef
	item      forgeapi.PullRequest
	merged    forgeapi.Support
}

// readPull is the single-pull-request read three operations share: the document where
// this connection's documents work, and the REST record where they do not.
//
// fold says whether the caller wants the complete check fold, which is what separates
// the read that RETURNS checks from the merge-state read that returns none: following
// the contexts connection's pages costs a request each, so the operation that publishes
// no checks does not pay for them.
func (c *Client) readPull(ctx context.Context, op string, repo forgeapi.RepoRef, pr forgeapi.PRRef, fold bool) (pullRead, error) {
	if err := c.checkRepo(repo); err != nil {
		return pullRead{}, err
	}
	if pr.Number <= 0 {
		return pullRead{}, errorf(forgeapi.CodeRepoRefInvalid, "pull-request number is not positive")
	}
	if c.isDegraded() {
		return c.degradedRead(ctx, op, repo, pr)
	}
	// A document refused at runtime is this call's own failure and the CONNECTION's
	// record, never a second request inside the same call: a call that fell back here
	// would spend the refusal plus the REST read where the row publishes an exact
	// price, and that contradiction is what the sibling GraphQL family's own table row
	// rules out. Every later call takes the REST arm from the start.
	return c.documentRead(ctx, op, repo, pr, fold)
}

// documentRead is the single-pull-request document read, with the complete fold
// following the contexts connection's pages up to the published bound.
func (c *Client) documentRead(ctx context.Context, op string, repo forgeapi.RepoRef, pr forgeapi.PRRef, follow bool) (pullRead, error) {
	owner, name, err := ownerName(repo)
	if err != nil {
		return pullRead{}, err
	}
	vars := map[string]any{varOwner: owner, varName: name, "number": pr.Number, "checksAfter": nil}
	var payload docPayload
	if _, execErr := c.execute(ctx, op, prRead, vars, &payload); execErr != nil {
		return pullRead{}, execErr
	}
	node, err := c.pullNode(ctx, op, &payload)
	if err != nil {
		return pullRead{}, err
	}
	canonical, _ := c.documentRepo(payload.Repository, repo)
	item := c.normalizeDocPull(node, canonical)
	folded := c.foldRollup(rollupOf(node))
	if follow {
		followed, followErr := c.followContexts(ctx, op, vars, &folded)
		if followErr != nil {
			return pullRead{}, followErr
		}
		folded = followed
	}
	c.applyFold(&item, &folded)
	return pullRead{item: item, merged: supportOf(node.Merged)}, nil
}

// pullNode is the pull request one document read resolved, with the two absence
// answers the envelope separates: a null repository is invisible, and a resolved
// repository with no pull request is a number that does not exist.
func (c *Client) pullNode(ctx context.Context, op string, payload *docPayload) (*docPullRequest, error) {
	if payload.Repository == nil {
		return nil, c.core.Fail(ctx, op, transport.Document(http.MethodPost),
			forgeapi.CodeRepoOrPRNotVisible, forgeapi.KindNotFound, http.StatusOK,
			"the document resolved no repository for this selector")
	}
	if payload.Repository.PullRequest == nil {
		return nil, c.core.Fail(ctx, op, transport.Document(http.MethodPost),
			forgeapi.CodePRNotFound, forgeapi.KindNotFound, http.StatusOK,
			"the document resolved the repository and no pull request with that number")
	}
	return payload.Repository.PullRequest, nil
}

// followContexts completes one pull request's fold by asking the same document for the
// next page of its contexts connection, up to the published bound.
//
// The first page arrived with the read itself, so the bound counts PAGES rather than
// extra requests: a bound of three follows two further pages, which is the figure the
// published range covers. A fold still short at the bound keeps its exact verdict and
// carries the pagination marker, because on this product the counts describe the whole
// collection and what is short is the names.
func (c *Client) followContexts(ctx context.Context, op string, vars map[string]any, held *fold) (fold, error) {
	out := *held
	for pages := 1; out.truncated && pages < c.core.Budget().StatusPages; pages++ {
		if out.cursor == "" {
			break
		}
		next := maps.Clone(vars)
		next["checksAfter"] = out.cursor
		var payload docPayload
		if _, err := c.execute(ctx, op, prRead, next, &payload); err != nil {
			return out, err
		}
		node, err := c.pullNode(ctx, op, &payload)
		if err != nil {
			return out, err
		}
		page := c.foldRollup(rollupOf(node))
		out = merged(&out, &page)
	}
	return out, nil
}

// merged joins a further page of a fold onto the pages already held: the context rows
// accumulate and the truncation is the LAST page's own.
//
// Whether the counts accumulate is what the exactness says. Counts the connection
// reported over its whole collection are the same on every page, measured
// byte-identical across three, so adding them again would multiply the collection by
// the pages read; counts folded over nodes are that page's and do add.
func merged(held, page *fold) fold {
	out := *held
	out.contexts = append(out.contexts, page.contexts...)
	out.truncated = page.truncated
	out.cursor = page.cursor
	if held.exact || page.exact {
		if !held.exact {
			out.passing, out.failing, out.pending = page.passing, page.failing, page.pending
			out.neutral, out.unknown, out.total = page.neutral, page.unknown, page.total
			out.exact = true
		}
		out.state = out.verdict()
		return out
	}
	out.passing += page.passing
	out.failing += page.failing
	out.pending += page.pending
	out.neutral += page.neutral
	out.unknown += page.unknown
	out.total += page.total
	out.state = out.verdict()
	return out
}

// applyFold puts one fold's verdict and counts on a pull request, with the pagination
// marker where the collection extends past what was read.
//
// The marker is the same one the list's rows and the folded status carry, on the same
// terms: the counts describe the whole collection on this product, so what the marker
// says is short is the context NAMES and never the verdict.
func (c *Client) applyFold(item *forgeapi.PullRequest, folded *fold) {
	item.Action.Checks = folded.state
	item.Action.ChecksPassing = folded.passing
	item.Action.ChecksFailing = folded.failing
	item.Action.ChecksPending = folded.pending
	item.Action.ChecksNeutral = folded.neutral
	item.Action.ChecksUnknown = folded.unknown
	item.Action.ChecksTotal = folded.total
	if folded.truncated {
		item.Partial = c.partial(forgeapi.PartialPaginationCap, len(folded.contexts))
	}
}

// degradedRead is the single-pull-request REST read a connection whose document was
// refused falls back to. Its absence answer is the UNION, because one REST status
// carries no evidence separating a pull request that does not exist from a repository
// the credential cannot see, and mapping on the message text is the guess this library
// refuses. Its check state is unknown, because that route carries no rollup at all.
func (c *Client) degradedRead(ctx context.Context, op string, repo forgeapi.RepoRef, pr forgeapi.PRRef) (pullRead, error) {
	var record restPull
	var moved rename
	path := repoRoute(repo, "/pulls/"+strconv.Itoa(pr.Number))
	if _, err := c.readMovedJSON(ctx, op, path, nil, &record, &moved); err != nil {
		return pullRead{}, err
	}
	return pullRead{
		item:      c.normalizeRESTPull(&record, repo),
		successor: moved.to,
		merged:    supportOf(record.Merged),
	}, nil
}

// CreatePR implements [forgeapi.PullRequests].
//
// It is ONE request where the caller names no label and TWO where it does, which is
// what this product's creation takes: measured, that route declares no labels
// parameter at all, so a creation that sent them would have them ignored and a
// creation that dropped them would succeed without doing what was asked. Applying
// them is the ISSUES route acting on the created pull request's own number, which is
// the second request this operation's price states. A request of either that answers
// from another address spends the one further read that resolves the successor, which
// is the third.
//
// A failure applying them returns the created pull request BESIDE the error, which is
// the one shape on this surface where a non-nil error arrives with an answer: the
// creation happened, so an error alone would hide an object the caller now owns, and
// the error names what was not done to it. The pull request carries
// [forgeapi.PartialLabelsNotApplied] on its own marker, counting every label named,
// because the labels request applies them all or none.
//
// The created record carries no label row of its own either way, measured on the
// creation's own response, so a caller that renders the labels reads the pull request
// again; the departure row of this operation on this product says so.
//
// The draft flag IS a field here, unlike the family that marks a draft with a title
// prefix.
//
//nolint:gocritic // hugeParam: the record is the published signature's own parameter
func (c *Client) CreatePR(ctx context.Context, repo forgeapi.RepoRef, req forgeapi.NewPullRequest) (forgeapi.PullRequest, error) {
	const op = "CreatePR"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.PullRequest{}, err
	}
	body := map[string]any{
		keyTitle: req.Title,
		keyBody:  req.Body,
		"head":   req.SourceBranch,
		"base":   req.TargetBranch,
		keyDraft: req.Draft,
	}
	var record restPull
	if err := c.sendJSON(ctx, op, http.MethodPost, repoRoute(repo, "/pulls"), body, &record); err != nil {
		return forgeapi.PullRequest{}, err
	}
	item := c.normalizeMutatedPull(&record, repo)
	if len(req.Labels) == 0 {
		return item, nil
	}
	if err := c.applyLabels(ctx, op, repo, record.Number, req.Labels); err != nil {
		item.Partial = c.partial(forgeapi.PartialLabelsNotApplied, 0)
		item.Partial.OmittedAtLeast = len(req.Labels)
		return item, err
	}
	return item, nil
}

// applyLabels puts the labels a creation named on the object it created, through the
// route this product declares for them, which takes their NAMES and is addressed by
// the object's own number under the issues path on a pull request as on an issue.
func (c *Client) applyLabels(ctx context.Context, op string, repo forgeapi.RepoRef, number int, labels []string) error {
	if number <= 0 {
		return errorf(forgeapi.CodeValidation, "the creation answered no number, so the labels have nothing to address")
	}
	path := repoRoute(repo, "/issues/"+strconv.Itoa(number)+"/labels")
	return c.sendJSON(ctx, op, http.MethodPost, path, map[string]any{keyLabels: labels}, nil)
}

// ClosePR implements [forgeapi.PullRequests].
func (c *Client) ClosePR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	return c.setPullState(ctx, "ClosePR", repo, pr, stateClosed)
}

// ReopenPR implements [forgeapi.PullRequests].
func (c *Client) ReopenPR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	return c.setPullState(ctx, "ReopenPR", repo, pr, stateOpen)
}

// setPullState is the one mutation both lifecycle transitions make. This product takes
// the TARGET state rather than an event, so the two transitions send the two members of
// its own state enumeration.
//
// Measured on both of them: the response is the same forty-six-key object the creation
// answers, so what differs per operation is the values rather than the field set.
func (c *Client) setPullState(ctx context.Context, op string, repo forgeapi.RepoRef, pr forgeapi.PRRef, state string) (forgeapi.PullRequest, error) {
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.PullRequest{}, err
	}
	if pr.Number <= 0 {
		return forgeapi.PullRequest{}, errorf(forgeapi.CodeRepoRefInvalid, "pull-request number is not positive")
	}
	var record restPull
	path := repoRoute(repo, "/pulls/"+strconv.Itoa(pr.Number))
	if err := c.sendJSON(ctx, op, http.MethodPatch, path, map[string]any{keyState: state}, &record); err != nil {
		return forgeapi.PullRequest{}, err
	}
	return c.normalizeMutatedPull(&record, repo), nil
}

// RerunFailedChecks implements [forgeapi.PullRequests].
//
// It is TWO requests: the workflow run carrying the head the caller pinned has to be
// resolved before it can be retried, because this product's retry verb is addressed by
// a server-allocated run id rather than by a commit. A request of either that answers
// from another address spends the one further read that resolves the successor, and the
// resolving read spends it in place of the retry where the refusal is what the caller
// gets.
//
// Whether this instance serves the verb is [forgeapi.CapRerunChecks] from
// [Client.ConnectionCaps], and the measurement found that question scoped wrong for
// this product: the runner is enabled per REPOSITORY rather than per instance or per
// version, no connection-scope read names it, the read that answers it for one
// repository needs admin, and a workflow-runs read answers 200 whether the runner is
// enabled or merely unused. So the connection-scope verdict is unknown, that capability
// is what a consumer reads to render the control with its reason, and what refuses a
// re-run here is the instance's own answer rather than a verdict this library holds. No
// request is spent obtaining one, which is what keeps this figure the same on a fresh
// connection as on a used one.
//
// The head pin binds and costs no request of its own: the resolving read is filtered BY
// the caller's SHA, which this product declares as a parameter of that route, and a SHA
// no run carries means the pull request has moved since the caller's row was rendered.
// That is refused rather than retried, because a re-run can carry deployment side
// effects and a row displaying one commit's red status must not act on another's. An
// empty SHA means the forge reported no head, and the re-run then proceeds against the
// FIRST run the instance returns, which is the one case where the pin is unavailable
// rather than waived.
//
// The verb retries the failed jobs of that run rather than the whole run, which is what
// this operation publishes.
//
//nolint:revive // unused-parameter: the pull-request reference is the published signature's; this product's route is addressed by the head commit's own workflow run, which the head SHA pins
func (c *Client) RerunFailedChecks(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef, headSHA string) error {
	const op = "RerunFailedChecks"
	ctx = c.core.CallWrite(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return err
	}
	if headSHA != "" {
		if err := forgeapi.ValidateRef(headSHA); err != nil {
			return err
		}
	}
	query := url.Values{keyPerPage: {strconv.Itoa(maxRunPageItems)}}
	if headSHA != "" {
		query.Set("head_sha", headSHA)
	}
	var moved rename
	runs, resp, err := c.readRuns(ctx, op, repo, query, &moved)
	if err != nil {
		return err
	}
	if moved.to != nil {
		return c.stale(ctx, op, http.MethodGet, resp.Status, moved.to)
	}
	run, ok := pickRun(runs.Runs, headSHA)
	if !ok {
		return c.core.Fail(ctx, op, transport.REST(http.MethodGet), forgeapi.CodeStaleHead, forgeapi.KindConflict, http.StatusOK,
			"no workflow run carries the head this caller pinned")
	}
	path := repoRoute(repo, "/actions/runs/"+strconv.FormatInt(run.ID, 10)+"/rerun-failed-jobs")
	return c.sendJSON(ctx, op, http.MethodPost, path, nil, nil)
}

// pickRun is the head pin read off the resolving read's own rows: the run whose head
// matches what the caller pinned, or the FIRST row the instance returned where the
// caller brought no pin.
//
// That second arm claims no more than it knows. This product's runs route declares a
// head-SHA filter and returns its rows newest first, and this read sends no other
// ordering, so the first row is the instance's own choice. The PINNED arm is sound for a
// different reason: the SHA is a declared filter, so upstream applies it and the
// comparison here is a second check rather than the only one.
func pickRun(runs []restWorkflowRun, headSHA string) (restWorkflowRun, bool) {
	for i := range runs {
		if runs[i].ID == 0 {
			continue
		}
		if headSHA == "" || runs[i].HeadSHA == headSHA {
			return runs[i], true
		}
	}
	return restWorkflowRun{}, false
}

// readRuns is the one read of a repository's workflow runs, which the re-run and the
// run listing share: the re-run filters it by the head it pins, and the listing pages
// it with no head filter at all. moved is what the read learned about the address,
// which the two callers act on differently: the re-run has no carrier for a successor
// and refuses, and the listing publishes it beside the rows.
func (c *Client) readRuns(ctx context.Context, op string, repo forgeapi.RepoRef, query url.Values, moved *rename) (restWorkflowRuns, *transport.Response, error) {
	var runs restWorkflowRuns
	resp, err := c.readMovedJSON(ctx, op, repoRoute(repo, "/actions/runs"), query, &runs, moved)
	return runs, resp, err
}

// ListRuns implements [forgeapi.Checks].
//
// A run is an Actions workflow run, the row the re-run resolves its run from: its
// state folds status and conclusion, its branch is the head branch, its name the
// workflow's display name. A page is one request, plus one read resolving the
// successor where the route answered from another address, published on
// [forgeapi.Page.Successor]. It is not gated on [forgeapi.CapRerunChecks], the
// re-run verb's, since every repository answers the route.
func (c *Client) ListRuns(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Run], error) {
	const op = "ListRuns"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	query, walk, err := c.listing(op, repo, fixedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	if bounded, ok := c.unpagedList[forgeapi.Run](walk.here()); ok {
		return bounded, nil
	}
	var moved rename
	runs, resp, err := c.readRuns(ctx, op, repo, query, &moved)
	if err != nil {
		if deferred, ok := c.deferredPage[forgeapi.Run](err); ok {
			return deferred, nil
		}
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	items := make([]forgeapi.Run, 0, len(runs.Runs))
	for i := range runs.Runs {
		items = append(items, c.normalizeRun(&runs.Runs[i], repo))
	}
	next := walk.next(resp.Header)
	return forgeapi.Page[forgeapi.Run]{
		Items:     items,
		Next:      next,
		Partial:   c.pagePartial(len(runs.Runs), next, walk.page),
		Successor: moved.to,
	}, nil
}

// MergePR implements [forgeapi.Merges].
//
// This is the family whose merge is ASYNCHRONOUS, and it is why a merge has an
// outcome rather than only a failure: the request is accepted and runs in the
// background, answering 202 with a handle, and it answers immediately where the pull
// request is already merged.
//
// The handle this product mints for a background merge is not held: the caller
// reads the merge back through [Client.MergeStatus] on the same pull request, which
// reads the pull request's document. This method does not poll. A queued merge routinely outlives the per-operation
// deadline, so polling inside the mutation would leave no bound left to publish.
// It also issues no second request to read a queue position: the position lives
// on the queue entry the list document already selects, so it arrives with the
// next list read and reads [forgeapi.QueuePositionUnknown] in between. The 202's own
// body carries no queue key of any kind, measured, which is why this operation's queue
// verdict is unknown rather than the neutral member.
//
// Two things the request sends are decisions rather than plumbing. The head pin is
// mandatory on every family, because a merge sent without a SHA merges whatever the
// head is now; here it rides `sha`, the key the route reads, and the pending answer
// echoes it as `expected_head_sha`. And nothing that DEFERS the merge is sent unless
// the caller set [forgeapi.MergeRequest.AutoMerge]: a merge this product accepts
// completes in the background on its own, so a deferral nobody asked for would answer
// a merge nobody asked to wait for, which is the defect that started this library.
//
// A merge asked to wait reads the pull request's REST record first, because this
// product arms an auto-merge only on a pull request that cannot merge now and refuses
// to arm one that can: a state that is mergeable now merges as above, and any other is
// armed through the enableAutoMerge document with the pin as its expected head, which
// answers [forgeapi.MergeOutcomeEnqueued]. An arm the forge refuses is
// [forgeapi.CodeNotMergeable], its cause named in human text alone, a credential
// that may not merge included: no capture yet witnesses that refusal's body.
// [forgeapi.MergeRequest.DeleteBranch] reaches neither route, which take no such
// field; the repository's own delete-branch-on-merge setting decides.
//
// A 409 is the route's answer to a merge sent while another is pending: its body names
// that merge's handle, so it is [forgeapi.MergeOutcomeInFlight] under
// [forgeapi.CodeAlreadyEnqueued] rather than a refusal.
func (c *Client) MergePR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef, req forgeapi.MergeRequest) (forgeapi.MergeOutcome, error) {
	const op = opMergePR
	ctx = c.core.CallWrite(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.MergeOutcome{}, err
	}
	if pr.Number <= 0 {
		return forgeapi.MergeOutcome{}, errorf(forgeapi.CodeRepoRefInvalid, "pull-request number is not positive")
	}
	if req.HeadSHA == "" {
		return forgeapi.MergeOutcome{}, errorf(forgeapi.CodeMissingSHA, "a merge carries no head SHA")
	}
	if err := forgeapi.ValidateRef(req.HeadSHA); err != nil {
		return forgeapi.MergeOutcome{}, err
	}
	method, err := mergeMethod(req)
	if err != nil {
		return forgeapi.MergeOutcome{}, err
	}
	if req.AutoMerge {
		return c.autoMerge(ctx, repo, pr, req.HeadSHA, method)
	}
	return c.mergeNow(ctx, repo, pr, req.HeadSHA, method)
}

// mergeableNow is the mergeable states this product merges at once and refuses to arm
// an auto-merge on, in the REST record's spelling: the MergeStateStatus members its
// reference describes as mergeable (CLEAN, HAS_HOOKS, UNSTABLE).
var mergeableNow = []string{"clean", "has_hooks", "unstable"}

// autoMerge is a merge asked to wait for its requirements. The pull request's own
// record decides between merging now and arming, read where the merge is addressed, so
// a repository that answers from another address is the stale refusal a write gets.
func (c *Client) autoMerge(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef, head, method string) (forgeapi.MergeOutcome, error) {
	var record restPull
	if err := c.readRepoJSON(ctx, opMergePR, repoRoute(repo, "/pulls/"+strconv.Itoa(pr.Number)), nil, &record); err != nil {
		return forgeapi.MergeOutcome{}, err
	}
	if slices.Contains(mergeableNow, record.MergeableState) {
		return c.mergeNow(ctx, repo, pr, head, method)
	}
	var answer armData
	result, err := c.execute(ctx, opMergePR, enableAutoMerge, map[string]any{
		"pullRequestId":   record.NodeID,
		"mergeMethod":     strings.ToUpper(method),
		"expectedHeadOid": head,
	}, &answer)
	if err != nil {
		return forgeapi.MergeOutcome{}, err
	}
	if answer.Enable == nil {
		return forgeapi.MergeOutcome{}, c.core.Fail(ctx, opMergePR, transport.Document(http.MethodPost),
			forgeapi.CodeNotMergeable, forgeapi.KindNotMergeable, result.Status,
			"the forge refused to arm the merge: "+result.Refusal)
	}
	state := forgeapi.MergeOutcomeEnqueued
	if answer.Enable.PullRequest == nil || answer.Enable.PullRequest.AutoMergeRequest == nil {
		c.unmapped("auto-merge answer (document)", "no auto-merge request on the armed pull request")
		state = forgeapi.MergeOutcomeUnknown
	}
	return forgeapi.MergeOutcome{
		State:         state,
		QueueState:    forgeapi.QueueUnknown,
		QueuePosition: forgeapi.QueuePositionUnknown,
	}, nil
}

// mergeNow sends the asynchronous merge pinned to head.
func (c *Client) mergeNow(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef, head, method string) (forgeapi.MergeOutcome, error) {
	const op = opMergePR
	body := map[string]any{
		"merge_method": method,
		"sha":          head,
	}
	resp, err := c.exchange(ctx, &transport.Request{
		Op: op, Method: http.MethodPut, Body: body, Answers: []int{http.StatusConflict},
		Path: repoRoute(repo, "/pulls/"+strconv.Itoa(pr.Number)+"/merge-async"),
	})
	if err != nil {
		return forgeapi.MergeOutcome{}, err
	}
	var answer restMergeAnswer
	if err := transport.Decode(resp.Body, &answer); err != nil {
		return forgeapi.MergeOutcome{}, c.core.FailBody(ctx, op, transport.REST(http.MethodPut), resp.Status)
	}
	if resp.Status == http.StatusConflict {
		return c.inFlight(ctx, &answer)
	}
	outcome := forgeapi.MergeOutcome{
		State:         c.mergeOutcome(&answer),
		QueueState:    forgeapi.QueueUnknown,
		QueuePosition: forgeapi.QueuePositionUnknown,
	}
	if outcome.State == forgeapi.MergeOutcomeAccepted {
		outcome.Code = forgeapi.CodeAlreadyEnqueued
	}
	return outcome, nil
}

// inFlight reads the 409 the merge route answers while another merge on the pull
// request is pending. Its body names that merge's handle, so it is the
// already-in-flight outcome; a 409 naming no handle is a shape no capture witnesses,
// answered as the plain conflict. The split is the body's structure, never its message.
func (c *Client) inFlight(ctx context.Context, answer *restMergeAnswer) (forgeapi.MergeOutcome, error) {
	handle := handleOf(answer)
	if handle == "" {
		return forgeapi.MergeOutcome{}, c.core.Fail(ctx, opMergePR, transport.REST(http.MethodPut), "",
			forgeapi.KindConflict, http.StatusConflict, "the merge route answered 409 naming no merge in flight")
	}
	return forgeapi.MergeOutcome{
		State:         forgeapi.MergeOutcomeInFlight,
		Code:          forgeapi.CodeAlreadyEnqueued,
		QueueState:    forgeapi.QueueUnknown,
		QueuePosition: forgeapi.QueuePositionUnknown,
	}, nil
}

// mergeMethod is the merge strategy this product's route takes, refused LOCALLY
// against this family's closed set before any request. A strategy the REPOSITORY has
// disabled leaves the process and comes back as the forge's own refusal mapped to the
// same code, because it is one cause a consumer branches on once.
func mergeMethod(req forgeapi.MergeRequest) (string, error) {
	if req.Strategy != "" {
		if !slices.Contains(mergeMethods, req.Strategy) {
			return "", errorf(forgeapi.CodeStrategyNotAllowed,
				"strategy %q is outside this family's own set", req.Strategy)
		}
		return req.Strategy, nil
	}
	if req.Intent == forgeapi.IntentSquash {
		return memberSquash, nil
	}
	return memberMerge, nil
}

// handleOf is the handle this product minted for a pending merge, empty where the
// answer names none.
func handleOf(answer *restMergeAnswer) string {
	if answer.Details == nil {
		return ""
	}
	return answer.Details.UUID
}

// mergeOutcome reads what happened out of the asynchronous merge's answer, whose
// status names where the merge got to.
//
// The mapping is total over the measured statuses, and anything else is reported as an
// UNKNOWN outcome with the value named rather than as a merge: a merge this library
// reports as done when it is not is the worst answer available here.
func (c *Client) mergeOutcome(answer *restMergeAnswer) forgeapi.MergeOutcomeState {
	switch member(answer.Status) {
	case memberMerged:
		return forgeapi.MergeOutcomeMerged
	case memberPending, "in_progress":
		return forgeapi.MergeOutcomeAccepted
	case memberQueued, "enqueued":
		return forgeapi.MergeOutcomeEnqueued
	case "":
		c.unmapped("merge answer (rest)", "no status")
		return forgeapi.MergeOutcomeUnknown
	}
	c.unmapped("merge status (rest)", answer.Status)
	return forgeapi.MergeOutcomeUnknown
}

// MergeStatus implements [forgeapi.Merges].
//
// It is ONE request on both of its arms, the single-pull-request document
// whose selection already carries the merged flag, the merge-queue entry and the pull
// request's own URL, and the REST pull-request record on a connection that has
// ALREADY discovered its documents refused, which carries the merged flag and the URL
// and answers [forgeapi.QueueNone], the value that record's own absence of a
// merge-queue entry takes. That REST record answering from another address spends the
// one further read that resolves the successor, which this operation publishes on
// [forgeapi.MergeStatus.Successor] rather than refusing, and it is the second request
// the price states. A call that meets the refusal itself spends that one
// request and answers with it, so neither arm exceeds the published price, and the
// remedy for the caller is the next call on the same connection, which takes the REST
// arm from the start. So the queue verdict here has the five members that entry
// reports and
// nothing this operation returns needs a second call. It follows no page of
// checks: it returns none, so [Client.ReadPR] is what a caller wanting the folded
// verdict calls, on the same pull request, and this read's
// [forgeapi.MergeStatus.WebURL] is the page for a person.
//
// It reads no merge handle: upstream's 404 on the handle endpoint does not separate
// an expired result from an invisible repository or a wrong pull request, so the
// only honest treatment of it is the document read, and a handle read that falls
// back to the document is TWO requests where the arm that answers publishes one.
func (c *Client) MergeStatus(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.MergeStatus, error) {
	const op = "MergeStatus"
	ctx = c.core.Call(ctx, op)
	read, err := c.readPull(ctx, op, repo, pr, false)
	if err != nil {
		return forgeapi.MergeStatus{}, err
	}
	return forgeapi.MergeStatus{
		Merged:    read.merged,
		Queue:     read.item.Action.QueueState,
		WebURL:    read.item.WebURL,
		Successor: read.successor,
	}, nil
}

// CommitStatus implements [forgeapi.Checks].
//
// The fold is computed here rather than taken from this product's rollup
// verdict, because the neutral state is invisible in that field and reachable
// only per run. The measurement covered the endpoints it reads from, and the combined
// status alone is not one of them: on a repository whose CI is Actions that
// endpoint answers an EMPTY array, byte-identical to a commit with no CI at all,
// so a fold over it reports a green that is not there. The sources are the
// statuses and the check runs together, or the one rollup document that unions
// them and is the only read that tells an empty fold from a passing one. This
// read addresses one commit, so the fold is COMPLETE and bounded by
// [forgeapi.Budget.StatusPages].
//
// It is one to three requests. The document arm spends one plus a further page of the
// contexts connection wherever that connection has one, up to that bound; the degraded
// arm, which a connection takes once it has already discovered its documents refused,
// spends the two REST reads, plus the one further read that resolves the successor
// where they answered from another address, which both of them meet and one read
// answers. A call that meets the refusal answers with it rather than
// buying those two inside the same call.
func (c *Client) CommitStatus(ctx context.Context, repo forgeapi.RepoRef, ref string) (forgeapi.CommitChecks, error) {
	const op = "CommitStatus"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.CommitChecks{}, err
	}
	if err := forgeapi.ValidateRef(ref); err != nil {
		return forgeapi.CommitChecks{}, err
	}
	if c.isDegraded() {
		return c.degradedStatus(ctx, op, repo, ref)
	}
	// As on the pull-request read, a runtime refusal is this call's own failure plus
	// the connection's record rather than a fall-back inside the call, so the price
	// stays what the row publishes on both arms.
	return c.documentStatus(ctx, op, repo, ref)
}

// documentStatus is the commit rollup read, which is the only source on this product
// that tells a commit with no CI from a commit whose checks all passed.
func (c *Client) documentStatus(ctx context.Context, op string, repo forgeapi.RepoRef, ref string) (forgeapi.CommitChecks, error) {
	owner, name, err := ownerName(repo)
	if err != nil {
		return forgeapi.CommitChecks{}, err
	}
	vars := map[string]any{varOwner: owner, varName: name, "ref": ref, "checksAfter": nil}
	var payload docPayload
	if _, execErr := c.execute(ctx, op, commitRollup, vars, &payload); execErr != nil {
		return forgeapi.CommitChecks{}, execErr
	}
	if payload.Repository == nil {
		return forgeapi.CommitChecks{}, c.core.Fail(ctx, op, transport.Document(http.MethodPost),
			forgeapi.CodeRepoOrPRNotVisible, forgeapi.KindNotFound, http.StatusOK,
			"the document resolved no repository for this selector")
	}
	commit := payload.Repository.Object
	if commit == nil {
		return forgeapi.CommitChecks{}, c.core.Fail(ctx, op, transport.Document(http.MethodPost),
			forgeapi.CodeRefInvalid, forgeapi.KindNotFound, http.StatusOK,
			"the document resolved the repository and no commit at that reference")
	}
	folded := c.foldRollup(commit.StatusCheckRollup)
	folded, err = c.followCommitContexts(ctx, op, vars, &folded)
	if err != nil {
		return forgeapi.CommitChecks{}, err
	}
	return c.checksOf(ref, commit.OID, &folded), nil
}

// followCommitContexts completes the commit fold the same way the pull-request read
// completes its own, and under the same bound.
func (c *Client) followCommitContexts(ctx context.Context, op string, vars map[string]any, held *fold) (fold, error) {
	out := *held
	for pages := 1; out.truncated && pages < c.core.Budget().StatusPages; pages++ {
		if out.cursor == "" {
			break
		}
		next := maps.Clone(vars)
		next["checksAfter"] = out.cursor
		var payload docPayload
		if _, err := c.execute(ctx, op, commitRollup, next, &payload); err != nil {
			return out, err
		}
		if payload.Repository == nil || payload.Repository.Object == nil {
			return out, c.core.FailBody(ctx, op, transport.Document(http.MethodPost), http.StatusOK)
		}
		page := c.foldRollup(payload.Repository.Object.StatusCheckRollup)
		out = merged(&out, &page)
	}
	return out, nil
}

// checksOf is one fold as the published answer, with the commit the rows name where the
// read resolved one: a caller may address a branch, and the answer says which commit
// was folded.
func (c *Client) checksOf(ref, resolved string, folded *fold) forgeapi.CommitChecks {
	out := forgeapi.CommitChecks{
		Ref:      ref,
		Contexts: folded.contexts,
		State:    folded.state,
		Passing:  folded.passing,
		Failing:  folded.failing,
		Pending:  folded.pending,
		Neutral:  folded.neutral,
		Unknown:  folded.unknown,
		Total:    folded.total,
	}
	if resolved != "" {
		out.Ref = resolved
	}
	if folded.truncated {
		out.Partial = c.partial(forgeapi.PartialPaginationCap, len(folded.contexts))
	}
	return out
}

// degradedStatus is the REST arm a connection whose document was refused falls back
// to, and it reads BOTH sources rather than the combined status alone. That is the
// measured hazard this arm exists around: on a repository whose CI is Actions the
// combined status answers an empty statuses array with a state of pending, which is
// byte-identical to what a commit with no CI at all answers, so a fold over that
// endpoint alone reports a verdict for checks it never saw.
func (c *Client) degradedStatus(ctx context.Context, op string, repo forgeapi.RepoRef, ref string) (forgeapi.CommitChecks, error) {
	query := url.Values{keyPerPage: {strconv.Itoa(maxStatusPageItems)}}
	// Both reads meet the same hop, and one rename value across the two is what holds
	// this arm to the one further read the row prices.
	var moved rename
	var combined restCombined
	if _, err := c.readMovedJSON(ctx, op, repoRoute(repo, "/commits/"+ref+"/status"), query, &combined, &moved); err != nil {
		return forgeapi.CommitChecks{}, err
	}
	var runs restCheckRuns
	if _, err := c.readMovedJSON(ctx, op, repoRoute(repo, "/commits/"+ref+"/check-runs"), query, &runs, &moved); err != nil {
		return forgeapi.CommitChecks{}, err
	}
	var folded fold
	for _, row := range combined.Statuses {
		state := c.checkState("commit status state (rest)", row.State)
		folded.contexts = append(folded.contexts, forgeapi.CheckContext{
			Name: row.Context, Description: row.Description, TargetURL: row.TargetURL, State: state,
		})
		folded.add(state, 1)
	}
	for _, row := range runs.CheckRuns {
		state := c.runState("check run (rest)", row.Status, row.Conclusion)
		folded.contexts = append(folded.contexts, forgeapi.CheckContext{
			Name: row.Name, TargetURL: row.DetailsURL, State: state,
		})
		folded.add(state, 1)
	}
	folded.state = folded.verdict()
	out := c.checksOf(ref, combined.SHA, &folded)
	out.Successor = moved.to
	return out, nil
}

// ListIssues implements [forgeapi.Issues].
//
// This route's population is issues and PULL REQUESTS both on this product, and every
// pull-request row carries a key naming its own pull request, so a row carrying that
// key is dropped here: the operation publishes issues, and a list that answered pull
// requests among them would make a consumer's issue count wrong for free.
func (c *Client) ListIssues(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Issue], error) {
	const op = opListIssues
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	query, walk, err := c.listing(op, repo, statefulList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	if bounded, ok := c.unpagedList[forgeapi.Issue](walk.here()); ok {
		return bounded, nil
	}
	var rows []restIssue
	var moved rename
	resp, err := c.readMovedJSON(ctx, op, repoRoute(repo, "/issues"), query, &rows, &moved)
	if err != nil {
		if deferred, ok := c.deferredPage[forgeapi.Issue](err); ok {
			return deferred, nil
		}
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	items := make([]forgeapi.Issue, 0, len(rows))
	for i := range rows {
		if rows[i].PullRequest != nil {
			continue
		}
		items = append(items, c.normalizeIssue(&rows[i], repo))
	}
	next := walk.next(resp.Header)
	return forgeapi.Page[forgeapi.Issue]{
		Items:     items,
		Next:      next,
		Partial:   c.pagePartial(len(rows), next, walk.page),
		Successor: moved.to,
	}, nil
}

// CreateIssue implements [forgeapi.Issues]. It is ONE request: this product's issue
// creation takes label NAMES, so nothing has to resolve them to identifiers first,
// which is the difference from the pull-request creation, whose route declares no
// labels parameter at all. A creation that answers from another address spends the one
// further read that resolves the successor, which is the second request the price
// states.
//
// So no second request applies them here and none is priced: measured on the
// creation's own response, the labels the request carried come back on the created
// issue, which is the same route that applies them to a pull request answering for
// its own population.
func (c *Client) CreateIssue(ctx context.Context, repo forgeapi.RepoRef, req forgeapi.NewIssue) (forgeapi.Issue, error) {
	const op = "CreateIssue"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Issue{}, err
	}
	body := map[string]any{keyTitle: req.Title, keyBody: req.Body}
	if len(req.Labels) > 0 {
		body[keyLabels] = req.Labels
	}
	var record restIssue
	if err := c.sendJSON(ctx, op, http.MethodPost, repoRoute(repo, "/issues"), body, &record); err != nil {
		return forgeapi.Issue{}, err
	}
	return c.normalizeIssue(&record, repo), nil
}

// CloseIssue implements [forgeapi.Issues]. This product fills the close REASON itself
// on a plain close, measured, so the request sends the state alone.
func (c *Client) CloseIssue(ctx context.Context, repo forgeapi.RepoRef, issue forgeapi.IssueRef) (forgeapi.Issue, error) {
	const op = "CloseIssue"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Issue{}, err
	}
	if issue.Number <= 0 {
		return forgeapi.Issue{}, errorf(forgeapi.CodeRepoRefInvalid, "issue number is not positive")
	}
	var record restIssue
	path := repoRoute(repo, "/issues/"+strconv.Itoa(issue.Number))
	if err := c.sendJSON(ctx, op, http.MethodPatch, path, map[string]any{keyState: stateClosed}, &record); err != nil {
		return forgeapi.Issue{}, err
	}
	return c.normalizeIssue(&record, repo), nil
}

// ListReleases implements [forgeapi.Releases].
func (c *Client) ListReleases(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Release], error) {
	const op = "ListReleases"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Page[forgeapi.Release]{}, err
	}
	query, walk, err := c.listing(op, repo, fixedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Release]{}, err
	}
	if bounded, ok := c.unpagedList[forgeapi.Release](walk.here()); ok {
		return bounded, nil
	}
	var rows []restRelease
	var moved rename
	resp, err := c.readMovedJSON(ctx, op, repoRoute(repo, "/releases"), query, &rows, &moved)
	if err != nil {
		if deferred, ok := c.deferredPage[forgeapi.Release](err); ok {
			return deferred, nil
		}
		return forgeapi.Page[forgeapi.Release]{}, err
	}
	items := make([]forgeapi.Release, 0, len(rows))
	for i := range rows {
		items = append(items, normalizeRelease(&rows[i]))
	}
	next := walk.next(resp.Header)
	return forgeapi.Page[forgeapi.Release]{
		Items:     items,
		Next:      next,
		Partial:   c.pagePartial(len(rows), next, walk.page),
		Successor: moved.to,
	}, nil
}

// CreateRelease implements [forgeapi.Releases]. Both flags the contract carries are
// fields of this product's own creation, so a draft and a prerelease are what the
// caller asked for rather than an approximation, and a draft carries no publication
// instant at all, which the answer reports as the zero time.
func (c *Client) CreateRelease(ctx context.Context, repo forgeapi.RepoRef, req forgeapi.NewRelease) (forgeapi.Release, error) {
	const op = "CreateRelease"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Release{}, err
	}
	body := map[string]any{
		"tag_name":   req.TagName,
		"name":       req.Name,
		keyBody:      req.Body,
		keyDraft:     req.Draft,
		"prerelease": req.Prerelease,
	}
	if req.Target != "" {
		body["target_commitish"] = req.Target
	}
	var record restRelease
	if err := c.sendJSON(ctx, op, http.MethodPost, repoRoute(repo, "/releases"), body, &record); err != nil {
		return forgeapi.Release{}, err
	}
	return normalizeRelease(&record), nil
}

// ListLabels implements [forgeapi.Labels].
func (c *Client) ListLabels(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Label], error) {
	const op = "ListLabels"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Page[forgeapi.Label]{}, err
	}
	query, walk, err := c.listing(op, repo, fixedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Label]{}, err
	}
	if bounded, ok := c.unpagedList[forgeapi.Label](walk.here()); ok {
		return bounded, nil
	}
	var rows []restLabel
	var moved rename
	resp, err := c.readMovedJSON(ctx, op, repoRoute(repo, "/labels"), query, &rows, &moved)
	if err != nil {
		if deferred, ok := c.deferredPage[forgeapi.Label](err); ok {
			return deferred, nil
		}
		return forgeapi.Page[forgeapi.Label]{}, err
	}
	next := walk.next(resp.Header)
	return forgeapi.Page[forgeapi.Label]{
		Items:     normalizeLabels(rows),
		Next:      next,
		Partial:   c.pagePartial(len(rows), next, walk.page),
		Successor: moved.to,
	}, nil
}
