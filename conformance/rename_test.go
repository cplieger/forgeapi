package conformance

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// movedSelector is the selector the canonical subject's repository moved to.
const movedSelector = owner + "/" + repository + "-moved"

// giteaRepos is the API root's repository prefix on the Gitea family, under which
// both products address a repository by its path.
const giteaRepos = "/api/v1/repos/"

// githubRepositoryID is the id GitHub's redirect addresses the moved repository by.
const githubRepositoryID = "/repositories/42"

// successorCarriers are the Gitea family's repository-addressed reads whose answer
// declares a successor.
var successorCarriers = []string{
	"PullRequests.ListPRs",
	"Merges.MergeStatus",
	"Checks.CommitStatus",
	"Checks.ListRuns",
	"Issues.ListIssues",
	"Releases.ListReleases",
	"Labels.ListLabels",
}

// carrierlessReads are the Gitea family's repository-addressed reads whose answer
// declares no successor, so a rename reaches the caller through the error.
var carrierlessReads = []string{"PullRequests.ReadPR", "Capabilities.RepoAffordances"}

// movedInstance serves one case's fixture where the canonical subject's repository
// moved: every request under from, the repository's old address, answers a 301
// whose Location is the same request under to, path and query kept, and a request
// under to is answered by the fixture's route for the same request under from.
// resolved is what GitHub's id-addressed record answers at to itself, nil on the
// Gitea family, whose location needs no further read.
func movedInstance(t *testing.T, f fixture, from, to string, resolved []byte) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{spent: make([]int, len(f.Routes))}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxFixtureBody))
		if err != nil {
			body = nil
		}
		path := r.URL.EscapedPath()
		rec.record(sent{method: r.Method, path: path, query: r.URL.Query(), body: body})
		w.Header().Set("Content-Type", "application/json")
		if rest, ok := underRepository(path, from); ok {
			location := to + rest
			if r.URL.RawQuery != "" {
				location += "?" + r.URL.RawQuery
			}
			w.Header().Set("Location", location)
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
		if resolved != nil && path == to {
			if _, err := w.Write(resolved); err != nil {
				t.Errorf("Setup: writing the moved record: %v", err)
			}
			return
		}
		asked := r
		if rest, ok := underRepository(path, to); ok {
			asked = r.Clone(r.Context())
			asked.URL.Path, asked.URL.RawPath = from+rest, ""
		}
		route, ok := rec.take(f, asked, body)
		if !ok {
			rec.miss(r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		for k, v := range route.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(route.Status)
		if _, err := w.Write(route.Body); err != nil {
			t.Errorf("Setup: writing %s %s: %v", r.Method, path, err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// underRepository reports whether path addresses the repository at prefix, the
// repository itself or anything beneath it, and answers the part after the prefix.
func underRepository(path, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
		return "", false
	}
	return rest, true
}

// renamedCall is what one case's operation answered on an instance whose
// repository moved, and the requests it sent after its prelude.
type renamedCall struct {
	got    any
	err    error
	sent   []sent
	misses []string
}

// callOn runs one case's prelude and operation on a client of srv, and answers what
// the operation alone answered and sent.
func callOn(t *testing.T, e spec.Entry, srv *httptest.Server, rec *recorder) renamedCall {
	t.Helper()
	op, ok := operationFor(e.Method)
	if !ok {
		t.Fatalf("Setup: the contract declares no case for %s", e.Method)
	}
	client := pagedClient(t, e.Product, srv, forgeapi.WithMutations(true))
	s := canonicalSubject(e.Product)
	if prelude := preludeMethod(e, op); prelude != "" {
		runPrelude(t, e, prelude, client, s)
	}
	before := rec.count()
	got, err := guard(func() (any, error) { return op.invoke(t.Context(), client, s) })
	return renamedCall{got: got, err: err, sent: rec.since(before), misses: rec.misses()}
}

// baseCost is what one case's operation sends on its own fixture, where nothing
// moved.
func baseCost(t *testing.T, e spec.Entry) int {
	t.Helper()
	f, err := loadFixture(e.Product, e.Method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	srv, rec := newFixtureServer(t, f)
	call := callOn(t, e, srv, rec)
	if call.err != nil || len(call.misses) > 0 {
		t.Fatalf("Setup: %s on %s where nothing moved = error %v, unanswered %v, want the fixture's answer", e.Method, e.Product, call.err, call.misses)
	}
	return len(call.sent)
}

// successorOf is the successor a success answer carries, read from its own field.
func successorOf(got any) *forgeapi.RepoRef {
	ref, _ := fieldValue(reflect.ValueOf(got), "Successor").(*forgeapi.RepoRef)
	return ref
}

// checkSuccessor holds one carrier to the moved repository: a reference of the
// family a consumer can store, whose id decodes to the selector the location names.
func checkSuccessor(t *testing.T, call string, p spec.Product, got *forgeapi.RepoRef) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = no successor, want %q: the redirect's location names it", call, movedSelector)
		return
	}
	if got.Selector != movedSelector || got.Family != family(p) {
		t.Errorf("%s = successor %+v, want selector %q on %v", call, *got, movedSelector, family(p))
	}
	decoded, err := forgeapi.DecodeRepoRef(got.ID, family(p))
	if err != nil || decoded.Selector != movedSelector {
		t.Errorf("%s = successor id %q decoding to (%+v, %v), want a reference to %q a consumer can store", call, got.ID, decoded, err, movedSelector)
	}
}

// checkPricedArm holds what a call sent on a moved repository to the request count
// the case names and to the price the table publishes for the entry.
func checkPricedArm(t *testing.T, e spec.Entry, call renamedCall, want int) {
	t.Helper()
	if len(call.misses) > 0 {
		t.Errorf("%s on %s reached route(s) the moved instance does not answer: %v", e.Method, e.Product, call.misses)
	}
	if len(call.sent) != want {
		t.Errorf("%s on %s on a moved repository sent %d request(s) [%s], want %d", e.Method, e.Product, len(call.sent), describeRequests(call.sent), want)
	}
	items := 1
	if op, ok := operationFor(e.Method); ok && op.items != nil && call.err == nil {
		items = op.items(call.got)
	}
	if low, high := price(&e, items); len(call.sent) < low || len(call.sent) > high {
		t.Errorf("%s on %s on a moved repository sent %d request(s), want %d to %d for %d item(s): the table prices the rename arm",
			e.Method, e.Product, len(call.sent), low, high, items)
	}
}

// staleCode is the error a call answered, which must be the stale reference's.
func staleCode(t *testing.T, call string, err error) *forgeapi.Error {
	t.Helper()
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefStale {
		t.Errorf("%s = error %v, want code %q", call, err, forgeapi.CodeRepoRefStale)
		return nil
	}
	return fe
}

// A Gitea-family read of a moved repository answers the successor's rows and names
// the successor the redirect's location gives, and its later requests address that
// successor, so the move costs the call the one request of its first hop.
func TestARenamedGiteaFamilyRepositorysReadNamesItsSuccessorAndCostsOneRequestMore(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		for _, method := range successorCarriers {
			e := requireEntry(t, p, method)
			t.Run(caseName(e), func(t *testing.T) {
				base := baseCost(t, e)
				f, err := loadFixture(p, method)
				if err != nil {
					t.Fatalf("Setup: %v", err)
				}
				srv, rec := movedInstance(t, f, giteaRepos+selector, giteaRepos+movedSelector, nil)

				call := callOn(t, e, srv, rec)
				if call.err != nil {
					t.Fatalf("%s on %s on a moved repository = error %v, want the successor's answer: %s", method, p, call.err, describeRequests(call.sent))
				}
				checkSuccessor(t, method+" on "+string(p), p, successorOf(call.got))
				checkPricedArm(t, e, call, base+1)
			})
		}
	}
}

// A Gitea-family read whose answer declares no successor refuses a moved repository
// with the stale code carrying the successor, at its first read and that read's hop,
// rather than answering the moved record as though nothing moved.
func TestARenamedGiteaFamilyRepositorysCarrierlessReadRefusesWithItsSuccessor(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		for _, method := range carrierlessReads {
			e := requireEntry(t, p, method)
			t.Run(caseName(e), func(t *testing.T) {
				f, err := loadFixture(p, method)
				if err != nil {
					t.Fatalf("Setup: %v", err)
				}
				srv, rec := movedInstance(t, f, giteaRepos+selector, giteaRepos+movedSelector, nil)

				call := callOn(t, e, srv, rec)
				if fe := staleCode(t, method+" on "+string(p)+" on a moved repository", call.err); fe != nil {
					checkSuccessor(t, method+" on "+string(p)+"'s refusal", p, fe.Successor)
				}
				checkPricedArm(t, e, call, 2)
			})
		}
	}
}

// A location that names no repository path, or one whose selector the family's rule
// refuses, names no successor a consumer could be right to re-point to, so the call
// answers the stale code with none, which is the reconnect-and-relist remedy.
func TestARenamedGiteaFamilyRepositoryWhoseLocationNamesNoUsableRepositoryIsStaleWithNoSuccessor(t *testing.T) {
	const refusedSelector = "ex%20ample/moved"
	if err := forgeapi.ValidateSelector(forgeapi.FamilyGitea, "ex ample/moved"); err == nil {
		t.Fatalf("Setup: ValidateSelector accepts %q, want a selector the family's rule refuses", "ex ample/moved")
	}
	locations := map[string]string{
		"an_id_addressed_location": "/api/v1/repositories/42",
		"a_refused_selector":       giteaRepos + refusedSelector,
	}
	for _, p := range giteaFamilyProducts {
		for _, method := range []string{"Labels.ListLabels", "Capabilities.RepoAffordances"} {
			e := requireEntry(t, p, method)
			for name, to := range locations {
				t.Run(caseName(e)+"_"+name, func(t *testing.T) {
					f, err := loadFixture(p, method)
					if err != nil {
						t.Fatalf("Setup: %v", err)
					}
					srv, rec := movedInstance(t, f, giteaRepos+selector, to, nil)

					call := callOn(t, e, srv, rec)
					fe := staleCode(t, method+" on "+string(p)+" redirected to "+to, call.err)
					if fe != nil && fe.Successor != nil {
						t.Errorf("%s on %s redirected to %s = successor %+v, want none: the location names no repository the family's rule admits", method, p, to, *fe.Successor)
					}
				})
			}
		}
	}
}

// A redirect to the repository the call addressed, spelled with another case, is no
// rename: the derived identifier folds case, so the call answers as on any hop.
func TestAGiteaFamilyRedirectToTheSameRepositoryIsNoRename(t *testing.T) {
	sameRepo := strings.ToUpper(owner[:1]) + owner[1:] + "/" + strings.ToUpper(repository[:1]) + repository[1:]
	for _, p := range giteaFamilyProducts {
		for _, method := range []string{"Labels.ListLabels", "Capabilities.RepoAffordances"} {
			e := requireEntry(t, p, method)
			t.Run(caseName(e), func(t *testing.T) {
				f, err := loadFixture(p, method)
				if err != nil {
					t.Fatalf("Setup: %v", err)
				}
				srv, rec := movedInstance(t, f, giteaRepos+selector, giteaRepos+sameRepo, nil)

				call := callOn(t, e, srv, rec)
				if call.err != nil {
					t.Fatalf("%s on %s redirected to %s = error %v, want the answer: the location names the repository the call addressed", method, p, sameRepo, call.err)
				}
				if got := successorOf(call.got); got != nil {
					t.Errorf("%s on %s redirected to %s = successor %+v, want none", method, p, sameRepo, *got)
				}
			})
		}
	}
}

// A write's hop is refused rather than followed, since re-issuing a write against
// the successor is the caller's decision, so a write to a moved repository answers
// the stale code carrying the successor its refused hop's location names, at the
// one request the write sent.
func TestAWriteToARenamedGiteaFamilyRepositoryRefusesWithItsSuccessor(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		for _, method := range []string{"Issues.CloseIssue", "PullRequests.ClosePR"} {
			e := requireEntry(t, p, method)
			t.Run(caseName(e), func(t *testing.T) {
				f, err := loadFixture(p, method)
				if err != nil {
					t.Fatalf("Setup: %v", err)
				}
				srv, rec := movedInstance(t, f, giteaRepos+selector, giteaRepos+movedSelector, nil)

				call := callOn(t, e, srv, rec)
				if fe := staleCode(t, method+" on "+string(p)+" on a moved repository", call.err); fe != nil {
					checkSuccessor(t, method+" on "+string(p)+"'s refusal", p, fe.Successor)
				}
				if len(call.sent) != 1 {
					t.Errorf("%s on %s on a moved repository sent %d request(s) [%s], want 1: the write's hop is refused and its location read at no further request",
						method, p, len(call.sent), describeRequests(call.sent))
				}
			})
		}
	}
}

