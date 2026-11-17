package conformance

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// The table's fields for what a re-run does beyond its answer and what it and the
// connection read answer on a Forgejo before 17.
const (
	jobsRerun          = "jobs re-run"
	headPipelineRerun  = "run re-run"
	verbAbsentBefore17 = "error, verb absent, before 17"
	capsBefore17       = "Caps[rerun_checks], before 17"
)

// The swagger paths of the re-run routes the Gitea family's detection reads.
const (
	giteaFailedJobs = "/repos/{owner}/{repo}/actions/runs/{run}/rerun-failed-jobs"
	giteaWholeRun   = "/repos/{owner}/{repo}/actions/runs/{run}/rerun"
	forgejoWholeRun = "/repos/{owner}/{repo}/actions/runs/{run_id}/rerun"
)

var rerunMethods = []string{"PullRequests.RerunFailedChecks", "Checks.RerunFailedRun"}

// rerunSuffix is the last segment of the route each product's re-run posts to, which
// is what decides the jobs it re-runs: the failed jobs and their dependents on
// GitHub, the failed and canceled jobs on GitLab, the failed jobs on Gitea and the
// whole finished run on Forgejo, each as its own API description states.
var rerunSuffix = map[spec.Product]string{
	spec.GitHub:  "/rerun-failed-jobs",
	spec.GitLab:  "/retry",
	spec.Gitea:   "/rerun-failed-jobs",
	spec.Forgejo: "/rerun",
}

func TestEachProductReRunsTheJobsItsRouteDocuments(t *testing.T) {
	for _, p := range spec.Products {
		for _, method := range rerunMethods {
			e := requireEntry(t, p, method)
			t.Run(caseName(e), func(t *testing.T) {
				f, err := loadFixture(p, method)
				if err != nil {
					t.Fatalf("Setup: %v", err)
				}
				srv, rec := newFixtureServer(t, f)
				call := callOn(t, e, srv, rec)
				if call.err != nil {
					t.Fatalf("%s on %s = error %v, want nil", method, p, call.err)
				}
				if len(call.sent) == 0 {
					t.Fatalf("%s on %s sent nothing, want the re-run", method, p)
				}
				last := call.sent[len(call.sent)-1]
				if last.method != http.MethodPost || !strings.HasSuffix(last.path, rerunSuffix[p]) {
					t.Errorf("%s on %s ended with %s, want a POST ending %s: %s", method, p, last, rerunSuffix[p], jobsRerun)
				}
			})
		}
	}
}

// withSwagger is one product's connection setup with the version body reporting
// version, where it is not empty, and the API document's paths edited.
func withSwagger(t *testing.T, p spec.Product, version string, edit func(paths map[string]any)) []wire {
	t.Helper()
	f, err := loadFixture(p, connectionRead)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	routes := make([]wire, len(f.Routes))
	copy(routes, f.Routes)
	for i := range routes {
		switch routes[i].Path {
		case "/api/v1/version":
			if version != "" {
				routes[i].Body = jsonBody(t, map[string]any{"version": version})
			}
		case "/swagger.v1.json":
			doc := map[string]any{}
			if err := json.Unmarshal(routes[i].Body, &doc); err != nil {
				t.Fatalf("Setup: the API document is not a JSON object: %v", err)
			}
			paths, ok := doc["paths"].(map[string]any)
			if !ok {
				t.Fatalf("Setup: the API document declares no paths")
			}
			edit(paths)
			routes[i].Body = jsonBody(t, doc)
		}
	}
	return routes
}

// rerunClient is a mutating client on an instance serving routes, its connection
// read already made, with the capabilities that read answered.
func rerunClient(t *testing.T, p spec.Product, routes []wire) (any, *recorder, forgeapi.ConnectionCaps) {
	t.Helper()
	srv, rec := newFixtureServer(t, fixture{path: "inline", Routes: routes})
	client := pagedClient(t, p, srv, forgeapi.WithMutations(true))
	caps, err := client.(forgeapi.Capabilities).ConnectionCaps(t.Context())
	if err != nil {
		t.Fatalf("Setup: ConnectionCaps on %s: %v", p, err)
	}
	return client, rec, caps
}

func rerun(t *testing.T, client any, method string, p spec.Product, run forgeapi.RunRef) error {
	t.Helper()
	if method == "Checks.RerunFailedRun" {
		return client.(forgeapi.Checks).RerunFailedRun(t.Context(), repoRef(p), run)
	}
	return client.(forgeapi.PullRequests).RerunFailedChecks(t.Context(), repoRef(p), prRef(p), headSHA)
}

// A Forgejo before 17 declares no re-run route, measured on the swagger documents of
// tags v14.0.0, v15.0.0 and v16.0.5, so its connection reports the capability no
// and both re-runs refuse with no request.
func TestAForgejoBefore17RefusesTheReRunWithNoRequest(t *testing.T) {
	routes := withSwagger(t, spec.Forgejo, "16.0.5+gitea-1.22.0", func(paths map[string]any) {
		delete(paths, forgejoWholeRun)
	})
	client, rec, caps := rerunClient(t, spec.Forgejo, routes)
	if got := caps.Caps[forgeapi.CapRerunChecks]; got != forgeapi.SupportNo {
		t.Errorf("ConnectionCaps on Forgejo 16: Caps[rerun_checks] = %s, want no: %s", got, capsBefore17)
	}
	if got := caps.Ev[forgeapi.CapRerunChecks].Source; got != forgeapi.EvidenceSwagger {
		t.Errorf("ConnectionCaps on Forgejo 16: Ev[rerun_checks].Source = %s, want %s", got, forgeapi.EvidenceSwagger)
	}
	for _, method := range rerunMethods {
		t.Run(method, func(t *testing.T) {
			before := rec.count()
			err := rerun(t, client, method, spec.Forgejo, forgeapi.RunRef{ID: 1})
			fe, ok := errors.AsType[*forgeapi.Error](err)
			if !ok || fe.Code != forgeapi.CodeCapabilityUnsupported || fe.Capability != forgeapi.CapRerunChecks {
				t.Errorf("%s on Forgejo 16 = %v, want code %s on capability %s: %s", method, err, forgeapi.CodeCapabilityUnsupported, forgeapi.CapRerunChecks, verbAbsentBefore17)
			}
			if sent := rec.since(before); len(sent) != 0 {
				t.Errorf("%s on Forgejo 16 sent %v, want no request", method, paths(sent))
			}
		})
	}
}

