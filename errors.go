package forgeapi

import (
	"fmt"
	"strings"
	"time"
)

// ErrorKind is the class of a mapped failure: what a caller can do about it.
type ErrorKind int

// The ErrorKind members.
const (
	// KindUnknown is the zero value. It is not a mapping outcome: an
	// unmapped combination is KindUpstream, which says the failure reached us
	// unclassified rather than that nobody looked.
	KindUnknown ErrorKind = iota
	// KindUnauthorized is a dead credential. A probe separates it from
	// KindTransient, which is a dead network.
	KindUnauthorized
	// KindForbidden is a live credential without the permission, or an
	// instance refusing the operation outright.
	KindForbidden
	// KindNotFound is a resource this credential cannot see, which is what one
	// product answers instead of KindForbidden by design.
	KindNotFound
	// KindNotMergeable is a refusal to merge that no retry fixes.
	KindNotMergeable
	// KindConflict is optimistic concurrency on two families and a content
	// conflict on the third, which is why Code exists beside Kind.
	KindConflict
	// KindRateLimited is a throttle refusal. It fails fast for that instance
	// rather than consuming its retry budget, and RetryAfter carries the wait
	// where upstream stated one.
	KindRateLimited
	// KindTransient is a transport-level failure worth retrying.
	KindTransient
	// KindUpstream is a failure whose combination of family, operation, status
	// and headers is not in the mapping table. It is never guessed into
	// another kind.
	KindUpstream
)

// String returns the member's spelling, which is what a log line carries:
// "unknown", "unauthorized", "forbidden", "not_found", "not_mergeable",
// "conflict", "rate_limited", "transient" or "upstream".
func (k ErrorKind) String() string {
	return nameOf([]string{
		nameUnknown, "unauthorized", "forbidden", "not_found", "not_mergeable",
		"conflict", "rate_limited", "transient", "upstream",
	}, k)
}

var _ fmt.Stringer = ErrorKind(0)

