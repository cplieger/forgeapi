package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/githublimit"
	"github.com/cplieger/forgeapi/v3/internal/transport"
)

// documentPrefix opens the continuation a LIST DOCUMENT mints, which is a different
// encoding from the page-numbered one the REST lists use: the document's own cursor
// is an opaque upstream string whose bytes are outside the class
// [forgeapi.ValidateCursor] admits, so it crosses the surface base64url encoded.
const documentPrefix = "g"

// searchPrefix opens the continuation the two cross-repository SEARCHES mint. It is
// apart from [documentPrefix] because it carries a fact more than a position: the
// rows the walk has been served, which a page ending short of the search's stated
// total reports in [forgeapi.PartialResultWindow]'s count. So a repository list
// handed one would resume from a position in a different connection, and a search
// handed a repository list's would count a walk it never saw.
const searchPrefix = "s"

// maxSearchServed bounds the served count a search continuation is decoded with. A
// walk this library mints stays far below it, since the search serves a window of
// results, and the bound keeps a forged count from overflowing the walk's sum.
const maxSearchServed = 1 << 30

// foldPageSize is the contexts page a COMPLETE fold asks for, which is this
// product's own maximum for that connection: the read that returns the check names
// wants them, and a smaller page spends more requests for the same list.
const foldPageSize = 100

// listFoldPageSize is the contexts page a BOUNDED fold asks for, and it is ONE
// rather than the maximum, and that is a measurement rather than a preference: the
// connection's per-state counts describe the whole collection, so a list's verdict
// and its counts are exact from any page at all, and selecting one context beside
// them answered every row of a twenty-row list at 38,737 bytes against 533,301 for a
// hundred, at the same cost of one request. What the bound truncates is the check
// NAMES, and the row's own partial marker says so.
const listFoldPageSize = 1

// rateLimitSelection is the budget object every query document selects, which is how
// this product reports what a document cost. What the credential has left and when
// its window renews arrive in the response's rate-limit headers, as on REST.
const rateLimitSelection = `
  rateLimit { cost limit nodeCount remaining resetAt used }`

// rollupSelection is the status rollup both the pull-request documents and the commit
// read select, parameterized by the contexts page they take.
//
// The per-state COUNT arrays beside the nodes are what make a bounded fold exact on
// this product, and the union's two halves are both selected because the neutral and
// skipped verdicts live in a check run's CONCLUSION and are unreachable through the
// rollup's own state.
func rollupSelection(pageSize int, after string) string {
	page := "first: " + strconv.Itoa(pageSize)
	if after != "" {
		page += ", after: " + after
	}
	return `
            statusCheckRollup {
              state
              contexts(` + page + `) {
                totalCount
                checkRunCount
                statusContextCount
                checkRunCountsByState { state count }
                statusContextCountsByState { state count }
                pageInfo { hasNextPage endCursor }
                nodes {
                  __typename
                  ... on StatusContext { context description targetUrl state }
                  ... on CheckRun { name conclusion status detailsUrl }
                }
              }
            }`
}

// pullRequestFields is the node selection every pull-request document shares. Its
// load-bearing choices: `headRefOid` is the head a merge and a re-run pin to, not the
// rollup commit's; `mergeable` and `mergeStateStatus` answer different questions,
// measured MERGEABLE beside BLOCKED; `autoMergeRequest`'s presence is the armed
// answer; `labels(first: 100)` is this product's per-row label limit, so the page is
// the whole set; `latestOpinionatedReviews` is already the latest opinionated review
// per reviewer.
func pullRequestFields(foldPage int, foldAfter string) string {
	return `
        number
        title
        body
        url
        createdAt
        updatedAt
        isDraft
        state
        author { __typename login }
        headRefName
        headRepository { nameWithOwner }
        baseRefName
        headRefOid
        mergeable @include(if: $mergeState)
        mergeStateStatus @include(if: $mergeState)
        merged
        autoMergeRequest { enabledAt mergeMethod }
        mergeQueueEntry { state position }
        labels(first: 100) { totalCount nodes { name color description } }
        totalCommentsCount
        latestOpinionatedReviews(first: 20) @include(if: $reviews) { totalCount nodes { state } }
        commits(last: 1) {
          nodes {
            commit {
              oid` + rollupSelection(foldPage, foldAfter) + `
            }
          }
        }`
}

