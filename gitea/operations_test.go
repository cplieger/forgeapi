package gitea

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// The bodies these tests serve, trimmed to the fields the case reads.
const (
	userBody  = `{"login":"example-user","full_name":"Example User"}`
	labelBody = `[{"id":7,"name":"example-label","color":"ededed","description":"Example label."}]`
	issueBody = `{"number":1,"state":"open","title":"Example issue","user":{"login":"example-user"}}`
	statusOK  = `{"sha":"1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d","state":"success","total_count":1,` +
		`"statuses":[{"context":"example/build","status":"success"}]}`
)

// repoRow is one repository record, trimmed to what a listing case reads.
const repoRow = `{"full_name":"example/example","name":"example","owner":{"login":"example"},` +
	`"html_url":"https://forge.example/example/example","default_branch":"main"}`

// statusPath is the route a folded status arrives at for the canonical head.
const statusPath = "GET /api/v1/repos/example/example/commits/" + testHeadSHA + "/status"

// The API document detection reads, trimmed to the two fields it looks at: the title,
// which is the product mark, and the paths, which settle the one capability nothing
// cheaper answers. The titles are the ones both public instances of the family serve,
// "Gitea API" and "Forgejo API"; a document that names no product is no witness at all
// and is spelled here so a case can serve one.
const (
	giteaDoc     = `{"info":{"title":"Gitea API"},"paths":{}}`
	forgejoDoc   = `{"info":{"title":"Forgejo API"},"paths":{}}`
	anonymousDoc = `{"paths":{}}`
)

// interval is the rolling window the rotation cases drive with the step clock. No
// case asserts its LENGTH: what each one needs is the NEXT window, which one step
// reaches, so the figure has only to be longer than what one fold costs, and that
// cost is real time over a real listener however far the clock has been moved.
const interval = time.Minute

// searchRow is one row of GET /repos/issues/search, carrying the field set that
// route was measured to answer rather than the field set a pull-request row has.
//
// Every key here is one the anonymous capture of that route recorded on both public
// instances of this family, and the shape of the two that decide the decode is what
// both swagger documents declare: the row is an Issue, so it carries a pull_request
// object and no head, no base, no mergeable and no merged key, and its repository is
// the meta shape whose owner is a login STRING where a repository record carries a
// user object. A wire type that reads that owner as an object fails the whole
// response, which is the arm this body exists to hold.
// The draft flag is the one field of that set this route places elsewhere, so it is
// a parameter here: both documents declare it on the pull_request object and neither
// declares one on the Issue, so the row's top level carries none at all.
func searchRow(draft bool) string {
	return `{"id":1,"number":1,"state":"open","title":"Example pull request",` +
		`"body":"Example body.","user":{"login":"example-user"},` +
		`"html_url":"https://forge.example/example/example/pull/1",` +
		`"url":"https://forge.example/api/v1/repos/example/example/issues/1",` +
		`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z",` +
		`"closed_at":null,"due_date":null,"is_locked":false,"comments":0,"content_version":1,` +
		`"original_author":"","original_author_id":0,"pin_order":0,"time_estimate":0,"ref":"",` +
		`"assets":[],"assignee":null,"assignees":null,"milestone":null,"projects":null,` +
		`"labels":[{"name":"example-label","color":"ededed","description":"Example label."}],` +
		`"pull_request":{"draft":` + strconv.FormatBool(draft) + `,"merged":false},` +
		`"repository":{"id":1,"name":"example","owner":"example","full_name":"example/example"}}`
}

// combined is one combined-status body: what the ENDPOINT says the fold is, beside
// the rows the fold is actually computed over.
//
// The two are separate arguments because they are separate facts, and every fixture
// in this tree had them agreeing, which is what let a fold that trusts the
// endpoint's own verdict pass everything. The total is deliberately a figure of its
// own for the same reason.
func combined(state string, total int, rows ...string) string {
	return `{"sha":"` + testHeadSHA + `","state":"` + state + `","total_count":` + itoa(total) +
		`,"statuses":[` + strings.Join(rows, ",") + `]}`
}

// statusRow is one commit-status row under the key this product spells it with.
func statusRow(name, state string) string {
	return `{"context":"example/` + name + `","status":"` + state + `"}`
}

// fullStatusPage is a page of exactly the page size the folded read asks for, every
// row passing and the endpoint agreeing. A full page is the only witness of
// truncation this endpoint gives: its own state and total are computed over the page
// it returned, so a fold that trusted either would publish a verdict over part of
// the evidence and no field of the body would contradict it.
func fullStatusPage() string {
	rows := make([]string, 0, testMaxItems)
	for i := range testMaxItems {
		rows = append(rows, statusRow("check"+itoa(i), "success"))
	}
	return combined("success", testMaxItems, rows...)
}

// fullLabelPage is a page of exactly the page size the label resolution asks for,
// carrying none of the names the refusal cases look for. A full page is the only
// witness of truncation this route gives: it carries no next link a read can rely
// on, so a resolution that treated a full page as the whole list would refuse a
// name that exists one row past the bound.
func fullLabelPage() string {
	rows := make([]string, 0, testMaxItems)
	for i := range testMaxItems {
		rows = append(rows, `{"id":`+itoa(100+i)+`,"name":"example-label`+itoa(i)+`","color":"ededed"}`)
	}
	return `[` + strings.Join(rows, ",") + `]`
}

// pullRow is one pull-request row with a head this family can fold.
func pullRow(number int) string { return draftPullRow(number, false) }

// draftPullRow is that row with its draft flag stated, which THIS route carries at
// the row's top level. It is the control on the cross-repository arm: the same flag
// one level up has to keep arriving.
func draftPullRow(number int, draft bool) string {
	return `{"number":` + itoa(number) + `,"state":"open","title":"Row ` + itoa(number) + `",` +
		`"draft":` + strconv.FormatBool(draft) + `,` +
		`"user":{"login":"example-user"},"head":{"ref":"f","sha":"` + testHeadSHA + `"},` +
		`"base":{"ref":"main","repo":{"full_name":"example/example"}}}`
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestEveryReadCarriesTheCredential holds the mandatory credential contract at the
// one place it is observable, the wire.
//
// Injection is mandatory and the option that carries it is a root option, so a
// builder that stopped injecting would leave every private operation failing on a
// real instance while a fixture that does not look at the header answers anyway.
func TestEveryReadCarriesTheCredential(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/user":                         userBody,
		"GET /api/v1/repos/example/example/labels": labelBody,
	})
	if _, err := h.client.Whoami(t.Context()); err != nil {
		t.Fatalf("Whoami = %v, want nil", err)
	}
	if _, err := h.client.ListLabels(t.Context(), testRef()); err != nil {
		t.Fatalf("ListLabels = %v, want nil", err)
	}
	got := h.instance.credentials()
	if len(got) != 2 {
		t.Fatalf("the instance saw %d request(s), want 2: %v", len(got), h.instance.arrived())
	}
	for i, value := range got {
		if want := "Bearer " + testToken; value != want {
			t.Errorf("request %d arrived with Authorization %q, want %q: the credential is mandatory, so a request without it is a client that stopped authenticating", i, value, want)
		}
	}
}

// TestACreationSendsTheLabelIdentifiersRatherThanTheNames holds the one request
// shape a matcher comparing only method and path cannot see.
//
// Both products' creation options take an array of numeric label ids, measured
// against each one's own swagger document, while the published signature carries
// names. So a creation that sent the names would be refused by every instance of
// this family, and the resolution that makes it a request they accept is the
// repository's own label list, which is why the published price carries a second
// request where the caller named labels.
func TestACreationSendsTheLabelIdentifiersRatherThanTheNames(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/example/example/labels":  labelBody,
		"POST /api/v1/repos/example/example/issues": issueBody,
	})
	if _, err := h.client.CreateIssue(t.Context(), testRef(), forgeapi.NewIssue{
		Title:  "Example issue",
		Labels: []string{"example-label"},
	}); err != nil {
		t.Fatalf("CreateIssue = %v, want nil", err)
	}
	body := h.instance.body("POST /api/v1/repos/example/example/issues")
	if !strings.Contains(body, `"labels":[7]`) {
		t.Errorf("CreateIssue sent %s, want a labels array of the identifiers the option takes: this product's field is an array of int64, so names are a request it refuses", body)
	}
	if strings.Contains(body, `"example-label"`) {
		t.Errorf("CreateIssue sent %s, want no label NAME in the body", body)
	}
	if got, want := h.instance.count(), 2; got != want {
		t.Errorf("CreateIssue sent %d request(s), want %d: the label read plus the creation, which is the price the table publishes", got, want)
	}
}

