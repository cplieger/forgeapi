package conformance

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// namedIssue is the issue a named live subject's issue variable names in these
// cases, a number the offline fixtures never address.
const namedIssue = 16

// readIssue is the read case built on the live subject's issue.
const readIssue = "Issues.ReadIssue"

// issueInstance serves the issue read's fixture readdressed to repo and number, and
// points the live lane's URL and token for the product at it.
func issueInstance(t *testing.T, p spec.Product, repo string, number int) *recorder {
	t.Helper()
	f, err := loadFixture(p, readIssue)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	canonical := canonicalSubject(p)
	issue := "/issues/" + strconv.Itoa(canonical.issue.Number)
	for i := range f.Routes {
		path := strings.Replace(f.Routes[i].Path, "/repos/"+canonical.repo.Selector+"/", "/repos/"+repo+"/", 1)
		if head, ok := strings.CutSuffix(path, issue); ok {
			path = head + "/issues/" + strconv.Itoa(number)
		}
		f.Routes[i].Path = path
	}
	srv, rec := newFixtureServer(t, f)
	t.Setenv(liveVar(p, "URL"), srv.URL)
	t.Setenv(liveVar(p, "TOKEN"), "conformance-placeholder")
	return rec
}

// issuesRequested are the issue numbers the instance was asked for.
func issuesRequested(rec *recorder) []string {
	var out []string
	for _, req := range rec.since(0) {
		_, rest, found := strings.Cut(req.path, "/issues/")
		if !found {
			continue
		}
		number, _, _ := strings.Cut(rest, "/")
		out = append(out, number)
	}
	return out
}

// A lane that names its issue has the issue read address that number.
func TestALiveReadOfOneIssueAddressesTheIssueTheLaneNames(t *testing.T) {
	p := spec.Gitea
	rec := issueInstance(t, p, selector, namedIssue)
	t.Setenv(liveVar(p, "REPO"), selector)
	t.Setenv(liveVar(p, "ISSUE"), strconv.Itoa(namedIssue))

	if skipped := runReadLive(t, p, readIssue); skipped {
		t.Fatalf("%s on %s skipped with %s naming %d, want it run", readIssue, p, liveVar(p, "ISSUE"), namedIssue)
	}
	got := issuesRequested(rec)
	if want := []string{strconv.Itoa(namedIssue)}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s on %s addressed issue(s) %v [%s], want %v, the one %s names", readIssue, p, got, strings.Join(rec.requests(), ", "), want, liveVar(p, "ISSUE"))
	}
}

// No default project vouches for an issue at any number, so with the issue variable
// unset the read skips before any request, on a named subject and on the default one.
func TestALiveReadOfOneIssueSkipsWhereTheLaneNamesNone(t *testing.T) {
	p := spec.Gitea
	for name, repo := range map[string]string{"named_subject": selector, "default_subject": ""} {
		t.Run(name, func(t *testing.T) {
			rec := issueInstance(t, p, selector, canonicalSubject(p).issue.Number)
			if repo == "" {
				unsetLiveVar(t, p, "REPO")
			} else {
				t.Setenv(liveVar(p, "REPO"), repo)
			}
			unsetLiveVar(t, p, "ISSUE")

			if skipped := runReadLive(t, p, readIssue); !skipped {
				t.Errorf("%s on %s ran with %s unset, want it skipped", readIssue, p, liveVar(p, "ISSUE"))
			}
			if n := rec.count(); n != 0 {
				t.Errorf("the instance received %d request(s) [%s], want none", n, strings.Join(rec.requests(), ", "))
			}
		})
	}
}

// An issue variable that is set and names no positive number fails the read's setup
// before any request rather than skipping it. A failed setup ends its whole test, so
// each case runs in a child process of this test binary.
func TestALiveReadOfOneIssueFailsItsSetupOnAMalformedNumber(t *testing.T) {
	p := spec.Gitea
	if os.Getenv(liveSetupCase) == readIssue {
		rec := issueInstance(t, p, selector, namedIssue)
		runReadLive(t, p, readIssue)
		t.Logf("%s%d", liveRequestsLine, rec.count())
		return
	}
	for name, raw := range map[string]string{"not_a_number": "sixteen", "zero": "0", "negative": "-3"} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0],
				"-test.run=^TestALiveReadOfOneIssueFailsItsSetupOnAMalformedNumber$", "-test.v")
			cmd.Env = append(withoutLiveVars(os.Environ()),
				liveSetupCase+"="+readIssue, liveVar(p, "REPO")+"="+selector, liveVar(p, "ISSUE")+"="+raw)
			out, err := cmd.CombinedOutput()
			text := string(out)
			if _, failed := errors.AsType[*exec.ExitError](err); !failed {
				t.Fatalf("%s with %s=%q = exit %v, want the case to fail its setup:\n%s", readIssue, liveVar(p, "ISSUE"), raw, err, text)
			}
			if strings.Contains(text, "--- SKIP") {
				t.Errorf("%s with %s=%q skipped, want its setup to fail:\n%s", readIssue, liveVar(p, "ISSUE"), raw, text)
			}
			for _, want := range []string{
				"--- FAIL: TestALiveReadOfOneIssueFailsItsSetupOnAMalformedNumber/live",
				liveVar(p, "ISSUE") + " = " + strconv.Quote(raw),
				liveRequestsLine + "0",
			} {
				if !strings.Contains(text, want) {
					t.Errorf("%s with %s=%q printed no %q:\n%s", readIssue, liveVar(p, "ISSUE"), raw, want, text)
				}
			}
		})
	}
}
