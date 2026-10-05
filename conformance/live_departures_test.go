package conformance

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// liveDepartureCase names, in a child process of this test binary, the case the
// child drives through the live lane, and liveDepartureArm says whether the child's
// instance answers as recorded or with one cell altered; both are unset in the run
// the suite makes.
const (
	liveDepartureCase = "FORGEAPI_CONFORMANCE_LIVE_DEPARTURE_CASE"
	liveDepartureArm  = "FORGEAPI_CONFORMANCE_LIVE_DEPARTURE_ARM"
)

// The answers a child's instance gives.
const (
	asRecorded = "as_recorded"
	altered    = "altered"
)

// subjectIndependentCell is one cell whose value no subject of the live lane can
// move, and an answer that moves it: the instance it is read from, recorded, and
// the same recording with the one change that moves the cell.
type subjectIndependentCell struct {
	// stand points the live lane's variables at an instance of the product that
	// answers as recorded, or with the alteration where alter is true.
	stand   func(t *testing.T, p spec.Product, alter bool)
	product spec.Product
	method  string
	// field is the path of the cell the alteration moves, as the case's own
	// contract names it.
	field string
}

// forgejoRateLimit is the rate-limit signal codeberg.org sends with a list, which
// the Gitea family reads where an instance sends it: the table states the budget's
// remaining and reset cells on Forgejo from it and the same cells on Gitea as cannot
// supply, since Gitea sends none.
var forgejoRateLimit = map[string]string{
	"RateLimit":        `"baseline";r=4990;t=600`,
	"RateLimit-Policy": `"baseline";q=5000;w=600`,
}

// giteaRerunVerb is the route Gitea's API document declares for re-running a run's
// failed jobs, which Forgejo's declares none of: the table states Forgejo's re-run
// capability as cannot supply, read from that document.
const giteaRerunVerb = "/repos/{owner}/{repo}/actions/runs/{run}/rerun-failed-jobs"

