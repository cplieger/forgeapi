package conformance

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// ownedRepoNode is the canonical repository as a node of the owner's repositories
// connection the public GraphQL schema types for repositoryOwner.
var ownedRepoNode = map[string]any{
	"nameWithOwner": selector, "description": repoDesc, "url": repoWebURL,
	"isPrivate": false, "isArchived": false, "isFork": false, "updatedAt": updatedAt.Format("2006-01-02T15:04:05Z"),
	"defaultBranchRef": map[string]any{"name": "main"}, "hasIssuesEnabled": true, "viewerPermission": "WRITE",
	"mergeCommitAllowed": true, "squashMergeAllowed": true, "rebaseMergeAllowed": true,
}

// ownerDocument is the owner's repositories document's answer, owner null where the
// schema answers a login no user or organization holds.
func ownerDocument(t *testing.T, owner any) wire {
	t.Helper()
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"rateLimit":       map[string]any{"cost": 1, "limit": 5000, "remaining": remaining, "resetAt": "2026-01-02T00:00:00Z"},
		"repositoryOwner": owner,
	}})
	if err != nil {
		t.Fatalf("Setup: encoding the owner document: %v", err)
	}
	return wire{GraphQL: true, Status: http.StatusOK, Body: body}
}

// ownedRoutes is what each product's instance answers the owner-scoped repository
// listing for the canonical owner: the document on GitHub, the group route on GitLab,
// and the owner's route on the Gitea family, carrying the total that route states.
// The REST rows are the default case's own fixture rows.
func ownedRoutes(t *testing.T, e spec.Entry) []wire {
	t.Helper()
	routes := setupRoutes(t, e)
	if e.Product == spec.GitHub {
		return append(routes, ownerDocument(t, map[string]any{"repositories": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
			"nodes":    []any{ownedRepoNode},
		}}))
	}
	f, err := loadFixture(e.Product, e.Method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	rows := f.Routes[0]
	rows.Path = ownedRepoPath(e.Product, owner)
	rows.Headers = map[string]string{"Content-Type": "application/json", "X-Total-Count": "1"}
	if e.Product == spec.GitLab {
		rows.Body = withoutPermissions(t, rows.Body)
	}
	return append(routes, rows)
}

// withoutPermissions is GitLab's project rows as the group route presents them,
// Entities::Project rather than the Entities::ProjectWithAccess the default scope's
// route answers (lib/api/groups.rb, present_projects): the same row less its
// permissions object.
func withoutPermissions(t *testing.T, body json.RawMessage) json.RawMessage {
	t.Helper()
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("Setup: decoding the project rows: %v", err)
	}
	for _, row := range rows {
		delete(row, "permissions")
	}
	out, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("Setup: encoding the project rows: %v", err)
	}
	return out
}

// ownedRepoPath is the route the owner scope reads on a REST product.
func ownedRepoPath(p spec.Product, name string) string {
	if p == spec.GitLab {
		return "/api/v4/groups/" + name + "/projects"
	}
	return "/api/v1/users/" + name + "/repos"
}

// requireRepoOwnerArm fails a case whose entry names no owner scope on the listing.
func requireRepoOwnerArm(t *testing.T, p spec.Product) spec.Entry {
	t.Helper()
	e := requireEntry(t, p, "Repos.ListRepos")
	if pending(e) {
		t.Skip(pendingReason(e))
	}
	requireOwnerArm(t, e)
	return e
}

// TestTheOwnerScopeListsTheOwnersRepositories holds the repository listing's owner
// scope to one request reaching the route the table names, and to the row the default
// scope answers, with the clone URL on the table's terms.
func TestTheOwnerScopeListsTheOwnersRepositories(t *testing.T) {
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			e := requireRepoOwnerArm(t, p)
			got, requests, err := listAgainst(t, e, fixture{Routes: ownedRoutes(t, e)}, forgeapi.WithOwner(owner))
			if err != nil {
				t.Fatalf("ListRepos(WithOwner(%q)) on %s = error %v, want the owner's repositories", owner, p, err)
			}
			if len(requests) != 1 {
				t.Fatalf("ListRepos(WithOwner) on %s sent %d request(s) [%s], want 1", p, len(requests), describeRequests(requests))
			}
			if p == spec.GitHub {
				if body := searchText(requests[0].body); !strings.Contains(body, "RepoOwned") || !strings.Contains(body, owner) {
					t.Errorf("ListRepos(WithOwner) on %s posted %q, want the owner's repositories document naming %q", p, body, owner)
				}
			} else if requests[0].path != ownedRepoPath(p, owner) {
				t.Errorf("ListRepos(WithOwner) on %s reached %s, want %s", p, requests[0], ownedRepoPath(p, owner))
			}
			page := got.(forgeapi.Page[forgeapi.Repository])
			if len(page.Items) != 1 {
				t.Fatalf("ListRepos(WithOwner) on %s answered %d row(s), want 1", p, len(page.Items))
			}
			row := page.Items[0]
			if row.Ref != repoRef(p) {
				t.Errorf("ListRepos(WithOwner) on %s row Ref = %+v, want %+v", p, row.Ref, repoRef(p))
			}
			if _, departs := departureFor(&e, cloneUnderOwner); departs && row.CloneURL != repoWebURL+".git" {
				t.Errorf("ListRepos(WithOwner) on %s row CloneURL = %q, want the repository url with .git appended, as the table states", p, row.CloneURL)
			} else if !departs && row.CloneURL != repoCloneURL {
				t.Errorf("ListRepos(WithOwner) on %s row CloneURL = %q, want %q", p, row.CloneURL, repoCloneURL)
			}
			push := forgeapi.SupportYes
			if _, departs := departureFor(&e, canPushUnderGroup); departs {
				push = forgeapi.SupportUnknown
			}
			if got := row.Affordances.CanPush; got != push {
				t.Errorf("ListRepos(WithOwner) on %s row CanPush = %v, want %v", p, got, push)
			}
			if got := row.Affordances.DefaultBranch; got != targetBranch {
				t.Errorf("ListRepos(WithOwner) on %s row DefaultBranch = %q, want %q", p, got, targetBranch)
			}
		})
	}
}

