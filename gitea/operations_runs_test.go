package gitea

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// clampedRunPage is one page of a run listing whose instance served fewer rows than
// were asked for while its stated total says more exist, the page a clamping
// instance answers on every page number.
var clampedRunPage = `{"total_count":150,"workflow_runs":[` + strings.Repeat(forgejoRun+",", 49) + forgejoRun + `]}`

// TestAnUnpagedRunListingHandsBackAContinuationTheListingResumesFrom holds the page
// cap's zero to the run listing's own encoding. A response-level pagination cap
// always arrives with the continuation naming the page nobody asked for, and on the
// run listing that continuation has to be one the listing itself reads: naming the
// first page with nothing served where the call began a walk, and keeping the served
// count where it continued one, or the resumed walk would decide its next page
// against a count it never saw. Both clients are one connection, since a continuation
// resumes on the connection that minted it alone.
func TestAnUnpagedRunListingHandsBackAContinuationTheListingResumesFrom(t *testing.T) {
	unpaged := newHarness(t, map[string]string{runsRoute: clampedRunPage}, forgeapi.WithListPages(0))
	paged := newHarnessOver(t, unpaged.instance, time.Now)

	first, err := unpaged.client.ListRuns(t.Context(), testRef())
	if err != nil {
		t.Fatalf("ListRuns under a zero page cap = %v, want an empty page rather than a refusal", err)
	}
	if spent := unpaged.instance.count(); spent != 0 {
		t.Errorf("ListRuns under a zero page cap sent %d request(s), want 0: its own cap admits no page", spent)
	}
	if first.Partial == nil || first.Partial.Reason != forgeapi.PartialPaginationCap || first.Next == "" {
		t.Fatalf("ListRuns under a zero page cap = partial %v, Next %q, want the pagination cap with a continuation", first.Partial, first.Next)
	}
	resumed, err := paged.client.ListRuns(t.Context(), testRef(), forgeapi.WithAfter(first.Next))
	if err != nil {
		t.Fatalf("ListRuns(WithAfter(%q)) = %v, want the first page: the unpaged answer's continuation is the run listing's own", first.Next, err)
	}
	if got := paged.instance.query(runsRoute, keyPage); got != "1" {
		t.Errorf("ListRuns(WithAfter(%q)) sent page=%q, want %q: the page nobody asked for was the first", first.Next, got, "1")
	}
	if resumed.Next == "" {
		t.Fatalf("Setup: ListRuns over 50 of 150 runs = no continuation, want one")
	}

	held, err := unpaged.client.ListRuns(t.Context(), testRef(), forgeapi.WithAfter(resumed.Next))
	if err != nil {
		t.Fatalf("ListRuns(WithAfter(%q)) under a zero page cap = %v, want an empty page", resumed.Next, err)
	}
	if held.Next != resumed.Next {
		t.Errorf("ListRuns(WithAfter(%q)) under a zero page cap = Next %q, want %q: the page nobody asked for keeps the rows its walk was served",
			resumed.Next, held.Next, resumed.Next)
	}
}

