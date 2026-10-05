package forgeapi

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// The budget defaults. A standalone consumer must not have to invent page caps
// and concurrency limits for forges it has not measured, so the library ships
// them and a consumer overrides what its own poller needs.
const (
	// DefaultPageBound is the page size a list asks for when no
	// [WithPageBound] is given. A family whose instance or document serves
	// fewer rows a page asks for at most that many, and its walk still reaches
	// every row.
	DefaultPageBound = 100
	// DefaultListPages is the number of pages one list operation traverses
	// before reporting [PartialPaginationCap].
	DefaultListPages = 3
	// DefaultStatusPages is the number of pages one folded commit status
	// traverses. It is a bound of its own rather than the list bound, because
	// the folded-status endpoint is paginated at the instance's own page size
	// and neither its folded state nor its total reveals a truncation.
	DefaultStatusPages = 3
	// DefaultStatusReadsPerInterval is how many folded statuses one client reads
	// per rolling interval of its own keeping, on the family that needs one
	// request per pull request. The remainder is reported partial and reached in
	// the next interval, which resumes from the [RotationCursor] the consumer
	// stored, so no row starves.
	DefaultStatusReadsPerInterval = 20
	// DefaultMutationReserve is the budget the governor holds back from reads,
	// in the instance's own units, when no [WithMutationReserve] is given. It is
	// sized for a burst of mutations with their follow-up reads inside one
	// window, and it is a small share of every in-scope instance's window: one
	// per cent of GitHub's hourly REST window and two and a half of gitlab.com's
	// per-minute one, so a client that never mutates loses no more than that.
	DefaultMutationReserve = 50
	// DefaultRetries is the per-request retry cap. It is an upper bound rather
	// than a behaviour: the per-phase transport bounds sum to more than
	// [DefaultOperationTimeout], so a stalled instance exhausts the operation
	// deadline inside one attempt and the retries are reachable only on
	// failures that fail fast.
	DefaultRetries = 2
	// DefaultReadConcurrency is the number of concurrent reads per instance.
	// Two, following the upstream guidance to prefer serial over concurrent.
	DefaultReadConcurrency = 2
	// DefaultMutationConcurrency is the number of concurrent mutations per
	// instance. One.
	DefaultMutationConcurrency = 1
)

// The budget's time defaults.
const (
	// DefaultOperationTimeout bounds one operation. It is applied as a context
	// deadline, which is the only bound that can interrupt an attempt already
	// in flight.
	DefaultOperationTimeout = 20 * time.Second
	// DefaultStatusTimePerInterval is the wall clock one client spends on
	// folded-status reads per rolling interval of its own keeping.
	DefaultStatusTimePerInterval = 60 * time.Second
	// DefaultMutationInterval is the minimum gap between consecutive mutations
	// on one instance, following the upstream instruction to pause between
	// mutative requests. It is a library default rather than something a
	// standalone consumer has to discover from a secondary rate limit.
	DefaultMutationInterval = time.Second
)

// DefaultRefreshLead is how long before its expiry a rotating credential is due
// for refresh when no [WithRefreshLead] is given.
const DefaultRefreshLead = 5 * time.Minute

