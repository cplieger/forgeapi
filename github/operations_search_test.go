package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// searchPageRoutes answers both cross-repository documents with a next page, so a
// call mints the continuation a later call is handed. The page holds no row, since
// the one route answers both documents and a row of one search's type is not a row
// of the other's.
func searchPageRoutes(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		documentRoute: documentEnvelope(`"search":{"issueCount":2,"pageInfo":{"hasNextPage":true,"endCursor":"Y3Vyc29yOjE="},"nodes":[]}`),
	}
}

// sentFirst is the page size one posted document asked for.
func sentFirst(t *testing.T, body string) int {
	t.Helper()
	var posted struct {
		Variables struct {
			First *int `json:"first"`
		} `json:"variables"`
	}
	if err := json.Unmarshal([]byte(body), &posted); err != nil || posted.Variables.First == nil {
		t.Fatalf("Setup: the posted document %s carries no first variable: %v", body, err)
	}
	return *posted.Variables.First
}

// TestThePullRequestSearchAsksForAtMostItsPageCeiling holds the two cross-repository
// documents to the page each asks for: the pull-request search sends the page bound
// up to 25 under either scope, because a larger page of its rows outruns the
// upstream's evaluation time, and the issue search sends the bound as asked, since a
// page of 100 of its rows answered where the pull-request search's did not.
func TestThePullRequestSearchAsksForAtMostItsPageCeiling(t *testing.T) {
	for _, test := range []struct {
		list func(context.Context, *harness, ...forgeapi.ListOption) error
		name string
		opts []forgeapi.ListOption
		want int
	}{
		{name: "ListMyPRs_default", list: listMyPRs, want: 25},
		{name: "ListMyPRs_owner_default", list: listMyPRs, opts: []forgeapi.ListOption{forgeapi.WithOwner(testOwner)}, want: 25},
		{name: "ListMyPRs_bound_over_the_ceiling", list: listMyPRs, opts: []forgeapi.ListOption{forgeapi.WithPageBound(26)}, want: 25},
		{name: "ListMyPRs_bound_under_the_ceiling", list: listMyPRs, opts: []forgeapi.ListOption{forgeapi.WithPageBound(10)}, want: 10},
		{name: "ListMyIssues_default", list: listMyIssues, want: forgeapi.DefaultPageBound},
		{name: "ListMyIssues_owner_bound", list: listMyIssues, opts: []forgeapi.ListOption{forgeapi.WithOwner(testOwner), forgeapi.WithPageBound(60)}, want: 60},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, searchPageRoutes(t))
			if err := test.list(t.Context(), h, test.opts...); err != nil {
				t.Fatalf("%s = %v, want the page", test.name, err)
			}
			if got := sentFirst(t, h.instance.body(documentRoute)); got != test.want {
				t.Errorf("%s sent first = %d, want %d", test.name, got, test.want)
			}
		})
	}
}

func listMyPRs(ctx context.Context, h *harness, opts ...forgeapi.ListOption) error {
	_, err := h.client.ListMyPRs(ctx, opts...)
	return err
}

func listMyIssues(ctx context.Context, h *harness, opts ...forgeapi.ListOption) error {
	_, err := h.client.ListMyIssues(ctx, opts...)
	return err
}

// TestASearchContinuationNamesNoPositionInAnyOtherList holds the search lists'
// continuation apart from the two other encodings this family mints. It carries the
// rows the walk has been served beside the connection's position, so a repository
// list document or a REST list handed it would resume from a position in a
// different connection, and a search list handed one of theirs would count a walk it
// never saw. Each is refused with the continuation code before any request, and its
// message names the kind of list the continuation does not belong to, which is the
// change a caller has to make.
func TestASearchContinuationNamesNoPositionInAnyOtherList(t *testing.T) {
	h := newHarness(t, searchPageRoutes(t))
	page, err := h.client.ListMyPRs(t.Context())
	if err != nil || page.Next == "" {
		t.Fatalf("Setup: ListMyPRs = Next %q, %v, want a continuation: the search answered a next page", page.Next, err)
	}
	searchNext := page.Next
	for _, test := range []struct {
		call  func() error
		name  string
		names string
	}{
		{name: "ListPRs_given_a_search_continuation", names: "search", call: func() error {
			_, err := h.client.ListPRs(t.Context(), testRef(), forgeapi.WithAfter(searchNext))
			return err
		}},
		{name: "ListLabels_given_a_search_continuation", names: "documents", call: func() error {
			_, err := h.client.ListLabels(t.Context(), testRef(), forgeapi.WithAfter(searchNext))
			return err
		}},
		{name: "ListMyPRs_given_a_list_document_continuation", names: "search", call: func() error {
			_, err := h.client.ListMyPRs(t.Context(), forgeapi.WithAfter(encodeAfter(listCall(t, h.client, "ListPRs", testRef()), "Y3Vyc29yOjE=")))
			return err
		}},
		{name: "ListMyIssues_given_a_list_document_continuation", names: "search", call: func() error {
			_, err := h.client.ListMyIssues(t.Context(), forgeapi.WithAfter(encodeAfter(listCall(t, h.client, "ListPRs", testRef()), "Y3Vyc29yOjE=")))
			return err
		}},
		{name: "ListMyIssues_given_a_REST_continuation", names: "search", call: func() error {
			_, err := h.client.ListMyIssues(t.Context(), forgeapi.WithAfter(restCursor(t, h.client, "ListLabels", testRef(), 2)))
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
			if !strings.Contains(fe.Message, test.names) {
				t.Errorf("%s = message %q, want it to name %q", test.name, fe.Message, test.names)
			}
			if sent := h.instance.count() - before; sent != 0 {
				t.Errorf("%s sent %d request(s), want none: a continuation from another list is refused before the wire", test.name, sent)
			}
		})
	}
}

