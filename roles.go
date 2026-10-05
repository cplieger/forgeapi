package forgeapi

import "context"

// Identity reads who the connection's credential belongs to.
type Identity interface {
	// Whoami returns the authenticated account, and the granted scopes where
	// the family reports them.
	//
	// It is one request on every family, whoever calls it: a poller's first call
	// of a cycle and a connect dialog's confirmation are the same read, and
	// nothing about it is counted or rotated.
	//
	// Per forge:
	//
	//	GitHub   GET /user, whose X-OAuth-Scopes header carries the scopes, 1 request
	//	GitLab   GET /api/v4/user, 1 request
	//	Gitea    GET /api/v1/user, 1 request
	//	Forgejo  GET /api/v1/user, 1 request
	//
	// GitLab departs from the normalized contract:
	//
	//	Scopes                     cannot supply  nil: the user route carries no scope list among its 41 keys, and the token route's scopes describe one token rather than the grant
	//	error, scope insufficient  not measured   unmeasured, needs a GitLab token holding no user read: the fine-grained token measured holds User: Read and the user route answered it 200
	//
	// Gitea and Forgejo depart from the normalized contract:
	//
	//	Scopes  cannot supply  nil: the user route carries no scope list and its response no scope header
	Whoami(ctx context.Context) (Account, error)
}

// Repos lists the repositories a connection can reach.
//
// No read on this surface takes a polling unit, and that is deliberate: a cycle
// is one consumer's control flow, so naming it here would put a product's loop in
// a 1.0 a second consumer with a different loop could neither use nor remove, and
// a caller cannot be handed the unit that spends a budget only the client can see.
// The two bounds on folded-status work are counted by the client over a rolling
// window of its own ([Budget.StatusReadsPerInterval] and
// [Budget.StatusTimePerInterval]), the rotation across repositories is the
// client's own policy, and the one value a consumer holds between two clients is
// the [RotationCursor] it stores. A consumer that wants its whole cycle bounded
// brings its own context.
type Repos interface {
	// ListRepos returns repositories accessible to the authenticated account.
	//
	// Per forge:
	//
	//	GitHub   GET /user/repos, 1 request per page
	//	GitLab   GET /api/v4/projects, 1 request per page
	//	Gitea    GET /api/v1/user/repos, 1 request per page
	//	Forgejo  GET /api/v1/user/repos, 1 request per page
	//
	// GitHub departs from the normalized contract:
	//
	//	Items[].Affordances.MergeStrategies  cannot supply  empty: absent on a listing row
	//	Items[].Affordances.MergeTrain       cannot supply  no record field: a ruleset property
	//
	// GitLab departs from the normalized contract:
	//
	//	Items[].Ref                    differs in spelling  namespace path selector
	//	Items[].Affordances.HasIssues  differs in shape     varies by permission
	//	Items[].Affordances.CanPush    differs in shape     from a role integer
	//	Items[].Affordances.Ev         cannot supply        no source on the row for its three keys, anonymous
	//	Items[].Private                differs in shape     visibility string, no private key
	//
	// Gitea and Forgejo depart from the normalized contract:
	//
	//	Items[].Affordances.MergeTrain  cannot supply  SupportNo: no queue
	ListRepos(ctx context.Context, opts ...ListOption) (Page[Repository], error)
}

