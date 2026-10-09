package gitlab

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// documentPath is this product's GraphQL endpoint, which sits beside the versioned
// REST root rather than under it, so it is derived from the WEB base as the one
// other absolute read this library makes is.
const documentPath = "/api/graphql"

// documentPrefix opens the continuation the LIST DOCUMENT mints, which is a
// different encoding from the page-numbered one the REST lists use: the document's
// own cursor is an opaque upstream string whose bytes are outside the class
// [forgeapi.ValidateCursor] admits, so it crosses the surface base64url encoded.
const documentPrefix = "g"

// mergeRequestFields is the node selection both documents share, all a normalized
// pull request needs from this product. `diffHeadSha` is the head a merge and a
// re-run pin to; `headPipeline.sha` is a merged-results SHA, not the branch's.
// `labels` is a connection of `title`s, one page of a set this product does not cap,
// measured by `count` and `hasNextPage` ([Client.labelCut]). `headPipeline.status` is
// one scalar, so no checks fold here can truncate. No merge-train field is selected:
// introspected 2026-09-20, `Project.mergeTrains` and `MergeTrain.cars` are deprecated,
// which the schema gate refuses.
const mergeRequestFields = `
      iid
      title
      description
      author { username }
      sourceBranch
      sourceProject { fullPath }
      targetBranch
      webUrl
      diffHeadSha
      createdAt
      updatedAt
      state
      draft
      mergedAt
      mergeable
      detailedMergeStatus
      autoMergeEnabled
      labels(first: 100) {
        count
        pageInfo { hasNextPage }
        nodes { title color description }
      }
      headPipeline { status }`

// document is one named GraphQL document this family ships: its operation name, its
// text and the variables its callers fill.
//
// There are TWO rather than one, because their field sets differ at the collection
// they select: a read must not pay a list's cost and a list must not pay a read's.
// A document is not an operation, and the operations that reach each are named in
// its own doc comment below.
type document struct {
	name string
	text string
}

// prList is the pull-request LIST document. Its callers are [Client.ListPRs]. The
// state filter is a variable rather than a literal because the published list
// option reaches it, and the page size is a variable because the per-call page
// bound does.
var prList = document{
	name: "PRList",
	text: `query PRList($fullPath: ID!, $state: MergeRequestState, $first: Int!, $after: String) {
  project(fullPath: $fullPath) {
    fullPath
    userPermissions { pushCode readMergeRequest }
    mergeRequests(state: $state, first: $first, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes {` + mergeRequestFields + `
      }
    }
  }
  queryComplexity { score limit }
}
`,
}

// prRead is the single-merge-request document. Its callers are [Client.ReadPR],
// [Client.MergeStatus] and, through the permissions it selects, [Client.GrantCaps].
// The merged answer comes from the pair this selection carries, the state member
// and the nullable merged timestamp, because this product has no merged boolean.
var prRead = document{
	name: "PRRead",
	text: `query PRRead($fullPath: ID!, $iid: String!) {
  project(fullPath: $fullPath) {
    fullPath
    userPermissions { pushCode readMergeRequest }
    mergeRequest(iid: $iid) {` + mergeRequestFields + `
    }
  }
  queryComplexity { score limit }
}
`,
}

// docAcceptance is the ONE bounded question a connection asks its instance at setup:
// does this schema carry every field the two documents above select. Its answer is
// what decides whether this connection reads documents or REST, so no operation has
// to discover it and every operation keeps an exact price.
//
// It is built FROM the shared node selection rather than from a list of field names
// beside it, so a selection that changes cannot leave the question asking about the
// old one, and the two arguments the list document passes as variables are passed
// here as literals so that their names and the state member are ruled on too.
//
// It resolves NOTHING. The project selector is empty, so the project member is null
// and no merge request, label or pipeline is read, while validation still rules on
// every field the selection names. Measured anonymously against gitlab.com on
// 2026-09-20: 68 bytes of answer at a complexity of 53 against that instance's own
// limit of 200, and the same question carrying one field the schema does not declare
// answered 200 with an undefinedField error and no data, which is the refusal
// [Client.refuseDocument] already classifies.
//
// Introspection is not the question on this product, which is what makes this the
// smallest equivalent rather than a second choice. Measured the same day: a NAMED
// operation cannot select the introspection field at all, answering undefinedField
// for __type on the Query type, and an unnamed one is answered with the whole
// schema, 8.2 MB of it, whatever the query asked for.
var docAcceptance = document{
	name: "DocumentAcceptance",
	text: `query DocumentAcceptance {
  project(fullPath: "") {
    fullPath
    userPermissions { pushCode readMergeRequest }
    mergeRequests(state: opened, first: 1, after: null) {
      pageInfo { hasNextPage endCursor }
      nodes { iid }
    }
    mergeRequest(iid: "0") {` + mergeRequestFields + `
    }
  }
  queryComplexity { score limit }
}
`,
}

// askSchema asks the acceptance question and holds this connection to its answer.
//
// It answers nothing to its caller, because the question's outcome is held on the
// connection rather than returned: a refusal that is the DOCUMENT's own reaches
// [Client.markDegraded] through the arm every refused document reaches, so every
// document read on this connection goes to REST from its first call. A credential
// failure, a throttle and a transport failure are not the schema's answer and
// degrade nothing, which leaves the documents to be tried on a connection whose
// question went unanswered.
func (c *Client) askSchema(ctx context.Context, op string) {
	var answer docPayload
	// The error is the answer rather than a failure, and it is already recorded:
	// the refusal arm logs the exchange it happened on and marks the connection,
	// and a capability read must not fail because the schema question did.
	_, _ = c.execute(ctx, op, docAcceptance, map[string]any{}, &answer)
}

