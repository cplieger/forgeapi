package forgeapi

import (
	"fmt"
	"time"
)

// ListState filters a pull-request or issue listing.
type ListState int

// The ListState members.
const (
	// ListStateUnknown is the zero value and is refused at the call site
	// rather than sent, so a filter nobody chose cannot become a listing
	// nobody expected.
	ListStateUnknown ListState = iota
	// ListStateOpen lists open items. It is what a list defaults to when no
	// state option is given.
	ListStateOpen
	// ListStateClosed lists closed items.
	ListStateClosed
	// ListStateMerged lists merged pull requests. It is not a valid issue
	// filter and a family package refuses it there, and it is meaningless on
	// the lists [WithState] does not reach at all.
	ListStateMerged
	// ListStateAll lists every state.
	ListStateAll
)

// String returns the member's spelling: "unknown", "open", "closed", "merged"
// or "all".
func (l ListState) String() string {
	return nameOf([]string{nameUnknown, stateOpenName, stateClosedName, stateMergedName, "all"}, l)
}

var _ fmt.Stringer = ListState(0)

// Account is the authenticated identity on one connection.
type Account struct {
	// Login is the account name, which is also the username a git credential
	// helper hands over on the family that expects one.
	Login string
	// Name is the display name where the instance exposes one.
	Name string
	// Email is the account email where the instance exposes one.
	Email string
	// WebURL is the account's page on the instance.
	WebURL string
	// Scopes are the granted scopes where the family reports them, which is
	// what makes a grant capability evidence rather than inference. It is nil
	// where the family does not.
	Scopes []string
}

// Repository is one repository a connection can reach.
type Repository struct {
	// Ref addresses it.
	Ref RepoRef
	// Description is the repository description.
	Description string
	// WebURL is its page on the instance.
	WebURL string
	// CloneURL is the HTTPS clone URL, whose origin is the one a credential
	// helper answers for.
	CloneURL string
	// UpdatedAt is the last update the instance reported, zero where it
	// reported none.
	UpdatedAt time.Time
	// Affordances is what this repository allows, including its default
	// branch. It is the repository-scoped capability record and carries its own
	// evidence semantics.
	Affordances RepoAffordances
	// Private reports a private repository.
	Private bool
	// Archived reports an archived repository, where every mutation is refused
	// with [CodeRepoArchived] until an operator unarchives it.
	Archived bool
	// Fork reports a fork.
	Fork bool
}

// PRState is the state of one pull request.
type PRState int

// The PRState members.
const (
	// PRStateUnknown is the zero value: the state did not map, which is what an
	// omitted wire field on an old instance must reach a consumer as.
	PRStateUnknown PRState = iota
	// PRStateOpen is open, draft included.
	PRStateOpen
	// PRStateClosed is closed without merging.
	PRStateClosed
	// PRStateMerged is merged.
	PRStateMerged
)

// String returns the member's spelling: "unknown", "open", "closed" or
// "merged".
func (p PRState) String() string {
	return nameOf([]string{nameUnknown, stateOpenName, stateClosedName, stateMergedName}, p)
}

var _ fmt.Stringer = PRState(0)

