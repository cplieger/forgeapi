package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// labelPR is the pull request a label change addresses in these cases, numbered
// apart from the canonical issue so a request names which one it reached.
const labelPR = 7

// labelChange is one of the two label writes, driven on a client.
type labelChange func(ctx context.Context, c forgeapi.Labels, repo forgeapi.RepoRef, target forgeapi.LabelTarget, names []string) error

var labelChanges = map[string]labelChange{
	"Labels.AddLabels": func(ctx context.Context, c forgeapi.Labels, repo forgeapi.RepoRef, target forgeapi.LabelTarget, names []string) error {
		return c.AddLabels(ctx, repo, target, names)
	},
	"Labels.RemoveLabels": func(ctx context.Context, c forgeapi.Labels, repo forgeapi.RepoRef, target forgeapi.LabelTarget, names []string) error {
		return c.RemoveLabels(ctx, repo, target, names)
	},
}

// labelCall drives one label change on an instance serving routes, the product's
// connection setup first where its family has one, and answers the error and the
// requests the change sent.
func labelCall(t *testing.T, p spec.Product, method string, target forgeapi.LabelTarget, names []string, routes ...wire) ([]sent, *recorder, error) {
	t.Helper()
	srv, rec := newFixtureServer(t, fixture{path: "inline", Routes: append(routes, setupRoutes(t, requireEntry(t, p, method))...)})
	client := pagedClient(t, p, srv)
	if family(p) == forgeapi.FamilyGitea {
		if _, err := client.(forgeapi.Capabilities).ConnectionCaps(t.Context()); err != nil {
			t.Fatalf("Setup: ConnectionCaps on %s: %v", p, err)
		}
	}
	before := rec.count()
	err := labelChanges[method](t.Context(), client.(forgeapi.Labels), repoRef(p), target, names)
	return rec.since(before), rec, err
}

func jsonBody(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return b
}

// labelRows is a label page of n rows, none of them the canonical label.
func labelRows(n int) []map[string]any {
	rows := make([]map[string]any, 0, n)
	for i := range n {
		rows = append(rows, map[string]any{"id": i + 100, "name": fmt.Sprintf("other-%d", i), "color": "ededed"})
	}
	return rows
}

// labelReadPath is the route an addition reads the repository's labels on.
func labelReadPath(p spec.Product) string {
	switch p {
	case spec.GitHub:
		return "/repos/" + selector + "/labels"
	case spec.GitLab:
		return "/api/v4/projects/example%2Fexample/labels"
	}
	return giteaRepos + selector + "/labels"
}

// itemPath is the route a label change on the pull request labelPR writes to: the
// merge request on GitLab, the issue route the pull request shares elsewhere.
func itemPath(p spec.Product) string {
	switch p {
	case spec.GitHub:
		return fmt.Sprintf("/repos/%s/issues/%d", selector, labelPR)
	case spec.GitLab:
		return fmt.Sprintf("/api/v4/projects/example%%2Fexample/merge_requests/%d", labelPR)
	}
	return fmt.Sprintf("%s%s/issues/%d", giteaRepos, selector, labelPR)
}

func writes(arrived []sent) []sent {
	var out []sent
	for _, req := range arrived {
		if req.method != http.MethodGet {
			out = append(out, req)
		}
	}
	return out
}

