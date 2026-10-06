package gitlab

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// maxStatusPageItems is the page size the folded-status read asks for, which is this
// product's own maximum response items: that read's own pages are what the fold is
// computed over, so a smaller page spends more requests for the same verdict.
const maxStatusPageItems = 100

// maxPipelinePageItems is the page size the re-run's resolving read asks for. It is
// small because that read wants ONE pipeline, the one carrying the head the caller
// pinned, and upstream applies the pin as a declared filter.
const maxPipelinePageItems = 20

// The request-body fields several mutations share.
const (
	keyDescription = "description"
	keyStateEvent  = "state_event"
	keyLabels      = "labels"
)

// keyLabelDetails is the parameter that makes a row's labels arrive as objects rather
// than as bare names, which is what the normalized label's colour and description need.
const keyLabelDetails = "with_labels_details"

// Whoami implements [forgeapi.Identity].
//
// [forgeapi.Account.Scopes] is nil on this product and that is a cannot-supply rather
// than an omission: the identity route carries no scope list, and the route that does
// describes one token rather than the connection's grant, so a scope set read there
// would answer a different question from the one this field asks.
func (c *Client) Whoami(ctx context.Context) (forgeapi.Account, error) {
	const op = "Whoami"
	ctx = c.core.Call(ctx, op)
	var user restUser
	if _, err := c.readJSON(ctx, op, "/user", nil, &user); err != nil {
		return forgeapi.Account{}, err
	}
	return forgeapi.Account{
		Login:  user.Username,
		Name:   user.Name,
		Email:  user.Email,
		WebURL: user.WebURL,
	}, nil
}

// ListRepos implements [forgeapi.Repos].
//
// It asks for the projects the credential is a MEMBER of, which is this operation's
// published question read against a product whose unfiltered project list is every
// project the instance makes visible. On a hosted instance that list is millions of
// rows in no order a consumer asked for, so sending no filter would answer a
// different question at a price no page bound reaches; the sibling family answers the
// same operation from the credential's own repositories for the same reason.
func (c *Client) ListRepos(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Repository], error) {
	const op = "ListRepos"
	ctx = c.core.Call(ctx, op)
	set, err := listing(op, fixedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Repository]{}, err
	}
	query, walk, err := c.restQuery(op, forgeapi.RepoRef{}, set, fixedList)
	if err != nil {
		return forgeapi.Page[forgeapi.Repository]{}, err
	}
	query.Set("membership", "true")
	if bounded, ok := c.unpagedList[forgeapi.Repository](walk.here()); ok {
		return bounded, nil
	}
	var rows []restProject
	header, err := c.readJSON(ctx, op, "/projects", query, &rows)
	if err != nil {
		if page, ok := c.deferredPage[forgeapi.Repository](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.Repository]{}, err
	}
	items := make([]forgeapi.Repository, 0, len(rows))
	for i := range rows {
		items = append(items, c.normalizeRepo(&rows[i]))
	}
	next := walk.next(header)
	return forgeapi.Page[forgeapi.Repository]{
		Items:   items,
		Next:    next,
		Partial: c.pagePartial(len(rows), next, walk.page),
	}, nil
}

// ListPRs implements [forgeapi.PullRequests].
//
// It reads the list document rather than the REST list, and the reason here is
// staleness rather than absence: this product documents that listing may not
// proactively refresh the merge status because doing so is expensive, and the
// parameter that asks for a refresh guarantees nothing and can be ignored
// outright for a caller below a certain project role. A stale value is worse than
// a missing one, because nothing on the wire tells the two apart.
//
// It is ONE request per page on both of its arms. A connection whose setup found the
// documents refused reads the REST list instead, from its first call, and on that
// path the block reason and the armed auto-merge are reported unknown rather than
// trusted, because this product documents that list as able to serve a merge status
// nothing refreshed. A document refused at RUNTIME on a connection whose setup
// question was answered is that call's failure and marks the connection, so the next
// call reads REST and no call spends two requests where its row publishes one.
//
// Three action fields come from the document by name: the detailed merge status
// fills [forgeapi.ActionState.MergeBlocked], the auto-merge flag fills
// [forgeapi.ActionState.AutoMergeArmed], and the head pipeline's status fills
// [forgeapi.ActionState.Checks]. The measurement took that last one as ONE SCALAR: the
// document returns a nullable head pipeline whose status is a single enum member,
// so there is no collection to page and the bounded-fold rule every other list
// obeys has nothing to bound here. A row therefore never reads
// [forgeapi.CheckUnknown] for a pagination cap,
// [forgeapi.PartialPaginationCap] never arrives from this field, and the six
// per-state counts stay zero because one scalar cannot supply them. A row carries
// that marker only where its label connection names a further page. The pipeline's
// own SHA is not this pull request's head either, so it never fills
// [forgeapi.PullRequest.HeadSHA], which reads the diff head instead.
func (c *Client) ListPRs(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	const op = "ListPRs"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	set, err := listing(op, statefulList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	if c.isDegraded() {
		return c.degradedPullPage(ctx, op, repo, set)
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
		"fullPath": repo.Selector,
		"state":    listState(set.State),
		"first":    set.PageBound,
		"after":    nullableString(after),
	}, &payload)
	if err != nil {
		if page, ok := c.deferredPage[forgeapi.PullRequest](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	if payload.Project == nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, c.core.Fail(ctx, op, transport.Document(http.MethodPost),
			forgeapi.CodeRepoOrPRNotVisible, forgeapi.KindNotFound, http.StatusOK,
			"the document resolved no project for this selector")
	}
	c.recordPermissions(payload.Project)
	rows := payload.Project.MergeRequests
	if rows == nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, c.core.FailBody(ctx, op, transport.Document(http.MethodPost), http.StatusOK)
	}
	items := make([]forgeapi.PullRequest, 0, len(rows.Nodes))
	for i := range rows.Nodes {
		items = append(items, c.normalizeDocPull(&rows.Nodes[i], c.documentRepo(payload.Project, repo)))
	}
	page := forgeapi.Page[forgeapi.PullRequest]{Items: items}
	if rows.PageInfo.HasNextPage {
		page.Next = encodeAfter(call, rows.PageInfo.EndCursor)
	}
	if result.Partial {
		page.Partial = c.partial(forgeapi.PartialGraphQLPartial, len(items))
	}
	return page, nil
}

