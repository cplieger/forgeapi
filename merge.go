package forgeapi

import "fmt"

// MergeIntent is what the caller wants the merge to do to the history.
//
// It is the one enumeration here whose zero value is not an unknown member:
// IntentDefault at zero is genuinely the correct default rather than an absence,
// because every family has a project-level default and "do what this repository
// does" is a real request.
type MergeIntent int

// The MergeIntent members. Default, squash and no-squash is what three forges
// commonly express; a family spelling beyond that travels in
// [MergeRequest.Strategy].
const (
	// IntentDefault takes the repository's own configured behaviour.
	IntentDefault MergeIntent = iota
	// IntentSquash asks for one commit.
	IntentSquash
	// IntentNoSquash asks for the branch's commits to survive.
	IntentNoSquash
)

// String returns the member's spelling: "default", "squash" or "no_squash".
func (m MergeIntent) String() string {
	return nameOf([]string{"default", "squash", "no_squash"}, m)
}

var _ fmt.Stringer = MergeIntent(0)

// MergeRequest is one merge attempt.
type MergeRequest struct { //nolint:govet // fieldalignment: field order here is declared order, and it is API for an unkeyed composite literal
	// Intent is the cross-forge intent.
	Intent MergeIntent
	// Strategy is an OPTIONAL family spelling, and it is refused with
	// [CodeStrategyNotAllowed] before any request unless it is in that family's
	// own closed set of spellings. The check is local and reads no repository,
	// because an affordances read inside a merge is a request the budget does
	// not price, so a strategy this repository has DISABLED costs a round trip
	// and comes back as the forge's own refusal, [CodeNotMergeable], because a
	// merge route answers that and a pull request not yet mergeable under one
	// status. It stays a string because the validation is the point
	// rather than the type: it keeps a fast-forward-only or a rebase reachable
	// instead of folded into an approximation. One product can silently ignore a
	// value it does not support, which the family package reports rather than
	// swallows.
	Strategy string
	// HeadSHA is required, and mandatory rather than prudent: one product has
	// a group and instance setting that makes it compulsory and answers 400
	// without it. It is also the precondition that makes a merge safe, since a
	// push landing between the read and the click must fail the merge rather
	// than land unreviewed code.
	//
	// An empty HeadSHA is therefore REFUSED with [CodeMissingSHA] before any
	// request, on every family and not only the one that answers 400 for it.
	// The requirement is stated as a refusal because the alternative is the
	// field's own reason failing open: on the two families that do not demand
	// it, a merge sent without it merges whatever the head is now, which is the
	// outcome the pin exists to prevent. It is not
	// [PullRequests.RerunFailedChecks]'s empty case, where an absent head is a
	// pin the forge could not supply rather than one the caller omitted.
	HeadSHA string
	// DeleteBranch asks the forge to delete the source branch after merging.
	DeleteBranch bool
	// AutoMerge asks the forge to complete the merge itself once its
	// requirements are met, using each product's current field for that:
	// auto_merge on GitLab (never the merge_when_pipeline_succeeds it
	// deprecated), merge_when_checks_succeed on Gitea and Forgejo, and the
	// auto-merge arm on GitHub, which takes only a pull request that cannot
	// merge now, so GitHub reads the pull request first and merges one whose
	// requirements are already met. An armed merge is [MergeOutcomeEnqueued].
	AutoMerge bool
}

// QueuePositionUnknown is [ActionState.QueuePosition] or
// [MergeOutcome.QueuePosition] where no position was read.
//
// It is negative rather than zero on purpose: no merge response of any product
// carries a queue position, the only one in the surface is a field a list
// document reaches, and a zero would read as "next to merge".
const QueuePositionUnknown = -1

// MergeOutcomeState is what actually happened to a merge request.
type MergeOutcomeState int

// The MergeOutcomeState members: five outcomes, because a merge on a
// queue-protected branch is neither a merge nor a refusal, and two answers give
// the caller no verdict yet.
const (
	// MergeOutcomeUnknown is the zero value: no outcome was recorded.
	MergeOutcomeUnknown MergeOutcomeState = iota
	// MergeOutcomeMerged means the merge completed.
	MergeOutcomeMerged
	// MergeOutcomeEnqueued means the pull request entered a merge queue or
	// train, or was armed to merge itself once its requirements are met
	// ([MergeRequest.AutoMerge]). This is a SUCCESS: reporting it as a refusal
	// would label a perfectly mergeable pull request unmergeable, which is what
	// the bare status mapping does on a queue-protected branch.
	MergeOutcomeEnqueued
	// MergeOutcomeAccepted means the merge was accepted and is running in the
	// background. It carries [CodeAlreadyEnqueued] like MergeOutcomeInFlight
	// does: both leave the caller without a verdict, which is the one thing a
	// consumer branches on, and the read that follows either is
	// [Merges.MergeStatus] on the same pull request.
	MergeOutcomeAccepted
	// MergeOutcomeInFlight means an equivalent merge was already in flight
	// under a request this caller did not issue. Its requested options may
	// differ from ours, which is why it carries [CodeAlreadyEnqueued] rather
	// than being reported as a conflict.
	MergeOutcomeInFlight
	// MergeOutcomeRefused means the forge refused, with the reason in the
	// accompanying error's Kind and Code.
	MergeOutcomeRefused
)

