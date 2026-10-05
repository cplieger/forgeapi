package conformance

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// documentProducts are the products whose repository pull-request list reads a
// document, whose continuation is an upstream connection position rather than a
// page number.
var documentProducts = []spec.Product{spec.GitHub, spec.GitLab}

// documentEndCursor is the end cursor every page the document instance below
// answers names.
const documentEndCursor = "ZG9jdW1lbnQ6Mw=="

// documentInstance answers one product's repository pull-request document with the
// fixture's rows, a page after them and the end cursor above, and the document and
// routes the call's own prelude sends from that prelude's fixture.
type documentInstance struct {
	srv  *httptest.Server
	rec  *recorder
	name string
}

// listConnection is the connection object the repository list document answers its
// rows in, on each document product.
var listConnection = map[spec.Product][2]string{
	spec.GitHub: {"repository", "pullRequests"},
	spec.GitLab: {"project", "mergeRequests"},
}

// newDocumentInstance stands up one document product's repository pull-request
// list.
func newDocumentInstance(t *testing.T, p spec.Product) *documentInstance {
	t.Helper()
	e := requireEntry(t, p, "PullRequests.ListPRs")
	name, ok := tableDocument(&e)
	if !ok {
		t.Fatalf("Setup: the table names no document for %s on %s", e.Method, p)
	}
	list, err := loadFixture(p, e.Method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var answer map[string]any
	if err := json.Unmarshal(list.Routes[0].Body, &answer); err != nil {
		t.Fatalf("Setup: %s's body is not a JSON object: %v", list.path, err)
	}
	data, _ := answer["data"].(map[string]any)
	owner, _ := data[listConnection[p][0]].(map[string]any)
	rows, _ := owner[listConnection[p][1]].(map[string]any)
	if rows == nil {
		t.Fatalf("Setup: %s answers no %s connection", list.path, listConnection[p][1])
	}
	rows["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": documentEndCursor}
	page, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("Setup: re-encoding the document's answer: %v", err)
	}
	prelude, routes := preludeAnswers(t, e)
	di := &documentInstance{rec: &recorder{}, name: name}
	di.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxFixtureBody))
		if err != nil {
			raw = nil
		}
		path := r.URL.EscapedPath()
		di.rec.record(sent{method: r.Method, path: path, query: r.URL.Query(), body: raw})
		w.Header().Set("Content-Type", "application/json")
		body, ok := routes[r.Method+" "+path]
		if r.Method == http.MethodPost {
			body, ok = prelude, prelude != nil
			if operationName(raw) == name {
				body, ok = page, true
			}
		}
		if !ok {
			di.rec.miss(r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if _, err := w.Write(body); err != nil {
			t.Errorf("Setup: writing %s %s: %v", r.Method, path, err)
		}
	}))
	t.Cleanup(di.srv.Close)
	return di
}

// preludeAnswers are the answers one case's prelude reads, from that prelude's own
// fixture: the one document it sends, and each route by its method and path.
func preludeAnswers(t *testing.T, e spec.Entry) (document []byte, routes map[string][]byte) {
	t.Helper()
	routes = map[string][]byte{}
	op, ok := operationFor(e.Method)
	if !ok {
		t.Fatalf("Setup: the contract declares no case for %s", e.Method)
	}
	prelude := preludeMethod(e, op)
	if prelude == "" {
		return nil, routes
	}
	f, err := loadFixture(e.Product, prelude)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	for _, w := range f.Routes {
		if w.GraphQL {
			if document != nil {
				t.Fatalf("Setup: %s serves more than one document, want the one the prelude sends", f.path)
			}
			document = w.Body
			continue
		}
		for _, path := range append([]string{w.Path}, w.Paths...) {
			routes[w.Method+" "+path] = w.Body
		}
	}
	return document, routes
}

