package gitlab

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// The REST answers this file drives.
const (
	projectBody = `{"path_with_namespace": "example/group/example", "description": "Example repository.",
  "web_url": "https://forge.example/example/group/example",
  "http_url_to_repo": "https://forge.example/example/group/example.git",
  "last_activity_at": "2026-01-02T00:00:00Z", "visibility": "public", "default_branch": "main",
  "issues_enabled": true, "issues_access_level": "enabled",
  "permissions": {"project_access": {"access_level": 40}, "group_access": null},
  "merge_method": "merge", "squash_option": "default_off", "merge_trains_enabled": true}`

	issueBody = `{"iid": 1, "state": "opened", "title": "Example issue", "description": "Example issue body.",
  "author": {"username": "example-user"},
  "web_url": "https://forge.example/example/group/example/-/issues/1",
  "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z",
  "labels": [{"name": "example-label", "color": "#ededed", "description": "Example label."}]}`

	releaseBody = `{"tag_name": "v1.0.0", "name": "Example release", "description": "Example release notes.",
  "released_at": "2026-01-03T00:00:00Z", "upcoming_release": false,
  "_links": {"self": "https://forge.example/example/group/example/-/releases/v1.0.0"},
  "commit": {"id": "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d"}}`

	metadataBody = `{"version": "18.4.0", "enterprise": false, "revision": "abcdef1"}`

	pipelinesBody = `[{"id": 77, "sha": "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d", "status": "failed", "ref": "main"}]`

	// pipelinesMovedHeadBody is the same route answering a row whose head is NOT the
	// one the caller pinned, which is what an instance that ignores the sha filter, a
	// proxy that drops the query, or a merge request that moved since the row was
	// rendered puts on the wire. The id is nonzero so the refusal cannot come from the
	// zero-id arm instead.
	pipelinesMovedHeadBody = `[{"id": 78, "sha": "0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f60718293", "status": "failed", "ref": "main"}]`

	mergedBody = `{"iid": 1, "state": "merged", "title": "Example pull request", "description": "Example body.",
  "author": {"username": "example-user"},
  "web_url": "https://forge.example/example/group/example/-/merge_requests/1",
  "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z", "draft": false,
  "labels": ["example-label"], "source_branch": "example-feature", "target_branch": "main",
  "sha": "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d",
  "merge_status": "can_be_merged", "detailed_merge_status": "mergeable",
  "merged_at": "2026-01-02T00:00:00Z", "references": {"full": "example/group/example!1"}}`
)

// The routes this file's harnesses answer, spelled with the ENCODED selector: a family
// that sent the decoded namespace path would arrive at three path segments and match
// none of these.
const (
	projectRouteKey   = "GET /api/v4/projects/" + testEncoded
	issuesRoute       = "GET /api/v4/projects/" + testEncoded + "/issues"
	releasesRoute     = "GET /api/v4/projects/" + testEncoded + "/releases"
	labelsRoute       = "GET /api/v4/projects/" + testEncoded + "/labels"
	statusesRoute     = "GET /api/v4/projects/" + testEncoded + "/repository/commits/" + testHeadSHA + "/statuses"
	mergeRoute        = "PUT /api/v4/projects/" + testEncoded + "/merge_requests/1/merge"
	pipelinesRoute    = "GET /api/v4/projects/" + testEncoded + "/pipelines"
	retryRoute        = "POST /api/v4/projects/" + testEncoded + "/pipelines/77/retry"
	movedRetryRoute   = "POST /api/v4/projects/" + testEncoded + "/pipelines/78/retry"
	metadataRoute     = "GET /api/v4/metadata"
	myMergeRoute      = "GET /api/v4/merge_requests"
	myIssuesRoute     = "GET /api/v4/issues"
	groupMergeRoute   = "GET /api/v4/groups/example%2Fgroup/merge_requests"
	groupIssuesRoute  = "GET /api/v4/groups/example%2Fgroup/issues"
	projectsListRoute = "GET /api/v4/projects"
)

// crossIssueBody is one issue as a cross-repository list answers it, carrying its own
// project in the full reference under the issue sigil, where a merge request's uses
// its own.
const crossIssueBody = `{"iid": 1, "state": "opened", "title": "Example issue", "description": "Example issue body.",
  "author": {"username": "example-user"},
  "web_url": "https://forge.example/example/group/example/-/issues/1",
  "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z",
  "labels": [{"name": "example-label", "color": "#ededed", "description": "Example label."}],
  "project_id": 1, "references": {"full": "example/group/example#1"}}`

// runRows is one project's pipelines as the run listing reads them: a run its
// configuration names, and a run whose name is null and whose ref is the refs/ path a
// merge request's pipeline runs for.
const runRows = `[
  {"id": 77, "iid": 7, "project_id": 1, "sha": "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d", "ref": "main",
   "status": "failed", "source": "push", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z",
   "web_url": "https://forge.example/example/group/example/-/pipelines/77", "name": "Nightly"},
  {"id": 76, "iid": 6, "project_id": 1, "sha": "0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f60718293", "ref": "refs/merge-requests/1/head",
   "status": "running", "source": "merge_request_event", "created_at": "2026-01-01T00:00:00Z",
   "updated_at": "2026-01-01T00:10:00Z", "web_url": "https://forge.example/example/group/example/-/pipelines/76", "name": null}
]`

// statusRows is one commit's statuses as this product answers them: the HISTORY, with a
// context that failed and was retried appearing twice. A fold over the rows as they
// arrive reports the superseded failure as live.
const statusRows = `[
  {"id": 1, "name": "example/build", "description": "Example build.", "target_url": "https://ci.example/build/1",
   "status": "failed", "sha": "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d"},
  {"id": 2, "name": "example/build", "description": "Example build.", "target_url": "https://ci.example/build/2",
   "status": "success", "sha": "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d"},
  {"id": 3, "name": "example/test", "description": "Example test.", "target_url": "https://ci.example/test/1",
   "status": "success", "sha": "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d"}
]`

