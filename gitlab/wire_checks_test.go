package gitlab

import (
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

// withHeadPipeline is restMergeRequestBody on head with the stored head pipeline
// pipeline in place of its own.
func withHeadPipeline(t *testing.T, head, pipeline string) string {
	t.Helper()
	const recorded = `"head_pipeline": {"id": 1, "sha": "` + testHeadSHA + `", "status": "success"}`
	if !strings.Contains(restMergeRequestBody, recorded) {
		t.Fatal("Setup: restMergeRequestBody carries no head pipeline to replace")
	}
	body := strings.Replace(restMergeRequestBody, recorded, `"head_pipeline": `+pipeline, 1)
	return strings.Replace(body, `"sha": "`+testHeadSHA+`"`, `"sha": "`+head+`"`, 1)
}

// The REST merge request answers its STORED head pipeline, kept after a push until
// one of the new head exists, so every REST path reads its status only where that
// pipeline ran on the head, and answers CheckUnknown for one on another commit.
func TestARESTMergeRequestReadsOnlyAHeadPipelineOnItsHead(t *testing.T) {
	const other = "0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f60718293"
	stored := func(sha, ref, status string) string {
		return `{"id": 77, "project_id": 1, "sha": "` + sha + `", "ref": "` + ref + `", "status": "` + status + `"}`
	}
	paths := []struct {
		read func(t *testing.T, body string) forgeapi.PullRequest
		name string
	}{
		{name: "ClosePR", read: func(t *testing.T, body string) forgeapi.PullRequest {
			h := newHarness(t, map[string]string{pullStateRoute: body})
			pr, err := h.client.ClosePR(t.Context(), testRef(), testPR())
			if err != nil {
				t.Fatalf("ClosePR = %v, want the pull request", err)
			}
			return pr
		}},
		{name: "ReopenPR", read: func(t *testing.T, body string) forgeapi.PullRequest {
			h := newHarness(t, map[string]string{pullStateRoute: body})
			pr, err := h.client.ReopenPR(t.Context(), testRef(), testPR())
			if err != nil {
				t.Fatalf("ReopenPR = %v, want the pull request", err)
			}
			return pr
		}},
		{name: "degraded_ReadPR", read: func(t *testing.T, body string) forgeapi.PullRequest {
			h := newHarness(t, map[string]string{metadataRoute: metadataBody, documentRoute: docTooComplex, readRoute: body})
			h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
			if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
				t.Fatalf("Setup: ConnectionCaps = %v", err)
			}
			if !h.client.isDegraded() {
				t.Fatal("Setup: the refused schema question left the connection undegraded")
			}
			pr, err := h.client.ReadPR(t.Context(), testRef(), testPR())
			if err != nil {
				t.Fatalf("ReadPR = %v, want the pull request", err)
			}
			return pr
		}},
	}
	for _, path := range paths {
		for _, test := range []struct {
			name, head, pipeline string
			want                 forgeapi.CheckState
		}{
			{name: "a_failed_pipeline_on_the_head", head: testHeadSHA, pipeline: stored(testHeadSHA, "example-feature", "failed"), want: forgeapi.CheckFailing},
			{name: "a_running_pipeline_on_the_head", head: testHeadSHA, pipeline: stored(testHeadSHA, "example-feature", "running"), want: forgeapi.CheckPending},
			{name: "an_earlier_commits_failed_pipeline", head: testHeadSHA, pipeline: stored(other, "example-feature", "failed"), want: forgeapi.CheckUnknown},
			{name: "a_stored_merged_results_pipeline", head: testHeadSHA, pipeline: stored(other, "refs/merge-requests/1/merge", "failed"), want: forgeapi.CheckUnknown},
			{name: "no_head_pipeline", head: testHeadSHA, pipeline: "null", want: forgeapi.CheckUnknown},
			{name: "a_merge_request_with_no_head", pipeline: stored("", "example-feature", "failed"), want: forgeapi.CheckUnknown},
		} {
			t.Run(path.name+"_"+test.name, func(t *testing.T) {
				pr := path.read(t, withHeadPipeline(t, test.head, test.pipeline))
				if pr.Action.Checks != test.want {
					t.Errorf("%s = Action.Checks %v, want %v", path.name, pr.Action.Checks, test.want)
				}
			})
		}
	}
}
