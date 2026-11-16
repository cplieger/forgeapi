package conformance

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// cancelPermissionRefused is the table's field for what GitLab's deprecated cancel
// route answers a caller who may not cancel.
const cancelPermissionRefused = "error, permission refused, on cancel_merge_when_pipeline_succeeds"

// The routes the cancel reaches on the canonical subject.
const (
	githubPull       = "/repos/" + selector + "/pulls/1"
	gitlabMR         = "/api/v4/projects/example%2Fexample/merge_requests/1"
	gitlabCancel     = gitlabMR + "/cancel_auto_merge"
	gitlabDeprecated = gitlabMR + "/cancel_merge_when_pipeline_succeeds"
	giteaPull        = giteaRepos + selector + "/pulls/1"
	giteaCancel      = giteaPull + "/merge"
)

// cancelOn drives CancelAutoMerge on an instance serving routes, after GitLab's
// connection setup where version is not empty, and answers what the cancel alone
// sent and its error.
func cancelOn(t *testing.T, p spec.Product, version string, routes ...wire) ([]sent, *recorder, error) {
	t.Helper()
	if version != "" {
		routes = append(routes, gitlabSetup(t, version)...)
	}
	srv, rec := newFixtureServer(t, fixture{path: "inline", Routes: routes})
	client := pagedClient(t, p, srv)
	if version != "" {
		if _, err := client.(forgeapi.Capabilities).ConnectionCaps(t.Context()); err != nil {
			t.Fatalf("Setup: ConnectionCaps on %s: %v", p, err)
		}
	}
	before := rec.count()
	err := client.(forgeapi.Merges).CancelAutoMerge(t.Context(), repoRef(p), prRef(p))
	return rec.since(before), rec, err
}

// gitlabSetup is GitLab's connection setup, from the connection read's own fixture,
// with the metadata route reporting version.
func gitlabSetup(t *testing.T, version string) []wire {
	t.Helper()
	f, err := loadFixture(spec.GitLab, connectionRead)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	routes := slices.Clone(f.Routes)
	for i := range routes {
		if routes[i].Path != "/api/v4/metadata" {
			continue
		}
		meta := map[string]any{}
		if err := json.Unmarshal(routes[i].Body, &meta); err != nil {
			t.Fatalf("Setup: the metadata body is not a JSON object: %v", err)
		}
		meta["version"] = version
		routes[i].Body = jsonBody(t, meta)
	}
	return routes
}

// paths are the method and path of each request, in order.
func paths(arrived []sent) []string {
	out := make([]string, 0, len(arrived))
	for _, req := range arrived {
		out = append(out, req.String())
	}
	return out
}

// A pull request with nothing armed answers nil on every product, each read from the
// forge's own "nothing armed" answer as its API description gives it: GitHub's REST
// record with auto_merge null, which sends no disarm; GitLab's 409 on the current
// route and 201 with status error on the deprecated one; and the Gitea family's 404
// followed by the read finding the pull request.
func TestACancelWithNothingArmedIsNoRefusal(t *testing.T) {
	pull := json.RawMessage(`{"number":1,"node_id":"PR_kwDOexample","state":"open","auto_merge":null}`)
	for name, tc := range map[string]struct {
		product spec.Product
		version string
		routes  []wire
		want    []string
	}{
		"GitHub": {
			product: spec.GitHub,
			routes:  []wire{{Method: http.MethodGet, Path: githubPull, Status: http.StatusOK, Body: pull}},
			want:    []string{"GET " + githubPull},
		},
		"GitLab_current": {
			product: spec.GitLab, version: "19.5.0-ee",
			routes: []wire{{
				Method: http.MethodPost, Path: gitlabCancel, Status: http.StatusConflict,
				Body: json.RawMessage(`{"message":"Can't cancel the automatic merge"}`),
			}},
			want: []string{"POST " + gitlabCancel},
		},
		"GitLab_deprecated": {
			product: spec.GitLab,
			routes: []wire{{
				Method: http.MethodPost, Path: gitlabDeprecated, Status: http.StatusCreated,
				Body: json.RawMessage(`{"message":"Can't cancel the automatic merge","status":"error","http_status":406}`),
			}},
			want: []string{"POST " + gitlabDeprecated},
		},
		"Gitea": {
			product: spec.Gitea,
			routes: []wire{
				{Method: http.MethodDelete, Path: giteaCancel, Status: http.StatusNotFound, Body: json.RawMessage(notFound)},
				{Method: http.MethodGet, Path: giteaPull, Status: http.StatusOK, Body: json.RawMessage(`{"number":1,"state":"open"}`)},
			},
			want: []string{"DELETE " + giteaCancel, "GET " + giteaPull},
		},
		"Forgejo": {
			product: spec.Forgejo,
			routes: []wire{
				{Method: http.MethodDelete, Path: giteaCancel, Status: http.StatusNotFound, Body: json.RawMessage(notFound)},
				{Method: http.MethodGet, Path: giteaPull, Status: http.StatusOK, Body: json.RawMessage(`{"number":1,"state":"open"}`)},
			},
			want: []string{"DELETE " + giteaCancel, "GET " + giteaPull},
		},
	} {
		t.Run(name, func(t *testing.T) {
			arrived, rec, err := cancelOn(t, tc.product, tc.version, tc.routes...)
			if err != nil || len(rec.misses()) > 0 {
				t.Errorf("CancelAutoMerge with nothing armed on %s = %v, unanswered %v, want nil", tc.product, err, rec.misses())
			}
			if got := paths(arrived); !slices.Equal(got, tc.want) {
				t.Errorf("CancelAutoMerge with nothing armed on %s sent %v, want %v", tc.product, got, tc.want)
			}
		})
	}
}

