package conformance

import (
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/github"
	"github.com/cplieger/forgeapi/internal/spec"
)

// The live lane's variables, one pair per product: the instance URL and, where the
// case needs a credential, the token. A case skips with the reason when the
// variable it needs is unset, so the default run is the offline one and no case
// silently reaches the network.
//
// A URL with no token runs the ANONYMOUS read cases alone, which is what makes the
// harness runnable against a public instance: the two reads below need no
// credential, and the recordings the fixtures were cut from were taken the same way.
func liveURLVar(p spec.Product) string {
	return "FORGEAPI_LIVE_" + strings.ToUpper(string(p)) + "_URL"
}

func liveTokenVar(p spec.Product) string {
	return "FORGEAPI_LIVE_" + strings.ToUpper(string(p)) + "_TOKEN"
}

// anonymousReads are the operations a public instance of one product answers with
// no credential. It is keyed by PRODUCT, and it has to
// be: the set is a fact about each product's own authorization rather than about
// the operation, and a set shared across the four would claim a read is anonymous
// on a product that refuses it.
//
// Measured anonymously against gitlab.com on 2026-09-20, which is what moved this
// from one set to four. One repository's pull requests answer 200 there over the
// list document, and so does the pipelines route; the COMMIT STATUSES route answers
// 401, as do the labels route, the identity route, the metadata route and the
// cross-repository read, because this product gates those below its Reporter role
// and an anonymous caller is a guest. So the folded-status case is a credential-lane
// case on that product and an anonymous one on the other two.
var anonymousReads = map[spec.Product]map[string]bool{
	// GitHub's set is EMPTY, and that is measured rather than conservative: both of
	// this product's reads here go through a DOCUMENT, and its document surface
	// answers an anonymous caller nothing at all. Measured on 2026-09-20, one
	// unauthenticated POST to that endpoint carrying a query that selects only the
	// budget object answered 403 with the message that the rate limit is exceeded for
	// the caller's address and that an authenticated request gets a higher one, which
	// is the anonymous allowance on that surface being zero rather than a throttle
	// this run provoked; the same address read a public repository over REST at 200 in
	// the same second, so the refusal is the surface's and not the address's. So both
	// GitHub rows are the credentialed lane's, and the container's own read-only token
	// is what runs them.
	spec.GitHub: {},
	spec.GitLab: {
		"PullRequests.ListPRs": true,
		// A public project's pipelines answered the run listing anonymously on
		// 2026-10-01, a page of five with no credential.
		"Checks.ListRuns": true,
	},
	spec.Gitea: {
		"PullRequests.ListPRs": true,
		"Checks.CommitStatus":  true,
	},
	spec.Forgejo: {
		"PullRequests.ListPRs": true,
		"Checks.CommitStatus":  true,
		// A public repository's runs answered the run listing anonymously on
		// 2026-10-01, pages of 50 with page sent and the whole listing's total in
		// the body.
		"Checks.ListRuns": true,
	},
}

// counting wraps the wire so a live case can price a call the same way the offline
// fixture server does, by counting what left rather than what was answered, which is
// what a published price counts. It keeps each request as it left, so a case can
// hold the route a call reached as well as how many requests it sent.
type counting struct {
	next http.RoundTripper
	seen []sent
	n    atomic.Int64
	mu   sync.Mutex
}

func (c *counting) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	c.keep(r)
	return c.next.RoundTrip(r)
}

// keep records one request, its body read through GetBody so the request leaves
// with its own body unread.
func (c *counting) keep(r *http.Request) {
	s := sent{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.Query()}
	if r.GetBody != nil {
		if body, err := r.GetBody(); err == nil {
			s.body, _ = io.ReadAll(io.LimitReader(body, maxFixtureBody))
			_ = body.Close()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, s)
}

// since is the requests that left after the first n.
func (c *counting) since(n int64) []sent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.seen[min(int(n), len(c.seen)):])
}

// liveOptions is the option set a live case passes. The wire is a transport of this
// test's own rather than the default client, both because injection is mandatory
// and because the default one consults the environment's proxy variables, which
// this library never does.
//
// It is called from inside the guarded construction rather than beside it, because
// an option constructor is a published function of this library too and an
// implementation's panics need not be confined to its family packages.
//
// The two per-connection statements a plaintext loopback instance needs are made
// for exactly that instance: a URL over plain HTTP whose host is a loopback address,
// which is a throwaway instance on the same machine and nothing a developer could
// point at by accident. Every other instance is addressed under the library's
// defaults, which refuse plaintext and a private address.
func liveOptions(base string, wire *counting, token string) []forgeapi.Option {
	cred := anonymousCredential()
	if token != "" {
		cred = staticCredential(token)
	}
	opts := []forgeapi.Option{
		forgeapi.WithWireTransport(wire),
		forgeapi.WithCredentialSource(cred),
	}
	if loopbackPlaintext(base) {
		opts = append(opts, forgeapi.WithPlaintextHTTP(true), forgeapi.WithPrivateAddresses(true))
	}
	return opts
}