// TestARunContinuationNamesNoPositionInAnyOtherList holds the run listing's
// continuation apart from the page-numbered one every other list of this family
// mints. It carries the rows the walk has been served beside the page, so another
// list handed it would resume at a page of a different listing, and the run listing
// handed another list's could not tell how much of its walk was served. Each is
// refused with the continuation code before any request, and its message names the
// run listing, which is the change a caller has to make.
func TestARunContinuationNamesNoPositionInAnyOtherList(t *testing.T) {
	h := newHarness(t, map[string]string{
		runsRoute:                `{"total_count":150,"workflow_runs":[` + forgejoRun + `]}`,
		"GET /api/v1/user/repos": "[" + repoRow + "]",
	})
	runs, err := h.client.ListRuns(t.Context(), testRef())
	if err != nil || runs.Next == "" {
		t.Fatalf("Setup: ListRuns over 1 of 150 runs = Next %q, %v, want a continuation", runs.Next, err)
	}
	repos, err := h.client.ListRepos(t.Context(), forgeapi.WithPageBound(1))
	if err != nil || repos.Next == "" {
		t.Fatalf("Setup: ListRepos over a full page = Next %q, %v, want a continuation", repos.Next, err)
	}
	for _, test := range []struct {
		call func() error
		name string
	}{
		{name: "ListRepos_given_a_run_continuation", call: func() error {
			_, err := h.client.ListRepos(t.Context(), forgeapi.WithAfter(runs.Next))
			return err
		}},
		{name: "ListPRs_given_a_run_continuation", call: func() error {
			_, err := h.client.ListPRs(t.Context(), testRef(), forgeapi.WithAfter(runs.Next))
			return err
		}},
		{name: "ListLabels_given_a_run_continuation", call: func() error {
			_, err := h.client.ListLabels(t.Context(), testRef(), forgeapi.WithAfter(runs.Next))
			return err
		}},
		{name: "ListRuns_given_a_page_continuation", call: func() error {
			_, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithAfter(repos.Next))
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := h.instance.count()
			err := test.call()
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
				t.Fatalf("%s = %v, want code %q", test.name, err, forgeapi.CodeCursorInvalid)
			}
			if !strings.Contains(fe.Message, "run listing") {
				t.Errorf("%s = message %q, want it to name the run listing", test.name, fe.Message)
			}
			if sent := h.instance.count() - before; sent != 0 {
				t.Errorf("%s sent %d request(s), want none: a continuation from another list is refused before the wire", test.name, sent)
			}
		})
	}
}

// runCall is the call the run listing makes at the default options on the
// canonical subject, on a harness connection, which every continuation below is
// minted by.
func runCall(t *testing.T) transport.PageCall {
	t.Helper()
	set, err := forgeapi.ResolveList()
	if err != nil {
		t.Fatalf("Setup: ResolveList() = %v", err)
	}
	return newHarness(t, nil).client.core.PageCall("ListRuns", testRef(), set)
}

// TestTheRunContinuationCarriesItsPageAndServedCount holds the continuation a run
// page mints to the three facts the next page needs, the page it reads, the rows
// the walk was served and the limit its pages ask for, and to the validator a
// consumer hands it back through. A page that brings the walk to the stated total
// mints none, and neither does a page that served no row, since no later page can
// serve what it did not.
func TestTheRunContinuationCarriesItsPageAndServedCount(t *testing.T) {
	call := runCall(t)
	for _, test := range []struct {
		walk  runWalk
		rows  int
		total int
		want  runWalk
	}{
		{walk: runWalk{call: call, page: 1, limit: 100}, rows: 50, total: 2006, want: runWalk{call: call, page: 2, served: 50, limit: 100}},
		{walk: runWalk{call: call, page: 2, served: 50, limit: 100}, rows: 50, total: 2006, want: runWalk{call: call, page: 3, served: 100, limit: 100}},
		{walk: runWalk{call: call, page: 1, limit: 2}, rows: 2, total: 5, want: runWalk{call: call, page: 2, served: 2, limit: 2}},
	} {
		next := test.walk.next(test.rows, test.total)
		if err := forgeapi.ValidateCursor(next); err != nil {
			t.Errorf("ValidateCursor(%+v.next(%d, %d)) = %v, want nil: a continuation this library mints has to pass its own validator", test.walk, test.rows, test.total, err)
		}
		got, err := runWalkAt(call, next)
		if err != nil || got != test.want {
			t.Errorf("runWalkAt(%+v.next(%d, %d)) = %+v, %v, want %+v", test.walk, test.rows, test.total, got, err, test.want)
		}
	}
	for _, test := range []struct {
		walk  runWalk
		rows  int
		total int
	}{
		{walk: runWalk{call: call, page: 1, limit: 100}, rows: 100, total: 100},
		{walk: runWalk{call: call, page: 3, served: 100, limit: 100}, rows: 20, total: 120},
		{walk: runWalk{call: call, page: 2, served: 50, limit: 100}, rows: 50, total: 90},
		{walk: runWalk{call: call, page: 1, limit: 100}, rows: 0, total: 0},
		{walk: runWalk{call: call, page: 1, limit: 100}, rows: 0, total: 1},
		{walk: runWalk{call: call, page: 4, served: 150, limit: 50}, rows: 0, total: 200},
	} {
		if got := test.walk.next(test.rows, test.total); got != "" {
			t.Errorf("%+v.next(%d, %d) = %q, want empty: the walk has been served the stated total or the page served none", test.walk, test.rows, test.total, got)
		}
	}
	large := runWalk{call: call, page: 2, served: 1, limit: maxRunWalk + 1}
	if got, err := runWalkAt(call, large.here()); got != large || err != nil {
		t.Errorf("runWalkAt(%q) = %+v, %v, want %+v: a walk's limit is the page bound its first page asked for, which may be any positive one", large.here(), got, err, large)
	}
	if got, err := runWalkAt(call, ""); got != (runWalk{call: call, page: 1}) || err != nil {
		t.Errorf("runWalkAt(the first page) = %+v, %v, want page 1 with nothing served and no limit fixed", got, err)
	}
	unfixed := runWalk{call: call, page: 1}
	if got, err := runWalkAt(call, unfixed.here()); got != unfixed || err != nil {
		t.Errorf("runWalkAt(%q) = %+v, %v, want %+v: the first page of a walk no page has fixed is what an unpaged answer hands back", unfixed.here(), got, err, unfixed)
	}
}