// Budget is what one connection is allowed to spend. It is read and never
// filled: [DefaultBudget] publishes the shipped numbers, [Settings.Budget]
// reports what one client resolved, and each field names the one option that
// moves it, so a zero passed to that option is the literal zero.
//
// The per-interval fields bound a rolling interval the client keeps itself;
// nothing on this surface marks a polling cycle. The page size is the per-call
// [WithPageBound], and the reserve held back from reads is [WithMutationReserve].
type Budget struct {
	// ListPages is the page cap per list operation. [WithListPages] sets it.
	ListPages int
	// StatusPages is the page cap per folded commit status.
	// [WithStatusPages] sets it. It is a bound of its own rather than the list
	// bound, because the folded-status endpoint is paginated at the instance's
	// own page size and neither its folded state nor its total reveals a
	// truncation; it is also what bounds the COMPLETE fold on
	// [PullRequests.ReadPR], and it prices no list path at all, since a list
	// takes one page of the nested collection and marks the row.
	StatusPages int
	// StatusReadsPerInterval is the folded-status read cap per rolling interval,
	// on the family whose folded status costs one request per pull request.
	// [WithStatusReadsPerInterval] sets it. It is enforced: the read that would
	// exceed it is not issued and its row is marked [PartialBudget], and the
	// next interval starts each list at the row after the last one served there,
	// so no row starves under a poller presenting the same lists in the same
	// order. Each list rotates on its own; the [RotationCursor] the consumer
	// stored carries the rotation across a restart.
	StatusReadsPerInterval int
	// Retries is the per-request retry cap, an upper bound rather than a
	// behaviour for the reason [DefaultRetries] states. [WithRetries] sets it,
	// and a zero there is no retries.
	Retries int
	// ReadConcurrency is the concurrent-read limit per instance.
	// [WithReadConcurrency] sets it.
	ReadConcurrency int
	// MutationConcurrency is the concurrent-mutation limit per instance.
	// [WithMutationConcurrency] sets it.
	MutationConcurrency int
	// OperationTimeout bounds one operation, applied as a context deadline.
	// [WithOperationTimeout] sets it.
	OperationTimeout time.Duration
	// StatusTimePerInterval is the wall clock one rolling interval of
	// folded-status work is allowed. [WithStatusTimePerInterval] sets it. It is
	// enforced like StatusReadsPerInterval: a read that cannot be issued inside
	// the interval's remaining clock is not sent and its row is marked
	// [PartialBudget]. OperationTimeout bounds each read inside it, so the work
	// stops at whichever bound it reaches first. A consumer that wants its whole
	// polling cycle bounded brings its own context.
	StatusTimePerInterval time.Duration
	// MutationInterval is the minimum gap between mutations.
	// [WithMutationInterval] sets it.
	MutationInterval time.Duration
}

// DefaultBudget returns the budget an unconfigured client resolves: every field
// at its own Default* constant, the value each option's Default: clause names.
// It is a record to read, in the shape [ReservedHeaders] uses, never an input:
// a consumer moves a number one option at a time. Each call returns a fresh
// value.
func DefaultBudget() Budget {
	return Budget{
		ListPages:              DefaultListPages,
		StatusPages:            DefaultStatusPages,
		StatusReadsPerInterval: DefaultStatusReadsPerInterval,
		Retries:                DefaultRetries,
		ReadConcurrency:        DefaultReadConcurrency,
		MutationConcurrency:    DefaultMutationConcurrency,
		OperationTimeout:       DefaultOperationTimeout,
		StatusTimePerInterval:  DefaultStatusTimePerInterval,
		MutationInterval:       DefaultMutationInterval,
	}
}