// TestACreationWithNoLabelsCostsOneRequest holds the other arm of that price, so the
// resolution is paid for only where the caller asked for it.
func TestACreationWithNoLabelsCostsOneRequest(t *testing.T) {
	h := newHarness(t, map[string]string{
		"POST /api/v1/repos/example/example/issues": issueBody,
	})
	if _, err := h.client.CreateIssue(t.Context(), testRef(), forgeapi.NewIssue{Title: "Example issue"}); err != nil {
		t.Fatalf("CreateIssue = %v, want nil", err)
	}
	if got, want := h.instance.count(), 1; got != want {
		t.Errorf("CreateIssue with no labels sent %d request(s), want %d", got, want)
	}
	if labels, ok := sentBody(t, h, "POST /api/v1/repos/example/example/issues")["labels"]; ok {
		t.Errorf("CreateIssue with no labels sent labels %v, want no labels field", labels)
	}
}

// TestACreationRefusesALabelTheWholeListItReadDoesNotCarry holds the resolution's
// failure arm over a SHORT page, and holds it to claiming no more than the read
// establishes.
//
// A name with no identifier behind it is refused rather than dropped, because a
// creation that silently lost a label the caller asked for is the worse answer. What
// the refusal may SAY is bounded by what was read: one page at the instance maximum,
// which carries none of an organization's own labels, so a message asserting the
// repository has no such label states a fact that read cannot establish. A page that
// came back short IS the whole list this route can reach, so this arm keeps the code
// it carried before the truncated one existed, which is the rule for this pair.
// Its kind is the upstream one for the same reason the sibling refusal over an
// answered read uses it: nothing here is a merge, and the merge kind would tell a
// caller their pull request is unmergeable because an issue's label name did not
// resolve.
func TestACreationRefusesALabelTheWholeListItReadDoesNotCarry(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/example/example/labels":  labelBody,
		"POST /api/v1/repos/example/example/issues": issueBody,
	})
	_, err := h.client.CreateIssue(t.Context(), testRef(), forgeapi.NewIssue{
		Title:  "Example issue",
		Labels: []string{"no-such-label"},
	})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("CreateIssue with an unknown label = %v, want a *forgeapi.Error", err)
	}
	if fe.Code != forgeapi.CodeValidation {
		t.Errorf("CreateIssue over a short page = code %q, want %q: the page was the whole list, so the name exists nowhere this route reaches and the truncated code would claim it might",
			fe.Code, forgeapi.CodeValidation)
	}
	if fe.Kind != forgeapi.KindUpstream {
		t.Errorf("CreateIssue = kind %v, want %v: no merge is in question here, and the merge kind would have a consumer tell the caller their pull request is unmergeable",
			fe.Kind, forgeapi.KindUpstream)
	}
	if !strings.Contains(fe.Message, "first page") || !strings.Contains(fe.Message, itoa(testMaxItems)) {
		t.Errorf("CreateIssue = message %q, want one naming the page it read and the bound it stopped at: one page at the instance maximum is not the repository's label list, so a message about the repository claims more than the read establishes",
			fe.Message)
	}
	if strings.Contains(strings.Join(h.instance.arrived(), " "), "POST") {
		t.Errorf("CreateIssue sent the creation anyway: %v, want the label refusal before it", h.instance.arrived())
	}
}

// TestACreationRefusesALabelBeyondAFullPageUnderItsOwnCode holds the other arm of the
// same rule: the same missing name on a page that came back FULL carries the truncated
// code instead.
//
// The two remedies are different, which is the whole reason the code exists. A name
// the whole list does not carry does not exist, and the caller creates it; a name
// missing from a page the read stopped at may exist past the bound, and the caller
// asks for a larger page or a repository with fewer labels. A caller handed one code
// over both has to read the message to tell them apart, which is what a code exists
// to avoid, so the arms are held here by the code and not by the text.
func TestACreationRefusesALabelBeyondAFullPageUnderItsOwnCode(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/example/example/labels":  fullLabelPage(),
		"POST /api/v1/repos/example/example/issues": issueBody,
	})
	_, err := h.client.CreateIssue(t.Context(), testRef(), forgeapi.NewIssue{
		Title:  "Example issue",
		Labels: []string{"label-past-the-page"},
	})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("CreateIssue with a label past the page = %v, want a *forgeapi.Error", err)
	}
	if fe.Code != forgeapi.CodeLabelPageTruncated {
		t.Errorf("CreateIssue over a full page = code %q, want %q: the page the resolution read is not the repository's label list, so refusing under the whole-list code tells the caller a label may not exist when the read cannot say",
			fe.Code, forgeapi.CodeLabelPageTruncated)
	}
	if fe.Kind != forgeapi.KindUpstream {
		t.Errorf("CreateIssue = kind %v, want %v: the read answered, and this arm changes the code rather than the kind", fe.Kind, forgeapi.KindUpstream)
	}
	if fe.Status != http.StatusOK {
		t.Errorf("CreateIssue = status %d, want %d: the label read answered, so the status is the real one", fe.Status, http.StatusOK)
	}
	if !strings.Contains(fe.Message, itoa(testMaxItems)) {
		t.Errorf("CreateIssue = message %q, want one naming the page bound, which is what a caller raises to reach the rest of the list", fe.Message)
	}
	if strings.Contains(strings.Join(h.instance.arrived(), " "), "POST") {
		t.Errorf("CreateIssue sent the creation anyway: %v, want the label refusal before it", h.instance.arrived())
	}
}

// TestACreationResolvesALabelCarriedByAFullPage is the control on the arm above: a
// full page is not by itself a refusal, so the name the page DOES carry still
// resolves and the creation goes out.
func TestACreationResolvesALabelCarriedByAFullPage(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/example/example/labels":  fullLabelPage(),
		"POST /api/v1/repos/example/example/issues": issueBody,
	})
	if _, err := h.client.CreateIssue(t.Context(), testRef(), forgeapi.NewIssue{
		Title:  "Example issue",
		Labels: []string{"example-label0"},
	}); err != nil {
		t.Fatalf("CreateIssue naming a label the full page carries = %v, want nil", err)
	}
	if body := h.instance.body("POST /api/v1/repos/example/example/issues"); !strings.Contains(body, `"labels":[100]`) {
		t.Errorf("CreateIssue sent %s, want the identifier the full page carried for that name", body)
	}
}

// TestTheCrossRepositoryReadScopesWithTheFilterBothProductsDeclare holds the
// operation's own statement about what it answers, on each product of the family.
//
// The filter has to be one BOTH documents declare, because one request shape serves
// both products here. The boolean created is that filter: the instance resolves it
// against whoever the request authenticated as, so no account has to be read first.
// The username filter beside it, created_by, is one product's own parameter and the
// other declares none, so a call built on that spelling is contracted to scope the
// answer on one product and not on the other, while the suite's fixtures match on
// method and path and cannot see the difference.
func TestTheCrossRepositoryReadScopesWithTheFilterBothProductsDeclare(t *testing.T) {
	for _, test := range []struct {
		name     string
		version  string
		document string
	}{
		{name: "gitea", version: `{"version":"1.27.0+dev"}`, document: giteaDoc},
		{name: "forgejo", version: `{"version":"16.0.0-dev+gitea-1.22.0"}`, document: forgejoDoc},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				"GET /api/v1/version":             test.version,
				"GET /swagger.v1.json":            test.document,
				"GET /api/v1/repos/issues/search": "[" + searchRow(false) + "]",
			})
			// The product is what this case varies, and nothing but the
			// capability read establishes it, so it runs first.
			if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
				t.Fatalf("Setup: ConnectionCaps = %v, want nil", err)
			}
			page, err := h.client.ListMyPRs(t.Context())
			if err != nil {
				t.Fatalf("ListMyPRs = %v, want nil", err)
			}
			if len(page.Items) != 1 {
				t.Fatalf("ListMyPRs returned %d row(s), want 1", len(page.Items))
			}
			const route = "GET /api/v1/repos/issues/search"
			if got := h.instance.query(route, "created"); got != "true" {
				t.Errorf("ListMyPRs sent created=%q, want %q: without the filter both documents declare, the route answers every open pull request the credential can see", got, "true")
			}
			if got := h.instance.query(route, "created_by"); got != "" {
				t.Errorf("ListMyPRs sent created_by=%q, want none: that parameter is one product's own and the other declares none, so it scopes the answer on one of the two only", got)
			}
			if slices.Contains(h.instance.arrived(), "GET /api/v1/user") {
				t.Errorf("ListMyPRs reached %v, want no identity read: the filter is resolved by the instance against the request's own credential", h.instance.arrived())
			}
		})
	}
}

