package conformance

import (
	"slices"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// mintBound is the page bound every continuation below is minted at: small enough
// that a paged instance's first page is full and a continuation exists.
const mintBound = 3

// mintedRows is how many rows each paged instance below holds, three pages at the
// mint bound.
const mintedRows = 3 * mintBound

// mintContinuation reads one list's first page at the mint bound with the options
// given and answers its continuation, failing the case's setup where none exists.
func mintContinuation(t *testing.T, e spec.Entry, client any, pi *pagedInstance, s subject, opts ...forgeapi.ListOption) forgeapi.Cursor {
	t.Helper()
	opts = append([]forgeapi.ListOption{forgeapi.WithPageBound(mintBound)}, opts...)
	got, _, err := callList(t, e, client, pi.rec, s, opts...)
	if err != nil {
		t.Fatalf("Setup: %s on %s = error %v, want a first page", e.Method, e.Product, err)
	}
	_, next, ok := pageParts(got)
	if !ok || next == "" {
		t.Fatalf("Setup: %s on %s over %d rows at a page bound of %d = (%v, Next %q), want a continuation", e.Method, e.Product, mintedRows, mintBound, got, next)
	}
	return next
}

// checkCursorRefused holds one resumed call to the refusal a continuation earns
// from a call other than the one that minted it: the cursor code, and no request.
func checkCursorRefused(t *testing.T, call string, err error, window []sent) {
	t.Helper()
	if len(window) != 0 {
		t.Errorf("%s sent %d request(s) [%s], want none: the continuation is refused before any request", call, len(window), describeRequests(window))
	}
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeCursorInvalid {
		t.Errorf("%s = error %v, want code %q: the continuation was minted by another call", call, err, forgeapi.CodeCursorInvalid)
	}
}

// A continuation resumes the call that minted it, at the bound it was minted at, at
// the page after the one it was minted on.
func TestAPageNumberedContinuationResumesTheCallThatMintedItAtItsBound(t *testing.T) {
	for _, p := range spec.Products {
		for _, e := range pageNumberedLists(t, p) {
			t.Run(caseName(e), func(t *testing.T) {
				pi := newPagedInstance(t, p, mintedRows, 50)
				client := pagedClient(t, p, pi.serve(t))
				s := canonicalSubject(p)
				next := mintContinuation(t, e, client, pi, s)

				got, window, err := callList(t, e, client, pi.rec, s, forgeapi.WithPageBound(mintBound), forgeapi.WithAfter(next))
				if err != nil {
					t.Fatalf("%s(WithPageBound(%d), WithAfter(%q)) on %s = (%v, error %v), want the second page", e.Method, mintBound, next, p, got, err)
				}
				pages := pi.listRequests(window)
				if len(pages) == 0 || pages[0].query.Get("page") != "2" {
					t.Errorf("%s(WithPageBound(%d), WithAfter(%q)) on %s sent [%s], want the second page", e.Method, mintBound, next, p, describeRequests(window))
				}
			})
		}
	}
}

// A page number counts pages of one size, so a continuation read under another
// bound would resume at a row the walk already served or past one it never did.
func TestAPageNumberedContinuationUnderAnotherPageBoundIsRefused(t *testing.T) {
	bounds := map[string][]forgeapi.ListOption{
		"larger_bound":  {forgeapi.WithPageBound(mintBound + 1)},
		"smaller_bound": {forgeapi.WithPageBound(mintBound - 1)},
		"default_bound": nil,
	}
	for _, p := range spec.Products {
		for _, e := range pageNumberedLists(t, p) {
			for name, bound := range bounds {
				t.Run(caseName(e)+"_"+name, func(t *testing.T) {
					pi := newPagedInstance(t, p, mintedRows, 50)
					client := pagedClient(t, p, pi.serve(t))
					s := canonicalSubject(p)
					next := mintContinuation(t, e, client, pi, s)

					_, window, err := callList(t, e, client, pi.rec, s, append(slices.Clone(bound), forgeapi.WithAfter(next))...)
					checkCursorRefused(t, e.Method+" on "+string(p)+" under another bound", err, window)
				})
			}
		}
	}
}

// One list's continuation names that list, so another list of the same product,
// whose rows are another collection, refuses it.
func TestAPageNumberedContinuationHandedToAnotherListIsRefused(t *testing.T) {
	for _, p := range spec.Products {
		entries := pageNumberedLists(t, p)
		for _, minted := range entries {
			for _, other := range entries {
				if other.Method == minted.Method {
					continue
				}
				t.Run(caseName(minted)+"_to_"+other.Method, func(t *testing.T) {
					pi := newPagedInstance(t, p, mintedRows, 50)
					client := pagedClient(t, p, pi.serve(t))
					s := canonicalSubject(p)
					next := mintContinuation(t, minted, client, pi, s)

					_, window, err := callList(t, other, client, pi.rec, s, forgeapi.WithPageBound(mintBound), forgeapi.WithAfter(next))
					checkCursorRefused(t, other.Method+" on "+string(p)+" handed "+minted.Method+"'s continuation", err, window)
				})
			}
		}
	}
}

// A continuation names the family whose client minted it, so the same list on a
// client of another family refuses it although the two calls share the repository,
// the state, the scope and the bound: the page number counts pages of the listing
// that family read, and the other family's listing is another collection.
func TestAPageNumberedContinuationHandedToAnotherFamilyIsRefused(t *testing.T) {
	for _, minter := range spec.Products {
		for _, resumer := range spec.Products {
			if family(minter) == family(resumer) {
				continue
			}
			resumable := map[string]spec.Entry{}
			for _, e := range pageNumberedLists(t, resumer) {
				resumable[e.Method] = e
			}
			for _, minted := range pageNumberedLists(t, minter) {
				other, ok := resumable[minted.Method]
				if !ok {
					continue
				}
				t.Run(caseName(minted)+"_to_"+string(resumer), func(t *testing.T) {
					began := newPagedInstance(t, minter, mintedRows, 50)
					next := mintContinuation(t, minted, pagedClient(t, minter, began.serve(t)), began, canonicalSubject(minter))

					elsewhere := newPagedInstance(t, resumer, mintedRows, 50)
					_, window, err := callList(t, other, pagedClient(t, resumer, elsewhere.serve(t)), elsewhere.rec, canonicalSubject(resumer),
						forgeapi.WithPageBound(mintBound), forgeapi.WithAfter(next))
					checkCursorRefused(t, other.Method+" on "+string(resumer)+" handed the continuation "+string(minter)+" minted", err, window)
				})
			}
		}
	}
}

// crossRepositoryList reports whether a list reads across repositories, which is
// the one kind of list a repository does not address.
func crossRepositoryList(method string) bool {
	return method == "Repos.ListRepos" || slices.Contains(crossRepositoryLists, method)
}

// A repository-addressed list's continuation names its repository, so the same list
// on another repository refuses it.
func TestAPageNumberedContinuationOnAnotherRepositoryIsRefused(t *testing.T) {
	for _, p := range spec.Products {
		for _, e := range pageNumberedLists(t, p) {
			if crossRepositoryList(e.Method) {
				continue
			}
			t.Run(caseName(e), func(t *testing.T) {
				pi := newPagedInstance(t, p, mintedRows, 50)
				client := pagedClient(t, p, pi.serve(t))
				s := canonicalSubject(p)
				next := mintContinuation(t, e, client, pi, s)

				elsewhere := s
				elsewhere.repo = otherRepo(p)
				_, window, err := callList(t, e, client, pi.rec, elsewhere, forgeapi.WithPageBound(mintBound), forgeapi.WithAfter(next))
				checkCursorRefused(t, e.Method+" on "+string(p)+" for "+elsewhere.repo.Selector, err, window)
			})
		}
	}
}

// stateLists are the per-repository lists whose state filter selects their rows.
var stateLists = []string{"PullRequests.ListPRs", "Issues.ListIssues"}

// A state filter selects which rows a list's pages count, so a continuation minted
// under one state is refused under another.
func TestAPageNumberedContinuationUnderAnotherStateIsRefused(t *testing.T) {
	others := map[string]forgeapi.ListState{"closed": forgeapi.ListStateClosed, "all": forgeapi.ListStateAll}
	for _, p := range spec.Products {
		for _, e := range pageNumberedLists(t, p) {
			if !slices.Contains(stateLists, e.Method) {
				continue
			}
			for name, state := range others {
				t.Run(caseName(e)+"_"+name, func(t *testing.T) {
					pi := newPagedInstance(t, p, mintedRows, 50)
					client := pagedClient(t, p, pi.serve(t))
					s := canonicalSubject(p)
					next := mintContinuation(t, e, client, pi, s, forgeapi.WithState(forgeapi.ListStateOpen))

					_, window, err := callList(t, e, client, pi.rec, s, forgeapi.WithPageBound(mintBound), forgeapi.WithState(state), forgeapi.WithAfter(next))
					checkCursorRefused(t, e.Method+" on "+string(p)+" minted open, resumed "+name, err, window)
				})
			}
		}
	}
}

// anotherOwner is an owner other than the canonical subject's.
const anotherOwner = "another-owner"

// scopeChanges are the scope a continuation is minted under and the scope a later
// call resumes it under, every pair of which names another set of rows.
var scopeChanges = map[string]struct {
	minted  []forgeapi.ListOption
	resumed []forgeapi.ListOption
}{
	"viewer_to_owner":        {minted: nil, resumed: []forgeapi.ListOption{forgeapi.WithOwner(owner)}},
	"owner_to_viewer":        {minted: []forgeapi.ListOption{forgeapi.WithOwner(owner)}, resumed: nil},
	"owner_to_another_owner": {minted: []forgeapi.ListOption{forgeapi.WithOwner(owner)}, resumed: []forgeapi.ListOption{forgeapi.WithOwner(anotherOwner)}},
}

// A cross-repository list's scope selects whose rows its pages count, so a
// continuation minted under one scope is refused under another.
func TestAPageNumberedContinuationUnderAnotherScopeIsRefused(t *testing.T) {
	for _, p := range spec.Products {
		for _, e := range pageNumberedLists(t, p) {
			if !slices.Contains(crossRepositoryLists, e.Method) {
				continue
			}
			for name, change := range scopeChanges {
				t.Run(caseName(e)+"_"+name, func(t *testing.T) {
					pi := newPagedInstance(t, p, mintedRows, 50)
					client := pagedClient(t, p, pi.serve(t))
					s := canonicalSubject(p)
					next := mintContinuation(t, e, client, pi, s, change.minted...)

					resumed := append(slices.Clone(change.resumed), forgeapi.WithPageBound(mintBound), forgeapi.WithAfter(next))
					_, window, err := callList(t, e, client, pi.rec, s, resumed...)
					checkCursorRefused(t, e.Method+" on "+string(p)+" "+name, err, window)
				})
			}
		}
	}
}

// GitHub's two cross-repository lists read a search whose continuation names the
// query it walks, so the scope rule holds on that product too.
func TestASearchContinuationUnderAnotherScopeIsRefused(t *testing.T) {
	for _, method := range crossRepositoryLists {
		for name, change := range scopeChanges {
			t.Run(method+"_"+name, func(t *testing.T) {
				e := requireEntry(t, spec.GitHub, method)
				srv, rec := newSearchServer(t, method,
					searchPage{rows: 1, total: 5, hasNext: true, endCursor: firstWindowCursor},
					searchPage{rows: 1, total: 5, endCursor: secondWindowCursor},
				)
				got, _, err := listOn(t, e, srv, rec, change.minted...)
				if err != nil {
					t.Fatalf("Setup: %s on %s = error %v, want a first page", method, spec.GitHub, err)
				}
				_, next, ok := pageParts(got)
				if !ok || next == "" {
					t.Fatalf("Setup: %s on %s = %v, want a continuation", method, spec.GitHub, got)
				}

				_, window, err := listOn(t, e, srv, rec, append(slices.Clone(change.resumed), forgeapi.WithAfter(next))...)
				checkCursorRefused(t, method+" on "+string(spec.GitHub)+" "+name, err, window)
			})
		}
	}
}

// On the Gitea family a page's limit is the smaller of the bound and the
// connection's stated maximum, so the page number counts pages of that limit, and a
// connection stating another maximum, larger or smaller, which would send another
// limit for the same bound, refuses the continuation before it asks for a page. One
// stating the same maximum resumes it. Every client here is one connection, a fresh
// client over one instance whose administrator restates its maximum, since a
// continuation handed to another connection is refused whatever that one states.
func TestAGiteaFamilyContinuationOnAConnectionStatingAnotherMaximumIsRefused(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			e := requireEntry(t, p, "Releases.ListReleases")
			s := canonicalSubject(p)
			began := newPagedInstance(t, p, 12, 5)
			srv := began.serve(t)
			got, _, err := callList(t, e, pagedClient(t, p, srv), began.rec, s)
			if err != nil {
				t.Fatalf("Setup: ListReleases on %s stating 5 = error %v, want a first page", p, err)
			}
			_, next, ok := pageParts(got)
			if !ok || next == "" {
				t.Fatalf("Setup: ListReleases on %s over 12 releases stating 5 = %v, want a continuation", p, got)
			}

			began.restate(50)
			_, window, err := callList(t, e, pagedClient(t, p, srv), began.rec, s, forgeapi.WithAfter(next))
			checkCursorRefused(t, "ListReleases on "+string(p)+" stating 50, resuming a walk begun at 5", err, began.listRequests(window))

			began.restate(5)
			_, window, err = callList(t, e, pagedClient(t, p, srv), began.rec, s, forgeapi.WithAfter(next))
			pages := began.listRequests(window)
			if err != nil || len(pages) != 1 || pages[0].query.Get("page") != "2" || pageLimit(pages[0]) != "5" {
				t.Errorf("ListReleases on %s stating 5, resuming a walk begun at 5 = error %v, sent [%s], want the second page at limit 5", p, err, describeRequests(pages))
			}

			wide := newPagedInstance(t, p, 120, 50)
			wideSrv := wide.serve(t)
			got, _, err = callList(t, e, pagedClient(t, p, wideSrv), wide.rec, s)
			if err != nil {
				t.Fatalf("Setup: ListReleases on %s stating 50 = error %v, want a first page", p, err)
			}
			_, wideNext, ok := pageParts(got)
			if !ok || wideNext == "" {
				t.Fatalf("Setup: ListReleases on %s over 120 releases stating 50 = %v, want a continuation", p, got)
			}
			wide.restate(5)
			_, window, err = callList(t, e, pagedClient(t, p, wideSrv), wide.rec, s, forgeapi.WithAfter(wideNext))
			checkCursorRefused(t, "ListReleases on "+string(p)+" stating 5, resuming a walk begun at 50", err, wide.listRequests(window))
		})
	}
}

