package gitlab

import (
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

func statusRow(id int, name, status string) string {
	return `{"id": ` + strconv.Itoa(id) + `, "name": "` + name + `", "status": "` + status + `", "sha": "` + testHeadSHA + `"}`
}

func TestTheFoldIsTheWorstStatePresent(t *testing.T) {
	type tally struct {
		state                                            forgeapi.CheckState
		failing, unknown, pending, passing, neutral, all int
	}
	failed, unread := statusRow(1, "build", "failed"), statusRow(2, "lint", "frobnicated")
	running, success, skipped := statusRow(3, "test", "running"), statusRow(4, "deploy", "success"), statusRow(5, "docs", "skipped")
	for _, test := range []struct {
		name string
		rows []string
		want tally
	}{
		{
			name: "a_failure_outranks_everything",
			rows: []string{skipped, success, running, unread, failed},
			want: tally{state: forgeapi.CheckFailing, failing: 1, unknown: 1, pending: 1, passing: 1, neutral: 1, all: 5},
		},
		{
			name: "an_unread_state_outranks_a_pending_one",
			rows: []string{skipped, success, running, unread},
			want: tally{state: forgeapi.CheckUnknown, unknown: 1, pending: 1, passing: 1, neutral: 1, all: 4},
		},
		{
			name: "a_pending_check_outranks_a_pass",
			rows: []string{skipped, success, running},
			want: tally{state: forgeapi.CheckPending, pending: 1, passing: 1, neutral: 1, all: 3},
		},
		{
			name: "a_pass_outranks_a_skip",
			rows: []string{skipped, success},
			want: tally{state: forgeapi.CheckPassing, passing: 1, neutral: 1, all: 2},
		},
		{
			name: "only_skips_are_neutral",
			rows: []string{skipped},
			want: tally{state: forgeapi.CheckNeutral, neutral: 1, all: 1},
		},
		{name: "no_context_is_none", want: tally{state: forgeapi.CheckNone}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{statusesRoute: "[" + strings.Join(test.rows, ",") + "]"})
			checks, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA)
			if err != nil {
				t.Fatalf("CommitStatus = %v, want the fold", err)
			}
			got := tally{
				state: checks.State, failing: checks.Failing, unknown: checks.Unknown, pending: checks.Pending,
				passing: checks.Passing, neutral: checks.Neutral, all: checks.Total,
			}
			if got != test.want {
				t.Errorf("CommitStatus over %v = %+v, want %+v", test.rows, got, test.want)
			}
		})
	}
}

// A walk reads one status record twice when the history shifts between pages.
func TestAStatusServedTwiceUnderOneIdentifierCountsOnceAsTheLaterRead(t *testing.T) {
	h := newHarness(t, map[string]string{statusesRoute: "[" + statusRow(7, "build", "running") + "," + statusRow(7, "build", "success") + "]"})
	checks, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA)
	if err != nil {
		t.Fatalf("CommitStatus = %v, want the fold", err)
	}
	if checks.Total != 1 || checks.Passing != 1 || checks.State != forgeapi.CheckPassing {
		t.Errorf("CommitStatus over id 7 read running then success = %v with %d passing of %d, want %v with 1 passing of 1",
			checks.State, checks.Passing, checks.Total, forgeapi.CheckPassing)
	}
}

