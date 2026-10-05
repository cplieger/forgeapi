package conformance

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// ownerArm is the owner scope's request for the canonical owner.
func ownerArm(p spec.Product, method string) arm {
	return ownerArmFor(p, method, owner)
}

// ownerArmFor is the owner scope's request, every open item under one owner whoever
// authored it, as the table spells it per product. GitLab addresses a group by its
// encoded full path, so a nested group's separator travels as %2F.
func ownerArmFor(p spec.Product, method, name string) arm {
	issues := method == "Issues.ListMyIssues"
	switch p {
	case spec.GitHub:
		a := arm{terms: []string{"user:" + name}, without: []string{"author:@me"}}
		if issues {
			a.terms = append(a.terms, "type:issue")
		}
		return a
	case spec.GitLab:
		route, query := "merge_requests", map[string]string{"state": "opened"}
		if issues {
			route = "issues"
			query["with_labels_details"] = "true"
		}
		return arm{
			path:   "/api/v4/groups/" + strings.ReplaceAll(name, "/", "%2F") + "/" + route,
			query:  query,
			differ: map[string]string{"scope": "created_by_me"},
			absent: []string{"author_id", "author_username"},
		}
	default:
		return arm{
			path:   "/api/v1/repos/issues/search",
			query:  map[string]string{"type": giteaSearchType(issues), "owner": name},
			absent: []string{"created"},
		}
	}
}

// requireOwnerArm fails a case whose entry names no owner scope. Both
// cross-repository lists carry one on every product, so an entry without it has
// lost a ruled arm, and skipping there would let the table opt out of the ruling
// the case holds.
func requireOwnerArm(t *testing.T, e spec.Entry) {
	t.Helper()
	if !strings.Contains(e.Exercises, "owner scope") {
		t.Fatalf("the table names no owner scope for %s on %s, want one on every product: %q", e.Method, e.Product, e.Exercises)
	}
}

// TestTheOwnerScopeListsEveryOpenItemUnderTheOwner holds the owner scope of both
// cross-repository lists to the request the table names and to the answer: one
// request selecting the owner's open items in place of the credential's own, and the
// same normalized row the default scope answers.
func TestTheOwnerScopeListsEveryOpenItemUnderTheOwner(t *testing.T) {
	for _, method := range crossRepositoryLists {
		for _, p := range spec.Products {
			t.Run(string(p)+"_"+method, func(t *testing.T) {
				e := requireEntry(t, p, method)
				if pending(e) {
					t.Skip(pendingReason(e))
				}
				requireOwnerArm(t, e)
				op, ok := operationFor(method)
				if !ok {
					t.Fatalf("Setup: the contract declares no case for %s", method)
				}
				got, requests, err := listFixture(t, e, forgeapi.WithOwner(owner))
				if err != nil {
					t.Fatalf("%s on %s under the owner %q = error %v, want the owner's open items", method, p, owner, err)
				}
				checkArm(t, e, ownerArm(p, method), requests)
				if n := op.items(got); n != 1 {
					t.Errorf("%s on %s under the owner %q returned %d item(s), want 1: the fixture carries one", method, p, owner, n)
				}
				failures, _, _ := fieldFailures(e, op, got)
				for _, f := range failures {
					t.Error(f)
				}
			})
		}
	}
}

// unresolvableOwner is an owner name in the shared form that no instance the
// answers below were measured on holds a namespace for.
const unresolvableOwner = "no-such-owner"

// unresolvedOwnerAnswers is what each product's instance answered a cross-repository
// list scoped to an owner it holds no namespace for, cut from the measured captures
// and canonicalized: GitHub's search answers an empty page with no error, GitLab's
// group route answers 404 for an absent group and for a user's own namespace alike,
// and the Gitea family's search answers 400 naming the user it could not find.
var unresolvedOwnerAnswers = map[spec.Product]struct {
	body   string
	status int
}{
	spec.GitHub: {
		status: http.StatusOK,
		body:   `{"data":{"rateLimit":{"cost":1,"limit":5000,"remaining":4990,"resetAt":"2026-01-02T00:00:00Z"},"search":{"issueCount":0,"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[]}}}`,
	},
	spec.GitLab: {
		status: http.StatusNotFound,
		body:   `{"message":"404 Group Not Found"}`,
	},
	spec.Gitea: {
		status: http.StatusBadRequest,
		body:   `{"message":"user does not exist [uid: 0, name: no-such-owner]","url":"https://forge.example/api/swagger"}`,
	},
	spec.Forgejo: {
		status: http.StatusBadRequest,
		body:   `{"message":"user does not exist [uid: 0, name: no-such-owner]","url":"https://forge.example/api/swagger"}`,
	},
}

