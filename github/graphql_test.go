package github

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// documentCost is the cost every document this family ships is asserted against, as a
// CEILING at a stated page size rather than as an equality: a document whose cost
// drops upstream keeps passing, and a caller raising the page size does not turn a
// measurement into a failure.
//
// Measured on api.github.com: at a page of twenty both pull-request documents and the
// cross-repository search charged one, and at a page of a hundred the list document
// and the search charged three, which is where this ceiling comes from. The node
// count tracks what a document REQUESTS rather than what came back, so it is not a
// cost: the list document charged three at 2,300 requested nodes and at 10,300.
const documentCost = 3

// documentPageSize is the page size that ceiling is stated at.
const documentPageSize = 100

// documents is every document this family ships, which is what the document
// assertions iterate.
var documents = []document{prList, prRead, prMine, issueMine, commitRollup, enableAutoMerge}

// TestEveryDocumentThisFamilyShipsSelectsWhatItIsPricedOn holds every document to
// the selection their prices and their normalizers both rest on.
//
// The budget object is the one every query document must carry, because that is where
// this product reports what the document cost, and a document that stopped selecting
// it would leave the last call's cost at the request count rather than the billed one.
//
// A mutation is the exception the schema makes (ADR-0103): the Mutation root has no
// rate-limit field, which the endpoint answers as an undefined field, so the arm
// document opens a named mutation and selects no budget object.
func TestEveryDocumentThisFamilyShipsSelectsWhatItIsPricedOn(t *testing.T) {
	if len(documents) != 6 {
		t.Errorf("this family ships %d document(s), want 6: the pull-request list, the single read, the two cross-repository searches, the commit rollup and the auto-merge arm", len(documents))
	}
	for _, doc := range documents {
		t.Run(doc.name, func(t *testing.T) {
			kind := "query "
			if doc.mutation {
				kind = "mutation "
			}
			if !strings.Contains(doc.text, kind+doc.name+"(") && !strings.Contains(doc.text, kind+doc.name+" {") {
				t.Errorf("the %s document's text does not open a named %soperation called %s, which is what the request's operationName names", doc.name, kind, doc.name)
			}
			selects := strings.Contains(doc.text, "rateLimit { cost limit nodeCount remaining resetAt used }")
			if selects == doc.mutation {
				t.Errorf("the %s document (mutation %t) selects a rate-limit object: %t, want it on every query and on no mutation", doc.name, doc.mutation, selects)
			}
		})
	}
}

// TestThePullRequestDocumentsSelectTheFieldsTheNormalizerReads holds the three
// documents that carry a pull request to the fields the contract's action state is
// filled from. A selection that drops one of these decodes a zero value with no error,
// which is the defect this case exists for.
func TestThePullRequestDocumentsSelectTheFieldsTheNormalizerReads(t *testing.T) {
	want := []string{
		"headRefOid",
		"mergeable",
		"mergeStateStatus",
		"merged",
		"autoMergeRequest { enabledAt mergeMethod }",
		"mergeQueueEntry { state position }",
		"statusCheckRollup",
		"checkRunCountsByState { state count }",
		"statusContextCountsByState { state count }",
		"... on StatusContext { context description targetUrl state }",
		"... on CheckRun { name conclusion status detailsUrl }",
	}
	for _, doc := range []document{prList, prRead, prMine} {
		t.Run(doc.name, func(t *testing.T) {
			for _, field := range want {
				if !strings.Contains(doc.text, field) {
					t.Errorf("the %s document does not select %q, which the normalizer reads", doc.name, field)
				}
			}
		})
	}
}

