package conformance

import (
	"net/http"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// TestADuplicatePullRequestIsAValidationRefusalOnEveryProduct holds one cause to one
// code across families: a creation naming a head and base an open pull request already
// joins is a request upstream rejected as invalid, `CodeValidation`'s own definition,
// whatever status each product answers it with. Each body is the one the product
// answered on 2026-10-04 for the seed branch into `main`: GitHub's sandbox 422, GitLab's
// sandbox 409, and 409 on local Gitea 28.0.0, Forgejo 16.0.5 and Forgejo 15.0.9.
func TestADuplicatePullRequestIsAValidationRefusalOnEveryProduct(t *testing.T) {
	const gitea = `{"message":"pull request already exists for these targets [id: 1, issue_id: 1, head_repo_id: 1, base_repo_id: 1, head_branch: seed, base_branch: main]","url":"http://127.0.0.1:3101/api/swagger"}`
	for _, test := range []struct {
		product spec.Product
		body    string
		status  int
	}{
		{
			product: spec.GitHub, status: http.StatusUnprocessableEntity,
			body: `{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"A pull request already exists for cplieger-bot:seed."}],"documentation_url":"https://docs.github.com/rest/pulls/pulls#create-a-pull-request","status":"422"}`,
		},
		{
			product: spec.GitLab, status: http.StatusConflict,
			body: `{"message":["Another open merge request already exists for this source branch: !11"]}`,
		},
		{product: spec.Gitea, status: http.StatusConflict, body: gitea},
		{product: spec.Forgejo, status: http.StatusConflict, body: gitea},
	} {
		t.Run(string(test.product), func(t *testing.T) {
			fe := forgeErrorOf(t, "PullRequests.CreatePR", refusedOn(t, test.product, "PullRequests.CreatePR", test.status, test.body))
			if fe.Code != forgeapi.CodeValidation {
				t.Errorf("CreatePR on %s answered the duplicate's %d = %v / %q, want code %q", test.product, test.status, fe.Kind, fe.Code, forgeapi.CodeValidation)
			}
			if fe.Status != test.status {
				t.Errorf("CreatePR on %s = status %d, want %d", test.product, fe.Status, test.status)
			}
		})
	}
}

// TestAGiteaFamilyMerge409NamesNoCause holds the merge route's 409 on the family that
// answers it for a head that moved ("head out of date", measured on all three local
// instances) and for a merge that conflicts: the body names the cause in human text
// alone, so the code is the one for a refusal whose cause upstream did not name.
func TestAGiteaFamilyMerge409NamesNoCause(t *testing.T) {
	for _, product := range []spec.Product{spec.Gitea, spec.Forgejo} {
		t.Run(string(product), func(t *testing.T) {
			fe := forgeErrorOf(t, "Merges.MergePR", refusedOn(t, product, "Merges.MergePR", http.StatusConflict,
				`{"message":"head out of date","url":"http://127.0.0.1:3102/api/swagger"}`))
			if fe.Kind != forgeapi.KindConflict || fe.Code != forgeapi.CodeNotMergeable {
				t.Errorf("MergePR on %s answered 409 head out of date = %v / %q, want %v / %q", product, fe.Kind, fe.Code,
					forgeapi.KindConflict, forgeapi.CodeNotMergeable)
			}
		})
	}
}