// The statuses route matches pipelines on their sha column, so a ref that is not a
// full commit id answers [] there whatever ran on it; the read resolves it first.
func TestACommitStatusAtAnythingButAFullCommitIDFoldsTheCommitItResolvesTo(t *testing.T) {
	const commitsRoute = "GET /api/v4/projects/" + testEncoded + "/repository/commits/"
	for name, ref := range map[string]string{
		"a_branch":        "main",
		"a_nested_branch": "feature/x",
		"a_short_sha":     testHeadSHA[:8],
		"an_upper_sha":    strings.ToUpper(testHeadSHA),
	} {
		t.Run(name, func(t *testing.T) {
			escaped := strings.ReplaceAll(ref, "/", "%2F")
			h := newHarness(t, map[string]string{
				commitsRoute + escaped:               `{"id": "` + testHeadSHA + `", "short_id": "` + testHeadSHA[:8] + `"}`,
				commitsRoute + escaped + "/statuses": `[]`,
				statusesRoute:                        "[" + statusRow(1, "build", "failed") + "]",
			})
			checks, err := h.client.CommitStatus(t.Context(), testRef(), ref)
			if err != nil {
				t.Fatalf("CommitStatus(%s) = %v, want the fold", ref, err)
			}
			if checks.State != forgeapi.CheckFailing || checks.Failing != 1 || checks.Total != 1 {
				t.Errorf("CommitStatus(%s) = %v with %d failing of %d, want %v with 1 failing of 1, the head's own row",
					ref, checks.State, checks.Failing, checks.Total, forgeapi.CheckFailing)
			}
			if checks.Ref != testHeadSHA {
				t.Errorf("CommitStatus(%s) = ref %q, want %q, the commit the ref resolved to", ref, checks.Ref, testHeadSHA)
			}
			want := []string{commitsRoute + escaped, statusesRoute}
			if got := h.instance.arrived(); !slices.Equal(got, want) {
				t.Errorf("CommitStatus(%s) sent %v, want %v", ref, got, want)
			}
		})
	}
}

func TestACommitStatusAtAFullCommitIDSendsNoCommitRead(t *testing.T) {
	h := newHarness(t, map[string]string{statusesRoute: "[" + statusRow(1, "build", "success") + "]"})
	checks, err := h.client.CommitStatus(t.Context(), testRef(), testHeadSHA)
	if err != nil {
		t.Fatalf("CommitStatus = %v, want the fold", err)
	}
	if checks.Ref != testHeadSHA || checks.State != forgeapi.CheckPassing {
		t.Errorf("CommitStatus = ref %q state %v, want %q %v", checks.Ref, checks.State, testHeadSHA, forgeapi.CheckPassing)
	}
	if got, want := h.instance.arrived(), []string{statusesRoute}; !slices.Equal(got, want) {
		t.Errorf("CommitStatus sent %v, want %v", got, want)
	}
}