// Counters is how the library's own event counts reach a consumer: a struct of
// functions over this package's own types, so no metrics library appears in any
// signature and a consumer wires whatever it already has.
//
// Every field is optional; a nil function means that event is not counted. The
// set is frozen at 1.0, so a new counter is a new field, which is additive.
type Counters struct {
	// UnknownEnumValue fires for every upstream enumeration value outside a
	// mapping table, beside the log line naming it, which is how a maintainer
	// learns an upstream enumeration grew. It carries the family and field and
	// never the value, which is upstream-controlled and unbounded and would hand
	// a labelled metric unbounded cardinality; the log line names the value,
	// bounded and rune-sanitized.
	UnknownEnumValue func(family Family, field string)
	// CapabilityContradicted fires where an inferred capability is later
	// contradicted by a probe.
	CapabilityContradicted func(family Family, capability Capability)
	// RateLimited fires on a throttle refusal.
	RateLimited func(family Family)
	// Retried fires per retried request.
	Retried func(family Family, op string)
	// PartialResult fires per partial result, by reason.
	PartialResult func(family Family, reason PartialReason)
	// RefreshOutcome fires per credential-refresh outcome, naming the terminal
	// state it reached.
	RefreshOutcome func(family Family, terminal CredState)
	// HelperDecline fires per git-credential-helper decline, by reason, one of
	// unowned_origin, expired, reconnect_required, spent, no_account and
	// store_unreadable. A decline is the whole diagnosis a user gets when git
	// exits without naming a cause, so it is counted as well as reported. It is
	// the one counter here that carries no family, deliberately: its commonest
	// decline is a URL no connection owns, where there is no family to name.
	HelperDecline func(reason string)
	// HelperErase fires per erase git sends the credential helper for an origin
	// a connection owns, by that connection's family. Git sends one after any
	// authentication failure, and the helper keeps the connection rather than
	// let one failed push take it down, so this count is where those failures
	// show; a token that is really revoked surfaces through the probe.
	HelperErase func(family Family)
	// EnvelopeError fires per document-envelope error, by upstream's own error
	// type.
	EnvelopeError func(family Family, errType string)
	// SunsetSeen fires when an instance announces that a pinned API version is
	// closing down. It is DORMANT at 1.0: the version this library pins has no
	// closing date scheduled, so no instance announces one for it, and it fires
	// the day a successor to that version ships and the pinned one enters its
	// closing window. The older version that is already in such a window is
	// one this library never sends.
	SunsetSeen func(family Family)
	// AddressRefusal fires when the address policy refuses a host, by family and
	// by the refusal's kind. The library counts these itself rather than relying
	// on the refusing library's own log line, which lands in a sink this one does
	// not own, and a refusal is the event most worth correlating with the
	// operation that failed, which is why the family is a parameter here rather
	// than an addition after the tag, when adding one would break every
	// implementer.
	AddressRefusal func(family Family, kind string)
	// RedirectHopCapExceeded fires when a redirect chain is refused for length.
	// It is what separates a redirect loop from an operation that merely ran
	// out of time.
	RedirectHopCapExceeded func(family Family)
	// RedirectPortRefused fires when a hop's port is outside the connection's
	// allowlist.
	RedirectPortRefused func(family Family)
	// NonDurableCredentialWrite fires when a credential write returned no error
	// but could not be proved durable, which this library treats as a failed
	// write.
	NonDurableCredentialWrite func(family Family)
	// ReadDeferred fires when the governor defers a read rather than issuing one
	// that would cross the mutation reserve, by family and operation. It is what
	// separates a [PartialRateLimited] the library chose from one upstream
	// imposed, since both carry that one partial reason. It is dormant on Gitea,
	// the one product that sends no budget signal. The operation is spelled as
	// [Error.Op] is, so its label set is closed with that field's.
	ReadDeferred func(family Family, op string)
}

// Settings is the resolved option state: what [Resolve] produces and a family's
// constructor reads.
//
// A consumer cannot construct an [Option], so it cannot reach these fields
// except through the With* functions. A new field is additive.
type Settings struct { //nolint:govet // fieldalignment: the field order is this record's own reading order and it is API for an unkeyed composite literal
	// Logger receives the library's own lines. Nil means slog.Default.
	Logger *slog.Logger
	// Credential is the token source every request authenticates with. Nil
	// means the connection is unauthenticated, which a family's constructor
	// refuses rather than sending anonymous requests.
	Credential CredentialSource
	// WireTransport replaces the wire under the library's own client stack.
	// Nil, which is the ordinary case, means the library builds its own.
	WireTransport http.RoundTripper
	// RequestObserver wraps the transport outside the retry layer, so what it
	// installs sees one call per logical request. Nil means no hook.
	RequestObserver func(next http.RoundTripper) http.RoundTripper
	// AttemptObserver wraps inside the retry layer, so what it installs sees
	// one call per attempt. Nil means no hook.
	AttemptObserver func(next http.RoundTripper) http.RoundTripper
	// Counters receives the event counts.
	Counters Counters
	// Budget is the resolved budget: [DefaultBudget]'s numbers with each knob
	// the caller set in place of its default.
	Budget Budget
	// MutationReserve is the budget the governor holds back from reads, in the
	// instance's own units.
	MutationReserve int
	// RotationCursor is where the rotation resumes, empty for a first run. It
	// is the value [WithRotationCursor] took and the one
	// [BudgetState.RotationCursor] hands back.
	RotationCursor RotationCursor
	// Mutations reports whether mutating operations are permitted.
	Mutations bool
	// PrivateAddresses reports whether this connection may reach a
	// private-range or single-label host.
	PrivateAddresses bool
	// PlaintextHTTP reports whether this connection may speak plaintext HTTP.
	PlaintextHTTP bool
	// RefreshLead is how long before expiry a rotating credential is due for
	// refresh, the value [WithRefreshLead] took.
	RefreshLead time.Duration
}

