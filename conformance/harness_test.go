package conformance

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// The canonical subject. Every fixture on every product carries these values,
// whatever that product's own wire spelling for them is, which is what lets one
// expected value serve four products. Owner and repository are the placeholder
// the redaction rule names, and no value here is a real account, a real
// repository or a credential.
const (
	owner         = "example"
	repository    = "example"
	selector      = owner + "/" + repository
	prNumber      = 1
	issueNumber   = 1
	headSHA       = "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d"
	prTitle       = "Example pull request"
	prBody        = "Example body."
	author        = "example-user"
	sourceBranch  = "example-feature"
	targetBranch  = "main"
	prWebURL      = "https://forge.example/example/example/pull/1"
	issueTitle    = "Example issue"
	issueBody     = "Example issue body."
	issueWebURL   = "https://forge.example/example/example/issues/1"
	labelName     = "example-label"
	labelDesc     = "Example label."
	repoDesc      = "Example repository."
	repoWebURL    = "https://forge.example/example/example"
	repoCloneURL  = "https://forge.example/example/example.git"
	tagName       = "v1.0.0"
	releaseName   = "Example release"
	releaseBody   = "Example release notes."
	releaseWebURL = "https://forge.example/example/example/releases/tag/v1.0.0"
	accountName   = "Example User"
	accountEmail  = "example-user@forge.example"
	accountWeb    = "https://forge.example/example-user"
	remaining     = 4990
	lastCost      = 1
)

// The canonical times, the instants the fixtures spell in their own products'
// encodings.
var (
	createdAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	updatedAt = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	published = time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
)

// contexts is the canonical check collection, both members passing, so the folded
// verdict is CheckPassing and the merge block reason MergeBlockNone on every
// product. A pending member would fold to one verdict on a product that counts
// contexts and to another on a product whose single scalar cannot say which check
// is running, and that difference is a per-product expectation the table does not
// carry.
var contexts = []forgeapi.CheckContext{
	{Name: "example/build", Description: "Example build.", TargetURL: "https://ci.example/build/1", State: forgeapi.CheckPassing},
	{Name: "example/test", Description: "Example test.", TargetURL: "https://ci.example/test/1", State: forgeapi.CheckPassing},
}

// family maps a product onto the family package that serves it, which is three
// packages for four products: the Gitea family serves Gitea and Forgejo.
func family(p spec.Product) forgeapi.Family {
	switch p {
	case spec.GitHub:
		return forgeapi.FamilyGitHub
	case spec.GitLab:
		return forgeapi.FamilyGitLab
	case spec.Gitea, spec.Forgejo:
		return forgeapi.FamilyGitea
	}
	return forgeapi.FamilyUnknown
}

// sigil is the pull-request sigil a product's family renders, "!" on GitLab and "#"
// on the other two families, because a sigil is a rendering concern that never
// enters a path.
func sigil(p spec.Product) string {
	if p == spec.GitLab {
		return "!"
	}
	return "#"
}

// apiRoot is the path the API base URL adds under the test server, so that the path
// a request arrives at is the one the expectation table publishes: the table spells
// GitHub's routes with no prefix, which is that product's own dotcom addressing,
// and the other two with the v4 and v1 roots.
func apiRoot(p spec.Product) string {
	switch p {
	case spec.GitLab:
		return "/api/v4"
	case spec.Gitea, spec.Forgejo:
		return "/api/v1"
	}
	return ""
}

// repoRef is the canonical repository reference, derived rather than allocated: the
// id is the prefix plus the hex of the lowercased canonical selector, which is what
// lets a consumer mint one from a git remote with no round trip.
func repoRef(p spec.Product) forgeapi.RepoRef {
	return forgeapi.RepoRef{
		ID:          forgeapi.RepoIDPrefix + hex.EncodeToString([]byte(strings.ToLower(selector))),
		Family:      family(p),
		Selector:    selector,
		DisplayPath: selector,
	}
}

// prRef is the canonical pull-request reference.
func prRef(p spec.Product) forgeapi.PRRef {
	return forgeapi.PRRef{Number: prNumber, Sigil: sigil(p)}
}

