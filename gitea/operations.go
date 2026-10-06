package gitea

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// keyBody is the request-body field three creations share.
const keyBody = "body"

// Whoami implements [forgeapi.Identity].
func (c *Client) Whoami(ctx context.Context) (forgeapi.Account, error) {
	const op = "Whoami"
	ctx = c.core.Call(ctx, op)
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
func (c *Client) ListRepos(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Repository], error) {
	const op = "ListRepos"
	ctx = c.core.Call(ctx, op)
	set, query, walk, err := c.listing(op, forgeapi.RepoRef{}, fixedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Repository]{}, err
	}
	if page, ok := c.unpagedList[forgeapi.Repository](walk.here()); ok {
		return page, nil
	}
	var rows []wireRepo
	if err := c.readPage(ctx, op, "/user/repos", query, &walk, set.PageBound, &rows); err != nil {
		if page, ok := c.deferredPage[forgeapi.Repository](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.Repository]{}, err
	}
	items := make([]forgeapi.Repository, 0, len(rows))
	for i := range rows {
		items = append(items, c.normalizeRepo(&rows[i]))
	}
	next := walk.next(len(rows))
	return forgeapi.Page[forgeapi.Repository]{
		Items:   items,
		Next:    next,
		Partial: c.pagePartial(len(rows), next, walk.page),
	}, nil
}

