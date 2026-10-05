package gitlab

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// The document answers this file drives. Each is the envelope this product's endpoint
// actually sends for one shape, and the two error bodies are the two shapes measured
// on gitlab.com: an unknown field answers 200 with an extensions code, and a
// complexity refusal answers 200 with a message and no extensions at all.
const (
	docNode = `{
      "iid": "1", "title": "Example pull request", "description": "Example body.",
      "author": {"username": "example-user"},
      "sourceBranch": "example-feature", "targetBranch": "main",
      "webUrl": "https://forge.example/example/group/example/-/merge_requests/1",
      "diffHeadSha": "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d",
      "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-02T00:00:00Z",
      "state": "opened", "draft": false, "mergedAt": null,
      "mergeable": true, "detailedMergeStatus": "MERGEABLE", "autoMergeEnabled": false,
      "labels": {"pageInfo": {"hasNextPage": false},
                 "nodes": [{"title": "example-label", "color": "#ededed", "description": "Example label."}]},
      "headPipeline": {"status": "SUCCESS"}
    }`

	docList = `{"data": {"queryComplexity": {"score": 94, "limit": 200},
  "project": {"fullPath": "example/group/example",
    "userPermissions": {"pushCode": true, "readMergeRequest": true},
    "mergeRequests": {"pageInfo": {"hasNextPage": false, "endCursor": null},
      "nodes": [` + docNode + `]}}}}`

	docRead = `{"data": {"queryComplexity": {"score": 46, "limit": 200},
  "project": {"fullPath": "example/group/example",
    "userPermissions": {"pushCode": true, "readMergeRequest": true},
    "mergeRequest": ` + docNode + `}}}`

	docNullProject = `{"data": {"project": null}}`

	// docAcceptanceAnswer is what the bounded schema question setup asks answers on
	// an instance whose schema carries every field the two documents select: a null
	// project, because the question resolves none, beside the complexity the endpoint
	// charged. Both halves are the live measurement's own.
	docAcceptanceAnswer = `{"data": {"project": null, "queryComplexity": {"score": 53, "limit": 200}}}`

	docNullMergeRequest = `{"data": {"queryComplexity": {"score": 46, "limit": 200},
  "project": {"fullPath": "example/group/example",
    "userPermissions": {"pushCode": true, "readMergeRequest": true},
    "mergeRequest": null}}}`

	docUnknownField = `{"errors": [{"message": "Field 'noSuchField' doesn't exist on type 'MergeRequest'",
    "extensions": {"code": "undefinedField", "typeName": "MergeRequest", "fieldName": "noSuchField"}}]}`

	docTooComplex = `{"errors": [{"message": "Query has complexity of 448, which exceeds max complexity of 200"}]}`

	// docNullData is the third refusal shape: the data member is PRESENT and spelled
	// JSON null, which the specification permits for a failure that reaches no field
	// and which a length test over the raw member reads as a payload.
	docNullData = `{"data": null, "errors": [{"message": "the document was refused"}]}`

	docPartial = `{"errors": [{"message": "one selection could not be resolved"}],
  "data": {"queryComplexity": {"score": 94, "limit": 200},
  "project": {"fullPath": "example/group/example",
    "userPermissions": {"pushCode": true, "readMergeRequest": true},
    "mergeRequests": {"pageInfo": {"hasNextPage": false, "endCursor": null},
      "nodes": [` + docNode + `]}}}}`

	// restMergeRequestBody is the REST answer the degraded arm reads, with the merge
	// status this product's list can serve STALE and the detailed field beside it.
	restMergeRequestBody = `{"iid": 1, "state": "opened", "title": "Example pull request",
  "description": "Example body.", "author": {"username": "example-user"},
  "web_url": "https://forge.example/example/group/example/-/merge_requests/1",
  "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z", "draft": false,
  "labels": ["example-label"], "source_branch": "example-feature", "target_branch": "main",
  "sha": "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d",
  "merge_status": "can_be_merged", "detailed_merge_status": "mergeable", "merged_at": null,
  "merge_when_pipeline_succeeds": false,
  "references": {"full": "example/group/example!1"},
  "head_pipeline": {"id": 1, "sha": "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d", "status": "success"}}`
)

// readRoute and listRoute are the REST routes the degraded arms fall back to.
const (
	readRoute = "GET /api/v4/projects/" + testEncoded + "/merge_requests/1"
	listRoute = "GET /api/v4/projects/" + testEncoded + "/merge_requests"
)