// subject is what one case addresses: the canonical one offline, and the one the
// live lane's environment names live. It is a parameter rather than a constant
// because an offline fixture answers for example/example and a live instance
// answers for whatever repository the operator pointed the lane at.
//
// The fields after ref are what a mutation writes: the text a created title starts
// with, the label it applies, the two branches a pull request joins, and the tag a
// release is cut at. Offline they are the canonical values; live, the mutation's
// own setup points them at what it made in the sandbox.
//
// prInvalid and prUnnamed say why a live subject holds no pull request a read may
// address: the lane's pull-request variable does not hold a number, or the lane
// named a repository and no pull request on it. Both are empty offline and wherever
// pr is one the lane or the harness vouches for.
type subject struct {
	prInvalid error
	product   spec.Product
	ref       string
	prefix    string
	label     string
	source    string
	target    string
	tag       string
	prUnnamed string
	// owner scopes the cross-repository lists to one owner's open items; empty is
	// the viewer's own.
	owner string
	repo  forgeapi.RepoRef
	pr    forgeapi.PRRef
	issue forgeapi.IssueRef
}

// canonicalSubject is the subject every offline case addresses.
func canonicalSubject(p spec.Product) subject {
	return subject{
		product: p,
		ref:     headSHA,
		label:   labelName,
		source:  sourceBranch,
		target:  targetBranch,
		tag:     tagName,
		repo:    repoRef(p),
		pr:      prRef(p),
		issue:   forgeapi.IssueRef{Number: issueNumber},
	}
}

// liveSubject is the subject a live case addresses, named by the environment so
// that no repository, pull request or commit of anyone's is compiled in.
func liveSubject(p spec.Product) subject {
	s := canonicalSubject(p)
	sel := env(p, "REPO", defaultLiveRepo(p))
	s.repo = forgeapi.RepoRef{
		ID:          forgeapi.RepoIDPrefix + hex.EncodeToString([]byte(strings.ToLower(sel))),
		Family:      family(p),
		Selector:    sel,
		DisplayPath: sel,
	}
	s.ref = env(p, "REF", defaultLiveRef(p))
	s.pr, s.prUnnamed, s.prInvalid = livePullRequest(p)
	return s
}

// livePullRequest is the pull request a live read of one pull request addresses:
// the number the product's PR variable names; on the harness's own public subject,
// where that variable is unset, the canonical number; and on a repository the lane
// named without one, none, with the reason, because nobody said that repository
// holds a pull request at any number. A variable that is set and holds no positive
// number is an error rather than a reason to skip, since a lane that misnames its
// subject would otherwise pass having read nothing.
func livePullRequest(p spec.Product) (forgeapi.PRRef, string, error) {
	prefix := "FORGEAPI_LIVE_" + strings.ToUpper(string(p)) + "_"
	raw := os.Getenv(prefix + "PR")
	if raw == "" {
		if repo := env(p, "REPO", ""); repo != "" {
			return forgeapi.PRRef{}, fmt.Sprintf("%sREPO names %s and %sPR is unset, so no pull request is known to exist there to read", prefix, repo, prefix), nil
		}
		return prRef(p), "", nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return forgeapi.PRRef{}, "", fmt.Errorf("%sPR = %q, want the positive number of the pull request the live reads address", prefix, raw)
	}
	return forgeapi.PRRef{Number: n, Sigil: sigil(p)}, "", nil
}

// env reads one of the live lane's per-product variables.
func env(p spec.Product, suffix, fallback string) string {
	if v := os.Getenv("FORGEAPI_LIVE_" + strings.ToUpper(string(p)) + "_" + suffix); v != "" {
		return v
	}
	return fallback
}

// defaultLiveRepo and defaultLiveRef are one stable public project per product and
// the ref to read on it, so the anonymous lane runs against an instance URL alone
// rather than against three variables. Both are overridable per product.
//
// The subject is a harness PARAMETER rather than a fact about any product, which is
// what makes a default admissible here: it decides which repository the lane reads,
// and every case asserts the price, the contract and the shape rather than that
// repository's own values. The instance URL keeps no default, because it is what
// opts a run into the network at all: the default run of this suite is the offline
// one and no case may reach an instance nobody named.
//
// Each default is a project its own vendor publishes and keeps: on the Gitea family
// the two the anonymous recordings were read from, a GitLab project of that
// product's own organization carrying open merge requests, and on GitHub the
// repository that vendor maintains as its own example.
func defaultLiveRepo(p spec.Product) string {
	switch p {
	case spec.GitHub:
		return "octocat/Hello-World"
	case spec.GitLab:
		return "gitlab-org/editor-extensions/gitlab-jetbrains-plugin"
	case spec.Gitea:
		return "gitea/runner"
	case spec.Forgejo:
		return "forgejo/forgejo"
	}
	return ""
}