// A pull request's labels are changed at the route the product files it under: the
// merge request on GitLab, and the issue route the pull request's number shares on
// the other three.
func TestALabelChangeOnAPullRequestReachesThePullRequestsRoute(t *testing.T) {
	label := []map[string]any{{"id": 1, "name": labelName, "color": "ededed"}}
	for _, p := range spec.Products {
		pr := forgeapi.LabelTarget{PR: forgeapi.PRRef{Number: labelPR}}
		t.Run(string(p)+"_add", func(t *testing.T) {
			write := wire{Method: http.MethodPost, Path: itemPath(p) + "/labels", Status: http.StatusOK, Body: jsonBody(t, label)}
			if p == spec.GitLab {
				write = wire{Method: http.MethodPut, Path: itemPath(p), Status: http.StatusOK, Body: json.RawMessage(`{}`)}
			}
			arrived, rec, err := labelCall(t, p, "Labels.AddLabels", pr, []string{labelName},
				wire{Method: http.MethodGet, Path: labelReadPath(p), Status: http.StatusOK, Body: jsonBody(t, label)}, write)
			if err != nil || len(rec.misses()) > 0 {
				t.Fatalf("AddLabels on a pull request on %s = %v, unanswered %v, want nil at %s", p, err, rec.misses(), write.Path)
			}
			if w := writes(arrived); len(w) != 1 || w[0].path != write.Path {
				t.Errorf("AddLabels on a pull request on %s wrote %s, want one write to %s", p, describeRequests(w), write.Path)
			}
		})
		t.Run(string(p)+"_remove", func(t *testing.T) {
			routes := []wire{{Method: http.MethodDelete, Path: itemPath(p) + "/labels/" + labelName, Status: http.StatusOK, Body: json.RawMessage(`[]`)}}
			switch p {
			case spec.GitLab:
				routes = []wire{{Method: http.MethodPut, Path: itemPath(p), Status: http.StatusOK, Body: json.RawMessage(`{}`)}}
			case spec.Gitea:
				routes = []wire{
					{Method: http.MethodGet, Path: itemPath(p) + "/labels", Status: http.StatusOK, Body: jsonBody(t, label)},
					{Method: http.MethodDelete, Path: itemPath(p) + "/labels/1", Status: http.StatusNoContent, Body: json.RawMessage(`null`)},
				}
			case spec.Forgejo:
				routes = []wire{
					{Method: http.MethodGet, Path: itemPath(p) + "/labels", Status: http.StatusOK, Body: jsonBody(t, label)},
					{Method: http.MethodDelete, Path: itemPath(p) + "/labels/" + labelName, Status: http.StatusNoContent, Body: json.RawMessage(`null`)},
				}
			}
			arrived, rec, err := labelCall(t, p, "Labels.RemoveLabels", pr, []string{labelName}, routes...)
			if err != nil || len(rec.misses()) > 0 {
				t.Fatalf("RemoveLabels on a pull request on %s = %v, unanswered %v, want nil", p, err, rec.misses())
			}
			want := routes[len(routes)-1].Path
			if w := writes(arrived); len(w) != 1 || w[0].path != want {
				t.Errorf("RemoveLabels on a pull request on %s wrote %s, want one write to %s", p, describeRequests(w), want)
			}
		})
	}
}

// A target naming neither item, both, or a number below one is refused before any
// request with the code a family answers for an item it cannot address, and a change
// naming no label sends nothing and answers no error.
func TestALabelChangeIsRefusedOrSkippedBeforeAnyRequest(t *testing.T) {
	refused := map[string]forgeapi.LabelTarget{
		"neither":      {},
		"both":         {PR: forgeapi.PRRef{Number: 1}, Issue: forgeapi.IssueRef{Number: 1}},
		"negative_pr":  {PR: forgeapi.PRRef{Number: -1}},
		"negative_iss": {Issue: forgeapi.IssueRef{Number: -2}},
	}
	for _, p := range spec.Products {
		for method := range labelChanges {
			for name, target := range refused {
				t.Run(string(p)+"_"+method+"_"+name, func(t *testing.T) {
					arrived, _, err := labelCall(t, p, method, target, []string{labelName})
					var fe *forgeapi.Error
					if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefInvalid {
						t.Errorf("%s on %s with target %+v = %v, want code %q", method, p, target, err, forgeapi.CodeRepoRefInvalid)
					}
					if len(arrived) != 0 {
						t.Errorf("%s on %s with target %+v sent %s, want nothing", method, p, target, describeRequests(arrived))
					}
				})
			}
			t.Run(string(p)+"_"+method+"_no_names", func(t *testing.T) {
				arrived, _, err := labelCall(t, p, method, forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}, nil)
				if err != nil || len(arrived) != 0 {
					t.Errorf("%s on %s naming no label = %v after %s, want nil and nothing sent", method, p, err, describeRequests(arrived))
				}
			})
		}
	}
}

// labelPageMaximum is the most rows an addition's label read asks for: the REST page
// maximum on GitHub and GitLab, the stated maximum the Gitea-family setup serves.
var labelPageMaximum = map[spec.Product]int{spec.GitHub: 100, spec.GitLab: 100, spec.Gitea: 50, spec.Forgejo: 50}

