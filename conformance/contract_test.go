package conformance

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// check is one field of one operation's normalized answer.
//
// want is the contract's value, the SAME on all four products. none is the value
// the field takes on a product whose table row says it cannot supply it, which is
// the type's zero unless the row's own words name another; noneBy carries it for
// the fields where two products name two different ones. nonZero relaxes want to
// "populated at all", and it is used only where the normalized contract fixes that a
// value arrives without fixing its spelling, which is a gap in that contract rather than a
// choice of this suite.
type check struct {
	got     any
	want    any
	none    any
	noneBy  map[spec.Product]any
	path    string
	nonZero bool
}

// eq asserts the contract's value.
func eq(path string, got, want any) check {
	return check{path: path, got: got, want: want, none: zeroLike(want)}
}

// absent asserts the contract's value, naming the value a product that cannot
// supply the field answers instead where that value is not the type's zero.
func absent(path string, got, want, none any) check {
	return check{path: path, got: got, want: want, none: none}
}

// absentBy is absent with one none value per product, for a field two products
// cannot supply for two different reasons and with two different answers.
func absentBy(path string, got, want, none any, by map[spec.Product]any) check {
	return check{path: path, got: got, want: want, none: none, noneBy: by}
}

// present asserts only that the field is populated, for a field whose arrival the
// design fixes and whose normalized spelling it does not.
func present(path string, got, none any) check {
	return check{path: path, got: got, want: nil, none: none, nonZero: true}
}

// operation is how the suite drives one published operation and what it must
// answer. The method spelling is the table's own, which is what joins the two.
type operation struct {
	invoke func(ctx context.Context, c any, s subject) (any, error)
	expect func(p spec.Product, got any) []check
	items  func(got any) int
	// zero is the operation's answer type at its zero value, which is what lets
	// the departure-coverage gate read the declared field paths even on a tree
	// whose operations panic.
	zero any
	// errorArms are the table fields naming what the operation answers under an
	// option, or after an earlier answer on the connection, rather than a field of
	// its default answer. A dedicated test asserts each one, and the
	// departure-coverage gate reads them beside the fields expect declares.
	errorArms []string
	method    string
	prelude   string
	mutation  bool
	// readsPR marks a read addressed at the subject's pull request, which a live
	// run can make only where the lane says which pull request its subject holds.
	readsPR bool
}

// ownerUnresolved is the table's field for what a cross-repository list answers
// when the instance does not resolve the owner it was scoped to.
const ownerUnresolved = "error, owner unresolved"

// scopeInsufficient is the table's field for what an operation answers when the
// instance authenticated the credential and refused it for a permission or scope it
// lacks.
const scopeInsufficient = "error, scope insufficient"

// afterScopeRefusal is the table's field for what the merge-state grant answers
// once a request on the connection was refused for a permission the credential
// lacks.
const afterScopeRefusal = "Caps[read_merge_state], after a scope refusal"

// runsBeyondTheFixturePage are the products whose run fixture serves one row of a
// larger stated total, so the case meets a page the total says is not the last and
// its answer carries a continuation: the listing continues while the rows a walk
// was served are fewer than the body's total_count.
var runsBeyondTheFixturePage = map[spec.Product]bool{spec.Forgejo: true}

// runsNext is the run listing's continuation check for one product's fixture.
func runsNext(p spec.Product, next any) check {
	if runsBeyondTheFixturePage[p] {
		return present("Next", next, forgeapi.Cursor(""))
	}
	return eq("Next", next, forgeapi.Cursor(""))
}

// The canonical run of the run listing's fixtures: the workflow's display name and
// the run's own page, which GitLab addresses as a pipeline page and the other three
// products as an actions run page.
const (
	runName     = "example"
	runWeb      = "https://forge.example/example/example/actions/runs/1"
	pipelineWeb = "https://forge.example/example/example/-/pipelines/1"
)

// runWebURL is the page one product's fixture names for the canonical run. The
// value is the forge's own text, so it is per product where the products' routes
// for a run page differ.
func runWebURL(p spec.Product) string {
	if p == spec.GitLab {
		return pipelineWeb
	}
	return runWeb
}

// crossRepositoryIssues is the cross-repository issue list as the Issues role
// declares it. The case reaches it through this interface rather than through the
// role, so the suite compiles against a role that lacks it and reports the absence
// as a failure of its own; TestTheRolesDeclareEveryOperationTheContractDrives holds
// the role itself.
type crossRepositoryIssues interface {
	ListMyIssues(ctx context.Context, opts ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Issue], error)
}