// TestEveryRepositoryRouteCarriesTheSelectorAsOneEncodedSegment holds this family's
// whole addressing shape at once, over every operation that names a repository.
//
// The selector is a NESTED namespace path and this product's API takes it as ONE path
// segment with the separators encoded. Measured on gitlab.com, both other spellings
// answer 404: a decoded path reads as extra path segments and a doubly-encoded one
// resolves no project. The escaped path is what the instance records here, so a family
// that got either wrong fails by name.
func TestEveryRepositoryRouteCarriesTheSelectorAsOneEncodedSegment(t *testing.T) {
	h := newHarness(t, map[string]string{
		documentRoute:     docRead,
		projectRouteKey:   projectBody,
		issuesRoute:       "[" + issueBody + "]",
		releasesRoute:     "[" + releaseBody + "]",
		labelsRoute:       `[{"name": "example-label", "color": "#ededed", "description": "Example label."}]`,
		statusesRoute:     statusRows,
		projectsListRoute: "[" + projectBody + "]",
	})
	repo := testRef()
	for name, call := range map[string]func() error{
		"RepoAffordances": func() error { _, err := h.client.RepoAffordances(t.Context(), repo); return err },
		"ListIssues":      func() error { _, err := h.client.ListIssues(t.Context(), repo); return err },
		"ListReleases":    func() error { _, err := h.client.ListReleases(t.Context(), repo); return err },
		"ListLabels":      func() error { _, err := h.client.ListLabels(t.Context(), repo); return err },
		"CommitStatus":    func() error { _, err := h.client.CommitStatus(t.Context(), repo, testHeadSHA); return err },
	} {
		if err := call(); err != nil {
			t.Errorf("%s = %v, want the answer: the route it reached is one no fixture serves", name, err)
		}
	}
	for _, sent := range h.instance.arrived() {
		if strings.Contains(sent, "/projects/example/group/example") {
			t.Errorf("%s reached the instance with the selector DECODED, which this product reads as three path segments and answers 404 to", sent)
		}
		if strings.Contains(sent, "%252F") {
			t.Errorf("%s reached the instance with the selector doubly encoded, which resolves no project", sent)
		}
	}
}

// TestTheSelectorsTwoPunctuationBytesSurviveTheEncoderUnescaped holds the byte class the
// canonical subject cannot reach. A selector may carry '+' and '~', and the two encoders
// this route could have been written with disagree on exactly those two: the path
// encoder leaves both as themselves while the query encoder writes %2B and %7E, so
// swapping them is behaviour-preserving against every other case here and changes the
// project this route addresses.
func TestTheSelectorsTwoPunctuationBytesSurviveTheEncoderUnescaped(t *testing.T) {
	const (
		selector = "ex+ample/gr~oup/exam+ple"
		encoded  = "ex+ample%2Fgr~oup%2Fexam+ple"
	)
	route := "GET /api/v4/projects/" + encoded + "/labels"
	h := newHarness(t, map[string]string{
		route: `[{"name": "example-label", "color": "#ededed", "description": "Example label."}]`,
	})
	repo := forgeapi.RepoRef{Family: forgeapi.FamilyGitLab, Selector: selector, DisplayPath: selector}
	repo.ID = repo.Encode()
	if _, err := h.client.ListLabels(t.Context(), repo); err != nil {
		t.Fatalf("ListLabels over a selector carrying + and ~ = %v, want the answer: the route it reached is one no fixture serves", err)
	}
	sent := h.instance.arrived()
	if len(sent) != 1 || sent[0] != route {
		t.Fatalf("the instance saw %v, want [%q]: the two bytes travel as themselves in a path segment, and %%2B or %%7E there addresses a project whose name carries those literal sequences", sent, route)
	}
}

// TestEveryRequestCarriesTheCredential holds the injection every call owes, and it is
// the case a dropped credential header fails. A read that works anonymously on a public
// project would otherwise pass every other assertion here while silently sending
// nothing, and the first private instance would be where it was discovered.
func TestEveryRequestCarriesTheCredential(t *testing.T) {
	h := newHarness(t, map[string]string{
		documentRoute:     docList,
		projectsListRoute: "[" + projectBody + "]",
		issuesRoute:       "[" + issueBody + "]",
		metadataRoute:     metadataBody,
	})
	h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
	if _, err := h.client.ListPRs(t.Context(), testRef()); err != nil {
		t.Fatalf("ListPRs = %v", err)
	}
	if _, err := h.client.ListRepos(t.Context()); err != nil {
		t.Fatalf("ListRepos = %v", err)
	}
	if _, err := h.client.ListIssues(t.Context(), testRef()); err != nil {
		t.Fatalf("ListIssues = %v", err)
	}
	if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
		t.Fatalf("ConnectionCaps = %v", err)
	}
	sent := h.instance.credentials()
	if len(sent) < 4 {
		t.Fatalf("the instance saw %d request(s), want at least four: one per call above", len(sent))
	}
	for i, header := range sent {
		if want := "Bearer " + testToken; header != want {
			t.Errorf("request %d (%s) carried Authorization %q, want %q: this product refuses the token scheme, and a read that works anonymously hides that until the first private instance",
				i, h.instance.arrived()[i], header, want)
		}
	}
}

// TestTheFoldDeDuplicatesToTheLatestRowPerContext holds the one thing this product's
// statuses route makes necessary and no field of it reports: the route answers the
// HISTORY, so a context that failed and was retried appears twice and a fold over the
// rows as they arrive publishes the superseded failure as the live verdict.
//
// The case is what a fold trusting the endpoint fails on: the rows say failing, the
// live state of every context is success, and only the de-duplication tells them apart.
func TestTheFoldDeDuplicatesToTheLatestRowPerContext(t *testing.T) {
	h := newHarness(t, map[string]string{statusesRoute: statusRows})
	checks, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA)
	if err != nil {
		t.Fatalf("CommitStatus = %v, want the fold", err)
	}
	if got := len(checks.Contexts); got != 2 {
		t.Fatalf("CommitStatus = %d context(s) over three rows, want 2: the route answers the history and one context appears twice", got)
	}
	if checks.State != forgeapi.CheckPassing {
		t.Errorf("CommitStatus = %v, want %v: the superseded failure is not this commit's live state, and reporting it renders a green commit red",
			checks.State, forgeapi.CheckPassing)
	}
	if checks.Failing != 0 {
		t.Errorf("CommitStatus = %d failing, want 0: the failing row was retried and the retry succeeded", checks.Failing)
	}
	if checks.Passing != 2 || checks.Total != 2 {
		t.Errorf("CommitStatus = %d passing over %d, want 2 over 2", checks.Passing, checks.Total)
	}
	if checks.Contexts[0].Name != "example/build" {
		t.Errorf("CommitStatus = first context %q, want the order upstream first mentioned it in", checks.Contexts[0].Name)
	}
	if checks.Contexts[0].TargetURL != "https://ci.example/build/2" {
		t.Errorf("CommitStatus = first context's target %q, want the LATEST row's, since that is the run a human would open",
			checks.Contexts[0].TargetURL)
	}
	if checks.Partial != nil {
		t.Errorf("CommitStatus = partial %v over a complete fold, want none", checks.Partial)
	}
}