// ListPRs implements [forgeapi.PullRequests].
//
// There is no document to fold the merge state into this call, so what the list
// carries is what the REST list carries, and a state it cannot read is reported
// unknown rather than guessed. The folded check verdict for each row is a
// separate read with a separate cap; see [Client.CommitStatus].
//
// That read is BOUNDED on this path, which is the same rule every family's list
// follows arriving in another transport: one page of the status endpoint per row,
// and a row whose status had a further page answers [forgeapi.CheckUnknown] with
// [forgeapi.PartialPaginationCap] on [forgeapi.PullRequest.Partial]. A row the
// client's per-interval cap left unread carries [forgeapi.PartialBudget] instead,
// which is a different fact: the fold was not issued rather than truncated.
//
// Which rows those are is decided per LIST. The rotation this call resumes from is
// this repository's own, so no row of it starves across intervals, and nothing here
// schedules across repositories: a caller sweeping several through this call on one
// connection gives the first lists the folds and the later ones none inside one
// interval, in the order it chose. [Client.ListMyPRs] is the call whose rows span
// repositories, and it issues no fold and spends no part of the per-interval cap,
// because an issue-search row carries no head to address one by; the verdict for a
// row a consumer opens is [Client.ReadPR] or [Client.CommitStatus].
func (c *Client) ListPRs(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	const op = "ListPRs"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
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
	if err := c.readPageAt(ctx, op, addr, "/pulls", query, &walk, set.PageBound, &rows); err != nil {
		if page, ok := c.deferredPage[forgeapi.PullRequest](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	return c.pullPage(ctx, op, addr, rows, walk)
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
	ctx = c.core.Call(ctx, op)
	set, query, walk, err := c.listing(op, forgeapi.RepoRef{}, scopedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	query.Set("type", "pulls")
	if page, ok := c.unpagedList[forgeapi.PullRequest](walk.here()); ok {
		return page, nil
	}
	var rows []wirePull
	if err := c.readPage(ctx, op, issueSearch, query, &walk, set.PageBound, &rows); err != nil {
		if page, ok := c.deferredPage[forgeapi.PullRequest](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	return c.myPullPage(rows, walk), nil
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
	ctx = c.core.Call(ctx, op)
	set, query, walk, err := c.listing(op, forgeapi.RepoRef{}, scopedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	query.Set("type", "issues")
	if page, ok := c.unpagedList[forgeapi.Issue](walk.here()); ok {
		return page, nil
	}
	var rows []wireIssue
	if err := c.readPage(ctx, op, issueSearch, query, &walk, set.PageBound, &rows); err != nil {
		if page, ok := c.deferredPage[forgeapi.Issue](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	return c.issuePage(&address{}, rows, walk), nil
}

// pullPage is the per-repository list's shape: normalize each row, fold each row's
// checks under the bounded rule, carry the continuation from the limit the page was
// sent at, and name the successor the call's reads met.
func (c *Client) pullPage(ctx context.Context, op string, addr *address, rows []wirePull, walk pageWalk) (forgeapi.Page[forgeapi.PullRequest], error) {
	items := make([]forgeapi.PullRequest, 0, len(rows))
	for i := range rows {
		items = append(items, c.normalizePull(&rows[i], addr.named))
	}
	if err := c.foldRows(ctx, op, addr, items); err != nil {
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
		items = append(items, c.normalizePull(&rows[i], metaRepo(rows[i].Repository)))
	}
	next := walk.next(len(rows))
	return forgeapi.Page[forgeapi.PullRequest]{
		Items:   items,
		Next:    next,
		Partial: c.pagePartial(len(rows), next, walk.page),
	}
}

// foldRows fills the rows' folded verdicts in ROTATION order.
//
// The window starts at the row after the one the stored cursor names rather than at
// the first, so the rows a previous interval could not reach are the ones this
// interval serves: without that, a poller presenting the same list in the same
// order folds the same prefix forever and every row past the per-interval cap
// starves. The items keep the instance's own order; what moves is which of them the
// interval's budget buys.
// An upstream THROTTLE is the one failure it absorbs, and the envelope is what
// assigns the marker: a throttle refusal answered upstream is one of that reason's
// two producers, so the row the throttle refused and every row this interval had not
// yet reached carry it, and no further fold is issued into an instance that has just
// refused one. Any other upstream failure has no honest marker in the published
// vocabulary, so it FAILS the call, which is what the single-pull-request read on the
// same fold already does.
func (c *Client) foldRows(ctx context.Context, op string, addr *address, items []forgeapi.PullRequest) error {
	if len(items) == 0 {
		return nil
	}
	list := listKey(op, addr.named)
	start := c.rotationStart(list, items, c.core.BudgetState().RotationCursor)
	for n := range items {
		at := (start + n) % len(items)
		issued, err := c.foldOntoRow(ctx, op, addr, &items[at])
		if issued {
			c.rememberFoldPosition(list, at)
		}
		if err != nil {
			if transport.IsThrottled(err) {
				c.markThrottled(items, start, n)
				return nil
			}
			return err
		}
	}
	return nil
}

// markThrottled marks the row a throttle refused and every row this interval had not
// yet reached, so the rows carrying no verdict carry the reason instead. A row with
// nothing to fold is left alone: it was never going to be issued, so a throttle is not
// why it has no verdict.
func (c *Client) markThrottled(items []forgeapi.PullRequest, start, from int) {
	for n := from; n < len(items); n++ {
		at := (start + n) % len(items)
		if items[at].HeadSHA == "" {
			continue
		}
		items[at].Partial = c.partial(forgeapi.PartialRateLimited, 0)
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

// foldOntoRow fills one list row's folded verdict under the bounded rule: one page
// of the status endpoint, no request beyond it, and the row marked where the fold
// stopped short or was never issued. A row with no head SHA has nothing to fold and
// keeps the unknown verdict.
//
// It reports whether the fold was ISSUED, which is what the rotation advances on: a
// row nothing was spent on has not been served, so counting it would move the
// window past rows this interval never reached. A fold that FAILED is reported to the
// caller rather than turned into a marker here, because which reason an upstream
// failure earns is the list's decision and only one of them has a marker at all.
//
// The fold addresses the list's repository, the successor where the list moved.
func (c *Client) foldOntoRow(ctx context.Context, op string, list *address, item *forgeapi.PullRequest) (bool, error) {
	if item.HeadSHA == "" {
		return false, nil
	}
	if reason, ok := c.core.AdmitFold(op); !ok {
		item.Partial = c.partial(reason, 0)
		return false, nil
	}
	started := time.Now()
	checks, err := c.foldStatus(ctx, op, list, item.HeadSHA, 1)
	c.core.SpendFold(time.Since(started))
	c.core.Rotate(foldKey(item))
	if err != nil {
		return true, err
	}
	item.Action.Checks = checks.State
	item.Action.ChecksPassing = checks.Passing
	item.Action.ChecksFailing = checks.Failing
	item.Action.ChecksPending = checks.Pending
	item.Action.ChecksNeutral = checks.Neutral
	item.Action.ChecksUnknown = checks.Unknown
	item.Action.ChecksTotal = checks.Total
	item.Partial = checks.Partial
	return true, nil
}

// ReadPR implements [forgeapi.PullRequests].
//
// It is two to four REST calls on this family, which is the cost this operation
// publishes: the pull-request read, plus the folded commit status
// [forgeapi.ActionState.Checks] comes from, since there is no document to fold it
// into the first. The second request is always issued, and the fold is COMPLETE,
// so it follows that status's pages until it is total: the low end of the range is
// a single-page fold and the high end is [forgeapi.Budget.StatusPages]. None of it
// is counted against the folded-status cap the client keeps per rolling interval,
// because this read is priced per call. A fold that reaches that page bound
// reports [forgeapi.PartialPaginationCap] in [forgeapi.PullRequest.Partial] with
// the verdict at [forgeapi.CheckUnknown] rather than a verdict over part of the
// evidence.
//
// Its absence answer is the union, [forgeapi.CodeRepoOrPRNotVisible], always. One
// REST status carries no evidence separating a pull request that does not exist
// from a repository the credential cannot see; the body carries a message and no
// machine-readable cause, mapping on message text is the guess this library
// refuses, and a third read to prove the repository exists would break that
// published cost.
//
// A [forgeapi.PullRequest] carries no successor, so a repository that moved answers
// [forgeapi.CodeRepoRefStale] carrying it, at the first read and its hop.
func (c *Client) ReadPR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	const op = "ReadPR"
	ctx = c.core.Call(ctx, op)
	addr := &address{named: repo}
	record, err := c.readPull(ctx, op, addr, pr)
	if err != nil {
		return forgeapi.PullRequest{}, err
	}
	if stale := c.moved(ctx, op, addr); stale != nil {
		return forgeapi.PullRequest{}, stale
	}
	item := c.normalizePull(record, repo)
	if item.HeadSHA == "" {
		return item, nil
	}
	checks, err := c.foldStatus(ctx, op, addr, item.HeadSHA, c.core.Budget().StatusPages)
	if err != nil {
		return forgeapi.PullRequest{}, err
	}
	if stale := c.moved(ctx, op, addr); stale != nil {
		return forgeapi.PullRequest{}, stale
	}
	item.Action.Checks = checks.State
	item.Action.ChecksPassing = checks.Passing
	item.Action.ChecksFailing = checks.Failing
	item.Action.ChecksPending = checks.Pending
	item.Action.ChecksNeutral = checks.Neutral
	item.Action.ChecksUnknown = checks.Unknown
	item.Action.ChecksTotal = checks.Total
	item.Partial = checks.Partial
	return item, nil
}

// readPull is the pull-request record read, which two operations share.
func (c *Client) readPull(ctx context.Context, op string, addr *address, pr forgeapi.PRRef) (*wirePull, error) {
	if err := c.checkRepo(addr.named); err != nil {
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
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
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
	if err := c.checkRepo(repo); err != nil {
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

// RerunFailedChecks implements [forgeapi.PullRequests].
//
// This is the operation the two products of this family differ on, and it is why
// a missing verb is a CAPABILITY here rather than an unimplemented role: one
// package serves both, so failing to satisfy the role would withdraw the
// operation from the product that does serve it. An instance without it reports
// [forgeapi.CapRerunChecks] as no or unknown with its evidence, and the refusal
// arrives from the capability rather than from a call that cannot work:
// [forgeapi.CodeCapabilityUnsupported], with that evidence on it and no request
// sent.
//
// The head pin binds here and costs no request of its own: the route is addressed
// by a server-allocated run id, so the read that resolves it is filtered BY the
// caller's SHA, and a SHA no run carries means the pull request has moved since
// the caller's row was rendered. That is refused rather than re-run, because a
// re-run can carry deployment side effects and a row displaying one commit's red
// status must not act on another's. An empty SHA means the forge reported no head,
// and the re-run then proceeds against the FIRST run the instance returns, which is
// the one case where the pin is unavailable rather than waived, and which is the
// instance's own ordering rather than a newest this route's document states.
//
//nolint:revive // unused-parameter: the pull-request reference is the published signature's; this product's route is addressed by the head commit's own run, which the head SHA pins
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
	run, ok := pickRun(runs.WorkflowRuns, headSHA)
	if !ok {
		return c.core.Fail(ctx, op, transport.REST(http.MethodGet), forgeapi.CodeStaleHead, forgeapi.KindConflict, http.StatusOK,
			"no workflow run carries the head this caller pinned")
	}
	rest := "/actions/runs/" + strconv.FormatInt(run.ID, 10) + "/rerun-failed-jobs"
	return c.sendAt(ctx, op, http.MethodPost, addr, rest, nil, nil)
}

// pickRun is the head pin read off the resolving read's own rows: the run whose
// head matches what the caller pinned, or the FIRST row the instance returned where
// the caller brought no pin.
//
// That arm claims no more than it knows. The runs route declares a head filter, a
// branch, an event, an actor and a status, and no sort parameter and no ordering at
// all, so the first row is the instance's own choice rather than the newest run,
// and this comment says so rather than resting a re-run on an order no document
// states. The PINNED arm is sound for a different reason: the head SHA is a
// declared query parameter, so upstream applies the filter and the comparison here
// is a second check rather than the only one.
func pickRun(runs []wireRun, headSHA string) (wireRun, bool) {
	for i := range runs {
		if headSHA == "" || runs[i].head() == headSHA {
			return runs[i], true
		}
	}
	return wireRun{}, false
}

// readRuns is the one read of a repository's Actions runs, which the re-run and the
// run listing share: the re-run filters it by the head it pins, and the listing
// pages it with no head filter at all.
func (c *Client) readRuns(ctx context.Context, op string, addr *address, query url.Values) (wireRuns, error) {
	var runs wireRuns
	err := c.readAt(ctx, op, addr, "/actions/runs", query, &runs)
	return runs, err
}

// ListRuns implements [forgeapi.Checks].
//
// Both products answer the same envelope around their own row: Gitea's carries a
// status beside a conclusion, Forgejo's a status carrying the outcome under its own
// field names, and the row type reads both. Neither names the workflow it ran, only
// its file, so [forgeapi.Run.Name] is that file's name without its YAML extension.
// It is not gated on [forgeapi.CapRerunChecks], the re-run verb's, since both
// products carry the route whether or not they can re-run.
//
// The continuation is the one list of this family not decided by a short page. The
// envelope states the listing's whole total, and an instance serves at most its own
// maximum page size whatever limit is asked for, so a page continues exactly while
// the rows the walk has been served are fewer than that total, and the served count
// travels in the continuation. Every page of a walk sends the limit its first page
// was asked for, the smaller of the page bound and the instance's stated maximum,
// carried in the continuation, and a page number from 1, so the instance pages at
// one size throughout: a later call at another bound, or on a connection whose
// instance states another maximum, is refused rather than paging at a second size;
// Forgejo answers a request with no page number with every run of the repository.
// An envelope with no total says neither that the listing ended nor that it goes
// on, so it is refused as the malformed answer it is, and so is a total below zero,
// which counts nothing. A page that serves no row while the total states more ends
// the walk, since no later page can serve what it did not, and carries
// [forgeapi.PartialResultWindow] with what the walk was served.
func (c *Client) ListRuns(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Run], error) {
	const op = "ListRuns"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	set, err := listOptions(op, runList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	walk, err := runWalkAt(c.core.PageCall(op, repo, set), set.After)
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
	items := make([]forgeapi.Run, 0, len(rows))
	for i := range rows {
		items = append(items, c.normalizeRun(&rows[i], repo))
	}
	next := walk.next(len(rows), total)
	partial := c.pagePartial(len(rows), next, walk.page)
	if served := walk.served + len(rows); next == "" && served < total {
		partial = c.partial(forgeapi.PartialResultWindow, served)
		partial.OmittedAtLeast = total - served
	}
	return forgeapi.Page[forgeapi.Run]{Items: items, Next: next, Partial: partial, Successor: addr.successor}, nil
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
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
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
	ctx = c.core.Call(ctx, op)
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
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
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
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
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
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
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

// labelIDs resolves the caller's label NAMES onto the identifiers both products'
// creation options take.
//
// The published signature carries names and the wire field is an array of numeric
// ids on both products, measured against each one's own swagger document, so
// sending the names is a request every instance of this family refuses. Translating
// them costs the repository's own label list, which is why a creation's published
// price carries a second request where the caller named labels and one where it did
// not. A name that read does not answer is refused rather than dropped, because a
// creation that silently lost a label the caller asked for is the worse of the two
// answers.
//
// The read is ONE page at the instance's stated maximum, which bounds what the
// refusal can claim: a repository whose label list is longer than that page, and an
// organization's own labels, which this route does not return at all, are outside
// what was read, so the refusal states the page it read rather than the repository.
//
// Which of the two refusals a missing name gets is decided by the page itself. A
// page that came back FULL is the only evidence this product offers that a list
// continues, the same witness its continuations are minted from, so a name absent
// from a full page may exist past the bound and is refused under the truncated code;
// a page that came back SHORT is the repository's whole label list, and a name
// absent from it exists nowhere this route can reach.
//
// A read that meets a move refuses with the successor and no write is sent: writing
// to a repository the caller did not name is the caller's decision.
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
	if err := c.readAt(ctx, op, addr, "/labels", query, &rows); err != nil {
		return nil, err
	}
	if stale := c.moved(ctx, op, addr); stale != nil {
		return nil, stale
	}
	byName := make(map[string]int64, len(rows))
	for _, row := range rows {
		byName[row.Name] = row.ID
	}
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		id, ok := byName[name]
		if !ok {
			return nil, c.unresolvedLabel(ctx, op, name, len(rows), limit)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// unresolvedLabel is the refusal a label name with no identifier behind it gets,
// under the code the page it was looked for in earns.
//
// The kind is the upstream one this family's other local refusal over an answered
// read already uses: no request carrying the label was sent, the request that WAS
// sent answered 200, and nothing here is a merge, so the merge kind would tell a
// caller their pull request is unmergeable because an issue's label did not resolve.
func (c *Client) unresolvedLabel(ctx context.Context, op, name string, rows, limit int) *forgeapi.Error {
	code := forgeapi.CodeValidation
	read := "which was the whole list"
	if rows >= limit {
		code = forgeapi.CodeLabelPageTruncated
		read = "which came back full, so the list continues past it"
	}
	return c.core.Fail(ctx, op, transport.REST(http.MethodGet), code, forgeapi.KindUpstream, http.StatusOK,
		"no label named "+name+" on the first page of this repository's labels, bounded at "+
			strconv.Itoa(limit)+", "+read)
}

// CloseIssue implements [forgeapi.Issues].
func (c *Client) CloseIssue(ctx context.Context, repo forgeapi.RepoRef, issue forgeapi.IssueRef) (forgeapi.Issue, error) {
	const op = "CloseIssue"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Issue{}, err
	}
	if issue.Number <= 0 {
		return forgeapi.Issue{}, errorf(forgeapi.CodeRepoRefInvalid, "issue number is not positive")
	}
	var record wireIssue
	rest := "/issues/" + strconv.Itoa(issue.Number)
	if err := c.sendAt(ctx, op, http.MethodPatch, &address{named: repo}, rest, map[string]any{"state": stateClosed}, &record); err != nil {
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
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
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
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
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