// ownerScope is what a cross-repository case passes: the owner scope where the
// subject names an owner, and no option, the viewer's own scope, otherwise.
func ownerScope(s subject) []forgeapi.ListOption {
	if s.owner == "" {
		return nil
	}
	return []forgeapi.ListOption{forgeapi.WithOwner(s.owner)}
}

// methodAbsent is what an operation answers when the client serves the role and
// not the method, which is how a role growing a method looks before a family
// implements it.
func methodAbsent(method string) error {
	return fmt.Errorf("the client declares no %s", method)
}

// roleAbsent is what an operation answers when the client does not implement the
// role that declares it. The roles are the INTERSECTION, so a family that cannot
// serve one does not implement it, and the negative half cannot be a compile-time
// assertion: the assertion that would state it is the one that fails the build.
func roleAbsent(role string) error {
	return fmt.Errorf("the client does not implement the %s role", role)
}

// first returns a slice's first element, the zero value where there is none, which
// is what lets a row-level path be checked on a product whose answer carries no row
// at all.
func first[T any](s []T) T {
	var zero T
	if len(s) == 0 {
		return zero
	}
	return s[0]
}

// operations is the suite's contract: one entry per published operation, in the
// order the roles declare them.
//
// Every want here is derived from the normalized contract the returned types state and is
// single valued across the products. Three kinds of field carry no check. A field whose value is the forge's
// own text rather than a normalized value is checked against the fixture's own
// value, which is the same string on every product. A field the table marks
// unmeasured on a product is skipped on that product, by the driver, with the
// reason. And a field no normalized value is fixed for is named in the record rather
// than guessed at here.
var operations = []operation{
	{
		method:    "Identity.Whoami",
		zero:      forgeapi.Account{},
		errorArms: []string{scopeInsufficient},
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.Identity)
			if !ok {
				return nil, roleAbsent("Identity")
			}
			return r.Whoami(ctx)
		},
		expect: func(_ spec.Product, got any) []check {
			g := got.(forgeapi.Account)
			return []check{
				eq("Login", g.Login, author),
				eq("Name", g.Name, accountName),
				eq("Email", g.Email, accountEmail),
				eq("WebURL", g.WebURL, accountWeb),
				eq("Scopes", g.Scopes, []string{"repo", "workflow"}),
			}
		},
	},
	{
		method: "Repos.ListRepos",
		zero:   forgeapi.Page[forgeapi.Repository]{},
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.Repos)
			if !ok {
				return nil, roleAbsent("Repos")
			}
			return r.ListRepos(ctx)
		},
		items: func(got any) int { return len(got.(forgeapi.Page[forgeapi.Repository]).Items) },
		expect: func(p spec.Product, got any) []check {
			g := got.(forgeapi.Page[forgeapi.Repository])
			row := first(g.Items)
			a := row.Affordances
			return append(affordanceChecks("Items[].Affordances.", a), []check{
				eq("Items[].Ref", row.Ref, repoRef(p)),
				eq("Items[].Description", row.Description, repoDesc),
				eq("Items[].WebURL", row.WebURL, repoWebURL),
				eq("Items[].CloneURL", row.CloneURL, repoCloneURL),
				eq("Items[].UpdatedAt", row.UpdatedAt, updatedAt),
				eq("Items[].Private", row.Private, false),
				eq("Items[].Archived", row.Archived, false),
				eq("Items[].Fork", row.Fork, false),
				eq("Next", g.Next, forgeapi.Cursor("")),
				eq("Partial", g.Partial, (*forgeapi.Partial)(nil)),
				eq("Successor", g.Successor, (*forgeapi.RepoRef)(nil)),
			}...)
		},
	},
	{
		method: "PullRequests.ListPRs",
		zero:   forgeapi.Page[forgeapi.PullRequest]{},
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.PullRequests)
			if !ok {
				return nil, roleAbsent("PullRequests")
			}
			return r.ListPRs(ctx, s.repo)
		},
		items: func(got any) int { return len(got.(forgeapi.Page[forgeapi.PullRequest]).Items) },
		expect: func(p spec.Product, got any) []check {
			g := got.(forgeapi.Page[forgeapi.PullRequest])
			return append(pullRequestChecks("Items[].", p, first(g.Items), forgeapi.PRStateOpen), []check{
				eq("Next", g.Next, forgeapi.Cursor("")),
				eq("Partial", g.Partial, (*forgeapi.Partial)(nil)),
				eq("Successor", g.Successor, (*forgeapi.RepoRef)(nil)),
			}...)
		},
	},
	{
		method:    "PullRequests.ListMyPRs",
		zero:      forgeapi.Page[forgeapi.PullRequest]{},
		errorArms: []string{ownerUnresolved, scopeInsufficient},
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.PullRequests)
			if !ok {
				return nil, roleAbsent("PullRequests")
			}
			return r.ListMyPRs(ctx, ownerScope(s)...)
		},
		items: func(got any) int { return len(got.(forgeapi.Page[forgeapi.PullRequest]).Items) },
		expect: func(p spec.Product, got any) []check {
			g := got.(forgeapi.Page[forgeapi.PullRequest])
			return append(pullRequestChecks("Items[].", p, first(g.Items), forgeapi.PRStateOpen), []check{
				eq("Next", g.Next, forgeapi.Cursor("")),
				eq("Partial", g.Partial, (*forgeapi.Partial)(nil)),
				eq("Successor", g.Successor, (*forgeapi.RepoRef)(nil)),
			}...)
		},
	},
	{
		method:  "PullRequests.ReadPR",
		zero:    forgeapi.PullRequest{},
		readsPR: true,
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.PullRequests)
			if !ok {
				return nil, roleAbsent("PullRequests")
			}
			return r.ReadPR(ctx, s.repo, s.pr)
		},
		expect: func(p spec.Product, got any) []check {
			return pullRequestChecks("", p, got.(forgeapi.PullRequest), forgeapi.PRStateOpen)
		},
	},
	{
		method:   "PullRequests.CreatePR",
		zero:     forgeapi.PullRequest{},
		mutation: true,
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.PullRequests)
			if !ok {
				return nil, roleAbsent("PullRequests")
			}
			return r.CreatePR(ctx, s.repo, forgeapi.NewPullRequest{
				Title:        s.prefix + prTitle,
				Body:         prBody,
				SourceBranch: s.source,
				TargetBranch: s.target,
				Labels:       []string{s.label},
			})
		},
		expect: func(p spec.Product, got any) []check {
			return mutatedPullRequestChecks(p, got.(forgeapi.PullRequest), forgeapi.PRStateOpen)
		},
	},
	{
		method:   "PullRequests.ClosePR",
		zero:     forgeapi.PullRequest{},
		mutation: true,
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.PullRequests)
			if !ok {
				return nil, roleAbsent("PullRequests")
			}
			return r.ClosePR(ctx, s.repo, s.pr)
		},
		expect: func(p spec.Product, got any) []check {
			return mutatedPullRequestChecks(p, got.(forgeapi.PullRequest), forgeapi.PRStateClosed)
		},
	},
	{
		method:   "PullRequests.ReopenPR",
		zero:     forgeapi.PullRequest{},
		mutation: true,
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.PullRequests)
			if !ok {
				return nil, roleAbsent("PullRequests")
			}
			return r.ReopenPR(ctx, s.repo, s.pr)
		},
		expect: func(p spec.Product, got any) []check {
			return mutatedPullRequestChecks(p, got.(forgeapi.PullRequest), forgeapi.PRStateOpen)
		},
	},
	{
		method:   "PullRequests.RerunFailedChecks",
		zero:     nil,
		mutation: true,
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.PullRequests)
			if !ok {
				return nil, roleAbsent("PullRequests")
			}
			return nil, r.RerunFailedChecks(ctx, s.repo, s.pr, s.ref)
		},
		expect: func(spec.Product, any) []check { return nil },
	},
	{
		method:   "Merges.MergePR",
		zero:     forgeapi.MergeOutcome{},
		mutation: true,
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.Merges)
			if !ok {
				return nil, roleAbsent("Merges")
			}
			return r.MergePR(ctx, s.repo, s.pr, forgeapi.MergeRequest{
				Intent:  forgeapi.IntentDefault,
				HeadSHA: s.ref,
			})
		},
		expect: func(_ spec.Product, got any) []check {
			g := got.(forgeapi.MergeOutcome)
			return []check{
				// The contract's answer to a merge of a mergeable pull request is
				// the asynchronous product's accept, which leaves the caller no
				// verdict yet and carries the code saying so. The State and Code
				// rows name the SUBSET of the five outcome states a product that
				// completes the merge when it answers can reach, so its none
				// value is the merge itself, with no code.
				absent("State", g.State, forgeapi.MergeOutcomeAccepted, forgeapi.MergeOutcomeMerged),
				absent("Code", g.Code, forgeapi.CodeAlreadyEnqueued, ""),
				// The Gitea family always answers none, and the enumeration
				// discipline states that its fixed QueueNone counts as a
				// mapping because the suite asserts it as a value the family
				// PRODUCES rather than as an absence, so the value a product
				// that cannot supply the field answers is per product here.
				absentBy("QueueState", g.QueueState, forgeapi.QueueNone, forgeapi.QueueUnknown,
					map[spec.Product]any{spec.Gitea: forgeapi.QueueNone, spec.Forgejo: forgeapi.QueueNone}),
				eq("QueuePosition", g.QueuePosition, forgeapi.QueuePositionUnknown),
			}
		},
	},
	{
		method:  "Merges.MergeStatus",
		zero:    forgeapi.MergeStatus{},
		readsPR: true,
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.Merges)
			if !ok {
				return nil, roleAbsent("Merges")
			}
			return r.MergeStatus(ctx, s.repo, s.pr)
		},
		expect: func(_ spec.Product, got any) []check {
			g := got.(forgeapi.MergeStatus)
			return []check{
				eq("Merged", g.Merged, forgeapi.SupportNo),
				absent("Queue", g.Queue, forgeapi.QueueNone, forgeapi.QueueNone),
				eq("WebURL", g.WebURL, prWebURL),
				eq("Successor", g.Successor, (*forgeapi.RepoRef)(nil)),
			}
		},
	},
	{
		method: "Checks.CommitStatus",
		zero:   forgeapi.CommitChecks{},
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.Checks)
			if !ok {
				return nil, roleAbsent("Checks")
			}
			return r.CommitStatus(ctx, s.repo, s.ref)
		},
		expect: func(_ spec.Product, got any) []check {
			g := got.(forgeapi.CommitChecks)
			ctx0 := first(g.Contexts)
			return []check{
				eq("Ref", g.Ref, headSHA),
				eq("Contexts", len(g.Contexts), len(contexts)),
				eq("Contexts[].Name", ctx0.Name, contexts[0].Name),
				eq("Contexts[].Description", ctx0.Description, contexts[0].Description),
				eq("Contexts[].TargetURL", ctx0.TargetURL, contexts[0].TargetURL),
				eq("Contexts[].State", ctx0.State, contexts[0].State),
				eq("State", g.State, forgeapi.CheckPassing),
				eq("Passing", g.Passing, len(contexts)),
				eq("Failing", g.Failing, 0),
				eq("Pending", g.Pending, 0),
				eq("Neutral", g.Neutral, 0),
				eq("Unknown", g.Unknown, 0),
				eq("Total", g.Total, len(contexts)),
				eq("Partial", g.Partial, (*forgeapi.Partial)(nil)),
				eq("Successor", g.Successor, (*forgeapi.RepoRef)(nil)),
			}
		},
	},
	{
		method: "Checks.ListRuns",
		zero:   runPageZero(),
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			if _, ok := c.(forgeapi.Checks); !ok {
				return nil, roleAbsent("Checks")
			}
			return callListRuns(ctx, c, s.repo)
		},
		items: func(got any) int { return reflectedLen(reflected(got, "Items")) },
		expect: func(p spec.Product, got any) []check {
			row := firstReflected(reflected(got, "Items"))
			return []check{
				eq("Items[].Repo", fieldValue(row, "Repo"), repoRef(p)),
				eq("Items[].Name", fieldValue(row, "Name"), runName),
				eq("Items[].Branch", fieldValue(row, "Branch"), sourceBranch),
				eq("Items[].HeadSHA", fieldValue(row, "HeadSHA"), headSHA),
				eq("Items[].WebURL", fieldValue(row, "WebURL"), runWebURL(p)),
				eq("Items[].CreatedAt", fieldValue(row, "CreatedAt"), createdAt),
				eq("Items[].UpdatedAt", fieldValue(row, "UpdatedAt"), updatedAt),
				// The canonical run completed and failed, which every product's
				// status vocabulary folds to the verdict a failing check fold names.
				eq("Items[].State", fieldValue(row, "State"), forgeapi.CheckFailing),
				runsNext(p, fieldValue(reflect.ValueOf(got), "Next")),
				eq("Partial", fieldValue(reflect.ValueOf(got), "Partial"), (*forgeapi.Partial)(nil)),
				eq("Successor", fieldValue(reflect.ValueOf(got), "Successor"), (*forgeapi.RepoRef)(nil)),
			}
		},
	},
	{
		method: "Issues.ListIssues",
		zero:   forgeapi.Page[forgeapi.Issue]{},
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.Issues)
			if !ok {
				return nil, roleAbsent("Issues")
			}
			return r.ListIssues(ctx, s.repo)
		},
		items: func(got any) int { return len(got.(forgeapi.Page[forgeapi.Issue]).Items) },
		expect: func(p spec.Product, got any) []check {
			g := got.(forgeapi.Page[forgeapi.Issue])
			return append(issueChecks("Items[].", p, first(g.Items), forgeapi.IssueStateOpen), []check{
				eq("Next", g.Next, forgeapi.Cursor("")),
				eq("Partial", g.Partial, (*forgeapi.Partial)(nil)),
				eq("Successor", g.Successor, (*forgeapi.RepoRef)(nil)),
			}...)
		},
	},
	{
		method:    "Issues.ListMyIssues",
		zero:      forgeapi.Page[forgeapi.Issue]{},
		errorArms: []string{ownerUnresolved},
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			if _, ok := c.(forgeapi.Issues); !ok {
				return nil, roleAbsent("Issues")
			}
			r, ok := c.(crossRepositoryIssues)
			if !ok {
				return nil, methodAbsent("Issues.ListMyIssues")
			}
			return r.ListMyIssues(ctx, ownerScope(s)...)
		},
		items: func(got any) int { return len(got.(forgeapi.Page[forgeapi.Issue]).Items) },
		expect: func(p spec.Product, got any) []check {
			g := got.(forgeapi.Page[forgeapi.Issue])
			return append(issueChecks("Items[].", p, first(g.Items), forgeapi.IssueStateOpen), []check{
				eq("Next", g.Next, forgeapi.Cursor("")),
				eq("Partial", g.Partial, (*forgeapi.Partial)(nil)),
				eq("Successor", g.Successor, (*forgeapi.RepoRef)(nil)),
			}...)
		},
	},
	{
		method:   "Issues.CreateIssue",
		zero:     forgeapi.Issue{},
		mutation: true,
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.Issues)
			if !ok {
				return nil, roleAbsent("Issues")
			}
			return r.CreateIssue(ctx, s.repo, forgeapi.NewIssue{
				Title:  s.prefix + issueTitle,
				Body:   issueBody,
				Labels: []string{s.label},
			})
		},
		expect: func(p spec.Product, got any) []check {
			return issueChecks("", p, got.(forgeapi.Issue), forgeapi.IssueStateOpen)
		},
	},
	{
		method:   "Issues.CloseIssue",
		zero:     forgeapi.Issue{},
		mutation: true,
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.Issues)
			if !ok {
				return nil, roleAbsent("Issues")
			}
			return r.CloseIssue(ctx, s.repo, s.issue)
		},
		expect: func(p spec.Product, got any) []check {
			return issueChecks("", p, got.(forgeapi.Issue), forgeapi.IssueStateClosed)
		},
	},
	{
		method: "Capabilities.ConnectionCaps",
		zero:   forgeapi.ConnectionCaps{},
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.Capabilities)
			if !ok {
				return nil, roleAbsent("Capabilities")
			}
			return r.ConnectionCaps(ctx)
		},
		expect: func(_ spec.Product, got any) []check {
			g := got.(forgeapi.ConnectionCaps)
			return []check{
				absentBy("Caps[rerun_checks]", g.Caps[forgeapi.CapRerunChecks],
					forgeapi.SupportYes, forgeapi.SupportUnknown,
					map[spec.Product]any{spec.Forgejo: forgeapi.SupportNo}),
				present("Ev[rerun_checks].Source", g.Ev[forgeapi.CapRerunChecks].Source, forgeapi.EvidenceUnknown),
			}
		},
	},
	{
		method:    "Capabilities.GrantCaps",
		zero:      forgeapi.GrantCaps{},
		errorArms: []string{afterScopeRefusal},
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.Capabilities)
			if !ok {
				return nil, roleAbsent("Capabilities")
			}
			return r.GrantCaps(ctx)
		},
		expect: func(_ spec.Product, got any) []check {
			g := got.(forgeapi.GrantCaps)
			return []check{
				eq("Caps[read_merge_state]", g.Caps[forgeapi.CapReadMergeState], forgeapi.SupportYes),
				present("Ev", len(g.Ev), 0),
			}
		},
	},
	{
		method:    "Capabilities.RepoAffordances",
		zero:      forgeapi.RepoAffordances{},
		errorArms: []string{scopeInsufficient},
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.Capabilities)
			if !ok {
				return nil, roleAbsent("Capabilities")
			}
			return r.RepoAffordances(ctx, s.repo)
		},
		expect: func(p spec.Product, got any) []check {
			return affordanceChecks("", got.(forgeapi.RepoAffordances))
		},
	},
	{
		method:  "Governor.BudgetState",
		zero:    forgeapi.BudgetState{},
		prelude: "Repos.ListRepos",
		invoke: func(_ context.Context, c any, _ subject) (any, error) {
			r, ok := c.(forgeapi.Governor)
			if !ok {
				return nil, roleAbsent("Governor")
			}
			return r.BudgetState(), nil
		},
		expect: func(_ spec.Product, got any) []check {
			g := got.(forgeapi.BudgetState)
			return []check{
				absent("Remaining", g.Remaining, remaining, forgeapi.BudgetRemainingUnknown),
				present("Reset", g.Reset, time.Time{}),
				// The two rows naming this field say the library's own per-call
				// price answers where the product sends no cost, and the read
				// this case runs first costs one request on every product.
				absent("LastCost", g.LastCost, lastCost, lastCost),
				eq("RotationCursor", g.RotationCursor, forgeapi.RotationCursor("")),
			}
		},
	},
	{
		method: "Releases.ListReleases",
		zero:   forgeapi.Page[forgeapi.Release]{},
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.Releases)
			if !ok {
				return nil, roleAbsent("Releases")
			}
			return r.ListReleases(ctx, s.repo)
		},
		items: func(got any) int { return len(got.(forgeapi.Page[forgeapi.Release]).Items) },
		expect: func(_ spec.Product, got any) []check {
			g := got.(forgeapi.Page[forgeapi.Release])
			return append(releaseChecks("Items[].", first(g.Items)), []check{
				eq("Next", g.Next, forgeapi.Cursor("")),
				eq("Partial", g.Partial, (*forgeapi.Partial)(nil)),
				eq("Successor", g.Successor, (*forgeapi.RepoRef)(nil)),
			}...)
		},
	},
	{
		method:   "Releases.CreateRelease",
		zero:     forgeapi.Release{},
		mutation: true,
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.Releases)
			if !ok {
				return nil, roleAbsent("Releases")
			}
			return r.CreateRelease(ctx, s.repo, forgeapi.NewRelease{
				TagName:    s.tag,
				Name:       s.prefix + releaseName,
				Body:       releaseBody,
				Target:     s.target,
				Prerelease: true,
			})
		},
		expect: func(_ spec.Product, got any) []check {
			return releaseChecks("", got.(forgeapi.Release))
		},
	},
	{
		method: "Labels.ListLabels",
		zero:   forgeapi.Page[forgeapi.Label]{},
		invoke: func(ctx context.Context, c any, s subject) (any, error) {
			r, ok := c.(forgeapi.Labels)
			if !ok {
				return nil, roleAbsent("Labels")
			}
			return r.ListLabels(ctx, s.repo)
		},
		items: func(got any) int { return len(got.(forgeapi.Page[forgeapi.Label]).Items) },
		expect: func(_ spec.Product, got any) []check {
			g := got.(forgeapi.Page[forgeapi.Label])
			row := first(g.Items)
			return []check{
				eq("Items[].Name", row.Name, labelName),
				present("Items[].Color", row.Color, ""),
				eq("Items[].Description", row.Description, labelDesc),
				eq("Next", g.Next, forgeapi.Cursor("")),
				eq("Partial", g.Partial, (*forgeapi.Partial)(nil)),
				eq("Successor", g.Successor, (*forgeapi.RepoRef)(nil)),
			}
		},
	},
}