// The run listing is page-numbered too, so its walk is held to the limit its first
// page was sent at the same way, larger or smaller, although its continuation is
// decided by the stated total: a page of another size starts at another run whatever
// the total says.
func TestAGiteaFamilyRunWalkOnAConnectionStatingAnotherMaximumIsRefused(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			e := requireEntry(t, p, "Checks.ListRuns")
			s := canonicalSubject(p)
			began := newPagedInstance(t, p, 12, 5)
			srv := began.serve(t)
			got, _, err := callList(t, e, pagedClient(t, p, srv), began.rec, s)
			if err != nil {
				t.Fatalf("Setup: ListRuns on %s stating 5 = error %v, want a first page", p, err)
			}
			_, next, ok := pageParts(got)
			if !ok || next == "" {
				t.Fatalf("Setup: ListRuns on %s over 12 runs stating 5 = %v, want a continuation", p, got)
			}

			began.restate(50)
			_, window, err := callList(t, e, pagedClient(t, p, srv), began.rec, s, forgeapi.WithAfter(next))
			checkCursorRefused(t, "ListRuns on "+string(p)+" stating 50, resuming a walk begun at 5", err, began.listRequests(window))

			began.restate(5)
			_, window, err = callList(t, e, pagedClient(t, p, srv), began.rec, s, forgeapi.WithAfter(next))
			pages := began.listRequests(window)
			if err != nil || len(pages) != 1 || pages[0].query.Get("page") != "2" || pageLimit(pages[0]) != "5" {
				t.Errorf("ListRuns on %s stating 5, resuming a walk begun at 5 = error %v, sent [%s], want the second page at limit 5", p, err, describeRequests(pages))
			}

			wide := newPagedInstance(t, p, 120, 50)
			wideSrv := wide.serve(t)
			got, _, err = callList(t, e, pagedClient(t, p, wideSrv), wide.rec, s)
			if err != nil {
				t.Fatalf("Setup: ListRuns on %s stating 50 = error %v, want a first page", p, err)
			}
			_, wideNext, ok := pageParts(got)
			if !ok || wideNext == "" {
				t.Fatalf("Setup: ListRuns on %s over 120 runs stating 50 = %v, want a continuation", p, got)
			}
			wide.restate(5)
			_, window, err = callList(t, e, pagedClient(t, p, wideSrv), wide.rec, s, forgeapi.WithAfter(wideNext))
			checkCursorRefused(t, "ListRuns on "+string(p)+" stating 5, resuming a walk begun at 50", err, wide.listRequests(window))
		})
	}
}
