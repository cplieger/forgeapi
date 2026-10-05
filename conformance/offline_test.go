package conformance

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/families"
	"github.com/cplieger/forgeapi/gitea"
	"github.com/cplieger/forgeapi/github"
	"github.com/cplieger/forgeapi/gitlab"
	"github.com/cplieger/forgeapi/internal/spec"
)

// maxFixtureBody bounds what the fixture server reads from one request, so a
// runaway body cannot fill the test process.
const maxFixtureBody = 1 << 20

// sent is one request as it arrived: what a price counts, and what a row of the
// table's request facts is asserted against.
type sent struct {
	query  url.Values
	method string
	path   string
	body   []byte
}

func (s sent) String() string { return s.method + " " + s.path }

// recorder is what the fixture server observed: every request that arrived, in
// order, and every request no route answered.
type recorder struct {
	arrived   []sent
	unmatched []string
	spent     []int
	mu        sync.Mutex
}

func (r *recorder) record(s sent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.arrived = append(r.arrived, s)
}

// since is the requests that arrived after the first n, which is the window one
// case's own operation sent: a prelude's requests are the prelude's row, so a
// request fact of this operation cannot be satisfied by one.
func (r *recorder) since(n int) []sent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.arrived[min(n, len(r.arrived)):])
}

func (r *recorder) miss(method, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unmatched = append(r.unmatched, method+" "+path)
}

// count is how many requests have arrived, which is what a published price is
// measured against: every request sent, a refused one included.
func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.arrived)
}

func (r *recorder) misses() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.unmatched)
}

func (r *recorder) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.arrived))
	for _, s := range r.arrived {
		out = append(out, s.String())
	}
	return out
}

// take reserves the first route that answers this request and has uses left.
func (r *recorder) take(f fixture, req *http.Request, body []byte) (wire, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, w := range f.Routes {
		limit := max(w.Repeat, 1)
		if r.spent[i] < limit && f.Routes[i].matches(req, body) {
			r.spent[i]++
			return w, true
		}
	}
	return wire{}, false
}