// A pull request that does not exist answers KindNotFound with
// CodeRepoOrPRNotVisible on every product and on both GitLab routes, which is what
// every REST pull-request route of this library answers a 404 with.
func TestACancelOfAMissingPullRequestIsNotVisible(t *testing.T) {
	for name, tc := range map[string]struct {
		product spec.Product
		version string
		routes  []wire
	}{
		"GitHub": {product: spec.GitHub, routes: []wire{{Method: http.MethodGet, Path: githubPull, Status: http.StatusNotFound, Body: json.RawMessage(notFound)}}},
		"GitLab_current": {
			product: spec.GitLab, version: "19.6.0",
			routes: []wire{{Method: http.MethodPost, Path: gitlabCancel, Status: http.StatusNotFound, Body: json.RawMessage(notFound)}},
		},
		"GitLab_deprecated": {
			product: spec.GitLab,
			routes:  []wire{{Method: http.MethodPost, Path: gitlabDeprecated, Status: http.StatusNotFound, Body: json.RawMessage(notFound)}},
		},
		"Gitea": {product: spec.Gitea, routes: []wire{
			{Method: http.MethodDelete, Path: giteaCancel, Status: http.StatusNotFound, Body: json.RawMessage(notFound)},
			{Method: http.MethodGet, Path: giteaPull, Status: http.StatusNotFound, Body: json.RawMessage(notFound)},
		}},
		"Forgejo": {product: spec.Forgejo, routes: []wire{
			{Method: http.MethodDelete, Path: giteaCancel, Status: http.StatusNotFound, Body: json.RawMessage(notFound)},
			{Method: http.MethodGet, Path: giteaPull, Status: http.StatusNotFound, Body: json.RawMessage(notFound)},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			arrived, _, err := cancelOn(t, tc.product, tc.version, tc.routes...)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindNotFound || fe.Code != forgeapi.CodeRepoOrPRNotVisible {
				t.Errorf("CancelAutoMerge of a missing pull request on %s = %v, want KindNotFound with %q", tc.product, err, forgeapi.CodeRepoOrPRNotVisible)
			}
			if len(arrived) != len(tc.routes) {
				t.Errorf("CancelAutoMerge of a missing pull request on %s sent %v, want %d request(s)", tc.product, paths(arrived), len(tc.routes))
			}
		})
	}
}

// GitLab's current cancel route was introduced in 19.5, which deprecated the route
// answering a status hash, so the route follows the version connection setup read,
// and a connection holding no version, never set up or set up with the metadata read
// refused, sends the deprecated route every version serves.
func TestGitLabPicksTheCancelRouteByTheVersionSetupRead(t *testing.T) {
	current := wire{Method: http.MethodPost, Path: gitlabCancel, Status: http.StatusCreated, Body: json.RawMessage(`{"iid":1}`)}
	deprecated := wire{Method: http.MethodPost, Path: gitlabDeprecated, Status: http.StatusCreated, Body: json.RawMessage(`{"status":"success"}`)}
	for _, tc := range []struct {
		version string
		want    string
	}{
		{"19.5.0-ee", gitlabCancel},
		{"19.5.0-rc42", gitlabCancel},
		{"19.10.1", gitlabCancel},
		{"20.0.0", gitlabCancel},
		{"19.4.3-ee", gitlabDeprecated},
		{"18.4.0", gitlabDeprecated},
		{"", gitlabDeprecated},
	} {
		name := "version_" + tc.version
		if tc.version == "" {
			name = "no_setup"
		}
		t.Run(name, func(t *testing.T) {
			arrived, rec, err := cancelOn(t, spec.GitLab, tc.version, current, deprecated)
			if err != nil || len(rec.misses()) > 0 {
				t.Fatalf("CancelAutoMerge on GitLab %q = %v, unanswered %v, want nil", tc.version, err, rec.misses())
			}
			if got := paths(arrived); !slices.Equal(got, []string{"POST " + tc.want}) {
				t.Errorf("CancelAutoMerge on GitLab %q sent %v, want POST %s alone", tc.version, got, tc.want)
			}
		})
	}
	t.Run("metadata_refused", func(t *testing.T) {
		routes := gitlabSetup(t, "19.6.0")
		for i := range routes {
			if routes[i].Path == "/api/v4/metadata" {
				routes[i].Status, routes[i].Body = http.StatusUnauthorized, json.RawMessage(`{"message":"401 Unauthorized"}`)
			}
		}
		srv, rec := newFixtureServer(t, fixture{path: "inline", Routes: append(routes, current, deprecated)})
		client := pagedClient(t, spec.GitLab, srv)
		if _, err := client.(forgeapi.Capabilities).ConnectionCaps(t.Context()); err != nil {
			t.Fatalf("Setup: ConnectionCaps on GitLab with the metadata read refused: %v", err)
		}
		before := rec.count()
		if err := client.(forgeapi.Merges).CancelAutoMerge(t.Context(), repoRef(spec.GitLab), prRef(spec.GitLab)); err != nil {
			t.Fatalf("CancelAutoMerge on GitLab with the metadata read refused = %v, want nil", err)
		}
		if got := paths(rec.since(before)); !slices.Equal(got, []string{"POST " + gitlabDeprecated}) {
			t.Errorf("CancelAutoMerge on GitLab with the metadata read refused sent %v, want the deprecated route alone", got)
		}
	})
}