// subjectIndependentCells are the cells the live lane's departure assertion is held
// to here. The cannot-supply cells are each moved by an answer carrying what the
// product's family reads where its sibling product sends it: a cannot-supply cell is
// a statement about the family whatever the record holds, so such an answer is the
// product moving under it. The creation's cells are contract cells of what its live
// preparation fixes: the state the creation leaves the object in, and the title and
// label the case itself sent. The seed's cells are what the sandbox's permanent read
// subject fixes on a read of it: the branch its pull request is opened from, the
// label it carries, and the open issue the issue list holds.
var subjectIndependentCells = map[string]subjectIndependentCell{
	"gitea_budget_under_a_rate_limit_signal": {
		product: spec.Gitea, method: "Governor.BudgetState", field: "Remaining",
		stand: func(t *testing.T, p spec.Product, alter bool) {
			standRead(t, p, "Governor.BudgetState", func(t *testing.T, f *fixture) {
				for i := range f.Routes {
					if f.Routes[i].Path != "/api/v1/user/repos" {
						continue
					}
					headers := maps.Clone(f.Routes[i].Headers)
					maps.Copy(headers, forgejoRateLimit)
					f.Routes[i].Headers = headers
					return
				}
				t.Fatalf("Setup: %s serves no repository list to carry the rate-limit signal", f.path)
			}, alter)
		},
	},
	"forgejo_connection_under_a_rerun_verb": {
		product: spec.Forgejo, method: "Capabilities.ConnectionCaps", field: "Caps[rerun_checks]",
		stand: func(t *testing.T, p spec.Product, alter bool) {
			standRead(t, p, "Capabilities.ConnectionCaps", func(t *testing.T, f *fixture) {
				for i := range f.Routes {
					if f.Routes[i].Path != "/swagger.v1.json" {
						continue
					}
					var doc map[string]any
					if err := json.Unmarshal(f.Routes[i].Body, &doc); err != nil {
						t.Fatalf("Setup: %s's API document is not a JSON object: %v", f.path, err)
					}
					paths, ok := doc["paths"].(map[string]any)
					if !ok {
						t.Fatalf("Setup: %s's API document declares no paths", f.path)
					}
					paths[giteaRerunVerb] = map[string]any{"post": map[string]any{"operationId": "rerunFailedWorkflowRun"}}
					body, err := json.Marshal(doc)
					if err != nil {
						t.Fatalf("Setup: re-encoding the API document: %v", err)
					}
					f.Routes[i].Body = body
					return
				}
				t.Fatalf("Setup: %s serves no API document", f.path)
			}, alter)
		},
	},
	"gitea_creation_answered_closed": {
		product: spec.Gitea, method: "Issues.CreateIssue", field: "State",
		stand: func(t *testing.T, p spec.Product, alter bool) {
			standCreation(t, p, func(answer map[string]any) { answer["state"] = "closed" }, alter)
		},
	},
	"gitea_creation_answered_under_another_title": {
		product: spec.Gitea, method: "Issues.CreateIssue", field: "Title",
		stand: func(t *testing.T, p spec.Product, alter bool) {
			standCreation(t, p, func(answer map[string]any) { answer["title"] = "Someone else's issue" }, alter)
		},
	},
	"gitea_creation_answered_without_its_label": {
		product: spec.Gitea, method: "Issues.CreateIssue", field: "Labels[].Name",
		stand: func(t *testing.T, p spec.Product, alter bool) {
			standCreation(t, p, func(answer map[string]any) { answer["labels"] = []any{} }, alter)
		},
	},
	"gitea_seed_read_from_another_branch": {
		product: spec.Gitea, method: "PullRequests.ReadPR", field: "SourceBranch",
		stand: func(t *testing.T, p spec.Product, alter bool) {
			standSeed(t, p, "PullRequests.ReadPR", "/api/v1/repos/"+selector+"/pulls/"+strconv.Itoa(prNumber),
				func(pr map[string]any) { pr["head"].(map[string]any)["ref"] = "seed" },
				func(pr map[string]any) { pr["head"].(map[string]any)["ref"] = "elsewhere" }, alter)
		},
	},
	"gitlab_seed_read_without_its_label": {
		product: spec.GitLab, method: "PullRequests.ReadPR", field: "Labels[].Name",
		stand: func(t *testing.T, p spec.Product, alter bool) {
			mergeRequest := func(doc map[string]any) map[string]any {
				return doc["data"].(map[string]any)["project"].(map[string]any)["mergeRequest"].(map[string]any)
			}
			// The seed carries the lane's label, which the hosted sandboxes name
			// apart from the canonical one.
			const lanes = "forgeapi-a"
			standSeed(t, p, "PullRequests.ReadPR", "",
				func(doc map[string]any) {
					mr := mergeRequest(doc)
					mr["sourceBranch"] = "seed"
					node := mr["labels"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
					node["title"] = lanes
				},
				func(doc map[string]any) { mergeRequest(doc)["labels"] = map[string]any{"nodes": []any{}} }, alter)
			t.Setenv(liveVar(p, "LABEL"), lanes)
		},
	},
	"forgejo_seed_list_holding_no_issue": {
		product: spec.Forgejo, method: "Issues.ListIssues", field: "Items[].State",
		stand: func(t *testing.T, p spec.Product, alter bool) {
			standSeedList(t, p, "Issues.ListIssues", "/api/v1/repos/"+selector+"/issues", alter)
		},
	},
}

// standSeed serves one read case's fixture as the lane's permanent read subject
// answers it, and names that subject to the live lane: the sandbox variable names
// its repository and the pull-request variable names its pull request. The answer
// at path, or the document where path is empty, is rewritten by seed into the seed
// pull request, open from a branch named seed into main, and then by alteration
// where alter is true.
func standSeed(t *testing.T, p spec.Product, method, path string, seed, alteration func(map[string]any), alter bool) {
	t.Helper()
	f, err := loadFixture(p, method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	f.Routes = slices.Clone(f.Routes)
	edit := func(answer map[string]any) {
		seed(answer)
		if alter {
			alteration(answer)
		}
	}
	rewritten := false
	for i := range f.Routes {
		if (path == "" && f.Routes[i].GraphQL) || (path != "" && f.Routes[i].Path == path) {
			f.Routes[i].Body = rewriteAnswer(t, f.Routes[i].Body, edit)
			rewritten = true
		}
	}
	if !rewritten {
		t.Fatalf("Setup: %s serves no answer at %q to stand the seed in", f.path, path)
	}
	nameSeed(t, p, f)
}

// standSeedList serves one list case's fixture on the lane's permanent read
// subject, the list at path answered with no row where alter is true.
func standSeedList(t *testing.T, p spec.Product, method, path string, alter bool) {
	t.Helper()
	f, err := loadFixture(p, method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	f.Routes = slices.Clone(f.Routes)
	for i := range f.Routes {
		if alter && f.Routes[i].Path == path {
			f.Routes[i].Body = json.RawMessage(`[]`)
		}
	}
	nameSeed(t, p, f)
}

// nameSeed serves f and points the live lane's variables for the product at it,
// with the canonical subject named as the sandbox and its pull request as the seed.
func nameSeed(t *testing.T, p spec.Product, f fixture) {
	t.Helper()
	srv, _ := newFixtureServer(t, f)
	t.Setenv(liveVar(p, "URL"), srv.URL)
	t.Setenv(liveVar(p, "TOKEN"), "conformance-placeholder")
	t.Setenv(liveVar(p, "REPO"), selector)
	t.Setenv(liveVar(p, "SANDBOX"), selector)
	t.Setenv(liveVar(p, "PR"), strconv.Itoa(prNumber))
}

// rewriteAnswer is one recorded JSON object with edit applied.
func rewriteAnswer(t *testing.T, body json.RawMessage, edit func(map[string]any)) json.RawMessage {
	t.Helper()
	var answer map[string]any
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("Setup: the recorded answer is not a JSON object: %v", err)
	}
	edit(answer)
	out, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("Setup: re-encoding the answer: %v", err)
	}
	return out
}

// standRead serves one read case's fixture, altered where alter is true, and
// points the live lane's variables for the product at it.
func standRead(t *testing.T, p spec.Product, method string, alteration func(*testing.T, *fixture), alter bool) {
	t.Helper()
	f, err := loadFixture(p, method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	f.Routes = slices.Clone(f.Routes)
	if alter {
		alteration(t, &f)
	}
	srv, _ := newFixtureServer(t, f)
	t.Setenv(liveVar(p, "URL"), srv.URL)
	t.Setenv(liveVar(p, "TOKEN"), "conformance-placeholder")
	t.Setenv(liveVar(p, "REPO"), selector)
}

// standCreation serves the issue creation and the close of what it creates as a
// forge answers them, alteration applied over the creation's answer where alter is
// true, with the canonical subject named as the sandbox, so the creation runs.
func standCreation(t *testing.T, p spec.Product, alteration func(map[string]any), alter bool) {
	t.Helper()
	if !alter {
		alteration = nil
	}
	creationInstance(t, p, selector, alteration)
}

// runSubjectIndependentCell is what a child process runs: the named cell's
// instance, stood up as its arm says, and the cell's case driven through the live
// lane.
func runSubjectIndependentCell(t *testing.T, named, arm string) {
	t.Helper()
	keys := slices.Sorted(maps.Keys(subjectIndependentCells))
	i := slices.Index(keys, named)
	if i < 0 || (arm != asRecorded && arm != altered) {
		t.Fatalf("Setup: %s = %q and %s = %q, want one of %v and one of %q, %q", liveDepartureCase, named, liveDepartureArm, arm, keys, asRecorded, altered)
	}
	cell := subjectIndependentCells[keys[i]]
	cell.stand(t, cell.product, arm == altered)
	e := requireEntry(t, cell.product, cell.method)
	op, ok := operationFor(cell.method)
	if !ok {
		t.Fatalf("Setup: the contract declares no case for %s", cell.method)
	}
	t.Run("live", func(t *testing.T) {
		runLive(t, e, op)
	})
}

// A live case asserts every cell whose value no subject of the lane can move, so an
// answer moving one turns the case red, and the same answer as recorded keeps it
// green. A failed case ends its whole test, so each arm runs in a child process of
// this test binary, whose output and exit status are what the lane itself would see.
func TestALiveCaseFailsWhereACellItsSubjectCannotMoveLeavesTheTable(t *testing.T) {
	if named := os.Getenv(liveDepartureCase); named != "" {
		runSubjectIndependentCell(t, named, os.Getenv(liveDepartureArm))
		return
	}
	const name = "TestALiveCaseFailsWhereACellItsSubjectCannotMoveLeavesTheTable"
	for _, caseName := range slices.Sorted(maps.Keys(subjectIndependentCells)) {
		cell := subjectIndependentCells[caseName]
		for _, arm := range []string{asRecorded, altered} {
			t.Run(caseName+"_"+arm, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+name+"$", "-test.v")
				cmd.Env = append(withoutLiveVars(os.Environ()), liveDepartureCase+"="+caseName, liveDepartureArm+"="+arm)
				out, err := cmd.CombinedOutput()
				text := string(out)
				_, failed := errors.AsType[*exec.ExitError](err)
				if err != nil && !failed {
					t.Fatalf("Setup: running the child for %s: %v", caseName, err)
				}
				if strings.Contains(text, "--- SKIP: "+name+"/live") {
					t.Fatalf("%s on %s %s skipped, want it run against the instance the child stood up:\n%s", cell.method, cell.product, arm, text)
				}
				if arm == asRecorded {
					if failed || !strings.Contains(text, "--- PASS: "+name+"/live") {
						t.Errorf("%s on %s answered as recorded = exit %v, want the live case to pass: the recording holds every cell the table states:\n%s", cell.method, cell.product, err, text)
					}
					return
				}
				if !failed || !strings.Contains(text, "--- FAIL: "+name+"/live") {
					t.Errorf("%s on %s with %s altered = exit %v, want the live case to fail: no subject of the lane moves that cell, so an answer moving it is the product leaving the table:\n%s", cell.method, cell.product, cell.field, err, text)
				}
				if !strings.Contains(text, cell.field+" = ") {
					t.Errorf("%s on %s with %s altered printed no %q, want the failure to name the cell it holds:\n%s", cell.method, cell.product, cell.field, cell.field+" = ", text)
				}
			})
		}
	}
}