// TestAFoldThatReachedItsBoundReportsTheCapRatherThanAVerdict holds the other half of
// the fold rule: a fold still short at the published page bound answers the unknown
// member with the cap's marker, rather than a verdict over part of the evidence.
//
// The witness for a remainder is this product's own next-page header rather than a full
// page, which is what lets the bound be reached on a page of any size.
func TestAFoldThatReachedItsBoundReportsTheCapRatherThanAVerdict(t *testing.T) {
	h := newHarness(t, map[string]string{statusesRoute: statusRows}, forgeapi.WithStatusPages(2))
	h.instance.answerHeader(headerNextPage, "2")
	checks, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA)
	if err != nil {
		t.Fatalf("CommitStatus = %v, want the bounded fold", err)
	}
	if h.instance.count() != 2 {
		t.Errorf("CommitStatus sent %d request(s), want 2: the bound is the published page cap and nothing past it is issued", h.instance.count())
	}
	if checks.State != forgeapi.CheckUnknown {
		t.Errorf("CommitStatus = %v over a truncated fold, want %v: a verdict here is a verdict over part of the evidence",
			checks.State, forgeapi.CheckUnknown)
	}
	if checks.Partial == nil || checks.Partial.Reason != forgeapi.PartialPaginationCap {
		t.Errorf("CommitStatus = partial %v, want the pagination cap: without it the unknown verdict has no cause", checks.Partial)
	}
	if h.spy.times("PartialResult:pagination_cap") == 0 {
		t.Error("a truncated fold fired no counter, so a partial no consumer's metrics can see")
	}
}

// TestAZeroPageBoundIsTheLiteralZeroTheOptionPublishes holds the single configuration
// door: a zero passed to an option is the literal zero, so a bound admitting no page
// issues NOTHING and says so, rather than being clamped up to one.
//
// Two options are asserted, because the two answer differently by design: a zero page
// SIZE is refused before any request, and a zero page COUNT is an answer with no rows
// and a continuation naming the page nobody asked for.
func TestAZeroPageBoundIsTheLiteralZeroTheOptionPublishes(t *testing.T) {
	t.Run("a_zero_page_size_is_refused_before_any_request", func(t *testing.T) {
		h := newHarness(t, map[string]string{issuesRoute: "[" + issueBody + "]"})
		_, err := h.client.ListIssues(t.Context(), testRef(), forgeapi.WithPageBound(0))
		var fe *forgeapi.Error
		if !asForgeError(err, &fe) {
			t.Fatalf("ListIssues with a zero page bound = %v, want a *forgeapi.Error", err)
		}
		if fe.Code != forgeapi.CodePageBoundInvalid {
			t.Errorf("ListIssues = code %q, want %q", fe.Code, forgeapi.CodePageBoundInvalid)
		}
		if h.instance.count() != 0 {
			t.Errorf("ListIssues sent %d request(s), want 0: upstream answers an item-less page beside a claim that there is more, so a zero is refused at the call site rather than sent",
				h.instance.count())
		}
	})
	t.Run("a_zero_page_count_issues_no_page_and_names_the_remainder", func(t *testing.T) {
		h := newHarness(t, map[string]string{issuesRoute: "[" + issueBody + "]"}, forgeapi.WithListPages(0))
		page, err := h.client.ListIssues(t.Context(), testRef())
		if err != nil {
			t.Fatalf("ListIssues with a zero page count = %v, want an empty page", err)
		}
		if h.instance.count() != 0 {
			t.Errorf("ListIssues sent %d request(s), want 0: a list that sent one page anyway would do the opposite of what the option says",
				h.instance.count())
		}
		if len(page.Items) != 0 {
			t.Errorf("ListIssues = %d row(s), want 0", len(page.Items))
		}
		if want := restCursor(t, h.client, opListIssues, testRef(), 1); page.Next != want {
			t.Errorf("ListIssues = next %q, want %q: the remainder has to be reachable without re-running a call this bound refuses",
				page.Next, want)
		}
		if page.Partial == nil || page.Partial.Reason != forgeapi.PartialPaginationCap {
			t.Errorf("ListIssues = partial %v, want the pagination cap", page.Partial)
		}
	})
	t.Run("a_zero_status_page_count_folds_nothing", func(t *testing.T) {
		h := newHarness(t, map[string]string{statusesRoute: statusRows}, forgeapi.WithStatusPages(0))
		checks, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA)
		if err != nil {
			t.Fatalf("CommitStatus with a zero status page count = %v, want the unknown verdict", err)
		}
		if h.instance.count() != 0 {
			t.Errorf("CommitStatus sent %d request(s), want 0", h.instance.count())
		}
		if checks.State != forgeapi.CheckUnknown || checks.Partial == nil {
			t.Errorf("CommitStatus = %v with partial %v, want the unknown verdict and the cap's marker", checks.State, checks.Partial)
		}
	})
}