// Option configures a client. Every option takes a parameter and none signals by
// presence, so a caller composing options at runtime never has to rebuild an
// argument list; where two options set the same thing, the last one wins.
//
// The interface cannot be implemented outside this package, deliberately: an
// interface a consumer implements is the most frozen thing in a v1, and nothing
// here needs a consumer-supplied option.
type Option interface {
	applyOption(*Settings)
}

// Resolve applies defaults and then every option in order, and is what a
// family's constructor calls on the options it was handed.
func Resolve(opts ...Option) Settings {
	s := Settings{
		Budget:          DefaultBudget(),
		MutationReserve: DefaultMutationReserve,
		Mutations:       true,
		RefreshLead:     DefaultRefreshLead,
	}
	for _, o := range opts {
		if o != nil {
			o.applyOption(&s)
		}
	}
	return s
}

// WithMutations permits or refuses every mutating operation on this client.
// WithMutations(false) is enforced at the operation boundary with
// [CodeMutationsDisabled] on CreatePR, ClosePR, ReopenPR, MergePR, CreateIssue,
// CloseIssue, CreateRelease and RerunFailedChecks, which mutates no forge object
// but can carry deployment side effects. MergeStatus and ListRuns are reads.
//
// Default: true. An embedding that never sets it holds a client that can merge
// and close pull requests; read-only is the deliberate posture.
func WithMutations(allowed bool) Option {
	return optionFunc(func(s *Settings) { s.Mutations = allowed })
}

// The budget knobs, one option each and the only door to a [Budget]: an
// option's absence is its default, which its Default: clause names, so a zero
// passed to one is the literal zero. A negative is refused by a family's
// constructor with [CodeBudgetInvalid] on every knob: a negative cap or interval
// is a bound of negative size, and a negative deadline fails every call like a
// slow instance. The constructor refuses because [Resolve] returns one value.
// [WithReadConcurrency] and [WithMutationConcurrency] refuse a zero too, because
// a client admitting no request could run no operation at all.

// WithListPages sets how many pages one list operation traverses before
// reporting [PartialPaginationCap]. Zero traverses none.
//
// Default: [DefaultListPages].
func WithListPages(n int) Option {
	return optionFunc(func(s *Settings) { s.Budget.ListPages = n })
}

// WithStatusPages sets how many pages one folded commit status traverses. It is a
// bound of its own rather than the list bound, for the reason
// [Budget.StatusPages] states, and it is what bounds the complete fold on
// [PullRequests.ReadPR]. Zero traverses none.
//
// Default: [DefaultStatusPages].
func WithStatusPages(n int) Option {
	return optionFunc(func(s *Settings) { s.Budget.StatusPages = n })
}