// PullRequests is the pull-request surface other than the merge itself.
type PullRequests interface {
	// ListPRs lists pull requests in repo, [ListStateOpen] unless
	// [WithState] says otherwise.
	//
	// Its fold is BOUNDED, which is the difference between a list and
	// [PullRequests.ReadPR]: the library takes the FIRST page of each row's
	// nested checks collection and issues no further request, because the remedy
	// is a read of that one pull request rather than another page of this list.
	// Two measured reasons, not one. A list carries N rows each with their own
	// collection, so following pages there is an extra fetch per row per call,
	// which is the fan-out the budget exists to make visible, and the bound is the
	// same rule on the list a poll actually runs, [PullRequests.ListMyPRs]. And
	// the size compounds: selecting a hundred check contexts
	// beside twenty pull requests moves one measured response from 38,737 bytes to
	// 533,301 before any further page.
	//
	// What the bound COSTS is per product, and on one of the four it costs
	// nothing. The Gitea family pays the rule as written: where a witness says
	// there is more, that row's [ActionState.Checks] is [CheckUnknown] and its
	// [PullRequest.Partial] carries [PartialPaginationCap], with no cursor. GitHub
	// pays only the check NAMES, because the connection its first page carries
	// reports per-state counts over the WHOLE collection, so the verdict and the
	// counts are exact on page 1. GitLab has no nested check collection to bound
	// at all, only one scalar head-pipeline status, so its fold cannot truncate
	// and the marker reaches its rows only for their labels
	// ([PartialPaginationCap]). Each product's own cell below states which of
	// the three it pays.
	//
	// A hundred-plus checks is rare across repositories and SYSTEMATIC within
	// one, a matrix of ten platforms by twelve versions putting a hundred and
	// twenty on every pull request, so the callers who meet this meet it on every
	// call: the marker is what tells them to read the row they care about rather
	// than believe a verdict folded over part of the evidence.
	//
	// This is the REPOSITORY VIEW's call, one repository at a time, and that is
	// what decides how the per-interval fold budget reaches its rows. The fairness
	// [WithStatusReadsPerInterval] publishes is per LIST: this list resumes where
	// it left off, and a caller sweeping many repositories through this call on one
	// connection gives the first lists the folds and the later ones none inside an
	// interval, in the order that caller chose. [PullRequests.ListMyPRs] is the
	// call whose rows span repositories, and it buys no fold at all on the family
	// whose rows would need one, so a consumer wanting check state per row across
	// repositories reads it through [PullRequests.ReadPR] or
	// [Checks.CommitStatus] on the rows it renders.
	//
	// Per forge:
	//
	//	GitHub   the PRList document, asking for at most 50 rows a page whatever the page bound asks, since a larger page of its rows answered 502 on every attempt on a large repository, 1 request per page
	//	GitLab   the PRList document, or GET /api/v4/projects/{id}/merge_requests where this connection's setup found the documents refused, 1 request per page; needs read_merge_state
	//	Gitea    GET /api/v1/repos/{owner}/{repo}/pulls, then each row's folded commit status, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor, 1 to 2 requests per page plus one per row
	//	Forgejo  GET /api/v1/repos/{owner}/{repo}/pulls, then each row's folded commit status, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor, 1 to 2 requests per page plus one per row
	//
	// GitLab departs from the normalized contract:
	//
	//	Items[].Ref                                  differs in spelling  iid, sigil "!"
	//	Items[].Labels[].Name                        differs in spelling  labels.nodes[].title, not name
	//	Items[].Labels[].Color                       differs in spelling  color string, leading #
	//	Items[].Action.Checks                        differs in shape     headPipeline.status scalar, no fold
	//	Items[].Action.ChecksPassing .. ChecksTotal  cannot supply        absent: one scalar status
	//	Items[].Action.QueueState                    cannot supply        QueueNone always: every field that would name a train carries a deprecation reason, so no document can select one
	//	Items[].Action.QueuePosition                 cannot supply        no position; train index null
	//	Successor                                    cannot supply        nil: GraphQL sends no successor
	//
	// Gitea and Forgejo depart from the normalized contract:
	//
	//	Items[].Action.AutoMergeArmed  cannot supply  no auto_merge key: unknown
	//	Items[].Action.QueueState      cannot supply  QueueNone always
	//	Items[].Action.QueuePosition   cannot supply  QueuePositionUnknown: no queue
	//	Items[].Action.MergeBlocked    cannot supply  mergeable and draft, no reason
	ListPRs(ctx context.Context, repo RepoRef, opts ...ListOption) (Page[PullRequest], error)

	// ListMyPRs lists the authenticated credential's OPEN pull requests across
	// every repository it can reach, in one call.
	//
	// Under [WithOwner] it lists every OPEN pull request in the repositories
	// under that owner instead, whoever authored it, at the same price, with the
	// same row and the same continuation; the two scopes are exclusive, and an
	// owner the instance does not resolve answers [CodeOwnerUnresolved] where the
	// product's answer can say so, which each product's cell below states.
	//
	// It names no repository, because it is keyed by the credential or by the
	// owner rather than by a selector: there is nothing
	// for [ValidateSelector] to check here and no stored identifier being
	// resolved, so neither [CodeRepoRefInvalid] nor [CodeRepoRefStale] can arise
	// from the call itself. Each row carries its own [PullRequest.Repo], which is
	// what a consumer acts on, and the response-level [Page.Successor] is never
	// SET for the same reason [Repos.ListRepos]' is not: neither call addresses a
	// repository, so neither has one that could have moved. A rename under a row
	// reaches the consumer as that row's own repository differing from the one it
	// held.
	//
	// It exists because the shape of the alternative is what spends a budget.
	// Answering "which of my pull requests are open" by listing per repository
	// costs one request per repository per cycle, so the cost is set by how many
	// repositories the user has, which this library cannot bound: measured on a
	// workspace of 62 repositories against a per-USER quota of 5,000 requests an
	// hour, one such poller consumed about 3,700 of it and starved every other
	// application drawing on the same account. This read makes a cycle
	// [Identity.Whoami] plus this call, TWO requests as its base count on every
	// product. No product adds a per-row term to it, the Gitea family included:
	// that family's rows carry neither a check state nor a head commit, and a
	// folded status is addressed BY the head, so its rows answer
	// [CheckUnknown] with a zero total and the per-forge block below says so
	// field by field. What no product's count carries is a term in the repository
	// count, which is the property this operation exists for.
	// [PullRequests.ListPRs] stays for the per-repository view a user opens
	// deliberately, and it is the call, with [PullRequests.ReadPR] and
	// [Checks.CommitStatus], that answers a check verdict.
	//
	// Its state is OPEN by construction rather than by default, so [WithState] is
	// REFUSED here with [CodeListStateInvalid] as it is on every list with no
	// state to filter on: each product's endpoint carries the open state as
	// part of the query this operation is, so a filter has nothing to select.
	// Widening that later is additive; narrowing a published filter is not, which
	// is why the refusal is the side to start on.
	//
	// It is paged and continued exactly as the other lists are, one request per
	// page up to [Budget.ListPages], and what the count moves with is how many
	// pull requests the credential has open rather than how many repositories it
	// watches. Where a product answers the call from a search with a result
	// window, a walk ends at the window's edge with no continuation, and the
	// page that ends it carries [PartialResultWindow] wherever the search's own
	// total states more than the walk was served; the per-forge block's Next
	// row states the window. A row carries no affordance object of its own, so a consumer that
	// wants a row's merge strategies reads them per repository through
	// [Capabilities.RepoAffordances], bounded by the distinct repositories the
	// credential has open pull requests in.
	//
	// Per forge:
	//
	//	GitHub   the PRMine search document, asking for at most 25 rows a page whatever the page bound asks, since a larger page of its rows answered 502 under both owners measured, the smaller of them holding fewer than a hundred open pull requests; under an owner scope the same document with user:{owner} in place of the author keyword, which resolves an organization as it resolves a user, 1 request per page
	//	GitLab   GET /api/v4/merge_requests?scope=created_by_me; under an owner scope GET /api/v4/groups/{owner}/merge_requests with the open state, which answers 404 for a user's own namespace; on either route, GET /api/v4/projects/{id} once for each distinct fork a row's source_project_id names, 1 request per page plus up to one per row
	//	Gitea    GET /api/v1/repos/issues/search filtered to pull requests and to the rows the authenticated credential created, whose rows carry no head commit and therefore no folded status; under an owner scope the same route filtered by owner={owner} in place of created=true, 1 request per page
	//	Forgejo  GET /api/v1/repos/issues/search filtered to pull requests and to the rows the authenticated credential created, whose rows carry no head commit and therefore no folded status; under an owner scope the same route filtered by owner={owner} in place of created=true, 1 request per page
	//
	// GitHub departs from the normalized contract:
	//
	//	Next                     differs in shape  a continuation while hasNextPage holds, as on a viewer's first page of 20 rows over issueCount 23; past the search connection's 1,000th result, where it answers hasNextPage false while issueCount states 2673, and a page asked for after it answers no rows and a null end cursor, so a walk ends there with no continuation and carries result_window
	//	error, owner unresolved  cannot supply     empty: an owner the search index holds no account for answers issueCount 0 and no error, so no such owner reads as nothing open
	//
	// GitLab departs from the normalized contract:
	//
	//	Items[].Ref                                  differs in spelling  iid, sigil "!"
	//	Items[].SourceRepo                           differs in shape     source_project_id beside target_project_id, a fork resolved by one GET /api/v4/projects/{id} per distinct fork on the page to its path_with_namespace, the zero value where the id is null on a deleted fork or the lookup answers 404 or 403
	//	Items[].Labels[].Color                       differs in spelling  color string, leading #, on 2 of 2, upper-case hex on 0
	//	Items[].Action.Mergeable                     cannot supply        no mergeable key on a list row, which states mergeability only through detailed_merge_status, mergeable 1, unchecked 2, and answers unchecked before the asynchronous check
	//	Items[].Action.Checks                        cannot supply        CheckUnknown: no head_pipeline key on 3 of 3 list rows
	//	Items[].Action.ChecksPassing .. ChecksTotal  cannot supply        zero: no head_pipeline key on a list row, so no status to count
	//	Items[].Action.QueueState                    cannot supply        QueueNone always: no merge-train key among the 51 keys a list row answers
	//	Items[].Action.QueuePosition                 cannot supply        QueuePositionUnknown: no merge-train key on a list row
	//	Items[].Action.MergeBlocked                  cannot supply        unknown on a list row, whose detailed_merge_status is the status the product stored when it last checked, which listing may leave unrefreshed: mergeable 1, unchecked 2 on the 3 open rows both scopes answered
	//	Items[].Partial                              cannot supply        nil: no status read runs for a list row, which carries no head_pipeline
	//	error, owner unresolved                      cannot supply        404 Group Not Found on a user's own namespace, an account the users route answers, exactly as on an owner no namespace holds
	//
	// Gitea and Forgejo depart from the normalized contract:
	//
	//	Items[].SourceBranch, Items[].SourceRepo     cannot supply     no head key on an issue-search row
	//	Items[].TargetBranch                         cannot supply     no base key on an issue-search row
	//	Items[].HeadSHA                              cannot supply     no head key on an issue-search row
	//	Items[].Action.Mergeable                     cannot supply     no mergeable key on an issue-search row
	//	Items[].Action.Checks                        cannot supply     CheckUnknown: no head commit on an issue-search row, so no status read addresses it
	//	Items[].Action.ChecksPassing .. ChecksTotal  cannot supply     zero: no fold runs on a row that carries no head commit
	//	Items[].Action.AutoMergeArmed                cannot supply     no auto-merge key on an issue-search row
	//	Items[].Action.QueueState                    cannot supply     QueueNone always
	//	Items[].Action.QueuePosition                 cannot supply     QueuePositionUnknown: no queue
	//	Items[].Action.MergeBlocked                  cannot supply     no merge-blocked key on an issue-search row
	//	Items[].Partial                              cannot supply     nil: no fold runs on a row that carries no head commit, so none can stop short
	//	Items[].Draft                                differs in shape  draft on the pull_request object, not at the row's top level
	ListMyPRs(ctx context.Context, opts ...ListOption) (Page[PullRequest], error)

	// ReadPR reads one pull request.
	//
	// It exists because the only alternative is listing: a list pays the cost
	// of the slowest merge-state field across every open pull request plus
	// pagination, which is the exact cost a read of one pull request avoids. Its
	// per-call request cost is published as a range and asserted, so a read that
	// reached for a list, issued a request to fill a field the document already
	// carries, or scaled with the number of open pull requests fails a test
	// rather than a budget.
	//
	// It is priced per CALL, because the ordinary caller is a UI action or the
	// follow-up read after a mutation rather than a poller: one to three requests
	// on GitHub, exactly one on GitLab and two to four on the Gitea family, and
	// none of it counted against the folded-status cap the client keeps per
	// rolling interval. A published price counts every request the call puts on
	// the wire, a refused one included, because a refused request spends the same
	// shared quota an answered one does. Which product's upper arm is a page of a
	// fold is not uniform: it is [Budget.StatusPages] on GitHub and on the Gitea
	// family, whose low ends are a single-page fold, and on GitLab there is no
	// upper arm at all, because the measurement found that this read returns one
	// scalar head-pipeline status there and no collection, and because whether
	// that connection's documents are accepted is settled and priced at
	// connection setup rather than by whichever call meets the answer first.
	//
	// That same degradation reaches GitHub, whose document an instance can refuse
	// too, and there it is priced INSIDE the range above rather than beside it:
	// the one call per connection that discovers the refusal spends the document
	// plus the single REST pull-request read that answers it, TWO requests, and no
	// page of a fold follows it, because REST carries no checks rollup to follow
	// and [ActionState.Checks] is therefore [CheckUnknown] for that connection
	// rather than folded over a second endpoint. Every later call on that
	// connection spends one.
	//
	// Its fold is COMPLETE, which is the difference between this read and a
	// list. The caller named one pull request and is owed a verdict, so where the
	// product serves a checks collection the library follows its pages until the
	// fold is total and [ActionState.Checks] is a real answer; the extra cost is
	// bounded by [Budget.StatusPages] and arises only on the repositories that
	// need it, a matrix of ten platforms by twelve versions being the shape that
	// does.
	// Past that page bound the row carries [PartialPaginationCap] with
	// [ActionState.Checks] at [CheckUnknown], which is the one case this read
	// answers unknown.
	//
	// Per forge:
	//
	//	GitHub   the PRRead document, then a further page of its contexts connection, or GET /repos/{owner}/{repo}/pulls/{number} where this connection has already discovered the documents refused, plus one read against the id-addressed location to resolve the successor where that record answered from another address, 1 to 3 requests
	//	GitLab   the PRRead document, or GET /api/v4/projects/{id}/merge_requests/{iid} where this connection's setup found the documents refused, 1 request; needs read_merge_state
	//	Gitea    GET /api/v1/repos/{owner}/{repo}/pulls/{number}, then the complete commit-status fold, and where the repository moved, the one 301 its first read answers, whose Location names the successor the stale refusal carries, 2 to 4 requests
	//	Forgejo  GET /api/v1/repos/{owner}/{repo}/pulls/{number}, then the complete commit-status fold, and where the repository moved, the one 301 its first read answers, whose Location names the successor the stale refusal carries, 2 to 4 requests
	//
	// GitLab departs from the normalized contract:
	//
	//	Ref                                  differs in spelling  iid, sigil "!"
	//	Labels[].Name                        differs in spelling  labels.nodes[].title, not name
	//	Labels[].Color                       differs in spelling  color string, leading #
	//	Action.Checks                        differs in shape     headPipeline.status scalar, no fold
	//	Action.ChecksPassing .. ChecksTotal  cannot supply        absent: one scalar status
	//	Action.QueueState                    cannot supply        QueueNone always: every field that would name a train carries a deprecation reason, so no document can select one
	//	Action.QueuePosition                 cannot supply        no position; train index null
	//
	// Gitea and Forgejo depart from the normalized contract:
	//
	//	Action.AutoMergeArmed  cannot supply  no auto_merge key: unknown
	//	Action.QueueState      cannot supply  QueueNone always
	//	Action.QueuePosition   cannot supply  QueuePositionUnknown: no queue
	//	Action.MergeBlocked    cannot supply  mergeable and draft, no reason
	ReadPR(ctx context.Context, repo RepoRef, pr PRRef) (PullRequest, error)

	// CreatePR opens a pull request and returns the created object.
	//
	// Per forge:
	//
	//	GitHub   POST /repos/{owner}/{repo}/pulls, then POST /repos/{owner}/{repo}/issues/{number}/labels where the caller names labels, which is the route this product declares for them because its creation takes none, plus one read against the id-addressed location to resolve the successor where either of them answered from another address, 1 to 3 requests
	//	GitLab   POST /api/v4/projects/{id}/merge_requests, 1 request
	//	Gitea    GET /api/v1/repos/{owner}/{repo}/labels where the caller names labels, whose ids this product's option takes in place of their names, then POST /api/v1/repos/{owner}/{repo}/pulls, 1 to 2 requests
	//	Forgejo  GET /api/v1/repos/{owner}/{repo}/labels where the caller names labels, whose ids this product's option takes in place of their names, then POST /api/v1/repos/{owner}/{repo}/pulls, 1 to 2 requests
	//
	// GitHub departs from the normalized contract:
	//
	//	Labels[].Name, Labels[].Color, Labels[].Description  cannot supply  no label row on a create
	//	Action.Mergeable                                     cannot supply  unknown on a mutation's own answer, whose mergeable is the asynchronous check's state as the creation lands, null on every creation measured
	//	Action.QueueState                                    cannot supply  no merge-queue key among 46
	//	Action.QueuePosition                                 cannot supply  no queue key, so unknown
	//	Action.MergeBlocked                                  cannot supply  unknown on a mutation's own answer, whose mergeable_state is the asynchronous check's state as the creation lands, unknown on every creation measured
	//
	// GitLab departs from the normalized contract:
	//
	//	Ref                                  differs in spelling  iid, sigil "!"
	//	Labels[].Color                       cannot supply        no colour on the answer: its labels arrive as name strings
	//	Labels[].Description                 cannot supply        no description on the answer: its labels arrive as name strings
	//	Action.Mergeable                     cannot supply        merge_status checking and detailed_merge_status preparing on the answer, unknown until the asynchronous check settles
	//	Action.Checks                        cannot supply        CheckUnknown: head_pipeline null on the creation answer, which the product sets after it, null here although the source branch's pipeline had already started on its head
	//	Action.ChecksPassing .. ChecksTotal  cannot supply        zero: head_pipeline null on the answer, so no status to count
	//	Action.QueueState                    cannot supply        QueueNone always: no merge-train key among the 61 keys the answer carries
	//	Action.QueuePosition                 cannot supply        QueuePositionUnknown: no merge-train key on the answer
	//	Action.MergeBlocked                  cannot supply        unknown on a mutation's own answer, whose detailed_merge_status is the asynchronous check's state as the creation lands, preparing on every creation measured
	//	State                                differs in spelling  state string, opened
	//
	// Gitea and Forgejo depart from the normalized contract:
	//
	//	Action.Mergeable       cannot supply  unknown on a mutation's own answer, whose mergeable bool is false while the instance's asynchronous check holds the pull request queued as the creation lands, true on every creation measured
	//	Action.AutoMergeArmed  cannot supply  no auto_merge key: unknown
	//	Action.QueueState      cannot supply  QueueNone always
	//	Action.QueuePosition   cannot supply  QueuePositionUnknown: no queue
	//	Action.MergeBlocked    cannot supply  mergeable and draft, no reason
	//
	// GitHub sends this, where a wrong default would change the request silently:
	//
	//	labels  body  the labels the caller named, as the NAMES the labels route takes, on the second request this row prices: this product's pull-request creation declares no labels parameter at all, so a creation that sent them there would have them ignored and one that dropped them would answer a pull request the caller did not ask for
	//
	// GitLab sends this, where a wrong default would change the request silently:
	//
	//	labels  body  the labels the caller named, as the NAMES this product's creation takes, so nothing has to resolve them first
	//
	// Gitea and Forgejo send this, where a wrong default would change the request silently:
	//
	//	labels  body  the labels the caller named, as the numeric ids this product's creation declares in place of their names, which is what the label read this row prices resolves
	CreatePR(ctx context.Context, repo RepoRef, req NewPullRequest) (PullRequest, error)

	// ClosePR closes a pull request without merging and returns what that
	// product's response carried, which the table below states per field. What a
	// consumer does with it, patch a row or refetch, is the consumer's ruling
	// against that table rather than something this library promises.
	//
	// Per forge:
	//
	//	GitHub   PATCH /repos/{owner}/{repo}/pulls/{number}, then one read against the id-addressed location to resolve the successor where it answered from another address, 1 to 2 requests
	//	GitLab   PUT /api/v4/projects/{id}/merge_requests/{iid}, 1 request
	//	Gitea    PATCH /api/v1/repos/{owner}/{repo}/pulls/{number}, 1 request
	//	Forgejo  PATCH /api/v1/repos/{owner}/{repo}/pulls/{number}, 1 request
	//
	// GitHub departs from the normalized contract:
	//
	//	Action.Mergeable      cannot supply  unknown on a mutation's own answer, whose mergeable is the asynchronous check's state as the close lands: null before the check settles, true after it
	//	Action.QueueState     cannot supply  no merge-queue key among 46
	//	Action.QueuePosition  cannot supply  no queue key, so unknown
	//	Action.MergeBlocked   cannot supply  unknown on a mutation's own answer, whose mergeable_state is the asynchronous check's state as the close lands: unknown before the check settles, clean after it
	//
	// GitLab departs from the normalized contract:
	//
	//	Ref                                  differs in spelling  iid, sigil "!"
	//	SourceRepo                           differs in shape     source_project_id beside target_project_id, a fork named by id alone: the addressed project where the two are equal, the zero value otherwise
	//	Labels[].Color                       cannot supply        no colour on the answer: its labels arrive as name strings
	//	Labels[].Description                 cannot supply        no description on the answer: its labels arrive as name strings
	//	Action.Mergeable                     cannot supply        merge_status checking and detailed_merge_status preparing on the answer before the asynchronous check settles, can_be_merged and not_open after it, unknown on both: the REST arm reads no mergeable flag
	//	Action.Checks                        differs in shape     head_pipeline.status scalar on the answer, no fold: null where the head ran no pipeline, so CheckUnknown, and the head pipeline's own status, running here, where one ran
	//	Action.ChecksPassing .. ChecksTotal  cannot supply        zero: the answer's head_pipeline is one scalar status, null or set, with nothing under it to count
	//	Action.QueueState                    cannot supply        QueueNone always: no merge-train key among the 61 keys the answer carries
	//	Action.QueuePosition                 cannot supply        QueuePositionUnknown: no merge-train key on the answer
	//	Action.MergeBlocked                  cannot supply        unknown on a mutation's own answer, whose detailed_merge_status is the asynchronous check's state as the close lands: preparing before the check settles, not_open after it, checking after a reopen
	//
	// Gitea and Forgejo depart from the normalized contract:
	//
	//	Action.Mergeable       cannot supply  unknown on a mutation's own answer, whose mergeable bool is false while the instance's asynchronous check holds the pull request queued as the close lands, true on every close measured
	//	Action.AutoMergeArmed  cannot supply  no auto_merge key: unknown
	//	Action.QueueState      cannot supply  QueueNone always
	//	Action.QueuePosition   cannot supply  QueuePositionUnknown: no queue
	//	Action.MergeBlocked    cannot supply  mergeable and draft, no reason
	ClosePR(ctx context.Context, repo RepoRef, pr PRRef) (PullRequest, error)

	// ReopenPR reopens a closed pull request and returns it.
	//
	// Per forge:
	//
	//	GitHub   PATCH /repos/{owner}/{repo}/pulls/{number}, then one read against the id-addressed location to resolve the successor where it answered from another address, 1 to 2 requests
	//	GitLab   PUT /api/v4/projects/{id}/merge_requests/{iid}, 1 request
	//	Gitea    PATCH /api/v1/repos/{owner}/{repo}/pulls/{number}, 1 request
	//	Forgejo  PATCH /api/v1/repos/{owner}/{repo}/pulls/{number}, 1 request
	//
	// GitHub departs from the normalized contract:
	//
	//	Action.Mergeable      cannot supply  unknown on a mutation's own answer, whose mergeable is the asynchronous check's state as the reopen lands, null on every reopen measured
	//	Action.QueueState     cannot supply  no merge-queue key among 46
	//	Action.QueuePosition  cannot supply  no queue key, so unknown
	//	Action.MergeBlocked   cannot supply  unknown on a mutation's own answer, whose mergeable_state is the asynchronous check's state as the reopen lands, unknown on every reopen measured
	//
	// GitLab departs from the normalized contract:
	//
	//	Ref                                  differs in spelling  iid, sigil "!"
	//	SourceRepo                           differs in shape     source_project_id beside target_project_id, a fork named by id alone: the addressed project where the two are equal, the zero value otherwise
	//	Labels[].Color                       cannot supply        no colour on the answer: its labels arrive as name strings
	//	Labels[].Description                 cannot supply        no description on the answer: its labels arrive as name strings
	//	Action.Mergeable                     cannot supply        merge_status unchecked and detailed_merge_status unchecked on the answer, unknown until the asynchronous check settles
	//	Action.Checks                        differs in shape     head_pipeline.status scalar on the answer, no fold: null where the head ran no pipeline, so CheckUnknown, and the head pipeline's own status, running here, where one ran
	//	Action.ChecksPassing .. ChecksTotal  cannot supply        zero: the answer's head_pipeline is one scalar status, null or set, with nothing under it to count
	//	Action.QueueState                    cannot supply        QueueNone always: no merge-train key among the 61 keys the answer carries
	//	Action.QueuePosition                 cannot supply        QueuePositionUnknown: no merge-train key on the answer
	//	Action.MergeBlocked                  cannot supply        unknown on a mutation's own answer, whose detailed_merge_status is the asynchronous check's state as the reopen lands, unchecked on every reopen measured, the status a reopen resets to
	//	State                                differs in spelling  state string, opened, closed_at cleared
	//
	// Gitea departs from the normalized contract:
	//
	//	Action.Mergeable       cannot supply  unknown on a mutation's own answer, whose mergeable bool is false while the instance's asynchronous check holds the pull request queued as the reopen lands, true on every reopen measured
	//	Action.AutoMergeArmed  cannot supply  no auto_merge key: unknown
	//	Action.QueueState      cannot supply  QueueNone always
	//	Action.QueuePosition   cannot supply  QueuePositionUnknown: no queue
	//	Action.MergeBlocked    cannot supply  mergeable and draft, no reason
	//
	// Forgejo departs from the normalized contract:
	//
	//	Action.Mergeable       cannot supply  unknown on a mutation's own answer, whose mergeable bool is false while the instance's asynchronous check holds the pull request queued as the reopen lands, false on one reopen measured and true on the others
	//	Action.AutoMergeArmed  cannot supply  no auto_merge key: unknown
	//	Action.QueueState      cannot supply  QueueNone always
	//	Action.QueuePosition   cannot supply  QueuePositionUnknown: no queue
	//	Action.MergeBlocked    cannot supply  mergeable and draft, no reason
	ReopenPR(ctx context.Context, repo RepoRef, pr PRRef) (PullRequest, error)

	// RerunFailedChecks re-runs the failed CI of a pull request.
	//
	// headSHA is a PRECONDITION rather than a preference, exactly as
	// [MergeRequest.HeadSHA] is: the family package refuses with
	// [CodeStaleHead] rather than re-running when the pull request has moved
	// since, because a re-run can carry deployment side effects and a row
	// displaying one commit's red status must not act on another's. Empty means
	// the forge reported no head, and the re-run then proceeds against the pull
	// request's current one, which is the one case where the pin is unavailable
	// rather than waived.
	//
	// Whether an instance can do this at all is [CapRerunChecks], read from
	// [ConnectionCaps]: a product with no re-run verb reports the capability
	// rather than failing to satisfy this role, because the two products of one
	// family differ here and a family serves both.
	//
	// Called against an instance detection says lacks the verb, it refuses with
	// [CodeCapabilityUnsupported] and issues NO request, carrying the capability
	// and the evidence the verdict was reached on. A consumer that read
	// [ConnectionCaps] first and disabled the control never sees it, which is why
	// that refusal is written for whoever wrote the call rather than for the user.
	//
	// Per forge:
	//
	//	GitHub   GET /repos/{owner}/{repo}/actions/runs to resolve the head commit's run, then POST /repos/{owner}/{repo}/actions/runs/{run}/rerun-failed-jobs, plus one read against the id-addressed location to resolve the successor where either of them answered from another address, 2 to 3 requests; needs rerun_checks
	//	GitLab   GET /api/v4/projects/{id}/pipelines, filtered by the head SHA, to resolve the head commit's pipeline, then POST /api/v4/projects/{id}/pipelines/{run}/retry, 2 requests; needs rerun_checks
	//	Gitea    GET /api/v1/repos/{owner}/{repo}/actions/runs, filtered by the head SHA, to resolve the head commit's run, then POST /api/v1/repos/{owner}/{repo}/actions/runs/{run}/rerun-failed-jobs, both of which this product's swagger document names, 2 requests; needs rerun_checks
	//	Forgejo  no route: this product carries no such verb, so the call is refused; needs rerun_checks
	//
	// Forgejo departs from the normalized contract:
	//
	//	error, verb absent  cannot supply  no re-run verb in this product's API, which its swagger document settles
	//
	// GitHub sends this, where a wrong default would change the request silently:
	//
	//	head_sha  query  the head the caller pinned, as the filter this product's runs route declares, so the retry addresses the workflow run of the commit the caller's row was rendered from rather than whichever the instance returns first
	//
	// GitLab sends this, where a wrong default would change the request silently:
	//
	//	sha  query  the head the caller pinned, as the filter this product's pipelines route declares, so the retry addresses the pipeline of the commit the caller's row was rendered from rather than whichever the instance returns first
	//
	// Gitea sends this, where a wrong default would change the request silently:
	//
	//	head_sha  query  the head the caller pinned, as the filter this product's runs route declares, so the retry addresses the run of the commit the caller's row was rendered from rather than whichever the instance returns first
	RerunFailedChecks(ctx context.Context, repo RepoRef, pr PRRef, headSHA string) error
}