// TestTheIssueListAsksTheRouteForIssuesOnly holds the discriminator this route
// needs.
//
// Both documents declare the population of GET /repos/{owner}/{repo}/issues as
// issues AND pull requests, closed by a type filter whose two values they both
// spell. So an absent filter answers pull-request rows too, which this operation
// normalizes into issues and a consumer then closes, comments on or renders as
// issues; the fixtures match on method and path, so no conformance case can see it.
func TestTheIssueListAsksTheRouteForIssuesOnly(t *testing.T) {
	const route = "GET /api/v1/repos/example/example/issues"
	h := newHarness(t, map[string]string{route: "[" + issueBody + "]"})
	if _, err := h.client.ListIssues(t.Context(), testRef()); err != nil {
		t.Fatalf("ListIssues = %v, want nil", err)
	}
	if got := h.instance.query(route, "type"); got != "issues" {
		t.Errorf("ListIssues sent type=%q, want %q: this route's population is issues and pull requests, so the answer carries pull requests without it", got, "issues")
	}
}

// TestTheCrossRepositoryRowsDecodeTheShapeTheSearchRouteSends holds the operation
// against the row both products actually answer.
//
// The route is the ISSUE search, so its rows are Issues: the repository arrives as
// the meta shape whose owner is a login string, and a wire type declaring that owner
// as a user object fails the decode of the WHOLE response, which returns no rows at
// all on every real instance of both products. The row's own repository is also what
// the cross-repository answer is addressed by, so the same body holds the arm that
// recovers it.
//
// The absent fields are asserted here rather than left implicit, because they are
// what the published price rests on: the row carries no head commit, a folded status
// is addressed by a head, so no fold can be issued and the call costs the search
// alone and nothing per row.
func TestTheCrossRepositoryRowsDecodeTheShapeTheSearchRouteSends(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/issues/search": "[" + searchRow(false) + "]",
	})
	page, err := h.client.ListMyPRs(t.Context())
	if err != nil {
		t.Fatalf("ListMyPRs over the row this route sends = %v, want nil: the row is an Issue, whose repository owner is a login string", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListMyPRs returned %d row(s), want 1", len(page.Items))
	}
	row := page.Items[0]
	if row.Repo.Selector != testSelector {
		t.Errorf("the row's repository = %q, want %q: a cross-repository row's own repository is what a consumer addresses it by", row.Repo.Selector, testSelector)
	}
	if row.Title != "Example pull request" {
		t.Errorf("the row's title = %q, want %q", row.Title, "Example pull request")
	}
	if row.Author != "example-user" {
		t.Errorf("the row's author = %q, want %q", row.Author, "example-user")
	}
	if row.HeadSHA != "" {
		t.Errorf("the row's head = %q, want empty: this route's row carries no head key, which both documents declare", row.HeadSHA)
	}
	if row.Action.Checks != forgeapi.CheckUnknown || row.Action.ChecksTotal != 0 {
		t.Errorf("the row's folded verdict = %v over %d, want %v over 0: no head commit means no status read addresses this row",
			row.Action.Checks, row.Action.ChecksTotal, forgeapi.CheckUnknown)
	}
	if got, want := h.instance.count(), 1; got != want {
		t.Errorf("ListMyPRs sent %d request(s), want %d: the search alone, with nothing per row and nothing resolved before it", got, want)
	}
	// No fold was issued, so this call has no rotation: the per-interval cap
	// admits one here, and what leaves the cursor empty is the absence of a head
	// to address a fold by. It is asserted because the option that publishes the
	// cap once named this call's rotation as the cross-repository fairness a sweep
	// gets, and this list has none to give.
	if got := h.client.BudgetState().RotationCursor; got != "" {
		t.Errorf("ListMyPRs left the rotation cursor at %q, want it empty: a rotation records a fold, and this call issues none", got)
	}
}

// TestTheCrossRepositoryRowsDraftFlagIsReadWhereTheRouteCarriesIt holds the one
// field of this row whose PLACE differs from the pull route's.
//
// The row is an Issue, and neither product's document declares a draft key on an
// Issue: the flag is a property of the pull_request object both of them declare on
// it. So a read of the row's top level finds nothing, and because the published
// field is a bool with no unknown member, every row of the poller's own call would
// report a draft pull request as ready and no consumer could tell that from a real
// answer. The false arm is here for the same reason: a flag read from the right
// place has to still answer false where the row says so, or the fix is a constant.
func TestTheCrossRepositoryRowsDraftFlagIsReadWhereTheRouteCarriesIt(t *testing.T) {
	for _, test := range []struct {
		name string
		row  bool
	}{
		{name: "a_draft_row_answers_draft", row: true},
		{name: "a_ready_row_answers_ready", row: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				"GET /api/v1/repos/issues/search": "[" + searchRow(test.row) + "]",
			})
			page, err := h.client.ListMyPRs(t.Context())
			if err != nil {
				t.Fatalf("ListMyPRs = %v, want nil", err)
			}
			if len(page.Items) != 1 {
				t.Fatalf("ListMyPRs returned %d row(s), want 1", len(page.Items))
			}
			if got := page.Items[0].Draft; got != test.row {
				t.Errorf("the row says pull_request.draft=%t and the answer is Draft=%t, want %t: this route places the flag on the pull-request object, so a read of the row's top level answers false whatever the pull request is",
					test.row, got, test.row)
			}
		})
	}
}

// TestThePullRouteRowsDraftFlagIsReadAtItsTopLevel is the control on the arm above:
// the flag one level up, where the pull-request route puts it, must keep arriving.
func TestThePullRouteRowsDraftFlagIsReadAtItsTopLevel(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/example/example/pulls": "[" + draftPullRow(1, true) + "]",
		statusPath: statusOK,
	})
	page, err := h.client.ListPRs(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListPRs = %v, want nil", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListPRs returned %d row(s), want 1", len(page.Items))
	}
	if !page.Items[0].Draft {
		t.Error("the row says draft=true at its top level and the answer is Draft=false, want true: this route carries the flag there, so both places have to be read")
	}
}

// TestTheCrossRepositoryReadCostsOneRequestPerCall holds the other half of that
// price: the filter is resolved by the instance, so a second call on the same
// connection spends no more than the first.
func TestTheCrossRepositoryReadCostsOneRequestPerCall(t *testing.T) {
	h := newHarness(t, map[string]string{"GET /api/v1/repos/issues/search": "[]"})
	for range 2 {
		if _, err := h.client.ListMyPRs(t.Context()); err != nil {
			t.Fatalf("ListMyPRs = %v, want nil", err)
		}
	}
	if got, want := h.instance.count(), 2; got != want {
		t.Errorf("two calls spent %d request(s), want %d: one search each, with nothing resolved before either", got, want)
	}
}