// An addition never creates a label: a name the label page lacks is refused before
// anything is written, as a validation where the page was the whole list and as a
// truncated page where it came back full.
func TestAnAdditionOfAnUndefinedLabelWritesNothing(t *testing.T) {
	for _, p := range spec.Products {
		for name, tc := range map[string]struct {
			code string
			rows int
		}{
			"short_page": {rows: 2, code: forgeapi.CodeValidation},
			"full_page":  {rows: labelPageMaximum[p], code: forgeapi.CodeLabelPageTruncated},
		} {
			t.Run(string(p)+"_"+name, func(t *testing.T) {
				arrived, _, err := labelCall(t, p, "Labels.AddLabels", forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}, []string{labelName},
					wire{Method: http.MethodGet, Path: labelReadPath(p), Status: http.StatusOK, Body: jsonBody(t, labelRows(tc.rows))}, userOwner)
				var fe *forgeapi.Error
				if !asForgeError(err, &fe) || fe.Code != tc.code || !strings.Contains(fe.Message, labelName) {
					t.Errorf("AddLabels of an undefined label on %s over %d rows = %v, want code %q naming %q", p, tc.rows, err, tc.code, labelName)
				}
				if w := writes(arrived); len(w) != 0 {
					t.Errorf("AddLabels of an undefined label on %s wrote %s, want nothing written", p, describeRequests(w))
				}
			})
		}
	}
}

// userOwner is the Gitea family's answer to the organization label read for a
// repository a user owns, which the other products never send; it is matched by
// path, so it answers nothing they do send.
var userOwner = wire{Method: http.MethodGet, Path: giteaOrgLabels, Status: http.StatusNotFound, Body: json.RawMessage(`{"message":"GetOrgByName"}`)}

// giteaOrgLabels is the label page of the organization owning the canonical
// repository, as the Gitea family reads it.
const giteaOrgLabels = "/api/v1/orgs/example/labels"

// Both Gitea-family products' label option takes an organization label's id
// (orgListLabels; routers/api/v1/repo/issue_label.go, prepareForReplaceOrAdd), so a
// name the repository's page lacks is resolved on its organization's page, and on a
// repository a user owns, whose organization read answers 404, it stays refused.
func TestAGiteaFamilyAdditionResolvesAnOrganizationLabel(t *testing.T) {
	repoPage := func(t *testing.T, p spec.Product) wire {
		return wire{Method: http.MethodGet, Path: labelReadPath(p), Status: http.StatusOK, Body: jsonBody(t, labelRows(2))}
	}
	for _, p := range giteaFamilyProducts {
		t.Run(string(p)+"_an_organization_label", func(t *testing.T) {
			orgRows := jsonBody(t, []map[string]any{{"id": 71, "name": labelName, "color": "ededed"}})
			org := wire{Method: http.MethodGet, Path: giteaOrgLabels, Status: http.StatusOK, Body: orgRows}
			arrived, _, err := labelCall(t, p, "Labels.AddLabels", forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}, []string{labelName}, repoPage(t, p), org,
				wire{Method: http.MethodPost, Path: "/api/v1/repos/example/example/issues/1/labels", Status: http.StatusOK, Body: json.RawMessage(`[]`)})
			if err != nil {
				t.Fatalf("AddLabels of an organization label on %s = %v, want nil", p, err)
			}
			w := writes(arrived)
			if len(w) != 1 || !strings.Contains(string(w[0].body), "71") {
				t.Errorf("AddLabels of an organization label on %s wrote %s, want one write carrying its id 71", p, describeRequests(w))
			}
		})
		t.Run(string(p)+"_a_user_owned_repository", func(t *testing.T) {
			arrived, _, err := labelCall(t, p, "Labels.AddLabels", forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}, []string{labelName}, repoPage(t, p), userOwner)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeValidation || strings.Contains(fe.Message, "organization") {
				t.Errorf("AddLabels on a user's repository on %s = %v, want code %q naming the repository's page alone", p, err, forgeapi.CodeValidation)
			}
			if w := writes(arrived); len(w) != 0 {
				t.Errorf("AddLabels on a user's repository on %s wrote %s, want nothing", p, describeRequests(w))
			}
		})
	}
}