// A cell of a list row describes a row, so a live list holding none asserts no cell
// of one, a cannot-supply cell whose stated value is not its type's zero included:
// the lane's subject may hold no open row where the fixture holds one, and the
// subject's own emptiness is not the product moving. A sandbox the lane names with
// no seed pull request is such a subject too, since only the seed puts a row there.
func TestALiveListHoldingNoRowAssertsNoCellOfARow(t *testing.T) {
	const method = "PullRequests.ListPRs"
	p := spec.Gitea
	for name, sandbox := range map[string]string{"with_no_sandbox_named": "", "on_a_sandbox_naming_no_seed": selector} {
		t.Run(name, func(t *testing.T) {
			f, err := loadFixture(p, method)
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			f.Routes = slices.Clone(f.Routes)
			list := "/api/v1/repos/" + selector + "/pulls"
			for i := range f.Routes {
				if f.Routes[i].Path == list {
					f.Routes[i].Body = json.RawMessage(`[]`)
				}
			}
			srv, _ := newFixtureServer(t, f)
			t.Setenv(liveVar(p, "URL"), srv.URL)
			t.Setenv(liveVar(p, "TOKEN"), "conformance-placeholder")
			t.Setenv(liveVar(p, "REPO"), selector)
			t.Setenv(liveVar(p, "SANDBOX"), sandbox)
			unsetLiveVar(t, p, "PR")

			if skipped := runReadLive(t, p, method); skipped {
				t.Fatalf("%s on %s skipped against an instance answering no row, want it run", method, p)
			}
		})
	}
}