// TestARunContinuationThisLibraryDidNotMintIsRefused holds the decoder to the page,
// the count and the limit a continuation crosses with, since a continuation arrives
// at a consumer's route as untrusted input: a page below one, a count or a limit
// below zero, a limit of zero past the first page, a page or a count past the bound,
// any of them not a number, and a continuation missing any of them is refused with
// the continuation code. The limit is a page bound the caller asked for, so any
// positive one is read back.
func TestARunContinuationThisLibraryDidNotMintIsRefused(t *testing.T) {
	call := runCall(t)
	// The encoding's separator, and what follows the limit in a continuation the
	// call mints: its page bound and its digest.
	const sep = "."
	minted := string(runWalk{call: call, page: 2, served: 50, limit: 100}.here())
	fields := strings.SplitN(minted, sep, 4)
	if len(fields) != 4 {
		t.Fatalf("Setup: %q carries %d field(s), want the page, the count, the limit and the call", minted, len(fields))
	}
	tail := sep + fields[3]
	bound := strconv.Itoa(maxRunWalk + 1)
	walk := func(page, served, limit string) string {
		return runCursorPrefix + page + sep + served + sep + limit + tail
	}
	for name, c := range map[string]string{
		"page_zero":            walk("0", "0", "100"),
		"page_negative":        walk("-1", "0", "100"),
		"served_negative":      walk("2", "-1", "100"),
		"limit_zero":           walk("2", "50", "0"),
		"limit_negative":       walk("2", "50", "-1"),
		"page_past_the_bound":  walk(bound, "0", "100"),
		"count_past_the_bound": walk("2", bound, "100"),
		"limit_overflowing":    walk("2", "50", "99999999999999999999"),
		"overflowing":          walk("2", "99999999999999999999", "100"),
		"not_a_number":         walk("2", "x", "100"),
		"no_separator":         runCursorPrefix + "250",
		"no_count":             walk("2", "", "100"),
		"no_page":              walk("", "50", "100"),
		"no_limit":             runCursorPrefix + "2" + sep + "50" + tail,
		"empty_limit":          walk("2", "50", ""),
		"a_fourth_field":       walk("2", "50", "100") + sep + "1",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runWalkAt(call, forgeapi.Cursor(c))
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
				t.Errorf("runWalkAt(%q) = %v, want code %q", c, err, forgeapi.CodeCursorInvalid)
			}
		})
	}
}