// TestAnUnresolvedOwnerOnTheRepositoryListingAnswersAsTheTableStates holds the owner
// the instance does not resolve: owner_unresolved, a not-found refusal carrying the
// status the instance answered, and on GitLab only after the user route answered 404
// too, where an owner the user route answers with an empty list is the table's row.
func TestAnUnresolvedOwnerOnTheRepositoryListingAnswersAsTheTableStates(t *testing.T) {
	notFoundOn := func(path string) wire {
		return wire{Method: http.MethodGet, Path: path, Status: http.StatusNotFound, Body: json.RawMessage(notFound)}
	}
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			e := requireRepoOwnerArm(t, p)
			routes := setupRoutes(t, e)
			status, want := http.StatusNotFound, 1
			switch p {
			case spec.GitHub:
				routes, status = append(routes, ownerDocument(t, nil)), http.StatusOK
			case spec.GitLab:
				routes, want = append(routes, notFoundOn(ownedRepoPath(p, unresolvableOwner)), notFoundOn("/api/v4/users/"+unresolvableOwner+"/projects")), 2
			default:
				routes = append(routes, notFoundOn(ownedRepoPath(p, unresolvableOwner)))
			}
			_, requests, err := listAgainst(t, e, fixture{Routes: routes}, forgeapi.WithOwner(unresolvableOwner))
			if len(requests) != want {
				t.Errorf("ListRepos(WithOwner(an unresolved owner)) on %s sent %d request(s) [%s], want %d", p, len(requests), describeRequests(requests), want)
			}
			checkOwnerUnresolved(t, e, e.Method, err, status)
		})
	}
	t.Run("GitLab_a_private_profile", func(t *testing.T) {
		e := requireRepoOwnerArm(t, spec.GitLab)
		d, departs := departureFor(&e, ownerUnresolved)
		if !departs || d.Kind != spec.CannotSupply {
			t.Skip("the table states no private-profile answer on GitLab")
		}
		routes := append(setupRoutes(t, e), notFoundOn(ownedRepoPath(spec.GitLab, owner)),
			wire{Method: http.MethodGet, Path: "/api/v4/users/" + owner + "/projects", Status: http.StatusOK, Body: json.RawMessage(`[]`)})
		got, _, err := listAgainst(t, e, fixture{Routes: routes}, forgeapi.WithOwner(owner))
		if err != nil || len(got.(forgeapi.Page[forgeapi.Repository]).Items) != 0 {
			t.Errorf("ListRepos(WithOwner(a private-profile user)) on GitLab = %v, error %v, want an empty page and no error: %s", got, err, d.Says)
		}
	})
}

// TestTheCrossRepositoryListsLeaveTheArchivedDefaultToTheProduct holds what decides
// whether an archived repository's rows are listed: no request sends an archived
// filter, so each product's own default answers, which the table states per product.
func TestTheCrossRepositoryListsLeaveTheArchivedDefaultToTheProduct(t *testing.T) {
	for _, method := range crossRepositoryLists {
		for _, p := range spec.Products {
			for scope, opts := range map[string][]forgeapi.ListOption{"viewer": nil, "owner": {forgeapi.WithOwner(owner)}} {
				t.Run(string(p)+"_"+method+"_"+scope, func(t *testing.T) {
					e := requireEntry(t, p, method)
					if pending(e) {
						t.Skip(pendingReason(e))
					}
					_, requests, err := listFixture(t, e, opts...)
					if err != nil {
						t.Fatalf("%s on %s under the %s scope = error %v, want the page", method, p, scope, err)
					}
					for _, req := range requests {
						for _, name := range []string{"archived", "non_archived"} {
							if req.query.Has(name) {
								t.Errorf("%s on %s sent %s=%q, want no archived filter: the product's default is what the table states", method, p, name, req.query.Get(name))
							}
						}
						if strings.Contains(searchText(req.body), "archived:") {
							t.Errorf("%s on %s sent a search carrying an archived qualifier, want none", method, p)
						}
					}
				})
			}
		}
	}
}