// TestEveryOperationSpendsThePriceItPublishes holds each call to the request count the
// expectation table publishes, which is what makes an accidental extra read a build
// failure rather than a slow poller.
//
// Every figure here is the table's own. The two that are not one are stated as such: the
// re-run resolves a server-allocated pipeline before it can retry it, and the capability
// accessor is cached with the connection, so its price is paid once.
func TestEveryOperationSpendsThePriceItPublishes(t *testing.T) {
	routes := map[string]string{
		documentRoute:     docRead,
		projectsListRoute: "[" + projectBody + "]",
		projectRouteKey:   projectBody,
		issuesRoute:       "[" + issueBody + "]",
		releasesRoute:     "[" + releaseBody + "]",
		labelsRoute:       `[{"name": "example-label", "color": "#ededed", "description": "Example label."}]`,
		statusesRoute:     statusRows,
		myMergeRoute:      "[" + restMergeRequestBody + "]",
		groupMergeRoute:   "[" + restMergeRequestBody + "]",
		myIssuesRoute:     "[" + crossIssueBody + "]",
		groupIssuesRoute:  "[" + crossIssueBody + "]",
		metadataRoute:     metadataBody,
		pipelinesRoute:    pipelinesBody,
		retryRoute:        `{}`,
		mergeRoute:        mergedBody,
		"GET /api/v4/user": `{"username": "example-user", "name": "Example User",
  "email": "example-user@forge.example", "web_url": "https://forge.example/example-user"}`,
		"POST /api/v4/projects/" + testEncoded + "/issues":          issueBody,
		"PUT /api/v4/projects/" + testEncoded + "/issues/1":         issueBody,
		"POST /api/v4/projects/" + testEncoded + "/merge_requests":  restMergeRequestBody,
		"PUT /api/v4/projects/" + testEncoded + "/merge_requests/1": restMergeRequestBody,
		"POST /api/v4/projects/" + testEncoded + "/releases":        releaseBody,
	}
	repo := testRef()
	for _, test := range []struct {
		name string
		call func(*harness) error
		want int
	}{
		{"Whoami", func(h *harness) error { _, err := h.client.Whoami(t.Context()); return err }, 1},
		{"ListRepos", func(h *harness) error { _, err := h.client.ListRepos(t.Context()); return err }, 1},
		{"ListPRs", func(h *harness) error {
			h.instance.serve(documentRoute, docList)
			_, err := h.client.ListPRs(t.Context(), repo)
			return err
		}, 1},
		{"ListMyPRs", func(h *harness) error { _, err := h.client.ListMyPRs(t.Context()); return err }, 1},
		{"ListMyPRs_under_an_owner", func(h *harness) error {
			_, err := h.client.ListMyPRs(t.Context(), forgeapi.WithOwner("example/group"))
			return err
		}, 1},
		{"ListMyIssues", func(h *harness) error { _, err := h.client.ListMyIssues(t.Context()); return err }, 1},
		{"ListMyIssues_under_an_owner", func(h *harness) error {
			_, err := h.client.ListMyIssues(t.Context(), forgeapi.WithOwner("example/group"))
			return err
		}, 1},
		{"ListRuns", func(h *harness) error { _, err := h.client.ListRuns(t.Context(), repo); return err }, 1},
		{"ReadPR", func(h *harness) error { _, err := h.client.ReadPR(t.Context(), repo, testPR()); return err }, 1},
		{"CreatePR", func(h *harness) error {
			_, err := h.client.CreatePR(t.Context(), repo, forgeapi.NewPullRequest{
				Title: "Example pull request", SourceBranch: "example-feature", TargetBranch: "main",
				Labels: []string{"example-label"},
			})
			return err
		}, 1},
		{"ClosePR", func(h *harness) error { _, err := h.client.ClosePR(t.Context(), repo, testPR()); return err }, 1},
		{"ReopenPR", func(h *harness) error { _, err := h.client.ReopenPR(t.Context(), repo, testPR()); return err }, 1},
		{"MergePR", func(h *harness) error {
			_, err := h.client.MergePR(t.Context(), repo, testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA})
			return err
		}, 1},
		{"MergeStatus", func(h *harness) error { _, err := h.client.MergeStatus(t.Context(), repo, testPR()); return err }, 1},
		{"CommitStatus", func(h *harness) error {
			_, err := h.client.CommitStatus(t.Context(), repo, testHeadSHA)
			return err
		}, 1},
		{"ListIssues", func(h *harness) error { _, err := h.client.ListIssues(t.Context(), repo); return err }, 1},
		{"CreateIssue", func(h *harness) error {
			_, err := h.client.CreateIssue(t.Context(), repo, forgeapi.NewIssue{Title: "Example issue"})
			return err
		}, 1},
		{"CloseIssue", func(h *harness) error {
			_, err := h.client.CloseIssue(t.Context(), repo, forgeapi.IssueRef{Number: 1})
			return err
		}, 1},
		{"ConnectionCaps", func(h *harness) error { _, err := h.client.ConnectionCaps(t.Context()); return err }, 2},
		{"GrantCaps", func(h *harness) error { _, err := h.client.GrantCaps(t.Context()); return err }, 0},
		{"RepoAffordances", func(h *harness) error { _, err := h.client.RepoAffordances(t.Context(), repo); return err }, 1},
		{"ListReleases", func(h *harness) error { _, err := h.client.ListReleases(t.Context(), repo); return err }, 1},
		{"CreateRelease", func(h *harness) error {
			_, err := h.client.CreateRelease(t.Context(), repo, forgeapi.NewRelease{TagName: "v1.0.0"})
			return err
		}, 1},
		{"ListLabels", func(h *harness) error { _, err := h.client.ListLabels(t.Context(), repo); return err }, 1},
		{"RerunFailedChecks", func(h *harness) error {
			if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
				return err
			}
			before := h.instance.count()
			err := h.client.RerunFailedChecks(t.Context(), repo, testPR(), testHeadSHA)
			if spent := h.instance.count() - before; spent != 2 {
				t.Errorf("RerunFailedChecks sent %d request(s) after the capability was resolved, want 2: the retry verb is addressed by a pipeline id, so the head has to be resolved first",
					spent)
			}
			return err
		}, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, routes)
			h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
			if err := test.call(h); err != nil {
				t.Fatalf("%s = %v, want the answer", test.name, err)
			}
			if got := h.instance.count(); got != test.want {
				t.Errorf("%s sent %d request(s), want %d: %v", test.name, got, test.want, h.instance.arrived())
			}
		})
	}
}

// TestTheCapabilityAccessorIsResolvedOncePerConnection holds the accessor's own
// ceiling, which is what an extra request per call would break: the metadata read is
// paid once and every later call answers from what the client holds.
func TestTheCapabilityAccessorIsResolvedOncePerConnection(t *testing.T) {
	h := newHarness(t, map[string]string{
		metadataRoute:  metadataBody,
		documentRoute:  docAcceptanceAnswer,
		pipelinesRoute: pipelinesBody,
		retryRoute:     `{}`,
	})
	h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
	for range 3 {
		if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
			t.Fatalf("ConnectionCaps = %v", err)
		}
	}
	if err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA); err != nil {
		t.Fatalf("RerunFailedChecks = %v", err)
	}
	metadata, questions := 0, 0
	for _, sent := range h.instance.arrived() {
		switch sent {
		case metadataRoute:
			metadata++
		case documentRoute:
			questions++
		}
	}
	if metadata != 1 {
		t.Errorf("the metadata route was read %d time(s), want 1: the accessor's price is one request per connection and every later caller answers from what the client holds", metadata)
	}
	// The schema question is the other half of the same ceiling, and it is the half a
	// per-call reading would make expensive rather than merely wrong: the question is
	// the connection's, so a second one on the same connection is a request nobody's
	// row prices.
	if questions != 1 {
		t.Errorf("the schema question was asked %d time(s), want 1: it settles a property of the connection, not of a call", questions)
	}
}

// TestTheReRunCostsTheSameOnAFreshConnectionAsOnAUsedOne holds the figure this
// operation's row publishes against the two connections a consumer actually has.
//
// A re-run that resolved the capability itself spent one figure on a connection whose
// setup had run and another on one whose had not, which makes an exact price impossible
// on either. The capability is the CONNECTION's: it is read and priced at setup, so this
// call reads the verdict it holds, and on a connection that holds none this product's
// own default answers, because the retry verb is on every version in the supported set
// and refusing there would disable a control that works on every instance of it.
func TestTheReRunCostsTheSameOnAFreshConnectionAsOnAUsedOne(t *testing.T) {
	routes := map[string]string{
		metadataRoute:  metadataBody,
		documentRoute:  docAcceptanceAnswer,
		pipelinesRoute: pipelinesBody,
		retryRoute:     `{}`,
	}
	for _, test := range []struct {
		name  string
		setup func(*testing.T, *harness)
	}{
		{name: "fresh_nothing_has_run_on_this_connection"},
		{
			name: "used_setup_has_run_and_the_verdict_is_held",
			setup: func(t *testing.T, h *harness) {
				t.Helper()
				if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
					t.Fatalf("Setup: ConnectionCaps = %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, routes)
			h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
			if test.setup != nil {
				test.setup(t, h)
			}
			before := h.instance.count()
			if err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA); err != nil {
				t.Fatalf("RerunFailedChecks = %v, want the retry", err)
			}
			spent := h.instance.count() - before
			if spent != 2 {
				t.Errorf("RerunFailedChecks sent %d request(s), want 2: the resolving read and the retry, which is every request this row prices", spent)
			}
			for _, sent := range h.instance.arrived()[before:] {
				if sent == metadataRoute || sent == documentRoute {
					t.Errorf("RerunFailedChecks reached %s, so it bought a connection property this call's row does not price", sent)
				}
			}
		})
	}
}

