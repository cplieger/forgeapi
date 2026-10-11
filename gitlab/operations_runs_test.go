package gitlab

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// docsPipeline is the pipeline row doc/api/pipelines.md shows as the example
// response of "List project pipelines", trimmed to the keys the row reads.
const docsPipeline = `{"id":47,"iid":12,"project_id":1,"status":"pending","source":"push","ref":"new-pipeline",` +
	`"sha":"a91957a858320c0e17f3a0eca7cfacbff50ea29a","web_url":"https://example.com/foo/bar/pipelines/47",` +
	`"created_at":"2016-08-11T11:28:34.085Z","updated_at":"2016-08-11T11:32:35.169Z"}`

func TestAPipelineCarriesItsIdentityAndWhatStartedIt(t *testing.T) {
	h := newHarness(t, map[string]string{pipelinesRoute: `[` + docsPipeline + `]`})
	page, err := h.client.ListRuns(t.Context(), testRef())
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ListRuns = %d row(s), %v, want one", len(page.Items), err)
	}
	run := page.Items[0]
	if want := (forgeapi.RunRef{ID: 47, Number: 12}); run.Ref != want {
		t.Errorf("ListRuns row Ref = %+v, want %+v: id is what the pipeline routes take, iid the project's own sequence", run.Ref, want)
	}
	if run.Trigger != forgeapi.TriggerPush {
		t.Errorf("ListRuns row Trigger = %v, want %v from source push", run.Trigger, forgeapi.TriggerPush)
	}
}

// TestThePipelineSourceMapsTotally holds the trigger to a table over every member of
// Enums::Ci::Pipeline.sources, unknown included as the member it is.
func TestThePipelineSourceMapsTotally(t *testing.T) {
	for source, want := range map[string]forgeapi.RunTrigger{
		"unknown":                               forgeapi.TriggerUnknown,
		"":                                      forgeapi.TriggerUnknown,
		"push":                                  forgeapi.TriggerPush,
		"web":                                   forgeapi.TriggerManual,
		"trigger":                               forgeapi.TriggerManual,
		"schedule":                              forgeapi.TriggerSchedule,
		"api":                                   forgeapi.TriggerManual,
		"external":                              forgeapi.TriggerOther,
		"pipeline":                              forgeapi.TriggerOther,
		"chat":                                  forgeapi.TriggerManual,
		"webide":                                forgeapi.TriggerManual,
		"merge_request_event":                   forgeapi.TriggerPullRequest,
		"external_pull_request_event":           forgeapi.TriggerPullRequest,
		"parent_pipeline":                       forgeapi.TriggerOther,
		"ondemand_dast_scan":                    forgeapi.TriggerOther,
		"ondemand_dast_validation":              forgeapi.TriggerOther,
		"security_orchestration_policy":         forgeapi.TriggerOther,
		"container_registry_push":               forgeapi.TriggerOther,
		"duo_workflow":                          forgeapi.TriggerOther,
		"pipeline_execution_policy_schedule":    forgeapi.TriggerSchedule,
		"dependency_management_security_update": forgeapi.TriggerOther,
	} {
		h := newHarness(t, nil)
		if got := h.client.normalizeRun(&restPipeline{Source: source}, testRef()).Trigger; got != want {
			t.Errorf("normalizeRun(source %q) = trigger %v, want %v", source, got, want)
		}
		if n := h.spy.times("UnknownEnumValue:pipeline source (rest)"); n != 0 {
			t.Errorf("normalizeRun(source %q) counted %d unmapped value(s), want 0: the member is declared", source, n)
		}
	}
	h := newHarness(t, nil)
	if got := h.client.normalizeRun(&restPipeline{Source: "teleport"}, testRef()).Trigger; got != forgeapi.TriggerUnknown {
		t.Errorf("normalizeRun(an undeclared source) = trigger %v, want %v", got, forgeapi.TriggerUnknown)
	}
	if n := h.spy.times("UnknownEnumValue:pipeline source (rest)"); n != 1 {
		t.Errorf("normalizeRun(an undeclared source) counted %d unmapped value(s), want 1", n)
	}
	if !h.logged("teleport") {
		t.Error("normalizeRun(an undeclared source) logged no line naming the value, want one")
	}
}

