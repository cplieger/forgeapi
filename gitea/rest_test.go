package gitea

import (
	"testing"

	"github.com/cplieger/forgeapi"
)

const (
	userReposRoute = "GET /api/v1/user/repos"
	pullsRoute     = "GET /api/v1/repos/example/example/pulls"
	issuesRoute    = "GET /api/v1/repos/example/example/issues"
)

func TestAListRefusesAnOptionItsRouteCannotApplyBeforeAnyRequest(t *testing.T) {
	for _, test := range []struct {
		call func(h *harness) error
		name string
		code string
	}{
		{name: "a_state_on_a_list_whose_route_fixes_it", code: forgeapi.CodeListStateInvalid, call: func(h *harness) error {
			_, err := h.client.ListRepos(t.Context(), forgeapi.WithState(forgeapi.ListStateClosed))
			return err
		}},
		{name: "an_owner_on_a_list_no_owner_scopes", code: forgeapi.CodeListOwnerInvalid, call: func(h *harness) error {
			_, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner(testOwner))
			return err
		}},
		{name: "an_owner_written_as_a_path", code: forgeapi.CodeListOwnerInvalid, call: func(h *harness) error {
			_, err := h.client.ListMyPRs(t.Context(), forgeapi.WithOwner(testOwner+"/nested"))
			return err
		}},
		{name: "the_merged_state_on_an_issue_list", code: forgeapi.CodeListStateInvalid, call: func(h *harness) error {
			_, err := h.client.ListIssues(t.Context(), testRef(), forgeapi.WithState(forgeapi.ListStateMerged))
			return err
		}},
		{name: "an_option_the_shared_form_refuses", call: func(h *harness) error {
			_, err := h.client.ListRepos(t.Context(), forgeapi.WithPageBound(0))
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				userReposRoute:                    "[" + repoRow + "]",
				issuesRoute:                       "[" + issueBody + "]",
				"GET /api/v1/repos/issues/search": "[" + searchRow(false) + "]",
			})
			err := test.call(h)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) {
				t.Fatalf("%s = %v, want a *forgeapi.Error", test.name, err)
			}
			if test.code != "" && fe.Code != test.code {
				t.Errorf("%s = code %q, want %q", test.name, fe.Code, test.code)
			}
			if sent := h.instance.arrived(); len(sent) != 0 {
				t.Errorf("%s sent %v, want nothing: the option is refused before the wire", test.name, sent)
			}
		})
	}
}

func TestAListSendsTheOptionsItsRouteApplies(t *testing.T) {
	h := newHarness(t, map[string]string{
		pullsRoute:                        "[]",
		"GET /api/v1/repos/issues/search": "[]",
	})
	if _, err := h.client.ListPRs(t.Context(), testRef(), forgeapi.WithState(forgeapi.ListStateClosed)); err != nil {
		t.Fatalf("ListPRs(WithState(closed)) = %v, want nil", err)
	}
	if got := h.instance.query(pullsRoute, "state"); got != stateClosed {
		t.Errorf("ListPRs(WithState(closed)) sent state=%q, want %q", got, stateClosed)
	}
	if _, err := h.client.ListPRs(t.Context(), testRef(), forgeapi.WithState(forgeapi.ListStateMerged)); err != nil {
		t.Errorf("ListPRs(WithState(merged)) = %v, want nil: a pull request has a merged state", err)
	}
	if _, err := h.client.ListMyIssues(t.Context(), forgeapi.WithOwner(testOwner)); err != nil {
		t.Fatalf("ListMyIssues(WithOwner(%q)) = %v, want nil", testOwner, err)
	}
	if got := h.instance.query("GET /api/v1/repos/issues/search", "owner"); got != testOwner {
		t.Errorf("ListMyIssues(WithOwner(%q)) sent owner=%q, want %q", testOwner, got, testOwner)
	}
}

func TestAListResumesFromItsOwnContinuationAtTheNextPage(t *testing.T) {
	h := newHarness(t, map[string]string{userReposRoute: "[" + repoRow + "]"})
	first, err := h.client.ListRepos(t.Context(), forgeapi.WithPageBound(1))
	if err != nil || first.Next == "" {
		t.Fatalf("Setup: ListRepos over a full page = Next %q, %v, want a continuation", first.Next, err)
	}
	if _, err := h.client.ListRepos(t.Context(), forgeapi.WithPageBound(1), forgeapi.WithAfter(first.Next)); err != nil {
		t.Fatalf("ListRepos(WithAfter(%q)) = %v, want the second page", first.Next, err)
	}
	if got := h.instance.query(userReposRoute, keyPage); got != "2" {
		t.Errorf("ListRepos(WithAfter(%q)) sent page=%q, want %q", first.Next, got, "2")
	}
	if got := h.instance.query(userReposRoute, keyLimit); got != "1" {
		t.Errorf("ListRepos(WithAfter(%q)) sent limit=%q, want %q: every page of a walk is sent at the limit it began at", first.Next, got, "1")
	}
	if !h.logged(" page=2 ") {
		t.Errorf("no request record names page=2, want the resumed read's: %s", h.logs)
	}
}

func TestAListAtItsPageCapMarksTheAnswerPartialAndCountsIt(t *testing.T) {
	capped := newHarness(t, map[string]string{userReposRoute: "[" + repoRow + "]"}, forgeapi.WithListPages(1))
	page, err := capped.client.ListRepos(t.Context(), forgeapi.WithPageBound(1))
	if err != nil {
		t.Fatalf("ListRepos at its page cap = %v, want nil", err)
	}
	want := forgeapi.Partial{Reason: forgeapi.PartialPaginationCap, Fetched: 1, OmittedAtLeast: 1}
	if page.Partial == nil || *page.Partial != want || page.Next == "" {
		t.Errorf("ListRepos at its page cap = partial %+v, Next %q, want %+v with a continuation", page.Partial, page.Next, want)
	}
	if got := capped.spy.times("PartialResult:" + forgeapi.PartialPaginationCap.String()); got != 1 {
		t.Errorf("the PartialResult counter fired %d time(s) for the page cap, want 1", got)
	}

	under := newHarness(t, map[string]string{userReposRoute: "[" + repoRow + "]"})
	page, err = under.client.ListRepos(t.Context(), forgeapi.WithPageBound(1))
	if err != nil {
		t.Fatalf("ListRepos under its page cap = %v, want nil", err)
	}
	if page.Partial != nil || page.Next == "" {
		t.Errorf("ListRepos under its page cap = partial %+v, Next %q, want no marker and a continuation", page.Partial, page.Next)
	}
}