// TestTheTwoFoldPageSizesAreTheRuleTheyPublish holds the one difference between the
// list documents' rollup selection and the single read's, which is the bounded-fold
// rule itself: a list asks for ONE context beside the whole-collection counts, measured
// at 38,737 bytes against 533,301 for a hundred at the same request cost, and the read
// that returns the check NAMES asks for the maximum page and takes a cursor.
func TestTheTwoFoldPageSizesAreTheRuleTheyPublish(t *testing.T) {
	if listFoldPageSize != 1 {
		t.Errorf("listFoldPageSize = %d, want 1: the counts make a list's verdict exact from any page, so the page is there for the names a row does not publish", listFoldPageSize)
	}
	for _, doc := range []document{prList, prMine} {
		if !strings.Contains(doc.text, "contexts(first: 1)") {
			t.Errorf("the %s document does not bound its fold to one context, want it: a list following those pages is the fan-out the budget refuses", doc.name)
		}
		if strings.Contains(doc.text, "checksAfter") {
			t.Errorf("the %s document takes a contexts cursor, want none: a list's fold is bounded, so there is no page for it to follow", doc.name)
		}
	}
	for _, doc := range []document{prRead, commitRollup} {
		if !strings.Contains(doc.text, "contexts(first: 100, after: $checksAfter)") {
			t.Errorf("the %s document does not take the maximum contexts page and a cursor, want both: its fold is complete and follows pages up to the published bound", doc.name)
		}
	}
}

// TestTheCommitDocumentResolvesACommitRatherThanAPullRequest holds the one thing that
// makes the commit rollup a document of its own: the pull-request read resolves a pull
// request and this one resolves a commit, so neither selection reaches the other's
// subject.
//
// It resolves it by EXPRESSION rather than by object id, and that is what the published
// parameter requires rather than a preference: the operation takes a REF, a branch name
// is what a caller reading a repository view has, and the id-typed variable refuses one
// outright. The live lane caught exactly that as a document refused for its variable's
// type and a connection degraded to REST from then on, so this row is the one the
// measurement moved.
func TestTheCommitDocumentResolvesACommitRatherThanAPullRequest(t *testing.T) {
	if !strings.Contains(commitRollup.text, "object(expression: $ref)") {
		t.Error("the CommitRollup document does not resolve a commit by expression, which is what the ref this operation publishes needs: an object-id variable refuses a branch name")
	}
	if strings.Contains(commitRollup.text, "GitObjectID") {
		t.Error("the CommitRollup document declares an object-id variable, which cannot take the branch name this operation's ref may be")
	}
	if strings.Contains(commitRollup.text, "pullRequest(") {
		t.Error("the CommitRollup document resolves a pull request, want none: a caller of the folded status named a commit")
	}
	if !strings.Contains(commitRollup.text, "... on Commit") {
		t.Error("the CommitRollup document does not narrow its object selection to a commit, so a tag or a tree at that id would decode nothing")
	}
}

// TestTheCrossRepositoryDocumentCarriesEachRowsOwnRepository holds what makes that
// operation's rows actionable: the call addresses no repository, so every row has to
// carry its own, and the query is scoped by the CREDENTIAL rather than by a repository,
// which is what keeps the price independent of how many repositories it can reach.
func TestTheCrossRepositoryDocumentCarriesEachRowsOwnRepository(t *testing.T) {
	for _, doc := range []document{prMine, issueMine} {
		if !strings.Contains(doc.text, "repository { nameWithOwner }") {
			t.Errorf("the %s document does not select each row's repository, so a consumer could not act on a row", doc.name)
		}
		if !strings.Contains(doc.text, "search(query: $q, type: ISSUE") {
			t.Errorf("the %s document is not a search, want one: no repository-addressed selection reaches rows from repositories the call never names", doc.name)
		}
	}
	if !strings.Contains(issueMine.text, "... on Issue {") {
		t.Error("the IssueMine document does not narrow its rows to issues, so its nodes would decode nothing")
	}
}

// TestTheSearchQueryNamesOneScopeAndTheOpenState holds the query both
// cross-repository documents send to the scope it was asked for: the author keyword
// for the credential's own items, the owner qualifier in its place under an owner,
// never both, and the open state either way. These are the query strings the
// measurement sent on 2026-10-01.
func TestTheSearchQueryNamesOneScopeAndTheOpenState(t *testing.T) {
	owned := forgeapi.ListSettings{Owner: "example-org", OwnerSet: true}
	for _, test := range []struct {
		itemType string
		want     string
		set      forgeapi.ListSettings
	}{
		{itemType: searchPulls, want: "type:pr author:@me state:open"},
		{itemType: searchIssues, want: "type:issue author:@me state:open"},
		{itemType: searchPulls, set: owned, want: "type:pr user:example-org state:open"},
		{itemType: searchIssues, set: owned, want: "type:issue user:example-org state:open"},
	} {
		if got := searchQuery(test.itemType, test.set); got != test.want {
			t.Errorf("searchQuery(%q, owner %q set %v) = %q, want %q", test.itemType, test.set.Owner, test.set.OwnerSet, got, test.want)
		}
	}
}