// created_after is strictly after (CreatedAtFilter#by_created_at, created_at > t), so
// the bound, read at whole seconds, is sent one second early and the rows before it
// dropped.
func TestASinceBoundIsSentAsCreatedAfterAndHeldAtWholeSeconds(t *testing.T) {
	h := newHarness(t, map[string]string{pipelinesRoute: `[` +
		`{"id":3,"created_at":"2026-01-01T00:00:01Z"},{"id":2,"created_at":"2026-01-01T00:00:00.100Z"},{"id":1,"created_at":"2025-12-31T23:59:59.900Z"}]`})
	bound := time.Date(2026, 1, 1, 0, 0, 0, 500_000_000, time.UTC)
	page, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithSince(bound))
	if err != nil {
		t.Fatalf("ListRuns(WithSince(%v)) = %v, want the page", bound, err)
	}
	if got, want := h.instance.query(pipelinesRoute, "created_after"), "2025-12-31T23:59:59Z"; got != want {
		t.Errorf("ListRuns(WithSince(%v)) sent created_after %q, want %q", bound, got, want)
	}
	if len(page.Items) != 2 || page.Items[0].Ref.ID != 3 || page.Items[1].Ref.ID != 2 {
		t.Errorf("ListRuns(WithSince(%v)) answered %+v, want pipelines 3 and 2: pipeline 1 is created before the bound's second", bound, page.Items)
	}
}