// operationName is the document one request names.
func operationName(raw []byte) string {
	var probe struct {
		OperationName string `json:"operationName"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return probe.OperationName
}

// client is one connection to the instance, its prelude already run, so every call
// on it sends the list's own requests alone.
func (di *documentInstance) client(t *testing.T, p spec.Product) any {
	t.Helper()
	client := pagedClient(t, p, di.srv)
	e := requireEntry(t, p, "PullRequests.ListPRs")
	op, _ := operationFor(e.Method)
	if prelude := preludeMethod(e, op); prelude != "" {
		runPrelude(t, e, prelude, client, canonicalSubject(p))
	}
	return client
}

// listRequests are the requests of one window that carried the list's document.
func (di *documentInstance) listRequests(window []sent) []sent {
	var out []sent
	for _, req := range window {
		if req.method == http.MethodPost && operationName(req.body) == di.name {
			out = append(out, req)
		}
	}
	return out
}

// mintDocument reads one document product's first page of pull requests with the
// options given and answers its continuation.
func mintDocument(t *testing.T, p spec.Product, client any, di *documentInstance, s subject, opts ...forgeapi.ListOption) forgeapi.Cursor {
	t.Helper()
	e := requireEntry(t, p, "PullRequests.ListPRs")
	got, _, err := callList(t, e, client, di.rec, s, opts...)
	if err != nil {
		t.Fatalf("Setup: ListPRs on %s = error %v, want a first page", p, err)
	}
	_, next, ok := pageParts(got)
	if !ok || next == "" {
		t.Fatalf("Setup: ListPRs on %s over a document answering hasNextPage true = %v, want a continuation", p, got)
	}
	return next
}

// carriesEndCursor reports whether one document request carried the end cursor the
// page before it named.
func carriesEndCursor(req sent) bool {
	return bytes.Contains(req.body, []byte(documentEndCursor))
}

// An upstream connection position resumes at the row after the last one served
// whatever page size the next call asks for, so a document continuation carries no
// bound, and the call that minted it resumes it at another bound as at its own.
func TestADocumentContinuationResumesItsCallAtAnyPageBound(t *testing.T) {
	bounds := map[string][]forgeapi.ListOption{
		"its_own_bound":      nil,
		"a_smaller_bound":    {forgeapi.WithPageBound(7)},
		"the_default_bound":  {forgeapi.WithPageBound(forgeapi.DefaultPageBound)},
		"the_smallest_bound": {forgeapi.WithPageBound(1)},
	}
	for _, p := range documentProducts {
		for name, bound := range bounds {
			t.Run(string(p)+"_"+name, func(t *testing.T) {
				di := newDocumentInstance(t, p)
				client := di.client(t, p)
				s := canonicalSubject(p)
				next := mintDocument(t, p, client, di, s, forgeapi.WithPageBound(mintBound))

				e := requireEntry(t, p, "PullRequests.ListPRs")
				_, window, err := callList(t, e, client, di.rec, s, append(slices.Clone(bound), forgeapi.WithAfter(next))...)
				pages := di.listRequests(window)
				if err != nil || len(pages) != 1 || !carriesEndCursor(pages[0]) {
					t.Errorf("ListPRs on %s resumed at %s = error %v, sent [%s], want one document request carrying the end cursor %q", p, name, err, describeRequests(window), documentEndCursor)
				}
			})
		}
	}
}

// A document continuation names the repository whose pull requests it walks, so the
// same list on another repository refuses it rather than resuming at whatever row
// the position names in that repository's connection.
func TestADocumentContinuationOnAnotherRepositoryIsRefused(t *testing.T) {
	for _, p := range documentProducts {
		t.Run(string(p), func(t *testing.T) {
			di := newDocumentInstance(t, p)
			client := di.client(t, p)
			s := canonicalSubject(p)
			next := mintDocument(t, p, client, di, s)

			elsewhere := s
			elsewhere.repo = otherRepo(p)
			_, window, err := callList(t, requireEntry(t, p, "PullRequests.ListPRs"), client, di.rec, elsewhere, forgeapi.WithAfter(next))
			checkCursorRefused(t, "ListPRs on "+string(p)+" for "+elsewhere.repo.Selector, err, window)
		})
	}
}

// A document continuation names the state filter its rows were selected under, so
// the same list under another state refuses it.
func TestADocumentContinuationUnderAnotherStateIsRefused(t *testing.T) {
	others := map[string]forgeapi.ListState{"closed": forgeapi.ListStateClosed, "all": forgeapi.ListStateAll}
	for _, p := range documentProducts {
		for name, state := range others {
			t.Run(string(p)+"_"+name, func(t *testing.T) {
				di := newDocumentInstance(t, p)
				client := di.client(t, p)
				s := canonicalSubject(p)
				next := mintDocument(t, p, client, di, s, forgeapi.WithState(forgeapi.ListStateOpen))

				_, window, err := callList(t, requireEntry(t, p, "PullRequests.ListPRs"), client, di.rec, s, forgeapi.WithState(state), forgeapi.WithAfter(next))
				checkCursorRefused(t, "ListPRs on "+string(p)+" minted open, resumed "+name, err, window)
			})
		}
	}
}

// A document continuation names the connection it was minted on, so the same list
// on another instance of the product refuses it.
func TestADocumentContinuationOnAnotherInstanceOfTheProductIsRefused(t *testing.T) {
	for _, p := range documentProducts {
		t.Run(string(p), func(t *testing.T) {
			began := newDocumentInstance(t, p)
			s := canonicalSubject(p)
			next := mintDocument(t, p, began.client(t, p), began, s)

			elsewhere := newDocumentInstance(t, p)
			_, window, err := callList(t, requireEntry(t, p, "PullRequests.ListPRs"), elsewhere.client(t, p), elsewhere.rec, s, forgeapi.WithAfter(next))
			checkCursorRefused(t, "ListPRs on another "+string(p)+" instance", err, window)
		})
	}
}

// resumeElsewhere drives one product's list on an instance of its own, handed a
// continuation another product minted, and answers what it returned and the
// requests its list sent: on a page-numbered list, those that reached a list route,
// since a connection that holds no stated maximum yet reads it first.
func resumeElsewhere(t *testing.T, p spec.Product, method string, next forgeapi.Cursor) (any, []sent, error) {
	t.Helper()
	e := requireEntry(t, p, method)
	s := canonicalSubject(p)
	opts := []forgeapi.ListOption{forgeapi.WithAfter(next)}
	if slices.Contains(documentProducts, p) && method == "PullRequests.ListPRs" {
		di := newDocumentInstance(t, p)
		got, window, err := callList(t, e, di.client(t, p), di.rec, s, opts...)
		return got, window, err
	}
	if p == spec.GitHub && slices.Contains(crossRepositoryLists, method) {
		srv, rec := newSearchServer(t, method, searchPage{rows: 1, total: 1})
		return listOn(t, e, srv, rec, opts...)
	}
	pi := newPagedInstance(t, p, mintedRows, 50)
	got, window, err := callList(t, e, pagedClient(t, p, pi.serve(t)), pi.rec, s, append(opts, forgeapi.WithPageBound(mintBound))...)
	return got, pi.listRequests(window), err
}

// A document continuation names the family whose client minted it, so the same list
// on a client of another family refuses it, a document of that family's own included.
func TestADocumentContinuationHandedToAnotherFamilyIsRefused(t *testing.T) {
	for _, minter := range documentProducts {
		for _, resumer := range spec.Products {
			if family(resumer) == family(minter) {
				continue
			}
			t.Run(string(minter)+"_to_"+string(resumer), func(t *testing.T) {
				di := newDocumentInstance(t, minter)
				next := mintDocument(t, minter, di.client(t, minter), di, canonicalSubject(minter))

				_, window, err := resumeElsewhere(t, resumer, "PullRequests.ListPRs", next)
				checkCursorRefused(t, "ListPRs on "+string(resumer)+" handed the document continuation "+string(minter)+" minted", err, window)
			})
		}
	}
}

// Another family's page-numbered continuation names that family, so a document
// product's list refuses it rather than reading it as a position in its own
// connection.
func TestAnotherFamilysContinuationHandedToADocumentListIsRefused(t *testing.T) {
	for _, resumer := range documentProducts {
		for _, minter := range spec.Products {
			if family(minter) == family(resumer) {
				continue
			}
			e, ok := entryFor(minter, "PullRequests.ListPRs")
			if !ok || !slices.ContainsFunc(pageNumberedLists(t, minter), func(n spec.Entry) bool { return n.Method == e.Method }) {
				continue
			}
			t.Run(string(minter)+"_to_"+string(resumer), func(t *testing.T) {
				began := newPagedInstance(t, minter, mintedRows, 50)
				next := mintContinuation(t, e, pagedClient(t, minter, began.serve(t)), began, canonicalSubject(minter))

				_, window, err := resumeElsewhere(t, resumer, "PullRequests.ListPRs", next)
				checkCursorRefused(t, "ListPRs on "+string(resumer)+" handed the continuation "+string(minter)+" minted", err, window)
			})
		}
	}
}

// mintSearch reads the first page of one of GitHub's searches over a search
// instance answering a page after it, with the options given, and answers its
// continuation.
func mintSearch(t *testing.T, method string, srv *httptest.Server, rec *recorder, opts ...forgeapi.ListOption) forgeapi.Cursor {
	t.Helper()
	got, _, err := listOn(t, requireEntry(t, spec.GitHub, method), srv, rec, opts...)
	if err != nil {
		t.Fatalf("Setup: %s on %s = error %v, want a first page", method, spec.GitHub, err)
	}
	_, next, ok := pageParts(got)
	if !ok || next == "" {
		t.Fatalf("Setup: %s on %s = %v, want a continuation", method, spec.GitHub, got)
	}
	return next
}

// twoSearchPages is a search walk of two pages, the first naming the end cursor the
// second is asked for by.
func twoSearchPages() []searchPage {
	return []searchPage{
		{rows: 1, total: 2, hasNext: true, endCursor: firstWindowCursor},
		{rows: 1, total: 2, endCursor: secondWindowCursor},
	}
}

// A search continuation carries no bound either, so the call that minted it resumes
// it at any page size.
func TestASearchContinuationResumesItsCallAtAnyPageBound(t *testing.T) {
	bounds := map[string][]forgeapi.ListOption{
		"its_own_bound":   nil,
		"a_smaller_bound": {forgeapi.WithPageBound(7)},
		"a_larger_bound":  {forgeapi.WithPageBound(forgeapi.DefaultPageBound)},
	}
	for _, method := range crossRepositoryLists {
		for name, bound := range bounds {
			t.Run(method+"_"+name, func(t *testing.T) {
				srv, rec := newSearchServer(t, method, twoSearchPages()...)
				next := mintSearch(t, method, srv, rec)

				got, window, err := listOn(t, requireEntry(t, spec.GitHub, method), srv, rec, append(slices.Clone(bound), forgeapi.WithAfter(next))...)
				rows, _, _ := pageParts(got)
				if err != nil || len(window) != 1 || !bytes.Contains(window[0].body, []byte(firstWindowCursor)) || rows != 1 {
					t.Errorf("%s on %s resumed at %s = (%d row(s), error %v), sent [%s], want the second page asked for by the end cursor %q", method, spec.GitHub, name, rows, err, describeRequests(window), firstWindowCursor)
				}
			})
		}
	}
}

// GitHub's searches read one connection, so one search's continuation names its own
// call and the other search refuses it before any request.
func TestASearchContinuationHandedToTheOtherSearchIsRefused(t *testing.T) {
	for _, minted := range crossRepositoryLists {
		for _, other := range crossRepositoryLists {
			if other == minted {
				continue
			}
			t.Run(minted+"_to_"+other, func(t *testing.T) {
				srv, rec := newSearchServer(t, minted, twoSearchPages()...)
				next := mintSearch(t, minted, srv, rec)

				elsewhere, elsewhereRec := newSearchServer(t, other, twoSearchPages()...)
				_, window, err := listOn(t, requireEntry(t, spec.GitHub, other), elsewhere, elsewhereRec, forgeapi.WithAfter(next))
				checkCursorRefused(t, other+" on "+string(spec.GitHub)+" handed "+minted+"'s continuation", err, window)
			})
		}
	}
}

// A search continuation names the connection it was minted on, so the same search
// on another instance of the product refuses it.
func TestASearchContinuationOnAnotherInstanceOfTheProductIsRefused(t *testing.T) {
	for _, method := range crossRepositoryLists {
		for scope, opts := range searchScopes {
			t.Run(method+"_"+scope, func(t *testing.T) {
				srv, rec := newSearchServer(t, method, twoSearchPages()...)
				next := mintSearch(t, method, srv, rec, opts...)

				elsewhere, elsewhereRec := newSearchServer(t, method, twoSearchPages()...)
				_, window, err := listOn(t, requireEntry(t, spec.GitHub, method), elsewhere, elsewhereRec, append(slices.Clone(opts), forgeapi.WithAfter(next))...)
				checkCursorRefused(t, method+" on another "+string(spec.GitHub)+" instance", err, window)
			})
		}
	}
}

// A search continuation names the family whose client minted it, so the same
// cross-repository list on a client of another family refuses it, and a search
// refuses another family's page-numbered continuation of that list.
func TestASearchContinuationHandedAcrossFamiliesIsRefused(t *testing.T) {
	for _, method := range crossRepositoryLists {
		for _, other := range spec.Products {
			if family(other) == family(spec.GitHub) {
				continue
			}
			if _, ok := entryFor(other, method); !ok || !slices.ContainsFunc(pageNumberedLists(t, other), func(n spec.Entry) bool { return n.Method == method }) {
				continue
			}
			t.Run(method+"_from_"+string(spec.GitHub)+"_to_"+string(other), func(t *testing.T) {
				srv, rec := newSearchServer(t, method, twoSearchPages()...)
				next := mintSearch(t, method, srv, rec)

				_, window, err := resumeElsewhere(t, other, method, next)
				checkCursorRefused(t, method+" on "+string(other)+" handed the search continuation "+string(spec.GitHub)+" minted", err, window)
			})
			t.Run(method+"_from_"+string(other)+"_to_"+string(spec.GitHub), func(t *testing.T) {
				e := requireEntry(t, other, method)
				began := newPagedInstance(t, other, mintedRows, 50)
				next := mintContinuation(t, e, pagedClient(t, other, began.serve(t)), began, canonicalSubject(other))

				_, window, err := resumeElsewhere(t, spec.GitHub, method, next)
				checkCursorRefused(t, method+" on "+string(spec.GitHub)+" handed the continuation "+string(other)+" minted", err, window)
			})
		}
	}
}