// TestAMergeRefusesEveryShapeItCannotSendHonestly holds the four local refusals a merge
// on this product owes, each before any request.
//
// The strategy refusal is this product's own: its merge endpoint takes a squash flag and
// an auto-merge flag and NO strategy at all, and its project merge method is read-only
// display text, so a caller naming one would have it silently dropped.
func TestAMergeRefusesEveryShapeItCannotSendHonestly(t *testing.T) {
	for _, test := range []struct {
		name string
		req  forgeapi.MergeRequest
		code string
	}{
		{name: "no_head_commit", req: forgeapi.MergeRequest{}, code: forgeapi.CodeMissingSHA},
		{
			name: "a_head_commit_that_is_not_safe_in_a_path",
			req:  forgeapi.MergeRequest{HeadSHA: "../../../etc"},
			code: forgeapi.CodeRefInvalid,
		},
		{
			name: "a_strategy_this_product_cannot_send",
			req:  forgeapi.MergeRequest{HeadSHA: testHeadSHA, Strategy: "rebase"},
			code: forgeapi.CodeStrategyNotAllowed,
		},
		{
			name: "a_strategy_that_is_this_product_s_own_merge_method",
			req:  forgeapi.MergeRequest{HeadSHA: testHeadSHA, Strategy: "merge"},
			code: forgeapi.CodeStrategyNotAllowed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{mergeRoute: mergedBody})
			_, err := h.client.MergePR(t.Context(), testRef(), testPR(), test.req)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) {
				t.Fatalf("MergePR = %v, want a *forgeapi.Error", err)
			}
			if fe.Code != test.code {
				t.Errorf("MergePR = code %q, want %q", fe.Code, test.code)
			}
			if h.instance.count() != 0 {
				t.Errorf("MergePR sent %d request(s), want 0: this refusal is reached before anything is sent", h.instance.count())
			}
			if fe.DiagID != "" {
				t.Errorf("MergePR = diagnostic id %q, want none: a local refusal names no operation for one to correlate", fe.DiagID)
			}
		})
	}
}

// TestAMergeArmsAutoMergeOnlyWhenAsked holds the defect this family replaces: the tool
// it supersedes armed auto-merge by DEFAULT, so a merge the user asked for now completed
// later, unattended, after the user had left.
//
// The body is read rather than the outcome, because the outcome is the same either way
// on an instance that can merge immediately: what differs is what the instance was told.
func TestAMergeArmsAutoMergeOnlyWhenAsked(t *testing.T) {
	for _, test := range []struct {
		name string
		ask  bool
	}{
		{name: "disarmed_unless_asked"},
		{name: "armed_when_asked", ask: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{mergeRoute: mergedBody})
			if _, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{
				HeadSHA:   testHeadSHA,
				AutoMerge: test.ask,
			}); err != nil {
				t.Fatalf("MergePR = %v, want the outcome", err)
			}
			body := h.instance.body(mergeRoute)
			armed := strings.Contains(body, `"auto_merge"`)
			if armed != test.ask {
				t.Errorf("MergePR sent %s, auto-merge armed %v, want %v: arming it unasked completes a merge after the user has left",
					body, armed, test.ask)
			}
			if strings.Contains(body, "merge_when_pipeline_succeeds") {
				t.Errorf("MergePR sent %s, which names the parameter this product deprecated rather than its successor", body)
			}
			if !strings.Contains(body, `"sha":"`+testHeadSHA+`"`) {
				t.Errorf("MergePR sent %s, want the head commit pinned: a merge without it merges whatever the head is now", body)
			}
		})
	}
}

// TestAMergedAnswerIsTheMergeAndAnythingElseIsNotGuessed holds the three outcome states
// this product can reach and the one it must not invent: an answer whose state this
// family cannot read is an UNKNOWN outcome, named and counted, rather than a merge.
func TestAMergedAnswerIsTheMergeAndAnythingElseIsNotGuessed(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		want  forgeapi.MergeOutcomeState
		named bool
	}{
		{name: "a_merged_state_is_the_merge", body: mergedBody, want: forgeapi.MergeOutcomeMerged},
		{
			name: "an_armed_auto_merge_is_the_enqueue",
			body: strings.Replace(restMergeRequestBody, `"merge_when_pipeline_succeeds": false`, `"merge_when_pipeline_succeeds": true`, 1),
			want: forgeapi.MergeOutcomeEnqueued,
		},
		{
			name:  "anything_else_is_unknown_rather_than_a_merge",
			body:  restMergeRequestBody,
			want:  forgeapi.MergeOutcomeUnknown,
			named: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{mergeRoute: test.body})
			out, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA})
			if err != nil {
				t.Fatalf("MergePR = %v, want the outcome", err)
			}
			if out.State != test.want {
				t.Errorf("MergePR = state %v, want %v", out.State, test.want)
			}
			if out.QueuePosition != forgeapi.QueuePositionUnknown {
				t.Errorf("MergePR = queue position %d, want %d: no merge answer of this product carries one, and a zero reads as next to merge",
					out.QueuePosition, forgeapi.QueuePositionUnknown)
			}
			if out.QueueState != forgeapi.QueueNone {
				t.Errorf("MergePR = queue state %v, want %v: the only fields that would answer otherwise are a deprecated experiment the schema gate refuses",
					out.QueueState, forgeapi.QueueNone)
			}
			if named := h.spy.times("UnknownEnumValue:merge outcome state (rest)") > 0; named != test.named {
				t.Errorf("MergePR named the unreadable state %v, want %v: an answer nothing mapped has to be visible as that rather than as a merge", named, test.named)
			}
		})
	}
}