// TestBothDocumentsAreNamedAndTakeTheirVariables holds the two documents to what is
// asked of every one this library ships: a name, the variables its callers
// fill, the field set the normalizer reads, a cursor on the list and none on the read,
// and the complexity object this product prices a call by.
//
// It reads the document TEXT rather than a request, because the assertions are about
// what this library ships rather than about one call: a document that lost a selection
// would still execute, and what would change is the answer a consumer gets.
func TestBothDocumentsAreNamedAndTakeTheirVariables(t *testing.T) {
	for _, doc := range documents {
		t.Run(doc.name, func(t *testing.T) {
			if !strings.Contains(doc.text, "query "+doc.name+"(") {
				t.Errorf("the %s document does not declare itself as a named operation, so the endpoint cannot select it and this library cannot name it in a log line", doc.name)
			}
			for _, want := range []string{
				"$fullPath: ID!",
				"fullPath",
				"userPermissions { pushCode readMergeRequest }",
				"iid",
				"diffHeadSha",
				"detailedMergeStatus",
				"autoMergeEnabled",
				"headPipeline { status }",
				"queryComplexity { score limit }",
			} {
				if !strings.Contains(doc.text, want) {
					t.Errorf("the %s document does not select %q, which a normalized pull request reads", doc.name, want)
				}
			}
			if strings.Contains(doc.text, "mergeStatus") {
				t.Errorf("the %s document selects the older merge status, whose GraphQL twin is deprecated: that field is read over REST alone", doc.name)
			}
		})
	}
	if !strings.Contains(prList.text, "$after: String") || !strings.Contains(prList.text, "pageInfo { hasNextPage endCursor }") {
		t.Error("the PRList document takes no continuation or selects no page info, so its list cannot be resumed")
	}
	if !strings.Contains(prList.text, "$first: Int!") || !strings.Contains(prList.text, "$state: MergeRequestState") {
		t.Error("the PRList document takes no page size or no state, which are the two published list options that reach it")
	}
	if strings.Contains(prRead.text, "$after") || strings.Contains(prRead.text, "pageInfo { hasNextPage endCursor }") {
		t.Error("the PRRead document takes a continuation, which a single read has no use for: that is the list document's own shape")
	}
	if !strings.Contains(prRead.text, "$iid: String!") {
		t.Error("the PRRead document takes no merge-request number, which this product declares as a STRING")
	}
	if !strings.Contains(prRead.text, "mergedAt") || !strings.Contains(prRead.text, "state") {
		t.Error("the PRRead document does not select the pair the merged answer is derived from, which this product has no boolean for")
	}
}

// TestTheDocumentSentCarriesItsOwnNameAndVariables holds the REQUEST rather than the
// text: the endpoint selects by operation name, so a body that posted the text without
// naming it would execute a different document than the one this library logs.
func TestTheDocumentSentCarriesItsOwnNameAndVariables(t *testing.T) {
	h := newHarness(t, map[string]string{documentRoute: docList})
	if _, err := h.client.ListPRs(t.Context(), testRef()); err != nil {
		t.Fatalf("ListPRs = %v, want the page", err)
	}
	posted := h.instance.posted()
	if len(posted) != 1 {
		t.Fatalf("ListPRs posted %d document(s), want 1", len(posted))
	}
	var sent struct {
		OperationName string         `json:"operationName"`
		Query         string         `json:"query"`
		Variables     map[string]any `json:"variables"`
	}
	if err := json.Unmarshal([]byte(posted[0]), &sent); err != nil {
		t.Fatalf("the posted body is not a GraphQL request: %v", err)
	}
	if sent.OperationName != prList.name {
		t.Errorf("the posted body names operation %q, want %q", sent.OperationName, prList.name)
	}
	if sent.Query != prList.text {
		t.Error("the posted query is not the document this library ships, so what executes and what the assertions above hold are two different texts")
	}
	if got := sent.Variables["fullPath"]; got != testSelector {
		t.Errorf("the posted variables carry fullPath %v, want the UNENCODED selector %q: this transport takes the path as a variable and encodes nothing", got, testSelector)
	}
	if got := sent.Variables["state"]; got != stateOpened {
		t.Errorf("the posted variables carry state %v, want %q", got, stateOpened)
	}
	if got, ok := sent.Variables["after"]; ok && got != nil {
		t.Errorf("the posted variables carry after %v on a first page, want null: the empty string is a position upstream refuses", got)
	}
}