// GitHub's removal answers 404 for a label the item lacks and for a missing item
// alike, so the first 404 reads the item once: found makes it and every later 404 a
// name not carried, and not found is that read's own refusal.
func TestAGitHubRemovalOfALabelNotCarriedReadsTheItemOnce(t *testing.T) {
	issue := fmt.Sprintf("/repos/%s/issues/1", selector)
	missing := wire{Method: http.MethodDelete, Path: issue + "/labels/{}", Status: http.StatusNotFound, Repeat: 2, Body: json.RawMessage(`{"message":"Label does not exist"}`)}
	t.Run("item_found", func(t *testing.T) {
		arrived, _, err := labelCall(t, spec.GitHub, "Labels.RemoveLabels", forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}, []string{"a", "b"},
			missing, wire{Method: http.MethodGet, Path: issue, Status: http.StatusOK, Body: json.RawMessage(`{"number":1}`)})
		if err != nil {
			t.Errorf("RemoveLabels of two names the item lacks on GitHub = %v, want nil", err)
		}
		if len(arrived) != 3 || arrived[1].method != http.MethodGet {
			t.Errorf("RemoveLabels of two names the item lacks on GitHub sent %s, want DELETE, the item read, DELETE", describeRequests(arrived))
		}
	})
	t.Run("item_missing", func(t *testing.T) {
		arrived, _, err := labelCall(t, spec.GitHub, "Labels.RemoveLabels", forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}, []string{"a", "b"},
			missing, wire{Method: http.MethodGet, Path: issue, Status: http.StatusNotFound, Body: json.RawMessage(`{"message":"Not Found"}`)})
		var fe *forgeapi.Error
		if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindNotFound {
			t.Errorf("RemoveLabels on a missing item on GitHub = %v, want the item read's not-found refusal", err)
		}
		if len(arrived) != 2 {
			t.Errorf("RemoveLabels on a missing item on GitHub sent %s, want the DELETE and the item read", describeRequests(arrived))
		}
	})
}

// A removal refused after an earlier name was removed leaves that removal in place,
// and its message names it.
func TestARemovalRefusedPartwayNamesTheLabelsItRemoved(t *testing.T) {
	carried := []map[string]any{{"id": 1, "name": "a"}, {"id": 2, "name": "b"}}
	refusal := json.RawMessage(`{"message":"Forbidden"}`)
	for _, p := range []spec.Product{spec.GitHub, spec.Gitea, spec.Forgejo} {
		t.Run(string(p), func(t *testing.T) {
			base := fmt.Sprintf("/repos/%s/issues/1/labels/", selector)
			first, second := "a", "b"
			var routes []wire
			if p != spec.GitHub {
				base = giteaRepos + selector + "/issues/1/labels/"
				routes = append(routes, wire{Method: http.MethodGet, Path: strings.TrimSuffix(base, "/"), Status: http.StatusOK, Body: jsonBody(t, carried)})
				if p == spec.Gitea {
					first, second = "1", "2"
				}
			}
			routes = append(routes,
				wire{Method: http.MethodDelete, Path: base + first, Status: http.StatusOK, Body: json.RawMessage(`[]`)},
				wire{Method: http.MethodDelete, Path: base + second, Status: http.StatusForbidden, Body: refusal})
			_, rec, err := labelCall(t, p, "Labels.RemoveLabels", forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}, []string{"a", "b"}, routes...)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindForbidden || !strings.Contains(fe.Message, "removed before this refusal: a") {
				t.Errorf("RemoveLabels refused at its second name on %s = %v, want the refusal naming label a as removed", p, err)
			}
			if strings.Contains(fe.Message, ": a, b") {
				t.Errorf("RemoveLabels refused at its second name on %s names b as removed: %q", p, fe.Message)
			}
			if len(rec.misses()) > 0 {
				t.Errorf("RemoveLabels on %s reached unanswered routes %v", p, rec.misses())
			}
		})
	}
}