// newAnswerServer answers every request with one status and one body and records
// what arrived, for a case whose answer is a refusal no 2xx fixture carries. The
// connection read the case runs first is answered from that read's own fixture, so
// the one answer is what the case's own operation meets.
func newAnswerServer(t *testing.T, e spec.Entry, status int, body string) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	setup := setupRoutes(t, e)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxFixtureBody))
		if err != nil {
			raw = nil
		}
		rec.record(sent{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.Query(), body: raw})
		w.Header().Set("Content-Type", "application/json")
		for i := range setup {
			if setup[i].matches(r, raw) {
				w.WriteHeader(setup[i].Status)
				if _, err := w.Write(setup[i].Body); err != nil {
					t.Errorf("Setup: writing the connection read's answer to %s %s: %v", r.Method, r.URL.EscapedPath(), err)
				}
				return
			}
		}
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("Setup: writing the answer to %s %s: %v", r.Method, r.URL.EscapedPath(), err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// TestAnUnresolvedOwnerAnswersAsTheTableStates holds the owner scope's answer for an
// owner the instance does not resolve: owner_unresolved, a not-found refusal of the
// operation that asked, where the product's answer lets the family tell; and the
// empty page with no error the table names where the product answers no such owner
// exactly as it answers nothing open. Either way the call is the one request its
// price states, with no resolving read.
func TestAnUnresolvedOwnerAnswersAsTheTableStates(t *testing.T) {
	for _, method := range crossRepositoryLists {
		for _, p := range spec.Products {
			t.Run(string(p)+"_"+method, func(t *testing.T) {
				e := requireEntry(t, p, method)
				if pending(e) {
					t.Skip(pendingReason(e))
				}
				requireOwnerArm(t, e)
				answer := unresolvedOwnerAnswers[p]
				srv, rec := newAnswerServer(t, e, answer.status, answer.body)
				got, requests, err := listOn(t, e, srv, rec, forgeapi.WithOwner(unresolvableOwner))
				if len(requests) != 1 {
					t.Errorf("%s on %s under an unresolved owner sent %d request(s) [%s], want 1: the owner is never resolved by a read of its own",
						method, p, len(requests), describeRequests(requests))
				}
				d, departs := departureFor(&e, ownerUnresolved)
				if departs && d.Kind == spec.CannotSupply && strings.HasPrefix(d.Says, "empty") {
					checkEmptyAnswer(t, e, method, got, err, d)
					return
				}
				checkOwnerUnresolved(t, e, method, err, answer.status)
			})
		}
	}
}

// checkEmptyAnswer holds a product that cannot tell no such owner from nothing open
// to the answer the table names for it: no error and no row.
func checkEmptyAnswer(t *testing.T, e spec.Entry, method string, got any, err error, d spec.Departure) {
	t.Helper()
	if err != nil {
		t.Errorf("%s on %s under an unresolved owner = error %v, want none: the table says %q", method, e.Product, err, d.Says)
		return
	}
	op, ok := operationFor(method)
	if !ok {
		t.Fatalf("Setup: the contract declares no case for %s", method)
	}
	if n := op.items(got); n != 0 {
		t.Errorf("%s on %s under an unresolved owner returned %d item(s), want 0", method, e.Product, n)
	}
}

// checkOwnerUnresolved holds the refusal of an owner the instance does not resolve.
func checkOwnerUnresolved(t *testing.T, e spec.Entry, method string, err error, status int) {
	t.Helper()
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Errorf("%s on %s under an unresolved owner = error %v, want a *forgeapi.Error with code %q", method, e.Product, err, forgeapi.CodeOwnerUnresolved)
		return
	}
	if fe.Code != forgeapi.CodeOwnerUnresolved {
		t.Errorf("%s on %s under an unresolved owner = code %q, want %q", method, e.Product, fe.Code, forgeapi.CodeOwnerUnresolved)
	}
	if fe.Kind != forgeapi.KindNotFound {
		t.Errorf("%s on %s under an unresolved owner = kind %v, want %v", method, e.Product, fe.Kind, forgeapi.KindNotFound)
	}
	if fe.Status != status {
		t.Errorf("%s on %s under an unresolved owner = status %d, want %d, the status the instance answered", method, e.Product, fe.Status, status)
	}
	if _, name, _ := strings.Cut(method, "."); !strings.Contains(fe.Op, name) {
		t.Errorf("%s on %s under an unresolved owner = Op %q, want the role method's own spelling", method, e.Product, fe.Op)
	}
	if want := family(e.Product); fe.Family != want {
		t.Errorf("%s on %s under an unresolved owner = family %v, want %v", method, e.Product, fe.Family, want)
	}
	if fe.DiagID == "" {
		t.Errorf("%s on %s under an unresolved owner = empty DiagID, want one: the operation failed", method, e.Product)
	}
}