// TestTheDocumentsCostIsPublishedAsTheProductsOwnFigure holds the one place this
// family's price is not the request count: the endpoint answers a complexity score and
// the expectation table prices a document by it, where a REST request costs one.
//
// The ceiling is asserted rather than the figure: a document whose cost
// drops upstream keeps passing, and a caller raising the page size does not turn a
// measurement into a failure here.
func TestTheDocumentsCostIsPublishedAsTheProductsOwnFigure(t *testing.T) {
	h := newHarness(t, map[string]string{documentRoute: docList})
	if _, err := h.client.ListPRs(t.Context(), testRef()); err != nil {
		t.Fatalf("ListPRs = %v, want the page", err)
	}
	cost := h.client.BudgetState().LastCost
	if cost <= 0 {
		t.Fatalf("ListPRs published a last cost of %d, want the complexity the endpoint charged: this product bills a document by that figure rather than by the one request it took", cost)
	}
	if cost > documentCeiling {
		t.Errorf("ListPRs published a last cost of %d, over the ceiling of %d at a page of %d", cost, documentCeiling, documentPageSize)
	}
	if cost == 1 {
		t.Error("ListPRs published a last cost of 1, which is the REQUEST count rather than the complexity: the two figures differ on this product and the table publishes the complexity")
	}
}

// TestAnEnvelopeCarryingErrorsAndNoDataIsAFailure holds the classification rule for the
// three shapes of that answer, all of which arrive at HTTP 200: nothing about the status
// classifies them, so an implementation reading the status alone reports a success and
// hands the caller a zero value.
//
// The first two omit the data member; the third spells it JSON null, which is a
// DIFFERENT byte string and the one a length test over the raw member gets wrong. There
// the misreading is not a zero value with no error but a zero value carrying the PARTIAL
// marker, which a consumer renders as a truncated answer rather than as the refusal the
// instance sent.
func TestAnEnvelopeCarryingErrorsAndNoDataIsAFailure(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		kind string
	}{
		{name: "an_unknown_field", body: docUnknownField, kind: "undefinedField"},
		{name: "a_complexity_refusal", body: docTooComplex, kind: "no-extensions"},
		{name: "a_data_member_spelled_json_null", body: docNullData, kind: "no-extensions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// The classifier is driven directly rather than through an
			// operation, because every operation that sends a document falls
			// back to REST on a refusal, so a case run through one would be
			// asserting the FALLBACK rather than the classification. The
			// fallback has its own case below.
			h := newHarness(t, map[string]string{documentRoute: test.body})
			var payload docPayload
			_, err := h.client.execute(t.Context(), "ListPRs", prList, map[string]any{}, &payload)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) {
				t.Fatalf("a document answering errors and no data = %v, want a *forgeapi.Error: every shape of that answer arrives at 200", err)
			}
			if fe.Kind != forgeapi.KindUpstream {
				t.Errorf("the refusal = kind %v, want %v: no measured shape of this product's envelope error maps to another, so none is invented", fe.Kind, forgeapi.KindUpstream)
			}
			if fe.Status != http.StatusOK {
				t.Errorf("the refusal = status %d, want %d: the real status is carried always, and this refusal's real status is a success", fe.Status, http.StatusOK)
			}
			if !strings.Contains(fe.Message, prList.name) {
				t.Errorf("the refusal message %q does not name the document that was refused", fe.Message)
			}
			if h.spy.times("EnvelopeError:"+test.kind) != 1 {
				t.Errorf("no envelope counter fired for %q, so the shape that arrived is invisible to a maintainer", test.kind)
			}
			if !h.client.isDegraded() {
				t.Error("a document this instance refused left the connection undegraded, so every later call re-pays the refusal")
			}
		})
	}
}

// TestPartialDataArrivesWithItsMarkerRatherThanTruncated holds the third envelope
// shape: data present AND errors non-empty, which is neither a failure nor a complete
// answer. Returning the data silently would publish a truncated list as a whole one,
// and failing outright would discard rows the instance did answer.
func TestPartialDataArrivesWithItsMarkerRatherThanTruncated(t *testing.T) {
	h := newHarness(t, map[string]string{documentRoute: docPartial})
	page, err := h.client.ListPRs(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListPRs against partial data = %v, want the rows plus a marker", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListPRs = %d row(s), want 1: the data the instance did answer is returned rather than discarded", len(page.Items))
	}
	if page.Partial == nil {
		t.Fatal("ListPRs answered partial data with no marker, so a consumer reads a truncated list as a whole one")
	}
	if page.Partial.Reason != forgeapi.PartialGraphQLPartial {
		t.Errorf("ListPRs = partial reason %v, want %v", page.Partial.Reason, forgeapi.PartialGraphQLPartial)
	}
	if h.spy.times("PartialResult:graphql_partial") != 1 {
		t.Error("a partial answer fired no counter, so a partial no consumer's metrics can see")
	}
	if h.client.isDegraded() {
		t.Error("partial data degraded the connection to REST, which it must not: the document answered, so every later call would pay a fallback for a call that worked")
	}
}