// document is one named GraphQL document this family ships: its operation name, its
// text, and whether it is a mutation, which the transport governs as a write. A
// document is not an operation, and the operations that reach each are named in its
// own comment below.
//
// absence names a nullable root field whose NOT_FOUND error is the schema's answer for
// no such node, so an envelope carrying only that error is an answer, not a refusal.
type document struct {
	name     string
	text     string
	absence  string
	mutation bool
}

// prList is the pull-request LIST document, whose caller is [Client.ListPRs]. Its
// fold is BOUNDED, which is what the one-context page says, and its state filter,
// merge state gate and page size are variables because the published list options
// reach them.
var prList = document{
	name: "PRList",
	text: `query PRList($owner: String!, $name: String!, $first: Int!, $after: String, $states: [PullRequestState!], $mergeState: Boolean!, $reviews: Boolean!) {` + rateLimitSelection + `
  repository(owner: $owner, name: $name) {
    nameWithOwner
    viewerPermission
    pullRequests(first: $first, after: $after, states: $states, orderBy: {field: UPDATED_AT, direction: DESC}) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes {` + pullRequestFields(listFoldPageSize, "") + `
      }
    }
  }
}
`,
}

// prListPageCeiling is the most rows [prList] asks for on one page, whatever the page
// bound asks. Its rows carry the check rollup and the merge state, and a larger page
// outruns the upstream's evaluation time: measured 2026-11-16 on two repositories
// holding 1,355 and 3,128 open pull requests, a page of 50 answered 502 on 8 of 15
// attempts after 10.8 to 12 s, 40 answered in up to 10.9 s, and 30 answered all six
// attempts in at most 9.1 s. A gateway answer is retried and still charged, so a
// larger page spends the budget and serves nothing; a page this ceiling shortened
// carries its continuation like any other.
const prListPageCeiling = 30

// prRead is the single-pull-request document, whose callers are [Client.ReadPR] and
// [Client.MergeStatus]. Its field set is the list's minus the list machinery plus the
// contexts CURSOR, because the fold on a single read is complete: the cursor is handed
// back until the connection reports no next page, bounded by
// [forgeapi.Budget.StatusPages].
var prRead = document{
	name: "PRRead",
	text: `query PRRead($owner: String!, $name: String!, $number: Int!, $checksAfter: String, $mergeState: Boolean = true, $reviews: Boolean = true) {` + rateLimitSelection + `
  repository(owner: $owner, name: $name) {
    nameWithOwner
    viewerPermission
    pullRequest(number: $number) {` + pullRequestFields(foldPageSize, "$checksAfter") + `
    }
  }
}
`,
}

// prMine is the cross-repository document, whose caller is [Client.ListMyPRs]. It is
// a SEARCH rather than a variant of either document above: neither the list nor the
// single selection reaches rows from repositories the call never names, and its rows
// carry their own repository because that addressing is what a consumer acts on.
//
// Its fold is bounded for the same reason the list's is, and its cost moves with the
// page size rather than with how many repositories the credential can reach, which is
// the whole property this operation exists for.
var prMine = document{
	name: "PRMine",
	text: `query PRMine($q: String!, $first: Int!, $after: String, $mergeState: Boolean!, $reviews: Boolean!) {` + rateLimitSelection + `
  search(query: $q, type: ISSUE, first: $first, after: $after) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes {
      __typename
      ... on PullRequest {` + pullRequestFields(listFoldPageSize, "") + `
        repository { nameWithOwner }
      }
    }
  }
}
`,
}