// loopbackPlaintext reports whether an instance URL is plain HTTP to a loopback
// host: the name localhost, or an address in the loopback range.
func loopbackPlaintext(base string) bool {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// TestLiveConformance runs the same cases as the offline suite against a real
// instance, and it is the lane that reports when a forge has moved under a
// recording.
//
// A live instance answers for its own subject rather than the canonical one, so a
// cell describing the record read is the offline case's alone. What a live case
// asserts is the published price, the three-way error contract and every cell no
// subject of the lane can move: each cell the table says the product cannot supply,
// and each cell the lane fixed itself, a mutation's own writes and the sandbox's
// permanent read subject.
//
// A mutation runs only where its product's sandbox variable names the repository
// the case writes to, so nothing in this suite writes to an instance a developer
// happened to point it at. On each product where one does, the run first removes
// what an aborted run left under the lane's marks and lists the sandbox, and once
// every case has run the listing must match: every case closes what it opened and
// deletes the branches, tags and releases it made.
func TestLiveConformance(t *testing.T) {
	for _, p := range spec.Products {
		bracketSandbox(t, p)
	}
	for _, e := range spec.Table {
		op, ok := operationFor(e.Method)
		if !ok {
			continue
		}
		runLiveEntry(t, e, op)
	}
}

// runLiveEntry runs the lane's cases for one table entry: the entry's own case and,
// on a cross-repository list, the same list under the owner of the product's
// sandbox, because the owner scope is an upstream route of its own on every product
// and a route the lane never sends cannot go red.
func runLiveEntry(t *testing.T, e spec.Entry, op operation) {
	t.Helper()
	t.Run(caseName(e), func(t *testing.T) {
		runLive(t, e, op)
	})
	if slices.Contains(crossRepositoryLists, e.Method) {
		t.Run(caseName(e)+"_owner", func(t *testing.T) {
			runLiveScoped(t, e, op, true)
		})
	}
}

func runLive(t *testing.T, e spec.Entry, op operation) {
	t.Helper()
	runLiveScoped(t, e, op, false)
}

// runLiveScoped is one live case, under the sandbox owner's scope where ownerScoped
// is set. The owner case holds the same price and cells as the viewer's, and the
// route its page request reached.
func runLiveScoped(t *testing.T, e spec.Entry, op operation, ownerScoped bool) {
	t.Helper()
	// The support status gates the live case exactly as it gates the offline one,
	// and the gate sits ABOVE every environment branch: a pending entry has no
	// family behind it, so it skips for the table's own reason rather than for a
	// missing instance, a missing credential or a mutation it could never have
	// reached.
	if pending(e) {
		t.Skip(pendingReason(e))
	}
	if refuses(e) {
		t.Skipf("the table says %s carries no %s verb, so detection refuses the call before any request, which the offline case asserts without an instance", e.Product, e.Method)
	}
	base := os.Getenv(liveURLVar(e.Product))
	if base == "" {
		t.Skipf("%s is unset, so no live instance is named for %s", liveURLVar(e.Product), e.Product)
	}
	s := liveSubject(e.Product)
	if op.mutation {
		if reason := sandboxGate(e.Product, s); reason != "" {
			t.Skipf("%s writes to the instance: %s", e.Method, reason)
		}
	}
	token := os.Getenv(liveTokenVar(e.Product))
	if token == "" && !anonymousReads[e.Product][e.Method] {
		t.Skipf("%s is unset, so only the anonymous read cases run against %s: %s", liveTokenVar(e.Product), e.Product, strings.Join(anonymousReadNames(e.Product), " and "))
	}
	if ownerScoped {
		if s.owner = liveOwner(e.Product); s.owner == "" {
			t.Skipf("%s is unset, so the lane names no owner on %s; the offline owner cases hold that scope", liveVar(e.Product, "SANDBOX"), e.Product)
		}
	}

	if s.repo.Selector == "" {
		t.Skipf("%s names no repository and this product has no public default, so the live subject is unknown", env(e.Product, "REPO", ""))
	}
	if op.readsPR {
		if s.prInvalid != nil {
			t.Fatalf("Setup: %s on %s reads one pull request: %v", e.Method, e.Product, s.prInvalid)
		}
		if s.prUnnamed != "" {
			t.Skipf("%s reads one pull request: %s", e.Method, s.prUnnamed)
		}
	}
	wire := &counting{next: &http.Transport{}}
	conn := forgeapi.Connection{WebBaseURL: base}
	client, err := guard(func() (any, error) { return newClient(e.Product, conn, liveOptions(base, wire, token)...) })
	if err != nil {
		t.Fatalf("Setup: %s client for %s: %v", e.Product, base, err)
	}
	var sandbox *lane
	var live liveMutation
	if op.mutation {
		sandbox = newLane(e.Product, base, token, s.repo.Selector)
		var ok bool
		if live, ok = liveMutations[e.Method]; !ok {
			t.Fatalf("Setup: %s is a mutation with no live setup, so the lane cannot make what it writes against", e.Method)
		}
		if live.prepare != nil {
			live.prepare(t, sandbox, &s)
		}
	}
	if prelude := preludeMethod(e, op); prelude != "" {
		runLivePrelude(t, e, prelude, client, s)
	}
	before := wire.n.Load()

	got, callErr := guard(func() (any, error) { return op.invoke(t.Context(), client, s) })
	spent := int(wire.n.Load() - before)
	if callErr == nil && live.made != nil {
		live.made(t, sandbox, s, got)
	}
	if callErr != nil {
		t.Fatalf("%s on %s against %s = error %v, want nil", e.Method, e.Product, base, callErr)
	}

	items := 1
	if op.items != nil {
		items = op.items(got)
	}
	low, high := price(&e, items)
	// The low arm is a floor only where the governor issued every read the price
	// covers. A read it DEFERRED to hold the mutation reserve, or one the
	// per-interval caps stopped, takes the count BELOW the table rather than above
	// it, which is what the budget rule states and what leaves the N+1 shape the
	// only one this assertion can fail on. A live instance with more open rows than
	// one interval admits reaches that arm; the offline fixture never does.
	if deferred := deferredRows(got); deferred > 0 {
		low = 1
		t.Logf("%s on %s against %s deferred the fold on %d row(s), so the count is bounded above and not below", e.Method, e.Product, base, deferred)
	}
	if spent < low || spent > high {
		t.Errorf("%s on %s against %s sent %d request(s), want %d to %d for %d item(s)",
			e.Method, e.Product, base, spent, low, high, items)
	}
	if s.owner != "" {
		// The price holds how many requests the list sent; the arm holds the page
		// request, the first, which is the one that selects the scope.
		page := wire.since(before)
		checkArm(t, e, ownerArmFor(e.Product, e.Method, s.owner), page[:min(1, len(page))])
		// The sandbox's read subject is open under the owner, so a route that stopped
		// spanning it, or a qualifier matching nothing, answers a page without it.
		if sandboxRows(got, s.repo.Selector) == 0 {
			t.Errorf("%s on %s against %s: the owner route answered no row of the sandbox %s, whose read subject is open under %s",
				e.Method, e.Product, base, s.repo.Selector, s.owner)
		}
	}
	for _, failure := range liveCellFailures(e, op, got, laneFixed(e, op, live, s)) {
		t.Errorf("%s against %s", failure, base)
	}
	t.Logf("%s on %s against %s answered in %d request(s) with %d item(s)", e.Method, e.Product, base, spent, items)
}

// liveCellFailures is what a live answer fails of the cells no subject of the lane
// can move: each cell the table says the product cannot supply, and each cell in
// fixed, held to the value fixed there. Both go through the offline case's own
// comparison. A cannot-supply cell is held on every element its path reads, each
// row of a list and each label of a row, and an element the answer does not hold, a
// row of an empty list or a label of an unlabelled record, states nothing about this
// answer and is not asserted. A fixed cell is asserted whatever the answer holds,
// since what fixed it put that element there, except where the product's own entry
// marks the cell cannot supply: the table's value holds there instead.
func liveCellFailures(e spec.Entry, op operation, got any, fixed map[string]any) []string {
	if op.expect == nil || got == nil {
		return nil
	}
	var held []check
	for _, c := range op.expect(e.Product, got) {
		d, departs := departureFor(&e, c.path)
		want, isFixed := fixed[c.path]
		switch {
		case departs && d.Kind == spec.CannotSupply:
			for _, view := range elementViews(got, c.path) {
				held = append(held, checksAt(op.expect(e.Product, view), c.path)...)
			}
		case isFixed:
			c.want = want
			held = append(held, c)
		}
	}
	failures, _, _ := cellFailures(e, held)
	return distinct(failures)
}

// elementViews is the answer once per element a cell's path reads. Each segment of a
// path is a field of the answer, and one ending in [] is a slice whose first element
// the rest of the path reads, so each view narrows every such slice to one of its
// elements and the case's own check reads that element. A path naming no slice is
// the answer itself, and one naming a slice the answer holds no element of has no
// view.
func elementViews(got any, path string) []any {
	segments := strings.Split(path, ".")
	last := -1
	for i, segment := range segments {
		if strings.HasSuffix(segment, "[]") {
			last = i
		}
	}
	if last < 0 {
		return []any{got}
	}
	var views []any
	for _, v := range narrowed(reflect.ValueOf(got), segments[:last+1]) {
		views = append(views, v.Interface())
	}
	return views
}

// narrowed is v once per element segments reach, each a copy of v holding that
// element alone in every slice along the way. A value the segments do not describe
// is returned as it stands, so the check reads it as the offline case does.
func narrowed(v reflect.Value, segments []string) []reflect.Value {
	if len(segments) == 0 || v.Kind() == reflect.Pointer && v.IsNil() {
		return []reflect.Value{v}
	}
	if v.Kind() == reflect.Pointer {
		var out []reflect.Value
		for _, inner := range narrowed(v.Elem(), segments) {
			p := reflect.New(inner.Type())
			p.Elem().Set(inner)
			out = append(out, p)
		}
		return out
	}
	name, collection := strings.CutSuffix(segments[0], "[]")
	if v.Kind() != reflect.Struct || !v.FieldByName(name).IsValid() {
		return []reflect.Value{v}
	}
	field := v.FieldByName(name)
	with := func(f reflect.Value) reflect.Value {
		c := reflect.New(v.Type()).Elem()
		c.Set(v)
		c.FieldByName(name).Set(f)
		return c
	}
	var out []reflect.Value
	if !collection {
		for _, inner := range narrowed(field, segments[1:]) {
			out = append(out, with(inner))
		}
		return out
	}
	if field.Kind() != reflect.Slice {
		return nil
	}
	for i := range field.Len() {
		for _, inner := range narrowed(field.Index(i), segments[1:]) {
			one := reflect.MakeSlice(field.Type(), 1, 1)
			one.Index(0).Set(inner)
			out = append(out, with(one))
		}
	}
	return out
}

// checksAt is the checks of one path among a case's checks.
func checksAt(checks []check, path string) []check {
	var at []check
	for _, c := range checks {
		if c.path == path {
			at = append(at, c)
		}
	}
	return at
}

// distinct is failures with each repeated line kept once, in order: rows answering
// one value a cell refuses fail it in the same words.
func distinct(failures []string) []string {
	seen := make(map[string]bool, len(failures))
	var out []string
	for _, f := range failures {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// laneFixed is every cell the lane itself fixed on one case's subject, with the
// value it must hold there: what a mutation's preparation and its own request
// fix, or, on a read of the sandbox's permanent subject, what the seeds fix.
func laneFixed(e spec.Entry, op operation, live liveMutation, s subject) map[string]any {
	switch {
	case op.mutation && live.fixes != nil:
		return live.fixes(s)
	case !op.mutation && seeded(e.Product, s) && seedFixes[e.Method] != nil:
		return seedFixes[e.Method](s)
	}
	return nil
}

// The permanent read subject a sandbox holds: a pull request open from a branch
// named seed into main and carrying the lane's label, and an open issue. No case
// writes to either.
const (
	seedBranch = "seed"
	seedBase   = "main"
)

// seeded reports whether the lane names the sandbox's permanent read subject: the
// product's sandbox variable names the subject's repository and its pull-request
// variable names the seed pull request there.
func seeded(p spec.Product, s subject) bool {
	return sandboxGate(p, s) == "" && os.Getenv(liveVar(p, "PR")) != "" && s.prInvalid == nil
}

// seedFixes are the cells the permanent read subject fixes on each read reaching
// it. The one-pull-request reads address the seed pull request, and the
// repository's open pull requests and open issues each hold a seed, so each list
// holds a row and that row is open.
var seedFixes = map[string]func(s subject) map[string]any{
	"PullRequests.ReadPR": func(s subject) map[string]any {
		return map[string]any{
			"State":         forgeapi.PRStateOpen,
			"SourceBranch":  seedBranch,
			"TargetBranch":  seedBase,
			"Labels[].Name": laneLabel(s.product),
		}
	},
	"Merges.MergeStatus": func(subject) map[string]any {
		return map[string]any{"Merged": forgeapi.SupportNo}
	},
	"PullRequests.ListPRs": func(subject) map[string]any {
		return map[string]any{"Items[].State": forgeapi.PRStateOpen}
	},
	"Issues.ListIssues": func(subject) map[string]any {
		return map[string]any{"Items[].State": forgeapi.IssueStateOpen}
	},
}

// deferredRows counts the rows whose own work the governor did not issue, which is
// what separates a count below the table from a count the implementation lost.
// Only a pull-request list carries a per-row fold, so only that answer can report
// one.
// sandboxRows counts the rows of a cross-repository page that live in the repository
// selector names.
func sandboxRows(got any, selector string) int {
	var repos []forgeapi.RepoRef
	switch page := got.(type) {
	case forgeapi.Page[forgeapi.PullRequest]:
		for i := range page.Items {
			repos = append(repos, page.Items[i].Repo)
		}
	case forgeapi.Page[forgeapi.Issue]:
		for i := range page.Items {
			repos = append(repos, page.Items[i].Repo)
		}
	}
	n := 0
	for _, r := range repos {
		if strings.EqualFold(r.Selector, selector) {
			n++
		}
	}
	return n
}

func deferredRows(got any) int {
	page, ok := got.(forgeapi.Page[forgeapi.PullRequest])
	if !ok {
		return 0
	}
	n := 0
	for _, row := range page.Items {
		if row.Partial != nil && (row.Partial.Reason == forgeapi.PartialBudget || row.Partial.Reason == forgeapi.PartialRateLimited) {
			n++
		}
	}
	return n
}

func runLivePrelude(t *testing.T, e spec.Entry, method string, client any, s subject) {
	t.Helper()
	pre, ok := operationFor(method)
	if !ok {
		t.Fatalf("Setup: %s on %s names prelude %q, which the contract declares no case for", e.Method, e.Product, method)
	}
	if inner := innerPrelude(e.Product, method, pre); inner != "" {
		runLivePrelude(t, e, inner, client, s)
	}
	if _, err := guard(func() (any, error) { return pre.invoke(t.Context(), client, s) }); err != nil {
		t.Fatalf("Setup: %s on %s needs %s first, which failed: %v", e.Method, e.Product, method, err)
	}
}

// anonymousReadNames is one product's anonymous set, for a skip message that names
// what would run.
func anonymousReadNames(p spec.Product) []string {
	var out []string
	for method, anonymous := range anonymousReads[p] {
		if anonymous {
			out = append(out, method)
		}
	}
	slices.Sort(out)
	return out
}

// TestLiveAnonymousPullRequests reads one public repository's pull requests with no
// credential, bounded to one page of one row, and holds the answer to what a
// reference must carry whatever instance answered: a number, this family's sigil,
// and the repository the row belongs to.
//
// It is the case that proves the harness reaches a real instance rather than only a
// recording, and it is runnable on a developer's machine against gitea.com and
// codeberg.org, which is where the fixtures for those two products were cut from.
func TestLiveAnonymousPullRequests(t *testing.T) {
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			if !anonymousReads[p]["PullRequests.ListPRs"] {
				t.Skipf("%s answers no repository's pull requests to an anonymous caller, so this read is the credentialed lane's there: set %s", p, liveTokenVar(p))
			}
			client, s, wire := anonymousClient(t, p)
			page, err := guard(func() (any, error) {
				r, ok := client.(forgeapi.PullRequests)
				if !ok {
					return nil, roleAbsent("PullRequests")
				}
				return r.ListPRs(t.Context(), s.repo, forgeapi.WithPageBound(1))
			})
			if err != nil {
				t.Fatalf("ListPRs(%q) = error %v, want nil", s.repo.Selector, err)
			}
			got := page.(forgeapi.Page[forgeapi.PullRequest])
			if len(got.Items) == 0 {
				t.Fatalf("ListPRs(%q) returned no item, want at least one: the subject is a repository with open pull requests", s.repo.Selector)
			}
			row := got.Items[0]
			if row.Ref.Number <= 0 {
				t.Errorf("ListPRs(%q) row 0 = number %d, want a positive one", s.repo.Selector, row.Ref.Number)
			}
			if want := sigil(p); row.Ref.Sigil != want {
				t.Errorf("ListPRs(%q) row 0 = sigil %q, want %q", s.repo.Selector, row.Ref.Sigil, want)
			}
			if row.Repo.Selector != s.repo.Selector {
				t.Errorf("ListPRs(%q) row 0 = repository %q, want %q", s.repo.Selector, row.Repo.Selector, s.repo.Selector)
			}
			if row.Title == "" {
				t.Errorf("ListPRs(%q) row 0 = empty title, want the instance's own", s.repo.Selector)
			}
			t.Logf("ListPRs(%q) answered %d row(s) in %d request(s), row 0 is %s%d %q",
				s.repo.Selector, len(got.Items), wire.n.Load(), row.Ref.Sigil, row.Ref.Number, row.Title)
		})
	}
}