// The machine-readable codes. A code is issued where something PROVES it and
// never where it would be inferred: two conditions a status cannot separate get
// the wider code, not a guess.
//
// Codes are strings rather than a defined type because they cross a consumer's
// wire as strings, and because this block is where every one of them is declared,
// so a defined type would add a name without adding a fact.
//
// The namespace is governed by a RULE rather than pinned by a total, and the rule
// is three clauses. Every refusal a consumer must branch on carries a code. The
// code is declared HERE, in the root package, beside the refusal that issues it,
// so a family package mints none of its own: a code a consumer branches on lives
// in this block, or it reaches that branch as a bare literal nothing on this
// surface documents. And a code's wire spelling is the constant's documented
// string, never a second vocabulary.
//
// What holds the rule is api/forgeapi.txt, which lists every declared Code
// constant and fails the build on any movement. That is also what holds the one
// growth rule a 1.0 owes: a spelling can be ADDED and never changed or removed.
// No count of this block appears anywhere, deliberately: a total is a number
// every new refusal renegotiates, and it was never the gate. A reader who wants
// the list reads the generated surface file or this block.
const (
	// CodeRepoRefInvalid is a decoded repository selector that failed
	// validation. It is answered before any request.
	CodeRepoRefInvalid = "repo_ref_invalid"
	// CodeRepoRefStale is a repository that moved. It is detected from the
	// redirect rather than from a 404, because a followed redirect would
	// otherwise succeed and a stale identifier would keep working silently
	// until the day the old path stopped redirecting. [Error.Successor]
	// carries the repository it moved to, and is nil where the move was
	// detected but the successor could not be resolved, which is the case a
	// consumer answers with reconnect-and-relist rather than a one-click
	// re-point.
	CodeRepoRefStale = "repo_ref_stale"
	// CodeRepoOrPRNotVisible is the union answer: a stale identifier and a
	// repository the credential cannot see are indistinguishable from the
	// status alone, and on one family a single REST 404 cannot separate a
	// missing pull request from an invisible repository either.
	CodeRepoOrPRNotVisible = "repo_or_pr_not_visible"
	// CodePRNotFound is a pull request that does not exist in a repository
	// that does. It is issued only where the evidence separates the two, which
	// on two families is the document's own envelope.
	CodePRNotFound = "pr_not_found"
	// CodeOwnerUnresolved is a cross-repository list scoped by [WithOwner] to an
	// owner the instance does not resolve to a namespace it lists by owner for
	// this credential: absent, invisible, or a namespace of a kind that product
	// lists by no owner route. It is a not-found answer carrying the status the
	// instance sent. A product whose answer cannot separate no such owner from
	// nothing open answers an empty page instead, and that product's own
	// per-forge block on the operation says so.
	CodeOwnerUnresolved = "owner_unresolved"
	// CodeNotMergeable is a refusal whose cause upstream did not name.
	CodeNotMergeable = "not_mergeable"
	// CodeMissingSHA is a merge whose head SHA is empty: refused by this
	// library before the request on every family, and refused upstream by an
	// instance of the one product an operator can configure to require it. The
	// local refusal carries this code rather than one of its own because it is
	// the same cause, and it is every family's rather than that product's
	// because [MergeRequest.HeadSHA] is the precondition that makes a merge
	// safe wherever it runs.
	CodeMissingSHA = "missing_sha"
	// CodeNoMergePermission is a merge the credential may not perform.
	CodeNoMergePermission = "no_merge_permission"
	// CodeStrategyNotAllowed is a merge strategy the repository does not offer.
	CodeStrategyNotAllowed = "strategy_not_allowed"
	// CodeStaleHead is optimistic concurrency the forge names apart: the head
	// moved between the read and a re-run, refused before the re-run on every
	// family, or between the read and a merge on the product whose merge route
	// answers that cause in a status of its own (GitLab). A merge route that
	// answers a moved head in human text alone answers [CodeNotMergeable], and
	// the remedy for both is reading the pull request again.
	CodeStaleHead = "stale_head"
	// CodeValidation is a request upstream rejected as invalid, a RESPONSE this
	// library will not accept, which is a body it could not decode, and a label
	// name a creation could not resolve to an identifier on a read that answered
	// the repository's WHOLE label list. One code covers them because what a
	// consumer does about each is the same, and the Kind and the Status say which
	// end the shape came from.
	CodeValidation = "validation"
	// CodeLabelPageTruncated is that same unresolved label name where the page
	// read came back FULL, so the repository's label list continues past what the
	// resolution saw.
	//
	// It is its own code because the two answers have different remedies. A name
	// missing from a page that was the whole list does not exist, and the caller
	// creates it; a name missing from a TRUNCATED page may exist past the bound,
	// and the caller retries with a larger page bound or names a repository with
	// fewer labels. One code over both would send a caller back to the message,
	// which is what a code exists to avoid. The message names the page bound
	// either way.
	CodeLabelPageTruncated = "label_page_truncated"
	// CodeBranchCannotMerge is one product's refusal naming the branch rather
	// than the request.
	CodeBranchCannotMerge = "branch_cannot_merge"
	// CodeRepoArchived is a mutation against an archived repository: a
	// permanent refusal whose remedy is an operator unarchiving it, never
	// retried and never reported as a conflict, because no rebase and no
	// re-read reaches that state.
	CodeRepoArchived = "repo_archived"
	// CodeScopeInsufficient is a credential the instance authenticated and
	// refused for a permission or scope the operation needs: a fine-grained
	// token without the resource permission, a legacy token on a group that
	// requires fine-grained ones, or a legacy token without the scope. Its
	// remedy is the credential's permission set, never a project role and never
	// a reconnect, which is why it is neither [KindUnauthorized] nor the plain
	// [KindForbidden] a role refusal answers. It is issued on the one product
	// whose refusal names the cause in a machine-readable error member, and
	// decided by that member alone; [Error.Message] carries the instance's own
	// text naming what is missing, which is never branched on.
	CodeScopeInsufficient = "scope_insufficient"
	// CodeAlreadyEnqueued is a merge in flight with no verdict yet: an
	// equivalent merge already running under a request this caller did not
	// issue, or a merge accepted and running in the background. It rides a
	// successful outcome rather than an error, because the remedy is a read,
	// [Merges.MergeStatus] on the same pull request, and it is ONE code for both
	// of those states because what a consumer branches on is that it must read
	// the state back, not which answer put the merge in flight.
	CodeAlreadyEnqueued = "already_enqueued"
	// CodeAPIVersionRetired is a pinned REST API version the instance does not
	// serve: retired past its window on a product that has moved on, or not yet
	// present on a release that predates it. Both reach a consumer as the same
	// remedy, which is a release of this library carrying a version the instance
	// answers under, so they are one code rather than two.
	CodeAPIVersionRetired = "api_version_retired"
	// CodeMutationsDisabled is an operation refused because the client was
	// built with mutations off. The refusal is enforced at the operation
	// boundary rather than advisory, so the read-only posture is worth taking.
	CodeMutationsDisabled = "mutations_disabled"
	// CodeRedirectHopCap is a redirect chain longer than the hop cap. It is
	// its own code so a redirect loop is distinguishable from an operation
	// that merely ran out of time.
	CodeRedirectHopCap = "redirect_hop_cap"
	// CodeRedirectPortRefused is a redirect hop to a port outside the
	// connection's allowlist. It is refused in this library's own redirect
	// policy so it carries a code and a counter, rather than arriving as a
	// transport error the retry layer would treat as transient.
	CodeRedirectPortRefused = "redirect_port_refused"

	// The codes below are this library's OWN refusals, answered before any
	// request. They are in the same namespace as the codes above because the
	// contract is the same one: a caller branches on Code and never on
	// Message, and a consumer rendering a connect dialog has to name the
	// remedy for the refusal it actually got rather than a generic warning.
	//
	// One member of the group is in it by remedy and outside it by audience,
	// [CodeCapabilityUnsupported]: its remedy belongs to whoever wrote the call
	// rather than to whoever is looking at the dialog, and its own comment states
	// what that changes about the fields it carries.

	// CodeConnectionInvalid is a [Connection] a client cannot serve honestly:
	// no web base URL, a half-present client-certificate pair, a
	// [Connection.Proxy] net/url refuses or carrying no scheme this library
	// proxies over, or a [Header] entry with an empty name, or with a name or a
	// value outside net/http's field grammar, which the transport would refuse
	// on every request. It is the catch-all for a malformed connection and never
	// the answer where a refusal below names the cause.
	CodeConnectionInvalid = "connection_invalid"
	// CodeAnonymousRefused is a client built with no [CredentialSource], whose
	// requests would be anonymous. It is its own code because its remedy is a
	// credential rather than a corrected connection: a client that silently
	// read only public data would look like a permission problem on the
	// instance.
	CodeAnonymousRefused = "anonymous_refused"
	// CodePlaintextRefused is a plaintext http connection built without
	// [WithPlaintextHTTP]. It is its own code because the consumer names the
	// actual consequence to the user, which is that the token and every
	// request travel in cleartext on that network.
	CodePlaintextRefused = "plaintext_refused"
	// CodePrivateAddressRefused is a private-range or single-label host built
	// without [WithPrivateAddresses]. It is its own code because the consumer's
	// remedy is one deliberate statement per instance, which it cannot offer
	// without knowing that this was the refusal.
	CodePrivateAddressRefused = "private_address_refused"
	// CodeBudgetInvalid is a NEGATIVE value passed to one of the budget
	// options, [WithMutationReserve] and [WithRefreshLead] included, the second
	// refused by the creds package's source rather than by a family's
	// constructor. It is refused rather than defaulted, because a negative
	// deadline would expire every operation on the client and a negative bound,
	// reserve or lead would read as a limit nobody set. A ZERO is never this
	// refusal: with one configuration door a zero is the literal zero, and each
	// option's godoc says what its own zero does.
	CodeBudgetInvalid = "budget_invalid"
	// CodePageBoundInvalid is a page bound of zero or less, refused by
	// [ResolveList] rather than sent: one upstream accepts a zero with HTTP 200
	// and answers an item-less page beside "there is more", which would render
	// as an empty list marked [PartialPaginationCap] on every such call with no
	// upstream error to classify.
	CodePageBoundInvalid = "page_bound_invalid"
	// CodeListStateInvalid is a state filter that is [ListStateUnknown], one
	// the list it was given to has no state to filter on, or
	// [ListStateMerged] on [Issues.ListIssues], which has a state and no
	// merged one. The first is [ResolveList]'s to refuse, since it is invalid
	// whichever list is being resolved; the other two are the family method's,
	// which is what knows the list.
	CodeListStateInvalid = "list_state_invalid"
	// CodeListOwnerInvalid is a [WithOwner] scope that cannot be sent: an empty
	// owner or one outside the shared form, which [ResolveList] refuses whichever
	// list it resolves for, and an owner named on a list with no owner scope, or
	// a path where the family's owner is a single name, which the family method
	// refuses because it knows the list. The form is checked rather than escaped
	// because the owner can reach a search string, whose grammar would read a
	// space, a colon or a quote as a second qualifier.
	CodeListOwnerInvalid = "list_owner_invalid"
	// CodeCursorInvalid is a [Cursor] that is not well formed, refused by
	// [ValidateCursor] and by [ResolveList] before it can be interpolated into
	// a request, and one well formed that names no position in the call it was
	// handed to, refused by the family before any request: another encoding, a
	// continuation minted by another call or on another connection, or a
	// page-numbered one minted at another page bound, as [WithAfter] states.
	CodeCursorInvalid = "cursor_invalid"
	// CodeRefInvalid is a commit ref that is not safe to interpolate into an
	// API path, refused by [ValidateRef] before any request. It is traversal
	// and form only, never git's grammar: the forge stays the authority on
	// which names a repository holds, so a name git would reject reaches the
	// instance and comes back as upstream's own refusal.
	CodeRefInvalid = "ref_invalid"
	// CodeHeaderReserved is an extra request header on a [Connection] whose
	// name this library or the HTTP stack beneath it writes itself, the set
	// [ReservedHeaders] returns. It is refused at connect time naming the header
	// rather than dropped silently, because an operator whose setting was
	// ignored believes something false about their traffic.
	CodeHeaderReserved = "header_reserved"

	// CodeCapabilityUnsupported is an operation refused because this instance
	// lacks a capability the operation requires: detection said so, so no request
	// is issued at all, which is the property that makes the refusal free. The
	// standing case is [PullRequests.RerunFailedChecks] against an instance whose
	// API has no re-run verb, which is [CapRerunChecks] read from
	// [Capabilities.ConnectionCaps].
	//
	// It carries the operation in [Error.Op], the capability the operation
	// required in [Error.Capability], and the [Evidence] detection reached the
	// verdict on, its Source and Detail, in [Error.Evidence], so the message
	// states a fact about the instance rather than a bare refusal. Those two
	// fields are this code's alone: every other code leaves them at their zero
	// values, and no other code populates either.
	//
	// It sits in the local group by remedy and apart from it by shape, which is
	// what [CodeFamilyUndetected] does in the opposite direction: this refusal
	// names both an operation and a family, so Op and [Error.Family] are
	// populated and [Error.Status] is zero because nothing was sent, and it
	// carries a [Error.DiagID] because there IS an operation whose log lines it
	// correlates with.
	//
	// It is written for a DEVELOPER, not for a user, and that is what shapes it.
	// A consumer following this library's own contract reads
	// [Capabilities.ConnectionCaps] and [Capabilities.GrantCaps] first and
	// renders a disabled control with the reason, so it never reaches this code;
	// reaching it means an operation was called that the caller could have known
	// was unsupported. So the message is written for a log rather than for a
	// dialog, and the code carries no user-facing text obligation.
	CodeCapabilityUnsupported = "capability_unsupported"

	// CodeFamilyUndetected is a connection whose forge family could not be
	// established: detection ran and nothing answered. It belongs to neither
	// group above (a request was made, and its answer identified no family),
	// so it carries the real status where one arrived and [FamilyUnknown]
	// always. It is its own code because its remedy is a corrected instance URL
	// rather than a credential or a permission, and because it is the one
	// failure on this surface that belongs to no family at all.
	CodeFamilyUndetected = "family_undetected"

	// The codes below are the credential machinery's, issued by the creds
	// package: the refresh behind its [CredentialSource] and the OAuth device
	// grant.

	// CodeReconnectRequired is a credential the machinery has no way left to
	// renew: no record under the connection's key, a record of the unknown
	// kind, a static token past its expiry, a rotating one past its expiry
	// with no refresh token, a refresh token past its own expiry, or a refresh
	// the product answered terminally. Token fails with it until a human
	// connects again, and [CredReconnectRequired] is the state it surfaces as.
	CodeReconnectRequired = "reconnect_required"
	// CodeGrantDenied is a device grant the user declined at the verification
	// address. It is terminal for that grant.
	CodeGrantDenied = "grant_denied"
	// CodeGrantExpired is a device grant whose codes expired before the user
	// approved it, whether the product said so or a poll came after the
	// codes' own expiry. It is terminal for that grant.
	CodeGrantExpired = "grant_expired"
	// CodeGrantUnsupported is a device grant asked of a family this library
	// runs no device grant for. It is refused before any request.
	CodeGrantUnsupported = "grant_unsupported"
)