// prSearchPageCeiling is the most rows [prMine] asks for on one page, whatever the
// page bound asks, under either scope. Both the page and the owner's size move its
// evaluation time: measured 2026-11-16 under an owner holding 57,746 open pull
// requests, a page of 25 answered 502 on 2 of 5 attempts, 20 answered in up to 10.2
// s and 15 answered all three attempts in at most 8.1 s, while 25 answered in 6 s
// under an owner holding few. A gateway answer is retried and still charged, so a
// larger page spends the budget and serves nothing; a page this ceiling shortened
// carries its continuation like any other.
const prSearchPageCeiling = 15

// issueMine is the cross-repository ISSUE document, whose caller is
// [Client.ListMyIssues]. It is the same search connection [prMine] reads, selecting
// issue rows instead, because an issue node carries none of the pull-request
// selection: no head, no merge state and no rollup. Its rows carry their own
// repository for the reason that document's do, and its labels page is the
// product's per-row limit for the reason [pullRequestFields] states. Its cost moves
// with the page alone, measured at one point for a page of twenty under either scope
// and for a page of a hundred.
var issueMine = document{
	name: "IssueMine",
	text: `query IssueMine($q: String!, $first: Int!, $after: String) {` + rateLimitSelection + `
  search(query: $q, type: ISSUE, first: $first, after: $after) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes {
      __typename
      ... on Issue {
        number
        title
        body
        url
        createdAt
        updatedAt
        state
        author { __typename login }
        labels(first: 100) { totalCount nodes { name color description } }
        comments { totalCount }
        repository { nameWithOwner }
      }
    }
  }
}
`,
}

// commitRollup is the commit's own rollup document, whose caller is
// [Client.CommitStatus]. It is a document rather than the REST pair because it is the
// only source on this product that tells an empty fold from a passing one: measured on
// a commit with no CI at all, the REST combined status answers a state of pending with
// an empty statuses array, byte-identical to what an Actions-only repository with
// twenty-one green checks answers, while the rollup here is NULL.
//
// It resolves the commit by EXPRESSION rather than by object id, because the published
// operation takes a REF: a branch name is what a caller reading a repository view has,
// and the id-typed variable refuses one outright, which the live lane caught as a
// document refused for its variable's type and a connection degraded to REST for the
// rest of its life. The expression form takes a SHA too, so nothing is lost.
var commitRollup = document{
	name: "CommitRollup",
	text: `query CommitRollup($owner: String!, $name: String!, $ref: String!, $checksAfter: String) {` + rateLimitSelection + `
  repository(owner: $owner, name: $name) {
    nameWithOwner
    object(expression: $ref) {
      __typename
      ... on Commit {
        oid` + rollupSelection(foldPageSize, "$checksAfter") + `
      }
    }
  }
}
`,
}

// enableAutoMerge is the arm document, whose caller is [Client.MergePR] on a merge
// asked to wait that cannot complete now. It is a MUTATION because this product arms an
// auto-merge through no REST route, and it selects no budget object because the
// Mutation root carries none (rateLimit is a Query field), so its cost reaches the
// governor through the response's budget headers alone.
var enableAutoMerge = document{
	name: "EnableAutoMerge",
	text: `mutation EnableAutoMerge($pullRequestId: ID!, $mergeMethod: PullRequestMergeMethod!, $expectedHeadOid: GitObjectID!) {
  enablePullRequestAutoMerge(input: {pullRequestId: $pullRequestId, mergeMethod: $mergeMethod, expectedHeadOid: $expectedHeadOid}) {
    pullRequest { number mergeStateStatus autoMergeRequest { enabledAt mergeMethod } }
  }
}
`,
	mutation: true,
}

// disableAutoMerge is the disarm document, whose caller is [Client.CancelAutoMerge]
// on a pull request whose record names an armed auto-merge. It selects no budget
// object for the reason [enableAutoMerge] gives.
var disableAutoMerge = document{
	name: "DisableAutoMerge",
	text: `mutation DisableAutoMerge($pullRequestId: ID!) {
  disablePullRequestAutoMerge(input: {pullRequestId: $pullRequestId}) {
    pullRequest { number autoMergeRequest { enabledAt } }
  }
}
`,
	mutation: true,
}