// The rule above is a statement about the answer, not about rows: a cannot-supply
// cell of a row is held wherever the answer holds that row. The cell here is one
// this test adds to the table's own entry, on an answer whose row holds a value
// there, so the rule is held whatever row cells the table states.
func TestALiveRowCellIsHeldWhereTheAnswerHoldsTheRow(t *testing.T) {
	const method = "PullRequests.ListPRs"
	e := requireEntry(t, spec.Gitea, method)
	e.Departures = append(slices.Clone(e.Departures), spec.Departure{Field: "Items[].Title", Kind: spec.CannotSupply, Says: "empty"})
	op, ok := operationFor(method)
	if !ok {
		t.Fatalf("Setup: the contract declares no case for %s", method)
	}
	held := forgeapi.Page[forgeapi.PullRequest]{Items: []forgeapi.PullRequest{{Title: prTitle}}}

	failures := liveCellFailures(e, op, held, nil)
	if !slices.ContainsFunc(failures, func(f string) bool { return strings.Contains(f, "Items[].Title = ") }) {
		t.Errorf("liveCellFailures(%s on %s, a row titled %q) = %q, want a failure naming Items[].Title: the table says the product cannot supply it and the row holds one",
			method, spec.Gitea, prTitle, failures)
	}
}

// A value the family supplies on a subject other than the fixture's is no departure
// the lane holds: a named GitLab pipeline, an archived fork among a GitLab
// credential's memberships, a GitLab merge request closed or reopened once its head
// ran a pipeline, a queued GitHub pull request found by the viewer's search, and a
// viewer whose search spans more than one page each pass the live assertion, so the
// lane stays green on a subject that legitimately holds them.
func TestALiveAnswerHoldingWhatTheFamilySuppliesOnAnotherSubjectPasses(t *testing.T) {
	ranAPipeline := forgeapi.PullRequest{Action: forgeapi.ActionState{
		Checks:        forgeapi.CheckPending,
		QueueState:    forgeapi.QueueNone,
		QueuePosition: forgeapi.QueuePositionUnknown,
	}}
	for _, test := range []struct {
		answer  any
		product spec.Product
		method  string
		holds   string
	}{
		{
			product: spec.GitLab, method: "Checks.ListRuns", holds: "a pipeline its configuration names",
			answer: forgeapi.Page[forgeapi.Run]{Items: []forgeapi.Run{{Name: "nightly"}}},
		},
		{
			product: spec.GitLab, method: "Repos.ListRepos", holds: "an archived fork among the memberships",
			answer: forgeapi.Page[forgeapi.Repository]{Items: []forgeapi.Repository{{Archived: true, Fork: true}}},
		},
		{
			product: spec.GitLab, method: "PullRequests.ClosePR", holds: "a head pipeline that ran",
			answer: ranAPipeline,
		},
		{
			product: spec.GitLab, method: "PullRequests.ReopenPR", holds: "a head pipeline that ran",
			answer: ranAPipeline,
		},
		{
			product: spec.GitHub, method: "PullRequests.ListMyPRs", holds: "a queued pull request and a further page",
			answer: forgeapi.Page[forgeapi.PullRequest]{
				Items: []forgeapi.PullRequest{{Action: forgeapi.ActionState{QueueState: forgeapi.QueueQueued, QueuePosition: 2}}},
				Next:  forgeapi.Cursor("s25.example"),
			},
		},
		{
			product: spec.GitHub, method: "Issues.ListMyIssues", holds: "a further page",
			answer: forgeapi.Page[forgeapi.Issue]{Items: []forgeapi.Issue{{}}, Next: forgeapi.Cursor("s25.example")},
		},
	} {
		t.Run(caseName(spec.Entry{Method: test.method, Product: test.product}), func(t *testing.T) {
			e := requireEntry(t, test.product, test.method)
			op, ok := operationFor(test.method)
			if !ok {
				t.Fatalf("Setup: the contract declares no case for %s", test.method)
			}

			if failures := liveCellFailures(e, op, test.answer, nil); len(failures) != 0 {
				t.Errorf("liveCellFailures(%s on %s, an answer holding %s) = %q, want none: the family supplies these on such a subject",
					test.method, test.product, test.holds, failures)
			}
		})
	}
}