// TestLiveAnonymousCommitStatus reads one public repository's commit status with no
// credential and holds the answer to the two things every product must normalize: a
// folded verdict that is a declared member, and counts that add up to the total.
//
// The two SHAPES the answer can take are asserted separately rather than as their
// union, because the instance decides which one arrives and a run where the
// reference carries no status at all is the ordinary case on a quiet repository. A
// case whose every assertion is satisfied by a wholly zero answer holds nothing on
// that run while looking green, which is the defect the suite's own zero-answer red
// check exists for. So an empty fold is its own arm, stating what an empty
// answer must be, and the log says the run proved the price rather than the fold.
func TestLiveAnonymousCommitStatus(t *testing.T) {
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			if !anonymousReads[p]["Checks.CommitStatus"] {
				t.Skipf("%s gates the folded-status route below a role an anonymous caller does not hold, so this read is the credentialed lane's on that product: set %s", p, liveTokenVar(p))
			}
			client, s, wire := anonymousClient(t, p)
			checks, err := guard(func() (any, error) {
				r, ok := client.(forgeapi.Checks)
				if !ok {
					return nil, roleAbsent("Checks")
				}
				return r.CommitStatus(t.Context(), s.repo, s.ref)
			})
			if err != nil {
				t.Fatalf("CommitStatus(%q, %q) = error %v, want nil", s.repo.Selector, s.ref, err)
			}
			got := checks.(forgeapi.CommitChecks)
			if got.State < forgeapi.CheckUnknown || got.State > forgeapi.CheckNeutral {
				t.Errorf("CommitStatus(%q, %q) = state %v, want a declared CheckState member", s.repo.Selector, s.ref, got.State)
			}
			if sum := got.Passing + got.Failing + got.Pending + got.Neutral + got.Unknown; sum != got.Total {
				t.Errorf("CommitStatus(%q, %q) = counts summing to %d, want the total %d: the counts are one per member so nothing is conflated",
					s.repo.Selector, s.ref, sum, got.Total)
			}
			if len(got.Contexts) == 0 {
				checkEmptyFold(t, s, got)
				t.Logf("CommitStatus(%q, %q) answered %v over 0 context(s) in %d request(s): this instance offered nothing to fold, so this run proved the price and the empty answer's own shape rather than a verdict",
					s.repo.Selector, s.ref, got.State, wire.n.Load())
				return
			}
			checkFoldedAnswer(t, s, got)
			t.Logf("CommitStatus(%q, %q) answered %v over %d context(s) in %d request(s)",
				s.repo.Selector, s.ref, got.State, len(got.Contexts), wire.n.Load())
		})
	}
}

