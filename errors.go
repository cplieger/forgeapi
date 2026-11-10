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
	// KindUnknown is the zero value, never a mapping outcome: an unmapped
	// combination is KindUpstream.
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
	// KindRateLimited is a throttle refusal, failed fast rather than retried;
	// RetryAfter carries the wait where upstream stated one.
	KindRateLimited
	// KindTransient is a transport-level failure worth retrying.
	KindTransient
	// KindUpstream is a failure whose family, operation, status and headers
	// are not in the mapping table, never guessed into another kind.
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

// The machine-readable codes. A code is issued where something proves it and
// never where it would be inferred: two conditions a status cannot separate get
// the wider code, not a guess.
//
// Every refusal a consumer must branch on carries a code declared in this
// block, so a family package mints none of its own, and its wire spelling is
// the constant's string. api/forgeapi.txt lists every constant and fails the
// build on any movement: a spelling is added, never changed or removed.
const (
	// CodeRepoRefInvalid is a decoded repository selector that failed
	// validation. It is answered before any request.
	CodeRepoRefInvalid = "repo_ref_invalid"
	// CodeRepoRefStale is a repository that moved, detected from the redirect
	// so a stale identifier never keeps working silently. [Error.Successor]
	// carries where it moved, or nil where that could not be resolved: then
	// reconnect and relist rather than re-point.
	CodeRepoRefStale = "repo_ref_stale"
	// CodeRepoOrPRNotVisible is the union answer where the status cannot tell a
	// stale identifier, an invisible repository and a missing pull request apart.
	CodeRepoOrPRNotVisible = "repo_or_pr_not_visible"
	// CodePRNotFound is a pull request missing from a repository that exists,
	// issued only where the evidence separates the two.
	CodePRNotFound = "pr_not_found"
	// CodeOwnerUnresolved is a list scoped by [WithOwner] to an owner the
	// instance does not resolve for this credential: absent, invisible, or of a
	// kind with no owner route. A product that cannot tell no such owner from
	// nothing open answers an empty page, as its per-forge block says.
	CodeOwnerUnresolved = "owner_unresolved"
	// CodeNotMergeable is a refusal whose cause upstream did not name.
	CodeNotMergeable = "not_mergeable"
	// CodeMissingSHA is a merge whose head SHA is empty: refused by this
	// library before the request on every family, since [MergeRequest.HeadSHA]
	// is what makes a merge safe, and refused upstream by the one product an
	// operator can configure to require it.
	CodeMissingSHA = "missing_sha"
	// CodeNoMergePermission is a merge the credential may not perform.
	CodeNoMergePermission = "no_merge_permission"
	// CodeStrategyNotAllowed is a merge strategy the repository does not offer.
	CodeStrategyNotAllowed = "strategy_not_allowed"
	// CodeStaleHead is a head that moved after the read: before a re-run on
	// every family, or before a merge on GitLab, whose merge route gives it a
	// status of its own. A route that says so only in text answers
	// [CodeNotMergeable]. The remedy for both is reading the pull request again.
	CodeStaleHead = "stale_head"
	// CodeValidation is a request upstream rejected as invalid, a response
	// body this library could not decode, or a label name a creation could not
	// resolve on a read that answered the whole label list. Kind and Status say
	// which end the shape came from.
	CodeValidation = "validation"
	// CodeLabelPageTruncated is that same unresolved label name where the page
	// read came back full, so the name may exist past the bound: retry with a
	// larger page bound. The message names the page bound.
	CodeLabelPageTruncated = "label_page_truncated"
	// CodeBranchCannotMerge is one product's refusal naming the branch rather
	// than the request.
	CodeBranchCannotMerge = "branch_cannot_merge"
	// CodeRepoArchived is a mutation against an archived repository: permanent
	// until an operator unarchives it, so never retried or reported as a conflict.
	CodeRepoArchived = "repo_archived"
	// CodeScopeInsufficient is a credential the instance authenticated and
	// refused for a permission or scope the operation needs: a fine-grained
	// token without the resource permission, a legacy token on a group that
	// requires fine-grained ones, or a legacy token without the scope. It rides
	// [KindForbidden], and its remedy is the credential's permission set, never a
	// role or a reconnect. It is issued only where the product names the cause in
	// a machine-readable error member; [Error.Message] names what is missing.
	CodeScopeInsufficient = "scope_insufficient"
	// CodeAlreadyEnqueued is a merge in flight with no verdict yet: an
	// equivalent merge another request started, or one accepted and running in
	// the background. It rides a successful outcome, not an error: the caller
	// reads the verdict back with [Merges.MergeStatus].
	CodeAlreadyEnqueued = "already_enqueued"
	// CodeAPIVersionRetired is a pinned REST API version the instance does not
	// serve, retired or not yet present. The remedy is a release of this library
	// pinning a version the instance serves.
	CodeAPIVersionRetired = "api_version_retired"
	// CodeMutationsDisabled is a write refused, before any request, by a client
	// built with mutations off.
	CodeMutationsDisabled = "mutations_disabled"
	// CodeRedirectHopCap is a redirect chain longer than the hop cap, so a
	// loop reads apart from an operation that ran out of time.
	CodeRedirectHopCap = "redirect_hop_cap"
	// CodeRedirectPortRefused is a redirect hop or a dial to a port outside
	// the connection's allowlist. It is not retried.
	CodeRedirectPortRefused = "redirect_port_refused"
	// CodeTLSRefused is a handshake that cannot succeed as configured: the
	// certificate did not verify, the peer does not speak TLS, or it sent a
	// fatal alert. It is not retryable.
	CodeTLSRefused = "tls_refused"
	// CodeUnreachable is an instance the request could not reach, or that
	// dropped the connection before answering. It is retryable, and its message
	// names the host.
	CodeUnreachable = "instance_unreachable"
	// CodeResponseTooLarge is an answer whose body passed this library's bound
	// for it. It carries the status the instance answered with.
	CodeResponseTooLarge = "response_too_large"

	// The codes below are this library's own refusals, answered before any
	// request, in the same namespace because the contract is the same: a caller
	// branches on Code, never on Message, and names the remedy for the refusal
	// it got. [CodeCapabilityUnsupported] is here by remedy, but its remedy
	// belongs to whoever wrote the call.

	// CodeConnectionInvalid is a [Connection] a client cannot serve: no web
	// base URL, a half-present client-certificate pair, a [Connection.Proxy]
	// that does not parse or has no scheme this library proxies over, or a
	// [Header] entry outside net/http's field grammar.
	CodeConnectionInvalid = "connection_invalid"
	// CodeAnonymousRefused is a client built with no [CredentialSource], whose
	// requests would be anonymous. The remedy is a credential.
	CodeAnonymousRefused = "anonymous_refused"
	// CodePlaintextRefused is a plaintext http connection built without
	// [WithPlaintextHTTP], over which the token would travel in cleartext.
	CodePlaintextRefused = "plaintext_refused"
	// CodePrivateAddressRefused is a private-range or single-label host built
	// without [WithPrivateAddresses], or a hop or a dial whose address the
	// connection's address policy refused.
	CodePrivateAddressRefused = "private_address_refused"
	// CodeBudgetInvalid is a negative value passed to one of the budget
	// options, [WithMutationReserve] and [WithRefreshLead] included, the last
	// refused by the creds package's sources. A zero is never this refusal: each
	// option's godoc says what its zero does.
	CodeBudgetInvalid = "budget_invalid"
	// CodePageBoundInvalid is a page bound of zero or less, refused by
	// [ResolveList] because one upstream answers a zero with an empty page that
	// says there is more.
	CodePageBoundInvalid = "page_bound_invalid"
	// CodeListStateInvalid is a state filter that is [ListStateUnknown],
	// refused by [ResolveList], or one the list has no state for, such as
	// [ListStateMerged] on [Issues.ListIssues], refused by the family method.
	CodeListStateInvalid = "list_state_invalid"
	// CodeListOwnerInvalid is a [WithOwner] scope that cannot be sent: an empty
	// owner or one outside the shared form, refused by [ResolveList], and an
	// owner on a list with no owner scope, or a path where the family's owner is
	// one name, refused by the family method.
	CodeListOwnerInvalid = "list_owner_invalid"
	// CodeListSinceInvalid is a [WithSince] bound of the zero time, refused by
	// [ResolveList], or on a list with no creation time, refused by the family.
	CodeListSinceInvalid = "list_since_invalid"
	// CodeListMergeStateInvalid is a [WithMergeState] named on a list with no
	// merge state to leave out, refused by the family method at either value.
	CodeListMergeStateInvalid = "list_merge_state_invalid"
	// CodeRunUndated is a run row with no creation time on a listing held to a
	// [WithSince] bound. Read with no bound, the row answers a zero CreatedAt;
	// any other malformed run row is [CodeValidation].
	CodeRunUndated = "run_undated"
	// CodeCursorInvalid is a [Cursor] that is not well formed, refused by
	// [ValidateCursor] and [ResolveList], or one that names no position in the
	// call it was handed to, as [WithAfter] states, refused by the family.
	CodeCursorInvalid = "cursor_invalid"
	// CodeRefInvalid is a commit ref unsafe to interpolate into an API path,
	// refused by [ValidateRef]. It checks traversal and form, not git's grammar,
	// which the forge judges.
	CodeRefInvalid = "ref_invalid"
	// CodeHeaderReserved is an extra request header on a [Connection] whose
	// name this library or net/http writes itself, the set [ReservedHeaders]
	// returns. It is refused naming the header, never dropped silently.
	CodeHeaderReserved = "header_reserved"

	// CodeCapabilityUnsupported is an operation refused, with no request sent,
	// because detection found the instance lacks a capability it requires, such
	// as [PullRequests.RerunFailedChecks] without [CapRerunChecks]. Only this code
	// sets [Error.Capability] and [Error.Evidence], the verdict's Source and
	// Detail; Op, [Error.Family] and [Error.DiagID] are set and [Error.Status] is
	// zero. It is written for a developer's log: a consumer that reads
	// [Capabilities.ConnectionCaps] and [Capabilities.GrantCaps] first never
	// reaches it.
	CodeCapabilityUnsupported = "capability_unsupported"

	// CodeFamilyUndetected is a connection whose forge family detection could
	// not establish. It carries the status where one arrived, and
	// [FamilyUnknown] always. The remedy is a corrected instance URL.
	CodeFamilyUndetected = "family_undetected"

	// The codes below are the credential machinery's, issued by the creds
	// package: the refresh behind its [CredentialSource], the OAuth device
	// grant and the GitHub App credentials.

	// CodeReconnectRequired is a credential the machinery has no way left to
	// renew: no record under the connection's key, a record of the unknown
	// kind, a static token past its expiry, a rotating one past its expiry
	// with no refresh token, a refresh token past its own expiry, a refresh
	// the product answered terminally, or a GitHub App mint refused for its
	// key, installation or grant. Token fails with it until a human reconnects,
	// or for an App source until a re-probe finds the cause fixed, and
	// [CredReconnectRequired] is the state it surfaces as.
	CodeReconnectRequired = "reconnect_required"
	// CodeGrantDenied is a device grant the user declined at the verification
	// address. It is terminal for that grant.
	CodeGrantDenied = "grant_denied"
	// CodeGrantExpired is a device grant whose codes expired before the user
	// approved it, said by the product or past the codes' own expiry. It is
	// terminal for that grant.
	CodeGrantExpired = "grant_expired"
	// CodeGrantUnsupported is a device grant asked of a family this library
	// runs no device grant for. It is refused before any request.
	CodeGrantUnsupported = "grant_unsupported"
	// CodeAppKeyInvalid is a GitHub App key refused at construction: an issuer
	// that is empty or holds whitespace or a non-printable character, PEM that
	// is not one RSA private key, or a key not RSA or under 2048 bits.
	CodeAppKeyInvalid = "app_key_invalid"
	// CodeAppGrantInvalid is a GitHub App source's config refused at
	// construction: an installation selector that names no installation, a
	// grant naming no permission and not the full grant, a full grant with
	// permissions or repositories filled in, a permission name outside
	// lowercase letters, digits and underscores, a level other than read,
	// write or admin, a repository that is not one name, or more than 500
	// repositories.
	CodeAppGrantInvalid = "app_grant_invalid"
)