// TestTheReRunRefusesAHeadThePipelinesDoNotCarry holds ONE of the pin's two arms: the
// route answered no row at all, so the loop found nothing to retry. That is the arm an
// instance applying the sha filter upstream takes when the merge request has moved.
//
// The other arm is the local comparison, held by
// TestTheReRunRefusesAPipelineRowCarryingAnotherHead, and the two are separate because
// an empty page reaches the refusal without ever evaluating the head at all.
func TestTheReRunRefusesAHeadThePipelinesDoNotCarry(t *testing.T) {
	h := newHarness(t, map[string]string{
		metadataRoute:  metadataBody,
		pipelinesRoute: `[]`,
		retryRoute:     `{}`,
	})
	h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
	err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA)
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("RerunFailedChecks against a head no pipeline carries = %v, want a *forgeapi.Error", err)
	}
	if fe.Code != forgeapi.CodeStaleHead {
		t.Errorf("RerunFailedChecks = code %q, want %q", fe.Code, forgeapi.CodeStaleHead)
	}
	for _, sent := range h.instance.arrived() {
		if sent == retryRoute {
			t.Error("RerunFailedChecks retried a pipeline for a head the caller did not pin, which can carry deployment side effects on a commit the caller never saw")
		}
	}
	if got := h.instance.query(pipelinesRoute, "sha"); got != testHeadSHA {
		t.Errorf("the resolving read sent sha %q, want the caller's pin %q: upstream declares that filter, so the comparison here is a second check rather than the only one",
			got, testHeadSHA)
	}
}

// TestTheReRunRefusesAPipelineRowCarryingAnotherHead holds the arm the empty page never
// reaches: the route answered a row, and its head is not the one the caller pinned.
//
// The sha is a declared parameter of that route, so a conformant instance filters
// upstream and this comparison is the second check. It is the one that has to hold when
// the first does not: a proxy that drops the query, a version that changes the filter's
// meaning, or an instance that ignores it all put a foreign row on the wire, and a
// re-run can carry deployment side effects, so a row displaying one commit's red status
// must not act on another's.
//
// The instance serves the retry route the foreign row's own id addresses, so an
// implementation that dropped the comparison would SUCCEED here rather than fail on a
// missing route: the failure this case reports is the retry that happened.
func TestTheReRunRefusesAPipelineRowCarryingAnotherHead(t *testing.T) {
	h := newHarness(t, map[string]string{
		metadataRoute:   metadataBody,
		pipelinesRoute:  pipelinesMovedHeadBody,
		retryRoute:      `{}`,
		movedRetryRoute: `{}`,
	})
	h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
	err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA)
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("RerunFailedChecks against a row carrying another head = %v, want a *forgeapi.Error", err)
	}
	if fe.Code != forgeapi.CodeStaleHead {
		t.Errorf("RerunFailedChecks = code %q, want %q", fe.Code, forgeapi.CodeStaleHead)
	}
	if fe.Kind != forgeapi.KindConflict {
		t.Errorf("RerunFailedChecks = kind %v, want %v", fe.Kind, forgeapi.KindConflict)
	}
	for _, sent := range h.instance.arrived() {
		if sent == retryRoute || sent == movedRetryRoute {
			t.Errorf("RerunFailedChecks reached %s, so it retried a pipeline built for a commit the caller never saw", sent)
		}
	}
}

// TestTheStateFilterIsRefusedOnEveryListWhoseEndpointFixesIt holds the one list-option
// vocabulary against the lists: the filter applies to the lists that have a state and
// is refused with a local code on the others, because the endpoints behind them fix
// it.
func TestTheStateFilterIsRefusedOnEveryListWhoseEndpointFixesIt(t *testing.T) {
	h := newHarness(t, map[string]string{
		projectsListRoute: "[" + projectBody + "]",
		releasesRoute:     "[" + releaseBody + "]",
		labelsRoute:       `[]`,
		myMergeRoute:      "[]",
		myIssuesRoute:     "[]",
		pipelinesRoute:    "[]",
	})
	for name, call := range map[string]func() error{
		"ListRepos": func() error {
			_, err := h.client.ListRepos(t.Context(), forgeapi.WithState(forgeapi.ListStateClosed))
			return err
		},
		"ListMyPRs": func() error {
			_, err := h.client.ListMyPRs(t.Context(), forgeapi.WithState(forgeapi.ListStateClosed))
			return err
		},
		"ListMyIssues": func() error {
			_, err := h.client.ListMyIssues(t.Context(), forgeapi.WithState(forgeapi.ListStateOpen))
			return err
		},
		"ListRuns": func() error {
			_, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithState(forgeapi.ListStateOpen))
			return err
		},
		"ListReleases": func() error {
			_, err := h.client.ListReleases(t.Context(), testRef(), forgeapi.WithState(forgeapi.ListStateOpen))
			return err
		},
		"ListLabels": func() error {
			_, err := h.client.ListLabels(t.Context(), testRef(), forgeapi.WithState(forgeapi.ListStateOpen))
			return err
		},
	} {
		var fe *forgeapi.Error
		if err := call(); !asForgeError(err, &fe) || fe.Code != forgeapi.CodeListStateInvalid {
			t.Errorf("%s with a state filter = %v, want %q: the endpoint behind it fixes the state, so a filter there would be silently dropped",
				name, err, forgeapi.CodeListStateInvalid)
		}
	}
	if h.instance.count() != 0 {
		t.Errorf("the refusals sent %d request(s), want 0: each is reached before anything is sent", h.instance.count())
	}
	// The one filter that is not a state and IS refused on a list that has one: an
	// issue has no merged state, so the member is refused there rather than sent as
	// a word this product would not recognise.
	var fe *forgeapi.Error
	_, err := h.client.ListIssues(t.Context(), testRef(), forgeapi.WithState(forgeapi.ListStateMerged))
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeListStateInvalid {
		t.Errorf("ListIssues with the merged filter = %v, want %q", err, forgeapi.CodeListStateInvalid)
	}
}