// pullRequestChecks is the normalized pull request, one statement serving the
// single read and both list rows, since a list item and a read return the same type
// and the same vocabulary.
func pullRequestChecks(prefix string, p spec.Product, g forgeapi.PullRequest, state forgeapi.PRState) []check {
	label := first(g.Labels)
	a := g.Action
	return []check{
		eq(prefix+"Ref", g.Ref, prRef(p)),
		eq(prefix+"Repo", g.Repo, repoRef(p)),
		eq(prefix+"Title", g.Title, prTitle),
		eq(prefix+"Body", g.Body, prBody),
		eq(prefix+"Author", g.Author, author),
		eq(prefix+"SourceBranch", g.SourceBranch, sourceBranch),
		// The fixture's pull request is opened inside its repository (ADR-0104).
		eq(prefix+"SourceRepo", g.SourceRepo, repoRef(p)),
		eq(prefix+"TargetBranch", g.TargetBranch, targetBranch),
		eq(prefix+"WebURL", g.WebURL, prWebURL),
		eq(prefix+"HeadSHA", g.HeadSHA, headSHA),
		eq(prefix+"Labels[].Name", label.Name, labelName),
		present(prefix+"Labels[].Color", label.Color, ""),
		eq(prefix+"Labels[].Description", label.Description, labelDesc),
		eq(prefix+"CreatedAt", g.CreatedAt, createdAt),
		eq(prefix+"UpdatedAt", g.UpdatedAt, updatedAt),
		eq(prefix+"State", g.State, state),
		eq(prefix+"Draft", g.Draft, false),
		eq(prefix+"Action.Mergeable", a.Mergeable, forgeapi.SupportYes),
		eq(prefix+"Action.Checks", a.Checks, forgeapi.CheckPassing),
		eq(prefix+"Action.ChecksPassing", a.ChecksPassing, len(contexts)),
		eq(prefix+"Action.ChecksFailing", a.ChecksFailing, 0),
		eq(prefix+"Action.ChecksPending", a.ChecksPending, 0),
		eq(prefix+"Action.ChecksNeutral", a.ChecksNeutral, 0),
		eq(prefix+"Action.ChecksUnknown", a.ChecksUnknown, 0),
		eq(prefix+"Action.ChecksTotal", a.ChecksTotal, len(contexts)),
		eq(prefix+"Action.AutoMergeArmed", a.AutoMergeArmed, forgeapi.SupportNo),
		absent(prefix+"Action.QueueState", a.QueueState, forgeapi.QueueNone, forgeapi.QueueNone),
		// The unknown position crosses as -1 on every product, because the zero
		// would read as next-to-merge and this wire has no other sentinel for an
		// integer, so a product that cannot supply a position answers the same
		// value the contract states rather than that integer's zero.
		absent(prefix+"Action.QueuePosition", a.QueuePosition, forgeapi.QueuePositionUnknown, forgeapi.QueuePositionUnknown),
		eq(prefix+"Action.MergeBlocked", a.MergeBlocked, forgeapi.MergeBlockNone),
		eq(prefix+"Partial", g.Partial, (*forgeapi.Partial)(nil)),
	}
}