// repoOwned is one owner's repositories, whose caller is [Client.ListRepos] under an
// owner scope. repositoryOwner resolves a user and an organization alike, and
// ownerAffiliations is sent because the schema defaults it to [OWNER, COLLABORATOR].
var repoOwned = document{
	name:    "RepoOwned",
	absence: "repositoryOwner",
	text: `query RepoOwned($owner: String!, $first: Int!, $after: String) {` + rateLimitSelection + `
  repositoryOwner(login: $owner) {
    repositories(first: $first, after: $after, ownerAffiliations: [OWNER]) {
      pageInfo { hasNextPage endCursor }
      nodes {
        nameWithOwner description url isPrivate isArchived isFork updatedAt
        defaultBranchRef { name }
        hasIssuesEnabled viewerPermission mergeCommitAllowed squashMergeAllowed rebaseMergeAllowed
      }
    }
  }
}
`,
}

// repoOwnedPageCeiling is the most rows [repoOwned] asks for on one page: this
// product refuses a connection's first argument above 100
// (https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api).
const repoOwnedPageCeiling = 100

// envelope is the GraphQL response envelope, decoded whenever the body is JSON
// whatever the status: an unknown field, an unresolvable selection and a partial
// result all arrive at HTTP 200, so the status alone classifies none of them.
type envelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []docError      `json:"errors"`
}

// carriesData reports whether this envelope answered a payload beside its errors,
// which is what separates a partial success from a refusal.
//
// The two ways an envelope declines to answer are not the same bytes. An OMITTED data
// member leaves the raw message empty; a data member spelled JSON null holds the four
// bytes of that literal, so a length test alone reads the second as a payload and
// reports a refusal as partial data.
func (e *envelope) carriesData() bool {
	return len(e.Data) > 0 && string(e.Data) != "null"
}

// docError is one envelope error. Its path is a HETEROGENEOUS array of strings and
// integers on this product, measured, which is why it is held as raw JSON rather than
// as a typed list that would fail to decode the other element type.
type docError struct {
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Path    json.RawMessage `json:"path"`
}

// notFoundType is the discriminator this product sends for a node no one holds.
const notFoundType = "NOT_FOUND"

// on reports whether this error's path starts at the root field named.
func (e *docError) on(field string) bool {
	var path []json.RawMessage
	var first string
	return json.Unmarshal(e.Path, &path) == nil && len(path) > 0 &&
		json.Unmarshal(path[0], &first) == nil && first == field
}

// absenceOf reads an envelope's errors against the document's nullable root: whether
// every error is a NOT_FOUND on it, and the first other error on it, which is a
// resolver failure that nulled the root rather than an absence.
func (d *document) absenceOf(errs []docError) (onlyNotFound bool, failure *docError) {
	if d.absence == "" || len(errs) == 0 {
		return false, nil
	}
	onlyNotFound = true
	for i := range errs {
		absent := errs[i].Type == notFoundType && errs[i].on(d.absence)
		onlyNotFound = onlyNotFound && absent
		if failure == nil && !absent && errs[i].on(d.absence) {
			failure = &errs[i]
		}
	}
	return onlyNotFound, failure
}

// docData is the decoded `data` of one document, which reports what the endpoint
// charged for it off the budget object every document selects.
type docData interface {
	cost() int
}

// docResult is what one document execution answered beside the decoded payload: the
// cost the endpoint charged, and whether data arrived BESIDE errors, which is the
// partial success the caller marks rather than truncating or failing.
type docResult struct {
	// Answer is the answer the document came on.
	Answer *transport.Response
	// Refusal is the first envelope error's message, quoted, on a partial answer: a
	// mutation whose payload is null beside an error is refused by that error.
	Refusal string
	// RootFailure is the quoted message of a non-NOT_FOUND error on the document's
	// absence root, empty where none arrived: the root's null is then that failure.
	RootFailure string
	// errors are the envelope errors of a partial answer, which a caller places
	// against the rows they fell in (see [docResult.placed]).
	errors  []docError
	Cost    int
	Partial bool
}

