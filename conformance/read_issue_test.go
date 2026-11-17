package conformance

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// sharedNumberSpace is each product whose issue route answers a pull request's
// number with that pull request's record, and the pull-request key the record then
// carries, as each API description gives it: GitHub's issue schema pull_request
// object and the Gitea family's Issue.pull_request PullRequestMeta, recorded on
// gitea.com and codeberg.org 2026-11-16. GitLab numbers merge requests apart.
var sharedNumberSpace = map[spec.Product]map[string]any{
	spec.GitHub:  {"url": "https://forge.example/repos/example/example/pulls/1"},
	spec.Gitea:   {"merged": false, "draft": false},
	spec.Forgejo: {"merged": false, "draft": false},
}

// A number that names a pull request is no issue, so the issue read answers not
// found with no code rather than the pull request's record as an issue.
func TestAnIssueReadOfAPullRequestsNumberIsNotFound(t *testing.T) {
	for p, key := range sharedNumberSpace {
		e := requireEntry(t, p, "Issues.ReadIssue")
		t.Run(caseName(e), func(t *testing.T) {
			f, err := loadFixture(p, e.Method)
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			record := map[string]any{}
			if err := json.Unmarshal(f.Routes[0].Body, &record); err != nil {
				t.Fatalf("Setup: %s's record is not a JSON object: %v", f.path, err)
			}
			record["pull_request"] = key
			if f.Routes[0].Body, err = json.Marshal(record); err != nil {
				t.Fatalf("Setup: encoding %s: %v", f.path, err)
			}
			srv, rec := newFixtureServer(t, f)
			client := pagedClient(t, p, srv)
			s := canonicalSubject(p)

			_, err = client.(forgeapi.Issues).ReadIssue(t.Context(), s.repo, s.issue)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindNotFound || fe.Code != "" {
				t.Errorf("ReadIssue of a pull request's number on %s = %v, want KindNotFound with no code", p, err)
			}
			if n := rec.count(); n != 1 {
				t.Errorf("ReadIssue of a pull request's number on %s sent %d request(s), want the one read", p, n)
			}
		})
	}
}