// newFixtureServer serves one case's fixture and records what arrived. A request
// no route answers is recorded and answered with a status the library maps to a
// failure, so a family reaching an undocumented route fails the case and names the
// route it reached rather than passing on a body it was not meant to see.
func newFixtureServer(t *testing.T, f fixture) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{spent: make([]int, len(f.Routes))}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxFixtureBody))
		if err != nil {
			body = nil
		}
		rec.record(sent{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.Query(), body: body})
		route, ok := rec.take(f, r, body)
		if !ok {
			rec.miss(r.Method, r.URL.EscapedPath())
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"message":"no fixture route for %s %s in %s"}`, r.Method, r.URL.EscapedPath(), f.path)
			return
		}
		for k, v := range route.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(route.Status)
		if _, err := w.Write(route.Body); err != nil {
			t.Errorf("Setup: writing the fixture response for %s %s: %v", r.Method, r.URL.EscapedPath(), err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// newClient builds the family client that serves one product. Gitea and Forgejo
// take the same package, which is the family-versus-product distinction: what
// tells them apart is the instance, and offline that is the fixture.
func newClient(p spec.Product, conn forgeapi.Connection, opts ...forgeapi.Option) (any, error) {
	switch p {
	case spec.GitHub:
		c, err := github.New(conn, opts...)
		if err != nil {
			return nil, err
		}
		return c, nil
	case spec.GitLab:
		c, err := gitlab.New(conn, opts...)
		if err != nil {
			return nil, err
		}
		return c, nil
	case spec.Gitea, spec.Forgejo:
		c, err := gitea.New(conn, opts...)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
	return nil, fmt.Errorf("no family serves product %q", p)
}

// offlineOptions is the option set every offline case passes. Transport injection
// is mandatory and never falls back to the default client, the credential source
// is mandatory too, and the two per-connection statements are what let a case
// address a loopback server over plaintext at all.
func offlineOptions(srv *httptest.Server) []forgeapi.Option {
	return []forgeapi.Option{
		forgeapi.WithWireTransport(srv.Client().Transport),
		forgeapi.WithCredentialSource(staticCredential("conformance-placeholder")),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
}

// operationFor finds the suite's contract for one table method.
func operationFor(method string) (operation, bool) {
	for _, op := range operations {
		if op.method == method {
			return op, true
		}
	}
	return operation{}, false
}

// caseName is one case's subtest name: product and method, with no space and no
// slash, because the runner escapes a space and cannot select a name carrying a
// slash at all.
func caseName(e spec.Entry) string {
	return string(e.Product) + "_" + e.Method
}

// TestOfflineConformance is the suite: one case per operation per product, one per
// table entry, each one asserting the published price, the route or document
// the table names, and the normalized contract with that product's departures
// applied.
//
// Every case FAILS against an implementation whose method bodies panic, and a
// case that passed there would be a case asserting nothing.
func TestOfflineConformance(t *testing.T) {
	for _, e := range spec.Table {
		op, ok := operationFor(e.Method)
		if !ok {
			t.Errorf("spec.Table carries method %q, which the conformance contract declares no case for", e.Method)
			continue
		}
		t.Run(caseName(e), func(t *testing.T) {
			runOffline(t, e, op)
		})
	}
}

// runOffline drives one case.
func runOffline(t *testing.T, e spec.Entry, op operation) {
	t.Helper()
	// The support status gates the case, and the gate sits ABOVE the setup: a
	// pending entry has no family behind it, so its client cannot be built at all
	// and a setup failure there would report the absence of an implementation as a
	// defect in this case's own fixture.
	if pending(e) {
		t.Skip(pendingReason(e))
	}
	f, err := loadFixture(e.Product, e.Method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	srv, rec := newFixtureServer(t, f)
	conn := forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL + apiRoot(e.Product)}
	client, err := guard(func() (any, error) { return newClient(e.Product, conn, offlineOptions(srv)...) })
	if err != nil {
		t.Fatalf("Setup: %s client: %v", e.Product, err)
	}

	s := canonicalSubject(e.Product)
	if prelude := preludeMethod(e, op); prelude != "" {
		runPrelude(t, e, prelude, client, s)
	}
	before := rec.count()

	got, callErr := guard(func() (any, error) { return op.invoke(t.Context(), client, s) })
	spent := rec.count() - before

	if misses := rec.misses(); len(misses) > 0 {
		t.Errorf("%s on %s reached %d route(s) no fixture answers: %v; the fixture serves %s",
			e.Method, e.Product, len(misses), misses, routeSummary(f))
	}
	if refuses(e) {
		checkCapabilityRefusal(t, e, callErr, spent)
		return
	}
	if callErr != nil {
		t.Errorf("%s on %s = error %v, want nil: the requests that arrived were %v", e.Method, e.Product, callErr, rec.requests())
		return
	}
	checkPrice(t, e, op, got, spent, rec.requests())
	checkRoute(t, e, f, rec.since(before))
	checkSends(t, e, s, rec.since(before))
	checkFields(t, e, op, got)
}

// checkSends holds the request this call put on the wire to what the table says it
// sends, once per product.
//
// It is the cross-forge half of a property each family also tests: a merge that arms
// auto-merge unasked, a creation sending label names where the product takes ids and a
// re-run that drops the head pin are all request-shape defects, and an output
// assertion cannot see any of them. A row naming an ABSENCE is asserted over EVERY
// request of the call rather than one, because a field absent from the body and
// present in the query is still sent.
func checkSends(t *testing.T, e spec.Entry, s subject, requests []sent) {
	t.Helper()
	for _, row := range e.Sends {
		if row.Holds == spec.HoldsNothing {
			if req, found := carrier(requests, &row); found {
				t.Errorf("%s on %s sent %s in the %s of %s, want it absent: %s",
					e.Method, e.Product, row.Field, row.Where, req, row.Says)
			}
			continue
		}
		req, found := carrier(requests, &row)
		if !found {
			t.Errorf("%s on %s sent no %s in any request's %s, want it: %s; the requests were %v",
				e.Method, e.Product, row.Field, row.Where, row.Says, requests)
			continue
		}
		got := carried(req, &row)
		if row.Holds == spec.HoldsLabelIDs {
			if !numberList(json.RawMessage(got)) {
				t.Errorf("%s on %s sent %s = %s, want a non-empty list of this product's own numeric ids: %s",
					e.Method, e.Product, row.Field, got, row.Says)
			}
			continue
		}
		want, ok := wanted(&row, &s)
		if !ok {
			t.Errorf("%s on %s names %s holding %q, which this suite resolves to no value",
				e.Method, e.Product, row.Field, row.Holds)
			continue
		}
		if got != want {
			t.Errorf("%s on %s sent %s = %s in the %s of %s, want %s: %s",
				e.Method, e.Product, row.Field, got, row.Where, req, want, row.Says)
		}
	}
}

// carrier is the request one row's field arrived on, and whether it arrived at all.
func carrier(requests []sent, row *spec.Sent) (sent, bool) {
	for _, req := range requests {
		switch row.Where {
		case spec.InQuery:
			if req.query.Has(row.Field) {
				return req, true
			}
		case spec.InBody:
			if _, ok := bodyField(req.body, row.Field); ok {
				return req, true
			}
		}
	}
	return sent{}, false
}

// carried is the value one row's field arrived with, as the JSON a body carries or
// the literal a query carries.
func carried(req sent, row *spec.Sent) string {
	if row.Where == spec.InQuery {
		return req.query.Get(row.Field)
	}
	raw, _ := bodyField(req.body, row.Field)
	return string(raw)
}

// decides are the operations whose REQUEST carries a decision at 1.0: the merge, the
// two creations, the re-run, and the run listing, which reads the re-run's own route
// and must leave off the head filter the re-run sends there. It is the gate over the
// table's own completeness, because a Sends row deleted from the derivation would
// leave every case green while the property it held went unchecked, which is the
// shape of defect this column was added for.
var decides = map[string]bool{
	"Merges.MergePR":                 true,
	"PullRequests.CreatePR":          true,
	"Issues.CreateIssue":             true,
	"PullRequests.RerunFailedChecks": true,
	"Checks.ListRuns":                true,
}

// TestEveryRequestDecisionTheDesignNamesIsStated holds the table to the operations
// above in both directions: each one states what it sends on every product whose
// family serves it, and no other operation states one, because a request carrying no
// decision has nothing here to state.
func TestEveryRequestDecisionTheDesignNamesIsStated(t *testing.T) {
	for _, e := range spec.Table {
		switch {
		case !decides[e.Method]:
			if len(e.Sends) > 0 {
				t.Errorf("%s on %s states %d request fact(s), want none: this operation's request carries no decision a caller makes",
					e.Method, e.Product, len(e.Sends))
			}
		case e.Support != spec.Supported:
			// A pending column has no code to send anything and an unsupported one
			// is refused before any request, so neither owes a row: what each owes
			// is the support status it already carries.
			continue
		case len(e.Sends) == 0:
			t.Errorf("%s on %s is supported and states no request fact, want the fields a wrong default would change silently",
				e.Method, e.Product)
		}
	}
}

// preludeMethod is the operation a case runs before its own, either the capability
// accessor the entry's capability implies or the read the operation declares.
func preludeMethod(e spec.Entry, op operation) string {
	if op.prelude != "" {
		return op.prelude
	}
	return preludeFor(&e)
}

// runPrelude runs the preceding operation whose answer the case's own operation
// needs: a resolved capability, or a response carrying the budget signal the
// governor reports. Its failure is reported as a setup failure, because a case
// whose prelude did not run is asserting nothing about its own operation.
//
// A prelude whose own case runs on a connection already set up runs after that
// setup, so it costs what its own case prices it at: a governor read reporting the
// price of the list it ran first reports that list's published figure only then.
func runPrelude(t *testing.T, e spec.Entry, method string, client any, s subject) {
	t.Helper()
	pre, ok := operationFor(method)
	if !ok {
		t.Fatalf("Setup: %s on %s names prelude %q, which the contract declares no case for", e.Method, e.Product, method)
	}
	if inner := innerPrelude(e.Product, method, pre); inner != "" {
		runPrelude(t, e, inner, client, s)
	}
	if _, err := guard(func() (any, error) { return pre.invoke(t.Context(), client, s) }); err != nil {
		t.Fatalf("Setup: %s on %s needs %s first, which failed: %v", e.Method, e.Product, method, err)
	}
}

// innerPrelude is the connection read where a case's prelude runs on a connection
// already set up in its own case, and empty otherwise. Only that setup runs ahead of
// a prelude: it changes no answer a case reads, only what a page asks for and so
// what the prelude costs, where another accessor's answer is itself what a case
// reads.
func innerPrelude(p spec.Product, method string, pre operation) string {
	inner, ok := entryFor(p, method)
	if !ok {
		return ""
	}
	if prelude := preludeMethod(inner, pre); prelude == connectionRead {
		return prelude
	}
	return ""
}

// connectionRead is the operation every connection's setup is.
const connectionRead = "Capabilities.ConnectionCaps"

// setupRoutes are the routes of the connection read one case runs first, from that
// read's own fixture, for an instance the case stands up itself rather than from the
// case's fixture; empty where the case runs no connection read.
func setupRoutes(t *testing.T, e spec.Entry) []wire {
	t.Helper()
	op, ok := operationFor(e.Method)
	if !ok || preludeMethod(e, op) != connectionRead {
		return nil
	}
	f, err := loadFixture(e.Product, connectionRead)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return f.Routes
}

// refuses reports whether detection refuses this operation on this product, which
// the table states as an UNSUPPORTED cell: the product carries no such verb, so the
// capability answers no and the call is refused before any request. That
// is a fact about the product, so no implementation changes it, and the entry's own
// departure row says what is absent.
func refuses(e spec.Entry) bool {
	return e.Support == spec.Unsupported
}

// pending reports whether the table claims no support for this entry yet, which is
// what makes its case a claim about code that does not run. An entry flips to
// supported only in the change that makes its case pass, so a green whole-module
// run means the table is honest and a red one means a supported claim is false.
func pending(e spec.Entry) bool {
	return e.Support == spec.Pending
}

// pendingReason names the product and what owes the case, and it distinguishes the
// two states a pending entry can be in: one whose route the table settles and whose
// family nobody has implemented, and one whose route is not settled at all, which
// spec.Entry's own godoc spells as an empty Exercises. Nothing about either is
// assertable, and the second is the family implementer's own decision to settle.
func pendingReason(e spec.Entry) string {
	if e.Exercises == "" && e.Requests.Max == 0 {
		return fmt.Sprintf("the table settles no route for %s on %s, so there is no price, route or refusal to hold it to; the phase that lands the %s family owes this case and settling that row is the same phase's own decision",
			e.Method, e.Product, e.Product)
	}
	return fmt.Sprintf("%s on %s is pending: the table claims no support yet, so the phase that lands the %s family owes this case",
		e.Method, e.Product, e.Product)
}

// checkCapabilityRefusal holds the refusal to its ruled code, payload and price: one
// dedicated code carrying the operation, the capability and the evidence, with no
// request on the wire.
func checkCapabilityRefusal(t *testing.T, e spec.Entry, err error, spent int) {
	t.Helper()
	if spent != 0 {
		t.Errorf("%s on %s sent %d request(s), want 0: detection refuses the call before any request", e.Method, e.Product, spent)
	}
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Errorf("%s on %s = error %v, want a *forgeapi.Error with code %q", e.Method, e.Product, err, forgeapi.CodeCapabilityUnsupported)
		return
	}
	if fe.Code != forgeapi.CodeCapabilityUnsupported {
		t.Errorf("%s on %s = code %q, want %q", e.Method, e.Product, fe.Code, forgeapi.CodeCapabilityUnsupported)
	}
	if want := forgeapi.Capability(e.Needs); fe.Capability != want {
		t.Errorf("%s on %s = capability %q, want %q", e.Method, e.Product, fe.Capability, want)
	}
	if want := family(e.Product); fe.Family != want {
		t.Errorf("%s on %s = family %v, want %v", e.Method, e.Product, fe.Family, want)
	}
	if fe.Status != 0 {
		t.Errorf("%s on %s = status %d, want 0: no request was sent", e.Method, e.Product, fe.Status)
	}
	if fe.Op == "" {
		t.Errorf("%s on %s = empty Op, want the role method's own spelling: this refusal names its operation", e.Method, e.Product)
	}
	if fe.DiagID == "" {
		t.Errorf("%s on %s = empty DiagID, want one: this refusal names an operation, so it carries a diagnostic id", e.Method, e.Product)
	}
	if fe.Evidence.Source == forgeapi.EvidenceUnknown {
		t.Errorf("%s on %s = unknown evidence source, want the source detection held for the verdict", e.Method, e.Product)
	}
}

// asForgeError reports whether err is this library's own error type, which is one
// of the three answers every operation can give.
func asForgeError(err error, out **forgeapi.Error) bool {
	fe, ok := err.(*forgeapi.Error)
	if !ok {
		return false
	}
	*out = fe
	return true
}

// checkPrice holds the call to the published request count, which is what makes an
// accidental extra read a build failure rather than a slow poller.
func checkPrice(t *testing.T, e spec.Entry, op operation, got any, spent int, arrived []string) {
	t.Helper()
	items := 1
	if op.items != nil {
		items = op.items(got)
	}
	low, high := price(&e, items)
	if spent < low || spent > high {
		t.Errorf("%s on %s sent %d request(s), want %d to %d for %d item(s): %v",
			e.Method, e.Product, spent, low, high, items, arrived)
	}
	if op.items != nil && items != 1 {
		t.Errorf("%s on %s returned %d item(s), want 1: the fixture carries one", e.Method, e.Product, items)
	}
}

// checkRoute holds the first request the OPERATION sent, a prelude's excluded, to the
// route the table names, PATH included: a fixture can serve a route the row does not
// name, so comparing the method alone would leave the path claim the table publishes
// held by nothing here.
//
// A table row naming a DOCUMENT names it by NAME rather than by a path, because the
// design fixes no endpoint path for either GraphQL product. That name is what the
// request's own operationName has to carry, so an operation wired to another of its
// family's documents fails this case instead of being answered by a fixture that
// accepts any document at all.
func checkRoute(t *testing.T, e spec.Entry, f fixture, arrived []sent) {
	t.Helper()
	if len(arrived) == 0 {
		if e.Requests.Min > 0 {
			t.Errorf("%s on %s sent no request, want the route the table names: %q", e.Method, e.Product, e.Exercises)
		}
		return
	}
	first := arrived[0]
	for _, w := range f.Routes {
		if w.GraphQL {
			checkDocument(t, e, first)
			return
		}
	}
	named := tablePaths(&e)
	if len(named) == 0 {
		// A row that names no route at all: a capability read answered from a
		// header that rode traffic already sent, or a governor read answered from
		// a signal that did.
		return
	}
	for _, w := range f.Routes {
		if first.method != w.Method {
			continue
		}
		if slices.ContainsFunc(named, func(p string) bool { return pathMatches(p, first.path) }) {
			return
		}
	}
	t.Errorf("%s on %s reached %q first, want one of the routes the table names in %q: %v",
		e.Method, e.Product, first, e.Exercises, named)
}

// checkDocument holds one document request to the document the table names.
func checkDocument(t *testing.T, e spec.Entry, first sent) {
	t.Helper()
	want, ok := tableDocument(&e)
	if !ok {
		return
	}
	var probe struct {
		OperationName string `json:"operationName"`
	}
	if err := json.Unmarshal(first.body, &probe); err != nil {
		t.Errorf("%s on %s sent a body no document could be read from: %v", e.Method, e.Product, err)
		return
	}
	if probe.OperationName != want {
		t.Errorf("%s on %s sent the %q document, want %q: that is the document the table names in %q",
			e.Method, e.Product, probe.OperationName, want, e.Exercises)
	}
}

// checkFields holds the answer to the normalized contract, with the table's
// departures applied per field.
func checkFields(t *testing.T, e spec.Entry, op operation, got any) {
	t.Helper()
	if op.zero != nil && reflect.DeepEqual(got, op.zero) {
		t.Errorf("%s on %s answered no error and a wholly zero %T, want a populated answer", e.Method, e.Product, got)
	}
	failures, unmeasured, _ := fieldFailures(e, op, got)
	for _, f := range failures {
		t.Error(f)
	}
	if len(unmeasured) > 0 {
		t.Logf("%s on %s leaves %d field(s) unasserted, each one a table row marking the cell unmeasured: %v",
			e.Method, e.Product, len(unmeasured), unmeasured)
	}
}

// fieldFailures is the field comparison itself, returning what it would report
// rather than reporting it, so the same machinery serves the suite and the red
// check that holds the suite to rejecting a wrong answer.
func fieldFailures(e spec.Entry, op operation, got any) (failures, unmeasured []string, positive int) {
	if op.expect == nil || got == nil {
		return nil, nil, 0
	}
	return cellFailures(e, op.expect(e.Product, got))
}

// cellFailures compares each check against its cell, and it is the one comparison
// both lanes apply, so a cell is asserted one way offline and live.
//
// The departure kind decides the comparison. A cell the table marks unmeasured is
// not asserted at all and is returned as such, because nothing measured it and a
// value here would be a guess. A field the product cannot supply is compared
// against the value that product answers instead. A field that merely differs in
// spelling or in shape is compared against the check's own value, since
// normalizing it is the whole job.
func cellFailures(e spec.Entry, checks []check) (failures, unmeasured []string, positive int) {
	for _, c := range checks {
		d, departs := departureFor(&e, c.path)
		if statesAPresence(c, d, departs, e.Product) {
			positive++
		}
		switch {
		case departs && d.Kind == spec.NotMeasured:
			unmeasured = append(unmeasured, c.path+" ("+d.Evidence+")")
			continue
		case departs && d.Kind == spec.CannotSupply:
			want := c.none
			if by, ok := c.noneBy[e.Product]; ok {
				want = by
			}
			if !reflect.DeepEqual(c.got, want) {
				failures = append(failures, fmt.Sprintf("%s on %s: %s = %v, want %v: the table says this product cannot supply it, %q",
					e.Method, e.Product, c.path, c.got, want, d.Says))
			}
		case c.nonZero:
			if isZero(c.got) {
				failures = append(failures, fmt.Sprintf("%s on %s: %s = %v, want a populated value: the normalized contract fixes that it arrives and not how it is spelled",
					e.Method, e.Product, c.path, c.got))
			}
		default:
			if !reflect.DeepEqual(c.got, c.want) {
				failures = append(failures, fmt.Sprintf("%s on %s: %s = %v, want %v", e.Method, e.Product, c.path, c.got, c.want))
			}
		}
	}
	return failures, unmeasured, positive
}

// statesAPresence reports whether one check expects a value that is not its type's
// zero, which is what decides whether a zero answer can refute it. A check that
// expects an ABSENCE is satisfied by a zero answer however wrong the answer is, so
// counting those as assertions would let a case read as red-checked when nothing
// about it can go red.
func statesAPresence(c check, d spec.Departure, departs bool, p spec.Product) bool {
	switch {
	case departs && d.Kind == spec.NotMeasured:
		return false
	case departs && d.Kind == spec.CannotSupply:
		want := c.none
		if by, ok := c.noneBy[p]; ok {
			want = by
		}
		return !isZero(want)
	case c.nonZero:
		return true
	}
	return !isZero(c.want)
}

// TestEveryCaseRejectsAZeroAnswer is the suite's own red check: every case is
// handed the zero value of its answer type and must report at least one failure.
//
// A case that reports nothing there asserts nothing anywhere, which is the one
// defect a suite written before the implementation cannot otherwise see: against an
// implementation whose constructor panics, every case fails for the same reason, and a
// case whose field checks were vacuous would look exactly like one whose checks
// were real.
func TestEveryCaseRejectsAZeroAnswer(t *testing.T) {
	for _, e := range spec.Table {
		op, ok := operationFor(e.Method)
		if !ok || op.zero == nil {
			continue
		}
		t.Run(caseName(e), func(t *testing.T) {
			failures, unmeasured, positive := fieldFailures(e, op, op.zero)
			if positive == 0 {
				t.Logf("%s on %s states no field PRESENCE, so a zero answer cannot refute it: %d cell(s) are unmeasured in the table and the rest state an absence; what this case holds is the price, the route and the arrival of an answer: %v",
					e.Method, e.Product, len(unmeasured), unmeasured)
				return
			}
			if len(failures) == 0 {
				t.Errorf("%s on %s states %d field presence(s) and reports nothing against a zero answer, so those checks cannot fail",
					e.Method, e.Product, positive)
			}
		})
	}
}

// TestEveryAssertableDepartureIsAsserted holds the suite to the claim that it
// asserts each product's departures: every table row that is not an unmeasured
// cell names a field, and some case's contract has to declare that field, or the
// row is a statement about this product nothing checks.
//
// It runs against the contract alone rather than against a client, so it reports
// the gap even on a tree whose operations panic.
func TestEveryAssertableDepartureIsAsserted(t *testing.T) {
	for _, e := range spec.Table {
		op, ok := operationFor(e.Method)
		if !ok {
			continue
		}
		if refuses(e) {
			// A refused operation answers an error and nothing else, so its
			// departure is asserted by checkCapabilityRefusal rather than by a
			// field comparison: that is where the code, the capability, the
			// family and the zero price are held.
			continue
		}
		declared := map[string]bool{}
		for _, c := range op.expect(e.Product, op.zero) {
			declared[c.path] = true
		}
		for _, path := range op.errorArms {
			declared[path] = true
		}
		for _, d := range e.Departures {
			if d.Kind == spec.NotMeasured {
				continue
			}
			if !slices.ContainsFunc(coveredPaths(d.Field), func(p string) bool { return declared[p] }) {
				t.Errorf("%s on %s departs at %q (%s, %q) and no case asserts that field; the contract declares %v",
					e.Method, e.Product, d.Field, d.Kind, d.Says, slices.Sorted(maps.Keys(declared)))
			}
		}
	}
}

// TestEveryTableEntryHasOneCase holds the suite's shape to the table's: one case
// per operation per product, with no operation the table carries and the contract
// does not, and none the contract carries and the table does not.
func TestEveryTableEntryHasOneCase(t *testing.T) {
	seen := map[string]int{}
	for _, e := range spec.Table {
		seen[e.Method]++
	}
	for _, op := range operations {
		if n := seen[op.method]; n != len(spec.Products) {
			t.Errorf("the contract declares %s, which spec.Table carries %d times, want %d, one per product",
				op.method, n, len(spec.Products))
		}
	}
	for method := range seen {
		if _, ok := operationFor(method); !ok {
			t.Errorf("spec.Table carries %s, which the conformance contract declares no case for", method)
		}
	}
	if len(spec.Table) != len(operations)*len(spec.Products) {
		t.Errorf("spec.Table holds %d entries, want %d, one per operation per product",
			len(spec.Table), len(operations)*len(spec.Products))
	}
}

// TestGiteaFamilyListsTheMergeStrategiesItsRecordSwitchesOn holds
// [forgeapi.RepoAffordances.MergeStrategies] to what the repository allows on the
// family whose record carries one flag per merge style: a style is listed when its
// flag is true and never when it is false or absent, and the manually-merged style,
// which records a merge made outside the forge and takes a commit
// [forgeapi.MergeRequest] does not carry, is not a strategy whatever its flag says.
// The flag-to-style pairs are Gitea's swagger's (`Repository` and
// `MergePullRequestOption`).
func TestGiteaFamilyListsTheMergeStrategiesItsRecordSwitchesOn(t *testing.T) {
	op, ok := operationFor("Capabilities.RepoAffordances")
	if !ok {
		t.Fatal("Setup: the contract declares no case for Capabilities.RepoAffordances")
	}
	allOn := map[string]bool{
		"allow_merge_commits": true, "allow_squash_merge": true, "allow_rebase": true,
		"allow_rebase_explicit": true, "allow_fast_forward_only_merge": true, "allow_manual_merge": true,
	}
	for _, test := range []struct {
		name  string
		flags map[string]bool
		want  []string
	}{
		{name: "captured_record", want: []string{"merge", "squash"}},
		{name: "every_style_on", flags: allOn, want: []string{"fast-forward-only", "merge", "rebase", "rebase-merge", "squash"}},
		{
			name:  "merge_commits_off_rebase_on",
			flags: map[string]bool{"allow_merge_commits": false, "allow_rebase": true},
			want:  []string{"rebase", "squash"},
		},
	} {
		for _, p := range []spec.Product{spec.Gitea, spec.Forgejo} {
			t.Run(string(p)+"_"+test.name, func(t *testing.T) {
				f, err := loadFixture(p, op.method)
				if err != nil {
					t.Fatalf("Setup: %v", err)
				}
				setRecordFlags(t, &f, test.flags)
				srv, _ := newFixtureServer(t, f)
				conn := forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL + apiRoot(p)}
				client, err := guard(func() (any, error) { return newClient(p, conn, offlineOptions(srv)...) })
				if err != nil {
					t.Fatalf("Setup: %s client: %v", p, err)
				}
				answer, err := guard(func() (any, error) { return op.invoke(t.Context(), client, canonicalSubject(p)) })
				if err != nil {
					t.Fatalf("%s on %s = error %v, want the repository record's affordances", op.method, p, err)
				}
				affordances, isRecord := answer.(forgeapi.RepoAffordances)
				if !isRecord {
					t.Fatalf("%s on %s answered %T, want a forgeapi.RepoAffordances", op.method, p, answer)
				}
				got := slices.Sorted(slices.Values(affordances.MergeStrategies))
				if !slices.Equal(got, test.want) {
					t.Errorf("%s on %s with flags %v = MergeStrategies %v, want %v", op.method, p, test.flags, got, test.want)
				}
			})
		}
	}
}

// setRecordFlags sets each named flag on the repository record a fixture serves, only
// where the record carries the key: a product whose record has no such flag stays
// without it.
func setRecordFlags(t *testing.T, f *fixture, flags map[string]bool) {
	t.Helper()
	for i := range f.Routes {
		var record map[string]any
		if json.Unmarshal(f.Routes[i].Body, &record) != nil {
			continue
		}
		if _, isRecord := record["allow_squash_merge"]; !isRecord {
			continue
		}
		for key, on := range flags {
			if _, carried := record[key]; carried {
				record[key] = on
			}
		}
		body, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("Setup: re-encode the record: %v", err)
		}
		f.Routes[i].Body = body
	}
}

// TestFamiliesOpenReturnsTheCoreRole holds the one aggregate the factory publishes:
// a consumer that opens a connection receives the Core role, and the two optional
// roles are reached by a type assertion rather than by a product name.
func TestFamiliesOpenReturnsTheCoreRole(t *testing.T) {
	f, err := loadFixture(spec.Gitea, "Capabilities.ConnectionCaps")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	srv, _ := newFixtureServer(t, f)
	conn := forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL + apiRoot(spec.Gitea)}
	got, err := guard(func() (any, error) {
		core, fam, openErr := families.Open(t.Context(), conn, offlineOptions(srv)...)
		if openErr != nil {
			return nil, openErr
		}
		if fam != forgeapi.FamilyGitea {
			return nil, fmt.Errorf("families.Open = family %v, want %v", fam, forgeapi.FamilyGitea)
		}
		return core, nil
	})
	if err != nil {
		t.Fatalf("families.Open = error %v, want a Core", err)
	}
	if _, ok := got.(forgeapi.Core); !ok {
		t.Errorf("families.Open returned %T, want a value satisfying forgeapi.Core", got)
	}
}

// zeroLike is the zero value of whatever type its argument carries, which is the
// default answer for a field a product cannot supply: the unknown member is the
// zero of every enum in this library.
func zeroLike(v any) any {
	if v == nil {
		return nil
	}
	return reflect.Zero(reflect.TypeOf(v)).Interface()
}

// isZero reports whether a value is its type's zero.
func isZero(v any) bool {
	if v == nil {
		return true
	}
	return reflect.ValueOf(v).IsZero()
}

// routeSummary names the routes one fixture serves, for a failure message about a
// request none of them answered.
func routeSummary(f fixture) string {
	var out []string
	for _, w := range f.Routes {
		if w.GraphQL {
			out = append(out, "POST <a GraphQL document>")
			continue
		}
		paths := w.Path
		if len(w.Paths) > 0 {
			paths = strings.Join(append([]string{w.Path}, w.Paths...), " or ")
		}
		out = append(out, w.Method+" "+paths)
	}
	return strings.Join(out, ", ")
}