// Codes returns every declared Code constant's string, in declaration order. A
// spelling is only ever added, so a later release returns a superset.
func Codes() []string {
	return []string{
		CodeRepoRefInvalid,
		CodeRepoRefStale,
		CodeRepoOrPRNotVisible,
		CodePRNotFound,
		CodeOwnerUnresolved,
		CodeNotMergeable,
		CodeMissingSHA,
		CodeNoMergePermission,
		CodeStrategyNotAllowed,
		CodeStaleHead,
		CodeValidation,
		CodeLabelPageTruncated,
		CodeBranchCannotMerge,
		CodeRepoArchived,
		CodeScopeInsufficient,
		CodeAlreadyEnqueued,
		CodeAPIVersionRetired,
		CodeMutationsDisabled,
		CodeRedirectHopCap,
		CodeRedirectPortRefused,
		CodeTLSRefused,
		CodeUnreachable,
		CodeResponseTooLarge,
		CodeConnectionInvalid,
		CodeAnonymousRefused,
		CodePlaintextRefused,
		CodePrivateAddressRefused,
		CodeBudgetInvalid,
		CodePageBoundInvalid,
		CodeListStateInvalid,
		CodeListOwnerInvalid,
		CodeListSinceInvalid,
		CodeListMergeStateInvalid,
		CodeRunUndated,
		CodeCursorInvalid,
		CodeRefInvalid,
		CodeHeaderReserved,
		CodeCapabilityUnsupported,
		CodeFamilyUndetected,
		CodeReconnectRequired,
		CodeGrantDenied,
		CodeGrantExpired,
		CodeGrantUnsupported,
		CodeAppKeyInvalid,
		CodeAppGrantInvalid,
	}
}