// WithStatusReadsPerInterval sets the folded-status read cap per rolling
// interval, on the family whose folded status costs one request per pull
// request. Zero issues none, which leaves every such row partial with
// [PartialBudget]. The rotation is fair within one list, not across lists: a
// caller sweeping repositories through [PullRequests.ListPRs] gives the first
// lists the folds, and [PullRequests.ListMyPRs] spends none of the cap.
//
// Default: [DefaultStatusReadsPerInterval].
func WithStatusReadsPerInterval(n int) Option {
	return optionFunc(func(s *Settings) { s.Budget.StatusReadsPerInterval = n })
}

// WithRetries sets the per-request retry cap. Zero means no retries, which is a
// posture rather than an absence: a caller that wants one attempt asks for zero
// retries, and does not express it with a negative. A published price counts each
// request once; every attempt is sent, so a retried request adds up to n to the
// call's [BudgetState.LastCost].
//
// Default: [DefaultRetries].
func WithRetries(n int) Option {
	return optionFunc(func(s *Settings) { s.Budget.Retries = n })
}

// WithReadConcurrency sets the concurrent-read limit per instance. Zero admits no
// read at all, so it is REFUSED by a family's constructor with
// [CodeBudgetInvalid] rather than installed: a client that can issue no read is a
// client with no working operation on it.
//
// Default: [DefaultReadConcurrency].
func WithReadConcurrency(n int) Option {
	return optionFunc(func(s *Settings) { s.Budget.ReadConcurrency = n })
}

// WithMutationConcurrency sets the concurrent-mutation limit per instance. Zero
// admits no mutation at all, so it is REFUSED by a family's constructor with
// [CodeBudgetInvalid] rather than installed. A caller that wants a read-only client
// asks for one with [WithMutations](false), which refuses a mutation at the call
// with its own code and leaves every read working.
//
// Default: [DefaultMutationConcurrency].
func WithMutationConcurrency(n int) Option {
	return optionFunc(func(s *Settings) { s.Budget.MutationConcurrency = n })
}

// WithOperationTimeout bounds one operation. It is applied as a context
// deadline, which is the only bound that can interrupt an attempt already in
// flight. Zero is a deadline that has already passed.
//
// Default: [DefaultOperationTimeout].
func WithOperationTimeout(d time.Duration) Option {
	return optionFunc(func(s *Settings) { s.Budget.OperationTimeout = d })
}

// WithStatusTimePerInterval sets the wall clock one rolling interval of
// folded-status work is allowed. Zero allows none.
//
// Default: [DefaultStatusTimePerInterval].
func WithStatusTimePerInterval(d time.Duration) Option {
	return optionFunc(func(s *Settings) { s.Budget.StatusTimePerInterval = d })
}

// WithMutationInterval sets the minimum gap between consecutive mutations on one
// instance. Zero leaves no gap, which is what a secondary rate limit answers a
// burst for.
//
// Default: [DefaultMutationInterval].
func WithMutationInterval(d time.Duration) Option {
	return optionFunc(func(s *Settings) { s.Budget.MutationInterval = d })
}

// WithRotationCursor hands the library the [RotationCursor] a previous client
// reported through [BudgetState.RotationCursor], so this client resumes the
// rotation where that one stopped. It is checked as [ValidateCursor] checks a
// page cursor, never parsed, and a family's constructor refuses a malformed one
// with [CodeCursorInvalid]: something the consumer stored is corrupt.
//
// Default: the empty cursor, a first run. A family with nothing to rotate always
// reports it, so replaying the stored value needs no per-family branch.
func WithRotationCursor(c RotationCursor) Option {
	return optionFunc(func(s *Settings) { s.RotationCursor = c })
}

// WithMutationReserve sets the budget the governor holds back from reads, in the
// instance's own units: a read taking [BudgetState.Remaining] below n is
// deferred, marked [PartialRateLimited] and counted by [Counters.ReadDeferred],
// and no mutation is held back. Remaining is the forge's own figure, the whole
// account's on GitHub and GitLab. Gitea sends none, so the reserve is inert there.
//
// Default: [DefaultMutationReserve]. Zero holds nothing back, and a negative n is
// refused by a family's constructor with [CodeBudgetInvalid].
func WithMutationReserve(n int) Option {
	return optionFunc(func(s *Settings) { s.MutationReserve = n })
}