// TestADocumentRefusedDegradesTheConnectionToREST holds the price range the expectation
// table publishes for the single read, and the mechanism under it.
//
// The call that DISCOVERS the refusal spends the refused document plus the REST read,
// which is two requests, and every later call on that connection spends one. That is
// why the published price is a range rather than an exact figure, and it is per
// CONNECTION and per runtime failure rather than per version.
// TestTheSchemaQuestionAtSetupSendsEveryReadToREST holds WHERE this connection's
// document acceptance is settled, which is what lets every operation publish an exact
// figure: the question is asked once, at connection setup, priced on the connection's
// own row, and a connection whose schema refuses it reads REST from its FIRST call
// rather than from the call that happened to discover the refusal.
func TestTheSchemaQuestionAtSetupSendsEveryReadToREST(t *testing.T) {
	h := newHarness(t, map[string]string{
		metadataRoute: metadataBody,
		documentRoute: docTooComplex,
		readRoute:     restMergeRequestBody,
	})
	h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
	before := h.instance.count()
	if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
		t.Fatalf("ConnectionCaps = %v, want the capabilities: a schema question the instance refused is an answer rather than a failure", err)
	}
	if spent := h.instance.count() - before; spent != 2 {
		t.Errorf("setup sent %d request(s), want 2: the metadata read and the one bounded schema question, both priced on the connection's own row", spent)
	}
	if !h.client.isDegraded() {
		t.Fatal("the refused schema question left the connection undegraded, so the first document read would discover it and spend a request its row does not price")
	}
	before = h.instance.count()
	item, err := h.client.ReadPR(t.Context(), testRef(), testPR())
	if err != nil {
		t.Fatalf("ReadPR on a connection whose documents are refused = %v, want the REST answer", err)
	}
	if spent := h.instance.count() - before; spent != 1 {
		t.Errorf("ReadPR sent %d request(s), want 1: the discovery is the connection's cost and this call's row publishes one", spent)
	}
	if item.Ref.Number != 1 || item.Ref.Sigil != sigil {
		t.Errorf("ReadPR = %+v, want the merge request the REST read answered", item.Ref)
	}
	// The two fields the degraded arm must not trust. This product documents that
	// its merge-request reads can serve a status nothing refreshed, and the
	// parameter that asks for a recalculation is refusable by role, so a value read
	// here is indistinguishable from a fresh one.
	if item.Action.MergeBlocked != forgeapi.MergeBlockUnknown {
		t.Errorf("the degraded ReadPR = block reason %v, want %v: the REST answer carries a status this product may not have refreshed",
			item.Action.MergeBlocked, forgeapi.MergeBlockUnknown)
	}
	if item.Action.AutoMergeArmed != forgeapi.SupportUnknown {
		t.Errorf("the degraded ReadPR = auto-merge %v, want %v", item.Action.AutoMergeArmed, forgeapi.SupportUnknown)
	}
	before = h.instance.count()
	if _, err := h.client.ReadPR(t.Context(), testRef(), testPR()); err != nil {
		t.Fatalf("the second ReadPR = %v, want the REST answer", err)
	}
	if spent := h.instance.count() - before; spent != 1 {
		t.Errorf("the second ReadPR sent %d request(s), want 1: the degradation is held with the connection, so no later call re-pays the refused document", spent)
	}
	for _, sent := range h.instance.arrived()[2:] {
		if sent == documentRoute {
			t.Error("a call after setup posted a document again, so every call on this connection pays a refusal setup already settled")
		}
	}
}