func defaultLiveRef(p spec.Product) string {
	switch p {
	case spec.GitHub:
		return "master"
	case spec.GitLab, spec.Gitea:
		return "main"
	case spec.Forgejo:
		return "forgejo"
	}
	return ""
}

// credential is the credential source every case supplies, because injection is
// mandatory and a client built with no source is refused with
// [forgeapi.CodeAnonymousRefused]. An empty
// token is how the anonymous live cases ask for an anonymous read: the refusal
// names a missing SOURCE, and a source holding no token is not a missing one.
type credential struct {
	token string
	kind  forgeapi.CredKind
	state forgeapi.CredState
}

func (c credential) Token(context.Context) (string, error) { return c.token, nil }
func (c credential) Kind() forgeapi.CredKind               { return c.kind }
func (c credential) State() forgeapi.CredState             { return c.state }

// staticCredential is the offline and credentialed-live source: a static token,
// valid, whose value is a placeholder rather than a secret.
func staticCredential(token string) credential {
	return credential{token: token, kind: forgeapi.CredKindStaticPAT, state: forgeapi.CredValid}
}

// anonymousCredential is the source the anonymous live cases supply: present, so
// the client is not refused, and holding nothing, so no credential is sent.
func anonymousCredential() credential {
	return credential{kind: forgeapi.CredKindUnknown, state: forgeapi.CredUnknown}
}

// fixture is one case's offline wire: the provenance line and the routes the server
// answers.
type fixture struct {
	Provenance string `json:"//"`
	path       string
	Routes     []wire `json:"routes"`
}