// PullRequest is one pull request, called a merge request on one family.
type PullRequest struct { //nolint:govet // fieldalignment: field order is the reading order this type documents, addressing first, and it is API for an unkeyed composite literal
	// Ref addresses it, sigil included.
	Ref PRRef
	// Repo is the repository it belongs to, so a row carries its own
	// addressing rather than depending on the call it arrived from.
	Repo RepoRef
	// Title is its title.
	Title string
	// Body is its description.
	Body string
	// Author is the account that opened it.
	Author string
	// SourceBranch is the branch being merged.
	SourceBranch string
	// SourceRepo is the repository SourceBranch lives in: Repo for a pull
	// request opened inside its repository, the fork for one opened from a
	// fork. It is the zero RepoRef where the answer names no head repository
	// the credential can read: a deleted fork on every family, a fork the
	// credential cannot see and, where a product's table says so, a route whose
	// answer does not carry it; a consumer joining a local clone matches it
	// beside Repo.
	SourceRepo RepoRef
	// TargetBranch is the branch it merges into.
	TargetBranch string
	// WebURL is its page on the instance.
	WebURL string
	// HeadSHA is the head commit of the source branch: the value a merge and a
	// re-run pin themselves to.
	HeadSHA string
	// Labels are the labels on it, nil on a family that has none. Where the
	// product served part of the set, [PullRequest.Partial] says so.
	Labels []Label
	// CreatedAt is when it was opened, zero where the instance reported none.
	CreatedAt time.Time
	// UpdatedAt is its last update, zero where the instance reported none.
	UpdatedAt time.Time
	// Action is everything its own controls need, and it rides the item
	// because every field in it is a property of this pull request.
	Action ActionState
	// Partial is non-nil when something this pull request needed stopped short, and
	// nil on every complete row. It carries [PartialPaginationCap] where the check
	// fold or, on GitLab, the labels stopped short (that member names each path), and
	// beside it [ActionState.Checks] stays exact where the product counted the whole
	// collection, only the check names short, and is [CheckUnknown] where it did not;
	// [PartialBudget] where the per-interval folded-status cap left the fold unissued;
	// [PartialRateLimited] where a read the row needed was deferred or throttled, the
	// Gitea family's folded status or GitLab's fork lookup, [PullRequest.SourceRepo]
	// then the zero reference on GitLab;
	// and [PartialLabelsNotApplied] on the pull request a creation answers beside its
	// error where the labels it named were not applied.
	Partial *Partial
	// State is open, closed or merged.
	State PRState
	// Draft reports a draft pull request.
	Draft bool
}

// NewPullRequest describes a pull request to open.
type NewPullRequest struct {
	// Title is required.
	Title string
	// Body is the description.
	Body string
	// SourceBranch is the branch to merge.
	SourceBranch string
	// TargetBranch is the branch to merge into.
	TargetBranch string
	// Labels are labels to apply, refused on a family with no pull-request
	// labels rather than dropped.
	Labels []string
	// Draft opens it as a draft.
	Draft bool
}

// IssueState is the state of one issue.
type IssueState int

// The IssueState members.
const (
	// IssueStateUnknown is the zero value: the state did not map.
	IssueStateUnknown IssueState = iota
	// IssueStateOpen is open.
	IssueStateOpen
	// IssueStateClosed is closed.
	IssueStateClosed
)

// String returns the member's spelling: "unknown", "open" or "closed".
func (i IssueState) String() string {
	return nameOf([]string{nameUnknown, stateOpenName, stateClosedName}, i)
}

var _ fmt.Stringer = IssueState(0)

// Issue is one issue.
type Issue struct { //nolint:govet // fieldalignment: field order is the reading order this type documents, addressing first, and it is API for an unkeyed composite literal
	// Ref addresses it.
	Ref IssueRef
	// Repo is the repository it belongs to.
	Repo RepoRef
	// Title is its title.
	Title string
	// Body is its description.
	Body string
	// Author is the account that filed it.
	Author string
	// WebURL is its page on the instance.
	WebURL string
	// Labels are the labels on it.
	Labels []Label
	// CreatedAt is when it was filed, zero where the instance reported none.
	CreatedAt time.Time
	// UpdatedAt is its last update, zero where the instance reported none.
	UpdatedAt time.Time
	// State is open or closed.
	State IssueState
}

// NewIssue describes an issue to file.
type NewIssue struct {
	// Title is required.
	Title string
	// Body is the description.
	Body string
	// Labels are labels to apply.
	Labels []string
}

// Release is one tagged release.
type Release struct { //nolint:govet // fieldalignment: field order is the reading order this type documents, the tag first, and it is API for an unkeyed composite literal
	// TagName is the tag it points at.
	TagName string
	// Name is its title.
	Name string
	// Body is its notes.
	Body string
	// WebURL is its page on the instance.
	WebURL string
	// PublishedAt is when it was published, zero for an unpublished draft.
	PublishedAt time.Time
	// Draft reports an unpublished release.
	Draft bool
	// Prerelease reports a prerelease.
	Prerelease bool
}

