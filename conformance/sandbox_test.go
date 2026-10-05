package conformance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// sandboxCase is the mutation the gate cases drive: one issue created on the Gitea
// family, which the case closes again, so the instance below needs the creation's
// routes and the close's and nothing a branch or a tag would need.
const sandboxCase = "Issues.CreateIssue"

// liveVar is one of the live lane's per-product variables, FORGEAPI_LIVE_ then the
// product then the variable's own name.
func liveVar(p spec.Product, name string) string {
	return "FORGEAPI_LIVE_" + strings.ToUpper(string(p)) + "_" + name
}

// liveOwner is the owner the lane scopes the cross-repository lists to: the first
// segment of the product's sandbox, the account or top-level group holding it, or
// empty where no sandbox is named. On GitLab the group route spans its subgroups, so
// the top-level group reaches a sandbox nested below it.
func liveOwner(p spec.Product) string {
	owner, _, _ := strings.Cut(os.Getenv(liveVar(p, "SANDBOX")), "/")
	return owner
}

// sandboxInstance serves the mutation case's fixture and the close of what it
// creates, and points the live lane's variables for the product at it.
//
// The listing of open items, branches, tags and releases before and after a run
// is the lane's own rather than a case's, and it is held live, so this instance
// answers the case's own requests and its cleanup.
func sandboxInstance(t *testing.T, p spec.Product, sandbox string) *recorder {
	t.Helper()
	return creationInstance(t, p, sandbox, nil)
}

// creationInstance serves the issue creation and the close of what it creates the
// way a forge answers them: the created issue carries the title and body the
// request sent, over the rest of the recorded answer, and alteration, where it is
// not nil, is applied over that. It points the live lane's variables for the
// product at the instance with the sandbox named as given.
func creationInstance(t *testing.T, p spec.Product, sandbox string, alteration func(map[string]any)) *recorder {
	t.Helper()
	created, err := loadFixture(p, sandboxCase)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	closing, err := loadFixture(p, "Issues.CloseIssue")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	f := created
	f.Routes = append(slices.Clone(created.Routes), closing.Routes...)
	creation := "/api/v1/repos/" + selector + "/issues"
	rec := &recorder{spent: make([]int, len(f.Routes))}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxFixtureBody))
		if err != nil {
			body = nil
		}
		rec.record(sent{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.Query(), body: body})
		route, ok := rec.take(f, r, body)
		if !ok {
			rec.miss(r.Method, r.URL.EscapedPath())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		answer := route.Body
		if r.Method == http.MethodPost && r.URL.EscapedPath() == creation {
			answer = echoedCreation(t, route.Body, body, alteration)
		}
		for k, v := range route.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(route.Status)
		if _, err := w.Write(answer); err != nil {
			t.Errorf("Setup: writing %s %s: %v", r.Method, r.URL.EscapedPath(), err)
		}
	}))
	t.Cleanup(srv.Close)
	pointLiveLaneAt(t, p, srv.URL, sandbox)
	return rec
}

// echoedCreation is the recorded creation answer carrying the title and body the
// request sent, with alteration applied over it where it is not nil.
func echoedCreation(t *testing.T, recorded, request []byte, alteration func(map[string]any)) []byte {
	t.Helper()
	var asked struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal(request, &asked); err != nil {
		t.Errorf("Setup: the creation request %q is not a JSON object: %v", request, err)
	}
	var answer map[string]any
	if err := json.Unmarshal(recorded, &answer); err != nil {
		t.Errorf("Setup: the recorded creation answer is not a JSON object: %v", err)
	}
	answer["title"], answer["body"] = asked.Title, asked.Body
	if alteration != nil {
		alteration(answer)
	}
	out, err := json.Marshal(answer)
	if err != nil {
		t.Errorf("Setup: re-encoding the creation answer: %v", err)
	}
	return out
}

