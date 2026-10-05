package forgeapi

import "fmt"

// Support is a three-valued answer to "can this be done here?".
//
// The zero value is SupportUnknown, deliberately: an unpopulated field or a
// missing map key must read as an honest unknown rather than as a wrong green. A
// bool cannot carry this shape, because it has one value for "no" and for "could
// not see", and the two need different treatment at a control the user is about
// to click.
type Support int

// The Support members.
const (
	// SupportUnknown is the zero value: nothing answered, so a consumer
	// renders a disabled control WITH its reason, visibly different from a
	// control that is absent.
	SupportUnknown Support = iota
	// SupportYes means an evidence source said the capability is available.
	SupportYes
	// SupportNo means an evidence source said it is not.
	SupportNo
)

// String returns the member's wire spelling: "unknown", "yes" or "no". Every
// Support-typed field crosses a consumer's wire in this spelling and never as a
// JSON boolean, so a field that could not be read arrives as unknown rather than
// as disarmed.
func (s Support) String() string {
	return nameOf([]string{nameUnknown, "yes", "no"}, s)
}

var _ fmt.Stringer = Support(0)

// Capability names one thing a connection, a credential grant or a repository
// may be able to do. It is the key of all three capability maps.
//
// The set is deliberately left open: nothing licenses a family to add a member,
// and nothing closes the set either, so a new member is additive.
type Capability string

// The capabilities named so far.
const (
	// CapRerunChecks is connection-scoped and sits behind
	// [PullRequests.RerunFailedChecks]: a product with no re-run verb, or an
	// instance too old to serve one, reports it no or unknown with its
	// evidence.
	CapRerunChecks Capability = "rerun_checks"
	// CapReadMergeState is grant-scoped: the merge state a Merge control reads
	// needs push permission on one product, so a token without it makes the
	// field unknown rather than the feature absent. Reporting this as
	// unsupported would tell a user their instance lacks a feature their token
	// merely cannot see.
	CapReadMergeState Capability = "read_merge_state"
	// CapHasIssues keys [RepoAffordances.HasIssues] in
	// [RepoAffordances.Ev].
	CapHasIssues Capability = "has_issues"
	// CapCanPush keys [RepoAffordances.CanPush] in [RepoAffordances.Ev].
	CapCanPush Capability = "can_push"
	// CapMergeTrain keys [RepoAffordances.MergeTrain] in
	// [RepoAffordances.Ev].
	CapMergeTrain Capability = "merge_train"
)

// EvidenceSource is the step of the resolution order that answered a
// capability.
//
// It is a defined type rather than a bare string because the membership is
// closed: there are seven steps, one per Evidence* constant below, and a family
// cannot add an eighth. Its zero value is [EvidenceUnknown], no step at all,
// under the same rule every enumeration here follows: an [Evidence] nobody
// populated reads as unknown rather than as one of the steps. Its underlying
// type is string because a step's member IS the wire spelling, so no separate
// rendering is needed for the seven.
type EvidenceSource string

// EvidenceUnknown is the zero EvidenceSource: no step of the resolution order
// answered, because the Evidence was never populated. It is the one member
// whose value is not its wire spelling, since the zero of a string is empty
// and an empty source on the wire would be indistinguishable from an omitted
// key, so the envelope renders it as "unknown".
const EvidenceUnknown EvidenceSource = ""

// String returns the member's wire spelling: each of the seven steps as it is
// spelled, and "unknown" for [EvidenceUnknown], whose value is empty.
//
// It exists for that one member. The other fourteen enumerations on this surface
// render through their own String methods, all fourteen of them defined types
// over int; of the FOUR string-based types this is the only one with a member
// whose value is not its spelling, since [Cursor] and [RotationCursor] are opaque
// by contract and a [Capability] member IS its spelling. So without this method
// an [Evidence] nobody populated would format through %s or %v as the empty
// string, which on the wire is indistinguishable from an omitted key.
func (s EvidenceSource) String() string {
	if s == EvidenceUnknown {
		return nameUnknown
	}
	return string(s)
}

var _ fmt.Stringer = EvidenceSource("")