// Merges is the merge surface: issuing a merge, and reading its state back. It
// is its own role because a merge is the one operation with an outcome rather
// than a result, and the read exists because that outcome can be "no verdict
// yet".
type Merges interface {
	// MergePR merges, enqueues, or accepts a background merge, and reports
	// which in the outcome. It never answers a bare error: a refusal is an
	// error beside [MergeOutcomeRefused], and everything else is a success
	// state that may carry a queue verdict.
	//
	// It does not poll. A queued merge routinely outlives the per-operation
	// deadline, so a library that polled inside the mutation would have no
	// bound left to publish; what follows an outcome without a verdict is
	// MergeStatus on the same pull request, at whatever cadence the caller
	// chooses.
	//
	// Per forge:
	//
	//	GitHub   PUT /repos/{owner}/{repo}/pulls/{number}/merge-async, this product's asynchronous merge, whose accept carries a handle and whose outcome Merges.MergeStatus reads from the pull request, then one read against the id-addressed location to resolve the successor where it answered from another address; a merge asked to wait first reads GET /repos/{owner}/{repo}/pulls/{number}, and a pull request that cannot merge now is armed by the EnableAutoMerge document in place of the merge, 1 to 3 requests
	//	GitLab   PUT /api/v4/projects/{id}/merge_requests/{iid}/merge, with the head commit required, 1 request
	//	Gitea    POST /api/v1/repos/{owner}/{repo}/pulls/{number}/merge, and for a merge asked to wait whose form the route answers 405, the same form again carrying merge_when_checks_succeed, 1 to 2 requests
	//	Forgejo  POST /api/v1/repos/{owner}/{repo}/pulls/{number}/merge, and for a merge asked to wait whose form the route answers 405, the same form again carrying merge_when_checks_succeed, 1 to 2 requests
	//
	// GitHub departs from the normalized contract:
	//
	//	QueueState  cannot supply  no queue key on the 202; MergeStatus reads it
	//
	// GitLab departs from the normalized contract:
	//
	//	State  cannot supply  Merged, Enqueued or Refused
	//	Code   cannot supply  empty: no such state
	//
	// Gitea and Forgejo depart from the normalized contract:
	//
	//	State       cannot supply  Merged, Enqueued or Refused: a merge asked to wait sends the merge form first and, on its 405, the form with merge_when_checks_succeed, whose 201 is the scheduled merge
	//	Code        cannot supply  empty: no such state
	//	QueueState  cannot supply  QueueNone always
	//
	// GitHub sends this, where a wrong default would change the request silently:
	//
	//	merge_method            body  the strategy this product's own route takes, in its own spelling, and merge where the caller named none
	//	sha                     body  the head the caller pinned, under the key this product's asynchronous merge route reads, so a merge cannot land on a head that moved under the row the caller was looking at: a pin naming another commit is refused before anything merges, and a push between the request and the merge cancels it, as the route documents
	//	merge_action            body  absent: a merge this product accepts completes in the background on its own, so an action that deferred it would answer a merge nobody asked to wait for
	//	delete_branch_on_merge  body  absent: neither this product's merge route nor its auto-merge arm takes a branch deletion, so the repository's own delete_branch_on_merge setting decides whether the head branch goes, whatever the caller asked
	//
	// GitLab sends this, where a wrong default would change the request silently:
	//
	//	sha         body  the head the caller pinned, which a group or instance setting on this product can make compulsory and which is what makes the merge refuse a branch that moved
	//	squash      body  false unless the caller asked to squash: this product exposes no strategy selector, so a squash flag is what a merge intent reaches here
	//	auto_merge  body  absent unless the caller asked for it: sent by default it arms a merge that completes later and unattended on a pull request the user asked to merge now
	//
	// Gitea sends this, where a wrong default would change the request silently:
	//
	//	do                         body  the strategy this product takes, in its own spelling, and merge where the caller named none
	//	head_commit_id             body  the head the caller pinned
	//	merge_when_checks_succeed  body  absent unless the caller asked for it and the flagless form answered 405: sent, the instance schedules the merge whatever the checks, so sent by default it would answer a merge nobody asked to wait for
	//
	// Forgejo sends this, where a wrong default would change the request silently:
	//
	//	Do                         body  the strategy this product takes, in its own spelling, and merge where the caller named none
	//	head_commit_id             body  the head the caller pinned
	//	merge_when_checks_succeed  body  absent unless the caller asked for it and the flagless form answered 405: sent, the instance schedules the merge whatever the checks, so sent by default it would answer a merge nobody asked to wait for
	MergePR(ctx context.Context, repo RepoRef, pr PRRef, req MergeRequest) (MergeOutcome, error)

	// MergeStatus reads one pull request's merge state: whether it is merged,
	// and the queue's verdict where the repository merges through one.
	//
	// It is ONE cross-forge entry point keyed by the pull request, on every
	// family, because every product can answer whether a pull request is
	// merged and what differs is only how much each can add. It is addressed
	// exactly as MergePR and [PullRequests.ReadPR] are, by repo and pr, and it
	// carries no product token: the handle one family's asynchronous merge
	// mints stays inside that family, so a caller never holds something three
	// of the four products never issue.
	//
	// It is a READ, budgeted as one and priced per CALL, so the inter-mutation
	// interval does not apply to it. It is ONE request on every product, since one
	// response carries merged and queue state everywhere. On the one product whose
	// documents an instance can refuse at runtime that figure holds on both of its
	// arms, the document and the REST record: whether this connection's documents
	// are accepted is settled at connection setup, on the row that pays for it, so
	// no call here discovers it and none spends two where this one publishes one.
	// It is the follow-up read after a merge or a UI action's, never a
	// poller's, whose lists already carry the same queue verdict on every row.
	//
	// It follows no pages, and what it hands a caller instead is a POINTER. This
	// read returns whether the pull request is merged, the queue's verdict and
	// WebURL, and no field a page of checks could fill, so the complete fold
	// [PullRequests.ReadPR] performs had nothing here to fold and the one-to-three
	// requests this operation was priced at bought nothing. One response carries
	// merged and queue state on every product. A caller that wants the check list
	// behind the verdict calls [PullRequests.ReadPR] on the same [PRRef], which is
	// the operation that pays for those pages, and WebURL is the pull request's own
	// page for a person who wants to look rather than fetch.
	//
	// Successor is the rename mark every response-level carrier has, on the
	// same terms as [CommitChecks.Successor]: this read addresses a repository
	// and its return rides no list item, so the mark travels here rather than
	// only on the failure arm.
	//
	// Per forge:
	//
	//	GitHub   the PRRead document, or GET /repos/{owner}/{repo}/pulls/{number} where this connection has already discovered the documents refused, then one read against the id-addressed location to resolve the successor where that record answered from another address, 1 to 3 requests
	//	GitLab   the PRRead document, or GET /api/v4/projects/{id}/merge_requests/{iid} where this connection's setup found the documents refused, 1 request; needs read_merge_state
	//	Gitea    GET /api/v1/repos/{owner}/{repo}/pulls/{number}, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor, 1 to 2 requests
	//	Forgejo  GET /api/v1/repos/{owner}/{repo}/pulls/{number}, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor, 1 to 2 requests
	//
	// GitLab departs from the normalized contract:
	//
	//	Merged     differs in shape  state and mergedAt, no merged field
	//	Queue      cannot supply     QueueNone always: every field that would name a train carries a deprecation reason, so no document can select one
	//	Successor  cannot supply     nil: GraphQL sends no successor
	//
	// Gitea and Forgejo depart from the normalized contract:
	//
	//	Queue  cannot supply  QueueNone always
	MergeStatus(ctx context.Context, repo RepoRef, pr PRRef) (MergeStatus, error)
}

