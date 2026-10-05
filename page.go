package forgeapi

import "fmt"

// Cursor is an opaque forward continuation. It is the library's own encoding and
// a consumer never interprets it: it is handed back to the next call unchanged,
// and a consumer that puts it in a URL validates it as opaque rather than
// parsing it. The empty Cursor means the list is complete.
type Cursor string

// ValidateCursor refuses a [Cursor] that is not a well-formed continuation: a
// byte cap and the allowed character class of this library's own encoding,
// checked WITHOUT interpreting it. A failure is [CodeCursorInvalid] and never a
// request.
//
// It is exported for the same reason [ValidateSelector] is, and it is the half of
// [Cursor]'s contract a consumer cannot write for itself: a cursor comes back
// from a client as untrusted input on its way into an upstream request, the
// library owns the encoding, and a consumer that validated by parsing would be
// interpreting a value this type says it must not. [ResolveList] performs the
// same check on [WithAfter].
//
// The empty Cursor is valid and means the first page.
func ValidateCursor(c Cursor) error {
	if c == "" {
		return nil
	}
	if len(c) > maxPathValueBytes {
		return localError(CodeCursorInvalid, "continuation is over the byte cap")
	}
	for i := range len(c) {
		if !isCursorByte(c[i]) {
			return localError(CodeCursorInvalid, "continuation carries a byte outside this library's encoding")
		}
	}
	return nil
}

// isCursorByte reports whether one byte is in the class this library's own
// continuation encoding uses. The check is over the class alone: a cursor is
// opaque by contract, so validating it by parsing would be interpreting a value
// this type says nobody interprets.
func isCursorByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	switch b {
	case '-', '_', '.', '=', ':':
		return true
	}
	return false
}

// PartialReason is why a result is incomplete.
type PartialReason int

// The PartialReason members.
const (
	// PartialUnknown is the zero value: a result marked partial for a reason
	// nothing recorded, which is a defect rather than an outcome.
	PartialUnknown PartialReason = iota
	// PartialBudget means a per-operation bound, or one of the two per-interval
	// bounds the client keeps over its own rolling window, stopped the work.
	PartialBudget
	// PartialRateLimited has two producers and one word, because the remedy at
	// a consumer is the same wait. The instance refused further requests, which
	// fails fast for that instance rather than consuming its retries; or the
	// connection's governor DEFERRED the read rather than spend the mutation
	// reserve [WithMutationReserve] holds back. A consumer that wants to tell
	// them apart reads [BudgetState], where a deferral leaves the remaining
	// budget above zero, and the two are counted apart by
	// [Counters.RateLimited] and [Counters.ReadDeferred].
	PartialRateLimited
	// PartialGraphQLPartial means a document answered with data AND errors, so
	// the data is returned beside this marker rather than silently truncated or
	// failed outright.
	PartialGraphQLPartial
	// PartialPaginationCap means a page bound was reached. On [Page.Partial] it is
	// the list's page cap and always arrives with a [Cursor]: ask for the next page.
	// On an item or a single read a collection inside it stopped short, with no
	// cursor: a list row's checks had a further page (read that pull request alone;
	// [ActionState.Checks] states the verdict beside it per product); a complete fold
	// on [CommitChecks.Partial] or a single read reached [Budget.StatusPages] (widen
	// it); or a GitLab label connection named a further page, [Partial.Fetched]
	// counting the labels held and [Partial.OmittedAtLeast] its count less them, at least one.
	PartialPaginationCap
	// PartialLabelsNotApplied means a creation made its object and the second
	// request that applies the labels the call named did not succeed, so the
	// object exists without them. It is set on the created object's own marker,
	// which arrives beside the error naming what was not done, because an error
	// alone would have the caller create a second object on the retry. Its
	// [Partial.Fetched] is zero and its [Partial.OmittedAtLeast] is the number
	// of labels the call named, since that request applies them all or none.
	// Only a product whose creation route declares no labels produces it.
	PartialLabelsNotApplied
	// PartialResultWindow means the route stopped serving rows while its own
	// stated total says more exist, and no continuation reaches them, because
	// the remainder lies outside what the route serves at all. It rides
	// [Page.Partial] on the page where the walk ends, and it is the one
	// response-level reason that arrives WITHOUT a [Cursor]. Its
	// [Partial.Fetched] counts the rows the whole walk was served rather than
	// the one page, and its [Partial.OmittedAtLeast] is the stated total less
	// that, so the two sum to the total the route stated. A consumer renders
	// it as a list holding more rows than the route will serve, never as a
	// complete one. The cross-repository lists ([PullRequests.ListMyPRs],
	// [Issues.ListMyIssues]) produce it on a product whose search serves a
	// fixed window of results; a page whose document also answered errors
	// carries [PartialGraphQLPartial] instead, since that says the page's own
	// rows are in question. [Checks.ListRuns] produces it on a page that
	// served no row while the listing's stated total says more than the walk
	// was served.
	PartialResultWindow
)

// String returns the member's wire spelling: "unknown", "budget",
// "rate_limited", "graphql_partial", "pagination_cap", "labels_not_applied"
// or "result_window".
func (p PartialReason) String() string {
	return nameOf([]string{nameUnknown, "budget", "rate_limited", "graphql_partial", "pagination_cap", "labels_not_applied", "result_window"}, p)
}

var _ fmt.Stringer = PartialReason(0)

// Partial describes an incomplete result. A complete result carries no Partial
// at all, which is why every field on it is meaningful only when it is present.
type Partial struct {
	// Reason is why the result stopped short.
	Reason PartialReason
	// Fetched is how many items were actually read. On [PartialResultWindow]
	// it counts the whole walk rather than one page.
	Fetched int
	// OmittedAtLeast is a lower bound on what was left, because upstream
	// rarely says how much remains.
	OmittedAtLeast int
}

// Page is one page of a list operation, with the continuation and the partiality
// beside the items.
//
// No list operation returns a bare slice. A caller told "there is a remainder"
// with no way to ask for it has only one recourse, which is to re-run the same
// capped call, and adding the continuation after the tag would mean a second
// role interface for an operation that already has one.
type Page[T any] struct { //nolint:govet // fieldalignment: the items come first because that is the reading order this type documents, and field order is API for an unkeyed composite literal
	// Items are the rows read, in the order upstream returned them.
	Items []T
	// Next is the continuation, empty when the list is complete.
	Next Cursor
	// Partial is nil when the page is everything that was asked for.
	Partial *Partial
	// Successor addresses the repository the one this list addressed had moved
	// to, where the read followed the redirect. The rows are the successor's
	// and they are
	// returned rather than refused, because following is right and following
	// SILENTLY is not: a consumer re-points its stored identifier to this
	// reference. It is nil on every ordinary read, and nil too where the move
	// was detected but the successor could not be resolved, which reaches the
	// caller as [CodeRepoRefStale] instead.
	//
	// It is spelled as [Error.Successor] is, because it is the same value
	// answering the same question on the success arm: one concept carries one
	// name, and nil is the one absence test on every carrier.
	Successor *RepoRef
}