// WithCredentialSource sets the token source every request on this connection
// authenticates with. See [CredentialSource].
//
// Default: none, which a family's constructor refuses rather than sending
// anonymous requests: a client that silently reads only public data would look
// like a permission problem on the instance.
func WithCredentialSource(src CredentialSource) Option {
	return optionFunc(func(s *Settings) { s.Credential = src })
}

// WithRefreshLead sets how long before its expiry a rotating credential is due
// for refresh. The creds package's source reads it and clamps it to half the
// lifetime the credential was issued with, so a short lifetime cannot make every
// fresh token due on arrival.
//
// Default: [DefaultRefreshLead]. Zero refreshes a token only once it has
// expired. A negative d is refused by that source's constructor with
// [CodeBudgetInvalid], because a lead of negative size hands over expired tokens.
func WithRefreshLead(d time.Duration) Option {
	return optionFunc(func(s *Settings) { s.RefreshLead = d })
}

// WithWireTransport replaces the wire under this client's own HTTP stack, which
// is what a test substitutes to answer requests without a network. It bypasses
// containment: the address policy, the port allowlist, the trust anchor, the
// redirect policy and the connection's own proxy live in the transport it
// replaces, so a substitute enforces exactly what its author wrote. A client's
// Close does not reach it: a substitute's pool is its author's to release.
//
// Default: none, and the library builds its own transport. It never falls back
// to http.DefaultClient, which has no timeout and no redirect policy of ours.
func WithWireTransport(rt http.RoundTripper) Option {
	return optionFunc(func(s *Settings) { s.WireTransport = rt })
}

// WithRequestObserver installs a hook outside the retry layer, called once per
// logical request whatever the attempt count: the position for per-request
// accounting. The hook takes the next round tripper, returns one, and must pass
// the request on. A second WithRequestObserver replaces the first, so two hooks
// here compose in one wrap function. Both observer positions sit above the
// transport [WithWireTransport] replaces, so containment is unaffected.
//
// Default: none.
func WithRequestObserver(wrap func(next http.RoundTripper) http.RoundTripper) Option {
	return optionFunc(func(s *Settings) { s.RequestObserver = wrap })
}

// WithAttemptObserver installs a hook inside the retry layer, called once per
// attempt, so a retried request shows as more than one call. Its shape and
// containment are [WithRequestObserver]'s: the hook must pass the request on,
// and a second WithAttemptObserver replaces the first.
//
// Default: none.
func WithAttemptObserver(wrap func(next http.RoundTripper) http.RoundTripper) Option {
	return optionFunc(func(s *Settings) { s.AttemptObserver = wrap })
}

// WithLogger routes the library's own lines. The library retries, refreshes
// credentials and follows redirects, so it emits as part of its job rather than
// returning diagnostics and staying silent.
//
// Default: slog.Default.
func WithLogger(l *slog.Logger) Option {
	return optionFunc(func(s *Settings) { s.Logger = l })
}

// WithCounters installs the counter seam. See [Counters].
//
// Default: no counters, so every event is logged and none is counted.
//
//nolint:gocritic // hugeParam: the counter set is the published signature's own parameter
func WithCounters(c Counters) Option {
	return optionFunc(func(s *Settings) { s.Counters = c })
}

// WithPrivateAddresses permits this connection to reach a private-range address
// or a single-label host, the ordinary case for a self-hosted forge. It contains
// this library's own credentialed requests, not a process that can open a
// socket for itself, and the per-hop redirect revalidation runs regardless.
//
// Default: false. Reaching a private host is one deliberate statement per
// instance, so an agent driven by untrusted content cannot open one unasked.
func WithPrivateAddresses(allowed bool) Option {
	return optionFunc(func(s *Settings) { s.PrivateAddresses = allowed })
}