// TestADocumentRefusedAtRuntimeIsThatCallsFailureAndTheConnectionsRecord holds the
// other half of the same rule, which is the one an exact price turns on: a refusal
// the setup question did not predict is reported by the call that met it and marks
// the connection, rather than that one call buying a second request to answer from.
//
// A call that fell back inside itself would spend two where its row publishes one,
// and no fixture and no suite could see it, because the fall-back answers correctly.
// What a consumer gets instead is one failure and a connection that reads REST from
// the next call on.
func TestADocumentRefusedAtRuntimeIsThatCallsFailureAndTheConnectionsRecord(t *testing.T) {
	h := newHarness(t, map[string]string{
		documentRoute: docTooComplex,
		readRoute:     restMergeRequestBody,
	})
	before := h.instance.count()
	if _, err := h.client.ReadPR(t.Context(), testRef(), testPR()); err == nil {
		t.Fatal("ReadPR against a refused document = nil, want the refusal: the setup question was never asked on this connection, so this call sent the document")
	}
	if spent := h.instance.count() - before; spent != 1 {
		t.Errorf("the refused ReadPR sent %d request(s), want 1: a call that answered from a second request would spend two where its row publishes one", spent)
	}
	if !h.client.isDegraded() {
		t.Fatal("the refused document left the connection undegraded, so every later read would pay the same refusal again")
	}
	before = h.instance.count()
	item, err := h.client.ReadPR(t.Context(), testRef(), testPR())
	if err != nil {
		t.Fatalf("the ReadPR after the refusal = %v, want the REST answer", err)
	}
	if spent := h.instance.count() - before; spent != 1 {
		t.Errorf("the ReadPR after the refusal sent %d request(s), want 1: the connection reads REST from here on", spent)
	}
	if item.Ref.Number != 1 {
		t.Errorf("the ReadPR after the refusal = %+v, want the merge request the REST read answered", item.Ref)
	}
}

// TestTheRecordOfARefusedDocumentNamesTheDocumentArm holds the one line an operator
// reads to learn WHY a connection degraded. This product is the only one in scope
// with two transports, so the record's transport attribute has to be the exchange's
// own: a constant there reports the refusal of a POSTed document as a GET on REST,
// which is the arm the connection fell BACK to rather than the arm that failed.
//
// The refusal line is the one that carries the diagnostic id, so it is the line the
// id on a user-visible error leads a maintainer to, and it is the one that must
// describe the exchange it happened on rather than a default.
func TestTheRecordOfARefusedDocumentNamesTheDocumentArm(t *testing.T) {
	h := newHarness(t, map[string]string{
		documentRoute: docTooComplex,
		readRoute:     restMergeRequestBody,
	})
	if _, err := h.client.ReadPR(t.Context(), testRef(), testPR()); err == nil {
		t.Fatal("the ReadPR that met the refusal = nil, want the refusal it records")
	}
	if _, err := h.client.ReadPR(t.Context(), testRef(), testPR()); err != nil {
		t.Fatalf("the ReadPR after the refusal = %v, want the REST answer, which is the line the fall-back half reads", err)
	}
	refusal, ok := h.line("kind=upstream")
	if !ok {
		t.Fatalf("no line records the refused document; the client emitted %s", h.logs.String())
	}
	for _, want := range []string{"transport=graphql", "method=POST"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("the refused document was recorded as %s, want %s: a maintainer reading this line learns which arm failed from it and nowhere else", refusal, want)
		}
	}
	fallback, ok := h.line("method=GET")
	if !ok {
		t.Fatalf("no line records the REST read the degradation fell back to; the client emitted %s", h.logs.String())
	}
	if !strings.Contains(fallback, "transport=rest") {
		t.Errorf("the REST fallback was recorded as %s, want transport=rest", fallback)
	}
}

// TestTheRecordOfACapabilityRefusalNamesNoExchange holds the other half of the same
// attribute pair: this refusal sends NOTHING, so a method and an arm on its line
// describe a request that never happened and send whoever reads it looking for one.
func TestTheRecordOfACapabilityRefusalNamesNoExchange(t *testing.T) {
	h := newHarness(t, map[string]string{
		metadataRoute: `{"message":"401 Unauthorized"}`,
		documentRoute: docAcceptanceAnswer,
	})
	h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
	if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
		t.Fatalf("ConnectionCaps = %v, want the capabilities with the re-run unknown", err)
	}
	if err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA); err == nil {
		t.Fatal("RerunFailedChecks on an unresolved capability = nil, want the refusal")
	}
	refusal, ok := h.line("code=" + forgeapi.CodeCapabilityUnsupported)
	if !ok {
		t.Fatalf("no line records the capability refusal; the client emitted %s", h.logs.String())
	}
	for _, unwanted := range []string{"method=GET", "method=POST", "transport=rest", "transport=graphql"} {
		if strings.Contains(refusal, unwanted) {
			t.Errorf("the capability refusal was recorded as %s, which names %s for an exchange that never happened", refusal, unwanted)
		}
	}
}