// Evidence records WHY a capability reads the way it does, so an unknown can be
// rendered with a reason instead of as a silent absence.
type Evidence struct {
	// Source is the step of the resolution order that answered: one of the
	// seven Evidence* constants, one per step, or [EvidenceUnknown] where none
	// did.
	Source EvidenceSource
	// Detail is the version, the endpoint, or why the answer is unknown.
	Detail string
}

// The seven evidence sources, in resolution order: cheapest first.
//
// EvidenceSwagger sits ahead of EvidenceDefault and not after it. Default is
// terminal by definition, being the answer when nothing else answered, so an
// order that reached it would stop there and no capability would ever justify
// fetching a swagger document approaching a megabyte.
const (
	// EvidenceResponseHeader is a header on a response the client already
	// made.
	EvidenceResponseHeader EvidenceSource = "response-header"
	// EvidenceResponseBody is a field in the resource's own authenticated
	// response body, which is where a RepoAffordances value normally comes
	// from; the exception is [RepoAffordances.MergeTrain] on the Gitea family,
	// a fixed product property answered from the version body.
	EvidenceResponseBody EvidenceSource = "response-body"
	// EvidenceMetadata is an authenticated metadata endpoint.
	EvidenceMetadata EvidenceSource = "metadata"
	// EvidenceProbe is a bounded probe of the specific endpoint.
	EvidenceProbe EvidenceSource = "probe"
	// EvidenceVersion is inference from the instance's version. It is the
	// cheapest source in practice for one family, whose products send no
	// version header at all and separate themselves in the version body.
	EvidenceVersion EvidenceSource = "version"
	// EvidenceSwagger is the instance's swagger document, fetched under decode
	// bounds and cached with the connection, for a capability no cheaper
	// source answered. Its absence is SupportUnknown rather than an error: the
	// document is nearly a megabyte on two products and can be switched off on
	// a working instance.
	EvidenceSwagger EvidenceSource = "swagger"
	// EvidenceDefault is the answer when nothing else answered.
	EvidenceDefault EvidenceSource = "default"
)

// ConnectionCaps is what this instance can do, whoever is asking.
//
// It is a separate scope from GrantCaps and RepoAffordances because the three
// answer differently: a capability can be disabled per repository on three of
// the four products, unauthorized per credential, or unknown for want of a safe
// probe. One boolean map conflated all of them.
type ConnectionCaps struct {
	// Caps is the verdict per capability. A missing key reads as
	// SupportUnknown.
	Caps map[Capability]Support
	// Ev is the evidence behind each verdict, keyed the same way.
	Ev map[Capability]Evidence
}

// GrantCaps is what this credential can do on this instance. A capability the
// token cannot see is a grant fact, not a software fact.
type GrantCaps struct {
	// Caps is the verdict per capability. A missing key reads as
	// SupportUnknown.
	Caps map[Capability]Support
	// Ev is the evidence behind each verdict, keyed the same way.
	Ev map[Capability]Evidence
}

// RepoAffordances is what this repository allows, read from its own
// authenticated record wherever the record answers.
//
// One field is not read from it and the exception is named rather than left to
// the table: on the Gitea family [RepoAffordances.MergeTrain] is fixed by the
// PRODUCT rather than configured per repository, so it is answered from the
// instance's version body under [EvidenceVersion]. A fixed product property
// carries the evidence that proves it, which is why [RepoAffordances.Ev] is
// keyed per field rather than stated once for the struct.
type RepoAffordances struct { //nolint:govet // fieldalignment: field order here is declared order, Ev included, which sits beside the fields it explains, and it is API for an unkeyed composite literal
	// MergeStrategies is the merge strategies this repository allows, in the
	// family's own spellings: a style the repository record switches on, never
	// one it disables. On a family whose merge takes no strategy (GitLab), it is
	// the project's merge method and whether squash is allowed, display text no
	// [MergeRequest.Strategy] takes; squash is expressed there through
	// [MergeRequest.Intent]. [MergeRequest.Strategy] is checked locally against the
	// family's closed set rather than this list, so a strategy the repository
	// disables comes back as the forge's own refusal.
	MergeStrategies []string
	// HasIssues is not a bool: one product varies the field by permission, so
	// "no issues" and "cannot see" are different answers.
	HasIssues Support
	// CanPush is not a bool: one product reports a role integer, from which
	// absence of push and absence of visibility are not the same.
	CanPush Support
	// MergeTrain reports whether merging here goes through a queue or train,
	// never inferred from a refusal. What answered it is what [Ev] records:
	// the project's own setting on the family that configures it per
	// repository, and the instance's version body on the family where it is a
	// fixed product property, per the exception this struct's own doc names.
	MergeTrain Support
	// Ev is the evidence behind each Support-typed field above, keyed by
	// [CapHasIssues], [CapCanPush] and [CapMergeTrain] exactly as the two
	// sibling scopes are keyed, so an unknown affordance arrives with a reason
	// to render in the same {support, source, detail} shape a connection or
	// grant capability gives it, rather than as a silent absence. The fields
	// that are not Support carry their own values and have no entry here.
	//
	// It sits beside the fields it explains rather than appended after them,
	// which is why the directive on this struct names it: moving it last, so
	// that the other fields read as a prefix, gives this struct a leading run
	// of pointer data that costs [Repository] eight bytes of it and a
	// fieldalignment finding of its own.
	Ev map[Capability]Evidence
	// DefaultBranch is the repository's default branch.
	DefaultBranch string
}