// Checks reads the CI verdict for a commit.
type Checks interface {
	// CommitStatus returns the folded verdict for a commit ref, a branch or a
	// SHA, with the contexts it was folded from.
	//
	// ref is refused with [CodeRefInvalid] before any request where
	// [ValidateRef] refuses it, because the value reaches an API path.
	//
	// On the family whose folded status costs one request per pull request this
	// is the read [Budget.StatusReadsPerInterval] counts over the client's own
	// rolling window, and the one the [RotationCursor] rotates.
	//
	// Its fold is COMPLETE: this addresses one commit, so the library follows
	// the status endpoint's pages until the fold is total, bounded by
	// [Budget.StatusPages], and marks [CommitChecks.Partial] with
	// [PartialPaginationCap] and [CommitChecks.State] at [CheckUnknown] past
	// that bound. The endpoint's own folded state is not usable for this:
	// it is computed over the returned page, so a page of successes reports
	// success for a failing commit.
	//
	// Per forge:
	//
	//	GitHub   the CommitRollup document, the one source on this product that tells a commit with no CI from a commit whose checks all passed, or GET /repos/{owner}/{repo}/commits/{ref}/status beside GET /repos/{owner}/{repo}/commits/{ref}/check-runs where this connection has already discovered the documents refused, plus one read against the id-addressed location to resolve the successor where those reads answered from another address, which both of them meet and one read answers, 1 to 5 requests
	//	GitLab   GET /api/v4/projects/{id}/repository/commits/{sha}/statuses, 1 to 3 requests
	//	Gitea    GET /api/v1/repos/{owner}/{repo}/commits/{ref}/status, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor, 1 to 4 requests
	//	Forgejo  GET /api/v1/repos/{owner}/{repo}/commits/{ref}/status, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor, 1 to 4 requests
	//
	// GitLab departs from the normalized contract:
	//
	//	Successor  cannot supply  nil: no successor key
	CommitStatus(ctx context.Context, repo RepoRef, ref string) (CommitChecks, error)

	// ListRuns lists repo's CI runs, in the order the product answers them:
	// no sort parameter is sent, because one family's route declares none.
	//
	// It reads the route [PullRequests.RerunFailedChecks] resolves its run on,
	// with the page bound as its page size and none of the head filter that
	// read sends, so the answer is every run of the repository rather than one
	// commit's. A consumer wanting the failed ones filters [Run.State] on
	// [CheckFailing].
	//
	// [WithState] and [WithOwner] are refused on it, with [CodeListStateInvalid]
	// and [CodeListOwnerInvalid], before any request: a run has no open or
	// closed state to select on, and one repository has no owner to scope by.
	//
	// Per forge:
	//
	//	GitHub   GET /repos/{owner}/{repo}/actions/runs, the route the re-run resolves its run on, with the page bound as its page size and no head filter, then one read against the id-addressed location to resolve the successor where it answered from another address, 1 to 3 requests per page
	//	GitLab   GET /api/v4/projects/{id}/pipelines, the route the re-run resolves its pipeline on, with the page bound as its page size and no head filter, 1 request per page
	//	Gitea    GET /api/v1/repos/{owner}/{repo}/actions/runs, the route the re-run resolves its run on, with page from 1, the smaller of the page bound and the instance's stated maximum as its page size and no head filter, continued from the body's total_count, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor, 1 to 2 requests per page
	//	Forgejo  GET /api/v1/repos/{owner}/{repo}/actions/runs, with page from 1, the smaller of the page bound and the instance's stated maximum as its page size and no head filter, continued from the body's total_count, since the route sends no paging header and an instance whose maximum was lowered after the connection read it still serves a page of its own maximum, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor, 1 to 2 requests per page
	//
	// GitLab departs from the normalized contract:
	//
	//	Items[].Branch  differs in spelling  ref string: a branch on 2 of 5 and a refs/ path on 3, which merge-request and workload pipelines run for
	//	Successor       cannot supply        nil: no successor key
	//
	// Gitea departs from the normalized contract:
	//
	//	Items[].Name  differs in spelling  path string, the workflow file and its ref, on 6 of 6; no name key among the 26 the route answers
	//
	// Forgejo departs from the normalized contract:
	//
	//	Items[].Name    differs in spelling  workflow_id string, the workflow file's name, on 16 of 16 rows kept; no name key among the 23 the route answers
	//	Items[].Branch  differs in spelling  prettyref string, pull_request run "#820"; pull_request run "#841"; pull_request run "#842"; push run "main": the branch on a push run and a pull request's number on a pull-request run, no branch key
	//
	// GitHub sends this, where a wrong default would change the request silently:
	//
	//	head_sha  query  absent: the listing answers every run of the repository, and the head filter the re-run sends on the same route would narrow it to one commit's runs
	//
	// GitLab sends this, where a wrong default would change the request silently:
	//
	//	sha  query  absent: the listing answers every run of the repository, and the head filter the re-run sends on the same route would narrow it to one commit's runs
	//
	// Gitea and Forgejo send this, where a wrong default would change the request silently:
	//
	//	head_sha  query  absent: the listing answers every run of the repository, and the head filter the re-run sends on the same route would narrow it to one commit's runs
	//	page      query  the first page of a walk, sent with every limit: a Forgejo instance answers a request with no page with every run of the repository
	//	limit     query  the smaller of the page bound and the instance's stated maximum, here the maximum both public instances state, the same on every page of one walk, which is what lets the stated total decide the continuation where an instance serves fewer rows than asked
	ListRuns(ctx context.Context, repo RepoRef, opts ...ListOption) (Page[Run], error)
}