// wire is one recorded response, matched by method and path, or by being a GraphQL
// request at all where the table names a DOCUMENT rather than a route.
//
// Paths carries the alternatives for an entry whose table row names two routes a
// call may reach in either order, which is the re-run's resolving read beside its
// verb and the connection read's version sources; a row naming ONE route is served
// at one path, because a fixture answering a route the row does not name lets a
// family send the wrong one and be answered anyway. A path segment spelled {}
// matches any one segment, which is how a route carrying an upstream id nothing
// published pins is expressed.
type wire struct {
	Headers map[string]string `json:"headers"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Paths   []string          `json:"paths"`
	Body    json.RawMessage   `json:"body"`
	Status  int               `json:"status"`
	Repeat  int               `json:"repeat"`
	GraphQL bool              `json:"graphql"`
}

// loadFixture reads one case's fixture and holds it to the provenance rule: a
// fixture with no line naming its source capture and the date that capture was read
// is refused rather than served, because a recording whose origin nobody stated is a
// hand-written body wearing a recording's authority.
func loadFixture(product spec.Product, method string) (fixture, error) {
	path := filepath.Join("testdata", strings.ToLower(string(product)), method+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return fixture{}, fmt.Errorf("reading fixture: %w", err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		return fixture{}, fmt.Errorf("decoding %s: %w", path, err)
	}
	f.path = path
	if err := f.checkProvenance(); err != nil {
		return fixture{}, err
	}
	if len(f.Routes) == 0 {
		return fixture{}, fmt.Errorf("%s declares no route", path)
	}
	return f, nil
}

// checkProvenance holds the header comment to what it is for: naming the recording
// the fixture was cut from and the date that recording was read.
//
// It also holds it to being READABLE by whoever reads this repository. A fixture's
// provenance ships with the fixture, so a path under a working directory this
// repository does not carry is a line nobody can resolve, and naming the recording
// and its date says everything the provenance rule asks for without one.
func (f fixture) checkProvenance() error {
	switch {
	case f.Provenance == "":
		return fmt.Errorf("%s carries no provenance line under the %q key", f.path, "//")
	case !strings.Contains(f.Provenance, "cut from"):
		return fmt.Errorf("%s provenance %q does not name the recording it was cut from", f.path, f.Provenance)
	case !strings.Contains(f.Provenance, "read 20"):
		return fmt.Errorf("%s provenance %q names no read date", f.path, f.Provenance)
	case strings.Contains(f.Provenance, "/"):
		return fmt.Errorf("%s provenance %q carries a path, want the recording named: a reader of this repository cannot resolve one", f.path, f.Provenance)
	}
	return nil
}

// matches reports whether one recorded response answers this request.
func (w *wire) matches(r *http.Request, body []byte) bool {
	if w.GraphQL {
		return r.Method == http.MethodPost && isGraphQL(body)
	}
	if r.Method != w.Method {
		return false
	}
	if w.Path != "" && pathMatches(w.Path, r.URL.EscapedPath()) {
		return true
	}
	for _, p := range w.Paths {
		if pathMatches(p, r.URL.EscapedPath()) {
			return true
		}
	}
	return false
}

// pathMatches compares a route against an arriving path, segment by segment, with
// {} matching any one segment. The arriving path is the ESCAPED one, so a family
// that sends GitLab's namespace selector as a numeric id rather than as the encoded
// path fails the match rather than passing it silently.
func pathMatches(route, got string) bool {
	rs := strings.Split(route, "/")
	gs := strings.Split(got, "/")
	if len(rs) != len(gs) {
		return false
	}
	for i := range rs {
		if rs[i] != "{}" && rs[i] != gs[i] {
			return false
		}
	}
	return true
}

// isGraphQL reports whether a request body is a GraphQL request, which is how a
// document is recognized: the table names the DOCUMENT and neither GraphQL product
// has one endpoint path to fix, so a path expectation here would be an
// invention.
func isGraphQL(body []byte) bool {
	var probe struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Query != ""
}

// departureFor returns the table's departure row for one field path of one entry,
// expanding the collapsed row spelling the derivation uses for a RUN of fields.
func departureFor(e *spec.Entry, path string) (spec.Departure, bool) {
	for _, d := range e.Departures {
		if slices.Contains(coveredPaths(d.Field), path) {
			return d, true
		}
	}
	return spec.Departure{}, false
}

// checkRuns are the two ordered runs of count fields a collapsed departure row can
// name, in the order the exported surface declares them: a row reading
// "ChecksPassing .. ChecksTotal" covers every member between its two endpoints, so
// the expansion has to know the order rather than guess it.
var checkRuns = [][]string{
	{"ChecksPassing", "ChecksFailing", "ChecksPending", "ChecksNeutral", "ChecksUnknown", "ChecksTotal"},
	{"Passing", "Failing", "Pending", "Neutral", "Unknown", "Total"},
}

// coveredPaths returns every field path one departure row covers: itself, or the run
// it names. An unrecognized range is returned as itself, so it matches no declared
// path and stays reported as an uncovered departure rather than silently matching
// one.
func coveredPaths(field string) []string {
	from, to, ok := strings.Cut(field, " .. ")
	if !ok {
		return []string{field}
	}
	prefix, start := "", from
	if i := strings.LastIndex(from, "."); i >= 0 {
		prefix, start = from[:i+1], from[i+1:]
	}
	for _, run := range checkRuns {
		i, j := indexOf(run, start), indexOf(run, to)
		if i < 0 || j < i {
			continue
		}
		out := make([]string, 0, j-i+1)
		for _, name := range run[i : j+1] {
			out = append(out, prefix+name)
		}
		return out
	}
	return []string{field}
}

func indexOf(list []string, want string) int {
	for i, s := range list {
		if s == want {
			return i
		}
	}
	return -1
}

// price is what one entry's published request count comes to on the canonical
// fixture, whose every list carries one item: the call's own figure plus the
// per-item figure for the products whose list rows carry no check state and cost one
// read each, failed attempts included, and the per-item ceiling above the high end.
func price(e *spec.Entry, items int) (low, high int) {
	r := e.Requests
	return r.Min + r.PerItem*items, r.Max + (r.PerItem+r.PerItemCeiling)*items
}

// wanted is what one of the table's request facts expects on the wire, resolved
// against the subject the case drove the call with: the JSON a body field must
// carry, or the literal a query parameter must carry.
//
// The table states the KIND of value rather than the value, which is what keeps this
// suite's own canonical subject out of it: a row saying the merge sends the head the
// caller pinned is true of every subject, where a row spelling one SHA would be a
// fact about this fixture.
func wanted(s *spec.Sent, sub *subject) (string, bool) {
	switch s.Holds {
	case spec.HoldsLiteral:
		return s.Value, true
	case spec.HoldsHeadSHA:
		if s.Where == spec.InQuery {
			return sub.ref, true
		}
		return jsonOf(sub.ref), true
	case spec.HoldsLabelNames:
		return jsonOf([]string{labelName}), true
	}
	return "", false
}

// jsonOf is one value as the JSON a request body carries it as, which is what lets
// a body field's arriving value be compared against the table's statement without
// either side deciding how a string or a list is spelled.
func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// bodyField reads one member of a request body, reporting whether it was there at
// all, which is what a row stating an ABSENCE turns on.
//
// The member name is matched case-insensitively, and that is the products' own
// tolerance rather than this suite's laxity: the Gitea family's two documents declare
// the merge strategy member as `do` and as `Do`, and both products decode a body
// through the same library, which matches a member to a field without regard to case.
// A name neither product would accept still fails here.
func bodyField(body []byte, field string) (json.RawMessage, bool) {
	var members map[string]json.RawMessage
	if json.Unmarshal(body, &members) != nil {
		return nil, false
	}
	for name, raw := range members {
		if strings.EqualFold(name, field) {
			return raw, true
		}
	}
	return nil, false
}

// numberList reports whether a body member is a non-empty list of numbers, which is
// what a row stating that a product's creation takes label IDENTIFIERS as its own
// numeric ids asserts: the names a sibling product takes would arrive here as
// strings.
func numberList(raw json.RawMessage) bool {
	var rows []json.Number
	if json.Unmarshal(raw, &rows) != nil {
		return false
	}
	return len(rows) > 0
}

// preludeFor is the capability accessor a case calls before the operation itself,
// derived from the capability the entry needs: a call gated on a capability cannot
// be priced at its published figure unless detection has already run, and the
// accessor's own requests are the accessor's price rather than this operation's.
//
// The connection read is also the prelude of every Gitea-family operation whose
// requests ask for at most the instance's stated maximum page size, for the same
// reason: the connection reads that maximum at setup and prices the read on its own
// row, so the operation's published figure is its figure on a connection already
// set up.
func preludeFor(e *spec.Entry) string {
	switch forgeapi.Capability(e.Needs) {
	case forgeapi.CapRerunChecks:
		return "Capabilities.ConnectionCaps"
	case forgeapi.CapReadMergeState:
		return "Capabilities.GrantCaps"
	}
	if family(e.Product) == forgeapi.FamilyGitea && pagesAtTheStatedMaximum[e.Method] {
		return "Capabilities.ConnectionCaps"
	}
	return ""
}

// pagesAtTheStatedMaximum are the operations whose requests on the Gitea family ask
// for at most the instance's stated maximum page size: every page-numbered list, the
// folded status a pull-request read and a commit's status compute, and the label
// resolution a creation naming labels makes.
var pagesAtTheStatedMaximum = map[string]bool{
	"Repos.ListRepos":        true,
	"PullRequests.ListPRs":   true,
	"PullRequests.ListMyPRs": true,
	"PullRequests.ReadPR":    true,
	"PullRequests.CreatePR":  true,
	"Checks.CommitStatus":    true,
	"Checks.ListRuns":        true,
	"Issues.ListIssues":      true,
	"Issues.ListMyIssues":    true,
	"Issues.CreateIssue":     true,
	"Releases.ListReleases":  true,
	"Labels.ListLabels":      true,
}

// guard runs one operation and turns a panic into an error naming it, so an
// implementation whose methods panic fails each case in turn rather than aborting
// the whole suite at the first one.
func guard(fn func() (any, error)) (v any, err error) {
	defer func() {
		if p := recover(); p != nil {
			v, err = nil, fmt.Errorf("panicked: %v", p)
		}
	}()
	return fn()
}