// nullableString is a cursor variable a document takes as null rather than as an
// empty string, because the empty string is a position upstream refuses and null is
// the first page.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// documentRepo is the repository a document's rows belong to, taken from the
// canonical path the selection returns rather than from the selector asked for.
//
// That is what marks a RENAME on this arm: there is no redirect to record, since the
// selection resolves a renamed project silently at 200, so the document selects the
// canonical path and a difference against the selector is the move. The successor is
// not published on this arm, because a repository-addressed list of this product
// answers no successor for the caller to re-point to; the difference reaches the
// caller as the row's own reference.
func (c *Client) documentRepo(project *docProject, asked forgeapi.RepoRef) forgeapi.RepoRef {
	if project.FullPath == "" || project.FullPath == asked.Selector {
		return asked
	}
	return repoRef(project.FullPath)
}

// recordPermissions holds what a document reported for the project permissions, which
// is where [Client.GrantCaps] answers from at no request of its own.
func (c *Client) recordPermissions(project *docProject) {
	if project.UserPermissions == nil {
		return
	}
	c.rememberGrant(project.UserPermissions.PushCode, project.UserPermissions.ReadMergeRequest)
}

// degradedPullPage is the per-repository list a connection whose document was refused
// falls back to. It reads the REST list and marks the two fields that read would have
// to be trusted on, because this product documents that list as able to serve a merge
// status nothing refreshed.
func (c *Client) degradedPullPage(ctx context.Context, op string, repo forgeapi.RepoRef, set forgeapi.ListSettings) (forgeapi.Page[forgeapi.PullRequest], error) {
	query, walk, err := c.restQuery(op, repo, set, statefulList)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	query.Set(keyLabelDetails, "true")
	if bounded, ok := c.unpagedList[forgeapi.PullRequest](walk.here()); ok {
		return bounded, nil
	}
	var rows []restMergeRequest
	header, err := c.readJSON(ctx, op, projectRoute(repo, "/merge_requests"), query, &rows)
	if err != nil {
		if page, ok := c.deferredPage[forgeapi.PullRequest](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	items := make([]forgeapi.PullRequest, 0, len(rows))
	for i := range rows {
		items = append(items, c.normalizeRESTPull(&rows[i], repo, restDegraded))
	}
	next := walk.next(header)
	return forgeapi.Page[forgeapi.PullRequest]{
		Items:   items,
		Next:    next,
		Partial: c.pagePartial(len(rows), next, walk.page),
	}, nil
}

// ListMyPRs implements [forgeapi.PullRequests].
//
// This is a REST read on this product, the instance-wide merge-request list scoped to
// what the credential created and to the open state, and it takes no project path at
// all: it is scoped by the credential, so its rows span projects and each carries its
// own [forgeapi.PullRequest.Repo], recovered from the row's own full reference rather
// than from the numeric project id beside it, which is not a canonical selector.
//
// Under [forgeapi.WithOwner] it reads that group's own merge-request list instead,
// subgroups included, with the same open state and the same row, so the call is still
// one request per page and the group is resolved by the instance rather than by a read
// of its own. A group the instance does not resolve, and a user's own namespace, which
// has no listing route on this product, both answer 404, which this family reports as
// [forgeapi.CodeOwnerUnresolved].
//
// It does NOT read a document, so what a row can carry is what the REST row carries.
// That row carries no head pipeline, so the folded verdict is unknown and the six
// per-state counts stay zero. It names a row's source project by id alone, so each
// distinct fork on the page costs one GET /projects/:id ([Client.resolveForks]); a
// same-project row and a deleted fork cost nothing. Every row's block reason is
// unknown, under either scope, because this product documents that listing may not
// refresh the merge status; [Client.ReadPR] names it on a connection whose documents
// answer.
func (c *Client) ListMyPRs(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.PullRequest], error) {
	const op = opListMyPRs
	ctx = c.core.Call(ctx, op)
	set, err := listing(op, scopedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	query, walk, err := c.restQuery(op, forgeapi.RepoRef{}, set, scopedList)
	if err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	route := scopedRoute(set, query, "merge_requests")
	query.Set(keyLabelDetails, "true")
	if bounded, ok := c.unpagedList[forgeapi.PullRequest](walk.here()); ok {
		return bounded, nil
	}
	var rows []restMergeRequest
	header, err := c.readJSON(ctx, op, route, query, &rows)
	if err != nil {
		if page, ok := c.deferredPage[forgeapi.PullRequest](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	items := make([]forgeapi.PullRequest, 0, len(rows))
	for i := range rows {
		items = append(items, c.normalizeRESTPull(&rows[i], forgeapi.RepoRef{}, restListed))
	}
	if err := c.resolveForks(ctx, op, rows, items); err != nil {
		return forgeapi.Page[forgeapi.PullRequest]{}, err
	}
	next := walk.next(header)
	return forgeapi.Page[forgeapi.PullRequest]{
		Items:   items,
		Next:    next,
		Partial: c.pagePartial(len(rows), next, walk.page),
	}, nil
}

// resolveForks names each fork row's source project, one GET /projects/:id per
// distinct fork on the page, because this route names a row's projects by numeric
// id alone. A fork the lookup cannot read, 404 or 403, stays the zero
// reference, as a deleted fork's null id does with no request. A lookup the
// governor defers or the instance throttles ends the lookups and keeps the page
// already paid for: each fork row left unresolved keeps the zero reference and
// carries the rate-limited reason, as the page read's own deferral does.
func (c *Client) resolveForks(ctx context.Context, op string, rows []restMergeRequest, items []forgeapi.PullRequest) error {
	forks := map[int64]forgeapi.RepoRef{}
	stopped := false
	for i := range rows {
		source, target := rows[i].SourceProjectID, rows[i].TargetProjectID
		if source == nil || target == nil || *source == *target {
			continue
		}
		ref, seen := forks[*source]
		if !seen && !stopped {
			var err error
			ref, err = c.projectByID(ctx, op, *source)
			switch {
			case transport.IsDeferred(err) || transport.IsThrottled(err):
				stopped = true
			case err != nil:
				return err
			default:
				forks[*source], seen = ref, true
			}
		}
		if !seen {
			items[i].Partial = c.partial(forgeapi.PartialRateLimited, 0)
			continue
		}
		items[i].SourceRepo = ref
	}
	return nil
}

// projectByID is the reference for one numeric project id, the zero reference
// where the instance answers 404 or 403 for it.
func (c *Client) projectByID(ctx context.Context, op string, id int64) (forgeapi.RepoRef, error) {
	var project struct {
		PathWithNamespace string `json:"path_with_namespace"`
	}
	if _, err := c.readJSON(ctx, op, "/projects/"+strconv.FormatInt(id, 10), nil, &project); err != nil {
		var fe *forgeapi.Error
		if asForgeError(err, &fe) && (fe.Status == http.StatusNotFound || fe.Status == http.StatusForbidden) {
			return forgeapi.RepoRef{}, nil
		}
		return forgeapi.RepoRef{}, err
	}
	if project.PathWithNamespace == "" {
		return forgeapi.RepoRef{}, nil
	}
	return repoRef(project.PathWithNamespace), nil
}

// ReadPR implements [forgeapi.PullRequests].
//
// It reads the single-merge-request document, whose field set is its own, and its
// two absence answers are separated by the envelope rather than by the status: a
// resolved project with no merge request is [forgeapi.CodePRNotFound], a null
// project is [forgeapi.CodeRepoOrPRNotVisible], and the degraded REST path
// reports the union because there the status is all there is.
//
// It is ONE request on both of its arms, and neither arm is a page of a fold: the
// fold on a single-pull-request read is COMPLETE elsewhere and bounded by
// [forgeapi.Budget.StatusPages], and the measurement found that this family has no
// collection to fold at all, only the scalar head-pipeline status ListPRs above
// reads; its label connection is cut and marked as a ListPRs row's is. The second
// arm is the REST read a connection whose setup found the documents refused takes
// instead, from its first call, so the discovery is the connection's cost rather
// than this call's.
func (c *Client) ReadPR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	const op = "ReadPR"
	ctx = c.core.Call(ctx, op)
	read, err := c.readMergeRequest(ctx, op, repo, pr)
	if err != nil {
		return forgeapi.PullRequest{}, err
	}
	return read.item, nil
}

// mergeRequestRead is one single-merge-request read's answer: the normalized pull
// request, and the merged verdict beside it.
//
// The verdict rides here rather than being recomputed from the item because this
// product has no merged BOOLEAN: the answer is derived from the state member and a
// nullable merged timestamp, and the timestamp does not survive normalization, since
// no published field carries it.
type mergeRequestRead struct {
	item   forgeapi.PullRequest
	merged forgeapi.Support
}

// readMergeRequest is the single-merge-request read two operations share: the document
// where this connection's documents work, and the REST read where they do not.
func (c *Client) readMergeRequest(ctx context.Context, op string, repo forgeapi.RepoRef, pr forgeapi.PRRef) (mergeRequestRead, error) {
	if err := c.checkRepo(repo); err != nil {
		return mergeRequestRead{}, err
	}
	if pr.Number <= 0 {
		return mergeRequestRead{}, errorf(forgeapi.CodeRepoRefInvalid, "pull-request number is not positive")
	}
	if c.isDegraded() {
		return c.degradedRead(ctx, op, repo, pr)
	}
	var payload docPayload
	if _, err := c.execute(ctx, op, prRead, map[string]any{
		"fullPath": repo.Selector,
		"iid":      strconv.Itoa(pr.Number),
	}, &payload); err != nil {
		return mergeRequestRead{}, err
	}
	if payload.Project == nil {
		return mergeRequestRead{}, c.core.Fail(ctx, op, transport.Document(http.MethodPost),
			forgeapi.CodeRepoOrPRNotVisible, forgeapi.KindNotFound, http.StatusOK,
			"the document resolved no project for this selector")
	}
	c.recordPermissions(payload.Project)
	node := payload.Project.MergeRequest
	if node == nil {
		return mergeRequestRead{}, c.core.Fail(ctx, op, transport.Document(http.MethodPost),
			forgeapi.CodePRNotFound, forgeapi.KindNotFound, http.StatusOK,
			"the document resolved the project and no merge request with that number")
	}
	return mergeRequestRead{
		item:   c.normalizeDocPull(node, c.documentRepo(payload.Project, repo)),
		merged: c.mergedSupport("merge request state (document)", node.State, node.MergedAt),
	}, nil
}

// degradedRead is the single-merge-request REST read a connection whose document was
// refused falls back to. Its absence answer is the UNION, because one REST status
// carries no evidence separating a merge request that does not exist from a project
// the credential cannot see, and mapping on the message text is the guess this library
// refuses.
func (c *Client) degradedRead(ctx context.Context, op string, repo forgeapi.RepoRef, pr forgeapi.PRRef) (mergeRequestRead, error) {
	var record restMergeRequest
	path := projectRoute(repo, "/merge_requests/"+strconv.Itoa(pr.Number))
	if _, err := c.readJSON(ctx, op, path, nil, &record); err != nil {
		return mergeRequestRead{}, err
	}
	return mergeRequestRead{
		item:   c.normalizeRESTPull(&record, repo, restDegraded),
		merged: c.mergedSupport("merge request state (rest)", record.State, record.MergedAt),
	}, nil
}

// CreatePR implements [forgeapi.PullRequests].
//
// It is ONE request, which is what this product's own creation takes: the labels a
// caller names go on the wire as NAMES, so nothing has to resolve them to identifiers
// first, and the draft flag is the title prefix this product marks a draft with rather
// than a field of its own.
//
//nolint:gocritic // hugeParam: the record is the published signature's own parameter
func (c *Client) CreatePR(ctx context.Context, repo forgeapi.RepoRef, req forgeapi.NewPullRequest) (forgeapi.PullRequest, error) {
	const op = "CreatePR"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.PullRequest{}, err
	}
	title := req.Title
	if req.Draft {
		title = "Draft: " + req.Title
	}
	body := map[string]any{
		"title":         title,
		keyDescription:  req.Body,
		"source_branch": req.SourceBranch,
		"target_branch": req.TargetBranch,
	}
	if len(req.Labels) > 0 {
		body[keyLabels] = req.Labels
	}
	var record restMergeRequest
	if err := c.sendJSON(ctx, op, http.MethodPost, projectRoute(repo, "/merge_requests"), body, &record); err != nil {
		return forgeapi.PullRequest{}, err
	}
	return c.normalizeRESTPull(&record, repo, restMutated), nil
}

// ClosePR implements [forgeapi.PullRequests].
func (c *Client) ClosePR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	return c.setPullState(ctx, "ClosePR", repo, pr, "close")
}

// ReopenPR implements [forgeapi.PullRequests].
func (c *Client) ReopenPR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.PullRequest, error) {
	return c.setPullState(ctx, "ReopenPR", repo, pr, "reopen")
}