// envelope is the GraphQL response envelope, decoded whenever the body is JSON
// whatever the status: an unknown field, an unresolvable selection and a complexity
// refusal all arrive at HTTP 200, so the status alone classifies none of them.
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
// reports a refusal as partial data, handing the caller a zero value with a marker
// instead of the error the instance sent.
func (e *envelope) carriesData() bool {
	return len(e.Data) > 0 && string(e.Data) != "null"
}

// docError is one envelope error. Its extensions carry this product's own
// discriminator where it sends one, which is what the counter records.
type docError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

// docResult is what one document execution answered beside the decoded payload: the
// complexity the endpoint charged, and whether data arrived BESIDE errors, which is
// the partial success the caller marks rather than truncating or failing.
type docResult struct {
	Cost    int
	Partial bool
}

// execute sends one document and decodes its data into out.
//
// What it does with the two channels is the classification rule this product needs,
// and neither alone is sufficient. The envelope is decoded whatever the status; a
// non-2xx whose body carries no envelope is mapped from the status alone, which is
// the query-timeout case; the real status is carried always; and data present beside
// errors yields the data plus the partial marker rather than a silent truncation or
// a hard failure.
//
// A failure that is the DOCUMENT's own marks this connection degraded, so every
// later document read on it goes to REST. A credential failure, a throttle, a
// transport failure and an absence are not the document's, so none of them degrades
// a connection that would answer the document again on the next call.
func (c *Client) execute(ctx context.Context, op string, doc document, vars map[string]any, out any) (docResult, error) {
	target := strings.TrimSuffix(c.core.WebBase().String(), "/") + documentPath
	resp, err := c.do(ctx, &transport.Request{
		Op:     op,
		Method: http.MethodPost,
		Body: map[string]any{
			"operationName": doc.name,
			"query":         doc.text,
			"variables":     vars,
		},
		Document:    true,
		AbsoluteURL: target,
	})
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
	result := docResult{Cost: complexityOf(out), Partial: len(env.Errors) > 0}
	if result.Cost > 0 {
		c.core.Price(result.Cost)
	}
	if result.Partial {
		c.countEnvelope(env.Errors)
	}
	return result, nil
}

// responseBody is the bytes and the status of a response that may have come back
// with a failure, because the envelope is read on that arm too.
func responseBody(resp *transport.Response) (body []byte, status int) {
	if resp == nil {
		return nil, 0
	}
	return resp.Body, resp.Status
}

// refuseDocument is the refusal an envelope carrying errors and no data earns.
//
// Every shape measured on this product is the DOCUMENT's own failure rather than a
// classifiable cause: an unknown field answers 200 with an extensions code of
// undefinedField, and a complexity refusal answers 200 with a message and no
// extensions at all. So the kind is the upstream one and no code is minted, which is
// the section's own rule that an unmapped combination stays upstream and is never
// guessed; mapping on the message text is the guess this library refuses. The
// counter carries the extensions code where the product sent one, so a shape this
// reading does not cover is visible as that rather than as a bare upstream failure.
// The envelope's message is the instance's text, so it is quoted as the response's.
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
		kind := e.Extensions.Code
		if kind == "" {
			kind = "no-extensions"
		}
		counters.EnvelopeError(forgeapi.FamilyGitLab, kind)
	}
}

// degradeOn marks the connection degraded where the failure was the document's own.
// A credential failure, a throttle, an absence and a transport failure are not: a
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

// complexityOf is the cost the endpoint charged for the document just executed,
// read off the payload that carries it, zero where the payload carries none.
func complexityOf(out any) int {
	if payload, ok := out.(interface{ complexity() int }); ok {
		return payload.complexity()
	}
	return 0
}

// encodeAfter turns this product's own opaque list cursor into the continuation call
// mints, which crosses the surface, and decodeAfter turns it back.
//
// The cursor is base64url without padding, because the raw cursor is base64 of a
// JSON object and its standard alphabet carries bytes [forgeapi.ValidateCursor]
// refuses. The call's digest rides beside it, because upstream accepts the position
// in any project's or state's connection and would resume another walk there.
func encodeAfter(call transport.PageCall, cursor string) forgeapi.Cursor {
	if cursor == "" {
		return ""
	}
	return call.MintPosition(documentPrefix, base64.RawURLEncoding.EncodeToString([]byte(cursor)))
}

// decodeAfter recovers the upstream cursor from a continuation call minted, refusing
// one this library did not mint and one another call minted.
//
// It also refuses the PAGE-NUMBERED form, which is a real case rather than a
// defensive one: the list this document serves degrades to a REST list per
// connection, so a caller holding a continuation minted by one transport can hand it
// to the other, and the message names the change.
func decodeAfter(call transport.PageCall, c forgeapi.Cursor) (string, error) {
	if c == "" {
		return "", nil
	}
	if err := forgeapi.ValidateCursor(c); err != nil {
		return "", err
	}
	if !strings.HasPrefix(string(c), documentPrefix) {
		return "", transport.Local(forgeapi.CodeCursorInvalid,
			"continuation was minted by this connection's REST list and this call reads the document, so it names no position here")
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