// TestTheCrossRepositoryRowsCarryTheirOwnProject holds the addressing a consumer acts on
// from a list whose rows span projects: the row's own full reference is the witness, and
// the numeric project id beside it is not a canonical selector, so no identifier can be
// derived from it.
func TestTheCrossRepositoryRowsCarryTheirOwnProject(t *testing.T) {
	h := newHarness(t, map[string]string{myMergeRoute: "[" + restMergeRequestBody + "]"})
	page, err := h.client.ListMyPRs(t.Context())
	if err != nil {
		t.Fatalf("ListMyPRs = %v, want the page", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListMyPRs = %d row(s), want 1", len(page.Items))
	}
	row := page.Items[0]
	if row.Repo.Selector != testSelector {
		t.Errorf("ListMyPRs row 0 = project %q, want %q, recovered from the row's own full reference", row.Repo.Selector, testSelector)
	}
	if row.Repo.ID != testRef().ID {
		t.Errorf("ListMyPRs row 0 = id %q, want the derived %q: a consumer acts on the row by that identifier", row.Repo.ID, testRef().ID)
	}
	if row.Repo.Family != forgeapi.FamilyGitLab {
		t.Errorf("ListMyPRs row 0 = family %v, want %v", row.Repo.Family, forgeapi.FamilyGitLab)
	}
	if got := h.instance.query(myMergeRoute, "scope"); got != "created_by_me" {
		t.Errorf("ListMyPRs sent scope %q, want created_by_me: without it the answer is every merge request the credential can see, which is a different question", got)
	}
	if got := h.instance.query(myMergeRoute, "state"); got != stateOpened {
		t.Errorf("ListMyPRs sent state %q, want %q", got, stateOpened)
	}
	// The row carries the head pipeline's scalar status, which costs nothing, and no
	// per-state count, which one scalar cannot supply.
	if row.Action.Checks != forgeapi.CheckPassing {
		t.Errorf("ListMyPRs row 0 = checks %v, want %v from the head pipeline the row already carries", row.Action.Checks, forgeapi.CheckPassing)
	}
	if row.Action.ChecksTotal != 0 {
		t.Errorf("ListMyPRs row 0 = %d checks in total, want 0: one scalar status cannot supply a count", row.Action.ChecksTotal)
	}
}

// TestTheRunListingReadsEachPipelineRow holds the run listing to the pipeline rows it
// pages: the page bound is the page size, no head filter narrows the read to one
// commit, a name the pipeline's configuration sets is published and a null one is
// empty, the ref is the product's own spelling, and the next-page header is the
// continuation.
func TestTheRunListingReadsEachPipelineRow(t *testing.T) {
	h := newHarness(t, map[string]string{pipelinesRoute: runRows})
	h.instance.answerHeader(headerNextPage, "2")
	page, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithPageBound(2))
	if err != nil {
		t.Fatalf("ListRuns = %v, want the page", err)
	}
	if got := h.instance.arrived(); len(got) != 1 || got[0] != pipelinesRoute {
		t.Fatalf("ListRuns reached %v, want [%s] alone: the listing is one request per page", got, pipelinesRoute)
	}
	if got := h.instance.query(pipelinesRoute, keyPerPage); got != "2" {
		t.Errorf("ListRuns sent per_page %q, want the page bound 2", got)
	}
	if got := h.instance.query(pipelinesRoute, keyPage); got != "1" {
		t.Errorf("ListRuns sent page %q, want 1 on a first call", got)
	}
	if got := h.instance.query(pipelinesRoute, "sha"); got != "" {
		t.Errorf("ListRuns sent sha %q, want none: the head filter the re-run sends would narrow the listing to one commit's runs", got)
	}
	if len(page.Items) != 2 {
		t.Fatalf("ListRuns = %d run(s), want 2", len(page.Items))
	}
	want := []forgeapi.Run{
		{
			Repo:      testRef(),
			Name:      "Nightly",
			Branch:    "main",
			HeadSHA:   "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d",
			WebURL:    "https://forge.example/example/group/example/-/pipelines/77",
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			State:     forgeapi.CheckFailing,
		},
		{
			Repo:      testRef(),
			Branch:    "refs/merge-requests/1/head",
			HeadSHA:   "0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f60718293",
			WebURL:    "https://forge.example/example/group/example/-/pipelines/76",
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 1, 1, 0, 10, 0, 0, time.UTC),
			State:     forgeapi.CheckPending,
		},
	}
	for i := range want {
		if got := page.Items[i]; !reflect.DeepEqual(got, want[i]) {
			t.Errorf("ListRuns run %d = %+v, want %+v", i, got, want[i])
		}
	}
	if want := restCursor(t, h.client, "ListRuns", testRef(), 2, forgeapi.WithPageBound(2)); page.Next != want {
		t.Errorf("ListRuns = next %q, want %q from the next-page header", page.Next, want)
	}
}

// TestTheRunListingAnswersWhereTheReRunIsRefused holds the listing apart from the
// re-run verb's capability. An anonymous caller is answered 401 by the metadata route,
// which leaves the re-run's verdict short of yes and refuses the verb on that
// connection, while the pipelines route answers the same caller, so a listing that
// inherited the verb's gate would refuse a read the instance serves.
func TestTheRunListingAnswersWhereTheReRunIsRefused(t *testing.T) {
	h := newHarness(t, map[string]string{
		metadataRoute:  `{"message":"401 Unauthorized"}`,
		documentRoute:  docAcceptanceAnswer,
		pipelinesRoute: runRows,
	})
	h.instance.status(metadataRoute, http.StatusUnauthorized)
	h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
	caps, err := h.client.ConnectionCaps(t.Context())
	if err != nil {
		t.Fatalf("Setup: ConnectionCaps = %v", err)
	}
	if caps.Caps[forgeapi.CapRerunChecks] == forgeapi.SupportYes {
		t.Fatalf("Setup: the re-run capability = %v, want a verdict short of yes so the verb is refused here", caps.Caps[forgeapi.CapRerunChecks])
	}
	if err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA); err == nil {
		t.Fatal("Setup: RerunFailedChecks = nil on a connection whose re-run verdict is short of yes, want the refusal this case separates the listing from")
	}
	page, err := h.client.ListRuns(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListRuns = %v, want the page: the pipelines route answers this caller whatever the re-run verb's verdict", err)
	}
	if len(page.Items) != 2 {
		t.Errorf("ListRuns = %d run(s), want 2", len(page.Items))
	}
}

// TestAReferenceThatIsNotSafeInAPathIsRefusedBeforeAnyRequest holds the validation every
// repository-addressed call owes, because a derived identifier is consumer-controlled
// input on its way into a request path.
//
// The separator bound is this family's own: its selector is a nested namespace path, so
// more than one separator is valid here and a traversal segment is not.
func TestAReferenceThatIsNotSafeInAPathIsRefusedBeforeAnyRequest(t *testing.T) {
	for _, selector := range []string{"", "../../../etc", "example/../group", "/example/group", "example/group/"} {
		h := newHarness(t, nil)
		ref := forgeapi.RepoRef{Family: forgeapi.FamilyGitLab, Selector: selector}
		var fe *forgeapi.Error
		_, err := h.client.ListIssues(t.Context(), ref)
		if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefInvalid {
			t.Errorf("ListIssues(%q) = %v, want %q", selector, err, forgeapi.CodeRepoRefInvalid)
		}
		if h.instance.count() != 0 {
			t.Errorf("ListIssues(%q) sent %d request(s), want 0", selector, h.instance.count())
		}
	}
	// A nested path IS valid here, which is the half a separator ban would break.
	h := newHarness(t, map[string]string{
		"GET /api/v4/projects/a%2Fb%2Fc%2Fd/issues": "[" + issueBody + "]",
	})
	if _, err := h.client.ListIssues(t.Context(), forgeapi.RepoRef{Family: forgeapi.FamilyGitLab, Selector: "a/b/c/d"}); err != nil {
		t.Errorf("ListIssues over a four-segment namespace = %v, want the answer: this product's selector is a nested path", err)
	}
}