func TestACommitStatusWhoseCommitReadNamesNoFullIDFoldsNothing(t *testing.T) {
	const commitRoute = "GET /api/v4/projects/" + testEncoded + "/repository/commits/main"
	for name, body := range map[string]string{
		"no_id":    `{"short_id": "1f0e7b3c"}`,
		"short_id": `{"id": "1f0e7b3c"}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{commitRoute: body})
			_, err := h.client.CommitStatus(t.Context(), testRef(), "main")
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindUpstream {
				t.Errorf("CommitStatus(main) over %s = %v, want a %v error", body, err, forgeapi.KindUpstream)
			}
			if got, want := h.instance.arrived(), []string{commitRoute}; !slices.Equal(got, want) {
				t.Errorf("CommitStatus(main) over %s sent %v, want %v", body, got, want)
			}
		})
	}
}

func TestACommitStatusAtAMissingBranchFailsAsAMissingCommitDoes(t *testing.T) {
	const missing = `{"message": "404 Commit Not Found"}`
	failure := func(route, ref string) *forgeapi.Error {
		t.Helper()
		h := newHarness(t, map[string]string{route: missing})
		h.instance.status(route, http.StatusNotFound)
		_, err := h.client.CommitStatus(t.Context(), testRef(), ref)
		var fe *forgeapi.Error
		if !asForgeError(err, &fe) {
			t.Fatalf("CommitStatus(%s) = %v, want a forge error", ref, err)
		}
		return fe
	}
	bySHA := failure(statusesRoute, testHeadSHA)
	byBranch := failure("GET /api/v4/projects/"+testEncoded+"/repository/commits/gone", "gone")
	if byBranch.Kind != bySHA.Kind || byBranch.Code != bySHA.Code || byBranch.Status != bySHA.Status {
		t.Errorf("CommitStatus(gone) = kind %v code %q status %d, want the missing commit's kind %v code %q status %d",
			byBranch.Kind, byBranch.Code, byBranch.Status, bySHA.Kind, bySHA.Code, bySHA.Status)
	}
}

func TestListLabelsAnswersTheRepositorysLabels(t *testing.T) {
	h := newHarness(t, map[string]string{labelsRoute: labelRows})
	page, err := h.client.ListLabels(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListLabels = %v, want the page", err)
	}
	want := []forgeapi.Label{{Name: "example-label", Color: "#ededed", Description: "Example label."}}
	if !reflect.DeepEqual(page.Items, want) {
		t.Errorf("ListLabels = %+v, want %+v", page.Items, want)
	}
}

func TestWhoamiAnswersTheCredentialsAccount(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v4/user": `{"username": "example-user", "name": "Example User", "email": "user@example.com",
  "web_url": "https://forge.example/example-user"}`,
	})
	got, err := h.client.Whoami(t.Context())
	if err != nil {
		t.Fatalf("Whoami = %v, want the account", err)
	}
	want := forgeapi.Account{Login: "example-user", Name: "Example User", Email: "user@example.com", WebURL: "https://forge.example/example-user"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Whoami = %+v, want %+v", got, want)
	}
}

func TestARepositoryIsAForkExactlyWhereItsRecordNamesAParent(t *testing.T) {
	for name, test := range map[string]struct {
		parent string
		fork   bool
	}{
		"a_fork":     {parent: `, "forked_from_project": {"path_with_namespace": "upstream/example"}`, fork: true},
		"not_a_fork": {},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				projectsListRoute: `[{"path_with_namespace": "example/group/example"` + test.parent + `}]`,
			})
			page, err := h.client.ListRepos(t.Context())
			if err != nil {
				t.Fatalf("ListRepos = %v, want the page", err)
			}
			if len(page.Items) != 1 {
				t.Fatalf("ListRepos = %d row(s), want 1", len(page.Items))
			}
			if got := page.Items[0].Fork; got != test.fork {
				t.Errorf("ListRepos %s = fork %v, want %v", name, got, test.fork)
			}
		})
	}
}

func TestTheMergeStrategiesAreTheProjectsMethodAndSquashWhereItsOptionAdmitsIt(t *testing.T) {
	for _, test := range []struct {
		name, method, squash string
		want                 []string
	}{
		{name: "a_method_and_an_optional_squash", method: "merge", squash: "default_off", want: []string{"merge", "squash"}},
		{name: "a_method_whose_squash_is_never", method: "ff", squash: "Never", want: []string{"ff"}},
		{name: "a_squash_and_no_method", squash: "always", want: []string{"squash"}},
		{name: "neither"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				projectRouteKey: `{"path_with_namespace": "example/group/example", "merge_method": "` + test.method +
					`", "squash_option": "` + test.squash + `"}`,
			})
			got, err := h.client.RepoAffordances(t.Context(), testRef())
			if err != nil {
				t.Fatalf("RepoAffordances = %v, want the record", err)
			}
			if !reflect.DeepEqual(got.MergeStrategies, test.want) {
				t.Errorf("RepoAffordances(merge_method %q, squash_option %q) = strategies %q, want %q",
					test.method, test.squash, got.MergeStrategies, test.want)
			}
		})
	}
}

func TestAnIssueRowCarriesItsAuthorAndTheRepositoryItBelongsTo(t *testing.T) {
	anonymous := strings.Replace(crossIssueBody, `"author": {"username": "example-user"}`, `"author": null`, 1)
	for _, test := range []struct {
		list   func(h *harness) (forgeapi.Page[forgeapi.Issue], error)
		routes map[string]string
		name   string
		author string
	}{
		{
			name:   "an_addressed_repository",
			routes: map[string]string{issuesRoute: "[" + issueBody + "]"},
			list: func(h *harness) (forgeapi.Page[forgeapi.Issue], error) {
				return h.client.ListIssues(t.Context(), testRef())
			},
			author: "example-user",
		},
		{
			name:   "a_cross_repository_row",
			routes: map[string]string{myIssuesRoute: "[" + crossIssueBody + "]"},
			list: func(h *harness) (forgeapi.Page[forgeapi.Issue], error) {
				return h.client.ListMyIssues(t.Context())
			},
			author: "example-user",
		},
		{
			name:   "a_row_with_no_author",
			routes: map[string]string{myIssuesRoute: "[" + anonymous + "]"},
			list: func(h *harness) (forgeapi.Page[forgeapi.Issue], error) {
				return h.client.ListMyIssues(t.Context())
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, test.routes)
			page, err := test.list(h)
			if err != nil {
				t.Fatalf("%s = %v, want the page", test.name, err)
			}
			if len(page.Items) != 1 {
				t.Fatalf("%s = %d row(s), want 1", test.name, len(page.Items))
			}
			row := page.Items[0]
			if row.Repo != testRef() {
				t.Errorf("%s row 0 = repository %+v, want %+v", test.name, row.Repo, testRef())
			}
			if row.Author != test.author {
				t.Errorf("%s row 0 = author %q, want %q", test.name, row.Author, test.author)
			}
		})
	}
}