// On the Gitea family a removal reads the labels the item carries and removes only
// those, by id on Gitea and by its escaped name on Forgejo, and a label or item gone
// since the read is a name not carried.
func TestAGiteaFamilyRemovalRemovesOnlyWhatTheItemCarries(t *testing.T) {
	carried := []map[string]any{{"id": 9, "name": "area/ui"}}
	labels := giteaRepos + selector + "/issues/1/labels"
	for p, segment := range map[spec.Product]string{spec.Gitea: "9", spec.Forgejo: "area%2Fui"} {
		t.Run(string(p), func(t *testing.T) {
			arrived, rec, err := labelCall(t, p, "Labels.RemoveLabels", forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}, []string{"absent", "area/ui", "area/ui"},
				wire{Method: http.MethodGet, Path: labels, Status: http.StatusOK, Body: jsonBody(t, carried)},
				wire{Method: http.MethodDelete, Path: labels + "/" + segment, Status: http.StatusNoContent, Body: json.RawMessage(`null`)})
			if err != nil || len(rec.misses()) > 0 {
				t.Fatalf("RemoveLabels on %s = %v, unanswered %v, want nil", p, err, rec.misses())
			}
			if w := writes(arrived); len(w) != 1 || w[0].path != labels+"/"+segment {
				t.Errorf("RemoveLabels on %s wrote %s, want one DELETE of %s for the one carried name", p, describeRequests(w), segment)
			}
		})
		for _, status := range []int{http.StatusNotFound, http.StatusUnprocessableEntity} {
			t.Run(fmt.Sprintf("%s_gone_%d", p, status), func(t *testing.T) {
				_, _, err := labelCall(t, p, "Labels.RemoveLabels", forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}, []string{"area/ui"},
					wire{Method: http.MethodGet, Path: labels, Status: http.StatusOK, Body: jsonBody(t, carried)},
					wire{Method: http.MethodDelete, Path: labels + "/" + segment, Status: status, Body: json.RawMessage(`{"message":"gone"}`)})
				if err != nil {
					t.Errorf("RemoveLabels of a label gone since the read on %s, answered %d, = %v, want nil", p, status, err)
				}
			})
		}
	}
}

// sharedLabelName is the table's field for a removal of a name a repository and its
// organization both define.
const sharedLabelName = "removal, a name the repository and its organization share"

// An item can carry a repository label and an organization label of one name. Gitea
// removes each by its id; Forgejo's removal by name reaches the repository's label
// alone, which its departure row states, and an id would reach a repository label
// only, so it sends the name once.
func TestAGiteaFamilyRemovalOfANameTwoLabelsShare(t *testing.T) {
	carried := []map[string]any{{"id": 9, "name": "shared"}, {"id": 71, "name": "shared"}}
	labels := giteaRepos + selector + "/issues/1/labels"
	for p, segments := range map[spec.Product][]string{spec.Gitea: {"9", "71"}, spec.Forgejo: {"shared"}} {
		t.Run(string(p), func(t *testing.T) {
			if p == spec.Forgejo {
				e := requireEntry(t, p, "Labels.RemoveLabels")
				if d, ok := departureFor(&e, sharedLabelName); !ok || d.Kind != spec.CannotSupply {
					t.Errorf("RemoveLabels on %s states no %q departure, want the cannot-supply row", p, sharedLabelName)
				}
			}
			routes := []wire{{Method: http.MethodGet, Path: labels, Status: http.StatusOK, Body: jsonBody(t, carried)}}
			want := make([]string, 0, len(segments))
			for _, segment := range segments {
				routes = append(routes, wire{Method: http.MethodDelete, Path: labels + "/" + segment, Status: http.StatusNoContent, Body: json.RawMessage(`null`)})
				want = append(want, http.MethodDelete+" "+labels+"/"+segment)
			}
			arrived, _, err := labelCall(t, p, "Labels.RemoveLabels", forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}, []string{"shared"}, routes...)
			if err != nil {
				t.Fatalf("RemoveLabels on %s = %v, want nil", p, err)
			}
			got := make([]string, 0, len(segments))
			for _, w := range writes(arrived) {
				got = append(got, w.String())
			}
			if !slices.Equal(got, want) {
				t.Errorf("RemoveLabels of a shared name on %s wrote %v, want %v", p, got, want)
			}
		})
	}
}

// A read-only client refuses both label changes before anything is sent, the label
// read an addition or a removal makes first included.
func TestALabelChangeOnAReadOnlyClientSendsNothing(t *testing.T) {
	for _, p := range spec.Products {
		for method, change := range labelChanges {
			t.Run(string(p)+"_"+method, func(t *testing.T) {
				srv, rec := newFixtureServer(t, fixture{path: "inline"})
				client := pagedClient(t, p, srv, forgeapi.WithMutations(false))
				err := change(t.Context(), client.(forgeapi.Labels), repoRef(p), forgeapi.LabelTarget{Issue: forgeapi.IssueRef{Number: 1}}, []string{labelName})
				var fe *forgeapi.Error
				if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeMutationsDisabled {
					t.Errorf("%s on a read-only %s client = %v, want code %q", method, p, err, forgeapi.CodeMutationsDisabled)
				}
				if n := rec.count(); n != 0 {
					t.Errorf("%s on a read-only %s client sent %s, want nothing", method, p, strings.Join(rec.requests(), ", "))
				}
			})
		}
	}
}