// RunRef.ID is a pipeline's identity, so a row the route sent without one is refused
// rather than published as pipeline 0.
func TestAPipelineWithNoIdentityIsRefused(t *testing.T) {
	for name, row := range map[string]string{
		"missing": `{"iid":9,"created_at":"2026-01-01T00:00:00Z"}`,
		"zero":    `{"id":0,"iid":9,"created_at":"2026-01-01T00:00:00Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{pipelinesRoute: `[` + row + `]`})
			_, err := h.client.ListRuns(t.Context(), testRef())
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeValidation || fe.Kind != forgeapi.KindUpstream {
				t.Errorf("ListRuns over a row with an id %s = %v, want code %q of kind %v", name, err, forgeapi.CodeValidation, forgeapi.KindUpstream)
			}
		})
	}
}

// Every pipeline row carries created_at, so under a since bound a row without one is
// malformed, refused rather than dropped as older and answered as a page holding no
// pipeline.
func TestABoundedPipelineWithNoCreationTimeIsRefused(t *testing.T) {
	h := newHarness(t, map[string]string{pipelinesRoute: `[{"id":1}]`})
	bound := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	page, err := h.client.ListRuns(t.Context(), testRef(), forgeapi.WithSince(bound))
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeValidation || fe.Kind != forgeapi.KindUpstream {
		t.Errorf("ListRuns(WithSince(%v)) over a row with no created_at = %+v, %v, want code %q of kind %v", bound, page, err, forgeapi.CodeValidation, forgeapi.KindUpstream)
	}
}

func TestAnUnboundedPipelineListingSendsNoCreatedAfter(t *testing.T) {
	h := newHarness(t, map[string]string{pipelinesRoute: `[` + docsPipeline + `]`})
	if _, err := h.client.ListRuns(t.Context(), testRef()); err != nil {
		t.Fatalf("ListRuns = %v, want the page", err)
	}
	if _, sent := h.instance.queries[pipelinesRoute]["created_after"]; sent {
		t.Error("ListRuns with no bound sent created_after, want none")
	}
}

func TestASinceBoundIsRefusedOnEveryListButThePipelineListing(t *testing.T) {
	h := newHarness(t, nil)
	since := forgeapi.WithSince(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for name, call := range map[string]func() error{
		"ListRepos":    func() error { _, err := h.client.ListRepos(t.Context(), since); return err },
		"ListPRs":      func() error { _, err := h.client.ListPRs(t.Context(), testRef(), since); return err },
		"ListMyPRs":    func() error { _, err := h.client.ListMyPRs(t.Context(), since); return err },
		"ListIssues":   func() error { _, err := h.client.ListIssues(t.Context(), testRef(), since); return err },
		"ListMyIssues": func() error { _, err := h.client.ListMyIssues(t.Context(), since); return err },
		"ListReleases": func() error { _, err := h.client.ListReleases(t.Context(), testRef(), since); return err },
		"ListLabels":   func() error { _, err := h.client.ListLabels(t.Context(), testRef(), since); return err },
	} {
		t.Run(name, func(t *testing.T) {
			var fe *forgeapi.Error
			if err := call(); !asForgeError(err, &fe) || fe.Code != forgeapi.CodeListSinceInvalid {
				t.Errorf("%s(WithSince) = %v, want code %q", name, err, forgeapi.CodeListSinceInvalid)
			}
		})
	}
	if got := h.instance.count(); got != 0 {
		t.Errorf("the refused lists sent %d request(s), want 0: %v", got, h.instance.arrived())
	}
}

const (
	groupProjectsRoute = "GET /api/v4/groups/example%2Fgroup/projects"
	userProjectsRoute  = "GET /api/v4/users/example/projects"
	ownedProject       = `[{"id":1,"path_with_namespace":"example/group/example","web_url":"https://forge.example/example/group/example",` +
		`"http_url_to_repo":"https://forge.example/example/group/example.git","archived":true,"visibility":"private"}]`
)

// A group's projects are its own and its subgroups', never the ones shared into it:
// doc/api/groups.md defaults include_subgroups to false and with_shared to true.
func TestAGroupOwnerListsTheGroupsProjectsAndItsSubgroups(t *testing.T) {
	h := newHarness(t, map[string]string{groupProjectsRoute: ownedProject})
	page, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("example/group"))
	if err != nil {
		t.Fatalf("ListRepos(WithOwner(a group)) = %v, want the page", err)
	}
	if got := h.instance.arrived(); len(got) != 1 || got[0] != groupProjectsRoute {
		t.Errorf("ListRepos(WithOwner(a group)) sent %v, want the one group route", got)
	}
	for key, want := range map[string]string{"include_subgroups": "true", "with_shared": "false", "membership": ""} {
		if got := h.instance.query(groupProjectsRoute, key); got != want {
			t.Errorf("ListRepos(WithOwner(a group)) sent %s=%q, want %q", key, got, want)
		}
	}
	if len(page.Items) != 1 || page.Items[0].Ref.Selector != "example/group/example" || !page.Items[0].Archived || !page.Items[0].Private {
		t.Errorf("ListRepos(WithOwner(a group)) answered %+v, want the group's archived private project", page.Items)
	}
}

// A user's personal namespace has no group route, so a one-segment owner the group
// route answers 404 for is read from the user route, and the walk stays there.
func TestAUserOwnerFallsBackToTheUsersPersonalProjects(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v4/groups/example/projects": `{"message":"404 Group Not Found"}`,
		userProjectsRoute:                     ownedProject,
	})
	h.instance.status("GET /api/v4/groups/example/projects", http.StatusNotFound)
	h.instance.answerHeader(headerNextPage, "2")
	page, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("example"))
	if err != nil {
		t.Fatalf("ListRepos(WithOwner(a user)) = %v, want the user's projects", err)
	}
	if got := h.instance.arrived(); len(got) != 2 || got[1] != userProjectsRoute {
		t.Errorf("ListRepos(WithOwner(a user)) sent %v, want the group route and then the user route", got)
	}
	if len(page.Items) != 1 || page.Next == "" {
		t.Fatalf("ListRepos(WithOwner(a user)) answered %d row(s), Next %q, want one and a continuation", len(page.Items), page.Next)
	}
	before := h.instance.count()
	if _, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("example"), forgeapi.WithAfter(page.Next)); err != nil {
		t.Fatalf("ListRepos(WithAfter(the user walk's Next)) = %v, want the next page", err)
	}
	if got := h.instance.arrived()[before:]; len(got) != 1 || got[0] != userProjectsRoute {
		t.Errorf("ListRepos(WithAfter(the user walk's Next)) sent %v, want the user route alone: the continuation records the route", got)
	}
	if got := h.instance.query(userProjectsRoute, keyPage); got != "2" {
		t.Errorf("ListRepos(WithAfter(the user walk's Next)) sent page %q, want 2", got)
	}
}

func TestAnOwnerNeitherRouteResolvesIsOwnerUnresolved(t *testing.T) {
	h := newHarness(t, map[string]string{
		"GET /api/v4/groups/example/projects": `{"message":"404 Group Not Found"}`,
		userProjectsRoute:                     `{"message":"404 User Not Found"}`,
	})
	h.instance.status("GET /api/v4/groups/example/projects", http.StatusNotFound)
	h.instance.status(userProjectsRoute, http.StatusNotFound)
	_, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("example"))
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeOwnerUnresolved || fe.Kind != forgeapi.KindNotFound || fe.Status != http.StatusNotFound {
		t.Errorf("ListRepos(WithOwner(an owner neither route resolves)) = %v, want code %q of kind %v at 404", err, forgeapi.CodeOwnerUnresolved, forgeapi.KindNotFound)
	}
}

// A path names a group alone, so a 404 for it is unresolved without the user route.
func TestAGroupPathTheGroupRouteDoesNotResolveIsOwnerUnresolved(t *testing.T) {
	h := newHarness(t, map[string]string{groupProjectsRoute: `{"message":"404 Group Not Found"}`})
	h.instance.status(groupProjectsRoute, http.StatusNotFound)
	_, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("example/group"))
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeOwnerUnresolved {
		t.Errorf("ListRepos(WithOwner(an unresolved group path)) = %v, want code %q", err, forgeapi.CodeOwnerUnresolved)
	}
	if got := h.instance.count(); got != 1 {
		t.Errorf("ListRepos(WithOwner(an unresolved group path)) sent %d request(s), want 1: %v", got, h.instance.arrived())
	}
}

func TestTheConnectionNamesItsProduct(t *testing.T) {
	h := newHarness(t, map[string]string{metadataRoute: metadataBody})
	h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
	caps, err := h.client.ConnectionCaps(t.Context())
	if err != nil {
		t.Fatalf("ConnectionCaps = %v, want the capabilities", err)
	}
	if caps.Product != forgeapi.ProductGitLab {
		t.Errorf("ConnectionCaps = product %v, want %v", caps.Product, forgeapi.ProductGitLab)
	}
}

// The owner code describes an owner the call named, so a 404 on an owner-scoped route
// carries it under WithOwner and the default scope's 404 carries the ordinary cell.
func TestTheOwnerCodeRidesOnlyARequestAnOwnerScoped(t *testing.T) {
	for name, call := range map[string]func(context.Context, *Client, ...forgeapi.ListOption) error{
		"ListRepos": func(ctx context.Context, c *Client, opts ...forgeapi.ListOption) error {
			_, err := c.ListRepos(ctx, opts...)
			return err
		},
		"ListMyPRs": func(ctx context.Context, c *Client, opts ...forgeapi.ListOption) error {
			_, err := c.ListMyPRs(ctx, opts...)
			return err
		},
		"ListMyIssues": func(ctx context.Context, c *Client, opts ...forgeapi.ListOption) error {
			_, err := c.ListMyIssues(ctx, opts...)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			var fe *forgeapi.Error
			scoped := call(t.Context(), newRefusingHarness(t, http.StatusNotFound).client, forgeapi.WithOwner("example"))
			if !asForgeError(scoped, &fe) || fe.Code != forgeapi.CodeOwnerUnresolved || fe.Kind != forgeapi.KindNotFound {
				t.Errorf("%s(WithOwner(an owner the instance does not resolve)) over 404 = %v, want code %q of kind %v", name, scoped, forgeapi.CodeOwnerUnresolved, forgeapi.KindNotFound)
			}
			unscoped := call(t.Context(), newRefusingHarness(t, http.StatusNotFound).client)
			if !asForgeError(unscoped, &fe) || fe.Code != forgeapi.CodeRepoOrPRNotVisible || fe.Kind != forgeapi.KindNotFound {
				t.Errorf("%s() over 404 = %v, want code %q of kind %v: the call named no owner", name, unscoped, forgeapi.CodeRepoOrPRNotVisible, forgeapi.KindNotFound)
			}
		})
	}
}

// The group route presents Entities::Project, which exposes no permissions object,
// where the user route presents Entities::ProjectWithAccess (lib/api/groups.rb and
// lib/api/projects.rb, present_projects), so only the user route's rows say whether
// the credential may push.
func TestOnlyTheUserRoutesRowsSayWhetherTheCredentialMayPush(t *testing.T) {
	group := newHarness(t, map[string]string{groupProjectsRoute: ownedProject})
	page, err := group.client.ListRepos(t.Context(), forgeapi.WithOwner("example/group"))
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("ListRepos(WithOwner(a group)) = %+v, %v, want one row", page.Items, err)
	}
	if a := page.Items[0].Affordances; a.CanPush != forgeapi.SupportUnknown || len(a.Ev) != 0 {
		t.Errorf("ListRepos(WithOwner(a group)) row CanPush = %v, Ev %v, want unknown with no evidence: the group route carries no permissions", a.CanPush, a.Ev)
	}
	for level, want := range map[int]forgeapi.Support{developerAccess: forgeapi.SupportYes, developerAccess - 10: forgeapi.SupportNo} {
		user := newHarness(t, map[string]string{
			"GET /api/v4/groups/example/projects": `{"message":"404 Group Not Found"}`,
			userProjectsRoute: `[{"id":1,"path_with_namespace":"example/example","permissions":{"project_access":{"access_level":` +
				strconv.Itoa(level) + `},"group_access":null}}]`,
		})
		user.instance.status("GET /api/v4/groups/example/projects", http.StatusNotFound)
		page, err := user.client.ListRepos(t.Context(), forgeapi.WithOwner("example"))
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("ListRepos(WithOwner(a user)) at access level %d = %+v, %v, want one row", level, page.Items, err)
		}
		if got := page.Items[0].Affordances.CanPush; got != want {
			t.Errorf("ListRepos(WithOwner(a user)) at access level %d row CanPush = %v, want %v", level, got, want)
		}
	}
}

// Every namespace route reads a one-segment owner of digits alone as a numeric id
// (lib/api/helpers.rb find_group, app/finders/user_finder.rb), while a group path or a
// username may be digits alone, so such an owner is resolved by name before its route.
const (
	ownerLookupGroup = `{"data":{"group":{"id":"gid://gitlab/Group/4242"},"user":null,"queryComplexity":{"score":3,"limit":200}}}`
	ownerLookupUser  = `{"data":{"group":null,"user":{"id":"gid://gitlab/User/77"},"queryComplexity":{"score":3,"limit":200}}}`
	ownerLookupNone  = `{"data":{"group":null,"user":null,"queryComplexity":{"score":3,"limit":200}}}`
)

func TestAnOwnerNamedByDigitsAloneIsResolvedByNameBeforeItsRoute(t *testing.T) {
	t.Run("a_group", func(t *testing.T) {
		h := newHarness(t, map[string]string{documentRoute: ownerLookupGroup, "GET /api/v4/groups/4242/projects": ownedProject})
		page, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("123"))
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("ListRepos(WithOwner(\"123\"), a group) = %+v, %v, want the group's project", page.Items, err)
		}
		if got := h.instance.arrived(); len(got) != 2 || got[0] != documentRoute || got[1] != "GET /api/v4/groups/4242/projects" {
			t.Errorf("ListRepos(WithOwner(\"123\"), a group) sent %v, want the lookup and then the group's route by its id", got)
		}
		posted := h.instance.posted()
		if len(posted) != 1 || !strings.Contains(posted[0], `"operationName":"OwnerLookup"`) || !strings.Contains(posted[0], `"fullPath":"123"`) || !strings.Contains(posted[0], `"username":"123"`) {
			t.Errorf("ListRepos(WithOwner(\"123\")) posted %v, want the OwnerLookup document naming 123 as both a path and a username", posted)
		}
	})
	t.Run("a_user", func(t *testing.T) {
		h := newHarness(t, map[string]string{documentRoute: ownerLookupUser, "GET /api/v4/users/77/projects": ownedProject})
		h.instance.answerHeader(headerNextPage, "2")
		page, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("123"))
		if err != nil || len(page.Items) != 1 || page.Next == "" {
			t.Fatalf("ListRepos(WithOwner(\"123\"), a user) = %+v, Next %q, %v, want the user's project and a continuation", page.Items, page.Next, err)
		}
		if got := h.instance.arrived(); len(got) != 2 || got[1] != "GET /api/v4/users/77/projects" {
			t.Errorf("ListRepos(WithOwner(\"123\"), a user) sent %v, want the lookup and then the user's route by its id", got)
		}
		before := h.instance.count()
		if _, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("123"), forgeapi.WithAfter(page.Next)); err != nil {
			t.Fatalf("ListRepos(WithAfter(the user walk's Next)) = %v, want the next page", err)
		}
		if got := h.instance.arrived()[before:]; len(got) != 2 || got[1] != "GET /api/v4/users/77/projects" {
			t.Errorf("ListRepos(WithAfter(the user walk's Next)) sent %v, want the lookup and the user's route", got)
		}
	})
	t.Run("no_one", func(t *testing.T) {
		h := newHarness(t, map[string]string{documentRoute: ownerLookupNone})
		_, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("123"))
		var fe *forgeapi.Error
		if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeOwnerUnresolved || fe.Kind != forgeapi.KindNotFound {
			t.Errorf("ListRepos(WithOwner(\"123\"), no such name) = %v, want code %q of kind %v", err, forgeapi.CodeOwnerUnresolved, forgeapi.KindNotFound)
		}
		if got := h.instance.count(); got != 1 {
			t.Errorf("ListRepos(WithOwner(\"123\"), no such name) sent %d request(s), want the lookup alone: %v", got, h.instance.arrived())
		}
	})
	// GraphQL nulls a nullable field whose resolver failed and reports the failure
	// beside the data, so a null member carrying an error is no answer about that name.
	for name, lookup := range map[string]string{
		"a_failed_group_beside_a_user": `{"data":{"group":null,"user":{"id":"gid://gitlab/User/77"},"queryComplexity":{"score":3,"limit":200}},` +
			`"errors":[{"message":"Internal server error","path":["group"]}]}`,
		"both_failed": `{"data":{"group":null,"user":null,"queryComplexity":{"score":3,"limit":200}},` +
			`"errors":[{"message":"The resource that you are attempting to access does not exist or you don't have permission to perform this action","path":["user"],"extensions":{"code":"resourceNotAvailable"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: lookup, "GET /api/v4/users/77/projects": ownedProject})
			_, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("123"))
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code == forgeapi.CodeOwnerUnresolved || fe.Kind != forgeapi.KindUpstream || fe.Status != http.StatusOK {
				t.Errorf("ListRepos(WithOwner(\"123\"), a lookup answered %s) = %v, want a failure of kind %v at status 200 that is not %q", lookup, err, forgeapi.KindUpstream, forgeapi.CodeOwnerUnresolved)
			}
			if got := h.instance.count(); got != 1 {
				t.Errorf("ListRepos(WithOwner(\"123\"), a lookup answered %s) sent %d request(s), want the lookup alone: %v", lookup, got, h.instance.arrived())
			}
		})
	}
}