// forbiddenType is the discriminator this product sends for a field the credential
// may not read, measured on a check rollup's contexts under a fine-grained token.
const forbiddenType = "FORBIDDEN"

// placement is where a partial answer's envelope errors fell, against the node at the
// path a caller names.
type placement struct {
	// refused holds the row indices whose check rollup the product refused to the
	// credential; on a read of one node the only index is zero.
	refused map[int]bool
	// rollup reports another error under the node's check rollup.
	rollup bool
	// other reports an error anywhere else, which leaves unnamed what it nulled.
	other bool
}

// placed sorts the answer's errors against the node at prefix. rows says a row index
// follows prefix, and present says whether that row was answered: an error inside a
// row nothing answered is the page's, since the row itself is what it nulled.
func (r *docResult) placed(prefix []string, rows bool, present func(int) bool) placement {
	var out placement
	for i := range r.errors {
		row, rollup, ok := r.errors[i].under(prefix, rows)
		switch {
		case !ok || (rows && !present(row)):
			out.other = true
		case rollup && r.errors[i].Type == forbiddenType:
			if out.refused == nil {
				out.refused = map[int]bool{}
			}
			out.refused[row] = true
		case rollup:
			out.rollup = true
		default:
			out.other = true
		}
	}
	return out
}

// unplaced reports an error no row's own marker names, which leaves its page or its
// read partial.
func (p *placement) unplaced() bool { return p.rollup || p.other }

// whole reports a placement of no error at all.
func (p *placement) whole() bool { return len(p.refused) == 0 && !p.unplaced() }

// add folds a further page's placement into p.
func (p *placement) add(q placement) {
	for i := range q.refused {
		if p.refused == nil {
			p.refused = map[int]bool{}
		}
		p.refused[i] = true
	}
	p.rollup = p.rollup || q.rollup
	p.other = p.other || q.other
}

// under reports whether this error's path lies inside the node at prefix, which row of
// it where rows is set, and whether the path reaches into the node's check rollup.
func (e *docError) under(prefix []string, rows bool) (row int, rollup, ok bool) {
	var path []any
	if json.Unmarshal(e.Path, &path) != nil || len(path) <= len(prefix) {
		return 0, false, false
	}
	for i, want := range prefix {
		if got, isString := path[i].(string); !isString || got != want {
			return 0, false, false
		}
	}
	rest := path[len(prefix):]
	if rows {
		index, isNumber := rest[0].(float64)
		if !isNumber || index < 0 || index != float64(int(index)) {
			return 0, false, false
		}
		row, rest = int(index), rest[1:]
	}
	return row, slices.Contains(rest, any("statusCheckRollup")), true
}