// pointLiveLaneAt points the live lane's variables for one product at an instance,
// with the canonical subject as its repository and the sandbox named as given. Every
// other product's sandbox names the canonical subject, so a case passing here passes
// on its own product's variable alone.
func pointLiveLaneAt(t *testing.T, p spec.Product, url, sandbox string) {
	t.Helper()
	for _, other := range spec.Products {
		t.Setenv(liveVar(other, "SANDBOX"), selector)
	}
	t.Setenv(liveVar(p, "URL"), url)
	t.Setenv(liveVar(p, "TOKEN"), "conformance-placeholder")
	t.Setenv(liveVar(p, "REPO"), selector)
	t.Setenv(liveVar(p, "SANDBOX"), sandbox)
}

// runMutationLive drives the mutation case through the live lane and reports
// whether it skipped.
func runMutationLive(t *testing.T, p spec.Product) (skipped bool) {
	t.Helper()
	e := requireEntry(t, p, sandboxCase)
	op, ok := operationFor(sandboxCase)
	if !ok || !op.mutation {
		t.Fatalf("Setup: the contract declares no mutation case for %s", sandboxCase)
	}
	t.Run("live", func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		runLive(t, e, op)
	})
	return skipped
}

// A mutation runs only where its own product's sandbox variable names the
// repository it writes to, so an instance a developer points the suite at receives
// no write at all, whatever the other products' sandboxes name.
func TestALiveMutationWithNoSandboxNamedSendsNothing(t *testing.T) {
	rec := sandboxInstance(t, spec.Gitea, "")

	if skipped := runMutationLive(t, spec.Gitea); !skipped {
		t.Errorf("%s on %s ran with %s unset, want it skipped", sandboxCase, spec.Gitea, liveVar(spec.Gitea, "SANDBOX"))
	}
	if n := rec.count(); n != 0 {
		t.Errorf("the instance received %d request(s) [%s], want none", n, strings.Join(rec.requests(), ", "))
	}
}

func TestALiveMutationOnARepositoryOtherThanTheSandboxSendsNothing(t *testing.T) {
	rec := sandboxInstance(t, spec.Gitea, owner+"/another-"+repository)

	if skipped := runMutationLive(t, spec.Gitea); !skipped {
		t.Errorf("%s on %s ran against %s while the sandbox names another repository, want it skipped", sandboxCase, spec.Gitea, selector)
	}
	if n := rec.count(); n != 0 {
		t.Errorf("the instance received %d request(s) [%s], want none", n, strings.Join(rec.requests(), ", "))
	}
}

// On the sandbox the mutation runs, and it closes what it opened before the case
// ends. The repository is compared under the selector equality the library uses,
// which folds case.
func TestALiveMutationOnTheSandboxRunsAndClosesWhatItOpened(t *testing.T) {
	for name, sandbox := range map[string]string{"as_spelled": selector, "case_folded": strings.ToUpper(selector)} {
		t.Run(name, func(t *testing.T) {
			rec := sandboxInstance(t, spec.Gitea, sandbox)

			if skipped := runMutationLive(t, spec.Gitea); skipped {
				t.Fatalf("%s on %s skipped with %s naming %q, want it run", sandboxCase, spec.Gitea, liveVar(spec.Gitea, "SANDBOX"), sandbox)
			}
			created, closed := -1, -1
			for i, req := range rec.since(0) {
				switch {
				case req.method == "POST" && req.path == "/api/v1/repos/"+selector+"/issues":
					created = i
				case req.method == "PATCH" && req.path == "/api/v1/repos/"+selector+"/issues/1":
					if state, ok := bodyField(req.body, "state"); ok && string(state) == `"closed"` {
						closed = i
					}
				}
			}
			if created < 0 {
				t.Fatalf("the instance received no creation [%s], want the mutation sent", strings.Join(rec.requests(), ", "))
			}
			if closed < created {
				t.Errorf("the instance received no close of issue 1 after its creation [%s], want the case to close what it opened",
					strings.Join(rec.requests(), ", "))
			}
		})
	}
}