// checkEmptyFold is what a fold over nothing must be: the unknown member, a zero
// total and no partial marker. That is a real statement rather than the absence of
// one, because each of the three could be otherwise: a verdict over no evidence, a
// total from the endpoint's own figure, or a remainder marked where nothing was
// truncated.
func checkEmptyFold(t *testing.T, s subject, got forgeapi.CommitChecks) {
	t.Helper()
	if got.State != forgeapi.CheckUnknown {
		t.Errorf("CommitStatus(%q, %q) = state %v over no context at all, want %v: a verdict over no evidence is a verdict this library invented",
			s.repo.Selector, s.ref, got.State, forgeapi.CheckUnknown)
	}
	if got.Total != 0 {
		t.Errorf("CommitStatus(%q, %q) = total %d over no context, want 0: the total is the rows held rather than the figure the endpoint reported",
			s.repo.Selector, s.ref, got.Total)
	}
	if got.Partial != nil {
		t.Errorf("CommitStatus(%q, %q) = partial %v over no context, want none: nothing was truncated, so a marker here would give a consumer a remainder to chase that does not exist",
			s.repo.Selector, s.ref, got.Partial)
	}
}

// checkFoldedAnswer is what a fold over a real collection must satisfy: the total
// agrees with the rows held or a marker states the remainder, every row carries the
// name a consumer renders, and the verdict is unknown only for a reason the answer
// itself carries. The last of those is what an all-member check cannot state: the
// unknown member is legal, so a case that accepts it unconditionally accepts a fold
// that stopped reading.
func checkFoldedAnswer(t *testing.T, s subject, got forgeapi.CommitChecks) {
	t.Helper()
	if got.Total != len(got.Contexts) && got.Partial == nil {
		t.Errorf("CommitStatus(%q, %q) = total %d over %d context(s) with no partial marker, want either agreement or a stated remainder",
			s.repo.Selector, s.ref, got.Total, len(got.Contexts))
	}
	for i, c := range got.Contexts {
		if c.Name == "" {
			t.Errorf("CommitStatus(%q, %q) context %d carries no name, want the instance's own: the name is what a consumer renders beside the state",
				s.repo.Selector, s.ref, i)
		}
	}
	if got.State == forgeapi.CheckUnknown && got.Unknown == 0 && got.Partial == nil {
		t.Errorf("CommitStatus(%q, %q) = %v over %d context(s) with no unknown row and no partial marker, want a mapped verdict: an unknown here is a fold that read rows and published nothing about them",
			s.repo.Selector, s.ref, got.State, len(got.Contexts))
	}
}