// A creation that reads the repository's labels first meets the move on that read,
// and the successor the read's hop names is not the repository the caller asked to
// write to, so the creation refuses with the stale code carrying it and writes to
// neither address.
func TestACreationOnARenamedGiteaFamilyRepositoryWritesNothingAfterItsReadMeetsTheMove(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		e := requireEntry(t, p, "Issues.CreateIssue")
		t.Run(caseName(e), func(t *testing.T) {
			f, err := loadFixture(p, e.Method)
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			srv, rec := movedInstance(t, f, giteaRepos+selector, giteaRepos+movedSelector, nil)

			call := callOn(t, e, srv, rec)
			if fe := staleCode(t, "CreateIssue on "+string(p)+" on a moved repository", call.err); fe != nil {
				checkSuccessor(t, "CreateIssue on "+string(p)+"'s refusal", p, fe.Successor)
			}
			for _, req := range call.sent {
				if req.method != http.MethodGet {
					t.Errorf("CreateIssue on %s on a moved repository sent %s, want no write: re-issuing it against the successor is the caller's decision", p, req)
				}
			}
		})
	}
}

// githubRESTCarriers are GitHub's repository-addressed reads the conformance
// fixtures serve over REST, where a rename is witnessed by the redirect.
var githubRESTCarriers = []string{"Issues.ListIssues", "Releases.ListReleases", "Labels.ListLabels", "Checks.ListRuns"}

