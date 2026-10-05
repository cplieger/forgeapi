package conformance

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/internal/spec"
)

// namedPullRequest is the pull request the cases below name through the live lane's
// variable, a number the offline fixtures never address.
const namedPullRequest = 7

// onePullRequestReads are the read cases built on the live subject's pull request.
var onePullRequestReads = []string{"PullRequests.ReadPR", "Merges.MergeStatus"}

// pullRequestInstance serves one read case's fixture with its routes readdressed
// from the canonical subject's repository and pull request to the ones given, and
// points the live lane's URL and token for the product at it.
func pullRequestInstance(t *testing.T, p spec.Product, method, repo string, number int) *recorder {
	t.Helper()
	f, err := loadFixture(p, method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	canonical := canonicalSubject(p)
	pull := "/pulls/" + strconv.Itoa(canonical.pr.Number)
	for i := range f.Routes {
		path := strings.Replace(f.Routes[i].Path, "/repos/"+canonical.repo.Selector+"/", "/repos/"+repo+"/", 1)
		if head, ok := strings.CutSuffix(path, pull); ok {
			path = head + "/pulls/" + strconv.Itoa(number)
		}
		f.Routes[i].Path = path
	}
	srv, rec := newFixtureServer(t, f)
	t.Setenv(liveVar(p, "URL"), srv.URL)
	t.Setenv(liveVar(p, "TOKEN"), "conformance-placeholder")
	return rec
}

// unsetLiveVar removes one of the live lane's variables for the rest of the case,
// whatever the environment the suite runs in set it to.
func unsetLiveVar(t *testing.T, p spec.Product, name string) {
	t.Helper()
	t.Setenv(liveVar(p, name), "")
	if err := os.Unsetenv(liveVar(p, name)); err != nil {
		t.Fatalf("Setup: unsetting %s: %v", liveVar(p, name), err)
	}
}

// runReadLive drives one read case through the live lane and reports whether it
// skipped.
func runReadLive(t *testing.T, p spec.Product, method string) (skipped bool) {
	t.Helper()
	e := requireEntry(t, p, method)
	op, ok := operationFor(method)
	if !ok || op.mutation {
		t.Fatalf("Setup: the contract declares no read case for %s", method)
	}
	t.Run("live", func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		runLive(t, e, op)
	})
	return skipped
}

// pullsRequested are the pull-request numbers the instance was asked for, read off
// every request path addressing one.
func pullsRequested(rec *recorder) []string {
	var out []string
	for _, req := range rec.since(0) {
		_, rest, found := strings.Cut(req.path, "/pulls/")
		if !found {
			continue
		}
		number, _, _ := strings.Cut(rest, "/")
		out = append(out, number)
	}
	return out
}

// A named subject names its pull request too, and the cases that read one pull
// request address that one, not the offline fixture's number.
func TestALiveReadOfOnePullRequestAddressesThePullRequestTheLaneNames(t *testing.T) {
	for _, method := range onePullRequestReads {
		t.Run(method, func(t *testing.T) {
			p := spec.Gitea
			rec := pullRequestInstance(t, p, method, selector, namedPullRequest)
			t.Setenv(liveVar(p, "REPO"), selector)
			t.Setenv(liveVar(p, "PR"), strconv.Itoa(namedPullRequest))

			if skipped := runReadLive(t, p, method); skipped {
				t.Fatalf("%s on %s skipped with %s naming %d, want it run", method, p, liveVar(p, "PR"), namedPullRequest)
			}
			want := strconv.Itoa(namedPullRequest)
			got := pullsRequested(rec)
			if len(got) == 0 {
				t.Errorf("%s on %s addressed no pull request [%s], want %s", method, p, strings.Join(rec.requests(), ", "), want)
			}
			for _, number := range got {
				if number != want {
					t.Errorf("%s on %s addressed pull request %s [%s], want %s, the one %s names", method, p, number, strings.Join(rec.requests(), ", "), want, liveVar(p, "PR"))
				}
			}
		})
	}
}

// A subject named without its pull request names no number the repository is known
// to hold, so the cases that read one skip rather than address a guess.
func TestALiveReadOfOnePullRequestSkipsWhereTheNamedSubjectNamesNone(t *testing.T) {
	for _, method := range onePullRequestReads {
		t.Run(method, func(t *testing.T) {
			p := spec.Gitea
			rec := pullRequestInstance(t, p, method, selector, canonicalSubject(p).pr.Number)
			t.Setenv(liveVar(p, "REPO"), selector)
			unsetLiveVar(t, p, "PR")

			if skipped := runReadLive(t, p, method); !skipped {
				t.Errorf("%s on %s ran with %s naming a subject and %s unset, want it skipped", method, p, liveVar(p, "REPO"), liveVar(p, "PR"))
			}
			if n := rec.count(); n != 0 {
				t.Errorf("the instance received %d request(s) [%s], want none", n, strings.Join(rec.requests(), ", "))
			}
		})
	}
}