// listsWithoutOwnerScope are the lists that answer one repository or the credential's
// own repositories, where an owner names nothing.
var listsWithoutOwnerScope = []string{
	"Repos.ListRepos",
	"PullRequests.ListPRs",
	"Issues.ListIssues",
	"Checks.ListRuns",
	"Releases.ListReleases",
	"Labels.ListLabels",
}

// TestAnOwnerOnAListWithNoOwnerScopeIsRefused holds the owner option to the two lists
// it scopes: on every other list it is a misuse, refused before any request rather
// than ignored.
func TestAnOwnerOnAListWithNoOwnerScopeIsRefused(t *testing.T) {
	for _, method := range listsWithoutOwnerScope {
		for _, p := range spec.Products {
			t.Run(string(p)+"_"+method, func(t *testing.T) {
				e := requireEntry(t, p, method)
				if pending(e) {
					t.Skip(pendingReason(e))
				}
				_, requests, err := listAgainst(t, e, fixture{Routes: setupRoutes(t, e)}, forgeapi.WithOwner(owner))
				checkLocalRefusal(t, e, forgeapi.CodeListOwnerInvalid, err, requests)
			})
		}
	}
}

// ownersOutsideTheForm are owners the shared form refuses on every product: empty, a
// byte a search grammar reads as syntax, an empty, dot or dot-dot segment, a
// separator at either end, and one byte over the bound.
var ownersOutsideTheForm = map[string]string{
	"empty":            "",
	"space":            "an owner",
	"colon":            "user:example",
	"quote":            `example"`,
	"plus":             "example+one",
	"non_ascii":        "exämple",
	"dot_segment":      ".",
	"dot_dot_segment":  "..",
	"empty_segment":    "example//group",
	"dot_dot_inside":   "example/../group",
	"leading_slash":    "/example",
	"trailing_slash":   "example/",
	"over_the_bound":   strings.Repeat("a", 256),
	"nested_over_cap":  strings.Repeat("a", 128) + "/" + strings.Repeat("b", 127),
	"control_byte":     "example\n",
	"percent_encoding": "example%2Fgroup",
}

// TestAnOwnerOutsideTheSharedFormIsRefused holds the owner's form, checked before any
// request because two products put the owner inside a search string whose grammar
// this library does not own.
func TestAnOwnerOutsideTheSharedFormIsRefused(t *testing.T) {
	for _, method := range crossRepositoryLists {
		for _, p := range spec.Products {
			for name, value := range ownersOutsideTheForm {
				t.Run(string(p)+"_"+method+"_"+name, func(t *testing.T) {
					e := requireEntry(t, p, method)
					if pending(e) {
						t.Skip(pendingReason(e))
					}
					requireOwnerArm(t, e)
					_, requests, err := listAgainst(t, e, fixture{Routes: setupRoutes(t, e)}, forgeapi.WithOwner(value))
					checkLocalRefusal(t, e, forgeapi.CodeListOwnerInvalid, err, requests)
				})
			}
		}
	}
}

// notFound is an answer no route of either cross-repository list retries, for a case
// that holds only where the request went.
const notFound = `{"message":"404 Not Found"}`