// mutatedPullRequestChecks is the pull request a mutation returns. Every product's
// table row says a mutation carries no check state and no label row of its own, so
// those fields are asserted as absent. The mergeable flag and the armed auto-merge
// keep the read's statement, because each mutation's own row states them from that
// mutation's captured answer, and a product that cannot supply one there answers the
// unknown the departure names.
func mutatedPullRequestChecks(p spec.Product, g forgeapi.PullRequest, state forgeapi.PRState) []check {
	var out []check
	for _, c := range pullRequestChecks("", p, g, state) {
		switch c.path {
		case "Action.Checks":
			c = absent(c.path, g.Action.Checks, forgeapi.CheckUnknown, forgeapi.CheckUnknown)
		case "Action.ChecksPassing", "Action.ChecksTotal":
			c = absent(c.path, c.got, 0, 0)
		case "Labels[].Name":
			c = absent(c.path, c.got, labelName, "")
		case "Labels[].Description":
			c = absent(c.path, c.got, labelDesc, "")
		}
		out = append(out, c)
	}
	return out
}

// issueChecks is the normalized issue.
func issueChecks(prefix string, p spec.Product, g forgeapi.Issue, state forgeapi.IssueState) []check {
	label := first(g.Labels)
	return []check{
		eq(prefix+"Ref", g.Ref, forgeapi.IssueRef{Number: issueNumber}),
		eq(prefix+"Repo", g.Repo, repoRef(p)),
		eq(prefix+"Title", g.Title, issueTitle),
		eq(prefix+"Body", g.Body, issueBody),
		eq(prefix+"Author", g.Author, author),
		eq(prefix+"WebURL", g.WebURL, issueWebURL),
		eq(prefix+"Labels[].Name", label.Name, labelName),
		present(prefix+"Labels[].Color", label.Color, ""),
		eq(prefix+"Labels[].Description", label.Description, labelDesc),
		eq(prefix+"CreatedAt", g.CreatedAt, createdAt),
		eq(prefix+"UpdatedAt", g.UpdatedAt, updatedAt),
		eq(prefix+"State", g.State, state),
	}
}