// TestTheListOnARefusedConnectionIsOneRequestPerPage holds the same rule on the LIST,
// which is where the price was easiest to publish wrongly: all three of this family's
// document readers could be the call that discovered a refusal, so the arm sat on each
// of their rows. Setup takes the discovery, and what is left on each row is one
// request.
//
// The REST list answers rows rather than blanking, and the two fields it must not trust
// are unknown there for the reason the single read's are: this product documents that
// listing merge requests may not proactively refresh the merge status.
func TestTheListOnARefusedConnectionIsOneRequestPerPage(t *testing.T) {
	h := newHarness(t, map[string]string{
		metadataRoute: metadataBody,
		documentRoute: docTooComplex,
		listRoute:     "[" + restMergeRequestBody + "]",
	})
	h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
	if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
		t.Fatalf("ConnectionCaps = %v, want the capabilities", err)
	}
	before := h.instance.count()
	page, err := h.client.ListPRs(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListPRs on a connection whose documents are refused = %v, want the REST page", err)
	}
	if spent := h.instance.count() - before; spent != 1 {
		t.Errorf("ListPRs sent %d request(s), want 1 per page: the refusal was settled at setup and priced there", spent)
	}
	if len(page.Items) != 1 {
		t.Fatalf("the REST ListPRs answered %d row(s), want 1: the fallback answers the list rather than blanking it", len(page.Items))
	}
	row := page.Items[0]
	if row.Action.MergeBlocked != forgeapi.MergeBlockUnknown {
		t.Errorf("the degraded row = block reason %v, want %v: this product's list can serve a status nothing refreshed",
			row.Action.MergeBlocked, forgeapi.MergeBlockUnknown)
	}
	if row.Action.AutoMergeArmed != forgeapi.SupportUnknown {
		t.Errorf("the degraded row = auto-merge %v, want %v", row.Action.AutoMergeArmed, forgeapi.SupportUnknown)
	}
	before = h.instance.count()
	if _, err := h.client.ListPRs(t.Context(), testRef()); err != nil {
		t.Fatalf("the second ListPRs = %v, want the REST page", err)
	}
	if spent := h.instance.count() - before; spent != 1 {
		t.Errorf("the second ListPRs sent %d request(s), want 1: the degradation is held with the connection", spent)
	}
}

// TestTheMergeStateReadIsOneRequestOnBothOfItsArms holds the figure on the operation
// whose row states it most strictly: the merge-state read is exactly one request
// everywhere, and this product is the one whose documents an instance can refuse.
//
// Both arms are measured here, on one connection, because the claim is that they
// agree: the document while the connection reads documents, the REST record after the
// setup question settled that it does not, and one request either way. The range this
// row carried was the discovering call's, and the discovery now has a row of its own.
func TestTheMergeStateReadIsOneRequestOnBothOfItsArms(t *testing.T) {
	h := newHarness(t, map[string]string{
		metadataRoute: metadataBody,
		documentRoute: docRead,
		readRoute:     restMergeRequestBody,
	})
	h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
	before := h.instance.count()
	status, err := h.client.MergeStatus(t.Context(), testRef(), testPR())
	if err != nil {
		t.Fatalf("MergeStatus over the document = %v, want the answer", err)
	}
	if spent := h.instance.count() - before; spent != 1 {
		t.Errorf("MergeStatus over the document sent %d request(s), want 1", spent)
	}
	if status.Merged != forgeapi.SupportNo {
		t.Errorf("MergeStatus = merged %v, want %v", status.Merged, forgeapi.SupportNo)
	}
	h.instance.serve(documentRoute, docTooComplex)
	if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
		t.Fatalf("ConnectionCaps = %v, want the capabilities", err)
	}
	if !h.client.isDegraded() {
		t.Fatal("the refused schema question left the connection reading documents")
	}
	before = h.instance.count()
	status, err = h.client.MergeStatus(t.Context(), testRef(), testPR())
	if err != nil {
		t.Fatalf("MergeStatus over REST = %v, want the answer", err)
	}
	if spent := h.instance.count() - before; spent != 1 {
		t.Errorf("MergeStatus over REST sent %d request(s), want 1: the same figure the document arm spends, which is what the row publishes", spent)
	}
	if status.Merged != forgeapi.SupportNo {
		t.Errorf("the REST MergeStatus = merged %v, want %v: the record carries the state and the merged timestamp this product derives it from",
			status.Merged, forgeapi.SupportNo)
	}
	if status.Queue != forgeapi.QueueNone {
		t.Errorf("the REST MergeStatus = queue %v, want %v", status.Queue, forgeapi.QueueNone)
	}
}