// NewRelease describes a release to cut.
type NewRelease struct {
	// TagName is required.
	TagName string
	// Name is the title.
	Name string
	// Body is the notes.
	Body string
	// Target is the commit SHA or branch the tag is created at, where the tag
	// does not exist yet.
	Target string
	// Draft creates it unpublished.
	Draft bool
	// Prerelease marks it a prerelease.
	Prerelease bool
}

// Label is one label defined on a repository.
type Label struct {
	// Name is the label.
	Name string
	// Color is its colour as the instance spells it.
	Color string
	// Description is its description.
	Description string
}

// CheckContext is one reporting check on a commit: one context, one verdict.
type CheckContext struct {
	// Name is the context name upstream reported.
	Name string
	// Description is upstream's own description, sanitized and bounded.
	Description string
	// TargetURL is where a human reads the check, empty where none was given.
	TargetURL string
	// State is this context's own mapped state. An upstream value outside the
	// mapping table yields CheckUnknown, a counter increment and a log line
	// naming the value, rather than a guess.
	State CheckState
}

// CommitChecks is the folded CI verdict for one commit, with the contexts it was
// folded from.
//
// State is computed HERE, over the contexts actually held, rather than taken
// from an upstream folded field: on one family that endpoint is paginated and
// both its folded state and its total are computed over the returned PAGE, so a
// page of successes reports success for a commit that is failing and the
// truncation is not detectable from the body at all.
type CommitChecks struct { //nolint:govet // fieldalignment: field order is the reading order this type documents, the commit first, and it is API for an unkeyed composite literal
	// Ref is the commit the checks belong to: the SHA the read resolved the
	// requested ref to, so a branch asked for comes back as its head commit. It
	// is the ref as asked only where the answer named no commit, which includes
	// a page bound that admitted no read.
	Ref string
	// Contexts are the checks read, in the order upstream returned them.
	Contexts []CheckContext
	// Partial is non-nil when the fold stopped short of every context, on the
	// same per-product terms as [PullRequest.Partial]: where the response carried
	// counts over the whole collection, State and the counts stay exact and what
	// is short is the context NAMES, and where it did not, State is CheckUnknown
	// rather than a verdict over part of the evidence.
	Partial *Partial
	// Successor is non-nil when the repository this read addressed had moved and
	// the read followed the redirect to it, on the same terms as
	// [Page.Successor].
	Successor *RepoRef
	// State is the fold.
	State CheckState
	// Passing is the number of contexts in CheckPassing.
	Passing int
	// Failing is the number in CheckFailing.
	Failing int
	// Pending is the number in CheckPending.
	Pending int
	// Neutral is the number in CheckNeutral.
	Neutral int
	// Unknown is the number whose upstream value did not map.
	Unknown int
	// Total is the number of contexts the fold read.
	Total int
}

// Run is one CI run of one repository: a GitHub Actions workflow run, a GitLab
// pipeline, or a Gitea or Forgejo Actions run.
//
// It carries no id, no number and no trigger event. An id is a product token
// this surface does not hand out, and the event is one product's vocabulary
// with no counterpart on the others.
type Run struct { //nolint:govet // fieldalignment: field order is the reading order this type documents, addressing first, and it is API for an unkeyed composite literal
	// Repo is the repository the run belongs to.
	Repo RepoRef
	// Name is the workflow's or pipeline's display name, empty where the
	// product names none for this run.
	Name string
	// Branch is the branch the run ran for, as the product spells the ref.
	Branch string
	// HeadSHA is the commit the run ran on.
	HeadSHA string
	// WebURL is the run's page on the instance.
	WebURL string
	// CreatedAt is when the run was created.
	CreatedAt time.Time
	// UpdatedAt is when the run last changed.
	UpdatedAt time.Time
	// State is folded from the run's status and conclusion, in the verdict a
	// check fold names: a failed run is [CheckFailing], and one still running
	// is [CheckPending]. An upstream value outside the product's mapping table
	// yields CheckUnknown, a counter increment and a log line naming it.
	State CheckState
}