// affordanceChecks is the normalized repository affordances, one statement serving
// the accessor and the listing row that carries the same record.
func affordanceChecks(prefix string, g forgeapi.RepoAffordances) []check {
	return []check{
		// Presence is the whole claim here: the members are the family's own
		// spellings of what the repository allows, which
		// TestGiteaFamilyListsTheMergeStrategiesItsRecordSwitchesOn holds per flag.
		present(prefix+"MergeStrategies", len(g.MergeStrategies), 0),
		// On GitLab these two are three-valued rather than boolean because an
		// anonymous read of that product's project record answers neither, so
		// "no issues" and "cannot see" are different answers there.
		eq(prefix+"HasIssues", g.HasIssues, forgeapi.SupportYes),
		eq(prefix+"CanPush", g.CanPush, forgeapi.SupportYes),
		// Unknown on GitHub, where a merge queue is a ruleset property and the
		// only read that answers it costs a request per ruleset the budget does
		// not price; No on the Gitea family, where the absence is a fixed
		// product property its version body settles.
		absentBy(prefix+"MergeTrain", g.MergeTrain, forgeapi.SupportYes, forgeapi.SupportUnknown,
			map[spec.Product]any{spec.Gitea: forgeapi.SupportNo, spec.Forgejo: forgeapi.SupportNo}),
		present(prefix+"Ev", len(g.Ev), 0),
		eq(prefix+"DefaultBranch", g.DefaultBranch, targetBranch),
	}
}