// CheckState is the folded CI verdict for one commit.
type CheckState int

// The CheckState members. Each has a count of its own in ActionState, because
// the fold is policy and a consumer may want to parameterize it.
const (
	// CheckUnknown is the zero value: no check state was read, which includes
	// the case where a paginated status endpoint was truncated.
	CheckUnknown CheckState = iota
	// CheckPassing means every check that reported succeeded.
	CheckPassing
	// CheckFailing means at least one check reported failure.
	CheckFailing
	// CheckPending means at least one check is still running and none failed.
	CheckPending
	// CheckNeutral means the checks that reported were neutral or skipped. It
	// is unreachable through one product's rollup field and only visible per
	// check run, which is why a document selects the runs.
	CheckNeutral
)

// String returns the member's wire spelling: "unknown", "passing", "failing",
// "pending" or "neutral".
func (c CheckState) String() string {
	return nameOf([]string{nameUnknown, "passing", "failing", "pending", "neutral"}, c)
}

var _ fmt.Stringer = CheckState(0)

// MergeBlockReason is why a forge refuses to merge one pull request.
//
// It is not a Support: a three-valued type cannot tell "behind" from "conflicts"
// from "blocked", and that reason is what a Merge control renders.
type MergeBlockReason int

// The MergeBlockReason members: seven refusal causes plus MergeBlockNone.
const (
	// MergeBlockUnknown is the zero value: the cause could not be read. It is
	// what a degraded read reports rather than claiming the merge is clear.
	MergeBlockUnknown MergeBlockReason = iota
	// MergeBlockNone means nothing blocks this merge.
	MergeBlockNone
	// MergeBlockDraft means the pull request is still a draft.
	MergeBlockDraft
	// MergeBlockConflicts means the branches conflict.
	MergeBlockConflicts
	// MergeBlockChecksFailing means a required check failed.
	MergeBlockChecksFailing
	// MergeBlockChecksRunning means a required check has not finished.
	MergeBlockChecksRunning
	// MergeBlockBehind means the branch is behind its target and the
	// repository requires it to be current.
	MergeBlockBehind
	// MergeBlockBlocked means a rule other than the above refuses the merge, a
	// missing review among them.
	MergeBlockBlocked
)

// String returns the member's wire spelling: "unknown", "none", "draft",
// "conflicts", "checks_failing", "checks_running", "behind" or "blocked".
//
// Seven of the eight are the spellings a consumer's current wire already
// carries; "none" replaces an empty string, which is a consumer-visible change
// because a consumer that fails closed on an unrecognised cause would otherwise
// disable Merge on every mergeable pull request.
func (m MergeBlockReason) String() string {
	return nameOf([]string{
		nameUnknown, "none", "draft", "conflicts",
		"checks_failing", "checks_running", "behind", "blocked",
	}, m)
}

var _ fmt.Stringer = MergeBlockReason(0)

// QueueState is a merge queue's verdict on one pull request.
//
// It is a defined type rather than a bare string so that an unmapped upstream
// value, a product with no merge queue, and a field a document failed to select
// cannot collapse to one value on the wire.
type QueueState int

