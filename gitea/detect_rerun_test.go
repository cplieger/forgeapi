package gitea

import (
	"slices"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// The re-run routes each product's swagger declares, as gitea.com's 29.0.0+dev
// document and codeberg.org's 17.0.0 document read 2026-11-16 spell them.
const (
	giteaFailedJobsPath = `"/repos/{owner}/{repo}/actions/runs/{run}/rerun-failed-jobs":{"post":{"operationId":"rerunFailedWorkflowRun"}}`
	giteaWholeRunPath   = `"/repos/{owner}/{repo}/actions/runs/{run}/rerun":{"post":{"operationId":"rerunWorkflowRun"}}`
	forgejoWholeRunPath = `"/repos/{owner}/{repo}/actions/runs/{run_id}/rerun":{"post":{"operationId":"RerunActionRun"}}`
	forgejoJobRerunPath = `"/repos/{owner}/{repo}/actions/jobs/{job_id}/rerun":{"post":{"operationId":"repoRerunActionJob"}}`
)

// Detection takes the failed-jobs route wherever the document declares it, Forgejo's
// exact whole-run route where it declares that alone, and no other path: Gitea's
// whole-run route and Forgejo's per-job route are never read as a re-run of what
// failed.
func TestRerunDetectionTakesTheRouteTheDocumentDeclares(t *testing.T) {
	for _, test := range []struct {
		name, version, title, paths string
		want                        forgeapi.Support
		route                       string
	}{
		{"gitea_both", "1.27.0+dev", "Gitea API", giteaFailedJobsPath + "," + giteaWholeRunPath, forgeapi.SupportYes, "/rerun-failed-jobs"},
		{"gitea_whole_run_alone", "1.27.0+dev", "Gitea API", giteaWholeRunPath, forgeapi.SupportNo, ""},
		{"forgejo_17", "17.0.0-100-2254cb0+gitea-1.22.0", "Forgejo API", forgejoWholeRunPath + "," + forgejoJobRerunPath, forgeapi.SupportYes, "/rerun"},
		{"forgejo_job_route_alone", "17.0.0-100-2254cb0+gitea-1.22.0", "Forgejo API", forgejoJobRerunPath, forgeapi.SupportNo, ""},
		{"forgejo_16", "16.0.5+gitea-1.22.0", "Forgejo API", `"/repos/{owner}/{repo}/actions/runs":{"get":{}}`, forgeapi.SupportNo, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			routes := map[string]string{
				"GET /api/v1/version":  `{"version":"` + test.version + `"}`,
				"GET /swagger.v1.json": `{"info":{"title":"` + test.title + `"},"paths":{` + test.paths + `}}`,
			}
			if test.route != "" {
				routes["POST /api/v1/repos/example/example/actions/runs/5"+test.route] = ""
			}
			h := newHarness(t, routes)
			caps, err := h.client.ConnectionCaps(t.Context())
			if err != nil {
				t.Fatalf("ConnectionCaps = %v", err)
			}
			if got := caps.Caps[forgeapi.CapRerunChecks]; got != test.want {
				t.Fatalf("Caps[rerun_checks] = %s, want %s (evidence %q)", got, test.want, caps.Ev[forgeapi.CapRerunChecks].Detail)
			}
			if test.route == "" {
				return
			}
			if err := h.client.RerunFailedRun(t.Context(), testRef(), forgeapi.RunRef{ID: 5}); err != nil {
				t.Fatalf("RerunFailedRun = %v, want nil", err)
			}
			want := "POST /api/v1/repos/example/example/actions/runs/5" + test.route
			if sent := h.instance.arrived(); !slices.Contains(sent, want) {
				t.Errorf("RerunFailedRun sent %v, want %s", sent, want)
			}
		})
	}
}