// WithPlaintextHTTP permits this connection to speak http rather than https.
// The token and every request then travel in cleartext on that network, a
// consequence the consumer names to the user. A redirect from https down to
// http stays refused whatever this is set to.
//
// Default: false.
func WithPlaintextHTTP(allowed bool) Option {
	return optionFunc(func(s *Settings) { s.PlaintextHTTP = allowed })
}

// ListSettings is the resolved per-call option state for a list operation.
type ListSettings struct {
	// After is the continuation to resume from, empty for a first page.
	After Cursor
	// Owner is the owner scope [WithOwner] named, empty where it named none.
	Owner string
	// PageBound is the page size to ask for.
	PageBound int
	// State is the state filter, [ListStateOpen] where the caller named none.
	State ListState
	// StateSet reports that the caller named the filter, whatever it named:
	// [WithState] sets it even for the default value. A family sees only this
	// struct, since [ListOption]'s mutator is unexported, and without this field
	// ResolveList() and ResolveList(WithState(ListStateOpen)) would resolve the
	// same. The stateful lists read State and ignore this; every other list
	// refuses whenever it is true.
	StateSet bool
	// OwnerSet reports that the caller named an owner scope, whatever it named,
	// for the reason StateSet exists: an empty owner is refused, and without
	// this field it resolves to the same value as no owner at all. The
	// cross-repository lists read Owner where it is true; every other list
	// refuses whenever it is true.
	OwnerSet bool
}

// ListOption configures one list call. Like [Option] it cannot be implemented
// outside this package, and last one wins.
type ListOption interface {
	applyListOption(*ListSettings)
}

// ResolveList applies defaults and then every list option in order, and refuses
// rather than clamps, with a [*Error]: [CodePageBoundInvalid] for a page bound
// that is not positive, [CodeListStateInvalid] for [ListStateUnknown],
// [CodeListOwnerInvalid] for an owner outside the form [WithOwner] states, and
// [CodeCursorInvalid] for a [WithAfter] cursor that is not well formed. It is
// not told which list it resolves for, so it records a named filter or owner in
// [ListSettings.StateSet] and [ListSettings.OwnerSet]; the family method then
// refuses one its list cannot take, under the filter's or the owner's code.
func ResolveList(opts ...ListOption) (ListSettings, error) {
	s := ListSettings{PageBound: DefaultPageBound, State: ListStateOpen}
	for _, o := range opts {
		if o != nil {
			o.applyListOption(&s)
		}
	}
	switch {
	case s.PageBound <= 0:
		return ListSettings{}, localError(CodePageBoundInvalid, "page bound is not positive")
	case s.State == ListStateUnknown:
		return ListSettings{}, localError(CodeListStateInvalid, "list state filter is the unknown member")
	case s.OwnerSet && !ownerInForm(s.Owner):
		return ListSettings{}, localError(CodeListOwnerInvalid, "owner is outside the shared form")
	}
	if err := ValidateCursor(s.After); err != nil {
		return ListSettings{}, err
	}
	return s, nil
}

// ValidateOwner refuses, without a request, an owner the family's
// cross-repository lists would refuse under [WithOwner]: one outside the shared
// form, and a path on a family whose owner is one name. Only GitLab takes a
// path, a group's full path; [FamilyUnknown] takes the single-name rule, as
// [ValidateSelector] gives it the single-separator one.
//
// A failure is [CodeListOwnerInvalid], the code the list answers, so an owner
// stored for later calls can be refused when it is stored.
func ValidateOwner(family Family, owner string) error {
	if !ownerInForm(owner) {
		return localError(CodeListOwnerInvalid, "owner is outside the shared form")
	}
	if family != FamilyGitLab && strings.Contains(owner, "/") {
		return localError(CodeListOwnerInvalid, "an owner on this family is one user or organization name, never a path")
	}
	return nil
}