// TestLiveTheSchemaQuestionAtSetupIsAcceptedByARealInstance drives connection setup
// against a real instance, anonymously, and holds the answer to the one thing that
// question exists to settle: whether this instance accepts the documents the family
// ships.
//
// The witness is a PUBLISHED field rather than a flag inside a family package. A
// connection whose question was refused reads REST, and the REST arm reports the armed
// auto-merge unknown because that product's list can serve a value nothing refreshed; a
// connection reading the document fills it from a boolean the selection carries, so it
// is never unknown there. So an anonymous list whose rows answer yes or no is a live
// statement that the question was accepted and the documents are being read.
//
// It runs on the one product whose GraphQL schema an instance can refuse at runtime and
// whose anonymous set carries the list, and it needs only that product's instance URL.
func TestLiveTheSchemaQuestionAtSetupIsAcceptedByARealInstance(t *testing.T) {
	const product = spec.GitLab
	if !anonymousReads[product]["PullRequests.ListPRs"] {
		t.Skipf("%s answers no repository's pull requests to an anonymous caller, so this witness is the credentialed lane's", product)
	}
	client, s, wire := anonymousClient(t, product)
	caps, ok := client.(forgeapi.Capabilities)
	if !ok {
		t.Fatal(roleAbsent("Capabilities"))
	}
	if _, err := guard(func() (any, error) { return caps.ConnectionCaps(t.Context()) }); err != nil {
		t.Fatalf("ConnectionCaps on %s = error %v, want the capabilities: a question the instance refused is an answer rather than a failure", product, err)
	}
	setup := wire.n.Load()
	page, err := guard(func() (any, error) {
		r, ok := client.(forgeapi.PullRequests)
		if !ok {
			return nil, roleAbsent("PullRequests")
		}
		return r.ListPRs(t.Context(), s.repo, forgeapi.WithPageBound(1))
	})
	if err != nil {
		t.Fatalf("ListPRs(%q) = error %v, want nil", s.repo.Selector, err)
	}
	got := page.(forgeapi.Page[forgeapi.PullRequest])
	if len(got.Items) == 0 {
		t.Fatalf("ListPRs(%q) returned no item, want at least one: the subject is a repository with open pull requests", s.repo.Selector)
	}
	if spent := wire.n.Load() - setup; spent != 1 {
		t.Errorf("ListPRs(%q) sent %d request(s) after setup, want 1: the discovery is the connection's cost and this row publishes one", s.repo.Selector, spent)
	}
	if armed := got.Items[0].Action.AutoMergeArmed; armed == forgeapi.SupportUnknown {
		t.Errorf("ListPRs(%q) row 0 = auto-merge %v after a setup of %d request(s), want yes or no: an unknown there is the REST arm, so this instance refused the question the documents' own selection asks",
			s.repo.Selector, armed, setup)
	}
	t.Logf("setup asked %d request(s) and the list then answered rows carrying the document's own auto-merge state, so this instance accepts the documents this family ships", setup)
}