// TestAPathOwnerIsAGitLabGroupAndNothingElse holds the one product-dependent clause
// of the form: a slash-separated owner is a nested group's full path on GitLab, sent
// as that product's encoded group id, and refused before any request on the three
// products whose owner is a single name.
func TestAPathOwnerIsAGitLabGroupAndNothingElse(t *testing.T) {
	const nested = "example/group"
	for _, method := range crossRepositoryLists {
		for _, p := range spec.Products {
			t.Run(string(p)+"_"+method, func(t *testing.T) {
				e := requireEntry(t, p, method)
				if pending(e) {
					t.Skip(pendingReason(e))
				}
				requireOwnerArm(t, e)
				srv, rec := newAnswerServer(t, e, http.StatusNotFound, notFound)
				_, requests, err := listOn(t, e, srv, rec, forgeapi.WithOwner(nested))
				if p != spec.GitLab {
					checkLocalRefusal(t, e, forgeapi.CodeListOwnerInvalid, err, requests)
					return
				}
				checkArm(t, e, ownerArmFor(p, method, nested), requests)
				checkNotRefusedLocally(t, e, nested, err)
			})
		}
	}
}

// ownersInsideTheForm are owners the shared form admits on every product: every byte
// class it names, and an owner at the bound.
var ownersInsideTheForm = map[string]string{
	"every_byte_class": "Example-org_2.dev",
	"at_the_bound":     strings.Repeat("a", 255),
}

// TestAnOwnerInsideTheSharedFormReachesTheInstance holds the form's other edge: a
// name the form admits is the product's to refuse, so it reaches the instance as the
// one request the scope sends, carrying the owner as it was named.
func TestAnOwnerInsideTheSharedFormReachesTheInstance(t *testing.T) {
	for _, method := range crossRepositoryLists {
		for _, p := range spec.Products {
			for name, value := range ownersInsideTheForm {
				t.Run(string(p)+"_"+method+"_"+name, func(t *testing.T) {
					e := requireEntry(t, p, method)
					if pending(e) {
						t.Skip(pendingReason(e))
					}
					requireOwnerArm(t, e)
					srv, rec := newAnswerServer(t, e, http.StatusNotFound, notFound)
					_, requests, err := listOn(t, e, srv, rec, forgeapi.WithOwner(value))
					checkArm(t, e, ownerArmFor(p, method, value), requests)
					checkNotRefusedLocally(t, e, value, err)
				})
			}
		}
	}
}

// TestValidateOwnerAnswersWhatTheCrossRepositoryListsRefuse holds the exported check to
// the lists it speaks for: on every product, an owner the check refuses for the
// product's family sends no request and answers the check's code, and an owner it
// admits reaches the instance.
func TestValidateOwnerAnswersWhatTheCrossRepositoryListsRefuse(t *testing.T) {
	owners := map[string]string{"group_path": "example/group", "nested_group_path": "example/group/sub"}
	for name, value := range ownersOutsideTheForm {
		owners["outside_"+name] = value
	}
	for name, value := range ownersInsideTheForm {
		owners["inside_"+name] = value
	}
	for _, method := range crossRepositoryLists {
		for _, p := range spec.Products {
			for name, value := range owners {
				t.Run(string(p)+"_"+method+"_"+name, func(t *testing.T) {
					e := requireEntry(t, p, method)
					if pending(e) {
						t.Skip(pendingReason(e))
					}
					requireOwnerArm(t, e)
					srv, rec := newAnswerServer(t, e, http.StatusNotFound, notFound)
					_, requests, err := listOn(t, e, srv, rec, forgeapi.WithOwner(value))
					verdict := forgeapi.ValidateOwner(family(p), value)
					if verdict != nil {
						var fe *forgeapi.Error
						if !asForgeError(verdict, &fe) {
							t.Fatalf("ValidateOwner(%v, %d byte(s)) = %v, want a *forgeapi.Error", family(p), len(value), verdict)
						}
						checkLocalRefusal(t, e, fe.Code, err, requests)
						return
					}
					if len(requests) == 0 {
						t.Errorf("%s on %s under an owner ValidateOwner admits (%d byte(s)) sent no request, want the scope's request", e.Method, e.Product, len(value))
					}
					checkNotRefusedLocally(t, e, value, err)
				})
			}
		}
	}
}

// checkNotRefusedLocally holds an admitted owner to having reached the instance: the
// instance's answer here is a failure, and that failure must be the instance's rather
// than the form's.
func checkNotRefusedLocally(t *testing.T, e spec.Entry, value string, err error) {
	t.Helper()
	var fe *forgeapi.Error
	if asForgeError(err, &fe) && fe.Code == forgeapi.CodeListOwnerInvalid {
		t.Errorf("%s on %s under an owner of %d byte(s) = code %q, want the form to admit it", e.Method, e.Product, len(value), fe.Code)
	}
}