// The run route and one row of it in each product's own spelling: Gitea's is the
// GitHub-shaped run its document declares, a status beside a conclusion, and
// Forgejo's is its own run record, which names the workflow file, the ref's display
// form, the commit and the two times under keys of its own and folds the outcome
// into its status.
const (
	runsRoute = "GET /api/v1/repos/example/example/actions/runs"
	giteaRun  = `{"id":1,"display_title":"Example pull request","path":"example.yml@refs/heads/main",` +
		`"head_branch":"example-feature","head_sha":"` + testHeadSHA + `",` +
		`"html_url":"https://forge.example/example/example/actions/runs/1",` +
		`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z",` +
		`"status":"completed","conclusion":"failure","repository":{"full_name":"example/example"}}`
	forgejoRun = `{"id":1,"title":"Example pull request","workflow_id":"example.yml",` +
		`"prettyref":"example-feature","commit_sha":"` + testHeadSHA + `",` +
		`"html_url":"https://forge.example/example/example/actions/runs/1",` +
		`"created":"2026-01-01T00:00:00Z","updated":"2026-01-02T00:00:00Z",` +
		`"status":"failure","repository":{"full_name":"example/example"}}`
)

// TestTheRunListingReadsEachProductsOwnRunRow holds the one listing this family
// serves both products through to both rows: the same run, spelled apart, answers
// the same normalized record. A row read under the other product's keys would come
// back with no head, no times and no branch, and a status read without its
// conclusion would report a failed Gitea run as unknown, which is the run a
// consumer filtering for failures never sees.
//
// The request is held too: the page bound rides as the page size, and the head
// filter the re-run sends on the same route is absent, or the listing would answer
// one commit's runs.
func TestTheRunListingReadsEachProductsOwnRunRow(t *testing.T) {
	want := forgeapi.Run{
		Repo:      testRef(),
		Name:      "example",
		Branch:    "example-feature",
		HeadSHA:   testHeadSHA,
		WebURL:    "https://forge.example/example/example/actions/runs/1",
		CreatedAt: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC),
		State:     forgeapi.CheckFailing,
	}
	for _, test := range []struct {
		name string
		row  string
	}{
		{name: "gitea", row: giteaRun},
		{name: "forgejo", row: forgejoRun},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				runsRoute: `{"total_count":1,"workflow_runs":[` + test.row + `]}`,
			})
			page, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithPageBound(5))
			if err != nil {
				t.Fatalf("ListRuns = %v, want nil", err)
			}
			if len(page.Items) != 1 {
				t.Fatalf("ListRuns returned %d run(s), want 1", len(page.Items))
			}
			got := page.Items[0]
			if !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
				t.Errorf("ListRuns times = %v and %v, want %v and %v", got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt)
			}
			got.CreatedAt, got.UpdatedAt = want.CreatedAt, want.UpdatedAt
			if got != want {
				t.Errorf("ListRuns over the %s row = %+v, want %+v", test.name, got, want)
			}
			if page.Next != "" {
				t.Errorf("ListRuns over a short page = continuation %q, want none", page.Next)
			}
			if got := h.instance.query(runsRoute, "limit"); got != "5" {
				t.Errorf("ListRuns sent limit=%q, want %q: the page bound is the page size", got, "5")
			}
			if got := h.instance.query(runsRoute, "head_sha"); got != "" {
				t.Errorf("ListRuns sent head_sha=%q, want none: the head filter narrows the route to one commit's runs", got)
			}
			if n := h.instance.count(); n != 1 {
				t.Errorf("ListRuns sent %d request(s), want 1: %v", n, h.instance.arrived())
			}
		})
	}
}

// TestARunsNameIsItsWorkflowFileWithoutTheExtension holds the one field neither
// product's run spells as a name: both carry the workflow FILE, Gitea before the ref
// the run ran on and Forgejo alone, and the name a workflow carries where it
// declares none is that file's own, without its YAML extension.
func TestARunsNameIsItsWorkflowFileWithoutTheExtension(t *testing.T) {
	for _, test := range []struct {
		name string
		run  wireRun
		want string
	}{
		{name: "gitea_yml_on_a_branch", run: wireRun{Path: "build.yml@refs/heads/main"}, want: "build"},
		{name: "gitea_yaml_on_a_tag", run: wireRun{Path: "release.yaml@refs/tags/v1.0.0"}, want: "release"},
		{name: "forgejo_file_alone", run: wireRun{WorkflowID: "ci.yml"}, want: "ci"},
		{name: "a_file_in_a_directory", run: wireRun{Path: ".gitea/workflows/lint.yaml@refs/heads/main"}, want: "lint"},
		{name: "no_file_at_all", run: wireRun{}, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.run.name(); got != test.want {
				t.Errorf("name() over path %q and workflow_id %q = %q, want %q", test.run.Path, test.run.WorkflowID, got, test.want)
			}
		})
	}
}

// TestAZeroPageCapTraversesNoPage holds the two published page caps to the literal
// zero each one's doc comment states.
//
// There is one configuration door, so a zero passed to an option is that option's own
// meaning rather than a request for the default. A cap that executed the opposite of
// what it publishes would be worse than one that refuses: the caller reads the doc,
// gets a request anyway, and nothing in the answer says so.
func TestAZeroPageCapTraversesNoPage(t *testing.T) {
	t.Run("a_list", func(t *testing.T) {
		h := newHarness(t, map[string]string{"GET /api/v1/user/repos": "[" + repoRow + "]"},
			forgeapi.WithListPages(0))
		page, err := h.client.ListRepos(t.Context())
		if err != nil {
			t.Fatalf("ListRepos = %v, want an empty page rather than a refusal", err)
		}
		if spent := h.instance.count(); spent != 0 {
			t.Errorf("ListRepos sent %d request(s), want 0: its own cap admits no page", spent)
		}
		if len(page.Items) != 0 {
			t.Errorf("ListRepos returned %d row(s), want 0", len(page.Items))
		}
		if page.Partial == nil || page.Partial.Reason != forgeapi.PartialPaginationCap {
			t.Errorf("ListRepos = partial %v, want the pagination cap: a page bound is what stopped the work", page.Partial)
		}
		if page.Next == "" {
			t.Error("ListRepos carries no continuation, want the page nobody asked for: a response-level pagination cap always arrives with one, so the remainder is reachable without re-running a call this cap refuses")
		}
	})
	t.Run("a_list_resumes_from_the_page_nobody_asked_for", func(t *testing.T) {
		unpaged := newHarness(t, map[string]string{"GET /api/v1/user/repos": "[" + repoRow + "]"}, forgeapi.WithListPages(0))
		paged := newHarnessOver(t, unpaged.instance, time.Now)
		first, err := unpaged.client.ListRepos(t.Context())
		if err != nil || first.Next == "" {
			t.Fatalf("Setup: ListRepos under a zero page cap = Next %q, %v, want a continuation", first.Next, err)
		}
		if _, err := paged.client.ListRepos(t.Context(), forgeapi.WithAfter(first.Next)); err != nil {
			t.Fatalf("ListRepos(WithAfter(%q)) = %v, want the first page: no page fixed the limit the walk is sent at, so the connection's own is the one", first.Next, err)
		}
		if got := paged.instance.query("GET /api/v1/user/repos", keyPage); got != "1" {
			t.Errorf("ListRepos(WithAfter(%q)) sent page=%q, want %q: the page nobody asked for was the first", first.Next, got, "1")
		}
	})
	t.Run("a_folded_status", func(t *testing.T) {
		h := newHarness(t, map[string]string{statusPath: statusOK}, forgeapi.WithStatusPages(0))
		got, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA)
		if err != nil {
			t.Fatalf("CommitStatus = %v, want an unknown verdict rather than a refusal", err)
		}
		if spent := h.instance.count(); spent != 0 {
			t.Errorf("CommitStatus sent %d request(s), want 0: its own cap admits no page", spent)
		}
		if got.State != forgeapi.CheckUnknown {
			t.Errorf("CommitStatus = %v, want %v: nothing was read, so nothing was folded", got.State, forgeapi.CheckUnknown)
		}
		if got.Partial == nil || got.Partial.Reason != forgeapi.PartialPaginationCap {
			t.Errorf("CommitStatus = partial %v, want the pagination cap: a page bound is what stopped the fold", got.Partial)
		}
	})
}