// TestTheListDocumentTakesTheStateFilterAsAMemberList holds the one variable whose Go
// type is not the obvious one: this product's list takes a LIST of enumeration members
// rather than one word, and null where the caller asked for all of them.
func TestTheListDocumentTakesTheStateFilterAsAMemberList(t *testing.T) {
	for _, test := range []struct {
		want  any
		state forgeapi.ListState
	}{
		{state: forgeapi.ListStateOpen, want: []string{"OPEN"}},
		{state: forgeapi.ListStateClosed, want: []string{"CLOSED"}},
		{state: forgeapi.ListStateMerged, want: []string{"MERGED"}},
		{state: forgeapi.ListStateUnknown, want: []string{"OPEN"}},
	} {
		got, ok := documentStates(test.state).([]string)
		if !ok || len(got) != 1 || got[0] != test.want.([]string)[0] {
			t.Errorf("documentStates(%v) = %v, want %v", test.state, got, test.want)
		}
	}
	if documentStates(forgeapi.ListStateAll) != nil {
		t.Errorf("documentStates(all) = %v, want nil: the wildcard is the absence of the filter", documentStates(forgeapi.ListStateAll))
	}
}

// TestTheDocumentCursorSurvivesTheSurface holds the continuation this family mints from
// a document's own opaque cursor: it crosses the surface through the validator a
// consumer hands it back through, and it comes back as the bytes upstream sent.
func TestTheDocumentCursorSurvivesTheSurface(t *testing.T) {
	call := listCall(t, newHarness(t, nil).client, "ListPRs", testRef())
	for _, raw := range []string{"Y3Vyc29yOnYyOpHOAAGgsA==", "MTAw", "a/b+c=="} {
		crossed := encodeAfter(call, raw)
		if err := forgeapi.ValidateCursor(crossed); err != nil {
			t.Errorf("ValidateCursor(encodeAfter(%q)) = %v, want nil: a continuation this library mints has to pass its own validator", raw, err)
		}
		back, err := decodeAfter(call, crossed)
		if err != nil || back != raw {
			t.Errorf("decodeAfter(encodeAfter(%q)) = %q, %v, want the cursor back", raw, back, err)
		}
	}
	if got := encodeAfter(call, ""); got != "" {
		t.Errorf("encodeAfter(the first page) = %q, want empty: the absence of a cursor IS the first page", got)
	}
}

// TestACursorFromTheOtherTransportIsRefused holds the one continuation mix-up this
// family can actually meet, because it mints two encodings: a page number for its REST
// lists and the document's own cursor. Resuming from the wrong one would silently serve
// the head of a list the caller thought it was continuing.
func TestACursorFromTheOtherTransportIsRefused(t *testing.T) {
	client := newHarness(t, nil).client
	prs := listCall(t, client, "ListPRs", testRef())
	labels := restCursor(t, client, "ListLabels", testRef(), 2)
	if _, err := decodeAfter(prs, labels); err == nil {
		t.Errorf("decodeAfter(%q) = nil error, want the continuation refusal: that cursor names a REST page", labels)
	}
	docCursor := encodeAfter(prs, "MTAw")
	if _, err := restWalkAt(listCall(t, client, "ListLabels", testRef()), docCursor); err == nil {
		t.Errorf("restWalkAt(%q) = nil error, want the continuation refusal: that cursor names a document position", docCursor)
	}
	var fe *forgeapi.Error
	_, err := decodeAfter(prs, labels)
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
		t.Errorf("decodeAfter(a REST cursor) = %v, want code %q", err, forgeapi.CodeCursorInvalid)
	}
}

