package github

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cplieger/forgeapi"
)

func TestAListRefusesAnOptionItsRouteCannotApplyBeforeAnyRequest(t *testing.T) {
	for _, test := range []struct {
		call func(h *harness) error
		name string
		code string
	}{
		{name: "a_page_bound_of_zero", code: forgeapi.CodePageBoundInvalid, call: func(h *harness) error {
			_, err := h.client.ListRepos(t.Context(), forgeapi.WithPageBound(0))
			return err
		}},
		{name: "a_state_on_a_list_whose_route_fixes_it", code: forgeapi.CodeListStateInvalid, call: func(h *harness) error {
			_, err := h.client.ListRepos(t.Context(), forgeapi.WithState(forgeapi.ListStateClosed))
			return err
		}},
		{name: "a_state_on_a_cross_repository_search", code: forgeapi.CodeListStateInvalid, call: func(h *harness) error {
			_, err := h.client.ListMyPRs(t.Context(), forgeapi.WithState(forgeapi.ListStateClosed))
			return err
		}},
		{name: "an_owner_on_a_list_no_owner_scopes", code: forgeapi.CodeListOwnerInvalid, call: func(h *harness) error {
			_, err := h.client.ListLabels(t.Context(), testRef(), forgeapi.WithOwner(testOwner))
			return err
		}},
		{name: "an_owner_path_the_search_qualifier_cannot_take", code: forgeapi.CodeListOwnerInvalid, call: func(h *harness) error {
			_, err := h.client.ListMyPRs(t.Context(), forgeapi.WithOwner(testOwner+"/nested"))
			return err
		}},
		{name: "the_merged_state_on_issues", code: forgeapi.CodeListStateInvalid, call: func(h *harness) error {
			_, err := h.client.ListIssues(t.Context(), testRef(), forgeapi.WithState(forgeapi.ListStateMerged))
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, nil)
			err := test.call(h)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != test.code {
				t.Errorf("%s = %v, want code %q", test.name, err, test.code)
			}
			if sent := h.instance.count(); sent != 0 {
				t.Errorf("%s sent %d request(s), want none: an option the route cannot apply is refused before the wire: %v", test.name, sent, h.instance.arrived())
			}
		})
	}
}

func TestAListSendsTheStateFilterOnlyWhereItsRouteReadsIt(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		issuesRoute: `[` + recorded.raw(t, "rest_issue") + `]`,
		reposRoute:  `[` + recorded.raw(t, "rest_repo_listing_row") + `]`,
	})
	if _, err := h.client.ListIssues(t.Context(), testRef(), forgeapi.WithState(forgeapi.ListStateClosed)); err != nil {
		t.Fatalf("ListIssues(WithState(closed)) = %v, want the page", err)
	}
	if got := h.instance.query(issuesRoute, "state"); got != stateClosed {
		t.Errorf("ListIssues(WithState(closed)) sent state=%q, want %q", got, stateClosed)
	}
	if _, err := h.client.ListRepos(t.Context()); err != nil {
		t.Fatalf("ListRepos() = %v, want the page", err)
	}
	if got := h.instance.query(reposRoute, "state"); got != "" {
		t.Errorf("ListRepos() sent state=%q, want no state: its route fixes what it answers", got)
	}
}

func TestThePullRequestListTakesTheMergedState(t *testing.T) {
	h := newHarness(t, map[string]string{documentRoute: prListEnvelope(t, false, "")})
	if _, err := h.client.ListPRs(t.Context(), testRef(), forgeapi.WithState(forgeapi.ListStateMerged)); err != nil {
		t.Fatalf("ListPRs(WithState(merged)) = %v, want the page", err)
	}
	var posted struct {
		Variables struct {
			States []string `json:"states"`
		} `json:"variables"`
	}
	if err := json.Unmarshal([]byte(h.instance.body(documentRoute)), &posted); err != nil {
		t.Fatalf("Setup: decoding the posted document: %v", err)
	}
	if len(posted.Variables.States) != 1 || posted.Variables.States[0] != "MERGED" {
		t.Errorf("ListPRs(WithState(merged)) sent states %v, want [MERGED]", posted.Variables.States)
	}
}