// execute sends one document and decodes its data into out.
//
// What it does with the two channels is the classification rule this product needs,
// and neither alone is sufficient. The envelope is decoded whatever the status; a
// non-2xx whose body carries no envelope is mapped from the status alone; the real
// status is carried always; and data present beside errors yields the data plus the
// partial marker, which on this product is a MEASURED shape rather than a defensive
// one, since a selection needing context the caller cannot supply answers 200 with the
// scalars populated and one error per nulled node.
//
// A failure that is the DOCUMENT's own marks this connection degraded, so the two
// reads that have a REST arm take it from their next call. A credential failure, a
// throttle, a transport failure and an absence are not the document's, so none of
// them degrades a connection that would answer the document again.
//
// It carries no header of this family's own. The date version this family pins names
// a REST API version, so the pin belongs to the two REST helpers and sending it here
// would name a REST version on a surface that has none.
func (c *Client) execute(ctx context.Context, op string, doc document, vars map[string]any, out docData) (docResult, error) {
	resp, err := c.core.Do(ctx, &transport.Request{
		Op:     op,
		Method: http.MethodPost,
		Body: map[string]any{
			"operationName": doc.name,
			"query":         doc.text,
			"variables":     vars,
		},
		Document:    true,
		Mutation:    doc.mutation,
		AbsoluteURL: c.documentURL,
	})
	c.noteResponse(resp)
	body := responseBody(resp)
	var env envelope
	decodeErr := transport.Decode(body, &env)
	decoded := decodeErr == nil
	if err != nil && (!decoded || len(env.Errors) == 0 || transport.IsThrottled(err)) {
		markSecondary(err, decoded, env.Errors)
		c.degradeOn(err)
		return docResult{}, err
	}
	if !decoded {
		return docResult{}, c.core.FailDecode(ctx, op, transport.Document(http.MethodPost), resp, decodeErr)
	}
	onlyNotFound, rootFailure := doc.absenceOf(env.Errors)
	if len(env.Errors) > 0 && !env.carriesData() {
		if limited := c.documentLimit(ctx, op, doc, resp, env.Errors); limited != nil {
			return docResult{}, limited
		}
		if !onlyNotFound {
			return docResult{}, c.refuseDocument(ctx, op, doc, resp, env.Errors)
		}
		c.countEnvelope(env.Errors)
		return docResult{Answer: resp}, nil
	}
	if err := transport.Decode(env.Data, out); err != nil {
		return docResult{}, c.core.FailDecode(ctx, op, transport.Document(http.MethodPost), resp, err)
	}
	result := docResult{Answer: resp, Cost: out.cost(), Partial: len(env.Errors) > 0}
	if result.Cost > 0 {
		c.core.Price(result.Cost)
	}
	if result.Partial {
		c.countEnvelope(env.Errors)
		result.Refusal = resp.Quote(env.Errors[0].Message)
		result.errors = env.Errors
	}
	if rootFailure != nil {
		result.RootFailure = resp.Quote(rootFailure.Message)
	}
	return result, nil
}

// responseBody is the bytes of a response that may have come back with a failure,
// because the envelope is read on that arm too.
func responseBody(resp *transport.Response) []byte {
	if resp == nil {
		return nil
	}
	return resp.Body
}

// refuseDocument is the refusal an envelope carrying errors and no data earns.
//
// The kind is the upstream one and no code is minted, which is this library's own rule
// that an unmapped combination stays upstream and is never guessed: the shapes
// measured on this product are the document's own failure rather than a classifiable
// cause, and mapping on the message text is the guess this library refuses. The
// counter carries the envelope's own type where it sent one, so a shape this reading
// does not cover is visible as that rather than as a bare upstream failure. The
// envelope's message is the instance's text, so it is quoted as the response's.
func (c *Client) refuseDocument(ctx context.Context, op string, doc document, resp *transport.Response, errs []docError) *forgeapi.Error {
	c.countEnvelope(errs)
	c.markDegraded()
	return c.core.Fail(ctx, op, transport.Document(http.MethodPost), resp, "", forgeapi.KindUpstream,
		resp.Quote("the "+doc.name+" document was refused: "+errs[0].Message))
}

// documentLimit is the rate limit an envelope carrying errors and no data is, read
// as [githublimit.DocumentLimited] reads it with any envelope error's message, nil
// where it is none. It degrades nothing, since a throttle is not the document's.
func (c *Client) documentLimit(ctx context.Context, op string, doc document, resp *transport.Response, errs []docError) *forgeapi.Error {
	limited, secondary := githublimit.DocumentLimited(resp.Status, resp.Header, saidSecondary(errs))
	if !limited {
		return nil
	}
	c.countEnvelope(errs)
	if counters := c.core.Counters(); counters.RateLimited != nil {
		counters.RateLimited(forgeapi.FamilyGitHub)
	}
	fe := c.core.Fail(ctx, op, transport.Document(http.MethodPost), resp, "", forgeapi.KindRateLimited,
		resp.Quote("the "+doc.name+" document was rate-limited: "+errs[0].Message))
	fe.Secondary, fe.RetryAfter = secondary, transport.RetryAfter(resp.Header)
	return fe
}

// markSecondary sets Secondary on a throttle whose decoded envelope errors carry
// the secondary-limit message, which rides them rather than the member the
// transport's reading takes. Only a throttle fails with envelope errors before
// they are read.
func markSecondary(err error, decoded bool, errs []docError) {
	var fe *forgeapi.Error
	if decoded && saidSecondary(errs) && asForgeError(err, &fe) {
		fe.Secondary = true
	}
}

