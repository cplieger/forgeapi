package github

import (
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

func TestTheRESTListsAnswerTheRowsTheInstanceListed(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		reposRoute:    `[` + recorded.raw(t, "rest_repo_listing_row") + `]`,
		releasesRoute: `[` + recorded.raw(t, "rest_release") + `]`,
	})
	repos, err := h.client.ListRepos(t.Context())
	if err != nil {
		t.Fatalf("ListRepos = %v, want the page", err)
	}
	if len(repos.Items) != 1 || repos.Items[0].Ref != testRef() {
		t.Errorf("ListRepos = %+v, want the recorded row addressed as %s", repos.Items, testSelector)
	}
	releases, err := h.client.ListReleases(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListReleases = %v, want the page", err)
	}
	if len(releases.Items) != 1 || releases.Items[0].TagName != "v1.0.0" {
		t.Errorf("ListReleases = %+v, want the recorded v1.0.0", releases.Items)
	}
}

func TestARepositorysHasIssuesEvidenceNamesWhereItWasRead(t *testing.T) {
	for _, test := range []struct {
		name   string
		record string
		source forgeapi.EvidenceSource
		want   forgeapi.Support
	}{
		{name: "carried", record: `{"full_name":"example/example","has_issues":true}`, source: forgeapi.EvidenceResponseBody, want: forgeapi.SupportYes},
		{name: "omitted", record: `{"full_name":"example/example"}`, source: forgeapi.EvidenceDefault, want: forgeapi.SupportUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{repoRouteBase: test.record})
			got, err := h.client.RepoAffordances(t.Context(), testRef())
			if err != nil {
				t.Fatalf("RepoAffordances(%s) = %v, want the affordances", test.record, err)
			}
			ev := got.Ev[forgeapi.CapHasIssues]
			if got.HasIssues != test.want || ev.Source != test.source || !strings.Contains(ev.Detail, "has_issues") {
				t.Errorf("RepoAffordances(%s) = has issues %v from %v %q, want %v from %v naming has_issues", test.record, got.HasIssues, ev.Source, ev.Detail, test.want, test.source)
			}
		})
	}
}

func TestACommitStatusOnABranchNamesTheCommitItFolded(t *testing.T) {
	h := newHarness(t, map[string]string{
		documentRoute: documentEnvelope(`"repository":{"nameWithOwner":"example/example","object":{"oid":"` + testHeadSHA +
			`","statusCheckRollup":{"state":"SUCCESS","contexts":{"totalCount":1,"pageInfo":{"hasNextPage":false,"endCursor":null},` +
			`"nodes":[{"__typename":"StatusContext","context":"example/build","state":"SUCCESS"}]}}}}`),
	})
	checks, err := h.client.CommitStatus(t.Context(), testRef(), "main")
	if err != nil {
		t.Fatalf("CommitStatus(main) = %v, want the checks", err)
	}
	if checks.Ref != testHeadSHA {
		t.Errorf("CommitStatus(main) = ref %q, want the commit the rollup resolved, %q", checks.Ref, testHeadSHA)
	}
}

func TestASearchPageEndingShortOfItsTotalReportsTheResultWindow(t *testing.T) {
	for _, test := range searchRowsCases() {
		row := readCaptures(t).raw(t, test.row)
		for _, answer := range []struct {
			want  *forgeapi.Partial
			name  string
			nodes []string
			total int
			rows  int
		}{
			{
				name: "short_of_its_total", total: 3, nodes: []string{row}, rows: 1,
				want: &forgeapi.Partial{Reason: forgeapi.PartialResultWindow, Fetched: 1, OmittedAtLeast: 2},
			},
			{name: "at_its_total", total: 1, nodes: []string{row}, rows: 1},
			{name: "nothing_stated", total: 0, rows: 0},
		} {
			t.Run(test.name+"_"+answer.name, func(t *testing.T) {
				h := newHarness(t, map[string]string{documentRoute: searchAnswer(answer.total, answer.nodes, false)})
				rows, partial, err := test.list(t.Context(), h)
				if err != nil {
					t.Fatalf("%s over a total of %d = %v, want the page", test.name, answer.total, err)
				}
				if rows != answer.rows {
					t.Errorf("%s over a total of %d = %d row(s), want %d", test.name, answer.total, rows, answer.rows)
				}
				if (partial == nil) != (answer.want == nil) || (partial != nil && *partial != *answer.want) {
					t.Errorf("%s over a total of %d = partial %+v, want %+v", test.name, answer.total, partial, answer.want)
				}
			})
		}
	}
}