// With no subject named, the harness's own public subject is read at the harness's
// canonical number, so an anonymous lane against a public instance keeps running.
func TestALiveReadOfOnePullRequestOnTheDefaultSubjectAddressesTheCanonicalNumber(t *testing.T) {
	for _, method := range onePullRequestReads {
		t.Run(method, func(t *testing.T) {
			p := spec.Gitea
			unsetLiveVar(t, p, "REPO")
			unsetLiveVar(t, p, "PR")
			public := liveSubject(p).repo.Selector
			if public == "" {
				t.Fatalf("Setup: the harness names no public subject for %s", p)
			}
			number := canonicalSubject(p).pr.Number
			rec := pullRequestInstance(t, p, method, public, number)

			if skipped := runReadLive(t, p, method); skipped {
				t.Fatalf("%s on %s skipped with no subject named, want it run against the public subject %s", method, p, public)
			}
			want := strconv.Itoa(number)
			for _, got := range pullsRequested(rec) {
				if got != want {
					t.Errorf("%s on %s addressed pull request %s on the public subject, want %s, the harness's canonical number", method, p, got, want)
				}
			}
		})
	}
}

// liveSetupCase names, in a child process of this test binary, the read case the
// child drives through the live lane; it is unset in the run the suite makes.
const liveSetupCase = "FORGEAPI_CONFORMANCE_LIVE_SETUP_CASE"

// liveRequestsLine opens the line on which a child reports how many requests its
// instance received.
const liveRequestsLine = "the instance received requests: "

// withoutLiveVars is an environment with every live lane variable removed, so a
// child process reads only the ones its case sets.
func withoutLiveVars(environ []string) []string {
	var out []string
	for _, kv := range environ {
		if !strings.HasPrefix(kv, "FORGEAPI_LIVE_") {
			out = append(out, kv)
		}
	}
	return out
}

// A pull request variable that is set and names no positive number fails the setup
// of the cases that read one pull request rather than skipping them, since a lane
// that misnames its read subject would otherwise pass having read nothing, and it
// fails before any request. A failed setup ends its whole test, so each case runs in
// a child process of this test binary, whose output and exit status are what the
// lane itself would see.
func TestALiveReadOfOnePullRequestFailsItsSetupOnAMalformedNumber(t *testing.T) {
	p := spec.Gitea
	if named := os.Getenv(liveSetupCase); named != "" {
		// The case is the read the parent named, taken from the suite's own list
		// rather than from the environment's text.
		i := slices.Index(onePullRequestReads, named)
		if i < 0 {
			t.Fatalf("Setup: %s = %q, want one of %v", liveSetupCase, named, onePullRequestReads)
		}
		method := onePullRequestReads[i]
		rec := pullRequestInstance(t, p, method, selector, namedPullRequest)
		runReadLive(t, p, method)
		t.Logf("%s%d", liveRequestsLine, rec.count())
		return
	}
	malformed := map[string]string{"not_a_number": "seven", "zero": "0", "negative": "-3"}
	for _, method := range onePullRequestReads {
		for name, raw := range malformed {
			t.Run(method+"_"+name, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), os.Args[0],
					"-test.run=^TestALiveReadOfOnePullRequestFailsItsSetupOnAMalformedNumber$", "-test.v")
				cmd.Env = append(withoutLiveVars(os.Environ()),
					liveSetupCase+"="+method, liveVar(p, "REPO")+"="+selector, liveVar(p, "PR")+"="+raw)
				out, err := cmd.CombinedOutput()
				text := string(out)
				if _, failed := errors.AsType[*exec.ExitError](err); !failed {
					t.Fatalf("%s on %s with %s=%q = exit %v, want the case to fail its setup:\n%s", method, p, liveVar(p, "PR"), raw, err, text)
				}
				if strings.Contains(text, "--- SKIP") {
					t.Errorf("%s on %s with %s=%q skipped, want its setup to fail:\n%s", method, p, liveVar(p, "PR"), raw, text)
				}
				for _, want := range []string{
					"--- FAIL: TestALiveReadOfOnePullRequestFailsItsSetupOnAMalformedNumber/live",
					liveVar(p, "PR") + " = " + strconv.Quote(raw),
					liveRequestsLine + "0",
				} {
					if !strings.Contains(text, want) {
						t.Errorf("%s on %s with %s=%q printed no %q, want the setup failure naming the variable before any request:\n%s", method, p, liveVar(p, "PR"), raw, want, text)
					}
				}
			})
		}
	}
}