// TestASearchContinuationResumesOnlyTheSearchThatMintedIt holds each search
// continuation to the query it walks. The two searches read one connection, so a
// position from one item type's walk, or one scope's, is one upstream would accept
// in another and resume at an offset that walk never reached. Each crossing is
// refused with the continuation code before any request, and the search that minted
// a continuation resumes from it.
func TestASearchContinuationResumesOnlyTheSearchThatMintedIt(t *testing.T) {
	h := newHarness(t, searchPageRoutes(t))
	mint := func(t *testing.T, list func(context.Context, ...forgeapi.ListOption) (forgeapi.Cursor, error), opts ...forgeapi.ListOption) forgeapi.Cursor {
		t.Helper()
		next, err := list(t.Context(), opts...)
		if err != nil || next == "" {
			t.Fatalf("Setup: a search page = Next %q, %v, want a continuation: the search answered a next page", next, err)
		}
		return next
	}
	prs := func(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Cursor, error) {
		page, err := h.client.ListMyPRs(ctx, opts...)
		return page.Next, err
	}
	issues := func(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Cursor, error) {
		page, err := h.client.ListMyIssues(ctx, opts...)
		return page.Next, err
	}
	viewerPRs := mint(t, prs)
	viewerIssues := mint(t, issues)
	ownerPRs := mint(t, prs, forgeapi.WithOwner(testOwner))
	for _, test := range []struct {
		list  func(context.Context, ...forgeapi.ListOption) (forgeapi.Cursor, error)
		name  string
		after forgeapi.Cursor
		opts  []forgeapi.ListOption
	}{
		{name: "pull_request_walk_to_ListMyIssues", list: issues, after: viewerPRs},
		{name: "issue_walk_to_ListMyPRs", list: prs, after: viewerIssues},
		{name: "viewer_walk_to_an_owner", list: prs, after: viewerPRs, opts: []forgeapi.ListOption{forgeapi.WithOwner(testOwner)}},
		{name: "owner_walk_to_the_viewer", list: prs, after: ownerPRs},
		{name: "owner_walk_to_another_owner", list: prs, after: ownerPRs, opts: []forgeapi.ListOption{forgeapi.WithOwner(testOwner + "-other")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := h.instance.count()
			_, err := test.list(t.Context(), append(slices.Clone(test.opts), forgeapi.WithAfter(test.after))...)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
				t.Fatalf("%s = %v, want code %q", test.name, err, forgeapi.CodeCursorInvalid)
			}
			if sent := h.instance.count() - before; sent != 0 {
				t.Errorf("%s sent %d request(s), want none: a continuation from another search is refused before the wire", test.name, sent)
			}
		})
	}
	for name, resume := range map[string]func() error{
		"ListMyPRs_viewer": func() error { _, err := prs(t.Context(), forgeapi.WithAfter(viewerPRs)); return err },
		"ListMyIssues_viewer": func() error {
			_, err := issues(t.Context(), forgeapi.WithAfter(viewerIssues))
			return err
		},
		"ListMyPRs_owner": func() error {
			_, err := prs(t.Context(), forgeapi.WithOwner(testOwner), forgeapi.WithAfter(ownerPRs))
			return err
		},
	} {
		if err := resume(); err != nil {
			t.Errorf("%s resumed from its own continuation = %v, want the next page", name, err)
		}
	}
}

