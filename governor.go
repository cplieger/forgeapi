package forgeapi

import "time"

// RotationCursor is the opaque rotation cursor a consumer persists across client
// lifetimes: where the library's rotation of the reads it pays one request each
// for stopped, so the next client resumes at the row after the last one served
// rather than at the first.
//
// Like [Cursor] it is this library's own encoding and is never interpreted. A
// consumer hands the value it stored to [WithRotationCursor] unchanged, reads the
// current one back from [BudgetState.RotationCursor], and validates it as opaque
// rather than parsing it. The empty RotationCursor is a first run, and it is the
// only value the two families with nothing to rotate ever report.
//
// The round trip exists because durable storage is the one thing a consumer has
// and this library does not: the only store it ships is the credential store and
// a cursor is not a credential. So the library holds the cursor for the life of
// one client and the consumer holds it between two.
//
// It is a type of its own rather than a [Cursor] because the two are not
// interchangeable: a page cursor resumes one list and this one resumes a rotation
// across repositories, and handing one to the other's entry point is a compile
// error here rather than a rotation resumed from garbage. The refusal spelling is
// shared, [CodeCursorInvalid], because the remedy at a consumer is identical,
// that something it stored is corrupt, and a second spelling would be wire
// vocabulary a 1.0 freezes for no branch anyone takes.
type RotationCursor string

// BudgetRemainingUnknown is [BudgetState.Remaining] where the instance sends no
// budget signal, which of the four products is Gitea alone: Forgejo sends a
// structured `ratelimit` header the Gitea family parses like the other two
// families' signals. It is negative rather than zero on purpose: a zero would
// read as a budget spent to its last unit, and a poller that believed it would
// stop.
const BudgetRemainingUnknown = -1

// BudgetState is the governor's state for one connection: what the instance
// last reported its remaining budget to be, when that budget renews, what the
// last call cost, and where the rotation stands.
//
// It is read through one accessor, [Governor.BudgetState], which every family
// client implements and [Core] carries, and never written from outside. The
// governor paces the connection's reads from it: it holds back the reserve
// [WithMutationReserve] sets, so a cycle of reads never spends what the merge the
// user is about to click will need, and DEFERS a read that would cross the
// reserve rather than issuing it. A deferred read is marked partial with
// [PartialRateLimited], the same word an upstream throttle carries; the two are
// told apart here, where a deferral leaves Remaining above zero, and counted
// apart by [Counters.ReadDeferred]. The governor sits under the per-interval caps
// [Budget] publishes rather than replacing them, so the work is bounded by
// whichever it reaches first.
//
// The signal is per PRODUCT rather than per family. GitHub, GitLab and Forgejo
// each report remaining and reset on every response, two of them from a
// rate-limit header and GitLab's documents priced by their own complexity;
// Gitea alone sends nothing, so there Remaining is [BudgetRemainingUnknown],
// Reset is the zero time, and LastCost is this library's own per-call price, one
// per request. That is the neutral value in the field rather than a field
// omitted, so a consumer renders one shape for every family.
//
// What the budget belongs to is the USER rather than this library, and that is
// the reading of the whole type. On GitHub and GitLab the quota a response
// reports is the account's primary one, drawn on by every application acting for
// that user, so Remaining moves when something else spends and this library's own
// consumption is not recoverable from it. The reserve [WithMutationReserve] sets
// is therefore measured against that SHARED pool, which is the useful direction:
// the client backs off when anyone has drained the account rather than only when
// it has drained it itself, and it costs nothing, because the number is already
// in hand on every response.
type BudgetState struct { //nolint:govet // fieldalignment: field order is the reading order this type documents, remaining first, and it is API for an unkeyed composite literal
	// Remaining is the budget left in the current window, in the instance's own
	// units: a REST endpoint counts requests and a document endpoint counts
	// points. What one call spends in those units is per product, and every read
	// this library issues is unconditional, so no call of its own is answered by
	// the one status a product prices at nothing.
	// It is [BudgetRemainingUnknown] where the instance reports none.
	//
	// It is what the FORGE reports for the credential, which on GitHub and GitLab
	// is the user's own quota rather than this client's share of it: every
	// application acting for that user draws on the same pool, so this number
	// falls while this client sends nothing and a consumer that renders it as
	// "what the library has used" is showing something false. Rendered as the
	// account's remaining budget it is exactly right, and it is the number the
	// mutation reserve is held against.
	Remaining int
	// Reset is when the window renews, or the zero time where the instance
	// reports none.
	Reset time.Time
	// LastCost is what the last call spent, in the same units as Remaining.
	LastCost int
	// RotationCursor is where the rotation stands now: the value to store and
	// hand to the next client's [WithRotationCursor]. It is empty on a family
	// with nothing to rotate, which is the neutral value every family answers
	// rather than a special case.
	//
	// It is the one field here a consumer STORES rather than displays. The
	// three above are facts about the connection a user interface renders; this
	// one is an opaque token whose only reader is this library.
	RotationCursor RotationCursor
}