// saidSecondary reports whether an envelope error carries the secondary-limit
// message ([githublimit.SecondaryMessage]).
func saidSecondary(errs []docError) bool {
	return slices.ContainsFunc(errs, func(e docError) bool { return githublimit.SecondaryMessage(e.Message) })
}

// countEnvelope records one document's envelope errors by the discriminator this
// product sends, so a maintainer sees WHICH shape arrived rather than a count of
// failures.
func (c *Client) countEnvelope(errs []docError) {
	counters := c.core.Counters()
	if counters.EnvelopeError == nil {
		return
	}
	for _, e := range errs {
		kind := e.Type
		if kind == "" {
			kind = "no-type"
		}
		counters.EnvelopeError(forgeapi.FamilyGitHub, kind)
	}
}

// degradeOn marks the connection degraded where the failure was the document's own. A
// credential failure, a throttle, an absence, a transport failure and a refused
// attempt are not: a connection that meets one of those would answer the document on
// the next call, and degrading it would spend the rest of its life on REST for a
// reason that has passed.
func (c *Client) degradeOn(err error) {
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || transport.IsRefused(err) {
		return
	}
	switch fe.Kind {
	case forgeapi.KindUnauthorized, forgeapi.KindForbidden, forgeapi.KindRateLimited,
		forgeapi.KindTransient, forgeapi.KindNotFound:
		return
	case forgeapi.KindUnknown, forgeapi.KindNotMergeable, forgeapi.KindConflict, forgeapi.KindUpstream:
		c.markDegraded()
	}
}

// ownerName splits one selector into the two variables every repository-addressed
// document of this product takes, refusing a selector that is not the owner-and-name
// pair those variables need.
func ownerName(repo forgeapi.RepoRef) (owner, name string, err error) {
	owner, name, ok := strings.Cut(repo.Selector, "/")
	if !ok || owner == "" || name == "" {
		return "", "", errorf(forgeapi.CodeRepoRefInvalid,
			"repository selector %q is not the owner and name pair this product's documents take", repo.Selector)
	}
	return owner, name, nil
}

// documentStates is the state filter a list document takes, which is a LIST of
// enumeration members rather than one word, and null where the caller asked for all
// of them.
func documentStates(s forgeapi.ListState) any {
	switch s {
	case forgeapi.ListStateClosed:
		return []string{"CLOSED"}
	case forgeapi.ListStateMerged:
		return []string{"MERGED"}
	case forgeapi.ListStateAll:
		return nil
	case forgeapi.ListStateOpen, forgeapi.ListStateUnknown:
		return []string{"OPEN"}
	}
	return []string{"OPEN"}
}

// The item types the two cross-repository documents search for, in the search
// grammar's own spelling.
const (
	searchPulls  = "pr"
	searchIssues = "issue"
)

// searchQuery is the query string a cross-repository document takes: one item type,
// the scope, and the open state both lists are fixed to. The scope is the search
// vocabulary's author keyword for the credential's own items, or its owner qualifier
// in that keyword's place under an owner scope, which resolves an organization's login
// as it resolves a user's, measured on both. Either way the query names no
// repository, which is what makes the call's price independent of how many
// repositories the credential can reach.
func searchQuery(itemType string, set *forgeapi.ListSettings) string {
	scope := "author:@me"
	if set.OwnerSet {
		scope = "user:" + set.Owner
	}
	return "type:" + itemType + " " + scope + " state:open"
}

// searchVariables are the variables both cross-repository documents take for one
// page of one query.
func searchVariables(query string, set *forgeapi.ListSettings, after string) map[string]any {
	return map[string]any{
		"q":      query,
		varFirst: set.PageBound,
		varAfter: nullableString(after),
	}
}