// TestLiveTheVersionSetAtSetupDecidesWhetherThePinRides drives connection setup against
// a real instance of the one product that pins a REST API version, and holds both halves
// of what that resolution decides: setup reads the version set, and the pin rides the
// requests that come after only where that set carried it.
//
// The witness is the wire rather than a flag inside a family package: a recording
// transport reads the header off the request that left. The offline harness drives both
// arms of the decision on instances of its own; what it cannot say is which arm the real
// instance is, and that is the one fact this case measures.
//
// It also goes red on the day this instance retires the pinned version, which is the
// signal the constant's own obligation needs: the remedy is a release of this library and
// no run-time fall-back can take it.
func TestLiveTheVersionSetAtSetupDecidesWhetherThePinRides(t *testing.T) {
	const product = spec.GitHub
	base := os.Getenv(liveURLVar(product))
	token := os.Getenv(liveTokenVar(product))
	if base == "" || token == "" {
		t.Skipf("%s and %s are what name a live instance for %s, and this product answers no read anonymously: measured 2026-09-20, its document surface refuses an unauthenticated caller outright",
			liveURLVar(product), liveTokenVar(product), product)
	}
	pins := &pinRecorder{next: &http.Transport{}}
	wire := &counting{next: pins}
	client, err := guard(func() (any, error) {
		return newClient(product, forgeapi.Connection{WebBaseURL: base}, liveOptions(base, wire, token)...)
	})
	if err != nil {
		t.Fatalf("Setup: %s client: %v", product, err)
	}
	caps, ok := client.(forgeapi.Capabilities)
	if !ok {
		t.Fatal(roleAbsent("Capabilities"))
	}
	if _, capsErr := guard(func() (any, error) { return caps.ConnectionCaps(t.Context()) }); capsErr != nil {
		t.Fatalf("ConnectionCaps on %s = error %v, want the capabilities", product, capsErr)
	}
	setup := pins.seen()
	id, isIdentity := client.(forgeapi.Identity)
	if !isIdentity {
		t.Fatal(roleAbsent("Identity"))
	}
	if _, err := guard(func() (any, error) { return id.Whoami(t.Context()) }); err != nil {
		t.Fatalf("Whoami on %s = error %v, want the account", product, err)
	}
	after := pins.seen()[len(setup):]
	if len(after) != 1 {
		t.Fatalf("Whoami sent %d request(s) after setup, want 1", len(after))
	}
	if after[0] != github.APIVersion {
		t.Errorf("the read after setup carried the version pin %q, want %q: this instance publishes that version, so setup resolved it and every later REST request names it",
			after[0], github.APIVersion)
	}
	t.Logf("setup asked %d request(s) and the read after it carried the pin %q, so this instance serves the version this library names",
		len(setup), after[0])
}