// A merge the product accepted runs in the background and writes its base when it
// lands, so a base removed before then comes back and the run leaves the sandbox
// other than it found it. The branch removals a merge's setup registers therefore
// wait until the pull request reads merged, and wait for nothing after a merge the
// product completed when it answered.
func TestAnAcceptedMergesBranchesAreRemovedOnceItLands(t *testing.T) {
	merge := liveMutations["Merges.MergePR"]
	for _, tc := range []struct {
		name      string
		outcome   forgeapi.MergeOutcomeState
		wantReads int
	}{
		{name: "accepted", outcome: forgeapi.MergeOutcomeAccepted, wantReads: 2},
		{name: "merged", outcome: forgeapi.MergeOutcomeMerged, wantReads: 0},
	} {
		var mu sync.Mutex
		var seen []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, r.Method+" "+r.URL.Path)
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// The first read finds the merge still running, every later one finds it landed.
			answer := `{"state":"open","merged":false}`
			if slices.ContainsFunc(seen[:len(seen)-1], func(s string) bool { return strings.HasPrefix(s, "GET ") }) {
				answer = `{"state":"closed","merged":true}`
			}
			w.Header().Set("Content-Type", "application/json")
			if _, err := io.WriteString(w, answer); err != nil {
				t.Errorf("Setup: answering the pull-request read: %v", err)
			}
		}))
		l := newLane(spec.GitHub, srv.URL, "lane-token", selector)
		s := canonicalSubject(spec.GitHub)
		t.Run(tc.name, func(t *testing.T) {
			l.later(t, "deleting the base the setup made", func(ctx context.Context) error { return l.deleteBranch(ctx, "base") })
			if merge.made != nil {
				merge.made(t, l, s, forgeapi.MergeOutcome{State: tc.outcome})
			}
		})
		srv.Close()
		read := "GET /api/v3/repos/" + selector + "/pulls/" + strconv.Itoa(s.pr.Number)
		removal := slices.Index(seen, "DELETE /api/v3/repos/"+selector+"/git/refs/heads/base")
		if removal < 0 {
			t.Fatalf("%s: the instance received no removal of the base [%s], want one", tc.name, strings.Join(seen, ", "))
		}
		if got := len(slices.DeleteFunc(slices.Clone(seen[:removal]), func(r string) bool { return r != read })); got != tc.wantReads {
			t.Errorf("%s: %d pull-request read(s) before the base's removal [%s], want %d: the removal waits until the merge reads landed and no longer",
				tc.name, got, strings.Join(seen, ", "), tc.wantReads)
		}
	}
}

// GitHub's release listing omits a release for a second or more after its creation
// answered, while a read of the release by its tag finds it at once, so the removal
// a creation registered finds the release by its tag: a removal that looked for it
// in the listing deletes the tag alone and leaves the release behind.
func TestAReleaseIsRemovedByItsTagWhileTheListingOmitsIt(t *testing.T) {
	const tag = laneMark + "20261002-120000-release"
	for _, p := range []spec.Product{spec.GitHub, spec.Gitea} {
		var mu sync.Mutex
		var seen []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, r.Method+" "+r.URL.Path)
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			answer := `[]`
			if strings.HasSuffix(r.URL.Path, "/releases/tags/"+tag) {
				answer = `{"id":42,"tag_name":"` + tag + `"}`
			}
			w.Header().Set("Content-Type", "application/json")
			if _, err := io.WriteString(w, answer); err != nil {
				t.Errorf("Setup: answering %s: %v", r.URL.Path, err)
			}
		}))
		l := newLane(p, srv.URL, "lane-token", selector)
		err := l.removeRelease(t.Context(), tag)
		srv.Close()
		if err != nil {
			t.Fatalf("%s: removeRelease(%q) = %v, want nil", p, tag, err)
		}
		root := laneAPIRoot(p, srv.URL)
		release := slices.Index(seen, "DELETE "+strings.TrimPrefix(root, srv.URL)+"/repos/"+selector+"/releases/42")
		if release < 0 {
			t.Errorf("%s: requests [%s], want the release the read by its tag found deleted by its id", p, strings.Join(seen, ", "))
		}
		tagRoute := "/repos/" + selector + "/tags/" + tag
		if p == spec.GitHub {
			tagRoute = "/repos/" + selector + "/git/refs/tags/" + tag
		}
		if removal := slices.Index(seen, "DELETE "+strings.TrimPrefix(root, srv.URL)+tagRoute); removal < release {
			t.Errorf("%s: requests [%s], want the tag deleted after its release", p, strings.Join(seen, ", "))
		}
	}
}