func TestARowWithAnEmptyLabelArrayCarriesNoLabels(t *testing.T) {
	unlabelled := strings.Replace(issueBody,
		`"labels": [{"name": "example-label", "color": "#ededed", "description": "Example label."}]`, `"labels": []`, 1)
	h := newHarness(t, map[string]string{issuesRoute: "[" + unlabelled + "]"})
	page, err := h.client.ListIssues(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListIssues = %v, want the page", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListIssues = %d row(s), want 1", len(page.Items))
	}
	if got := page.Items[0].Labels; got != nil {
		t.Errorf("ListIssues row 0 = labels %+v, want none", got)
	}
}

func TestADocumentRowCarriesItsLabelsAndMarksALabelSetItDoesNotHold(t *testing.T) {
	served := []forgeapi.Label{{Name: "example-label", Color: "#ededed", Description: "Example label."}}
	for _, test := range []struct {
		want    *forgeapi.Partial
		name    string
		labels  string
		counted int
	}{
		{name: "the_whole_set", labels: `"labels": {"pageInfo": {"hasNextPage": false},`},
		{
			name:    "part_of_a_larger_set",
			labels:  `"labels": {"count": 4, "pageInfo": {"hasNextPage": true},`,
			want:    &forgeapi.Partial{Reason: forgeapi.PartialPaginationCap, Fetched: 1, OmittedAtLeast: 3},
			counted: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := strings.Replace(docList, `"labels": {"pageInfo": {"hasNextPage": false},`, test.labels, 1)
			h := newHarness(t, map[string]string{documentRoute: body})
			page, err := h.client.ListPRs(t.Context(), testRef())
			if err != nil {
				t.Fatalf("ListPRs = %v, want the page", err)
			}
			if len(page.Items) != 1 {
				t.Fatalf("ListPRs = %d row(s), want 1", len(page.Items))
			}
			row := page.Items[0]
			if !reflect.DeepEqual(row.Labels, served) {
				t.Errorf("ListPRs row 0 = labels %+v, want %+v", row.Labels, served)
			}
			if !reflect.DeepEqual(row.Partial, test.want) {
				t.Errorf("ListPRs row 0 = partial %+v, want %+v", row.Partial, test.want)
			}
			if got := h.spy.times("PartialResult:" + forgeapi.PartialPaginationCap.String()); got != test.counted {
				t.Errorf("ListPRs counted the label cut %d time(s), want %d", got, test.counted)
			}
		})
	}
}

func TestADocumentRowBelongsToTheProjectTheDocumentResolved(t *testing.T) {
	for _, test := range []struct {
		name, path string
		want       forgeapi.RepoRef
	}{
		{name: "a_renamed_project", path: "example/group/renamed", want: repoRef("example/group/renamed")},
		{name: "no_path_selected", path: "", want: testRef()},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := strings.Replace(docRead, `"fullPath": "example/group/example"`, `"fullPath": "`+test.path+`"`, 1)
			h := newHarness(t, map[string]string{documentRoute: body})
			pr, err := h.client.ReadPR(t.Context(), testRef(), testPR())
			if err != nil {
				t.Fatalf("ReadPR = %v, want the merge request", err)
			}
			if pr.Repo != test.want {
				t.Errorf("ReadPR over a document resolving %q = repository %+v, want %+v", test.path, pr.Repo, test.want)
			}
		})
	}
}
