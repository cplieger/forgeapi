package conformance

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// withBodyField is w with one top-level body key set to value.
func withBodyField(t *testing.T, w wire, key string, value any) wire {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body, &body); err != nil {
		t.Fatalf("Setup: decoding the %s %s body: %v", w.Method, w.Path, err)
	}
	body[key] = value
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Setup: re-encoding the %s %s body: %v", w.Method, w.Path, err)
	}
	w.Body = encoded
	return w
}

// A merge on GitLab asking for the branch's commits to survive keeps them or is
// refused before any write. The merge route sets the merge request's squash flag only
// on true, so a flag the merge request already carries still squashes, and a project
// whose squash_option is always squashes whatever is sent (lib/api/merge_requests.rb
// build_merge_params; app/models/merge_request.rb squash_on_merge?).
func TestAGitLabMergeKeepingTheCommitsKeepsThemOrIsRefusedBeforeAnyWrite(t *testing.T) {
	const (
		project = "GET /api/v4/projects/example%2Fexample"
		read    = "GET /api/v4/projects/example%2Fexample/merge_requests/1"
		update  = "PUT /api/v4/projects/example%2Fexample/merge_requests/1"
		merge   = "PUT /api/v4/projects/example%2Fexample/merge_requests/1/merge"
	)
	for _, tc := range []struct {
		name    string
		option  string
		squash  bool
		want    []string
		refused bool
	}{
		{name: "a_project_that_always_squashes", option: "always", want: []string{project}, refused: true},
		{name: "a_merge_request_flagged_to_squash", option: "default_on", squash: true, want: []string{project, read, update, merge}},
		{name: "a_merge_request_not_flagged", option: "default_off", want: []string{project, read, merge}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := loadFixture(spec.GitLab, "Merges.MergePR")
			if err != nil || len(f.Routes) != 3 {
				t.Fatalf("Setup: GitLab's merge fixture: %d route(s), error %v, want the project, the merge request and the merge", len(f.Routes), err)
			}
			f.Routes[0] = withBodyField(t, f.Routes[0], "squash_option", tc.option)
			f.Routes[1] = withBodyField(t, f.Routes[1], "squash", tc.squash)
			updated := withBodyField(t, f.Routes[1], "squash", false)
			updated.Method = http.MethodPut
			f.Routes = append(f.Routes, updated)
			srv, rec := newFixtureServer(t, f)
			conn := forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL + apiRoot(spec.GitLab)}
			client, err := guard(func() (any, error) { return newClient(spec.GitLab, conn, offlineOptions(srv)...) })
			if err != nil {
				t.Fatalf("Setup: GitLab client: %v", err)
			}
			s := canonicalSubject(spec.GitLab)
			_, err = guard(func() (any, error) {
				return client.(forgeapi.Merges).MergePR(t.Context(), s.repo, s.pr, forgeapi.MergeRequest{Intent: forgeapi.IntentNoSquash, HeadSHA: s.ref})
			})
			arrived := rec.since(0)
			got := make([]string, 0, len(arrived))
			for _, r := range arrived {
				got = append(got, r.String())
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("MergePR at IntentNoSquash on GitLab sent %v, want %v", got, tc.want)
			}
			if tc.refused {
				var fe *forgeapi.Error
				if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeStrategyNotAllowed || fe.Kind != forgeapi.KindNotMergeable {
					t.Errorf("MergePR at IntentNoSquash on a project that always squashes = %v, want (%q, %v)", err, forgeapi.CodeStrategyNotAllowed, forgeapi.KindNotMergeable)
				}
				return
			}
			if err != nil {
				t.Fatalf("MergePR at IntentNoSquash on GitLab = %v, want the merge", err)
			}
			for _, r := range arrived {
				if r.method != http.MethodPut {
					continue
				}
				if v, _ := bodyField(r.body, "squash"); string(v) != "false" {
					t.Errorf("MergePR at IntentNoSquash on GitLab sent squash %s in %s, want false", v, r)
				}
			}
		})
	}
}
