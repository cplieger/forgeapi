package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
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

// pullRequestFields is the node selection every pull-request document shares, which
// is the whole of what a normalized pull request needs from this product.
//
// Five choices in it are load-bearing. `headRefOid` is the head a merge and a re-run
// pin to, not the rollup commit's oid. `mergeable` and `mergeStateStatus` answer
// different questions, measured MERGEABLE beside BLOCKED on one pull request.
// `autoMergeRequest` is a nullable object whose presence is the armed answer.
// `mergeQueueEntry` carries the five-member queue state and the position. And
// `labels(first: 100)` is this product's limit on one row's labels, so the page is
// the whole set rather than a cut nothing marks.
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
        author { login }
        headRefName
        headRepository { nameWithOwner }
        baseRefName
        headRefOid
        mergeable
        mergeStateStatus
        merged
        autoMergeRequest { enabledAt mergeMethod }
        mergeQueueEntry { state position }
        labels(first: 100) { totalCount nodes { name color description } }
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
type document struct {
	name     string
	text     string
	mutation bool
}

// prList is the pull-request LIST document, whose caller is [Client.ListPRs]. Its
// fold is BOUNDED, which is what the one-context page says, and its state filter and
// page size are variables because the published list options reach them.
var prList = document{
	name: "PRList",
	text: `query PRList($owner: String!, $name: String!, $first: Int!, $after: String, $states: [PullRequestState!]) {` + rateLimitSelection + `
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
// bound asks. Its rows carry the selection [prMine]'s do, the check rollup and the
// merge state, and on a repository with many open pull requests a larger page outruns
// the upstream's evaluation time: measured on one such repository, a page of 50
// answered on every attempt, the slowest in under 8 s, and a page of 75 answered 502
// on every attempt after about 10.6 s. A gateway answer is retried and still charged,
// so a larger page spends the budget and serves nothing; a page this ceiling shortened
// carries its continuation like any other.
const prListPageCeiling = 50

// prRead is the single-pull-request document, whose callers are [Client.ReadPR] and
// [Client.MergeStatus]. Its field set is the list's minus the list machinery plus the
// contexts CURSOR, because the fold on a single read is complete: the cursor is handed
// back until the connection reports no next page, bounded by
// [forgeapi.Budget.StatusPages].
var prRead = document{
	name: "PRRead",
	text: `query PRRead($owner: String!, $name: String!, $number: Int!, $checksAfter: String) {` + rateLimitSelection + `
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
	text: `query PRMine($q: String!, $first: Int!, $after: String) {` + rateLimitSelection + `
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
// page bound asks, under either scope. Its rows' own selection, the check rollup and
// the merge state, outruns the upstream's evaluation time at a larger page: measured,
// a page of 100 answered 502 under two owners whatever the labels page, the smaller of
// them holding 99 open pull requests, and a page of 50 under the larger, while 25
// answered under both and [issueMine] answered a page of 100 under the larger. So the
// page decides it, not the owner's size. A gateway answer is retried and still
// charged, so a larger page spends the budget and serves nothing; a page this ceiling
// shortened carries its continuation like any other.
const prSearchPageCeiling = 25

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
        author { login }
        labels(first: 100) { totalCount nodes { name color description } }
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

// docError is one envelope error. Its path is not decoded: on this product it is a
// HETEROGENEOUS array of strings and integers, measured, so a typed list of either
// element type would fail to decode the other and take the envelope with it.
type docError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
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
	// Refusal is the first envelope error's message, quoted, on a partial answer: a
	// mutation whose payload is null beside an error is refused by that error.
	Refusal string
	Cost    int
	Status  int
	Partial bool
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
	body, status := responseBody(resp)
	var env envelope
	decoded := len(body) > 0 && transport.Decode(body, &env) == nil
	if err != nil && (!decoded || len(env.Errors) == 0) {
		c.degradeOn(err)
		return docResult{}, err
	}
	if !decoded {
		return docResult{}, c.core.FailBody(ctx, op, transport.Document(http.MethodPost), status)
	}
	if len(env.Errors) > 0 && !env.carriesData() {
		return docResult{}, c.refuseDocument(ctx, op, doc, resp, env.Errors)
	}
	if err := transport.Decode(env.Data, out); err != nil {
		return docResult{}, c.core.FailBody(ctx, op, transport.Document(http.MethodPost), status)
	}
	result := docResult{Cost: out.cost(), Status: status, Partial: len(env.Errors) > 0}
	if result.Cost > 0 {
		c.core.Price(result.Cost)
	}
	if result.Partial {
		c.countEnvelope(env.Errors)
		result.Refusal = resp.Quote(env.Errors[0].Message)
	}
	return result, nil
}

// responseBody is the bytes and the status of a response that may have come back with
// a failure, because the envelope is read on that arm too.
func responseBody(resp *transport.Response) (body []byte, status int) {
	if resp == nil {
		return nil, 0
	}
	return resp.Body, resp.Status
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
	return c.core.Fail(ctx, op, transport.Document(http.MethodPost), "", forgeapi.KindUpstream, resp.Status,
		resp.Quote("the "+doc.name+" document was refused: "+errs[0].Message))
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
// credential failure, a throttle, an absence and a transport failure are not: a
// connection that meets one of those would answer the document on the next call, and
// degrading it would spend the rest of its life on REST for a reason that has passed.
func (c *Client) degradeOn(err error) {
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
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
func searchQuery(itemType string, set forgeapi.ListSettings) string {
	scope := "author:@me"
	if set.OwnerSet {
		scope = "user:" + set.Owner
	}
	return "type:" + itemType + " " + scope + " state:open"
}

// searchVariables are the variables both cross-repository documents take for one
// page of one query.
func searchVariables(query string, set forgeapi.ListSettings, after string) map[string]any {
	return map[string]any{
		"q":     query,
		"first": set.PageBound,
		"after": nullableString(after),
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