// Gitea's own document declares a whole-run route under {run} beside its failed-jobs
// route, measured on gitea.com's 29.0.0+dev swagger read 2026-11-16: the failed-jobs
// route is the one taken, and a document declaring Gitea's whole-run route alone
// answers no, because only Forgejo's exact path is read as a whole-run re-run.
func TestTheGiteaFamilyTakesTheFailedJobsRouteWhereTheDocumentDeclaresIt(t *testing.T) {
	post := wire{
		Method: http.MethodPost, Path: giteaRepos + selector + "/actions/runs/1/rerun-failed-jobs",
		Status: http.StatusCreated, Body: json.RawMessage(`{}`),
	}
	t.Run("both_declared", func(t *testing.T) {
		routes := withSwagger(t, spec.Gitea, "", func(paths map[string]any) {
			paths[giteaWholeRun] = map[string]any{"post": map[string]any{"operationId": "rerunWorkflowRun"}}
		})
		client, rec, _ := rerunClient(t, spec.Gitea, append(routes, post))
		before := rec.count()
		if err := rerun(t, client, "Checks.RerunFailedRun", spec.Gitea, forgeapi.RunRef{ID: 1}); err != nil {
			t.Fatalf("RerunFailedRun on Gitea = %v, want nil", err)
		}
		if got := paths(rec.since(before)); len(got) != 1 || got[0] != "POST "+post.Path {
			t.Errorf("RerunFailedRun on Gitea sent %v, want [POST %s]", got, post.Path)
		}
	})
	t.Run("whole_run_under_run_alone", func(t *testing.T) {
		routes := withSwagger(t, spec.Gitea, "", func(paths map[string]any) {
			delete(paths, giteaFailedJobs)
			paths[giteaWholeRun] = map[string]any{"post": map[string]any{"operationId": "rerunWorkflowRun"}}
		})
		_, _, caps := rerunClient(t, spec.Gitea, routes)
		if got := caps.Caps[forgeapi.CapRerunChecks]; got != forgeapi.SupportNo {
			t.Errorf("ConnectionCaps on a document declaring only %s: Caps[rerun_checks] = %s, want no", giteaWholeRun, got)
		}
	})
}

// A run id that is not positive addresses no run, so every product refuses it before
// any request, its capability read included.
func TestAReRunOfARunWithNoIDSendsNothing(t *testing.T) {
	for _, p := range spec.Products {
		for _, id := range []int64{0, -1} {
			t.Run(string(p)+"_"+strconv.FormatInt(id, 10), func(t *testing.T) {
				srv, rec := newFixtureServer(t, fixture{path: "inline"})
				client := pagedClient(t, p, srv, forgeapi.WithMutations(true))
				err := client.(forgeapi.Checks).RerunFailedRun(t.Context(), repoRef(p), forgeapi.RunRef{ID: id})
				if fe, ok := errors.AsType[*forgeapi.Error](err); !ok || fe.Code != forgeapi.CodeRepoRefInvalid {
					t.Errorf("RerunFailedRun on %s of run %d = %v, want code %s", p, id, err, forgeapi.CodeRepoRefInvalid)
				}
				if n := rec.count(); n != 0 {
					t.Errorf("RerunFailedRun on %s of run %d sent %d request(s), want none", p, id, n)
				}
			})
		}
	}
}

// A pull-request number that is not positive addresses no pull request, so every
// product refuses a re-run of its checks before any request, pinned or not.
func TestAReRunOfAPullRequestWithNoNumberSendsNothing(t *testing.T) {
	for _, p := range spec.Products {
		for _, number := range []int{0, -1} {
			for _, pin := range []string{headSHA, ""} {
				t.Run(string(p)+"_"+strconv.Itoa(number)+map[bool]string{true: "_pinned", false: "_unpinned"}[pin != ""], func(t *testing.T) {
					srv, rec := newFixtureServer(t, fixture{path: "inline"})
					client := pagedClient(t, p, srv, forgeapi.WithMutations(true))
					err := client.(forgeapi.PullRequests).RerunFailedChecks(t.Context(), repoRef(p), forgeapi.PRRef{Number: number, Sigil: sigil(p)}, pin)
					if fe, ok := errors.AsType[*forgeapi.Error](err); !ok || fe.Code != forgeapi.CodeRepoRefInvalid {
						t.Errorf("RerunFailedChecks on %s of pull request %d = %v, want code %s", p, number, err, forgeapi.CodeRepoRefInvalid)
					}
					if n := rec.count(); n != 0 {
						t.Errorf("RerunFailedChecks on %s of pull request %d sent %d request(s), want none", p, number, n)
					}
				})
			}
		}
	}
}