// The run listing answers a page of a run record, and the suite reaches both by
// reflection: the method and its row type are named, so the case compiles before
// either is declared and reports their absence as a failure of its own instead of
// failing the build of every other case.

// listRunsMethod is the run listing as the Checks role declares it, and false
// where the role declares none.
func listRunsMethod() (reflect.Method, bool) {
	return reflect.TypeFor[forgeapi.Checks]().MethodByName("ListRuns")
}

// runPageZero is the run listing's answer at its zero value, or nil where the role
// declares no run listing, which is the one answer type the zero-answer red check
// cannot be handed then.
func runPageZero() any {
	m, ok := listRunsMethod()
	if !ok || m.Type.NumOut() == 0 {
		return nil
	}
	return reflect.Zero(m.Type.Out(0)).Interface()
}

// callListRuns drives one client's run listing for one repository.
func callListRuns(ctx context.Context, c any, repo forgeapi.RepoRef, opts ...forgeapi.ListOption) (any, error) {
	m := reflect.ValueOf(c).MethodByName("ListRuns")
	if !m.IsValid() {
		return nil, methodAbsent("Checks.ListRuns")
	}
	args := []reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(repo)}
	for _, o := range opts {
		args = append(args, reflect.ValueOf(o))
	}
	out := m.Call(args)
	if len(out) != 2 {
		return nil, fmt.Errorf("Checks.ListRuns answered %d values, want a page and an error", len(out))
	}
	var err error
	if e := out[1].Interface(); e != nil {
		err = e.(error)
	}
	return out[0].Interface(), err
}