// encodeAfter turns this product's own opaque list cursor into the continuation call
// mints, which crosses the surface, and decodeAfter turns it back.
//
// The cursor is base64url without padding, because the raw cursor is base64 of an
// upstream token and its standard alphabet carries bytes [forgeapi.ValidateCursor]
// refuses. The call's digest rides beside it, because upstream accepts the position
// in any repository's or state's connection and would resume another walk there.
func encodeAfter(call transport.PageCall, cursor string) forgeapi.Cursor {
	if cursor == "" {
		return ""
	}
	return call.MintPosition(documentPrefix, base64.RawURLEncoding.EncodeToString([]byte(cursor)))
}

// decodeAfter recovers the upstream cursor from a continuation call minted, refusing
// one this library did not mint and one another call minted.
//
// It also refuses the PAGE-NUMBERED form and a search's, which is a real case rather
// than a defensive one: this family mints all three encodings, so a caller holding one
// from a REST list or a cross-repository search can hand it to a repository's list
// document, and the message names the change.
func decodeAfter(call transport.PageCall, c forgeapi.Cursor) (string, error) {
	if c == "" {
		return "", nil
	}
	if err := forgeapi.ValidateCursor(c); err != nil {
		return "", err
	}
	if strings.HasPrefix(string(c), searchPrefix) {
		return "", transport.Local(forgeapi.CodeCursorInvalid,
			"continuation was minted by one of this family's cross-repository searches and this call reads a repository's list, so it names no position here")
	}
	if !strings.HasPrefix(string(c), documentPrefix) {
		return "", transport.Local(forgeapi.CodeCursorInvalid,
			"continuation was minted by one of this family's REST lists and this call reads a document, so it names no position here")
	}
	position, err := call.ResumePosition(c, documentPrefix, 1)
	if err != nil {
		return "", err
	}
	raw, err := base64.RawURLEncoding.DecodeString(position[0])
	if err != nil || len(raw) == 0 {
		return "", transport.Local(forgeapi.CodeCursorInvalid, "continuation is not one of this library's own")
	}
	return string(raw), nil
}

// searchNext is the continuation one page of a cross-repository search mints, empty
// where no request reaches a further page. It carries the rows the walk has been
// served, this page's included, beside the connection's own position, encoded for
// the reason [encodeAfter] states, and the call's digest, which names the item type
// and the scope the query is built from: the two searches read one connection, so
// upstream accepts one walk's position in another's.
func searchNext(info docPageInfo, call transport.PageCall, served int) forgeapi.Cursor {
	if !info.HasNextPage || info.EndCursor == "" {
		return ""
	}
	return call.MintPosition(searchPrefix, strconv.Itoa(served), base64.RawURLEncoding.EncodeToString([]byte(info.EndCursor)))
}

// decodeSearchAfter recovers the upstream position and the rows the walk has been
// served from a search continuation call minted, the empty one being the first page
// with none served. It refuses every other encoding this family mints, for the reason
// [searchPrefix] states, another call's continuation, and a count that is not one
// this library minted.
func decodeSearchAfter(c forgeapi.Cursor, call transport.PageCall) (position string, served int, err error) {
	if c == "" {
		return "", 0, nil
	}
	if invalid := forgeapi.ValidateCursor(c); invalid != nil {
		return "", 0, invalid
	}
	if !strings.HasPrefix(string(c), searchPrefix) {
		return "", 0, transport.Local(forgeapi.CodeCursorInvalid,
			"continuation was minted by another of this family's lists and this call reads a cross-repository search, so it names no position here")
	}
	fields, err := call.ResumePosition(c, searchPrefix, 2)
	if err != nil {
		return "", 0, err
	}
	served, notCount := strconv.Atoi(fields[0])
	if notCount != nil || served < 0 || served > maxSearchServed {
		return "", 0, transport.Local(forgeapi.CodeCursorInvalid, "continuation names no count of the rows its walk was served")
	}
	raw, notPosition := base64.RawURLEncoding.DecodeString(fields[1])
	if notPosition != nil || len(raw) == 0 {
		return "", 0, transport.Local(forgeapi.CodeCursorInvalid, "continuation is not one of this library's own")
	}
	return string(raw), served, nil
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