// A cannot-supply cell of a row states what the family answers for every row, so it
// is held on each row the answer holds and on each element of a collection inside
// one, not on the first alone: a later row supplying the value is the product
// leaving the table as much as the first row would be. The cells here are ones this
// test adds to the table's own entry.
func TestALiveRowCellIsHeldOnEveryRowTheAnswerHolds(t *testing.T) {
	const method = "PullRequests.ListPRs"
	op, ok := operationFor(method)
	if !ok {
		t.Fatalf("Setup: the contract declares no case for %s", method)
	}
	for _, test := range []struct {
		held  forgeapi.Page[forgeapi.PullRequest]
		name  string
		field string
	}{
		{
			name:  "the_second_row_supplies_a_title",
			field: "Items[].Title",
			held:  forgeapi.Page[forgeapi.PullRequest]{Items: []forgeapi.PullRequest{{}, {Title: prTitle}}},
		},
		{
			name:  "a_later_rows_label_supplies_a_name",
			field: "Items[].Labels[].Name",
			held: forgeapi.Page[forgeapi.PullRequest]{Items: []forgeapi.PullRequest{
				{},
				{Labels: []forgeapi.Label{{}, {Name: "example-label"}}},
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := requireEntry(t, spec.Gitea, method)
			e.Departures = append(slices.Clone(e.Departures), spec.Departure{Field: test.field, Kind: spec.CannotSupply, Says: "empty"})

			failures := liveCellFailures(e, op, test.held, nil)
			if !slices.ContainsFunc(failures, func(f string) bool { return strings.Contains(f, test.field+" = ") }) {
				t.Errorf("liveCellFailures(%s on %s, an answer where %s) = %q, want a failure naming %s: the table says the product cannot supply it and a later element holds one",
					method, spec.Gitea, strings.ReplaceAll(test.name, "_", " "), failures, test.field)
			}
		})
	}
}