// A lane request's refusal quotes the instance's body, and the instance can echo
// the credential the request carried, so the quoted text holds no part of the token,
// no control an instance sent, and no rune the bound cut in half.
func TestALaneRefusalQuotesTheInstanceWithoutTheTokenItCarried(t *testing.T) {
	const token = "lane-held-token-0123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		echo := "refused " + r.Header.Get("Authorization") + " \x1b[31m" + token + "\u202e x" + strings.Repeat("é", 400)
		if _, err := io.WriteString(w, echo); err != nil {
			t.Errorf("Setup: writing the refusal: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	_, err := newLane(spec.Gitea, srv.URL, token, selector).call(t.Context(), http.MethodGet, "/repos/"+selector, nil, nil)
	if err == nil {
		t.Fatalf("lane.call against a 403 = nil, want the refusal")
	}
	text := err.Error()
	if strings.Contains(text, token) || strings.Contains(text, token[:len(token)/2]) {
		t.Errorf("lane.call refusal = %q, want no part of the token the request carried", text)
	}
	if strings.ContainsAny(text, "\x1b\u202e") {
		t.Errorf("lane.call refusal = %q, want no terminal escape and no bidirectional override from the instance's body", text)
	}
	if !utf8.ValidString(text) {
		t.Errorf("lane.call refusal = %q, want valid UTF-8: the bound cuts on a rune boundary", text)
	}
	if !strings.Contains(text, "refused") {
		t.Errorf("lane.call refusal = %q, want it to quote what the instance said", text)
	}
}

// Each redaction in a lane refusal catches what the other cannot. A token carrying a
// space, echoed with a control byte in that place, is only whole once the sanitizer
// has turned the control into the space, so the redaction after the sanitizer is the
// one that finds it. A token carrying two spaces, echoed verbatim, is shortened by
// the whitespace collapse after the sanitizer, so the redaction before it is the one
// that finds it, and its tail is what would otherwise survive.
func TestALaneRefusalRedactsTheTokenOnBothSidesOfTheSanitizer(t *testing.T) {
	for _, test := range []struct {
		name   string
		token  string
		echo   string
		absent string
	}{
		{name: "rebuilt_by_the_sanitizer", token: "lane held-0123", echo: "x lane\x1bheld-0123 y", absent: "lane held-0123"},
		{name: "rewritten_by_the_collapse", token: "lane  held-0123", echo: "x lane  held-0123 y", absent: "held-0123"},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				if _, err := io.WriteString(w, test.echo); err != nil {
					t.Errorf("Setup: writing the refusal: %v", err)
				}
			}))
			t.Cleanup(srv.Close)

			_, err := newLane(spec.Gitea, srv.URL, test.token, selector).call(t.Context(), http.MethodGet, "/repos/"+selector, nil, nil)
			if err == nil {
				t.Fatalf("lane.call against a 403 = nil, want the refusal")
			}
			if text := err.Error(); strings.Contains(text, test.absent) {
				t.Errorf("lane.call refusal = %q, want no %q of the token the request carried", text, test.absent)
			}
		})
	}
}