// String returns the member's spelling: "unknown", "merged", "enqueued",
// "accepted", "in_flight" or "refused".
func (m MergeOutcomeState) String() string {
	return nameOf([]string{nameUnknown, stateMergedName, "enqueued", "accepted", "in_flight", "refused"}, m)
}

var _ fmt.Stringer = MergeOutcomeState(0)

// MergeOutcome is what a merge answers. A merge never reports a bare error:
// one product accepts a merge and runs it in the background, and another
// enqueues it on a queue-protected branch, so a merge has an outcome and not
// merely a failure.
//
// Nothing a product mints to track a merge rides here. The one family whose
// merge is asynchronous receives a handle for it and keeps that handle to
// itself, per connection and in memory; the read that follows any outcome is
// [Merges.MergeStatus], addressed by the same [PRRef] the merge was, so a
// caller written against one family holds nothing another family never mints.
//
// Nor does the pull request itself. A merge answers what happened to the merge,
// and whether a consumer patches a row from that or reads the pull request back
// is the consumer's own cache policy, which this library does not own and has no
// opinion about. The read that answers what the pull request now looks like is
// [Merges.MergeStatus] or [PullRequests.ReadPR].
type MergeOutcome struct { //nolint:govet // fieldalignment: field order is the reading order this type documents, the outcome first, and it is API for an unkeyed composite literal
	// State is the outcome.
	State MergeOutcomeState
	// Code is a machine-readable discriminator from the same namespace as
	// [Error.Code]. It is carried on the two success states that give the
	// caller no verdict yet, [MergeOutcomeAccepted] and [MergeOutcomeInFlight],
	// both as [CodeAlreadyEnqueued], and it is empty on the three states whose
	// own name is the whole answer.
	Code string
	// QueueState is the queue's verdict where the merge entered one. It is
	// spelled as [ActionState.QueueState] is, because it is the same
	// enumeration answering the same question and one concept carries one name.
	QueueState QueueState
	// QueuePosition is the position in that queue, or [QueuePositionUnknown]
	// when none was read. A position is not fetched inside a mutation: it
	// arrives with the next list read.
	QueuePosition int
}

// MergeStatus is one pull request's merge state, as [Merges.MergeStatus] reads
// it back: the read that follows a [MergeOutcomeAccepted] or
// [MergeOutcomeInFlight] outcome, and the one answer to "is this merged" every
// family gives in the same two fields.
//
// The two answer fields are this package's own types, so a caller written
// against one family runs unchanged against all of them: a product that cannot
// supply a field answers its unknown or none member in that field rather than
// omitting it, retyping it or moving it to a variant of its own. Beside them
// ride WebURL, which is where this read points a caller who wants more than it
// fetches, and the rename mark every response-level carrier has, because this
// read addresses a repository and its return rides no list item.
type MergeStatus struct { //nolint:govet // fieldalignment: field order is the reading order this type documents, the answer first, and it is API for an unkeyed composite literal
	// Merged reports whether the pull request is merged. It is a [Support]
	// rather than a bool because an answer the credential could not read is
	// unknown, not a wrong "not merged".
	Merged Support
	// Queue is the merge queue's or merge train's verdict where the product
	// runs one, and [QueueNone] where the repository merges without one. It is
	// spelled as [ActionState.QueueState] is, so a consumer reads one
	// vocabulary from a list row and from this single read alike.
	Queue QueueState
	// WebURL is the pull request's own page on the instance, spelled as
	// [PullRequest.WebURL] is, and it is what this read hands its caller INSTEAD
	// of detail it deliberately does not fetch.
	//
	// This operation costs exactly one request and returns no checks, so the two
	// things a caller might want next are named rather than fetched: this field
	// for a person who wants to look at the pull request, and
	// [PullRequests.ReadPR] on the same [PRRef] for a caller that wants the
	// folded check list, which is the operation that pays for those pages. Every
	// product's pull-request object carries the field, so it costs no request of
	// its own.
	WebURL string
	// Successor is non-nil when the repository this read addressed had moved and
	// the read followed the redirect to it, on the same terms as
	// [Page.Successor] and [CommitChecks.Successor].
	Successor *RepoRef
}