// movedRecord is the id-addressed repository record GitHub answers for the moved
// repository: the recorded one where the case reads it, naming the successor.
func movedRecord(t *testing.T, f fixture) []byte {
	t.Helper()
	record := map[string]any{}
	for _, w := range f.Routes {
		if w.Path == "/repos/"+selector {
			if err := json.Unmarshal(w.Body, &record); err != nil {
				t.Fatalf("Setup: %s's repository record is not a JSON object: %v", f.path, err)
			}
		}
	}
	record["full_name"] = movedSelector
	out, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Setup: encoding the moved record: %v", err)
	}
	return out
}

// GitHub's REST arm meets the redirect on each read, and the location is
// id-addressed, so the successor costs one further read: a one-read call sends its
// read, the hop and the resolving read, which is the arm the table prices.
func TestARenamedGitHubRepositorysRESTReadCostsItsReadItsHopAndOneResolvingRead(t *testing.T) {
	for _, method := range append(append([]string(nil), githubRESTCarriers...), "Capabilities.RepoAffordances") {
		e := requireEntry(t, spec.GitHub, method)
		t.Run(caseName(e), func(t *testing.T) {
			f, err := loadFixture(spec.GitHub, method)
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			srv, rec := movedInstance(t, f, "/repos/"+selector, githubRepositoryID, movedRecord(t, f))

			call := callOn(t, e, srv, rec)
			succ := successorOf(call.got)
			if method == "Capabilities.RepoAffordances" {
				if fe := staleCode(t, method+" on "+string(spec.GitHub)+" on a moved repository", call.err); fe != nil {
					succ = fe.Successor
				}
			} else if call.err != nil {
				t.Fatalf("%s on %s on a moved repository = error %v, want the successor's answer: %s", method, spec.GitHub, call.err, describeRequests(call.sent))
			}
			checkSuccessor(t, method+" on "+string(spec.GitHub), spec.GitHub, succ)
			checkPricedArm(t, e, call, 3)
		})
	}
}