// TestADecodeFailureCarriesTheStatusTheInstanceAnswered holds the published invariant
// that the status on an error is the real one always.
//
// A body this library cannot read is upstream's shape rather than a local refusal, so
// the response that carried it has a status and that status is what the error reports.
// A fabricated 200 is immediately wrong on a creation, which answers 201, and it sends
// a consumer looking for a successful response that never happened.
func TestADecodeFailureCarriesTheStatusTheInstanceAnswered(t *testing.T) {
	in := newInstance()
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in.mu.Lock()
		in.requests = append(in.requests, r.Method+" "+r.URL.EscapedPath())
		in.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if _, err := w.Write([]byte(`{"number":`)); err != nil {
			t.Errorf("Setup: writing the truncated creation: %v", err)
		}
	}))
	t.Cleanup(in.server.Close)
	h := newHarnessOver(t, in, time.Now)
	_, err := h.client.CreateIssue(t.Context(), testRef(), forgeapi.NewIssue{Title: "Example issue"})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("CreateIssue against a body that does not decode = %v, want a *forgeapi.Error", err)
	}
	if fe.Status != http.StatusCreated {
		t.Errorf("CreateIssue = status %d, want %d: the status is the one this instance answered, and a creation answers 201", fe.Status, http.StatusCreated)
	}
	if fe.Op != "CreateIssue" || fe.Kind != forgeapi.KindUpstream {
		t.Errorf("CreateIssue = op %q kind %v, want %q and %v", fe.Op, fe.Kind, "CreateIssue", forgeapi.KindUpstream)
	}
	// The field a consumer is told to branch on has to carry something, and the
	// rendered string a user reads has to have the cause where the cause belongs:
	// an empty code renders as a gap between the operation and the status.
	if fe.Code != forgeapi.CodeValidation {
		t.Errorf("CreateIssue = code %q, want %q: a consumer branches on this field, and a body this library will not accept is the shape that code names", fe.Code, forgeapi.CodeValidation)
	}
	if got, want := fe.Error(), "CreateIssue: validation (status 201): response body did not decode"; got != want {
		t.Errorf("CreateIssue = %q, wanted rendered as %q", got, want)
	}
}

// TestEveryFailureLineCarriesTheDiagnosticIdTheErrorShows holds the correlation the
// diagnostic id exists for.
//
// The value is minted per failed operation, shown to a user through the error and
// retained only in the log, so a failure line without it leaves the id a user reads
// out naming nothing at all.
//
// Every path that mints one is an arm here, because the defect this holds against
// shipped on the paths nothing exercised: a refusal the instance answered, a body
// this library could not read after the request had already been recorded as a
// success, the mutation a read-only client refuses, the capability detection says
// this instance lacks, and the read the governor deferred to hold the mutation
// reserve. The last three send nothing at all, which is exactly why a line for them
// is easy to lose.
func TestEveryFailureLineCarriesTheDiagnosticIdTheErrorShows(t *testing.T) {
	for _, test := range []struct {
		name  string
		build func(t *testing.T) *harness
		act   func(t *testing.T, h *harness) error
	}{
		{
			name:  "a_refusal_the_instance_answered",
			build: func(t *testing.T) *harness { t.Helper(); return newRefusingHarness(t, http.StatusInternalServerError) },
		},
		{
			name: "a_body_that_did_not_decode",
			build: func(t *testing.T) *harness {
				t.Helper()
				return newHarness(t, map[string]string{"GET /api/v1/user": `{"login":`})
			},
		},
		{
			name: "the_mutation_a_read_only_client_refuses",
			build: func(t *testing.T) *harness {
				t.Helper()
				return newHarness(t, nil, forgeapi.WithMutations(false))
			},
			act: func(t *testing.T, h *harness) error {
				t.Helper()
				_, err := h.client.CreateIssue(t.Context(), testRef(), forgeapi.NewIssue{Title: "Example issue"})
				return err
			},
		},
		{
			name: "the_capability_this_instance_carries_no_verb_for",
			build: func(t *testing.T) *harness {
				t.Helper()
				return newHarness(t, map[string]string{
					"GET /api/v1/version":  `{"version":"1.27.0+dev"}`,
					"GET /swagger.v1.json": giteaDoc,
				})
			},
			act: func(t *testing.T, h *harness) error {
				t.Helper()
				return h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA)
			},
		},
		{
			name: "the_read_the_governor_deferred",
			build: func(t *testing.T) *harness {
				t.Helper()
				h := newHarness(t, map[string]string{
					"GET /api/v1/user":                          userBody,
					"GET /api/v1/repos/example/example/pulls/1": pullRow(1),
				}, forgeapi.WithMutationReserve(1))
				h.instance.answerHeader("RateLimit", "r=1;t=60")
				// The reserve is crossed by what the FIRST read reports, so
				// one has to have happened before the deferral can.
				if _, err := h.client.Whoami(t.Context()); err != nil {
					t.Fatalf("Setup: Whoami = %v, want nil: the first read is what reads the signal", err)
				}
				return h
			},
			act: func(t *testing.T, h *harness) error {
				t.Helper()
				_, err := h.client.ReadPR(t.Context(), testRef(), testPR())
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := test.build(t)
			act := test.act
			if act == nil {
				act = func(t *testing.T, h *harness) error {
					t.Helper()
					_, err := h.client.Whoami(t.Context())
					return err
				}
			}
			err := act(t, h)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) {
				t.Fatalf("the operation = %v, want a *forgeapi.Error", err)
			}
			if fe.DiagID == "" {
				t.Fatal("the failure carries no diagnostic id, want one: this is a failed operation, which is what mints one")
			}
			if !h.logged("diag_id=" + fe.DiagID) {
				t.Errorf("no line carries diag_id=%s, want the failure line to: the id is retained only in the log, so an id the log never carries correlates with nothing", fe.DiagID)
			}
		})
	}
}

// TestTheLastCostIsOneCallsPriceRatherThanAnAccumulation holds the figure a poller
// repeating one operation reads.
//
// A cost accumulated per operation NAME grows without bound under exactly that
// poller: the second identity read of a cycle reported two, the third three, and the
// figure a consumer renders as this call's price became a count of how long the
// process had been running.
func TestTheLastCostIsOneCallsPriceRatherThanAnAccumulation(t *testing.T) {
	h := newHarness(t, map[string]string{"GET /api/v1/user": userBody})
	for call := 1; call <= 3; call++ {
		if _, err := h.client.Whoami(t.Context()); err != nil {
			t.Fatalf("Whoami call %d = %v, want nil", call, err)
		}
		if got := h.client.BudgetState().LastCost; got != 1 {
			t.Errorf("after Whoami call %d, BudgetState().LastCost = %d, want 1: this operation sends one request per call", call, got)
		}
	}
}

// TestAFailedFoldDoesNotPublishTheDefectReason holds what a list answers when one
// row's fold fails upstream, and it is the published vocabulary that decides it.
//
// The unknown partial reason is documented as a library defect rather than an
// outcome, so handing it to a consumer on a row that is otherwise a perfectly good
// list item says the library failed to classify its own result. Nothing else in the
// frozen vocabulary is true of a 500 either: no page bound was reached, no bound of
// this client's stopped the work, and nothing was throttled or deferred. So the call
// fails, which is also what the single-pull-request read does with the same fold.
func TestAFailedFoldDoesNotPublishTheDefectReason(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/example/example/pulls": "[" + pullRow(1) + "]",
	})
	page, err := h.client.ListPRs(t.Context(), testRef())
	if err == nil {
		t.Fatalf("ListPRs with a failing fold = %+v, want the upstream failure: the reason a row could carry would be the defect sentinel", page)
	}
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("ListPRs = %v, want a *forgeapi.Error", err)
	}
	if fe.Status != 500 {
		t.Errorf("ListPRs = status %d, want 500: the status carried is the one the instance answered", fe.Status)
	}
	if fired := h.spy.times("PartialResult:" + forgeapi.PartialUnknown.String()); fired != 0 {
		t.Errorf("the failed fold published the unknown partial reason %d time(s), want 0: that member is documented as a defect rather than an outcome", fired)
	}
}