// DiagIDLength is the rendered length of [Error.DiagID]: 13 base32 characters,
// because base32 carries 5 bits per character and 64 random bits need 13.
const DiagIDLength = 13

// Error is one mapped failure, mapped by family, operation, status and selected
// headers, never by status alone, which is why Code exists beside Kind.
//
// Every operation here and in every family package answers nil, a *Error, or
// context.Canceled or context.DeadlineExceeded unchanged where its context
// ended, the library's own per-operation deadline included. Recover this type
// with errors.As on a *Error. It wraps nothing, so no dependency's error type
// reaches the surface; branch on Code and Kind, never on Message.
type Error struct { //nolint:govet // fieldalignment: field order here is declared order, which api/forgeapi.txt records
	// Op is the role method that failed, spelled as the method that names it,
	// such as "ListPRs" or "MergePR": the eighteen operations, "ConnectionCaps",
	// "GrantCaps" and "RepoAffordances", and the creds package's "Token",
	// "Mint", "Resolve", "StartDeviceGrant" and "Poll". The set is closed
	// because [Counters.Retried] and [Counters.ReadDeferred] take it as a label.
	// It is empty where the library refused before any operation began, the
	// local refusals and [CodeFamilyUndetected], except for
	// [CodeCapabilityUnsupported], whose operation began.
	Op string
	// Code is the machine-readable cause, one of the Code* constants, which a
	// consumer branches on. It is empty on one failure only: the read the
	// governor deferred to hold the mutation reserve, which answers
	// [KindRateLimited] with no status.
	Code string
	// Family is the family that answered, or [FamilyUnknown] where nothing
	// answered or no family was in hand, [ValidateCursor]'s refusals among them.
	// [CodeCapabilityUnsupported] sends nothing and still carries the family
	// detection identified.
	Family Family
	// Status is the real HTTP status, and zero exactly where no request was
	// made. A document error arriving at HTTP 200 keeps the 200.
	Status int
	// Kind is the class of failure.
	Kind ErrorKind
	// Retryable reports whether another attempt may succeed, not that this
	// library made one: a transport failure other than a TLS or
	// address-policy refusal, a throttle, and a 408, 429 or 5xx status other
	// than 501, 505, 506, 508, 510 and 511, whose answer repeats. Whether to
	// try again, and when, is the caller's decision.
	Retryable bool
	// Message is upstream's own message, with the request's credential and the
	// connection's own header values redacted out of it, sanitized and bounded.
	// It is never classified on: a caller branches on Code and Kind.
	Message string
	// RetryAfter is the wait upstream asked for, zero when it named none.
	RetryAfter time.Duration
	// DiagID is a per-failure random identifier, [DiagIDLength] characters,
	// carried in the user-visible error and in every log line for that
	// operation; never a session identifier or a lookup key, and not stable
	// across retries. A local refusal names no operation and carries none.
	DiagID string
	// Successor addresses the repository this one moved to, carried on
	// [CodeRepoRefStale] and nil everywhere else, and nil there too where the
	// successor could not be named: reconnect and relist rather than re-point.
	// It is filled as [Page.Successor] is.
	Successor *RepoRef
	// Capability is the capability the refused operation required, carried on
	// [CodeCapabilityUnsupported] and empty everywhere else.
	Capability Capability
	// Evidence is what detection reached that verdict on, carried on
	// [CodeCapabilityUnsupported] and zero everywhere else: the value
	// [Capabilities.ConnectionCaps] renders beside a disabled control.
	Evidence Evidence
}

// Error renders the operation, code, status and message. The separator after
// the operation belongs to the code, so the governor's deferral, which carries
// none, leaves no gap.
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

// localError builds a refusal reached before any operation began: no
// operation, family, status or diagnostic id exists to report.
func localError(code, message string) *Error {
	return &Error{Code: code, Kind: KindUnknown, Message: message}
}

var _ error = (*Error)(nil)