// setPullState is the one mutation both lifecycle transitions make. This product takes
// a state EVENT rather than the target state, which is why the two transitions send
// different words from the ones the state enumeration spells.
func (c *Client) setPullState(ctx context.Context, op string, repo forgeapi.RepoRef, pr forgeapi.PRRef, event string) (forgeapi.PullRequest, error) {
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.PullRequest{}, err
	}
	if pr.Number <= 0 {
		return forgeapi.PullRequest{}, errorf(forgeapi.CodeRepoRefInvalid, "pull-request number is not positive")
	}
	var record restMergeRequest
	path := projectRoute(repo, "/merge_requests/"+strconv.Itoa(pr.Number))
	if err := c.sendJSON(ctx, op, http.MethodPut, path, map[string]any{keyStateEvent: event}, &record); err != nil {
		return forgeapi.PullRequest{}, err
	}
	return c.normalizeRESTPull(&record, repo, restMutated), nil
}

// RerunFailedChecks implements [forgeapi.PullRequests].
//
// It is TWO requests: the pipeline carrying the head the caller pinned has to be
// resolved before it can be retried, because this product's retry verb is addressed by
// a server-allocated pipeline id rather than by a commit.
//
// Those two are all of them, on a fresh connection as on a used one. The capability
// this operation is gated on is the CONNECTION's, resolved and priced at setup by
// [Client.ConnectionCaps], so this call reads the verdict it holds and sends nothing to
// obtain one.
//
// The head pin binds and costs no request of its own: the resolving read is filtered BY
// the caller's SHA, which this product declares as a parameter of that route, and a SHA
// no pipeline carries means the merge request has moved since the caller's row was
// rendered. That is refused rather than retried, because a re-run can carry deployment
// side effects and a row displaying one commit's red status must not act on another's.
// An empty SHA means the forge reported no head, and the re-run then proceeds against
// the FIRST pipeline the instance returns, which is the one case where the pin is
// unavailable rather than waived.
//
// The verb retries the failed and canceled jobs of that pipeline rather than the whole
// pipeline, which is what this operation publishes.
//
//nolint:revive // unused-parameter: the pull-request reference is the published signature's; this product's route is addressed by the head commit's own pipeline, which the head SHA pins
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
	query := url.Values{keyPerPage: {strconv.Itoa(maxPipelinePageItems)}}
	if headSHA != "" {
		query.Set("sha", headSHA)
	}
	pipelines, _, err := c.readPipelines(ctx, op, repo, query)
	if err != nil {
		return err
	}
	pipeline, ok := pickPipeline(pipelines, headSHA)
	if !ok {
		return c.core.Fail(ctx, op, transport.REST(http.MethodGet), forgeapi.CodeStaleHead, forgeapi.KindConflict, http.StatusOK,
			"no pipeline carries the head this caller pinned")
	}
	path := projectRoute(repo, "/pipelines/"+strconv.FormatInt(pipeline.ID, 10)+"/retry")
	return c.sendJSON(ctx, op, http.MethodPost, path, nil, nil)
}