// Issues is the issue surface.
type Issues interface {
	// ListIssues lists issues in repo.
	//
	// Per forge:
	//
	//	GitHub   GET /repos/{owner}/{repo}/issues, whose population is issues and pull requests both, so a row carrying a pull-request key is dropped, then one read against the id-addressed location to resolve the successor where it answered from another address, 1 to 3 requests per page
	//	GitLab   GET /api/v4/projects/{id}/issues, 1 request per page
	//	Gitea    GET /api/v1/repos/{owner}/{repo}/issues, filtered to issues, because this route's population is issues and pull requests both, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor, 1 to 2 requests per page
	//	Forgejo  GET /api/v1/repos/{owner}/{repo}/issues, filtered to issues, because this route's population is issues and pull requests both, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor, 1 to 2 requests per page
	//
	// GitLab departs from the normalized contract:
	//
	//	Items[].Ref             differs in spelling  iid int
	//	Items[].Labels[].Color  differs in spelling  color string, leading #
	//	Items[].State           differs in spelling  state string, opened
	//	Successor               cannot supply        nil: no successor key
	ListIssues(ctx context.Context, repo RepoRef, opts ...ListOption) (Page[Issue], error)

	// ListMyIssues lists OPEN issues across every repository the credential can
	// reach, in one call, scoped exactly as [PullRequests.ListMyPRs] is: what
	// the credential authored by default, and every open issue under one owner
	// under [WithOwner].
	//
	// It names no repository, for the reason [PullRequests.ListMyPRs] does, so
	// neither [CodeRepoRefInvalid] nor [CodeRepoRefStale] can arise from the call
	// itself, each row carries its own [Issue.Repo], and [Page.Successor] is never
	// set. It costs one request per page under either scope: the owner is never
	// resolved by a read of its own and no row adds a term. A search's result
	// window ends a walk as it does there, which the Next row below states.
	//
	// Its state is OPEN by construction, so [WithState] is refused here with
	// [CodeListStateInvalid] as it is on [PullRequests.ListMyPRs].
	//
	// Per forge:
	//
	//	GitHub   the IssueMine search document, whose query names type:issue and the author keyword, or user:{owner} in its place under an owner scope, 1 request per page
	//	GitLab   GET /api/v4/issues?scope=created_by_me, or GET /api/v4/groups/{owner}/issues under an owner scope, both with the open state and the label detail parameter, 1 request per page
	//	Gitea    GET /api/v1/repos/issues/search filtered to issues and to the rows the authenticated credential created, or by owner={owner} in place of created=true under an owner scope, 1 request per page
	//	Forgejo  GET /api/v1/repos/issues/search filtered to issues and to the rows the authenticated credential created, or by owner={owner} in place of created=true under an owner scope, 1 request per page
	//
	// GitHub departs from the normalized contract:
	//
	//	Next                     differs in shape  a continuation while hasNextPage holds, as on a viewer's first page of 20 rows over issueCount 188; past the search connection's 1,000th result, where it answers hasNextPage false while issueCount states 4617, and a page asked for after it answers no rows and a null end cursor, so a walk ends there with no continuation and carries result_window
	//	error, owner unresolved  cannot supply     empty: an owner the search index holds no account for answers issueCount 0 and no error, so no such owner reads as nothing open
	//
	// GitLab departs from the normalized contract:
	//
	//	Items[].Ref              differs in spelling  iid int, on the group route's 2 of 2 rows
	//	Items[].Labels[].Color   differs in spelling  color string, leading #, on 16 of 16, upper-case hex on 1
	//	Items[].State            differs in spelling  state string, opened on 2 of 2: the state parameter fixes it
	//	error, owner unresolved  cannot supply        404 Group Not Found on a user's own namespace, an account the users route answers, exactly as on an owner no namespace holds
	ListMyIssues(ctx context.Context, opts ...ListOption) (Page[Issue], error)

	// CreateIssue files an issue and returns the created object.
	//
	// Per forge:
	//
	//	GitHub   POST /repos/{owner}/{repo}/issues, whose own body carries the labels the caller named, then one read against the id-addressed location to resolve the successor where it answered from another address, 1 to 2 requests
	//	GitLab   POST /api/v4/projects/{id}/issues, 1 request
	//	Gitea    GET /api/v1/repos/{owner}/{repo}/labels where the caller names labels, whose ids this product's option takes in place of their names, then POST /api/v1/repos/{owner}/{repo}/issues, 1 to 2 requests
	//	Forgejo  GET /api/v1/repos/{owner}/{repo}/labels where the caller names labels, whose ids this product's option takes in place of their names, then POST /api/v1/repos/{owner}/{repo}/issues, 1 to 2 requests
	//
	// GitLab departs from the normalized contract:
	//
	//	Ref                   differs in spelling  iid int, the project's own sequence
	//	Labels[].Color        cannot supply        no colour on the answer: its labels arrive as name strings
	//	Labels[].Description  cannot supply        no description on the answer: its labels arrive as name strings
	//	State                 differs in spelling  state string, opened
	//
	// GitHub sends this, where a wrong default would change the request silently:
	//
	//	labels  body  the labels the caller named, as the NAMES this product's issue creation takes, which is the difference from its pull-request creation
	//
	// GitLab sends this, where a wrong default would change the request silently:
	//
	//	labels  body  the labels the caller named, as the NAMES this product's creation takes, so nothing has to resolve them first
	//
	// Gitea and Forgejo send this, where a wrong default would change the request silently:
	//
	//	labels  body  the labels the caller named, as the numeric ids this product's creation declares in place of their names, which is what the label read this row prices resolves
	CreateIssue(ctx context.Context, repo RepoRef, req NewIssue) (Issue, error)

	// CloseIssue closes an issue and returns it as the forge now reports it.
	//
	// Per forge:
	//
	//	GitHub   PATCH /repos/{owner}/{repo}/issues/{number}, then one read against the id-addressed location to resolve the successor where it answered from another address, 1 to 2 requests
	//	GitLab   PUT /api/v4/projects/{id}/issues/{iid}, 1 request
	//	Gitea    PATCH /api/v1/repos/{owner}/{repo}/issues/{number}, 1 request
	//	Forgejo  PATCH /api/v1/repos/{owner}/{repo}/issues/{number}, 1 request
	//
	// GitLab departs from the normalized contract:
	//
	//	Ref                   differs in spelling  iid int, the project's own sequence
	//	Labels[].Color        cannot supply        no colour on the answer: its labels arrive as name strings
	//	Labels[].Description  cannot supply        no description on the answer: its labels arrive as name strings
	CloseIssue(ctx context.Context, repo RepoRef, issue IssueRef) (Issue, error)
}