// pinRecorder reads the version pin off every request that leaves, which is the only
// place a request header is observable from here: the library publishes what it answers
// and not what it sent, and what a call SENDS is the property under test.
type pinRecorder struct {
	next http.RoundTripper
	pins []string
	mu   sync.Mutex
}

func (p *pinRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.pins = append(p.pins, r.Header.Get(github.APIVersionHeader))
	p.mu.Unlock()
	return p.next.RoundTrip(r)
}

func (p *pinRecorder) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.pins)
}

// anonymousClient builds the client the two anonymous cases drive, skipping with
// the reason when the instance URL is unset.
func anonymousClient(t *testing.T, p spec.Product) (any, subject, *counting) {
	t.Helper()
	base := os.Getenv(liveURLVar(p))
	if base == "" {
		t.Skipf("%s is unset, so no live instance is named for %s", liveURLVar(p), p)
	}
	s := liveSubject(p)
	if s.repo.Selector == "" || s.ref == "" {
		t.Skipf("no public subject is named for %s: set %s and %s",
			p, "FORGEAPI_LIVE_"+strings.ToUpper(string(p))+"_REPO", "FORGEAPI_LIVE_"+strings.ToUpper(string(p))+"_REF")
	}
	wire := &counting{next: &http.Transport{}}
	client, err := guard(func() (any, error) {
		return newClient(p, forgeapi.Connection{WebBaseURL: base}, liveOptions(base, wire, "")...)
	})
	if err != nil {
		t.Fatalf("Setup: anonymous %s client for %s: %v", p, base, err)
	}
	return client, s, wire
}