// TestACredentialFailureDoesNotDegradeTheConnection holds the other half of the
// degradation rule, and it is the half an over-broad reading gets wrong: a 401 is the
// credential rather than the document, so a connection that meets one would answer the
// document on the next call and must not spend the rest of its life on REST.
func TestACredentialFailureDoesNotDegradeTheConnection(t *testing.T) {
	h := newHarness(t, map[string]string{documentRoute: `{"message":"401 Unauthorized"}`})
	h.instance.status(documentRoute, http.StatusUnauthorized)
	if _, err := h.client.ReadPR(t.Context(), testRef(), testPR()); err == nil {
		t.Fatal("ReadPR against a 401 = nil, want the refusal")
	}
	if h.client.isDegraded() {
		t.Error("a credential failure degraded the connection, so a client whose token is fixed keeps paying a REST fallback for a document that works")
	}
	h.instance.status(documentRoute, 0)
	h.instance.serve(documentRoute, docRead)
	before := h.instance.count()
	if _, err := h.client.ReadPR(t.Context(), testRef(), testPR()); err != nil {
		t.Fatalf("ReadPR after the credential was accepted = %v, want the document's answer", err)
	}
	if spent := h.instance.count() - before; spent != 1 {
		t.Errorf("ReadPR after the credential was accepted sent %d request(s), want 1", spent)
	}
}

// TestTheAbsenceSplitComesFromTheEnvelopeRatherThanTheStatus holds the one thing the
// document buys this operation that no REST status can: the two absences are different
// answers with different remedies, and the envelope separates them at no extra request.
//
// Measured on gitlab.com: an unresolvable project answers 200 with a null project and
// NO errors array, and a resolved project with no such merge request answers 200 with a
// null merge request. Reading the status alone reports a success for both.
func TestTheAbsenceSplitComesFromTheEnvelopeRatherThanTheStatus(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		code string
	}{
		{name: "a_null_project_names_the_project", body: docNullProject, code: forgeapi.CodeRepoOrPRNotVisible},
		{name: "a_null_merge_request_names_the_merge_request", body: docNullMergeRequest, code: forgeapi.CodePRNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: test.body})
			_, err := h.client.ReadPR(t.Context(), testRef(), testPR())
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) {
				t.Fatalf("ReadPR = %v, want a *forgeapi.Error: this answer arrives at 200", err)
			}
			if fe.Code != test.code {
				t.Errorf("ReadPR = code %q, want %q", fe.Code, test.code)
			}
			if fe.Kind != forgeapi.KindNotFound {
				t.Errorf("ReadPR = kind %v, want %v", fe.Kind, forgeapi.KindNotFound)
			}
			if h.client.isDegraded() {
				t.Error("an absence degraded the connection, which it must not: the document answered the question asked")
			}
		})
	}
}

// TestTheListDocumentsContinuationCrossesTheSurfaceEncoded holds the continuation this
// product's own cursor cannot cross as: it is base64 of a JSON object, whose standard
// alphabet carries bytes this library's own validator refuses, so handing it over raw
// would publish a cursor the next call rejects.
func TestTheListDocumentsContinuationCrossesTheSurfaceEncoded(t *testing.T) {
	raw := `eyJjcmVhdGVkX2F0IjoiMjAyNi0wOS0xOCArMDA6MDAiLCJpZCI6IjUzNC82MzAifQ==`
	call := listCall(t, newHarness(t, nil).client, "ListPRs", testRef())
	cursor := encodeAfter(call, raw)
	if err := forgeapi.ValidateCursor(cursor); err != nil {
		t.Fatalf("ValidateCursor(%q) = %v, want nil: a continuation this library mints has to survive its own validator", cursor, err)
	}
	back, err := decodeAfter(call, cursor)
	if err != nil {
		t.Fatalf("decodeAfter(%q) = %v, want the upstream cursor", cursor, err)
	}
	if back != raw {
		t.Errorf("decodeAfter(encodeAfter(%q)) = %q, want the round trip", raw, back)
	}
	if encodeAfter(call, "") != "" {
		t.Error("encodeAfter minted a continuation for a complete list, which a consumer would follow to a page that does not exist")
	}
	// A continuation minted by the REST list is refused HERE rather than resumed
	// from the head, because the transport changed under the caller and the position
	// it holds is an index into an order this arm never served.
	if _, err := decodeAfter(call, forgeapi.Cursor(cursorPrefix+"2")); err == nil {
		t.Error("decodeAfter accepted a page-numbered continuation, so a caller whose connection degraded and recovered silently restarts the list")
	}
}

