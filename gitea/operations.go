package gitea

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/transport"
)

// keyBody is the request-body field three creations share.
const keyBody = "body"

// Whoami implements [forgeapi.Identity].
func (c *Client) Whoami(ctx context.Context) (forgeapi.Account, error) {
	const op = "Whoami"
	ctx = transport.Call(ctx, op)
	var user wireUser
	if err := c.readJSON(ctx, op, "/user", nil, &user); err != nil {
		return forgeapi.Account{}, err
	}
	return forgeapi.Account{
		Login:  user.Login,
		Name:   user.FullName,
		Email:  user.Email,
		WebURL: user.WebURL,
		Scopes: user.Scopes,
	}, nil
}

// ListRepos implements [forgeapi.Repos].
//
// Under an owner it reads the owner's own route, which both products resolve for a
// user and an organization alike and which pages in SQL before dropping the rows the
// caller has no unit access to, so its walk continues from the stated total. Its 404
// is an owner the instance does not hold (services/context/user.go), so it carries
// [forgeapi.CodeOwnerUnresolved].
func (c *Client) ListRepos(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Repository], error) {
	const op = "ListRepos"
	ctx = transport.Call(ctx, op)
	set, query, walk, err := c.listing(op, forgeapi.RepoRef{}, repoList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Repository]{}, err
	}
	if page, ok := c.unpagedList[forgeapi.Repository](walk.here()); ok {
		return page, nil
	}
	route := "/user/repos"
	if set.OwnerSet {
		route = "/users/" + set.Owner + "/repos"
	}
	var rows []wireRepo
	resp, err := c.readPageResponse(ctx, op, route, query, ownerStatus(&set, http.StatusNotFound), &walk, set.PageBound, &rows)
	if err != nil {
		if page, ok := c.deferredPage[forgeapi.Repository](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.Repository]{}, err
	}
	items := make([]forgeapi.Repository, 0, len(rows))
	for i := range rows {
		if rows[i].FullName == "" {
			return forgeapi.Page[forgeapi.Repository]{}, c.core.FailBody(ctx, op, transport.REST(http.MethodGet), http.StatusOK, errFullNameEmpty)
		}
		items = append(items, c.normalizeRepo(&rows[i]))
	}
	next := walk.next(len(rows))
	if set.OwnerSet {
		total, err := strconv.Atoi(resp.Header.Get("X-Total-Count"))
		if err != nil || total < 0 {
			return forgeapi.Page[forgeapi.Repository]{}, c.core.Fail(ctx, op, transport.REST(http.MethodGet), forgeapi.CodeValidation,
				forgeapi.KindUpstream, resp.Status, "the owner's repositories answered no X-Total-Count it can count by, which is what says whether a further page exists")
		}
		next = walk.nextOfTotal(total)
	}
	return forgeapi.Page[forgeapi.Repository]{
		Items:   items,
		Next:    next,
		Partial: c.pagePartial(len(rows), next, walk.page),
	}, nil
}