// pickPipeline is the head pin read off the resolving read's own rows: the pipeline
// whose head matches what the caller pinned, or the FIRST row the instance returned
// where the caller brought no pin.
//
// That second arm claims no more than it knows. This product's pipelines route declares
// a sha filter and an order parameter, and this read sends no order, so the first row is
// the instance's own choice rather than the newest pipeline. The PINNED arm is sound for
// a different reason: the SHA is a declared filter, so upstream applies it and the
// comparison here is a second check rather than the only one.
func pickPipeline(pipelines []restPipeline, headSHA string) (restPipeline, bool) {
	for i := range pipelines {
		p := &pipelines[i]
		if p.ID == 0 {
			continue
		}
		if headSHA == "" || p.SHA == headSHA {
			return *p, true
		}
	}
	return restPipeline{}, false
}

// readPipelines is the one read of a project's pipelines, which the re-run and the run
// listing share: the re-run filters it by the head it pins, and the listing pages it
// with no head filter at all. It answers the headers beside the rows, because the
// listing's continuation rides there.
func (c *Client) readPipelines(ctx context.Context, op string, repo forgeapi.RepoRef, query url.Values) ([]restPipeline, http.Header, error) {
	var pipelines []restPipeline
	header, err := c.readJSON(ctx, op, projectRoute(repo, "/pipelines"), query, &pipelines)
	return pipelines, header, err
}