// TestADocumentContinuationNamingNoPositionIsRefused holds the decoder to the position
// a document continuation crosses with, since it arrives at a consumer's route as
// untrusted input: one naming no position, or one that is not base64url, is refused
// with the continuation code rather than read as the first page.
func TestADocumentContinuationNamingNoPositionIsRefused(t *testing.T) {
	call := listCall(t, newHarness(t, nil).client, "ListPRs", testRef())
	minted := string(encodeAfter(call, "MTAw"))
	digest := minted[strings.LastIndex(minted, ".")+1:]
	for name, c := range map[string]string{
		"no_position":      documentPrefix + "." + digest,
		"position_not_b64": documentPrefix + "A." + digest,
		"no_digest":        documentPrefix + "TVRBdw",
		"a_further_field":  documentPrefix + "TVRBdw.TVRBdw." + digest,
		"another_digest":   documentPrefix + "TVRBdw." + strings.Repeat("A", len(digest)),
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

// TestAnotherCallsContinuationIsRefusedWhereTheBoundAdmitsNoPage holds the refusal
// ahead of the page cap: a list whose bound admits no page hands the caller's
// continuation back as the remainder, so one another call minted is refused there as
// on any page, before any request.
func TestAnotherCallsContinuationIsRefusedWhereTheBoundAdmitsNoPage(t *testing.T) {
	h := newHarness(t, nil, forgeapi.WithListPages(0))
	other := repoRef(testOwner + "/other")
	position := docPageInfo{HasNextPage: true, EndCursor: "Y3Vyc29yOjE="}
	for name, list := range map[string]func() error{
		"ListPRs_handed_another_repository_walk": func() error {
			_, err := h.client.ListPRs(t.Context(), testRef(), forgeapi.WithAfter(encodeAfter(listCall(t, h.client, "ListPRs", other), position.EndCursor)))
			return err
		},
		"ListMyPRs_handed_the_issue_search_walk": func() error {
			_, err := h.client.ListMyPRs(t.Context(), forgeapi.WithAfter(searchNext(position, listCall(t, h.client, "ListMyIssues", forgeapi.RepoRef{}), 1)))
			return err
		},
		"ListMyIssues_handed_the_pull_request_search_walk": func() error {
			_, err := h.client.ListMyIssues(t.Context(), forgeapi.WithAfter(searchNext(position, listCall(t, h.client, "ListMyPRs", forgeapi.RepoRef{}), 1)))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := list()
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
				t.Errorf("%s under a zero page cap = %v, want code %q", name, err, forgeapi.CodeCursorInvalid)
			}
			if got := h.instance.count(); got != 0 {
				t.Errorf("%s under a zero page cap sent %d request(s), want 0", name, got)
			}
		})
	}
}

// TestTheDocumentEndpointIsDerivedPerProduct holds the two addressings this family
// meets. The hosted instance serves its API on a host of its own, so the document sits
// under that host; an appliance serves a versioned REST root under its own web base and
// the document endpoint sits BESIDE that root rather than under it, so appending would
// address a path no instance serves.
func TestTheDocumentEndpointIsDerivedPerProduct(t *testing.T) {
	for _, test := range []struct {
		web  string
		api  string
		want string
	}{
		{web: "https://github.com", api: "https://api.github.com", want: "https://api.github.com/graphql"},
		{web: "https://www.github.com", api: "https://api.github.com", want: "https://api.github.com/graphql"},
		{web: "https://forge.example", api: "https://forge.example/api/v3", want: "https://forge.example/api/graphql"},
		{web: "http://127.0.0.1:8080", api: "http://127.0.0.1:8080/api/v3", want: "http://127.0.0.1:8080/api/graphql"},
	} {
		t.Run(test.web, func(t *testing.T) {
			web, err := url.Parse(test.web)
			if err != nil {
				t.Fatalf("Setup: parsing %q: %v", test.web, err)
			}
			if got := deriveAPIBase(web); got != test.api {
				t.Errorf("deriveAPIBase(%q) = %q, want %q", test.web, got, test.api)
			}
			api, err := url.Parse(test.api)
			if err != nil {
				t.Fatalf("Setup: parsing %q: %v", test.api, err)
			}
			if got := documentURL(api); got != test.want {
				t.Errorf("documentURL(%q) = %q, want %q", test.api, got, test.want)
			}
		})
	}
}