// Capabilities reads the three response-level capability scopes, so a consumer
// can gate a control on what this instance, this credential and this repository
// actually allow.
//
// All three are accessors over state the client detected, but resolving one can
// cost a request: the evidence order includes a bounded probe and, for a
// capability nothing cheaper answered, a swagger fetch. They take a context for
// that reason.
type Capabilities interface {
	// ConnectionCaps reports what this instance can do, whoever is asking.
	//
	// Per forge:
	//
	//	GitHub   the X-GitHub-Request-Id header that names the product and the x-github-enterprise-version header that names an appliance's version, both riding traffic the connection already sent, then GET /meta where no header answered, beside GET /versions, which settles whether this instance serves the version this family pins, all cached with the connection, 0 to 2 requests
	//	GitLab   the X-Gitlab-Meta header riding traffic the connection already sent, then GET /api/v4/metadata, beside the one bounded schema question that settles whether this instance accepts the documents this family ships, both cached with the connection, 0 to 2 requests
	//	Gitea    neither X-Gitea-Version nor X-Forgejo-Version, which neither product sends, so GET /api/v1/version, whose body separates the two, then GET /swagger.v1.json where no cheaper source answered, both cached with the connection, and GET /api/v1/settings/api, whose max_response_items every page-numbered list asks for at most, read once beside the version, 0 to 3 requests
	//	Forgejo  neither X-Gitea-Version nor X-Forgejo-Version, which neither product sends, so GET /api/v1/version, whose body separates the two, then GET /swagger.v1.json where no cheaper source answered, both cached with the connection, and GET /api/v1/settings/api, whose max_response_items every page-numbered list asks for at most, read once beside the version, 0 to 3 requests
	//
	// GitHub departs from the normalized contract:
	//
	//	Caps[rerun_checks]  cannot supply  no wire field: per repository
	//
	// Forgejo departs from the normalized contract:
	//
	//	Caps[rerun_checks]  cannot supply  swagger has no rerun verb: no
	ConnectionCaps(ctx context.Context) (ConnectionCaps, error)

	// GrantCaps reports what this credential can do on it.
	//
	// Per forge:
	//
	//	GitHub   GET /user, whose X-OAuth-Scopes header carries the scope pair, 0 to 1 request
	//	GitLab   the PRRead document's user permissions, 0 to 1 request
	//	Gitea    GET /api/v1/repos/{owner}/{repo}/pulls/{number}, whose body carries the mergeable flag, 0 to 1 request
	//	Forgejo  GET /api/v1/repos/{owner}/{repo}/pulls/{number}, whose body carries the mergeable flag, 0 to 1 request
	//
	// GitLab departs from the normalized contract:
	//
	//	Caps[read_merge_state], after a scope refusal  cannot supply  no token-permission source a request reads: the token route refused the fine-grained token for lacking [Personal Access Token: Read] at the user boundary, so once a refusal names what the credential lacks, the merge-state grant answers unknown with that refusal as its evidence
	//
	// Gitea and Forgejo depart from the normalized contract:
	//
	//	Ev  cannot supply  no evidence object; body mergeable
	GrantCaps(ctx context.Context) (GrantCaps, error)

	// RepoAffordances reports what one repository allows.
	//
	// It is NOT the way to fill a list: every row a repository listing returns
	// already carries its own [Repository.Affordances], so calling this per row
	// is the per-item fan-out inside a list that this library's budget exists
	// to make visible. Call it for one repository a consumer holds an
	// identifier for.
	//
	// Per forge:
	//
	//	GitHub   GET /repos/{owner}/{repo}, then one read against the id-addressed location to resolve the successor where it answered from another address, 1 to 3 requests
	//	GitLab   GET /api/v4/projects/{id}, 1 request
	//	Gitea    GET /api/v1/repos/{owner}/{repo}, and where the repository moved, the one 301 its first read answers, whose Location names the successor the stale refusal carries, 1 to 2 requests
	//	Forgejo  GET /api/v1/repos/{owner}/{repo}, and where the repository moved, the one 301 its first read answers, whose Location names the successor the stale refusal carries, 1 to 2 requests
	//
	// GitHub departs from the normalized contract:
	//
	//	MergeTrain  cannot supply  not on the record: a rulesets read
	//
	// GitLab departs from the normalized contract:
	//
	//	HasIssues                  differs in shape  varies by permission
	//	CanPush                    differs in shape  from a role integer
	//	Ev                         cannot supply     no source on the row for its three keys, anonymous
	//	error, scope insufficient  not measured      no refusal on a public project outside the token's group, which a fine-grained token reads without a permission and which answered 200 with its permission object; a private project outside it is unmeasured, needs a private project outside the token's group that the account can read
	//
	// Gitea and Forgejo depart from the normalized contract:
	//
	//	MergeTrain  cannot supply  SupportNo: no queue
	RepoAffordances(ctx context.Context, repo RepoRef) (RepoAffordances, error)
}