// DiagIDLength is the rendered length of [Error.DiagID]: 13 base32 characters,
// because base32 carries 5 bits per character and 64 random bits need 13.
const DiagIDLength = 13

// Error is one mapped failure.
//
// Mapping is by family plus operation plus status plus selected headers, never
// by status alone: a 409 is optimistic concurrency on one family's merge, a
// merge already in flight on another's, and on a creation the pull request that
// already exists. That is why Code exists beside Kind.
//
// The package's error contract is THREE-WAY: every operation in this package and
// in every family package answers nil, a *Error, or a context sentinel. This type
// is the second of those, and the pointer form is the one to recover:
//
//	var ferr *forgeapi.Error
//	if errors.As(err, &ferr) { switch ferr.Code { ... } }
//
// The third arm is context.Canceled or context.DeadlineExceeded, returned
// UNCHANGED where the operation's context ended, so errors.Is against Go's own
// sentinel holds. It is not mapped into this type, and that is the one place the
// every-error-is-ours rule yields: a function taking a context is expected to
// return that context's error, every consumer already writes the check that only
// works if it does, and a *Error carrying a code of its own would keep the rule
// literally true while breaking the check nobody tests for. It follows that the
// library's own per-operation deadline and the caller's own cancellation are
// indistinguishable by type; a caller that needs the distinction reads its own
// context.
//
// This type wraps nothing and declares no Unwrap, deliberately and decided here
// rather than after the tag: a transport cause reachable through this chain would
// put a dependency's error type in the published surface, which is the containment
// this library's own dependency rule refuses, and the mapping is a translation
// at the boundary rather than a pass-through. So the chain terminates here, and
// a consumer branches on Code and Kind and never on Message.
type Error struct { //nolint:govet // fieldalignment: field order here is declared order, and it is API for an unkeyed composite literal
	// Op is the role method that failed, spelled as the method that names it:
	// "ListPRs", "ListMyPRs", "ReadPR", "MergeStatus", "MergePR" and so on, one
	// of TWENTY-ONE. The spelling is fixed here rather than left to each family
	// package, because three families minting three spellings for one operation
	// is undetectable, and because this value is a counter dimension
	// ([Counters.Retried] and [Counters.ReadDeferred], the two counters that
	// take an operation) whose label set must be closed.
	//
	// Twenty-one rather than eighteen, and the three extra are why the label set
	// is closed on the METHOD rather than on the published operation list: every
	// error-returning role method can name itself here, so beside the eighteen
	// operations sit "ConnectionCaps", "GrantCaps" and "RepoAffordances", each
	// of which resolves evidence that can cost a request and each of which
	// therefore has an upstream failure of its own to report. [Governor] is the
	// fourth accessor and is not among them: [Governor.BudgetState] performs no
	// I/O and returns no error, so it can never name itself here.
	//
	// It is EMPTY where this library refused before any operation began, which
	// is the local-refusal group above and [CodeFamilyUndetected]: a validator and
	// a constructor are not operations, so an empty Op there is the honest value
	// rather than a field nobody set. [CodeCapabilityUnsupported] is the one
	// member of that group that DOES name an operation, because the operation
	// began and was refused on what detection already knew about the instance.
	Op string
	// Code is the machine-readable cause, one of the Code* constants. A
	// consumer branches on this.
	//
	// It is EMPTY on exactly one failure, the read the governor deferred to hold
	// the mutation reserve: that refusal is reported as the rate-limited KIND with
	// no status, which is the pair an upstream throttle carries minus the status,
	// and it deliberately mints no code of its own. Everywhere else this field
	// carries one of the constants.
	Code string
	// Family is the family that answered, or [FamilyUnknown] where nothing
	// answered: a local refusal, an undetected family, and every refusal reached
	// without a family in hand at all, [ValidateCursor]'s among them.
	// [CodeCapabilityUnsupported] is the one refusal that sends nothing and
	// carries a family anyway, because detection had already identified it and
	// the refusal is a fact about that family's instance.
	Family Family
	// Status is the real HTTP status, always, and ZERO exactly where no request
	// was made. A document error arriving at HTTP 200 keeps the 200 here and
	// records that the classification came from the body, rather than faking a
	// zero; a refusal from the local-refusal group above never had a status to
	// carry, and the zero there says so rather than standing in for one.
	Status int
	// Kind is the class of failure.
	Kind ErrorKind
	// Retryable reports whether this library considers another attempt useful.
	Retryable bool
	// Message is upstream's own message, with the request's credential and the
	// connection's own header values redacted out of it, sanitized and bounded.
	// It is never classified on: a caller branches on Code and Kind.
	Message string
	// RetryAfter is the wait upstream asked for, zero when it named none.
	RetryAfter time.Duration
	// DiagID is a per-failure random identifier, [DiagIDLength] characters,
	// carried in the user-visible error and in every log line for that
	// operation. It is not a session identifier, not stable across retries,
	// and never a lookup key for state.
	//
	// A local refusal carries none: it names no operation, so there are no log
	// lines for one to correlate. [CodeCapabilityUnsupported] carries one for
	// exactly that reason read the other way, since it does name an operation.
	DiagID string
	// Successor addresses the repository this one moved to, carried on
	// [CodeRepoRefStale] and nil everywhere else. It is nil on that code too where
	// the move was detected but the successor could not be named (the read resolving
	// it was refused or could not see it, or the redirect names no repository the
	// family addresses): reconnect and relist rather than re-point. It is spelled and
	// emptied as [Page.Successor], [CommitChecks.Successor] and [MergeStatus.Successor].
	Successor *RepoRef
	// Capability is the capability the refused operation required, carried on
	// [CodeCapabilityUnsupported] and empty everywhere else. It is a field
	// rather than prose inside Message because a consumer branches on values
	// here and never on text, which is the same rule Code states, and because
	// the refusal is this library's own: there is no upstream message for
	// Message to carry on this code.
	Capability Capability
	// Evidence is what detection reached that verdict on, its Source and its
	// Detail, carried on [CodeCapabilityUnsupported] and the zero [Evidence]
	// everywhere else. It is the same value [Capabilities.ConnectionCaps]
	// renders beside a disabled control, so a developer reading the log line
	// and a user reading the dialog are looking at one fact.
	Evidence Evidence
}

// Error implements the error interface.
//
// The separator after the operation belongs to the CODE, so the one failure that
// carries no code, the governor's deferral, renders as the operation and its status
// rather than with a gap where a cause would be.
func (e *Error) Error() string {
	var b strings.Builder
	if e.Op != "" {
		b.WriteString(e.Op)
		if e.Code != "" {
			b.WriteString(": ")
		}
	}
	b.WriteString(e.Code)
	if e.Status != 0 {
		fmt.Fprintf(&b, " (status %d)", e.Status)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// localError builds a refusal this library reached before any operation began. It
// reads as the local group reads: no operation, no family, no status and no
// diagnostic id, because none of the four exists for a validator or a constructor
// to report.
func localError(code, message string) *Error {
	return &Error{Code: code, Kind: KindUnknown, Message: message}
}

var _ error = (*Error)(nil)