// The cross-repository lists have a group route alone, so a name that resolves to a
// user is unresolved there, as a user's own namespace is on the group route.
func TestTheCrossRepositoryListsReadAnOwnerNamedByDigitsAloneByItsGroupID(t *testing.T) {
	for name, test := range map[string]struct {
		call  func(context.Context, *Client, ...forgeapi.ListOption) error
		route string
	}{
		"ListMyPRs": {
			call: func(ctx context.Context, c *Client, opts ...forgeapi.ListOption) error {
				_, err := c.ListMyPRs(ctx, opts...)
				return err
			},
			route: "GET /api/v4/groups/4242/merge_requests",
		},
		"ListMyIssues": {
			call: func(ctx context.Context, c *Client, opts ...forgeapi.ListOption) error {
				_, err := c.ListMyIssues(ctx, opts...)
				return err
			},
			route: "GET /api/v4/groups/4242/issues",
		},
	} {
		t.Run(name, func(t *testing.T) {
			group := newHarness(t, map[string]string{documentRoute: ownerLookupGroup, test.route: "[]"})
			if err := test.call(t.Context(), group.client, forgeapi.WithOwner("123")); err != nil {
				t.Fatalf("%s(WithOwner(\"123\"), a group) = %v, want the page", name, err)
			}
			if got := group.instance.arrived(); len(got) != 2 || got[1] != test.route {
				t.Errorf("%s(WithOwner(\"123\"), a group) sent %v, want the lookup and then %s", name, got, test.route)
			}
			user := newHarness(t, map[string]string{documentRoute: ownerLookupUser})
			err := test.call(t.Context(), user.client, forgeapi.WithOwner("123"))
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeOwnerUnresolved || fe.Kind != forgeapi.KindNotFound {
				t.Errorf("%s(WithOwner(\"123\"), a user) = %v, want code %q of kind %v", name, err, forgeapi.CodeOwnerUnresolved, forgeapi.KindNotFound)
			}
			if got := user.instance.count(); got != 1 {
				t.Errorf("%s(WithOwner(\"123\"), a user) sent %d request(s), want the lookup alone: %v", name, got, user.instance.arrived())
			}
		})
	}
}

// An owner with any letter in it is never a numeric id, so it costs no lookup.
func TestAnOwnerWithALetterCostsNoLookup(t *testing.T) {
	h := newHarness(t, map[string]string{"GET /api/v4/groups/123a/projects": ownedProject})
	if _, err := h.client.ListRepos(t.Context(), forgeapi.WithOwner("123a")); err != nil {
		t.Fatalf("ListRepos(WithOwner(\"123a\")) = %v, want the page", err)
	}
	if got := h.instance.arrived(); len(got) != 1 {
		t.Errorf("ListRepos(WithOwner(\"123a\")) sent %v, want the group route alone", got)
	}
}