// TestAThrottledFoldMarksTheRowsItCouldNotFold holds the one upstream failure inside
// a fold the envelope does assign a marker to.
//
// A throttle refusal answered upstream is one of the rate-limited reason's two
// producers, so the row it refused carries that reason, and so does every row the
// interval had not yet reached: no further fold is issued into an instance that has
// just refused one, and a row with no verdict and no reason cannot be told from a
// pull request with no checks.
func TestAThrottledFoldMarksTheRowsItCouldNotFold(t *testing.T) {
	in := newInstance()
	rows := "[" + pullRow(1) + "," + pullRow(2) + "]"
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.EscapedPath()
		in.mu.Lock()
		in.requests = append(in.requests, key)
		in.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.EscapedPath(), "/status") {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			if _, err := w.Write([]byte(`{"message":"slow down"}`)); err != nil {
				t.Errorf("Setup: writing the throttle: %v", err)
			}
			return
		}
		if _, err := w.Write([]byte(rows)); err != nil {
			t.Errorf("Setup: writing the rows: %v", err)
		}
	}))
	t.Cleanup(in.server.Close)
	h := newHarnessOver(t, in, time.Now)
	page, err := h.client.ListPRs(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListPRs against a throttled fold = %v, want the rows with their folds marked", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("ListPRs returned %d row(s), want 2", len(page.Items))
	}
	for i, row := range page.Items {
		if row.Action.Checks != forgeapi.CheckUnknown {
			t.Errorf("row %d = verdict %v, want %v: the fold was refused", i, row.Action.Checks, forgeapi.CheckUnknown)
		}
		if row.Partial == nil || row.Partial.Reason != forgeapi.PartialRateLimited {
			t.Errorf("row %d = partial %v, want the rate-limited reason: that is the reason the envelope assigns a throttle upstream answered", i, row.Partial)
		}
	}
	folds := 0
	for _, request := range in.arrived() {
		if strings.HasSuffix(request, "/status") {
			folds++
		}
	}
	if folds != 1 {
		t.Errorf("the list issued %d fold(s) after the throttle, want 1: a throttle ends the work for that instance rather than being asked again per row", folds)
	}
}

// TestTheFoldIsComputedOverTheRowsRatherThanTakenFromTheEndpoint holds the one
// measured fact the whole folded-status mechanism exists for, and the precedence the
// fold applies over a mixed collection.
//
// The combined-status endpoint is PAGINATED and both its own state and its own total
// are computed over the page it returned, so a page of successes reports success for
// a commit that is failing and nothing in the body contradicts it. That is why this
// family folds client-side over the rows it holds and never reads the returned
// state, and reporting a failing commit as passing is the worst answer on this path:
// it renders a red pull request green with no marker.
//
// Each case therefore states the endpoint's verdict and the rows SEPARATELY, and
// the two disagree: a fold that trusted either the state or the total answers the
// first column and the assertions are on the second.
func TestTheFoldIsComputedOverTheRowsRatherThanTakenFromTheEndpoint(t *testing.T) {
	for _, test := range []struct {
		name     string
		body     string
		want     forgeapi.CheckState
		passing  int
		failing  int
		pending  int
		neutral  int
		unknown  int
		contexts int
	}{
		{
			name:     "a_failing_row_under_a_success_verdict_is_failing",
			body:     combined("success", 99, statusRow("build", "success"), statusRow("test", "failure")),
			want:     forgeapi.CheckFailing,
			passing:  1,
			failing:  1,
			contexts: 2,
		},
		{
			name: "a_failing_row_outranks_a_pending_one",
			body: combined("pending", 0, statusRow("build", "pending"), statusRow("test", "failure"),
				statusRow("lint", "success")),
			want:     forgeapi.CheckFailing,
			passing:  1,
			failing:  1,
			pending:  1,
			contexts: 3,
		},
		{
			name:     "a_pending_row_outranks_a_passing_one",
			body:     combined("success", 1, statusRow("build", "success"), statusRow("test", "pending")),
			want:     forgeapi.CheckPending,
			passing:  1,
			pending:  1,
			contexts: 2,
		},
		{
			name:     "a_passing_collection_under_a_failure_verdict_is_passing",
			body:     combined("failure", 9, statusRow("build", "success"), statusRow("test", "success")),
			want:     forgeapi.CheckPassing,
			passing:  2,
			contexts: 2,
		},
		{
			name:     "a_neutral_collection_outranks_nothing_and_is_neutral",
			body:     combined("success", 5, statusRow("build", "skipped"), statusRow("test", "warning")),
			want:     forgeapi.CheckNeutral,
			neutral:  2,
			contexts: 2,
		},
		{
			// The oracle is the exported meaning of the two members rather
			// than what the fold happened to answer: passing means every
			// check reported succeeded, and a row this family's table did not
			// map is a check whose state was not read. So the unknown arm
			// outranks the passing one, and a state added upstream tomorrow
			// reaches a consumer as an unknown rather than as a green commit.
			name:     "an_unmapped_row_beside_a_passing_one_is_unknown_and_counted_unknown",
			body:     combined("success", 2, statusRow("build", "success"), statusRow("test", "future_state")),
			want:     forgeapi.CheckUnknown,
			passing:  1,
			unknown:  1,
			contexts: 2,
		},
		{
			name:     "an_unmapped_row_beside_a_failing_one_is_failing",
			body:     combined("success", 2, statusRow("build", "failure"), statusRow("test", "future_state")),
			want:     forgeapi.CheckFailing,
			failing:  1,
			unknown:  1,
			contexts: 2,
		},
		{
			name:     "an_empty_collection_is_unknown",
			body:     combined("success", 7),
			want:     forgeapi.CheckUnknown,
			contexts: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{statusPath: test.body})
			got, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA)
			if err != nil {
				t.Fatalf("CommitStatus = %v, want nil", err)
			}
			if got.State != test.want {
				t.Errorf("CommitStatus over %s = %v, want %v: the verdict is the worst state among the rows held, never the state the endpoint returned",
					test.body, got.State, test.want)
			}
			if got.Passing != test.passing || got.Failing != test.failing || got.Pending != test.pending ||
				got.Neutral != test.neutral || got.Unknown != test.unknown {
				t.Errorf("CommitStatus over %s = passing %d, failing %d, pending %d, neutral %d, unknown %d, want %d, %d, %d, %d, %d",
					test.body, got.Passing, got.Failing, got.Pending, got.Neutral, got.Unknown,
					test.passing, test.failing, test.pending, test.neutral, test.unknown)
			}
			if got.Total != test.contexts {
				t.Errorf("CommitStatus over %s = total %d, want %d: the total is the rows held, never the figure the endpoint reported",
					test.body, got.Total, test.contexts)
			}
			if got.Partial != nil {
				t.Errorf("CommitStatus over %s = partial %v, want none: the page was not full, so nothing was truncated", test.body, got.Partial)
			}
		})
	}
}

// TestAFullStatusPageOnAListAnswersUnknownWithThePaginationCap holds the other half
// of that measurement: what the fold does when it cannot see the whole collection.
//
// A page that comes back FULL is the only witness of truncation this endpoint gives,
// and on a list the rule is one page and no request beyond it. So the row answers
// unknown with the pagination cap rather than the verdict over the rows it did see,
// and the endpoint's own agreeing state is exactly what must not be published there.
func TestAFullStatusPageOnAListAnswersUnknownWithThePaginationCap(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/example/example/pulls": "[" + pullRow(1) + "]",
		statusPath: fullStatusPage(),
	})
	page, err := h.client.ListPRs(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListPRs = %v, want nil", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListPRs returned %d row(s), want 1", len(page.Items))
	}
	row := page.Items[0]
	if row.Action.Checks != forgeapi.CheckUnknown {
		t.Errorf("the row's folded verdict = %v, want %v: a full page means the collection may continue, and this endpoint's own state is computed over the page it returned",
			row.Action.Checks, forgeapi.CheckUnknown)
	}
	if row.Partial == nil || row.Partial.Reason != forgeapi.PartialPaginationCap {
		t.Fatalf("the row's partial marker = %v, want the pagination cap: an unknown with no reason cannot be told from a pull request with no checks", row.Partial)
	}
	if got, want := h.instance.count(), 2; got != want {
		t.Errorf("ListPRs sent %d request(s), want %d: the list plus one page of one fold, and no request beyond that page", got, want)
	}
}

