package gitlab

import (
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// sourceRow is one REST merge request whose head lives in the project the source id
// names, null for a deleted fork: this product's REST answer names both projects by
// numeric id alone (the merge requests API, source_project_id and target_project_id).
func sourceRow(iid int, source string) string {
	return `{"iid": ` + itoa(iid) + `, "project_id": 7, "source_project_id": ` + source + `,
	  "target_project_id": 7, "state": "opened", "title": "t", "source_branch": "feature",
	  "target_branch": "main", "sha": "1f0e7b3c9a5d84620ef1c3b7a6d5948e2f0b1c3d",
	  "references": {"full": "` + testSelector + `!` + itoa(iid) + `"}}`
}

func itoa(n int) string { return string(rune('0' + n)) }

const forkLookup = "GET /api/v4/projects/9"

// TestListMyPRsResolvesEachDistinctForkOnce holds ADR-0104's price on this product's
// cross-repository list: a row whose source id equals its target id names the row's
// own project and buys nothing, every row from one fork shares one GET
// /api/v4/projects/:id, and a deleted fork's null id buys nothing and answers the zero
// reference.
func TestListMyPRsResolvesEachDistinctForkOnce(t *testing.T) {
	rows := "[" + strings.Join([]string{sourceRow(1, "7"), sourceRow(2, "9"), sourceRow(3, "9"), sourceRow(4, "null")}, ",") + "]"
	h := newHarness(t, map[string]string{
		"GET /api/v4/merge_requests": rows,
		forkLookup:                   `{"id": 9, "path_with_namespace": "contributor/fork"}`,
	})
	page, err := h.client.ListMyPRs(t.Context())
	if err != nil {
		t.Fatalf("ListMyPRs = %v, want the page", err)
	}
	if len(page.Items) != 4 {
		t.Fatalf("ListMyPRs answered %d rows, want 4", len(page.Items))
	}
	fork := repoRef("contributor/fork")
	for i, want := range []forgeapi.RepoRef{page.Items[0].Repo, fork, fork, {}} {
		if got := page.Items[i].SourceRepo; got != want {
			t.Errorf("row %d SourceRepo = %+v, want %+v", i+1, got, want)
		}
	}
	lookups := 0
	for _, r := range h.instance.requests {
		if strings.HasPrefix(r, "GET /api/v4/projects/") {
			lookups++
			if r != forkLookup {
				t.Errorf("ListMyPRs sent %s, want no lookup but the fork's", r)
			}
		}
	}
	if lookups != 1 {
		t.Errorf("ListMyPRs sent %d project lookups, want 1 for two rows from one fork", lookups)
	}
}

// TestAForkTheLookupCannotReadAnswersTheZeroReference holds that a fork the credential
// cannot see, or one deleted between the list and the lookup, leaves the row's source
// unnamed rather than failing the page.
func TestAForkTheLookupCannotReadAnswersTheZeroReference(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusForbidden} {
		h := newHarness(t, map[string]string{
			"GET /api/v4/merge_requests": "[" + sourceRow(2, "9") + "]",
			forkLookup:                   `{"message": "404 Project Not Found"}`,
		})
		h.instance.status(forkLookup, code)
		page, err := h.client.ListMyPRs(t.Context())
		if err != nil {
			t.Fatalf("ListMyPRs with a %d lookup = %v, want the page", code, err)
		}
		if got := page.Items[0].SourceRepo; got != (forgeapi.RepoRef{}) {
			t.Errorf("SourceRepo after a %d lookup = %+v, want the zero reference", code, got)
		}
	}
}

// TestAForkLookupTheBudgetStopsMarksItsRowRateLimited holds that a lookup the
// instance throttles, or the governor defers to hold the mutation reserve, keeps the
// page already read: every row answers, the same-project row names its project, and
// the fork row keeps the zero reference and says why it stopped short, as the page
// read's own deferral answers a marker rather than an error.
func TestAForkLookupTheBudgetStopsMarksItsRowRateLimited(t *testing.T) {
	for _, test := range []struct {
		stop func(*instance)
		name string
	}{
		{name: "throttled", stop: func(in *instance) { in.status(forkLookup, http.StatusTooManyRequests) }},
		{name: "deferred", stop: func(in *instance) { in.answerHeader("RateLimit-Remaining", "10") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				"GET /api/v4/merge_requests": "[" + sourceRow(1, "7") + "," + sourceRow(2, "9") + "]",
				forkLookup:                   `{"id": 9, "path_with_namespace": "contributor/fork"}`,
			})
			test.stop(h.instance)
			page, err := h.client.ListMyPRs(t.Context())
			if err != nil {
				t.Fatalf("ListMyPRs with its fork lookup %s = %v, want the page", test.name, err)
			}
			if len(page.Items) != 2 {
				t.Fatalf("ListMyPRs with its fork lookup %s answered %d rows, want 2", test.name, len(page.Items))
			}
			if got, want := page.Items[0].SourceRepo, page.Items[0].Repo; got != want {
				t.Errorf("same-project row SourceRepo = %+v, want %+v", got, want)
			}
			fork := page.Items[1]
			if fork.SourceRepo != (forgeapi.RepoRef{}) {
				t.Errorf("fork row SourceRepo with its lookup %s = %+v, want the zero reference", test.name, fork.SourceRepo)
			}
			if fork.Partial == nil || fork.Partial.Reason != forgeapi.PartialRateLimited {
				t.Errorf("fork row Partial with its lookup %s = %+v, want the rate-limited reason", test.name, fork.Partial)
			}
		})
	}
}

// TestEveryOtherAnswerNamesTheSourceFromItsOwnFields holds the rest of ADR-0104 on
// this product: the documents name the head's project by path, null on a deleted
// fork, and a REST answer other than the cross-repository list names the addressed
// project where the two ids are equal and the zero reference for a fork.
func TestEveryOtherAnswerNamesTheSourceFromItsOwnFields(t *testing.T) {
	c := newHarness(t, nil).client
	repo := testRef()
	for _, test := range []struct {
		name string
		head *docProject
		want forgeapi.RepoRef
	}{
		{"same project", &docProject{FullPath: testSelector}, repo},
		{"same project, other case", &docProject{FullPath: strings.ToUpper(testSelector)}, repo},
		{"fork", &docProject{FullPath: "contributor/fork"}, repoRef("contributor/fork")},
		{"deleted fork", nil, forgeapi.RepoRef{}},
	} {
		if got := c.normalizeDocPull(&docMergeRequest{SourceProject: test.head}, repo).SourceRepo; got != test.want {
			t.Errorf("document, %s: SourceRepo = %+v, want %+v", test.name, got, test.want)
		}
	}
	seven, nine := int64(7), int64(9)
	for _, test := range []struct {
		name           string
		source, target *int64
		want           forgeapi.RepoRef
	}{
		{"same project", &seven, &seven, repo},
		{"fork", &nine, &seven, forgeapi.RepoRef{}},
		{"deleted fork", nil, &seven, forgeapi.RepoRef{}},
	} {
		for _, path := range []restPath{restMutated, restDegraded} {
			r := &restMergeRequest{SourceProjectID: test.source, TargetProjectID: test.target}
			if got := c.normalizeRESTPull(r, repo, path).SourceRepo; got != test.want {
				t.Errorf("REST path %d, %s: SourceRepo = %+v, want %+v", path, test.name, got, test.want)
			}
		}
	}
}