// TestADocumentRefusedWithNoDataDegradesTheConnection holds the classification rule
// this product needs: an envelope carrying errors and NO data is the document's own
// failure, so it is reported and the connection is marked, where a credential failure
// or a throttle is not the document's and leaves it to be tried again.
func TestADocumentRefusedWithNoDataDegradesTheConnection(t *testing.T) {
	h := newHarness(t, map[string]string{
		documentRoute: `{"errors":[{"type":"UNPROCESSABLE","message":"Field 'teleported' doesn't exist"}],"data":null}`,
	})
	var payload docPayload
	_, err := h.client.execute(t.Context(), "ReadPR", prRead, map[string]any{}, &payload)
	if err == nil {
		t.Fatal("execute(a refused document) = nil error, want the upstream refusal")
	}
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindUpstream {
		t.Errorf("execute(a refused document) = %v, want an upstream failure of this library's own type", err)
	}
	if !h.client.isDegraded() {
		t.Error("the connection is not degraded after a refused document, want it: the two reads with a REST arm take it from their next call")
	}
	if n := h.spy.times("EnvelopeError:UNPROCESSABLE"); n != 1 {
		t.Errorf("the envelope counter fired %d time(s) for the type the product sent, want 1", n)
	}
}

// TestADocumentAnsweringDataBesideErrorsIsPartialRatherThanRefused holds the shape this
// product MEASURED rather than a defensive one: a selection needing context the caller
// cannot supply answers 200 with the scalars populated and one error per nulled node, so
// the data is handed on with the partial marker instead of being thrown away.
func TestADocumentAnsweringDataBesideErrorsIsPartialRatherThanRefused(t *testing.T) {
	h := newHarness(t, map[string]string{
		documentRoute: `{"data":{"rateLimit":{"cost":1},"repository":{"nameWithOwner":"example/example"}},` +
			`"errors":[{"type":"UNPROCESSABLE","message":"A pull request ID or pull request number is required.","path":["repository","object","statusCheckRollup","contexts","nodes",0,"isRequired"]}]}`,
	})
	var payload docPayload
	result, err := h.client.execute(t.Context(), "ListPRs", prList, map[string]any{}, &payload)
	if err != nil {
		t.Fatalf("execute(a partial document) = %v, want the data beside the marker", err)
	}
	if !result.Partial {
		t.Error("execute(a partial document) reported no partial, want one: the errors arrived beside the data")
	}
	if payload.Repository == nil || payload.Repository.NameWithOwner != testSelector {
		t.Errorf("execute(a partial document) decoded %v, want the repository the envelope carried", payload.Repository)
	}
	if h.client.isDegraded() {
		t.Error("the connection is degraded after a PARTIAL document, want it not to be: the document answered")
	}
}

// TestADocumentsErrorPathDecodesAsTheHeterogeneousArrayItIs holds the one union in this
// product's envelope: the error path is a list of strings and integers together,
// measured, so a typed list of either element type fails to decode the other and takes
// the whole envelope with it.
func TestADocumentsErrorPathDecodesAsTheHeterogeneousArrayItIs(t *testing.T) {
	h := newHarness(t, map[string]string{
		documentRoute: `{"data":{"rateLimit":{"cost":1},"repository":{"nameWithOwner":"example/example"}},` +
			`"errors":[{"type":"UNPROCESSABLE","message":"refused","path":["repository","contexts","nodes",0,"isRequired"]}]}`,
	})
	var payload docPayload
	if _, err := h.client.execute(t.Context(), "ListPRs", prList, map[string]any{}, &payload); err != nil {
		t.Fatalf("execute(an envelope whose error path mixes strings and integers) = %v, want nil", err)
	}
}

// TestTheDocumentCostIsWhatTheEndpointCharged holds the figure the governor publishes
// for a document call: this product bills a document by the cost its own budget object
// reports, so the last call's cost is that figure rather than the number of requests.
func TestTheDocumentCostIsWhatTheEndpointCharged(t *testing.T) {
	h := newHarness(t, map[string]string{
		documentRoute: documentEnvelope(`"repository":{"nameWithOwner":"example/example"}`),
	})
	var payload docPayload
	result, err := h.client.execute(t.Context(), "ListPRs", prList, map[string]any{}, &payload)
	if err != nil {
		t.Fatalf("execute = %v, want nil", err)
	}
	if result.Cost != 1 {
		t.Errorf("execute reported a cost of %d, want the 1 the recorded budget object carries", result.Cost)
	}
	if result.Cost > documentCost {
		t.Errorf("execute reported a cost of %d, over this family's own ceiling of %d at a page of %d",
			result.Cost, documentCost, documentPageSize)
	}
	if got := h.client.BudgetState().LastCost; got != 1 {
		t.Errorf("BudgetState().LastCost = %d, want the cost the endpoint charged", got)
	}
}