// TestTheRotationResumesAfterTheRowItLastServed holds the mechanism the two
// per-interval caps are only fair with.
//
// A poller presents the same list in the same order every cycle, so an interval that
// always starts at the first row folds the same prefix forever and every row past the
// cap starves. The cursor is what moves the start, and it is also the one value that
// crosses the surface for a consumer to persist, so it must carry the row rather than
// being overwritten with the empty value.
func TestTheRotationResumesAfterTheRowItLastServed(t *testing.T) {
	clock := newStepClock()
	h := newClockedHarness(t, clock.now, map[string]string{
		"GET /api/v1/repos/example/example/pulls": "[" + pullRow(1) + "," + pullRow(2) + "]",
		statusPath: statusOK,
	},
		forgeapi.WithStatusReadsPerInterval(1),
		forgeapi.WithStatusTimePerInterval(interval),
	)
	served := make([]int, 0, 2)
	for pass := range 3 {
		page, err := h.client.ListPRs(t.Context(), testRef())
		if err != nil {
			t.Fatalf("ListPRs pass %d = %v, want nil", pass, err)
		}
		if len(page.Items) != 2 {
			t.Fatalf("ListPRs pass %d returned %d row(s), want 2", pass, len(page.Items))
		}
		served = append(served, folded(page.Items)...)
		if cursor := h.client.BudgetState().RotationCursor; cursor == "" {
			t.Errorf("pass %d left the rotation cursor empty, want the row it served: the empty value means a first run, so writing it destroys what the consumer persisted", pass)
		}
		clock.step(interval)
	}
	if !slices.Contains(served, 2) {
		t.Errorf("three passes folded rows %v, want row 2 among them: the interval after the cap resumes at the row following the one it last served, or the rows past the cap starve forever", served)
	}
}

// TestTheRotationAdvancesWhenTheRowItLastServedHasLeftTheList holds the arm the
// cursor alone cannot cover.
//
// The cursor names ONE row, and that row is merged, closed or paged out by the next
// cycle as a matter of course, so a miss is the ordinary outcome rather than an
// error. Returning to the head of the list there re-folds the prefix the previous
// interval already served and starves every row past the per-interval cap, which is
// the property the rotation exists for, so the miss resumes at the position after
// the one this connection last folded.
func TestTheRotationAdvancesWhenTheRowItLastServedHasLeftTheList(t *testing.T) {
	const route = "GET /api/v1/repos/example/example/pulls"
	clock := newStepClock()
	h := newClockedHarness(t, clock.now, map[string]string{
		route:      "[" + pullRow(1) + "," + pullRow(2) + "]",
		statusPath: statusOK,
	},
		forgeapi.WithStatusReadsPerInterval(1),
		forgeapi.WithStatusTimePerInterval(interval),
	)
	first, err := h.client.ListPRs(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListPRs pass 1 = %v, want nil", err)
	}
	if got := folded(first.Items); !slices.Equal(got, []int{1}) {
		t.Fatalf("ListPRs pass 1 folded %v, want row 1 alone: the cap admits one read per interval", got)
	}
	// The row the first pass served has left the list, which is what a merged or
	// closed pull request does between two cycles.
	h.instance.serve(route, "["+pullRow(2)+","+pullRow(3)+"]")
	clock.step(interval)
	second, err := h.client.ListPRs(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListPRs pass 2 = %v, want nil", err)
	}
	if got := folded(second.Items); !slices.Equal(got, []int{3}) {
		t.Errorf("ListPRs pass 2 folded %v, want row 3: the cursor named a row this list no longer carries, and starting over at the head re-serves the prefix the first pass already folded",
			got)
	}
}

// TestTheRotationServesEveryRowOfEveryListAPollerPresents holds the rotation on the
// shape a poller actually has: several repositories on ONE client.
//
// A position is an index into one list's order and means nothing in another's, and
// the per-repository list is a different order per repository. So a position held per
// CONNECTION is read by whichever list is presented next: measured over four cycles
// of two four-row repositories under a cap of one fold per interval, one position
// shared by both lists serves rows 1 and 3 of the first and 12 and 14 of the second
// and nothing else, a two-cycle lock that starves half of each, while no volatile
// position at all serves each list's head four times. Keyed per list, each list
// advances by one per cycle and every row is served, which is the property the
// rotation exists for.
func TestTheRotationServesEveryRowOfEveryListAPollerPresents(t *testing.T) {
	const (
		first  = "one/one"
		second = "two/two"
	)
	rows := map[string][]int{first: {1, 2, 3, 4}, second: {11, 12, 13, 14}}
	routes := map[string]string{}
	for selector, numbers := range rows {
		bodies := make([]string, 0, len(numbers))
		for _, number := range numbers {
			bodies = append(bodies, pullRow(number))
		}
		routes["GET /api/v1/repos/"+selector+"/pulls"] = "[" + strings.Join(bodies, ",") + "]"
	}
	clock := newStepClock()
	h := newClockedHarness(t, clock.now, routes,
		forgeapi.WithStatusReadsPerInterval(1),
		forgeapi.WithStatusTimePerInterval(interval),
	)
	// One instance answers both repositories' folds, because the canonical head is
	// the same commit on each and what this case measures is which ROWS were served.
	h.instance.serve(statusPath, statusOK)
	for selector := range rows {
		h.instance.serve("GET /api/v1/repos/"+selector+"/commits/"+testHeadSHA+"/status", statusOK)
	}
	served := map[string][]int{}
	for cycle := range 4 {
		for _, selector := range []string{first, second} {
			ref := forgeapi.RepoRef{Family: forgeapi.FamilyGitea, Selector: selector, DisplayPath: selector}
			ref.ID = ref.Encode()
			page, err := h.client.ListPRs(t.Context(), ref)
			if err != nil {
				t.Fatalf("ListPRs(%q) cycle %d = %v, want nil", selector, cycle, err)
			}
			if len(page.Items) != len(rows[selector]) {
				t.Fatalf("ListPRs(%q) cycle %d returned %d row(s), want %d", selector, cycle, len(page.Items), len(rows[selector]))
			}
			served[selector] = append(served[selector], folded(page.Items)...)
			// The interval's cap is per CONNECTION, so one window buys one fold
			// for the whole client: each list gets its own window, which is what a
			// poller pacing itself across a cycle gets on the real clock.
			clock.step(interval)
		}
	}
	for selector, want := range rows {
		got := append([]int(nil), served[selector]...)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("four cycles of %q folded rows %v, want each of %v exactly once: a poller presents several lists on one connection, so a position read in the wrong list resumes at a place nobody served and half the rows starve",
				selector, served[selector], want)
		}
	}
}

// folded is the rows one pass actually folded, by number, which is what a rotation
// assertion reads.
func folded(items []forgeapi.PullRequest) []int {
	var out []int
	for _, row := range items {
		if row.Action.Checks == forgeapi.CheckPassing {
			out = append(out, row.Ref.Number)
		}
	}
	return out
}

// TestTheRotationCursorACallerHandedInIsNotDestroyed holds the same value from the
// consumer's side: it crosses the surface at construction, so a list that overwrote
// it with the empty value would lose what a restart resumes from.
func TestTheRotationCursorACallerHandedInIsNotDestroyed(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/example/example/pulls": "[]",
	}, forgeapi.WithRotationCursor("carried-in"))
	if _, err := h.client.ListPRs(t.Context(), testRef()); err != nil {
		t.Fatalf("ListPRs = %v, want nil", err)
	}
	if got := h.client.BudgetState().RotationCursor; got != "carried-in" {
		t.Errorf("the rotation cursor after a list = %q, want %q: a list with no row to fold rotates nothing", got, "carried-in")
	}
}