// maxOwnerBytes bounds an owner as the shared form states it, separators
// included.
const maxOwnerBytes = 255

// ownerInForm reports whether an owner is in the shared form: one or more
// segments of ASCII letters, digits, '-', '_' and '.', joined by '/', no segment
// empty, "." or "..", and at most maxOwnerBytes in all.
func ownerInForm(owner string) bool {
	if owner == "" || len(owner) > maxOwnerBytes {
		return false
	}
	for segment := range strings.SplitSeq(owner, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, b := range []byte(segment) {
			if !ownerByte(b) {
				return false
			}
		}
	}
	return true
}

// ownerByte is the byte class an owner segment is made of.
func ownerByte(b byte) bool {
	switch {
	case 'a' <= b && b <= 'z', 'A' <= b && b <= 'Z', '0' <= b && b <= '9':
		return true
	}
	return b == '-' || b == '_' || b == '.'
}

// WithPageBound asks for n items per page. A page-numbered continuation
// resumes only at the bound it was minted at, as [WithAfter] states, so such a
// walk wanting another bound starts again from the first page.
//
// Default: [DefaultPageBound]. A zero or negative n is refused by
// [ResolveList] with [CodePageBoundInvalid] rather than sent.
func WithPageBound(n int) ListOption {
	return listOptionFunc(func(s *ListSettings) { s.PageBound = n })
}

// WithAfter resumes a list from the opaque cursor a previous [Page] returned.
// [ResolveList] refuses a malformed one with [CodeCursorInvalid], the check
// [ValidateCursor] makes without a request. A call differing from the minting
// one in family, API base, operation, repository, state filter or scope refuses
// it with that code before any request. A page-numbered one is also refused at
// another page bound; an upstream connection position resumes at any bound.
//
// Default: the first page.
func WithAfter(c Cursor) ListOption {
	return listOptionFunc(func(s *ListSettings) { s.After = c })
}

// WithState filters a listing by state. Only [PullRequests.ListPRs] and
// [Issues.ListIssues] take it: every other list refuses it, even at
// [ListStateOpen], with [CodeListStateInvalid], and so does a listing of issues
// asked for [ListStateMerged]. [PullRequests.ListMyPRs] and [Issues.ListMyIssues]
// list open items by construction, and [Checks.ListRuns] has no state to filter.
//
// Default: [ListStateOpen]. [ListStateUnknown] is refused by [ResolveList] with
// [CodeListStateInvalid] rather than sent.
func WithState(s ListState) ListOption {
	return listOptionFunc(func(ls *ListSettings) {
		ls.State = s
		ls.StateSet = true
	})
}

// WithOwner scopes a cross-repository list to every OPEN item under one owner,
// whoever authored it, in place of what the credential authored; the two scopes
// are exclusive. The owner is a user or organization login on GitHub, Gitea and
// Forgejo and a group's full path on GitLab, subgroups included, and it is never
// resolved by a read of its own: an unresolved owner answers [CodeOwnerUnresolved],
// or an empty page where the operation's per-forge block says the product cannot
// tell, so the call stays one request per page.
//
// Only [PullRequests.ListMyPRs] and [Issues.ListMyIssues] take it; every other
// list refuses it before any request with [CodeListOwnerInvalid], as both do an
// owner outside the shared form. The form is one or more segments of ASCII
// letters, digits, '-', '_' and '.', joined by '/', none empty, "." or "..", at
// most 255 bytes, with '/' accepted on GitLab alone; [ValidateOwner] answers the
// same refusal without a call. It is checked rather than escaped because the
// owner reaches a search string whose grammar this library does not own. An
// empty owner sets [ListSettings.OwnerSet] and is refused.
//
// Default: the credential's own authorship.
func WithOwner(owner string) ListOption {
	return listOptionFunc(func(ls *ListSettings) {
		ls.Owner = owner
		ls.OwnerSet = true
	})
}