// reflected is one named field of a struct answer, invalid where the answer is
// absent or carries no such field.
func reflected(got any, name string) reflect.Value {
	v := reflect.ValueOf(got)
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	return v.FieldByName(name)
}

// reflectedLen is the length of a reflected slice, zero where there is none.
func reflectedLen(v reflect.Value) int {
	if !v.IsValid() || v.Kind() != reflect.Slice {
		return 0
	}
	return v.Len()
}

// firstReflected is a reflected slice's first element, the element type's zero
// where the slice is empty, which is what first does for a typed one.
func firstReflected(items reflect.Value) reflect.Value {
	if !items.IsValid() || items.Kind() != reflect.Slice {
		return reflect.Value{}
	}
	if items.Len() == 0 {
		return reflect.Zero(items.Type().Elem())
	}
	return items.Index(0)
}

// fieldValue is one named field of a reflected struct as the value it holds, nil
// where the struct is absent or declares no such field, so a missing field fails
// the comparison it was named in rather than the run.
func fieldValue(v reflect.Value, name string) any {
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return nil
	}
	f := v.FieldByName(name)
	if !f.IsValid() {
		return nil
	}
	return f.Interface()
}

// releaseChecks is the normalized release.
func releaseChecks(prefix string, g forgeapi.Release) []check {
	return []check{
		eq(prefix+"TagName", g.TagName, tagName),
		eq(prefix+"Name", g.Name, releaseName),
		eq(prefix+"Body", g.Body, releaseBody),
		eq(prefix+"WebURL", g.WebURL, releaseWebURL),
		eq(prefix+"PublishedAt", g.PublishedAt, published),
		eq(prefix+"Draft", g.Draft, false),
		eq(prefix+"Prerelease", g.Prerelease, true),
	}
}