// TestTheProjectRecordAnswersTheThreeValuedAffordances holds the two affordances this
// product cannot answer as booleans, and the evidence map the expectation table says is
// empty here.
func TestTheProjectRecordAnswersTheThreeValuedAffordances(t *testing.T) {
	h := newHarness(t, map[string]string{projectRouteKey: projectBody})
	got, err := h.client.RepoAffordances(t.Context(), testRef())
	if err != nil {
		t.Fatalf("RepoAffordances = %v, want the record", err)
	}
	if got.HasIssues != forgeapi.SupportYes {
		t.Errorf("RepoAffordances = issues %v, want %v", got.HasIssues, forgeapi.SupportYes)
	}
	if got.CanPush != forgeapi.SupportYes {
		t.Errorf("RepoAffordances = push %v, want %v from the role integer", got.CanPush, forgeapi.SupportYes)
	}
	if got.MergeTrain != forgeapi.SupportYes {
		t.Errorf("RepoAffordances = train %v, want %v from the project's own setting rather than from a refusal", got.MergeTrain, forgeapi.SupportYes)
	}
	if got.DefaultBranch != "main" {
		t.Errorf("RepoAffordances = default branch %q, want main", got.DefaultBranch)
	}
	if len(got.Ev) != 0 {
		t.Errorf("RepoAffordances = %d evidence entr(ies), want none: measured anonymously, this product's project record carries none of the keys the three capabilities would name a source for", len(got.Ev))
	}
	// An anonymous read of the same record answers none of the four keys, which is
	// what makes the two fields three-valued rather than boolean.
	anon := newHarness(t, map[string]string{
		projectRouteKey: `{"path_with_namespace": "example/group/example", "visibility": "public", "default_branch": "main"}`,
	})
	bare, err := anon.client.RepoAffordances(t.Context(), testRef())
	if err != nil {
		t.Fatalf("RepoAffordances over an anonymous record = %v, want the record", err)
	}
	if bare.HasIssues != forgeapi.SupportUnknown || bare.CanPush != forgeapi.SupportUnknown || bare.MergeTrain != forgeapi.SupportUnknown {
		t.Errorf("RepoAffordances over an anonymous record = issues %v, push %v, train %v, want all three unknown: an unexplained false hides a working feature",
			bare.HasIssues, bare.CanPush, bare.MergeTrain)
	}
}

// TestTheVisibilityStringDecidesThePrivateFlag holds the one repository field this
// product spells as a word where the contract states a boolean: there is no private key,
// and an internal project is not public.
func TestTheVisibilityStringDecidesThePrivateFlag(t *testing.T) {
	for visibility, want := range map[string]bool{"public": false, "internal": true, "private": true} {
		h := newHarness(t, map[string]string{
			projectsListRoute: `[{"path_with_namespace": "example/group/example", "visibility": "` + visibility + `"}]`,
		})
		page, err := h.client.ListRepos(t.Context())
		if err != nil {
			t.Fatalf("ListRepos = %v", err)
		}
		if got := page.Items[0].Private; got != want {
			t.Errorf("ListRepos over visibility %q = private %v, want %v", visibility, got, want)
		}
	}
}

// TestAReleaseReportsNoDraftAndNoPrerelease holds the two fields this product has no
// concept for. The upcoming-release key beside them says the release DATE is in the
// future, which is a schedule rather than a prerelease, so reading it as one would
// publish a released version as unreleased.
func TestAReleaseReportsNoDraftAndNoPrerelease(t *testing.T) {
	h := newHarness(t, map[string]string{
		releasesRoute: `[` + strings.Replace(releaseBody, `"upcoming_release": false`, `"upcoming_release": true`, 1) + `]`,
	})
	page, err := h.client.ListReleases(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListReleases = %v", err)
	}
	row := page.Items[0]
	if row.Draft || row.Prerelease {
		t.Errorf("ListReleases = draft %v, prerelease %v, want both false: this product has neither concept, and the upcoming key is a schedule",
			row.Draft, row.Prerelease)
	}
	if row.WebURL == "" || row.PublishedAt.IsZero() || row.Body != "Example release notes." {
		t.Errorf("ListReleases = %+v, want the web URL from the links object, the publication time from released_at and the notes from description", row)
	}
}

// TestANonPositiveNumberIsRefusedBeforeAnyRequest holds the reference checks the
// numbered operations owe, because a number interpolated into a path is input too.
func TestANonPositiveNumberIsRefusedBeforeAnyRequest(t *testing.T) {
	h := newHarness(t, nil)
	repo := testRef()
	for name, call := range map[string]func() error{
		"ReadPR":      func() error { _, err := h.client.ReadPR(t.Context(), repo, forgeapi.PRRef{}); return err },
		"ClosePR":     func() error { _, err := h.client.ClosePR(t.Context(), repo, forgeapi.PRRef{}); return err },
		"ReopenPR":    func() error { _, err := h.client.ReopenPR(t.Context(), repo, forgeapi.PRRef{}); return err },
		"MergeStatus": func() error { _, err := h.client.MergeStatus(t.Context(), repo, forgeapi.PRRef{}); return err },
		"CloseIssue": func() error {
			_, err := h.client.CloseIssue(t.Context(), repo, forgeapi.IssueRef{Number: -1})
			return err
		},
		"MergePR": func() error {
			_, err := h.client.MergePR(t.Context(), repo, forgeapi.PRRef{}, forgeapi.MergeRequest{HeadSHA: testHeadSHA})
			return err
		},
	} {
		var fe *forgeapi.Error
		if err := call(); !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefInvalid {
			t.Errorf("%s with a non-positive number = %v, want %q", name, err, forgeapi.CodeRepoRefInvalid)
		}
	}
	if h.instance.count() != 0 {
		t.Errorf("the refusals sent %d request(s), want 0", h.instance.count())
	}
}

// TestAThrottleIsReportedWithItsOwnKindRatherThanRetried holds the one upstream refusal a
// consumer waits on rather than retrying, and holds it end to end rather than at the
// mapper: the counter is what separates it from a partial the governor chose.
func TestAThrottleIsReportedWithItsOwnKindRatherThanRetried(t *testing.T) {
	h := newRefusingHarness(t, http.StatusTooManyRequests)
	_, err := h.client.ListIssues(t.Context(), testRef())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("ListIssues against a throttle = %v, want a *forgeapi.Error", err)
	}
	if fe.Kind != forgeapi.KindRateLimited {
		t.Errorf("ListIssues = kind %v, want %v", fe.Kind, forgeapi.KindRateLimited)
	}
	if fe.Status != http.StatusTooManyRequests {
		t.Errorf("ListIssues = status %d, want %d", fe.Status, http.StatusTooManyRequests)
	}
}