// Governor reads the per-connection budget the library paces itself against, and
// the rotation cursor a consumer persists on its behalf.
//
// It is a role of its own, in [Core], rather than a method on each family client
// alone, for the reason the three capability accessors are: the one cross-forge
// entry point, the families subpackage's Open, hands out a Core, and a consumer
// on that path has to render the budget beside the capabilities, and store the
// cursor, without asserting a concrete client. One method, because the governor
// exposes one value and is never written from outside.
type Governor interface {
	// BudgetState reports the remaining budget, when it renews, what the last
	// call cost, and where the rotation stands. It performs no I/O and is safe
	// to call concurrently with operations; see [BudgetState] for how the
	// governor uses it and for the neutral values on Gitea, the one product that
	// sends no signal.
	//
	// The budget it reports belongs to the USER and not to this connection: on
	// GitHub and GitLab it is the account's own quota, drawn on by every
	// application acting for that user, so the number moves while this client
	// sends nothing. That is stated here as well as on [BudgetState] and
	// [BudgetState.Remaining] because a reader who stops at this accessor is the
	// one the misreading costs: a user-wide number read as this connection's own
	// is a budget the caller believes it alone is spending.
	//
	// It is also the one place the [RotationCursor] a consumer persists is read
	// back, which is why a client that rotates nothing still answers here: the
	// value is empty there, and a consumer stores what it is given without a
	// per-family branch.
	//
	// Per forge:
	//
	//	GitHub   the signal riding every response the other operations already received, no request
	//	GitLab   the signal riding every response the other operations already received, no request
	//	Gitea    nothing: this product sends no signal, so the neutral value is the answer, no request
	//	Forgejo  the signal riding every response the other operations already received, no request
	//
	// GitHub departs from the normalized contract:
	//
	//	Remaining       differs in shape  two pools, one per surface
	//	RotationCursor  cannot supply     empty: every response carries its budget
	//
	// GitLab departs from the normalized contract:
	//
	//	LastCost        differs in shape  queryComplexity.score; REST 1 per request
	//	RotationCursor  cannot supply     empty: nothing rotates
	//
	// Gitea departs from the normalized contract:
	//
	//	Remaining  cannot supply  BudgetRemainingUnknown: no signal
	//	Reset      cannot supply  zero time: no signal
	//	LastCost   cannot supply  own per-call price
	//
	// Forgejo departs from the normalized contract:
	//
	//	LastCost  cannot supply  own per-call price
	BudgetState() BudgetState
}