// GitLab's deprecated route answers 401 to a caller who may not cancel, which this
// library cannot tell from a rejected credential, so it is KindUnauthorized as the
// table's departure states; the current route answers that caller 403, the
// contract's KindForbidden.
func TestAGitLabCancelRefusedForPermissionIsEachRoutesOwnStatus(t *testing.T) {
	_, _, err := cancelOn(t, spec.GitLab, "", wire{
		Method: http.MethodPost, Path: gitlabDeprecated,
		Status: http.StatusUnauthorized, Body: json.RawMessage(`{"message":"401 Unauthorized"}`),
	})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindUnauthorized {
		t.Errorf("CancelAutoMerge refused 401 on the deprecated route = %v, want KindUnauthorized: %s", err, cancelPermissionRefused)
	}
	_, _, err = cancelOn(t, spec.GitLab, "19.5.0", wire{
		Method: http.MethodPost, Path: gitlabCancel,
		Status: http.StatusForbidden, Body: json.RawMessage(`{"message":"403 Forbidden"}`),
	})
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindForbidden {
		t.Errorf("CancelAutoMerge refused 403 on the current route = %v, want KindForbidden", err)
	}
}

// The deprecated route's 201 says what happened in its status member alone, success
// or error, so a status naming neither says nothing about the auto-merge and stays
// upstream rather than reading as disarmed.
func TestAGitLabDeprecatedCancelNamingNoKnownStatusStaysUpstream(t *testing.T) {
	_, _, err := cancelOn(t, spec.GitLab, "", wire{
		Method: http.MethodPost, Path: gitlabDeprecated,
		Status: http.StatusCreated, Body: json.RawMessage(`{"status":"pending"}`),
	})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindUpstream || !strings.Contains(fe.Message, "pending") {
		t.Errorf("CancelAutoMerge answered status pending = %v, want KindUpstream naming the status", err)
	}
}

// On the Gitea family a repository that moves between the cancel's 404 and its read
// leaves the read answering from the successor, which says nothing about the pull
// request the cancel addressed, so the call refuses with the stale code carrying the
// successor at the price the table publishes for that arm.
func TestAGiteaFamilyCancelWhoseReadMeetsAMoveIsStale(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		e := requireEntry(t, p, "Merges.CancelAutoMerge")
		t.Run(caseName(e), func(t *testing.T) {
			moved := giteaRepos + movedSelector + "/pulls/1"
			arrived, rec, err := cancelOn(t, p, "",
				wire{Method: http.MethodDelete, Path: giteaCancel, Status: http.StatusNotFound, Body: json.RawMessage(notFound)},
				wire{Method: http.MethodGet, Path: giteaPull, Status: http.StatusMovedPermanently, Headers: map[string]string{"Location": moved}, Body: json.RawMessage(`{}`)},
				wire{Method: http.MethodGet, Path: moved, Status: http.StatusOK, Body: json.RawMessage(`{"number":1,"state":"open"}`)},
			)
			if fe := staleCode(t, "CancelAutoMerge on "+string(p)+" whose read met a move", err); fe != nil {
				checkSuccessor(t, "CancelAutoMerge on "+string(p)+"'s refusal", p, fe.Successor)
			}
			checkPricedArm(t, e, renamedCall{err: err, sent: arrived, misses: rec.misses()}, 3)
		})
	}
}