func TestAListResumesFromItsOwnContinuationAtThePageItNames(t *testing.T) {
	for _, test := range []struct {
		want string
		page int
	}{
		{page: 1, want: "1"},
		{page: 2, want: "2"},
	} {
		t.Run("page_"+test.want, func(t *testing.T) {
			h := newHarness(t, map[string]string{labelsRoute: `[{"name":"example-label"}]`})
			after := restCursor(t, h.client, "ListLabels", testRef(), test.page)
			if _, err := h.client.ListLabels(t.Context(), testRef(), forgeapi.WithAfter(after)); err != nil {
				t.Fatalf("ListLabels(WithAfter(page %d)) = %v, want the page", test.page, err)
			}
			if got := h.instance.query(labelsRoute, keyPage); got != test.want {
				t.Errorf("ListLabels(WithAfter(page %d)) sent page=%q, want %q", test.page, got, test.want)
			}
			if !h.logged(" page=" + test.want + " ") {
				t.Errorf("ListLabels(WithAfter(page %d)): no request record names page=%s: %s", test.page, test.want, h.logs)
			}
		})
	}
}

func TestAContinuationThisListDidNotMintIsRefusedBeforeAnyRequest(t *testing.T) {
	h := newHarness(t, nil)
	for name, after := range map[string]forgeapi.Cursor{
		"page_zero":         restCursor(t, h.client, "ListLabels", testRef(), 0),
		"another_lists_own": restCursor(t, h.client, "ListReleases", testRef(), 2),
	} {
		t.Run(name, func(t *testing.T) {
			before := h.instance.count()
			_, err := h.client.ListLabels(t.Context(), testRef(), forgeapi.WithAfter(after))
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
				t.Errorf("ListLabels(WithAfter(%s)) = %v, want code %q", name, err, forgeapi.CodeCursorInvalid)
			}
			if sent := h.instance.count() - before; sent != 0 {
				t.Errorf("ListLabels(WithAfter(%s)) sent %d request(s), want none", name, sent)
			}
		})
	}
}

func TestAListAtItsPageCapMarksTheAnswerPartial(t *testing.T) {
	next := http.Header{"Link": []string{`<https://forge.example/repositories/1/labels?page=2>; rel="next"`}}
	for _, test := range []struct {
		header  http.Header
		name    string
		pages   int
		partial bool
	}{
		{name: "at_the_cap_with_a_next_page", pages: 1, header: next, partial: true},
		{name: "at_the_cap_on_the_last_page", pages: 1},
		{name: "under_the_cap_with_a_next_page", pages: 2, header: next},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{labelsRoute: `[{"name":"example-label"}]`}, forgeapi.WithListPages(test.pages))
			h.instance.answerHeaders(labelsRoute, test.header)
			page, err := h.client.ListLabels(t.Context(), testRef())
			if err != nil {
				t.Fatalf("ListLabels = %v, want the page", err)
			}
			counted := h.spy.times("PartialResult:" + forgeapi.PartialPaginationCap.String())
			if !test.partial {
				if page.Partial != nil || counted != 0 {
					t.Errorf("ListLabels = partial %+v counted %d time(s), want none", page.Partial, counted)
				}
				return
			}
			want := forgeapi.Partial{Reason: forgeapi.PartialPaginationCap, Fetched: 1, OmittedAtLeast: 1}
			if page.Partial == nil || *page.Partial != want {
				t.Errorf("ListLabels = partial %+v, want %+v", page.Partial, want)
			}
			if counted != 1 {
				t.Errorf("ListLabels counted the page cap %d time(s), want 1", counted)
			}
		})
	}
}