// TestTheSearchContinuationCarriesItsPositionAndServedCount holds the continuation
// a search page mints to the facts a later page needs: the connection's own position,
// as the bytes upstream sent, the rows the walk has been served, which is what the
// page ending the walk counts, and the call the walk makes. It passes the validator a
// consumer hands it back through, even under the longest owner the shared form
// admits, and a page with no further page mints none.
func TestTheSearchContinuationCarriesItsPositionAndServedCount(t *testing.T) {
	client := newHarness(t, nil).client
	viewer := listCall(t, client, "ListMyPRs", forgeapi.RepoRef{})
	longest := listCall(t, client, "ListMyIssues", forgeapi.RepoRef{}, forgeapi.WithOwner(strings.Repeat("o", 255)))
	for _, test := range []struct {
		position string
		name     string
		call     transport.PageCall
		served   int
	}{
		{position: "Y3Vyc29yOjI1", name: "viewer", call: viewer, served: 25},
		{position: "Y3Vyc29yOnYyOpHOAAGgsA==", name: "viewer", call: viewer, served: 1},
		{position: "a/b+c==", name: "viewer", call: viewer, served: 0},
		{position: "Y3Vyc29yOjEwMDA=", name: "longest_owner", call: longest, served: maxSearchServed},
	} {
		next := searchNext(docPageInfo{HasNextPage: true, EndCursor: test.position}, test.call, test.served)
		if err := forgeapi.ValidateCursor(next); err != nil {
			t.Errorf("ValidateCursor(searchNext(%q, %s, %d)) = %v, want nil: a continuation this library mints has to pass its own validator", test.position, test.name, test.served, err)
		}
		position, served, err := decodeSearchAfter(next, test.call)
		if err != nil || position != test.position || served != test.served {
			t.Errorf("decodeSearchAfter(searchNext(%q, %s, %d)) = %q, %d, %v, want the position and the count back", test.position, test.name, test.served, position, served, err)
		}
	}
	for _, info := range []docPageInfo{{HasNextPage: false, EndCursor: "Y3Vyc29yOjI1"}, {HasNextPage: true}} {
		if got := searchNext(info, viewer, 3); got != "" {
			t.Errorf("searchNext(%+v, viewer, 3) = %q, want empty: no request reaches a further page", info, got)
		}
	}
	if position, served, err := decodeSearchAfter("", viewer); position != "" || served != 0 || err != nil {
		t.Errorf("decodeSearchAfter(the first page) = %q, %d, %v, want no position, none served", position, served, err)
	}
}

// TestASearchContinuationWhoseCountIsNotOneThisLibraryMintedIsRefused holds the
// decoder to the count and the call a continuation crosses with, since a
// continuation arrives at a consumer's route as untrusted input: a count that is
// negative, not a number, past the bound or absent is refused with the continuation
// code, as is a continuation naming no call, another call, or no position.
func TestASearchContinuationWhoseCountIsNotOneThisLibraryMintedIsRefused(t *testing.T) {
	// The encoding's separator, and the digest every continuation the call mints
	// ends with.
	const sep = "."
	client := newHarness(t, nil).client
	call := listCall(t, client, "ListMyPRs", forgeapi.RepoRef{})
	digestOf := func(call transport.PageCall) string {
		minted := string(searchNext(docPageInfo{HasNextPage: true, EndCursor: "Y3Vyc29yOjI1"}, call, 5))
		return minted[strings.LastIndex(minted, sep)+1:]
	}
	digest := digestOf(call)
	position := base64.RawURLEncoding.EncodeToString([]byte("Y3Vyc29yOjI1"))
	walk := func(count, position, digest string) string {
		return searchPrefix + count + sep + position + sep + digest
	}
	for name, c := range map[string]string{
		"negative":         walk("-1", position, digest),
		"not_a_number":     walk("x", position, digest),
		"past_the_bound":   walk(strconv.Itoa(maxSearchServed+1), position, digest),
		"overflowing":      walk("99999999999999999999", position, digest),
		"no_separator":     searchPrefix + "5" + position + digest,
		"no_count":         walk("", position, digest),
		"no_digest":        searchPrefix + "5" + sep + position,
		"empty_digest":     walk("5", position, ""),
		"another_call":     walk("5", position, digestOf(listCall(t, client, "ListMyIssues", forgeapi.RepoRef{}))),
		"digest_not_text":  walk("5", position, "!"),
		"no_position":      walk("5", "", digest),
		"position_not_b64": walk("5", "A", digest),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := decodeSearchAfter(forgeapi.Cursor(c), call)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
				t.Errorf("decodeSearchAfter(%q) = %v, want code %q", c, err, forgeapi.CodeCursorInvalid)
			}
		})
	}
}