// TestADocumentContinuationNamingNoPositionIsRefused holds the decoder to the position
// a document continuation crosses with, since it arrives at a consumer's route as
// untrusted input: one naming no position, or one that is not base64url, is refused
// with the continuation code rather than read as the first page.
func TestADocumentContinuationNamingNoPositionIsRefused(t *testing.T) {
	call := listCall(t, newHarness(t, nil).client, "ListPRs", testRef())
	minted := string(encodeAfter(call, "eyJpZCI6IjEifQ=="))
	digest := minted[strings.LastIndex(minted, ".")+1:]
	for name, c := range map[string]string{
		"no_position":      documentPrefix + "." + digest,
		"position_not_b64": documentPrefix + "A." + digest,
		"no_digest":        documentPrefix + "ZXlKcFpDSTZJakVpZlE9PQ",
		"another_digest":   documentPrefix + "ZXlKcFpDSTZJakVpZlE9PQ." + strings.Repeat("A", len(digest)),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeAfter(call, forgeapi.Cursor(c))
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
				t.Errorf("decodeAfter(%q) = %v, want code %q", c, err, forgeapi.CodeCursorInvalid)
			}
		})
	}
}

// TestAnotherCallsDocumentContinuationIsRefusedWhereTheBoundAdmitsNoPage holds the
// refusal ahead of the page cap: a list whose bound admits no page hands the caller's
// continuation back as the remainder, so one another repository's list minted is
// refused there as on any page, before any request.
func TestAnotherCallsDocumentContinuationIsRefusedWhereTheBoundAdmitsNoPage(t *testing.T) {
	h := newHarness(t, nil, forgeapi.WithListPages(0))
	other := testRef()
	other.Selector += "-other"
	other.ID = other.Encode()
	minted := encodeAfter(listCall(t, h.client, "ListPRs", other), "eyJpZCI6IjEifQ==")
	_, err := h.client.ListPRs(t.Context(), testRef(), forgeapi.WithAfter(minted))
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
		t.Errorf("ListPRs under a zero page cap handed %q = %v, want code %q", minted, err, forgeapi.CodeCursorInvalid)
	}
	if got := h.instance.count(); got != 0 {
		t.Errorf("ListPRs under a zero page cap sent %d request(s), want 0", got)
	}
}

// TestTheListDocumentsCursorIsSentAndReturned holds the two ends of the continuation
// against one call: the cursor the answer carries comes back encoded, and the one the
// caller hands in goes out decoded as the variable the document declares.
func TestTheListDocumentsCursorIsSentAndReturned(t *testing.T) {
	withNext := strings.Replace(docList,
		`"pageInfo": {"hasNextPage": false, "endCursor": null}`,
		`"pageInfo": {"hasNextPage": true, "endCursor": "eyJpZCI6IjEifQ=="}`, 1)
	h := newHarness(t, map[string]string{documentRoute: withNext})
	page, err := h.client.ListPRs(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListPRs = %v, want the page", err)
	}
	if page.Next == "" {
		t.Fatal("ListPRs answered a page whose own page info says there is more and minted no continuation, so the remainder is unreachable")
	}
	if _, err := h.client.ListPRs(t.Context(), testRef(), forgeapi.WithAfter(page.Next)); err != nil {
		t.Fatalf("ListPRs resumed from its own continuation = %v, want the page", err)
	}
	posted := h.instance.posted()
	var sent struct {
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal([]byte(posted[len(posted)-1]), &sent); err != nil {
		t.Fatalf("the resumed body is not a GraphQL request: %v", err)
	}
	if got := sent.Variables["after"]; got != "eyJpZCI6IjEifQ==" {
		t.Errorf("the resumed document carries after %v, want the upstream cursor decoded back out of the continuation", got)
	}
}

// TestADocumentRefusalCarriesNoPartOfTheCredential holds an envelope error's message
// to what the request carried: the instance's text reaches the caller, and the token
// it echoes does not.
func TestADocumentRefusalCarriesNoPartOfTheCredential(t *testing.T) {
	h := newHarness(t, map[string]string{
		documentRoute: `{"data": null, "errors": [{"message": "cannot use credential Bearer ` + testToken + `"}]}`,
	})
	var payload docPayload
	_, err := h.client.execute(t.Context(), "ListPRs", prList, map[string]any{}, &payload)
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("execute(a refused document echoing the token) = %v, want a *forgeapi.Error", err)
	}
	for _, text := range []string{fe.Message, fe.Error()} {
		if strings.Contains(text, testToken) {
			t.Errorf("execute(a refused document echoing the token) = %q, want no %q", text, testToken)
		}
	}
	if !strings.Contains(fe.Message, "cannot use credential") {
		t.Errorf("execute(a refused document) = message %q, want the instance's own words", fe.Message)
	}
}