// labelsLimit is the most labels this product lets one issue or pull request carry.
const labelsLimit = 100

// TestEveryLabelsSelectionReadsTheWholeSet holds each document's labels page to the
// product's per-row limit, which is what makes the page the whole set: a row's labels
// cut short by a smaller page arrive as though complete, with nothing marking the rest.
func TestEveryLabelsSelectionReadsTheWholeSet(t *testing.T) {
	selection := regexp.MustCompile(`labels\(first: (\d+)\)`)
	found := 0
	for _, doc := range documents {
		for _, m := range selection.FindAllStringSubmatch(doc.text, -1) {
			found++
			if m[1] != strconv.Itoa(labelsLimit) {
				t.Errorf("the %s document selects labels(first: %s), want labels(first: %d), the per-row limit", doc.name, m[1], labelsLimit)
			}
		}
	}
	if found == 0 {
		t.Fatal("no document selects a row's labels, want the pull-request and issue rows to: a check over nothing passes for the wrong reason")
	}
}

// TestADocumentRefusalCarriesNoPartOfTheCredential holds an envelope error's message
// to what the request carried: the instance's text reaches the caller, and the token
// it echoes does not.
func TestADocumentRefusalCarriesNoPartOfTheCredential(t *testing.T) {
	h := newHarness(t, map[string]string{
		documentRoute: `{"errors":[{"type":"UNPROCESSABLE","message":"cannot use credential Bearer ` + testToken + `"}],"data":null}`,
	})
	var payload docPayload
	_, err := h.client.execute(t.Context(), "ReadPR", prRead, map[string]any{}, &payload)
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

func TestADocumentAnsweringErrorsWithItsDataMemberOmittedIsRefused(t *testing.T) {
	h := newHarness(t, map[string]string{
		documentRoute: `{"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a Repository"}]}`,
	})
	var payload docPayload
	_, err := h.client.execute(t.Context(), "ReadPR", prRead, map[string]any{}, &payload)
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindUpstream || !strings.Contains(fe.Message, "Could not resolve to a Repository") {
		t.Errorf("execute(errors with no data member) = %v, want the upstream refusal quoting the envelope's error", err)
	}
	if !h.client.isDegraded() {
		t.Error("the connection is not degraded after a refused document, want it")
	}
}

// A credential refusal degrades nothing: the document answers again once the
// credential does.
func TestADocumentRefusedByStatusWithNoEnvelopeErrorsIsThatStatusRefusal(t *testing.T) {
	h := newHarness(t, map[string]string{documentRoute: `{"message":"Bad credentials"}`})
	h.instance.status(documentRoute, http.StatusUnauthorized)
	var payload docPayload
	_, err := h.client.execute(t.Context(), "ReadPR", prRead, map[string]any{}, &payload)
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindUnauthorized || fe.Status != http.StatusUnauthorized {
		t.Errorf("execute(a 401 with no envelope errors) = %v, want the unauthorized refusal at status 401", err)
	}
	if h.client.isDegraded() {
		t.Error("the connection is degraded after a credential refusal, want it not to be")
	}
}

func TestTheDocumentCostReplacesTheCallsRequestCountOnlyWhereTheEndpointReportedOne(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want int
	}{
		{name: "charged_three", want: 3, body: `{"data":{"rateLimit":{"cost":3,"limit":5000,"remaining":4990},"repository":{"nameWithOwner":"example/example"}}}`},
		{name: "no_budget_object", want: 1, body: `{"data":{"repository":{"nameWithOwner":"example/example"}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: test.body})
			var payload docPayload
			if _, err := h.client.execute(t.Context(), "ListPRs", prList, map[string]any{}, &payload); err != nil {
				t.Fatalf("execute = %v, want nil", err)
			}
			if got := h.client.BudgetState().LastCost; got != test.want {
				t.Errorf("BudgetState().LastCost after %s = %d, want %d", test.name, got, test.want)
			}
		})
	}
}