// searchRowsCase is one cross-repository search driven over one answer: the
// operation, the recorded row of its own type, and a member of the other type.
type searchRowsCase struct {
	list  func(context.Context, *harness) (rows int, partial *forgeapi.Partial, err error)
	name  string
	row   string
	other string
}

func searchRowsCases() []searchRowsCase {
	return []searchRowsCase{
		{name: "ListMyPRs", row: "graphql_search_row", other: `{"__typename":"Issue"}`, list: func(ctx context.Context, h *harness) (int, *forgeapi.Partial, error) {
			page, err := h.client.ListMyPRs(ctx)
			return len(page.Items), page.Partial, err
		}},
		{name: "ListMyIssues", row: "graphql_issue_search_row", other: `{"__typename":"PullRequest"}`, list: func(ctx context.Context, h *harness) (int, *forgeapi.Partial, error) {
			page, err := h.client.ListMyIssues(ctx)
			return len(page.Items), page.Partial, err
		}},
	}
}

// searchAnswer is one search page holding nodes, with the envelope errors that
// null the first of them where nulled is set.
func searchAnswer(total int, nodes []string, nulled bool) string {
	body := documentEnvelope(`"search":{"issueCount":` + strconv.Itoa(total) +
		`,"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[` + strings.Join(nodes, ",") + `]}`)
	if !nulled {
		return body
	}
	return strings.TrimSuffix(body, "}") +
		`,"errors":[{"type":"FORBIDDEN","message":"Resource not accessible","path":["search","nodes",0]}]}`
}

// A search's nodes are nullable members of a union, so a page beside the envelope
// errors that null a node serves the rows that are of its own type and marks itself
// partial, and never answers the null node or another type's member as a row: a
// zero row addresses nothing and would sit in a consumer's cache as one.
func TestASearchPageServesOnlyTheRowsOfItsOwnTypeBesideTheErrorsThatNullOne(t *testing.T) {
	for _, test := range searchRowsCases() {
		row := readCaptures(t).raw(t, test.row)
		for name, nodes := range map[string][]string{"a_null_node": {"null", row}, "another_types_member": {test.other, row}} {
			t.Run(test.name+"_"+name, func(t *testing.T) {
				h := newHarness(t, map[string]string{documentRoute: searchAnswer(2, nodes, true)})
				rows, partial, err := test.list(t.Context(), h)
				if err != nil {
					t.Fatalf("%s over %s beside an error nulling it = %v, want the page", test.name, name, err)
				}
				if rows != 1 {
					t.Errorf("%s over %s and one row = %d row(s), want 1: only a row of the search's own type is one", test.name, name, rows)
				}
				if partial == nil || partial.Reason != forgeapi.PartialGraphQLPartial {
					t.Errorf("%s over %s beside an error = partial %v, want %v", test.name, name, partial, forgeapi.PartialGraphQLPartial)
				}
			})
		}
	}
}

// With no envelope error to account for it, a null node or another type's member
// is a page no search answers, and it is refused as malformed rather than served.
func TestASearchPageWithANodeNoErrorAccountsForIsRefused(t *testing.T) {
	for _, test := range searchRowsCases() {
		row := readCaptures(t).raw(t, test.row)
		for name, nodes := range map[string][]string{"a_null_node": {"null", row}, "another_types_member": {test.other, row}} {
			t.Run(test.name+"_"+name, func(t *testing.T) {
				h := newHarness(t, map[string]string{documentRoute: searchAnswer(2, nodes, false)})
				_, _, err := test.list(t.Context(), h)
				var fe *forgeapi.Error
				if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeValidation || fe.Kind != forgeapi.KindUpstream {
					t.Errorf("%s over %s with no error beside it = %v, want code %q of kind %v", test.name, name, err, forgeapi.CodeValidation, forgeapi.KindUpstream)
				}
			})
		}
	}
}

// The search's total is its own count of results, so one below zero counts nothing
// and is refused, never read as a complete page with nothing beyond it.
func TestASearchPageWhoseTotalIsNegativeIsRefused(t *testing.T) {
	for _, test := range searchRowsCases() {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: searchAnswer(-1, nil, false)})
			_, _, err := test.list(t.Context(), h)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeValidation || fe.Kind != forgeapi.KindUpstream || fe.Status != http.StatusOK {
				t.Errorf("%s over an issueCount of -1 = %v, want code %q of kind %v at status 200", test.name, err, forgeapi.CodeValidation, forgeapi.KindUpstream)
			}
		})
	}
}