// ListRuns implements [forgeapi.Checks].
//
// A run is a pipeline, the row the re-run resolves its pipeline from: its state is
// the status folded through the merge request's head-pipeline table, its branch the
// ref as this product spells it, a refs/ path on the pipelines a merge request or a
// workload starts, and its name the pipeline's own, empty unless configured. It is
// not gated on [forgeapi.CapRerunChecks], the re-run verb's, since every project
// carries the route.
func (c *Client) ListRuns(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Run], error) {
	const op = "ListRuns"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	set, err := listing(op, fixedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	query, walk, err := c.restQuery(op, repo, set, fixedList)
	if err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	if bounded, ok := c.unpagedList[forgeapi.Run](walk.here()); ok {
		return bounded, nil
	}
	rows, header, err := c.readPipelines(ctx, op, repo, query)
	if err != nil {
		if page, ok := c.deferredPage[forgeapi.Run](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	items := make([]forgeapi.Run, 0, len(rows))
	for i := range rows {
		items = append(items, c.normalizeRun(&rows[i], repo))
	}
	next := walk.next(header)
	return forgeapi.Page[forgeapi.Run]{
		Items:   items,
		Next:    next,
		Partial: c.pagePartial(len(rows), next, walk.page),
	}, nil
}

// MergePR implements [forgeapi.Merges].
//
// Two family facts decide this method. The head commit is not merely prudent
// here: a group or instance setting can make it compulsory and the request is
// refused without it, which is why it is a required field rather than an option.
// And this product exposes no per-request strategy selector at all (its project
// merge method is read-only display text), so a [forgeapi.MergeRequest.Strategy]
// is refused here; what it does accept is a squash intent it can then not apply,
// which is reported rather than swallowed.
//
// The auto-merge arm is DISARMED unless the caller asks for it, and the field it uses
// when asked is this product's current one rather than the pipeline-shaped parameter it
// deprecated. That is the defect this replaces: a merge that arms auto-merge by default
// completes later, unattended, on a pull request the user asked to merge now.
//
// Where the project merges through a train, the request is accepted and the merge
// enqueued rather than refused, which is a success state and not a refusal to merge.
// The witness for it is the answer's OWN auto-merge state, because no merge response of
// this product carries a queue position and the only fields that would name the train
// are a deprecated experiment connection the schema gate refuses. So the queue verdict
// is [forgeapi.QueueNone] and the position unknown, and an answer whose state this
// family cannot read is reported as an unknown outcome rather than as a merge.
func (c *Client) MergePR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef, req forgeapi.MergeRequest) (forgeapi.MergeOutcome, error) {
	const op = opMergePR
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
	if req.Strategy != "" {
		return forgeapi.MergeOutcome{}, errorf(forgeapi.CodeStrategyNotAllowed,
			"this product's merge takes a squash flag and an auto-merge flag and no strategy, so %q cannot be sent", req.Strategy)
	}
	body := map[string]any{
		"sha":                         req.HeadSHA,
		"squash":                      req.Intent == forgeapi.IntentSquash,
		"should_remove_source_branch": req.DeleteBranch,
	}
	if req.AutoMerge {
		body["auto_merge"] = true
	}
	var record restMergeRequest
	path := projectRoute(repo, "/merge_requests/"+strconv.Itoa(pr.Number)+"/merge")
	if err := c.sendJSON(ctx, op, http.MethodPut, path, body, &record); err != nil {
		return forgeapi.MergeOutcome{}, err
	}
	return forgeapi.MergeOutcome{
		State:         c.mergeOutcome(&record),
		QueueState:    forgeapi.QueueNone,
		QueuePosition: forgeapi.QueuePositionUnknown,
	}, nil
}

// mergeOutcome reads what happened out of the answer this product gives, which is the
// merge request itself rather than a result object.
//
// Three states are reachable and each has its own witness. A merged state or a merged
// timestamp is the merge; an answer whose auto-merge is armed is the enqueue, because
// that is what this product does with a merge it accepts and cannot complete now; and a
// refusal never reaches here, arriving as the error arm instead. Anything else is
// reported as an UNKNOWN outcome with the state named, rather than as a merge.
func (c *Client) mergeOutcome(r *restMergeRequest) forgeapi.MergeOutcomeState {
	if c.mergedSupport("merge request state (rest)", r.State, r.MergedAt) == forgeapi.SupportYes {
		return forgeapi.MergeOutcomeMerged
	}
	if supportOfPointer(r.MergeWhenPipelineSucceeds) == forgeapi.SupportYes {
		return forgeapi.MergeOutcomeEnqueued
	}
	c.unmapped("merge outcome state (rest)", r.State)
	return forgeapi.MergeOutcomeUnknown
}

// MergeStatus implements [forgeapi.Merges].
//
// It reads the single-merge-request document, whose selection carries the state and
// the merged timestamp this family derives the merged answer from and the merge
// request's own web URL. It follows no page of checks, which on this product is doubly
// true: this operation returns none, and the measurement found that the document serves
// one scalar head-pipeline status with no collection to page at all. A caller wanting
// the folded verdict calls [Client.ReadPR] on the same merge request.
//
// It is ONE request, on the same two arms [Client.ReadPR] has and for the same reason:
// it reaches that document, and a connection whose setup found the documents refused
// reads the REST record instead. Neither arm is a page of a fold, and neither spends a
// discovery, which is what makes this row the exactly one request it publishes.
//
// Queue is [forgeapi.QueueNone]: the only fields that could answer otherwise are the
// merge-train connection and its cars, both of which carry a deprecation reason, so a
// document selecting either fails the schema gate. Whether the project merges through
// a train at all is a repository affordance, read by [Client.RepoAffordances].
func (c *Client) MergeStatus(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef) (forgeapi.MergeStatus, error) {
	const op = "MergeStatus"
	ctx = c.core.Call(ctx, op)
	read, err := c.readMergeRequest(ctx, op, repo, pr)
	if err != nil {
		return forgeapi.MergeStatus{}, err
	}
	return forgeapi.MergeStatus{
		Merged: read.merged,
		Queue:  forgeapi.QueueNone,
		WebURL: read.item.WebURL,
	}, nil
}

// CommitStatus implements [forgeapi.Checks].
//
// This read addresses one commit, so its fold is COMPLETE and bounded by
// [forgeapi.Budget.StatusPages], and a fold that reaches that bound reports
// [forgeapi.PartialPaginationCap] with the verdict at [forgeapi.CheckUnknown].
//
// The fold is computed HERE rather than taken from any field upstream sends, and it
// DE-DUPLICATES to the latest row per context. This product's statuses route answers
// the historical rows, several per context where a check has been retried, so a fold
// over the rows as they arrive reports a superseded failure as live. There is no
// combined-status route to ask instead, so the de-duplication is this family's own:
// within one context the row with the greatest identifier is the live one, and the
// contexts keep the order upstream first mentioned them in.
func (c *Client) CommitStatus(ctx context.Context, repo forgeapi.RepoRef, ref string) (forgeapi.CommitChecks, error) {
	const op = "CommitStatus"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.CommitChecks{}, err
	}
	if err := forgeapi.ValidateRef(ref); err != nil {
		return forgeapi.CommitChecks{}, err
	}
	return c.foldStatus(ctx, op, repo, ref, c.core.Budget().StatusPages)
}

// foldStatus reads the commit's statuses and folds the verdict and the per-state counts
// CLIENT-side over the rows it holds.
//
// maxPages is the fold rule: the published bound on a read. A fold still short at that
// bound reports the pagination cap with the verdict unknown rather than a verdict over
// part of the evidence, and the witness for a remainder is this product's own next-page
// header rather than a full page.
func (c *Client) foldStatus(ctx context.Context, op string, repo forgeapi.RepoRef, ref string, maxPages int) (forgeapi.CommitChecks, error) {
	out := forgeapi.CommitChecks{Ref: ref}
	// A bound admitting no page is the literal zero its option publishes: nothing
	// is issued and the verdict is unknown with the page cap's own marker, where
	// clamping the bound up to one traversed a page the caller had ruled out.
	if maxPages <= 0 {
		out.State = forgeapi.CheckUnknown
		out.Partial = c.partial(forgeapi.PartialPaginationCap, 0)
		return out, nil
	}
	path := projectRoute(repo, "/repository/commits/"+url.PathEscape(ref)+"/statuses")
	var rows []restStatus
	truncated := false
	for page := 1; page <= maxPages; page++ {
		query := url.Values{
			keyPage:    {strconv.Itoa(page)},
			keyPerPage: {strconv.Itoa(maxStatusPageItems)},
		}
		var answered []restStatus
		header, err := c.readJSON(ctx, op, path, query, &answered)
		if err != nil {
			return forgeapi.CommitChecks{}, err
		}
		rows = append(rows, answered...)
		_, truncated = nextPage(header)
		if !truncated {
			break
		}
	}
	for _, row := range latestPerContext(rows) {
		out.Contexts = append(out.Contexts, forgeapi.CheckContext{
			Name:        row.Name,
			Description: row.Description,
			TargetURL:   row.TargetURL,
			State:       c.checkState("commit status state (rest)", row.Status),
		})
		// The commit a fold read is the one the rows name, which resolves a branch
		// the caller asked for onto its head. The FIRST row settles it, because a
		// later one carrying a different commit would be a row for another commit
		// entirely and overwriting from it would rename this answer.
		if out.Ref == ref && row.SHA != "" {
			out.Ref = row.SHA
		}
	}
	count(&out)
	if truncated {
		out.State = forgeapi.CheckUnknown
		out.Partial = c.partial(forgeapi.PartialPaginationCap, len(out.Contexts))
	}
	return out, nil
}

// latestPerContext keeps one row per context, the one with the greatest identifier,
// and preserves the order the contexts were first mentioned in.
//
// It exists because this product's statuses route answers the HISTORY: a context
// retried after a failure appears twice, and a fold over both reports the superseded
// row as live. The identifier is the discriminator rather than a timestamp, because
// every row carries one and it is monotonic per instance, where the finish time is
// null on a row that is still running.
func latestPerContext(rows []restStatus) []restStatus {
	order := make([]string, 0, len(rows))
	latest := make(map[string]restStatus, len(rows))
	for _, row := range rows {
		held, seen := latest[row.Name]
		if !seen {
			order = append(order, row.Name)
			latest[row.Name] = row
			continue
		}
		if row.ID >= held.ID {
			latest[row.Name] = row
		}
	}
	out := make([]restStatus, 0, len(order))
	for _, name := range order {
		out = append(out, latest[name])
	}
	return out
}

// count is the fold itself, over the rows the caller holds. The verdict is the
// worst state present, because one failing check makes the collection failing
// however many passed.
//
// The UNKNOWN arm sits second, above pending, passing and neutral, which is what
// the totality discipline asks of a fold: a value this family's table did not map is
// a check whose state was not read, and passing means every check reported succeeded.
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
// It asks for the label DETAILS, which is part of the question rather than an option:
// without that parameter a row's labels arrive as bare names, and the normalized label
// carries a colour and a description a consumer renders.
func (c *Client) ListIssues(ctx context.Context, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Issue], error) {
	const op = opListIssues
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	set, err := listing(op, statefulList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	query, walk, err := c.restQuery(op, repo, set, statefulList)
	if err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	query.Set(keyLabelDetails, "true")
	if bounded, ok := c.unpagedList[forgeapi.Issue](walk.here()); ok {
		return bounded, nil
	}
	return c.readIssuePage(ctx, op, projectRoute(repo, "/issues"), query, repo, walk)
}

// ListMyIssues implements [forgeapi.Issues].
//
// It is [Client.ListMyPRs] asking for the other collection: the instance-wide issue
// list narrowed to what the credential created, or under [forgeapi.WithOwner] that
// group's own issue list, both with the open state and the label details. The scope,
// the price and the unresolved-owner answer are that call's own. Each row carries its
// own [forgeapi.Issue.Repo], recovered from the row's own full reference, because the
// rows span projects and the numeric project id beside it is not a canonical selector.
func (c *Client) ListMyIssues(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Issue], error) {
	const op = opListMyIssues
	ctx = c.core.Call(ctx, op)
	set, err := listing(op, scopedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	query, walk, err := c.restQuery(op, forgeapi.RepoRef{}, set, scopedList)
	if err != nil {
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	route := scopedRoute(set, query, "issues")
	query.Set(keyLabelDetails, "true")
	if bounded, ok := c.unpagedList[forgeapi.Issue](walk.here()); ok {
		return bounded, nil
	}
	return c.readIssuePage(ctx, op, route, query, forgeapi.RepoRef{}, walk)
}

// readIssuePage is the read both issue lists share: one page of the route, each row
// normalized against the repository the call addressed, or against the row's own
// where the call addressed none, and the continuation the next-page header names.
func (c *Client) readIssuePage(ctx context.Context, op, route string, query url.Values, repo forgeapi.RepoRef, walk restWalk) (forgeapi.Page[forgeapi.Issue], error) {
	var rows []restIssue
	header, err := c.readJSON(ctx, op, route, query, &rows)
	if err != nil {
		if page, ok := c.deferredPage[forgeapi.Issue](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.Issue]{}, err
	}
	items := make([]forgeapi.Issue, 0, len(rows))
	for i := range rows {
		items = append(items, c.normalizeIssue(&rows[i], repo))
	}
	next := walk.next(header)
	return forgeapi.Page[forgeapi.Issue]{
		Items:   items,
		Next:    next,
		Partial: c.pagePartial(len(rows), next, walk.page),
	}, nil
}

// CreateIssue implements [forgeapi.Issues]. It is ONE request: this product's creation
// takes label NAMES, so nothing has to resolve them to identifiers first.
func (c *Client) CreateIssue(ctx context.Context, repo forgeapi.RepoRef, req forgeapi.NewIssue) (forgeapi.Issue, error) {
	const op = "CreateIssue"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Issue{}, err
	}
	body := map[string]any{"title": req.Title, keyDescription: req.Body}
	if len(req.Labels) > 0 {
		body[keyLabels] = req.Labels
	}
	var record restIssue
	if err := c.sendJSON(ctx, op, http.MethodPost, projectRoute(repo, "/issues"), body, &record); err != nil {
		return forgeapi.Issue{}, err
	}
	return c.normalizeIssue(&record, repo), nil
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
	var record restIssue
	path := projectRoute(repo, "/issues/"+strconv.Itoa(issue.Number))
	if err := c.sendJSON(ctx, op, http.MethodPut, path, map[string]any{keyStateEvent: "close"}, &record); err != nil {
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
	set, err := listing(op, fixedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Release]{}, err
	}
	query, walk, err := c.restQuery(op, repo, set, fixedList)
	if err != nil {
		return forgeapi.Page[forgeapi.Release]{}, err
	}
	if bounded, ok := c.unpagedList[forgeapi.Release](walk.here()); ok {
		return bounded, nil
	}
	var rows []restRelease
	header, err := c.readJSON(ctx, op, projectRoute(repo, "/releases"), query, &rows)
	if err != nil {
		if page, ok := c.deferredPage[forgeapi.Release](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.Release]{}, err
	}
	items := make([]forgeapi.Release, 0, len(rows))
	for i := range rows {
		items = append(items, normalizeRelease(&rows[i]))
	}
	next := walk.next(header)
	return forgeapi.Page[forgeapi.Release]{
		Items:   items,
		Next:    next,
		Partial: c.pagePartial(len(rows), next, walk.page),
	}, nil
}

// CreateRelease implements [forgeapi.Releases].
//
// The two flags this product has no field for are not sent and not faked: it has no
// draft release and no prerelease mark, so a caller asking for either gets a published
// release and the answer says so, which is what the operation's own per-forge block
// publishes.
func (c *Client) CreateRelease(ctx context.Context, repo forgeapi.RepoRef, req forgeapi.NewRelease) (forgeapi.Release, error) {
	const op = "CreateRelease"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.Release{}, err
	}
	body := map[string]any{
		"tag_name":     req.TagName,
		"name":         req.Name,
		keyDescription: req.Body,
	}
	if req.Target != "" {
		body["ref"] = req.Target
	}
	var record restRelease
	if err := c.sendJSON(ctx, op, http.MethodPost, projectRoute(repo, "/releases"), body, &record); err != nil {
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
	set, err := listing(op, fixedList, opts)
	if err != nil {
		return forgeapi.Page[forgeapi.Label]{}, err
	}
	query, walk, err := c.restQuery(op, repo, set, fixedList)
	if err != nil {
		return forgeapi.Page[forgeapi.Label]{}, err
	}
	if bounded, ok := c.unpagedList[forgeapi.Label](walk.here()); ok {
		return bounded, nil
	}
	var rows []restLabel
	header, err := c.readJSON(ctx, op, projectRoute(repo, "/labels"), query, &rows)
	if err != nil {
		if page, ok := c.deferredPage[forgeapi.Label](err); ok {
			return page, nil
		}
		return forgeapi.Page[forgeapi.Label]{}, err
	}
	next := walk.next(header)
	return forgeapi.Page[forgeapi.Label]{
		Items:   normalizeLabels(rows),
		Next:    next,
		Partial: c.pagePartial(len(rows), next, walk.page),
	}, nil
}