// Releases is the release surface. It is OPTIONAL: a family whose product has no
// release object does not implement it, and the absence is a type fact a caller
// can test for rather than a stub returning an error.
type Releases interface {
	// ListReleases returns releases of repo.
	//
	// Per forge:
	//
	//	GitHub   GET /repos/{owner}/{repo}/releases, then one read against the id-addressed location to resolve the successor where it answered from another address, 1 to 3 requests per page
	//	GitLab   GET /api/v4/projects/{id}/releases, 1 request per page
	//	Gitea    GET /api/v1/repos/{owner}/{repo}/releases, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor, 1 to 2 requests per page
	//	Forgejo  GET /api/v1/repos/{owner}/{repo}/releases, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor, 1 to 2 requests per page
	//
	// GitLab departs from the normalized contract:
	//
	//	Items[].Body        differs in spelling  description, no body key
	//	Items[].Draft       cannot supply        no draft key on the row
	//	Items[].Prerelease  cannot supply        no prerelease key; upcoming_release false here
	//	Successor           cannot supply        nil: no successor key
	ListReleases(ctx context.Context, repo RepoRef, opts ...ListOption) (Page[Release], error)

	// CreateRelease cuts a release and returns the created object.
	//
	// Per forge:
	//
	//	GitHub   POST /repos/{owner}/{repo}/releases, then one read against the id-addressed location to resolve the successor where it answered from another address, 1 to 2 requests
	//	GitLab   POST /api/v4/projects/{id}/releases, 1 request
	//	Gitea    POST /api/v1/repos/{owner}/{repo}/releases, 1 request
	//	Forgejo  POST /api/v1/repos/{owner}/{repo}/releases, 1 request
	//
	// GitLab departs from the normalized contract:
	//
	//	Body        differs in spelling  description, no body key
	//	Draft       cannot supply        no draft key on the answer
	//	Prerelease  cannot supply        no prerelease key; upcoming_release false here, though one was asked for
	CreateRelease(ctx context.Context, repo RepoRef, req NewRelease) (Release, error)
}

// Labels reads the labels a repository defines. It is OPTIONAL for the same
// reason [Releases] is: a family whose product has no pull-request labels does
// not implement it.
type Labels interface {
	// ListLabels returns labels defined on repo.
	//
	// Per forge:
	//
	//	GitHub   GET /repos/{owner}/{repo}/labels, then one read against the id-addressed location to resolve the successor where it answered from another address, 1 to 3 requests per page
	//	GitLab   GET /api/v4/projects/{id}/labels, 1 request per page
	//	Gitea    GET /api/v1/repos/{owner}/{repo}/labels, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor, 1 to 2 requests per page
	//	Forgejo  GET /api/v1/repos/{owner}/{repo}/labels, and where the repository moved, the one 301 its old path answers, whose Location names the successor, every later request of the call addressing that successor, 1 to 2 requests per page
	//
	// GitLab departs from the normalized contract:
	//
	//	Items[].Color  differs in spelling  color string, leading #, on 23 of 23, upper-case hex on 3
	//	Successor      cannot supply        nil: no successor key
	ListLabels(ctx context.Context, repo RepoRef, opts ...ListOption) (Page[Label], error)
}

// Core is the union of the roles EVERY family satisfies, and the union is
// therefore the INTERSECTION of what they can do.
//
// A union of everything would re-import the god-interface failure and could not
// be satisfied by a family whose product lacks releases or pull-request labels,
// so those two roles stay out of Core and are asserted separately. A consumer
// declares the narrowest role it actually uses; Core exists for the one place
// that has to hand out a single value a route might ask anything of.
//
// Adding a method to any of these interfaces is a breaking change for every
// implementor, so what is additive is a NEW role, a new family, a new struct
// field and a new enum member, never a method here.
type Core interface {
	Identity
	Repos
	PullRequests
	Merges
	Checks
	Issues
	Capabilities
	Governor

	// Close releases the client's pool, the idle connections of the transport the
	// library built for its connection: a caller that disconnects or re-addresses a
	// connection calls it to release them at once, and otherwise they close after
	// that transport's idle timeout. It adds no closed state, so a call after it
	// dials afresh.
	Close()
}