// ListPRs implements [forgeapi.PullRequests]. Each row's status fold and, under
// [forgeapi.WithReviews], its reviews are one page each, read in rotation order under
// the client's per-interval cap: a status with a further page answers
// [forgeapi.CheckUnknown] with [forgeapi.PartialPaginationCap], and a row the cap
// leaves unread carries [forgeapi.PartialBudget]. The rotation is this repository's
// own, so no row starves across intervals, and nothing schedules across repositories.
// [Client.ListMyPRs] folds nothing, since an issue-search row carries no head.
func (c *Client) ListPRs(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	const op = "ListPRs"
	ctx = transport.Call(ctx, op)
	if err := checkRepo(repo); err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	set, query, walk, err := c.listing(op, repo, statefulList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	if page, ok := c.unpagedList[forgeapi.PullRequest](walk.here()); ok {
		return page, nil
	}
	addr := &address{named: repo}
	var rows []wirePull
	if err = c.readPageAt(ctx, op, addr, "/pulls", query, &walk, set.PageBound, &rows); err != nil {
		if page, ok := c.deferredPage[forgeapi.PullRequest](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	page, err := c.pullPage(ctx, op, addr, rows, walk, set.Reviews)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	if !set.MergeState {
		for i := range page.Items {
			blankMergeState(&page.Items[i])
		}
	}
	return page, nil
}

// ListMyPRs implements [forgeapi.PullRequests].
//
// This family answers the cross-repository question through the ISSUE search
// route, filtered to pull requests and to the open state, so the rows span
// repositories and each carries its own [forgeapi.PullRequest.Repo].
//
// The filter that makes the rows the CREDENTIAL's is the boolean both products
// declare on this route, created, which the instance resolves against whoever the
// request authenticated as. Beside it one of the two declares a username filter,
// created_by, and the other declares none at all, so a call built on that spelling
// is contracted to scope the answer on one product of the family and not on the
// other. The shared boolean also prices the call at one request per page, because
// nothing has to be resolved before the question can be asked.
//
// Under [forgeapi.WithOwner] the owner filter both products declare on the same
// route takes the boolean's place, so the call is still one request per page and
// the owner is resolved by the instance rather than by a read of its own. An owner
// the instance holds no user or organization for is answered 400 naming the user it
// could not find, which this family maps to [forgeapi.CodeOwnerUnresolved]: the
// route answers 400 for an owner or a team it cannot resolve, and this call sends
// no team.
//
// No row of this route carries a folded verdict, and the reason is the route's
// own shape rather than a choice: an issue-search row carries neither a check
// state nor a HEAD COMMIT, measured on both products and declared by both
// documents, and a folded status is addressed BY the head. So every row answers
// [forgeapi.CheckUnknown] with a zero total, uniformly, which is what the
// expectation table states field by field for this operation on this family. The
// verdict for a row a consumer opens is [Client.ReadPR] or
// [Client.CommitStatus], one call on one pull request; recovering the head here
// would cost a second read per row, which is the per-item fan-out the budget
// refuses on a poller's hot path.
func (c *Client) ListMyPRs(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	const op = "ListMyPRs"
	ctx = transport.Call(ctx, op)
	set, query, walk, err := c.listing(op, forgeapi.RepoRef{}, scopedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	query.Set("type", "pulls")
	if page, ok := c.unpagedList[forgeapi.PullRequest](walk.here()); ok {
		return page, nil
	}
	var rows []wirePull
	if err := c.readPage(ctx, op, issueSearch, query, ownerStatus(&set, http.StatusBadRequest), &walk, set.PageBound, &rows); err != nil {
		if page, ok := c.deferredPage[forgeapi.PullRequest](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	page := c.myPullPage(rows, walk)
	if !set.MergeState {
		for i := range page.Items {
			blankMergeState(&page.Items[i])
		}
	}
	return page, nil
}

// issueSearch is the cross-repository route both scoped lists read, the issue
// search both products declare, told which population to answer by its type filter.
const issueSearch = "/repos/issues/search"

// ListMyIssues implements [forgeapi.Issues].
//
// It is [Client.ListMyPRs] asking the same route for the other population: the
// type filter both products declare selects issues, and the scope, the price and
// the owner refusal are that call's own. Each row carries its own repository as the
// meta shape the search route answers, which is what the row is addressed by.
func (c *Client) ListMyIssues(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Issue], error) {
	const op = "ListMyIssues"
	ctx = transport.Call(ctx, op)
	set, query, walk, err := c.listing(op, forgeapi.RepoRef{}, scopedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	query.Set("type", "issues")
	if page, ok := c.unpagedList[forgeapi.Issue](walk.here()); ok {
		return page, nil
	}
	var rows []wireIssue
	if err := c.readPage(ctx, op, issueSearch, query, ownerStatus(&set, http.StatusBadRequest), &walk, set.PageBound, &rows); err != nil {
		if page, ok := c.deferredPage[forgeapi.Issue](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	for i := range rows {
		if m := rows[i].Repository; m != nil && m.FullName == "" {
			return forgeapi.Page[forgeapi.Issue]{}, c.core.FailBody(ctx, op, transport.REST(http.MethodGet), http.StatusOK, errRepositoryFullNameEmpty)
		}
	}
	return c.issuePage(&address{}, rows, walk), nil
}

// pullPage is the per-repository list's shape: normalize each row, fold each row's
// checks under the bounded rule, carry the continuation from the limit the page was
// sent at, and name the successor the call's reads met. reviews says whether each
// row's reviews are read beside its fold.
func (c *Client) pullPage(ctx context.Context, op string, addr *address, rows []wirePull, walk pageWalk, reviews bool) (forgeapi.Page[forgeapi.PullRequest], error) {
	items := make([]forgeapi.PullRequest, 0, len(rows))
	for i := range rows {
		items = append(items, c.normalizePull(&rows[i], addr.named))
	}
	if err := c.foldRows(ctx, op, addr, items, reviews); err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	next := walk.next(len(rows))
	return forgeapi.Page[forgeapi.PullRequest]{
		Items:     items,
		Next:      next,
		Partial:   c.pagePartial(len(rows), next, walk.page),
		Successor: addr.successor,
	}, nil
}

func (c *Client) myPullPage(rows []wirePull, walk pageWalk) forgeapi.Page[forgeapi.PullRequest] {
	items := make([]forgeapi.PullRequest, 0, len(rows))
	for i := range rows {
		item := c.normalizePull(&rows[i], metaRepo(rows[i].Repository))
		// A search row is the issue record, which carries no review_comments key.
		item.Comments = commentCount(rows[i].Comments)
		items = append(items, item)
	}
	next := walk.next(len(rows))
	return forgeapi.Page[forgeapi.PullRequest]{
		Items:   items,
		Next:    next,
		Partial: c.pagePartial(len(rows), next, walk.page),
	}
}

// foldRows fills the rows' folded verdicts in ROTATION order, starting after the row
// the stored cursor names, so the rows one interval could not reach are the next
// one's first and no row past the per-interval cap starves; the items keep the
// instance's order. A throttle upstream, or the operation's own bound, ending a fold
// marks that row and every unreached one with the reason the envelope assigns,
// rate-limited or budget, and issues no further fold. Any other failure, the caller's
// context ending included, FAILS the call, as the single read's fold does.
func (c *Client) foldRows(ctx context.Context, op string, addr *address, items []forgeapi.PullRequest, reviews bool) error {
	if len(items) == 0 {
		return nil
	}
	list := listKey(op, addr.named)
	start := c.rotationStart(list, items, c.core.BudgetState().RotationCursor)
	for n := range items {
		at := (start + n) % len(items)
		issued, err := c.foldOntoRow(ctx, op, addr, &items[at], reviews)
		if issued {
			c.rememberFoldPosition(list, at)
		}
		switch {
		case err == nil:
		case transport.IsThrottled(err):
			c.markUnread(items, start, n, forgeapi.PartialRateLimited, reviews)
			return nil
		case transport.OperationExpired(ctx, err):
			c.markUnread(items, start, n, forgeapi.PartialBudget, reviews)
			return nil
		default:
			return err
		}
	}
	return nil
}

// markUnread marks the row a fold failed on and every row this interval had not yet
// reached with the reason that stopped the folds, so the rows carrying no verdict
// carry the reason instead. A row with nothing to read is left alone: it was never
// going to be issued, so the stop is not why it has no verdict.
func (c *Client) markUnread(items []forgeapi.PullRequest, start, from int, reason forgeapi.PartialReason, reviews bool) {
	for n := from; n < len(items); n++ {
		at := (start + n) % len(items)
		if items[at].HeadSHA == "" && !reviews {
			continue
		}
		items[at].Partial = c.partial(reason, 0)
	}
}

// listKey names the list a fold position was taken in, which is the operation plus
// the repository it addressed: the per-repository list is a different order per
// repository.
func listKey(op string, repo forgeapi.RepoRef) string { return op + " " + repo.Selector }

// rotationStart is the index this interval resumes at: the row after the one the
// cursor names, and the first row where the cursor names none of them and this
// connection has folded nothing in THIS list, which covers a first run, a restart
// and a list the connection is presenting for the first time.
//
// A cursor naming a row the list no longer carries is the ORDINARY outcome rather
// than an error, since the row it named is merged, closed or paged out by the next
// cycle, and returning to the head of the list there re-folds the prefix the
// previous interval already served and starves every row past the per-interval cap.
// So the miss resumes at the position after the one this connection last folded in
// this list: the list a poller presents keeps the instance's order, so that position
// is where the served row was, and the list has to be the SAME one or the position
// is an index into an order nobody served.
func (c *Client) rotationStart(list string, items []forgeapi.PullRequest, cursor forgeapi.RotationCursor) int {
	if cursor == "" {
		return 0
	}
	for i := range items {
		if foldKey(&items[i]) == cursor {
			return (i + 1) % len(items)
		}
	}
	if at, ok := c.foldPosition(list); ok {
		return (at + 1) % len(items)
	}
	return 0
}

// foldKey names the row a rotation cursor points at. It is the repository's derived
// identifier and the pull request's number, because a per-repository list's rows all
// share one repository and a cursor naming only that could not resume inside the
// list. Its bytes are the continuation encoding's own class, since the value crosses
// the surface and a consumer hands it back through the option that validates it.
func foldKey(item *forgeapi.PullRequest) forgeapi.RotationCursor {
	return forgeapi.RotationCursor(item.Repo.ID + ":" + strconv.Itoa(item.Ref.Number))
}

// foldOntoRow makes one list row's admitted reads under the bounded rule: one page of
// its status where it has a head, one page of its reviews where reviews is true, and
// neither, with the row marked, where the interval's cap does not admit it. It
// reports whether anything was ISSUED, which is what the rotation advances on, and
// hands a failed read to the caller, since which marker an upstream failure earns is
// the list's decision. It addresses the list's repository, the successor where moved.
func (c *Client) foldOntoRow(ctx context.Context, op string, list *address, item *forgeapi.PullRequest, reviews bool) (bool, error) {
	if item.HeadSHA == "" && !reviews {
		return false, nil
	}
	if reason, ok := c.core.AdmitFold(ctx, op); !ok {
		item.Partial = c.partial(reason, 0)
		return false, nil
	}
	started := time.Now()
	err := c.readRow(ctx, op, list, item, reviews)
	c.core.SpendFold(time.Since(started))
	c.core.Rotate(foldKey(item))
	return true, err
}

// readRow is one admitted list row's reads: the status fold where the row has a
// head, then its reviews where asked.
func (c *Client) readRow(ctx context.Context, op string, list *address, item *forgeapi.PullRequest, reviews bool) error {
	if item.HeadSHA != "" {
		checks, err := c.foldStatus(ctx, op, list, item.HeadSHA, 1)
		if err != nil {
			return err
		}
		applyChecks(item, &checks)
	}
	if !reviews {
		return nil
	}
	return c.applyReviews(ctx, op, list, item.Ref.Number, item)
}

// applyChecks puts a fold's verdict and counts on a pull request, with the fold's
// own marker.
func applyChecks(item *forgeapi.PullRequest, checks *forgeapi.CommitChecks) {
	item.Action.Checks = checks.State
	item.Action.ChecksPassing = checks.Passing
	item.Action.ChecksFailing = checks.Failing
	item.Action.ChecksPending = checks.Pending
	item.Action.ChecksNeutral = checks.Neutral
	item.Action.ChecksUnknown = checks.Unknown
	item.Action.ChecksTotal = checks.Total
	item.Partial = checks.Partial
}

// applyReviews reads one page of pull request number's reviews at the instance's
// stated maximum and puts the counts on item. A page that came back full may continue, and a
// count over part of the reviews would read as exact, so both counts stay unknown
// and the row carries the pagination marker unless it already carries one.
func (c *Client) applyReviews(ctx context.Context, op string, addr *address, number int, item *forgeapi.PullRequest) error {
	limit, err := c.statedMaximum(ctx)
	if err != nil {
		return err
	}
	query := url.Values{keyPage: {"1"}, keyLimit: {strconv.Itoa(limit)}}
	var rows []wireReview
	if err := c.readAt(ctx, op, addr, "/pulls/"+strconv.Itoa(number)+"/reviews", query, &rows); err != nil {
		return err
	}
	if len(rows) >= limit {
		if item.Partial == nil {
			item.Partial = c.partial(forgeapi.PartialPaginationCap, len(rows))
		}
		return nil
	}
	item.Action.Approvals, item.Action.ChangesRequested = reviewCounts(rows)
	return nil
}

// ReadPR implements [forgeapi.PullRequests]: the record, its COMPLETE status fold up
// to [forgeapi.Budget.StatusPages] pages outside the per-interval cap, since this call
// is priced per call, and one page of its reviews. A fold that reaches the bound
// answers [forgeapi.CheckUnknown] with [forgeapi.PartialPaginationCap]. Absence answers
// [forgeapi.CodeRepoOrPRNotVisible] always: one REST status cannot tell a missing pull
// request from an invisible repository, and a further read would break the price. A
// moved repository answers [forgeapi.CodeRepoRefStale] carrying the successor.
func (c *Client) ReadPR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	const op = "ReadPR"
	ctx = transport.Call(ctx, op)
	addr := &address{named: repo}
	record, err := c.readPull(ctx, op, addr, pr)
	if err != nil {
		return forgeapi.PullRequest{}, err
	}
	if stale := c.moved(ctx, op, addr); stale != nil {
		return forgeapi.PullRequest{}, stale
	}
	item := c.normalizePull(record, repo)
	if item.HeadSHA != "" {
		checks, err := c.foldStatus(ctx, op, addr, item.HeadSHA, c.core.Budget().StatusPages)
		if err != nil {
			return forgeapi.PullRequest{}, err
		}
		applyChecks(&item, &checks)
		if stale := c.moved(ctx, op, addr); stale != nil {
			return forgeapi.PullRequest{}, stale
		}
	}
	if err := c.applyReviews(ctx, op, addr, pr.Number, &item); err != nil {
		return forgeapi.PullRequest{}, err
	}
	if stale := c.moved(ctx, op, addr); stale != nil {
		return forgeapi.PullRequest{}, stale
	}
	return item, nil
}

// readPull is the pull-request record read, which two operations share.
func (c *Client) readPull(ctx context.Context, op string, addr *address, pr forgeapi.PRRef) (*wirePull, error) {
	if err := checkRepo(addr.named); err != nil {
		return nil, err
	}
	if pr.Number <= 0 {
		return nil, errorf(forgeapi.CodeRepoRefInvalid, "pull-request number is not positive")
	}
	var record wirePull
	if err := c.readAt(ctx, op, addr, "/pulls/"+strconv.Itoa(pr.Number), nil, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// CreatePR implements [forgeapi.PullRequests].
//
//nolint:gocritic // hugeParam: the record is the published signature's own parameter
func (c *Client) CreatePR(ctx context.Context, repo forgeapi.RepoRef, req forgeapi.NewPullRequest) (forgeapi.PullRequest, error) {
	const op = "CreatePR"
	ctx = transport.CallWrite(ctx, op)
	if err := checkRepo(repo); err != nil {
		return forgeapi.PullRequest{}, err
	}
	body := map[string]any{
		"title": req.Title,
		keyBody: req.Body,
		"head":  req.SourceBranch,
		"base":  req.TargetBranch,
	}
	addr := &address{named: repo}
	labels, err := c.labelIDs(ctx, op, addr, req.Labels)
	if err != nil {
		return forgeapi.PullRequest{}, err
	}
	if len(labels) > 0 {
		body["labels"] = labels
	}
	if req.Draft {
		body["title"] = "WIP: " + req.Title
	}
	var record wirePull
	if err := c.sendAt(ctx, op, http.MethodPost, addr, "/pulls", body, &record); err != nil {
		return forgeapi.PullRequest{}, err
	}
	return c.normalizeMutatedPull(&record, repo), nil
}

// ClosePR implements [forgeapi.PullRequests].
func (c *Client) ClosePR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	return c.setPullState(ctx, "ClosePR", repo, pr, stateClosed)
}

// ReopenPR implements [forgeapi.PullRequests].
func (c *Client) ReopenPR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	return c.setPullState(ctx, "ReopenPR", repo, pr, stateOpen)
}

// setPullState is the one mutation both lifecycle transitions make.
func (c *Client) setPullState(ctx context.Context, op string, repo forgeapi.RepoRef, pr forgeapi.PRRef, state string) (forgeapi.PullRequest, error) {
	ctx = transport.Call(ctx, op)
	if err := checkRepo(repo); err != nil {
		return forgeapi.PullRequest{}, err
	}
	if pr.Number <= 0 {
		return forgeapi.PullRequest{}, errorf(forgeapi.CodeRepoRefInvalid, "pull-request number is not positive")
	}
	var record wirePull
	rest := "/pulls/" + strconv.Itoa(pr.Number)
	if err := c.sendAt(ctx, op, http.MethodPatch, &address{named: repo}, rest, map[string]any{"state": state}, &record); err != nil {
		return forgeapi.PullRequest{}, err
	}
	return c.normalizeMutatedPull(&record, repo), nil
}

// RerunFailedChecks implements [forgeapi.PullRequests]. A missing verb is a
// capability rather than an unimplemented role, because one package serves every
// version of both products. The runs read is filtered by the pinned head, and a
// head no run carries is refused as stale rather than re-run, since a re-run can
// carry deployment side effects. A head carries a run per workflow, so the run
// re-run is the one [Client.pickRun] picks.
//
//nolint:revive // unused-parameter: the pull-request reference is the published signature's; this product's route is addressed by the head commit's own run, which the head SHA pins
func (c *Client) RerunFailedChecks(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef, headSHA string) error {
	const op = "RerunFailedChecks"
	ctx = transport.CallWrite(ctx, op)
	if err := checkRepo(repo); err != nil {
		return err
	}
	if headSHA != "" {
		if err := forgeapi.ValidateRef(headSHA); err != nil {
			return err
		}
	}
	if err := c.capability(ctx, op, forgeapi.CapRerunChecks); err != nil {
		return err
	}
	query := url.Values{}
	if headSHA != "" {
		query.Set("head_sha", headSHA)
	}
	addr := &address{named: repo}
	runs, err := c.readRuns(ctx, op, addr, query)
	if err != nil {
		return err
	}
	if stale := c.moved(ctx, op, addr); stale != nil {
		return stale
	}
	run, carried, found := c.pickRun(runs.WorkflowRuns, headSHA)
	switch {
	case found:
	case carried || headSHA == "":
		return c.core.Fail(ctx, op, transport.REST(http.MethodGet), forgeapi.CodeRunNotFailed, forgeapi.KindConflict, http.StatusOK,
			"no workflow run on this head failed or was cancelled")
	default:
		return c.core.Fail(ctx, op, transport.REST(http.MethodGet), forgeapi.CodeStaleHead, forgeapi.KindConflict, http.StatusOK,
			"no workflow run carries the head this caller pinned")
	}
	route, _ := c.rerunRoute(run.ID)
	return c.sendAt(ctx, op, http.MethodPost, addr, route, nil, nil)
}

// RerunFailedRun implements [forgeapi.Checks]. It is gated on
// [forgeapi.CapRerunChecks] as [Client.RerunFailedChecks] is, and posts to the
// route detection held: the failed-jobs re-run, or Forgejo's whole-run re-run,
// which starts any finished run again, so there the run is read first and one
// [Client.rerunnable] does not hold is refused.
func (c *Client) RerunFailedRun(ctx context.Context, repo forgeapi.RepoRef, run forgeapi.RunRef) error {
	const op = "RerunFailedRun"
	ctx = transport.CallWrite(ctx, op)
	if err := checkRepo(repo); err != nil {
		return err
	}
	if run.ID <= 0 {
		return errorf(forgeapi.CodeRepoRefInvalid, "run id is not positive")
	}
	if err := c.capability(ctx, op, forgeapi.CapRerunChecks); err != nil {
		return err
	}
	addr := &address{named: repo}
	route, whole := c.rerunRoute(run.ID)
	if whole {
		var record wireRun
		if err := c.readAt(ctx, op, addr, "/actions/runs/"+strconv.FormatInt(run.ID, 10), nil, &record); err != nil {
			return err
		}
		if stale := c.moved(ctx, op, addr); stale != nil {
			return stale
		}
		if !c.rerunnable(&record) {
			return c.core.Fail(ctx, op, transport.REST(http.MethodGet), forgeapi.CodeRunNotFailed, forgeapi.KindConflict, http.StatusOK,
				"workflow run "+strconv.FormatInt(run.ID, 10)+" neither failed nor was cancelled")
		}
	}
	return c.sendAt(ctx, op, http.MethodPost, addr, route, nil, nil)
}

// pickRun is the run a re-run of the head's failed checks addresses: the first of
// the resolving read's rows on the pinned head, or on any head where the caller
// brought no pin, that [Client.rerunnable] holds. carried reports whether any row
// was on the pinned head, and found whether one qualified.
//
// The runs route declares no sort parameter and no ordering, so among several such
// runs the first is the instance's own choice. The head SHA is a declared query
// parameter, so upstream applies the filter and the comparison here is a second
// check rather than the only one.
func (c *Client) pickRun(runs []wireRun, headSHA string) (run wireRun, carried, found bool) {
	for i := range runs {
		if headSHA != "" && runs[i].head() != headSHA {
			continue
		}
		carried = true
		if c.rerunnable(&runs[i]) {
			return runs[i], true, true
		}
	}
	return wireRun{}, carried, false
}

// rerunnable reports whether a run failed or was cancelled, the outcomes Gitea's
// failed-jobs re-run starts again (services/actions/rerun.go,
// GetFailedJobsForRerun); Forgejo's whole-run re-run would start any finished run.
func (c *Client) rerunnable(r *wireRun) bool {
	return c.runState(r) == forgeapi.CheckFailing || r.outcome() == "cancelled"
}

// readRuns is the one read of a repository's Actions runs, which the re-run and the
// run listing share: the re-run filters it by the head it pins, and the listing
// pages it with no head filter at all.
func (c *Client) readRuns(ctx context.Context, op string, addr *address, query url.Values) (wireRuns, error) {
	var runs wireRuns
	err := c.readAt(ctx, op, addr, "/actions/runs", query, &runs)
	return runs, err
}

// ListRuns implements [forgeapi.Checks]. The row type reads both products' status
// fields, [forgeapi.Run.WorkflowPath] is the workflow file's name and
// [forgeapi.Run.Name] that name without its YAML extension, since neither product
// names the workflow. It is not gated on
// [forgeapi.CapRerunChecks], since both carry the route whether or not they can
// re-run. The envelope's total_count decides the continuation (see runWalk.next), so
// one missing or below zero is refused, a page with no row while the total states
// more ends the walk with [forgeapi.PartialResultWindow], and under
// [forgeapi.WithSince] a page runsSince reports passed ends it complete.
func (c *Client) ListRuns(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Run], error) {
	const op = "ListRuns"
	ctx = transport.Call(ctx, op)
	if err := checkRepo(repo); err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	set, err := listOptions(op, runList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	walk, err := runWalkAt(c.core.PageCall(op, repo, &set), set.After)
	if err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	if page, ok := c.unpagedList[forgeapi.Run](walk.here()); ok {
		return page, nil
	}
	addr := &address{named: repo}
	runs, err := c.readRunPage(ctx, op, addr, &walk, set.PageBound)
	if err != nil {
		if page, ok := c.deferredPage[forgeapi.Run](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	if runs.TotalCount == nil || *runs.TotalCount < 0 {
		return forgeapi.Page[forgeapi.Run]{}, c.core.Fail(ctx, op, transport.REST(http.MethodGet), forgeapi.CodeValidation,
			forgeapi.KindUpstream, http.StatusOK, "the run listing answered no total_count it can count by, which is what says whether a further page exists")
	}
	rows, total := runs.WorkflowRuns, *runs.TotalCount
	items, passed, err := c.runsSince(ctx, op, rows, repo, &set)
	if err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	if passed {
		return forgeapi.Page[forgeapi.Run]{Items: items, Successor: addr.successor}, nil
	}
	next := walk.next(len(rows), total)
	partial := c.pagePartial(len(rows), next, walk.page)
	if served := walk.served + len(rows); next == "" && served < total {
		partial = c.partial(forgeapi.PartialResultWindow, served)
		partial.OmittedAtLeast = total - served
	}
	return forgeapi.Page[forgeapi.Run]{Items: items, Next: next, Partial: partial, Successor: addr.successor}, nil
}

// runOrderSkew is how far a run's creation stamp may fall behind an older-id run's,
// a host clock stepped back between the two inserts.
const runOrderSkew = time.Minute

// runsSince normalizes one page's runs and drops those created before the call's since
// bound. Both products answer runs newest id first and stamp a run's creation at
// insert, so passed reports a page whose every run predates the bound by more than
// [runOrderSkew], after which no later page holds a run inside it. A row with no id
// or a negative number, or with no creation time under a bound, is refused: a Gitea
// release before v28 sends no created_at, and holding it to a bound would read every
// run as older.
func (c *Client) runsSince(ctx context.Context, op string, rows []wireRun, repo forgeapi.RepoRef, set *forgeapi.ListSettings) (items []forgeapi.Run, passed bool, err error) {
	items = make([]forgeapi.Run, 0, len(rows))
	floor := set.Since.Add(-runOrderSkew)
	passed = set.SinceSet && len(rows) > 0
	for i := range rows {
		run := c.normalizeRun(&rows[i], repo)
		switch {
		case run.Ref.ID <= 0:
			return nil, false, c.refuseRun(ctx, op, "the run listing answered a run with no id, which is the run's identity")
		case run.Ref.Number < 0:
			return nil, false, c.refuseRun(ctx, op, "the run listing answered a negative run number, which is no place in the repository's run sequence")
		case set.SinceSet && run.CreatedAt.IsZero():
			return nil, false, c.core.Fail(ctx, op, transport.REST(http.MethodGet), forgeapi.CodeRunUndated, forgeapi.KindUpstream,
				http.StatusOK, "the run listing answered a run with no creation time, so it cannot be held to the since bound")
		}
		if passed && !run.CreatedAt.Before(floor) {
			passed = false
		}
		if set.SinceSet && run.CreatedAt.Before(set.Since) {
			continue
		}
		items = append(items, run)
	}
	return items, passed, nil
}

// refuseRun is the failure a run row this family will not publish answers.
func (c *Client) refuseRun(ctx context.Context, op, message string) error {
	return c.core.Fail(ctx, op, transport.REST(http.MethodGet), forgeapi.CodeValidation, forgeapi.KindUpstream, http.StatusOK, message)
}

// readRunPage reads the page a run walk stands at, at the limit [Client.pageLimit]
// answers for the call's bound, and fixes that limit on the walk, as
// [Client.pageQuery] does for every other list of this family.
func (c *Client) readRunPage(ctx context.Context, op string, addr *address, walk *runWalk, bound int) (wireRuns, error) {
	limit, err := c.pageLimit(ctx, bound)
	if err != nil {
		return wireRuns{}, err
	}
	if refused := fixLimit(&walk.limit, limit); refused != nil {
		return wireRuns{}, refused
	}
	return c.readRuns(ctx, op, addr, url.Values{keyPage: {strconv.Itoa(walk.page)}, keyLimit: {strconv.Itoa(limit)}})
}

// MergePR implements [forgeapi.Merges].
//
// The merge is synchronous here and this family has neither a merge queue nor a
// merge train, so a merge not asked to wait is merged or refused. A merge asked to
// wait sends the merge form first, which merges a pull request whose requirements are
// met, and only on that form's 405 sends it again with merge_when_checks_succeed, this
// family's own field for it, whose 201 is the scheduled merge,
// [forgeapi.MergeOutcomeEnqueued]: measured, every product of the family schedules a
// flagged merge whatever the checks, and Forgejo then waits for the next commit status
// on its head, so the flag goes only where the merge cannot complete now. Such a merge
// costs one or two requests. Two of its refusals are this
// family's own: a conflict is [forgeapi.CodeNotMergeable], because the route
// answers it for a head that moved, for a merge that conflicts and for a merge
// asked to wait while one is already scheduled, which stands, and names the
// cause in human text alone, and an archived repository is
// [forgeapi.CodeRepoArchived]. That is a permanent refusal whose remedy is an
// operator unarchiving the repository, never retried and never reported as a
// conflict, because no rebase and no re-read reaches that state. A strategy
// outside the family's own set is [forgeapi.CodeStrategyNotAllowed] before any
// request; the route's own 405 is
// [forgeapi.CodeNotMergeable], because this product answers it for a disallowed
// merge style and for a pull request not yet mergeable alike.
func (c *Client) MergePR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef, req forgeapi.MergeRequest) (forgeapi.MergeOutcome, error) {
	const op = "MergePR"
	ctx = transport.Call(ctx, op)
	if err := checkRepo(repo); err != nil {
		return forgeapi.MergeOutcome{}, err
	}
	if pr.Number <= 0 {
		return forgeapi.MergeOutcome{}, errorf(forgeapi.CodeRepoRefInvalid, "pull-request number is not positive")
	}
	// The head pin is mandatory rather than prudent: a merge sent without a SHA
	// merges whatever the head is now, so it is refused on every family and not
	// only on the one product that answers a status for it.
	if req.HeadSHA == "" {
		return forgeapi.MergeOutcome{}, errorf(forgeapi.CodeMissingSHA, "a merge carries no head SHA")
	}
	if err := forgeapi.ValidateRef(req.HeadSHA); err != nil {
		return forgeapi.MergeOutcome{}, err
	}
	style, err := mergeStyle(req)
	if err != nil {
		return forgeapi.MergeOutcome{}, err
	}
	body := map[string]any{
		"Do":                        style,
		"head_commit_id":            req.HeadSHA,
		"delete_branch_after_merge": req.DeleteBranch,
	}
	rest := "/pulls/" + strconv.Itoa(pr.Number) + "/merge"
	status, err := c.sendForStatus(ctx, op, http.MethodPost, &address{named: repo}, rest, body)
	if fe, ok := errors.AsType[*forgeapi.Error](err); ok && req.AutoMerge && fe.Status == http.StatusMethodNotAllowed {
		body["merge_when_checks_succeed"] = true
		status, err = c.sendForStatus(ctx, op, http.MethodPost, &address{named: repo}, rest, body)
	}
	if err != nil {
		return forgeapi.MergeOutcome{}, err
	}
	state := forgeapi.MergeOutcomeMerged
	if status == http.StatusCreated {
		state = forgeapi.MergeOutcomeEnqueued
	}
	return forgeapi.MergeOutcome{
		State:         state,
		QueueState:    forgeapi.QueueNone,
		QueuePosition: forgeapi.QueuePositionUnknown,
	}, nil
}

// mergeStyle is the merge option this product takes, refused LOCALLY against this
// family's closed set before any request. A strategy the REPOSITORY has disabled
// leaves the process and comes back as the forge's own 405, which this product
// answers for every refusal of its merge route and so names no cause.
func mergeStyle(req forgeapi.MergeRequest) (string, error) {
	if req.Strategy != "" {
		if !slices.Contains(mergeStrategies, req.Strategy) {
			return "", errorf(forgeapi.CodeStrategyNotAllowed,
				"strategy %q is outside this family's own set", req.Strategy)
		}
		return req.Strategy, nil
	}
	if req.Intent == forgeapi.IntentSquash {
		return styleSquash, nil
	}
	return styleMerge, nil
}

// MergeStatus implements [forgeapi.Merges].
//
// It is one REST call, the same pull-request read [Client.ReadPR] makes without
// the folded status that read adds, and the record carries the merged state and
// the pull request's own page. That folded status is what [Client.ReadPR] is for,
// and it is the call a caller wanting the check verdict makes on the same pull
// request; this read returns no checks and pays for none.
// Queue is [forgeapi.QueueNone] always: this family has no queue and no train,
// so the neutral member is the answer rather than a field omitted, which is what
// lets a caller written against another family read this one unchanged.
func (c *Client) MergeStatus(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.MergeStatus, error) {
	const op = "MergeStatus"
	ctx = transport.Call(ctx, op)
	addr := &address{named: repo}
	record, err := c.readPull(ctx, op, addr, pr)
	if err != nil {
		return forgeapi.MergeStatus{}, err
	}
	merged := record.Merged
	return forgeapi.MergeStatus{
		Merged:    support(&merged),
		Queue:     forgeapi.QueueNone,
		WebURL:    record.WebURL,
		Successor: addr.successor,
	}, nil
}

// CancelAutoMerge implements [forgeapi.Merges].
//
// The cancel route answers 404 for a pull request with nothing scheduled and for a
// missing one alike (routers/api/v1/repo/pull.go CancelScheduledAutoMerge on each
// product), so a 404 is followed by the pull-request read: finding the pull request
// means nothing was scheduled, and the read's own refusal answers otherwise.
func (c *Client) CancelAutoMerge(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) error {
	const op = "CancelAutoMerge"
	ctx = transport.CallWrite(ctx, op)
	if err := checkRepo(repo); err != nil {
		return err
	}
	if pr.Number <= 0 {
		return errorf(forgeapi.CodeRepoRefInvalid, "pull-request number is not positive")
	}
	addr := &address{named: repo}
	resp, err := c.core.Do(ctx, &transport.Request{
		Op: op, Method: http.MethodDelete, Path: addr.route("/pulls/" + strconv.Itoa(pr.Number) + "/merge"),
		Answers: []int{http.StatusNotFound},
	})
	if err != nil {
		c.nameRefusedSuccessor(addr, resp, err)
		return err
	}
	if resp.Status != http.StatusNotFound {
		return nil
	}
	if _, err := c.readPull(ctx, op, addr, pr); err != nil {
		return err
	}
	return c.moved(ctx, op, addr)
}

// CommitStatus implements [forgeapi.Checks].
//
// This is the family whose folded status costs one request per pull request, plus
// one per additional status page, and the fold is computed HERE rather than taken
// from the endpoint's own verdict: that endpoint is paginated, and both its folded
// state and its total are computed over the page it returned, so a page of
// successes reports success for a commit that is failing and the truncation is not
// detectable from the body at all.
//
// So a fold that stopped short reports the pagination cap in its partial marker
// and leaves the state unknown rather than publishing a verdict over part of the
// evidence. This read addresses one commit, so its fold is COMPLETE and bounded
// only by [forgeapi.Budget.StatusPages].
//
// It is also the read [forgeapi.Budget.StatusReadsPerInterval] counts, over a
// rolling window this client keeps for itself: the read past that cap is not
// issued and reports [forgeapi.PartialBudget], and the next interval starts at the
// row after the last one served in that LIST rather than at the first, so no row
// starves. What survives a restart is the [forgeapi.RotationCursor] a consumer
// stored and handed to [forgeapi.WithRotationCursor].
func (c *Client) CommitStatus(ctx context.Context, repo forgeapi.RepoRef, ref string) (forgeapi.CommitChecks, error) {
	const op = "CommitStatus"
	ctx = transport.Call(ctx, op)
	if err := checkRepo(repo); err != nil {
		return forgeapi.CommitChecks{}, err
	}
	if err := forgeapi.ValidateRef(ref); err != nil {
		return forgeapi.CommitChecks{}, err
	}
	addr := &address{named: repo}
	out, err := c.foldStatus(ctx, op, addr, ref, c.core.Budget().StatusPages)
	if err != nil {
		return forgeapi.CommitChecks{}, err
	}
	out.Successor = addr.successor
	return out, nil
}

// foldStatus reads the combined commit status and folds the verdict and the
// per-state counts CLIENT-side over the rows it holds, never trusting the state the
// page itself reports.
//
// maxPages is the fold rule: one page on a list, the published bound on a read. A
// fold still short at that bound reports the pagination cap with the verdict
// unknown rather than a verdict over part of the evidence.
//
// Each page asks for the instance's stated maximum, because the fold is computed
// over the rows a page carries and reads a page short of its limit as the last one:
// a limit above what the instance serves would fold one clamped page as every
// context there is.
func (c *Client) foldStatus(ctx context.Context, op string, addr *address, ref string, maxPages int) (forgeapi.CommitChecks, error) {
	out := forgeapi.CommitChecks{Ref: ref}
	// A bound admitting no page is the literal zero its option publishes: nothing
	// is issued and the verdict is unknown with the page cap's own marker, where
	// clamping the bound up to one traversed a page the caller had ruled out.
	if maxPages <= 0 {
		out.State = forgeapi.CheckUnknown
		out.Partial = c.partial(forgeapi.PartialPaginationCap, 0)
		return out, nil
	}
	limit, err := c.statedMaximum(ctx)
	if err != nil {
		return forgeapi.CommitChecks{}, err
	}
	truncated := false
	for page := 1; page <= maxPages; page++ {
		var body wireCombined
		query := url.Values{
			keyPage:  {strconv.Itoa(page)},
			keyLimit: {strconv.Itoa(limit)},
		}
		if err := c.readAt(ctx, op, addr, "/commits/"+url.PathEscape(ref)+"/status", query, &body); err != nil {
			return forgeapi.CommitChecks{}, err
		}
		if body.SHA != "" {
			out.Ref = body.SHA
		}
		for i := range body.Statuses {
			row := &body.Statuses[i]
			out.Contexts = append(out.Contexts, forgeapi.CheckContext{
				Name:        row.Context,
				Description: row.Description,
				TargetURL:   row.TargetURL,
				State:       c.checkState(statusState(row)),
			})
		}
		if len(body.Statuses) < limit {
			truncated = false
			break
		}
		truncated = true
	}
	count(&out)
	if truncated {
		out.State = forgeapi.CheckUnknown
		out.Partial = &forgeapi.Partial{
			Reason:         forgeapi.PartialPaginationCap,
			Fetched:        len(out.Contexts),
			OmittedAtLeast: 1,
		}
	}
	return out, nil
}

// count is the fold itself, over the rows the caller holds. The verdict is the
// worst state present, because one failing check makes the collection failing
// however many passed.
//
// The UNKNOWN arm sits second, above pending, passing and neutral, which is what
// the totality discipline asks of a fold: a value this family's table did not map is
// a check whose state was not read, and passing means every check reported succeeded.
// Below the failure arm a green verdict over a collection carrying one unclassified
// row is the worst answer on this path, because it renders a commit green on the
// strength of the rows that happened to map and the next upstream state added to the
// enumeration reaches a consumer as a success.
func count(out *forgeapi.CommitChecks) {
	for _, ctx := range out.Contexts {
		switch ctx.State {
		case forgeapi.CheckPassing:
			out.Passing++
		case forgeapi.CheckFailing:
			out.Failing++
		case forgeapi.CheckPending:
			out.Pending++
		case forgeapi.CheckNeutral:
			out.Neutral++
		case forgeapi.CheckUnknown:
			out.Unknown++
		}
	}
	out.Total = len(out.Contexts)
	switch {
	case out.Failing > 0:
		out.State = forgeapi.CheckFailing
	case out.Unknown > 0:
		out.State = forgeapi.CheckUnknown
	case out.Pending > 0:
		out.State = forgeapi.CheckPending
	case out.Passing > 0:
		out.State = forgeapi.CheckPassing
	case out.Neutral > 0:
		out.State = forgeapi.CheckNeutral
	default:
		out.State = forgeapi.CheckUnknown
	}
}

// ListIssues implements [forgeapi.Issues].
//
// This route's population on both products is issues AND pull requests, so the
// type filter both documents declare is part of the question rather than an
// option: without it the answer carries pull-request rows, which this operation
// would normalize as issues and a consumer would act on as issues.
func (c *Client) ListIssues(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Issue], error) {
	const op = "ListIssues"
	ctx = transport.Call(ctx, op)
	if err := checkRepo(repo); err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	set, query, walk, err := c.listing(op, repo, statefulList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	query.Set("type", "issues")
	if page, ok := c.unpagedList[forgeapi.Issue](walk.here()); ok {
		return page, nil
	}
	addr := &address{named: repo}
	var rows []wireIssue
	if err := c.readPageAt(ctx, op, addr, "/issues", query, &walk, set.PageBound, &rows); err != nil {
		if page, ok := c.deferredPage[forgeapi.Issue](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	return c.issuePage(addr, rows, walk), nil
}

// issuePage is the list shape both issue lists share: normalize each row, carry the
// continuation from the limit the page was sent at, and name the successor the read
// met. The address names no repository on the cross-repository list, whose rows name
// their own.
func (c *Client) issuePage(addr *address, rows []wireIssue, walk pageWalk) forgeapi.Page[forgeapi.Issue] {
	items := make([]forgeapi.Issue, 0, len(rows))
	for i := range rows {
		items = append(items, c.normalizeIssue(&rows[i], addr.named))
	}
	next := walk.next(len(rows))
	return forgeapi.Page[forgeapi.Issue]{
		Items:     items,
		Next:      next,
		Partial:   c.pagePartial(len(rows), next, walk.page),
		Successor: addr.successor,
	}
}

// CreateIssue implements [forgeapi.Issues].
func (c *Client) CreateIssue(ctx context.Context, repo forgeapi.RepoRef, req forgeapi.NewIssue) (forgeapi.Issue, error) {
	const op = "CreateIssue"
	ctx = transport.CallWrite(ctx, op)
	if err := checkRepo(repo); err != nil {
		return forgeapi.Issue{}, err
	}
	body := map[string]any{"title": req.Title, keyBody: req.Body}
	addr := &address{named: repo}
	labels, err := c.labelIDs(ctx, op, addr, req.Labels)
	if err != nil {
		return forgeapi.Issue{}, err
	}
	if len(labels) > 0 {
		body["labels"] = labels
	}
	var record wireIssue
	if err := c.sendAt(ctx, op, http.MethodPost, addr, "/issues", body, &record); err != nil {
		return forgeapi.Issue{}, err
	}
	return c.normalizeIssue(&record, repo), nil
}

// labelIDs resolves the caller's label NAMES onto the numeric ids both products'
// creation and label addition options take, from ONE page of the repository's labels
// at the instance's stated maximum, and where a name is missing from that page and
// it was the whole list, ONE page of its owner's organization labels at the same
// bound, which both products' option also takes. A name missing from a full
// repository page is refused as truncated before the organization is read, since the
// repository's own label of that name may sit past the page and outranks the
// organization's. A name both pages lack is refused, never dropped: under the truncated
// code where either page came back full, the only witness this product gives that a
// list continues, and as a validation where each was the whole list. A read that
// meets a move refuses with the successor and sends no write: writing to another
// repository is the caller's call.
func (c *Client) labelIDs(ctx context.Context, op string, addr *address, names []string) ([]int64, error) {
	if len(names) == 0 {
		return nil, nil
	}
	limit, err := c.statedMaximum(ctx)
	if err != nil {
		return nil, err
	}
	query := url.Values{
		keyPage:  {"1"},
		keyLimit: {strconv.Itoa(limit)},
	}
	var rows []wireLabel
	if readErr := c.readAt(ctx, op, addr, "/labels", query, &rows); readErr != nil {
		return nil, readErr
	}
	if stale := c.moved(ctx, op, addr); stale != nil {
		return nil, stale
	}
	byName := make(map[string]int64, len(rows))
	for _, row := range rows {
		byName[row.Name] = row.ID
	}
	read := labelRead{repo: len(rows), limit: limit}
	missing := slices.IndexFunc(names, func(name string) bool { _, ok := byName[name]; return !ok })
	if missing >= 0 && read.repo >= read.limit {
		return nil, c.unresolvedLabel(ctx, op, names[missing], read)
	}
	if missing >= 0 {
		if read.org, read.orgFound, err = c.addOrgLabels(ctx, op, addr, query, byName); err != nil {
			return nil, err
		}
	}
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		id, ok := byName[name]
		if !ok {
			return nil, c.unresolvedLabel(ctx, op, name, read)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// addOrgLabels adds to byName each label of [Client.orgLabels] whose name the
// repository's own page did not carry, which keeps a repository label first, and
// answers how many rows that page held and whether the owner is an organization.
func (c *Client) addOrgLabels(ctx context.Context, op string, addr *address, query url.Values, byName map[string]int64) (rows int, found bool, err error) {
	org, found, err := c.orgLabels(ctx, op, addr, query)
	if err != nil {
		return 0, false, err
	}
	for _, row := range org {
		if _, ok := byName[row.Name]; !ok {
			byName[row.Name] = row.ID
		}
	}
	return len(org), found, nil
}

// orgLabels is the first page of the labels the organization owning the call's
// repository defines, read under query, and whether the owner is an organization:
// the route answers 404 for a user, which owns no organization label.
func (c *Client) orgLabels(ctx context.Context, op string, addr *address, query url.Values) ([]wireLabel, bool, error) {
	owner, _, _ := strings.Cut(addr.current().Selector, "/")
	var rows []wireLabel
	_, err := c.get(ctx, op, "/orgs/"+owner+"/labels", query, http.StatusNotFound, &rows)
	if fe, ok := errors.AsType[*forgeapi.Error](err); ok && fe.Code == forgeapi.CodeOwnerUnresolved {
		return nil, false, nil
	}
	return rows, err == nil, err
}

// labelRead is how many rows each label page the resolution read answered, at the
// bound both were read at, and whether the owner's organization page was read.
type labelRead struct {
	repo, org, limit int
	orgFound         bool
}

// unresolvedLabel is the refusal a label name with no identifier behind it gets,
// under the code the page it was looked for in earns.
//
// The kind is the upstream one this family's other local refusal over an answered
// read already uses: no request carrying the label was sent, the request that WAS
// sent answered 200, and nothing here is a merge, so the merge kind would tell a
// caller their pull request is unmergeable because an issue's label did not resolve.
func (c *Client) unresolvedLabel(ctx context.Context, op, name string, read labelRead) *forgeapi.Error {
	code := forgeapi.CodeValidation
	pages, verdict := "the first page of this repository's labels", "which was the whole list"
	if read.orgFound {
		pages, verdict = "the first page of this repository's labels or of its organization's", "each the whole list"
	}
	if read.repo >= read.limit || read.org >= read.limit {
		code = forgeapi.CodeLabelPageTruncated
		verdict = "and a page came back full, so its list continues past it"
	}
	return c.core.Fail(ctx, op, transport.REST(http.MethodGet), code, forgeapi.KindUpstream, http.StatusOK,
		"no label named "+name+" on "+pages+", bounded at "+strconv.Itoa(read.limit)+", "+verdict)
}

// CloseIssue implements [forgeapi.Issues].
func (c *Client) CloseIssue(ctx context.Context, repo forgeapi.RepoRef, issue forgeapi.IssueRef) (forgeapi.Issue, error) {
	return c.setIssueState(ctx, "CloseIssue", repo, issue, stateClosed)
}

// ReopenIssue implements [forgeapi.Issues].
func (c *Client) ReopenIssue(ctx context.Context, repo forgeapi.RepoRef, issue forgeapi.IssueRef) (forgeapi.Issue, error) {
	return c.setIssueState(ctx, "ReopenIssue", repo, issue, stateOpen)
}

// ReadIssue implements [forgeapi.Issues]. A [forgeapi.Issue] carries no successor,
// so a repository that moved answers [forgeapi.CodeRepoRefStale] carrying it.
func (c *Client) ReadIssue(ctx context.Context, repo forgeapi.RepoRef, issue forgeapi.IssueRef) (forgeapi.Issue, error) {
	const op = "ReadIssue"
	ctx = transport.Call(ctx, op)
	if err := checkRepo(repo); err != nil {
		return forgeapi.Issue{}, err
	}
	if issue.Number <= 0 {
		return forgeapi.Issue{}, errorf(forgeapi.CodeRepoRefInvalid, "issue number is not positive")
	}
	addr := &address{named: repo}
	var record wireIssue
	if err := c.readAt(ctx, op, addr, "/issues/"+strconv.Itoa(issue.Number), nil, &record); err != nil {
		return forgeapi.Issue{}, err
	}
	if stale := c.moved(ctx, op, addr); stale != nil {
		return forgeapi.Issue{}, stale
	}
	if record.PullRequest != nil {
		return forgeapi.Issue{}, c.core.Fail(ctx, op, transport.REST(http.MethodGet), "", forgeapi.KindNotFound, http.StatusOK,
			"number "+strconv.Itoa(issue.Number)+" is a pull request on this product, and this operation reads issues only")
	}
	return c.normalizeIssue(&record, repo), nil
}

// setIssueState is the one transition both issue lifecycle operations make.
func (c *Client) setIssueState(ctx context.Context, op string, repo forgeapi.RepoRef, issue forgeapi.IssueRef, state string) (forgeapi.Issue, error) {
	ctx = transport.Call(ctx, op)
	if err := checkRepo(repo); err != nil {
		return forgeapi.Issue{}, err
	}
	if issue.Number <= 0 {
		return forgeapi.Issue{}, errorf(forgeapi.CodeRepoRefInvalid, "issue number is not positive")
	}
	var record wireIssue
	rest := "/issues/" + strconv.Itoa(issue.Number)
	if err := c.sendAt(ctx, op, http.MethodPatch, &address{named: repo}, rest, map[string]any{"state": state}, &record); err != nil {
		return forgeapi.Issue{}, err
	}
	return c.normalizeIssue(&record, repo), nil
}

// ListReleases implements [forgeapi.Releases].
func (c *Client) ListReleases(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Release], error) {
	const op = "ListReleases"
	ctx = transport.Call(ctx, op)
	if err := checkRepo(repo); err != nil {
		return forgeapi.Page[forgeapi.Release]{}, err
	}
	set, query, walk, err := c.listing(op, repo, fixedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Release]{}, err
	}
	if page, ok := c.unpagedList[forgeapi.Release](walk.here()); ok {
		return page, nil
	}
	addr := &address{named: repo}
	var rows []wireRelease
	if err := c.readPageAt(ctx, op, addr, "/releases", query, &walk, set.PageBound, &rows); err != nil {
		if page, ok := c.deferredPage[forgeapi.Release](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.Release]{}, err
	}
	items := make([]forgeapi.Release, 0, len(rows))
	for i := range rows {
		items = append(items, normalizeRelease(&rows[i]))
	}
	next := walk.next(len(rows))
	return forgeapi.Page[forgeapi.Release]{
		Items:     items,
		Next:      next,
		Partial:   c.pagePartial(len(rows), next, walk.page),
		Successor: addr.successor,
	}, nil
}

// CreateRelease implements [forgeapi.Releases].
func (c *Client) CreateRelease(ctx context.Context, repo forgeapi.RepoRef, req forgeapi.NewRelease) (forgeapi.Release, error) {
	const op = "CreateRelease"
	ctx = transport.Call(ctx, op)
	if err := checkRepo(repo); err != nil {
		return forgeapi.Release{}, err
	}
	body := map[string]any{
		"tag_name":         req.TagName,
		"name":             req.Name,
		keyBody:            req.Body,
		"target_commitish": req.Target,
		"draft":            req.Draft,
		"prerelease":       req.Prerelease,
	}
	var record wireRelease
	if err := c.sendAt(ctx, op, http.MethodPost, &address{named: repo}, "/releases", body, &record); err != nil {
		return forgeapi.Release{}, err
	}
	return normalizeRelease(&record), nil
}

// ListLabels implements [forgeapi.Labels].
func (c *Client) ListLabels(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Label], error) {
	const op = "ListLabels"
	ctx = transport.Call(ctx, op)
	if err := checkRepo(repo); err != nil {
		return forgeapi.Page[forgeapi.Label]{}, err
	}
	set, query, walk, err := c.listing(op, repo, fixedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Label]{}, err
	}
	if page, ok := c.unpagedList[forgeapi.Label](walk.here()); ok {
		return page, nil
	}
	addr := &address{named: repo}
	var rows []wireLabel
	if err := c.readPageAt(ctx, op, addr, "/labels", query, &walk, set.PageBound, &rows); err != nil {
		if page, ok := c.deferredPage[forgeapi.Label](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.Label]{}, err
	}
	next := walk.next(len(rows))
	return forgeapi.Page[forgeapi.Label]{
		Items:     normalizeLabels(rows),
		Next:      next,
		Partial:   c.pagePartial(len(rows), next, walk.page),
		Successor: addr.successor,
	}, nil
}

// AddLabels implements [forgeapi.Labels]. A read that meets a move refuses with the
// successor and nothing is written, as [Client.labelIDs] states.
func (c *Client) AddLabels(ctx context.Context, repo forgeapi.RepoRef, target forgeapi.LabelTarget, names []string) error {
	const op = "AddLabels"
	ctx = transport.CallWrite(ctx, op)
	number, err := labelItem(repo, target)
	if err != nil || len(names) == 0 {
		return err
	}
	addr := &address{named: repo}
	ids, err := c.labelIDs(ctx, op, addr, names)
	if err != nil {
		return err
	}
	return c.sendAt(ctx, op, http.MethodPost, addr, "/issues/"+strconv.Itoa(number)+"/labels", map[string]any{"labels": ids}, nil)
}

// labelItem is the number a label change addresses, refused before any request
// where the repository or the target cannot be addressed.
func labelItem(repo forgeapi.RepoRef, target forgeapi.LabelTarget) (int, error) {
	if err := checkRepo(repo); err != nil {
		return 0, err
	}
	if err := target.Validate(); err != nil {
		return 0, err
	}
	if target.PR.Number != 0 {
		return target.PR.Number, nil
	}
	return target.Issue.Number, nil
}

// RemoveLabels implements [forgeapi.Labels]. The item's own label read names every
// label it carries, an organization's included, so only those are removed. Gitea's
// route takes the label's id, so every carried label of a name is removed, a
// repository's and an organization's of one name both. Forgejo's takes a name or an
// id, resolving a name to the repository's label before the organization's and an id
// to a repository label only, so it is sent the name, which an id could be mistaken
// for (routers/api/v1/repo/issue_label.go DeleteIssueLabel on each). A label or item
// gone since the read answers 404 or 422 there, and the item carries no label.
func (c *Client) RemoveLabels(ctx context.Context, repo forgeapi.RepoRef, target forgeapi.LabelTarget, names []string) error {
	const op = "RemoveLabels"
	ctx = transport.CallWrite(ctx, op)
	number, err := labelItem(repo, target)
	if err != nil || len(names) == 0 {
		return err
	}
	caps, err := c.ConnectionCaps(ctx)
	if err != nil {
		return err
	}
	addr := &address{named: repo}
	rest := "/issues/" + strconv.Itoa(number) + "/labels"
	var carried []wireLabel
	if err := c.readAt(ctx, op, addr, rest, nil, &carried); err != nil {
		return err
	}
	if stale := c.moved(ctx, op, addr); stale != nil {
		return stale
	}
	segments := removalSegments(carried, caps.Product == forgeapi.ProductForgejo)
	var removed []string
	for _, name := range transport.DistinctNames(names) {
		took := false
		for _, segment := range segments[name] {
			ok, err := c.removeAt(ctx, op, addr, rest+"/", segment)
			if err != nil {
				return transport.AfterRemovals(err, removed)
			}
			took = took || ok
		}
		if took {
			removed = append(removed, name)
		}
	}
	return nil
}

// removalSegments maps each carried name to the path segments that remove it: every
// carried label's id, or on Forgejo the escaped name once.
func removalSegments(carried []wireLabel, byName bool) map[string][]string {
	segments := make(map[string][]string, len(carried))
	for _, label := range carried {
		segment := strconv.FormatInt(label.ID, 10)
		if byName {
			segment = url.PathEscape(label.Name)
		}
		if !slices.Contains(segments[label.Name], segment) {
			segments[label.Name] = append(segments[label.Name], segment)
		}
	}
	return segments
}