// TestBothPerIntervalCapsAreEnforced holds the two bounds the budget publishes over
// one rolling window, and holds the ZERO on each to what its own option says it
// means.
//
// The two must not be coupled: reading the clock as the interval's length resets the
// count as well, so a zero clock would switch the count cap off, and a zero count cap
// would be the only bound left.
func TestBothPerIntervalCapsAreEnforced(t *testing.T) {
	rows := "[" + pullRow(1) + "," + pullRow(2) + "]"
	for _, test := range []struct {
		name     string
		opts     []forgeapi.Option
		wantFold int
	}{
		{
			name:     "the_count_cap_admits_its_own_number",
			opts:     []forgeapi.Option{forgeapi.WithStatusReadsPerInterval(1)},
			wantFold: 1,
		},
		{
			name:     "a_zero_count_admits_none",
			opts:     []forgeapi.Option{forgeapi.WithStatusReadsPerInterval(0)},
			wantFold: 0,
		},
		{
			name:     "a_zero_clock_admits_none_whatever_the_count_allows",
			opts:     []forgeapi.Option{forgeapi.WithStatusTimePerInterval(0), forgeapi.WithStatusReadsPerInterval(20)},
			wantFold: 0,
		},
		{
			name:     "a_clock_one_read_cannot_fit_in_admits_one_and_then_refuses",
			opts:     []forgeapi.Option{forgeapi.WithStatusTimePerInterval(time.Nanosecond), forgeapi.WithStatusReadsPerInterval(20)},
			wantFold: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				"GET /api/v1/repos/example/example/pulls": rows,
				statusPath: statusOK,
			}, test.opts...)
			page, err := h.client.ListPRs(t.Context(), testRef())
			if err != nil {
				t.Fatalf("ListPRs = %v, want nil", err)
			}
			folded, deferred := 0, 0
			for _, row := range page.Items {
				if row.Action.Checks == forgeapi.CheckPassing {
					folded++
				}
				if row.Partial != nil && row.Partial.Reason == forgeapi.PartialBudget {
					deferred++
				}
			}
			if folded != test.wantFold {
				t.Errorf("ListPRs folded %d row(s), want %d", folded, test.wantFold)
			}
			if want := len(page.Items) - test.wantFold; deferred != want {
				t.Errorf("ListPRs marked %d row(s) with the budget reason, want %d: a fold the interval did not admit is reported rather than dropped", deferred, want)
			}
		})
	}
}

// TestAnOrdinaryReadIsDeferredBeforeItCrossesTheMutationReserve holds the reserve
// against every read rather than against the folded-status reads alone.
//
// The reserve is a floor under what the merge the user is about to click will need,
// so a cycle of ordinary reads that spent through it would empty exactly the budget
// it exists to hold.
func TestAnOrdinaryReadIsDeferredBeforeItCrossesTheMutationReserve(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/user":                         userBody,
		"GET /api/v1/repos/example/example/labels": labelBody,
	}, forgeapi.WithMutationReserve(1))
	h.instance.answerHeader("RateLimit", "r=1;t=60")
	if _, err := h.client.Whoami(t.Context()); err != nil {
		t.Fatalf("Whoami = %v, want nil: the first read is what reads the signal", err)
	}
	before := h.instance.count()
	page, err := h.client.ListLabels(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListLabels after the reserve was reached = %v, want an empty page marked partial", err)
	}
	if spent := h.instance.count() - before; spent != 0 {
		t.Errorf("ListLabels sent %d request(s), want 0: the read is deferred rather than issued once the remaining budget is down to the reserve", spent)
	}
	if page.Partial == nil || page.Partial.Reason != forgeapi.PartialRateLimited {
		t.Errorf("ListLabels = partial %v, want the rate-limited reason: a deferral is reported as a partial rather than as a failure", page.Partial)
	}
	if fired := h.spy.times("ReadDeferred"); fired == 0 {
		t.Error("the deferral fired no ReadDeferred counter, want one: that counter is what separates a partial the library chose from one upstream imposed")
	}
	if remaining := h.client.BudgetState().Remaining; remaining <= 0 {
		t.Errorf("the reported remaining budget = %d, want a figure above zero: that is what tells a deferral from an upstream throttle", remaining)
	}
}

// TestTheSwaggerDocumentIsFetchedOncePerConnection holds the price of the one read
// that is legitimately large. Two concurrent FIRST callers are the case a mutex
// around the cache checks does not cover: both pass the check before either writes,
// so both fetch a near-megabyte document.
func TestTheSwaggerDocumentIsFetchedOncePerConnection(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/version": `{"version":"1.27.0+dev"}`,
		"GET /swagger.v1.json": `{"info":{"title":"Gitea API"},` +
			`"paths":{"/repos/{owner}/{repo}/actions/runs/{run}/rerun-failed-jobs":` +
			`{"post":{"operationId":"rerun"}}}}`,
	})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
				t.Errorf("ConnectionCaps = %v, want nil", err)
			}
		})
	}
	wg.Wait()
	fetched := 0
	for _, request := range h.instance.arrived() {
		if request == "GET /swagger.v1.json" {
			fetched++
		}
	}
	if fetched != 1 {
		t.Errorf("four concurrent first callers fetched the swagger document %d time(s), want 1: its published price is one fetch per connection, and race freedom alone does not enforce a price", fetched)
	}
}

// TestTheBudgetReportsTheCostOfTheCallRatherThanOfOneRequest holds the figure a
// consumer renders. On this family a list costs one request plus one per row, so a
// constant of one would be wrong by an order of magnitude on exactly the call a
// poller runs most.
func TestTheBudgetReportsTheCostOfTheCallRatherThanOfOneRequest(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/example/example/pulls": "[" + pullRow(1) + "," + pullRow(2) + "]",
		statusPath: statusOK,
	})
	if _, err := h.client.ListPRs(t.Context(), testRef()); err != nil {
		t.Fatalf("ListPRs = %v, want nil", err)
	}
	if got, want := h.client.BudgetState().LastCost, h.instance.count(); got != want {
		t.Errorf("BudgetState().LastCost = %d, want %d: the last cost is a CALL's price, and this call spent the list plus one fold per row", got, want)
	}
}

// TestAStrategyOutsideTheFamilysSetIsRefusedBeforeAnyRequest holds the one refusal
// this family answers under the strategy code: a spelling outside its own closed set
// never leaves the process. The merge route's own 405 names no cause, so it is never
// what tells a caller the strategy is not this product's.
func TestAStrategyOutsideTheFamilysSetIsRefusedBeforeAnyRequest(t *testing.T) {
	h := newHarness(t, nil)
	_, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{Strategy: "octopus", HeadSHA: testHeadSHA})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeStrategyNotAllowed {
		t.Fatalf("MergePR with strategy %q = %v, want code %q", "octopus", err, forgeapi.CodeStrategyNotAllowed)
	}
	if n := h.instance.count(); n != 0 {
		t.Errorf("MergePR with strategy %q sent %d request(s), want 0: the closed set is checked before any request", "octopus", n)
	}
}

// TestManuallyMergedIsNotAStrategy holds the merge option's one style that merges
// nothing: manually-merged records a merge made outside the forge and names that
// merge's commit in the option's merge_commit_id (Gitea's swagger,
// MergePullRequestOption), which a merge request does not carry, so it is refused
// before any request as a spelling outside the family's set.
func TestManuallyMergedIsNotAStrategy(t *testing.T) {
	h := newHarness(t, nil)
	_, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{Strategy: "manually-merged", HeadSHA: testHeadSHA})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeStrategyNotAllowed {
		t.Fatalf("MergePR with strategy %q = %v, want code %q", "manually-merged", err, forgeapi.CodeStrategyNotAllowed)
	}
	if n := h.instance.count(); n != 0 {
		t.Errorf("MergePR with strategy %q sent %d request(s), want 0: the refusal precedes any request", "manually-merged", n)
	}
}

func TestTheCrossRepositoryListSpendsOneRequestPerPageWhateverARowCarries(t *testing.T) {
	row := strings.Replace(searchRow(false), `"pull_request":`, `"head":{"ref":"example-feature","sha":"`+testHeadSHA+`"},"pull_request":`, 1)
	h := newHarness(t, map[string]string{
		"GET /api/v1/repos/issues/search": "[" + row + "]",
		statusPath:                        statusOK,
	})
	page, err := h.client.ListMyPRs(t.Context())
	if err != nil {
		t.Fatalf("ListMyPRs = %v, want nil", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListMyPRs returned %d row(s), want 1", len(page.Items))
	}
	if got := page.Items[0].Action; got.Checks != forgeapi.CheckUnknown || got.ChecksTotal != 0 {
		t.Errorf("ListMyPRs over a row carrying a head = %v over %d, want %v over 0", got.Checks, got.ChecksTotal, forgeapi.CheckUnknown)
	}
	if got := h.instance.count(); got != 1 {
		t.Errorf("ListMyPRs over a row carrying a head sent %d request(s) %v, want 1", got, h.instance.arrived())
	}
}