// The QueueState members: the five a merge queue reports, plus unknown and the
// no-queue case.
const (
	// QueueUnknown is the zero value: the queue state was not read.
	QueueUnknown QueueState = iota
	// QueueNone means this repository merges without a queue.
	QueueNone
	// QueueQueued means the pull request is in the queue.
	QueueQueued
	// QueueAwaitingChecks means the queue is waiting on its checks.
	QueueAwaitingChecks
	// QueueMergeable means the queue considers the entry ready.
	QueueMergeable
	// QueueUnmergeable means the queue considers the entry unmergeable.
	QueueUnmergeable
	// QueueLocked means the queue has locked the entry.
	QueueLocked
)

// String returns the member's wire spelling, which is the member name lowercased
// with underscores: "unknown", "none", "queued", "awaiting_checks", "mergeable",
// "unmergeable" or "locked".
func (q QueueState) String() string {
	return nameOf([]string{
		nameUnknown, "none", "queued", "awaiting_checks",
		"mergeable", "unmergeable", "locked",
	}, q)
}

var _ fmt.Stringer = QueueState(0)

// ActionState is what one pull request's own controls need: whether it can be
// merged, what its checks say, and what is in the way.
//
// It is the fourth capability scope and it rides each item rather than the
// response, because mergeability, folded checks and armed auto-merge are
// properties of one pull request. A response-level slot could only describe one
// of the rows it was returned with.
type ActionState struct {
	// Mergeable is not a bool, for the same reason CanPush is not: one
	// product's list omits the field entirely and another can serve a stale
	// one.
	Mergeable Support
	// Checks is the folded verdict over the counts below.
	//
	// Whether that fold is COMPLETE depends on the operation that filled it,
	// which is why the same field can be a verdict on one call and
	// [CheckUnknown] on another. A single-pull-request read follows the checks
	// collection to its end and answers a real verdict; a LIST takes one page of
	// each row's collection, rather than paying a fetch per row for a confident
	// green.
	//
	// What that bound COSTS the field is per PRODUCT, and this sentence claimed
	// [CheckUnknown] on all four until the measurement took two of them. The Gitea
	// family pays it as written: a row whose folded status had more answers
	// [CheckUnknown] with [PartialPaginationCap] on [PullRequest.Partial].
	// GitHub's first page carries per-state counts over the WHOLE collection, so
	// its verdict stays exact and the marker means the check NAMES truncated.
	// GitLab serves one scalar head-pipeline status with no collection under it,
	// so there is nothing to truncate and nothing to mark. Each operation's own
	// per-product table states which of the three a call answers.
	Checks CheckState
	// ChecksPassing is the number of checks in the CheckPassing state.
	ChecksPassing int
	// ChecksFailing is the number in CheckFailing.
	ChecksFailing int
	// ChecksPending is the number in CheckPending.
	ChecksPending int
	// ChecksNeutral is the number in CheckNeutral.
	ChecksNeutral int
	// ChecksUnknown is the number whose upstream value did not map.
	ChecksUnknown int
	// ChecksTotal is the number of checks the fold read. It is not the
	// upstream's own total where that total is page-scoped.
	//
	// One count per CheckState member plus this total, rather than passing,
	// failing and total: a remainder computed from three ints conflates
	// pending, neutral and unknown, and the two production fold policies this
	// vocabulary has to serve both turn on exactly those. A count added after
	// the tag would mean the published set was wrong at 1.0.
	ChecksTotal int
	// AutoMergeArmed reports that the forge will merge this pull request
	// itself once its requirements are met. Not a bool: a degraded read leaves
	// it unknown, and a consumer must not read that as disarmed.
	AutoMergeArmed Support
	// QueueState is the merge queue's verdict, or QueueNone where the
	// repository has no queue.
	QueueState QueueState
	// QueuePosition is the position in that queue, or [QueuePositionUnknown]
	// when none was read. It is not fetched inside a mutation: a merge reports
	// the enqueue without one, and the position arrives with the next list read,
	// on that row's action state, which is why [MergeOutcome.QueuePosition] and
	// this field carry the same sentinel.
	QueuePosition int
	// MergeBlocked is the cause a merge is refused, or MergeBlockNone.
	MergeBlocked MergeBlockReason
}
