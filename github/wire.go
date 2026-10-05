package github

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// The operation names this family spells, which are the role methods' own. They are
// constants because each of them decides an error-mapping cell, so a typo there
// would silently change a refusal.
const (
	opMergePR    = "MergePR"
	opListIssues = "ListIssues"
)

// sigil is how a human writes this product's pull-request number. It is a rendering
// concern and never enters a request path.
const sigil = "#"

// The enumeration members this family spells in more than one place: two of the check
// tables share them with the merge answer's own statuses, and the draft member is both a
// merge state and a request field.
const (
	memberPending = "pending"
	memberQueued  = "queued"
	memberDraft   = "draft"
	memberMerge   = "merge"
	memberSquash  = "squash"
	memberMerged  = "merged"
)

// The GraphQL wire shapes. They are declared SEPARATELY from the REST shapes below
// and no struct serves both, which is a measured rule rather than a stylistic one:
// a pull request's head commit is `headRefOid` on a document and `head.sha` over
// REST, its labels are a connection here and an array of objects there, its state
// is screaming snake here and lower snake there, and the merge-state field the
// document carries has no REST twin at all. One shared struct across the two
// transports decodes neither.
type (
	docActor struct {
		Login string `json:"login"`
	}

	docPageInfo struct {
		EndCursor   string `json:"endCursor"`
		HasNextPage bool   `json:"hasNextPage"`
	}

	docLabel struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}

	docLabels struct {
		Nodes      []docLabel `json:"nodes"`
		TotalCount int        `json:"totalCount"`
	}

	// docStateCount is one member of the per-state count arrays the rollup's
	// contexts connection carries. Those arrays are what make a bounded fold exact
	// on this product: measured across three pages of a 446-context collection,
	// they are byte-identical and describe the COLLECTION rather than the page.
	docStateCount struct {
		State string `json:"state"`
		Count int    `json:"count"`
	}

	// docContext is one node of the rollup's contexts connection, which is a UNION
	// of two types. Both halves are decoded into one struct because the discriminator
	// is a selected field and the two halves share no key: a status context carries
	// `context` and `state`, a check run carries `name` beside a `status` and a
	// nullable `conclusion`, and which set is populated is what `__typename` says.
	docContext struct {
		Typename    string `json:"__typename"`
		Context     string `json:"context"`
		Description string `json:"description"`
		TargetURL   string `json:"targetUrl"`
		State       string `json:"state"`
		Name        string `json:"name"`
		Conclusion  string `json:"conclusion"`
		Status      string `json:"status"`
		DetailsURL  string `json:"detailsUrl"`
	}

	docContexts struct { //nolint:govet // fieldalignment: the field order is the document's own selection order, so a reader compares this type against the query it decodes
		TotalCount                 int             `json:"totalCount"`
		CheckRunCount              int             `json:"checkRunCount"`
		StatusContextCount         int             `json:"statusContextCount"`
		CheckRunCountsByState      []docStateCount `json:"checkRunCountsByState"`
		StatusContextCountsByState []docStateCount `json:"statusContextCountsByState"`
		PageInfo                   docPageInfo     `json:"pageInfo"`
		Nodes                      []docContext    `json:"nodes"`
	}

	docRollup struct { //nolint:govet // fieldalignment: the field order is the wire shape's own, so a reader compares this type against the bytes it decodes
		State    string       `json:"state"`
		Contexts *docContexts `json:"contexts"`
	}

	docCommit struct { //nolint:govet // fieldalignment: the field order is the wire shape's own, so a reader compares this type against the bytes it decodes
		OID               string     `json:"oid"`
		StatusCheckRollup *docRollup `json:"statusCheckRollup"`
	}

	docCommitNode struct {
		Commit *docCommit `json:"commit"`
	}

	docCommits struct {
		Nodes []docCommitNode `json:"nodes"`
	}

	docAutoMerge struct {
		EnabledAt   *time.Time `json:"enabledAt"`
		MergeMethod string     `json:"mergeMethod"`
	}

	// armData is the arm document's payload: null where the arm was refused, and the
	// pull request's auto-merge request where it was armed.
	armData struct {
		Enable *struct {
			PullRequest *struct {
				AutoMergeRequest *docAutoMerge `json:"autoMergeRequest"`
			} `json:"pullRequest"`
		} `json:"enablePullRequestAutoMerge"`
	}

	docQueueEntry struct { //nolint:govet // fieldalignment: the field order is the wire shape's own, so a reader compares this type against the bytes it decodes
		State    string `json:"state"`
		Position *int   `json:"position"`
	}

	docPullRequest struct { //nolint:govet // fieldalignment: the field order is the document's own selection order, so a reader compares this type against the response it decodes
		Number           int            `json:"number"`
		Title            string         `json:"title"`
		Body             string         `json:"body"`
		URL              string         `json:"url"`
		CreatedAt        time.Time      `json:"createdAt"`
		UpdatedAt        time.Time      `json:"updatedAt"`
		IsDraft          bool           `json:"isDraft"`
		State            string         `json:"state"`
		Author           *docActor      `json:"author"`
		HeadRefName      string         `json:"headRefName"`
		HeadRepository   *docRepoRef    `json:"headRepository"`
		BaseRefName      string         `json:"baseRefName"`
		HeadRefOID       string         `json:"headRefOid"`
		Mergeable        string         `json:"mergeable"`
		MergeStateStatus string         `json:"mergeStateStatus"`
		Merged           bool           `json:"merged"`
		AutoMergeRequest *docAutoMerge  `json:"autoMergeRequest"`
		MergeQueueEntry  *docQueueEntry `json:"mergeQueueEntry"`
		Labels           *docLabels     `json:"labels"`
		Commits          *docCommits    `json:"commits"`
		Repository       *docRepoRef    `json:"repository"`
	}

	// docRepoRef is the per-row repository the cross-repository document selects,
	// which that operation's rows need because the call addresses no repository.
	docRepoRef struct {
		NameWithOwner string `json:"nameWithOwner"`
	}

	docPullConnection struct { //nolint:govet // fieldalignment: the field order is the wire shape's own, so a reader compares this type against the bytes it decodes
		TotalCount int              `json:"totalCount"`
		PageInfo   docPageInfo      `json:"pageInfo"`
		Nodes      []docPullRequest `json:"nodes"`
	}

	docRepository struct { //nolint:govet // fieldalignment: the field order is the document's own selection order, so a reader compares this type against the query it decodes
		NameWithOwner    string             `json:"nameWithOwner"`
		ViewerPermission string             `json:"viewerPermission"`
		PullRequests     *docPullConnection `json:"pullRequests"`
		PullRequest      *docPullRequest    `json:"pullRequest"`
		Object           *docCommit         `json:"object"`
	}

	// docSearch is one page of the pull-request search. Its nodes are a list of
	// nullable union members, as the schema declares them, so each is read as a
	// pointer beside the member's own type name: a node nulled by a field error is
	// absent, and a member of another type is not a pull request.
	docSearch struct { //nolint:govet // fieldalignment: the field order is the wire shape's own, so a reader compares this type against the bytes it decodes
		IssueCount int              `json:"issueCount"`
		PageInfo   docPageInfo      `json:"pageInfo"`
		Nodes      []*docSearchPull `json:"nodes"`
	}

	// docSearchPull is one node of the pull-request search: the union member's type
	// name and, for a pull request, its fields.
	docSearchPull struct {
		docPullRequest
		Typename string `json:"__typename"`
	}

	// docSearchIssue is one node of the issue search, read as [docSearchPull] is.
	docSearchIssue struct {
		docIssue
		Typename string `json:"__typename"`
	}

	// docIssue is one issue row of the cross-repository issue search. It is a type
	// of its own rather than a reading of the pull-request node, because an issue
	// node is selected with the issue fields alone.
	docIssue struct { //nolint:govet // fieldalignment: the field order is the document's own selection order, so a reader compares this type against the response it decodes
		Number     int         `json:"number"`
		Title      string      `json:"title"`
		Body       string      `json:"body"`
		URL        string      `json:"url"`
		CreatedAt  time.Time   `json:"createdAt"`
		UpdatedAt  time.Time   `json:"updatedAt"`
		State      string      `json:"state"`
		Author     *docActor   `json:"author"`
		Labels     *docLabels  `json:"labels"`
		Repository *docRepoRef `json:"repository"`
	}

	docIssueSearch struct { //nolint:govet // fieldalignment: the field order is the wire shape's own, so a reader compares this type against the bytes it decodes
		IssueCount int               `json:"issueCount"`
		PageInfo   docPageInfo       `json:"pageInfo"`
		Nodes      []*docSearchIssue `json:"nodes"`
	}

	docRateLimit struct { //nolint:govet // fieldalignment: the field order is the document's own selection order, so a reader compares this type against the query it decodes
		Cost      int       `json:"cost"`
		Limit     int       `json:"limit"`
		NodeCount int       `json:"nodeCount"`
		Remaining int       `json:"remaining"`
		ResetAt   time.Time `json:"resetAt"`
		Used      int       `json:"used"`
	}

	// docPayload is the decoded `data` of every pull-request and commit document this
	// family ships. One type serves them because they differ in WHICH selection they
	// populate rather than in the vocabulary they speak: a list fills pullRequests, a
	// read fills pullRequest, the cross-repository read fills search and the commit
	// read fills object, and a caller of one reads nothing of the others'. The rule
	// that forbids a shared struct is about the two TRANSPORTS, where the same field
	// arrives in two shapes.
	docPayload struct {
		RateLimit  *docRateLimit  `json:"rateLimit"`
		Repository *docRepository `json:"repository"`
		Search     *docSearch     `json:"search"`
	}

	// docIssuePayload is the decoded `data` of the cross-repository issue document,
	// whose search selection holds issue rows under the key the pull-request search
	// fills with pull-request rows, which is why it cannot share [docPayload].
	docIssuePayload struct {
		RateLimit *docRateLimit   `json:"rateLimit"`
		Search    *docIssueSearch `json:"search"`
	}
)

// cost is what the endpoint charged for the document that filled this payload, read
// off the budget object every document selects. Measured at ONE for both
// pull-request documents at every shape read, and at three for the cross-repository
// search at a page of a hundred, which is why the assertion on it is a ceiling at a
// stated page size rather than an equality.
func (p *docPayload) cost() int {
	return p.RateLimit.charged()
}

// cost is what the endpoint charged for the issue document, read off the same budget
// object.
func (p *docIssuePayload) cost() int {
	return p.RateLimit.charged()
}

// cost is zero for the arm: a mutation selects no budget object, so the endpoint names
// no charge in the payload.
func (*armData) cost() int {
	return 0
}

// charged is the cost one budget object reports, zero where the document selected
// none.
func (r *docRateLimit) charged() int {
	if r == nil {
		return 0
	}
	return r.Cost
}

// The REST wire shapes.
type (
	restUser struct {
		Login  string `json:"login"`
		Name   string `json:"name"`
		Email  string `json:"email"`
		WebURL string `json:"html_url"`
	}

	restOwner struct {
		Login string `json:"login"`
	}

	restPermissions struct {
		Admin bool `json:"admin"`
		Push  bool `json:"push"`
		Pull  bool `json:"pull"`
	}

	restRepo struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		FullName      string           `json:"full_name"`
		Name          string           `json:"name"`
		Owner         *restOwner       `json:"owner"`
		Description   string           `json:"description"`
		WebURL        string           `json:"html_url"`
		CloneURL      string           `json:"clone_url"`
		UpdatedAt     time.Time        `json:"updated_at"`
		Private       bool             `json:"private"`
		Archived      bool             `json:"archived"`
		Fork          bool             `json:"fork"`
		DefaultBranch string           `json:"default_branch"`
		HasIssues     *bool            `json:"has_issues"`
		Permissions   *restPermissions `json:"permissions"`
		// The three merge-strategy flags are POINTERS because their absence is the
		// measured departure this product's listing row carries: the minimal
		// repository representation omits them, so a listed repository answers an
		// empty strategy set rather than three false flags.
		AllowMergeCommit *bool `json:"allow_merge_commit"`
		AllowSquashMerge *bool `json:"allow_squash_merge"`
		AllowRebaseMerge *bool `json:"allow_rebase_merge"`
	}

	restLabel struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}

	restRef struct {
		Repo *restRepoName `json:"repo"`
		Ref  string        `json:"ref"`
		SHA  string        `json:"sha"`
	}

	// restRepoName is the repository a REST branch reference lives in, null on a
	// pull request whose fork was deleted.
	restRepoName struct {
		FullName string `json:"full_name"`
	}

	restAutoMerge struct {
		MergeMethod string `json:"merge_method"`
	}

	restPull struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		Number         int            `json:"number"`
		NodeID         string         `json:"node_id"`
		State          string         `json:"state"`
		Title          string         `json:"title"`
		Body           string         `json:"body"`
		User           *restUser      `json:"user"`
		WebURL         string         `json:"html_url"`
		CreatedAt      time.Time      `json:"created_at"`
		UpdatedAt      time.Time      `json:"updated_at"`
		Draft          bool           `json:"draft"`
		Labels         []restLabel    `json:"labels"`
		Head           *restRef       `json:"head"`
		Base           *restRef       `json:"base"`
		Merged         bool           `json:"merged"`
		Mergeable      *bool          `json:"mergeable"`
		MergeableState string         `json:"mergeable_state"`
		AutoMerge      *restAutoMerge `json:"auto_merge"`
	}

	restIssue struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		Number    int         `json:"number"`
		State     string      `json:"state"`
		Title     string      `json:"title"`
		Body      string      `json:"body"`
		User      *restUser   `json:"user"`
		WebURL    string      `json:"html_url"`
		CreatedAt time.Time   `json:"created_at"`
		UpdatedAt time.Time   `json:"updated_at"`
		Labels    []restLabel `json:"labels"`
		// PullRequest is the key that makes a row of the issues route a PULL
		// REQUEST. It is read to DROP that row, because this route's population is
		// issues and pull requests both and the operation publishes issues.
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	}

	restRelease struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		TagName     string     `json:"tag_name"`
		Name        string     `json:"name"`
		Body        string     `json:"body"`
		WebURL      string     `json:"html_url"`
		PublishedAt *time.Time `json:"published_at"`
		Draft       bool       `json:"draft"`
		Prerelease  bool       `json:"prerelease"`
		Target      string     `json:"target_commitish"`
	}

	restStatus struct {
		Context     string `json:"context"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
		State       string `json:"state"`
	}

	// restCombined is the combined-status endpoint's answer. Its `state` is NOT
	// read as the verdict: measured on an Actions-only repository, that endpoint
	// answers an empty statuses array byte-identically to a commit with no CI at
	// all, so folding its state reports a green nothing earned.
	restCombined struct { //nolint:govet // fieldalignment: the field order is the wire shape's own, so a reader compares this type against the bytes it decodes
		State      string       `json:"state"`
		SHA        string       `json:"sha"`
		TotalCount int          `json:"total_count"`
		Statuses   []restStatus `json:"statuses"`
	}

	restCheckRun struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		DetailsURL string `json:"details_url"`
		HeadSHA    string `json:"head_sha"`
	}

	restCheckRuns struct { //nolint:govet // fieldalignment: the field order is the wire shape's own, so a reader compares this type against the bytes it decodes
		TotalCount int            `json:"total_count"`
		CheckRuns  []restCheckRun `json:"check_runs"`
	}

	restWorkflowRun struct { //nolint:govet // fieldalignment: the field order is the wire shape's own, so a reader compares this type against the bytes it decodes
		ID         int64     `json:"id"`
		Name       string    `json:"name"`
		HeadBranch string    `json:"head_branch"`
		HeadSHA    string    `json:"head_sha"`
		Status     string    `json:"status"`
		Conclusion string    `json:"conclusion"`
		WebURL     string    `json:"html_url"`
		CreatedAt  time.Time `json:"created_at"`
		UpdatedAt  time.Time `json:"updated_at"`
	}

	restWorkflowRuns struct { //nolint:govet // fieldalignment: the field order is the wire shape's own, so a reader compares this type against the bytes it decodes
		TotalCount int               `json:"total_count"`
		Runs       []restWorkflowRun `json:"workflow_runs"`
	}

	// restMeta is the metadata endpoint's answer. The version member is what an
	// APPLIANCE reports and dotcom omits, which is how the connection read tells
	// the family's two products apart.
	restMeta struct { //nolint:govet // fieldalignment: the field order is the wire shape's own, so a reader compares this type against the bytes it decodes
		InstalledVersion string `json:"installed_version"`
		PasswordAuth     *bool  `json:"verifiable_password_authentication"`
	}

	// restMergeAnswer is the asynchronous merge's answer: its status names where
	// the merge got to and `details.uuid` the pending merge's handle.
	restMergeAnswer struct { //nolint:govet // fieldalignment: the field order is the wire shape's own, so a reader compares this type against the bytes it decodes
		Status  string `json:"status"`
		Details *struct {
			UUID string `json:"uuid"`
		} `json:"details"`
	}
)

// statusStates is this product's own status-context state enumeration, mapped
// TOTALLY over the five members its schema declares. Both transports send the same
// member set in two cases, so the table is keyed on the lower-snake spelling and a
// document's screaming-snake value is folded to it before the lookup; a member added
// upstream in either case therefore misses the table and is counted and named rather
// than folded into a verdict.
//
// Measured by introspection on api.github.com, 2026-09-18: StatusState declares
// EXPECTED, ERROR, FAILURE, PENDING and SUCCESS, and none is deprecated.
var statusStates = map[string]forgeapi.CheckState{
	"success":     forgeapi.CheckPassing,
	"failure":     forgeapi.CheckFailing,
	"error":       forgeapi.CheckFailing,
	memberPending: forgeapi.CheckPending,
	"expected":    forgeapi.CheckPending,
	"":            forgeapi.CheckUnknown,
}

// checkRunStates is the enumeration the rollup's per-state COUNTS speak, mapped
// totally over the fourteen members its schema declares. It is a UNION of a check
// run's status and its conclusion, which is why status-shaped and conclusion-shaped
// members sit in one table.
//
// COMPLETED is the one member that names no verdict: it is the status half saying
// the run finished, with the conclusion that would say what it finished as living in
// the other half, so it answers the unknown member rather than a green.
//
// Measured by introspection on api.github.com, 2026-09-18: CheckRunState declares
// fourteen members, CheckConclusionState nine and CheckStatusState six, and the
// conclusion half is what makes [forgeapi.CheckNeutral] reachable on this product at
// all.
var checkRunStates = map[string]forgeapi.CheckState{
	"success":          forgeapi.CheckPassing,
	"failure":          forgeapi.CheckFailing,
	"timed_out":        forgeapi.CheckFailing,
	"startup_failure":  forgeapi.CheckFailing,
	"action_required":  forgeapi.CheckFailing,
	"neutral":          forgeapi.CheckNeutral,
	"skipped":          forgeapi.CheckNeutral,
	"cancelled":        forgeapi.CheckNeutral,
	"stale":            forgeapi.CheckNeutral,
	memberQueued:       forgeapi.CheckPending,
	"in_progress":      forgeapi.CheckPending,
	memberPending:      forgeapi.CheckPending,
	"waiting":          forgeapi.CheckPending,
	"requested":        forgeapi.CheckPending,
	"completed":        forgeapi.CheckUnknown,
	"expected":         forgeapi.CheckPending,
	"error":            forgeapi.CheckFailing,
	"":                 forgeapi.CheckUnknown,
	"startup_required": forgeapi.CheckUnknown,
}

// mergeStateStatuses is this product's merge-state enumeration, mapped totally over
// the eight members its schema declares, DRAFT included: that member carries a
// deprecation reason and is visible only to an introspection that asks for
// deprecated members explicitly, which is the member a gate reading the default view
// could not see.
//
// HAS_HOOKS is a MERGEABLE state rather than a block: it says the branch runs
// pre-receive hooks and the merge is otherwise clean, so reporting it as blocked
// would disable a control the forge would honour.
var mergeStateStatuses = map[string]forgeapi.MergeBlockReason{
	"clean":     forgeapi.MergeBlockNone,
	"has_hooks": forgeapi.MergeBlockNone,
	"dirty":     forgeapi.MergeBlockConflicts,
	"behind":    forgeapi.MergeBlockBehind,
	"blocked":   forgeapi.MergeBlockBlocked,
	"unstable":  forgeapi.MergeBlockChecksFailing,
	memberDraft: forgeapi.MergeBlockDraft,
	"unknown":   forgeapi.MergeBlockUnknown,
	"":          forgeapi.MergeBlockUnknown,
}

// mergeableStates is the three-member mergeability enumeration, mapped totally. It
// is a separate table from the merge state above because the two answer different
// questions and this product answers both at once: measured, one pull request
// reported MERGEABLE beside BLOCKED, which is why a boolean cannot carry
// mergeability.
var mergeableStates = map[string]forgeapi.Support{
	"mergeable":   forgeapi.SupportYes,
	"conflicting": forgeapi.SupportNo,
	"unknown":     forgeapi.SupportUnknown,
	"":            forgeapi.SupportUnknown,
}

// queueStates is this product's merge-queue entry state, mapped totally over the
// five members its schema declares. It is the one product whose queue verdict has
// five reachable members, which is why the queue state is its own enumeration rather
// than a three-valued answer.
var queueStates = map[string]forgeapi.QueueState{
	memberQueued:      forgeapi.QueueQueued,
	"awaiting_checks": forgeapi.QueueAwaitingChecks,
	"mergeable":       forgeapi.QueueMergeable,
	"unmergeable":     forgeapi.QueueUnmergeable,
	"locked":          forgeapi.QueueLocked,
	"":                forgeapi.QueueUnknown,
}

// prStates is this product's pull-request state enumeration, mapped totally on both
// transports: the document sends the screaming-snake member and REST sends the lower
// -snake one beside a separate merged boolean.
var prStates = map[string]forgeapi.PRState{
	"open":       forgeapi.PRStateOpen,
	"closed":     forgeapi.PRStateClosed,
	memberMerged: forgeapi.PRStateMerged,
	"":           forgeapi.PRStateUnknown,
}

// issueStates is this product's issue state enumeration, mapped totally.
var issueStates = map[string]forgeapi.IssueState{
	"open":   forgeapi.IssueStateOpen,
	"closed": forgeapi.IssueStateClosed,
	"":       forgeapi.IssueStateUnknown,
}

// mergeMethods is the closed set of merge strategies this product's own schema
// declares, in the spelling its merge route takes. A strategy outside it is refused
// LOCALLY before any request.
var mergeMethods = []string{memberMerge, memberSquash, "rebase"}

// unmapped is what every one of this family's totality tables does with a value it
// does not carry: count it by family and field, and log the value, which is what
// tells a maintainer the upstream enumeration grew rather than leaving an unknown
// with no cause. The value is the instance's own text, so it is bounded and
// rune-sanitized before it reaches a handler this library cannot see. The field names the TRANSPORT as well as the enumeration, because
// the two send the same members in two cases and which one grew is the first thing a
// maintainer needs.
func (c *Client) unmapped(field, value string) {
	if counters := c.core.Counters(); counters.UnknownEnumValue != nil {
		counters.UnknownEnumValue(forgeapi.FamilyGitHub, field)
	}
	c.core.Logger().Warn("forgeapi unmapped enumeration value",
		"family", forgeapi.FamilyGitHub.String(), "field", field, "value", transport.Sanitize(value))
}

// member folds an enumeration value onto the spelling the tables are keyed on, which
// is the lower-snake one both transports share.
func member(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) }

// checkState maps one status-context state onto the folded verdict, totally.
func (c *Client) checkState(field, raw string) forgeapi.CheckState {
	state, ok := statusStates[member(raw)]
	if ok {
		return state
	}
	c.unmapped(field, raw)
	return forgeapi.CheckUnknown
}

// runState maps one check RUN onto the folded verdict, from its conclusion where it
// reported one and from its status where it has not finished.
//
// The conclusion comes first because it is the only half that carries the neutral
// and skipped members at all, and a fold over the rollup's own state can therefore
// never answer neutral.
func (c *Client) runState(field, status, conclusion string) forgeapi.CheckState {
	if conclusion != "" {
		return c.countState(field+" conclusion", conclusion)
	}
	return c.countState(field+" status", status)
}

// countState maps one member of the union the per-state counts and the check runs
// both speak, totally.
func (c *Client) countState(field, raw string) forgeapi.CheckState {
	state, ok := checkRunStates[member(raw)]
	if ok {
		return state
	}
	c.unmapped(field, raw)
	return forgeapi.CheckUnknown
}

// blockReason maps the merge state onto the block reason, totally, on either
// transport's own spelling of the same member set.
func (c *Client) blockReason(field, raw string) forgeapi.MergeBlockReason {
	reason, ok := mergeStateStatuses[member(raw)]
	if ok {
		return reason
	}
	c.unmapped(field, raw)
	return forgeapi.MergeBlockUnknown
}

// mergeableSupport maps the mergeability enumeration, totally.
func (c *Client) mergeableSupport(field, raw string) forgeapi.Support {
	support, ok := mergeableStates[member(raw)]
	if ok {
		return support
	}
	c.unmapped(field, raw)
	return forgeapi.SupportUnknown
}

// queueState maps one merge-queue entry state, totally.
func (c *Client) queueState(field, raw string) forgeapi.QueueState {
	state, ok := queueStates[member(raw)]
	if ok {
		return state
	}
	c.unmapped(field, raw)
	return forgeapi.QueueUnknown
}

// pullState maps one pull request's state, totally. The merged member is the
// document's own and is derived from the merged boolean over REST, which is why the
// flag is a parameter here rather than a second table.
func (c *Client) pullState(field, raw string, merged bool) forgeapi.PRState {
	if merged {
		return forgeapi.PRStateMerged
	}
	state, ok := prStates[member(raw)]
	if ok {
		return state
	}
	c.unmapped(field, raw)
	return forgeapi.PRStateUnknown
}

// issueState maps one issue's state, totally, on either transport's own spelling of
// the same member set.
func (c *Client) issueState(field, raw string) forgeapi.IssueState {
	state, ok := issueStates[member(raw)]
	if ok {
		return state
	}
	c.unmapped(field, raw)
	return forgeapi.IssueStateUnknown
}

// supportOf turns a boolean this product did send into a three-valued answer.
func supportOf(b bool) forgeapi.Support {
	if b {
		return forgeapi.SupportYes
	}
	return forgeapi.SupportNo
}

// supportOfPointer turns a flag the record may not carry into a three-valued answer,
// which is why those fields are pointers: an absent flag is unknown rather than
// false.
func supportOfPointer(flag *bool) forgeapi.Support {
	if flag == nil {
		return forgeapi.SupportUnknown
	}
	return supportOf(*flag)
}

// sourceRepo is the repository a head branch lives in, from the selector the answer
// names: the addressed reference itself where both name one repository, whatever
// the case, and the zero reference where the answer names none (a deleted fork).
func sourceRepo(selector string, repo forgeapi.RepoRef) forgeapi.RepoRef {
	switch {
	case selector == "":
		return forgeapi.RepoRef{}
	case strings.EqualFold(selector, repo.Selector):
		return repo
	}
	return repoRef(selector)
}

// name is the selector a nullable repository selection answered, empty for null.
func (r *docRepoRef) name() string {
	if r == nil {
		return ""
	}
	return r.NameWithOwner
}

// repoRef derives the reference for one owner-and-repository selector. The
// identifier is derived from the selector rather than allocated, so it is the same
// value a consumer mints from a git remote with no round trip.
func repoRef(selector string) forgeapi.RepoRef {
	ref := forgeapi.RepoRef{Family: forgeapi.FamilyGitHub, Selector: selector, DisplayPath: selector}
	ref.ID = ref.Encode()
	return ref
}

// normalizeLabels is the label normalizer for the REST arm.
func normalizeLabels(in []restLabel) []forgeapi.Label {
	if len(in) == 0 {
		return nil
	}
	out := make([]forgeapi.Label, 0, len(in))
	for _, l := range in {
		out = append(out, forgeapi.Label{Name: l.Name, Color: l.Color, Description: l.Description})
	}
	return out
}

// normalizeDocLabels is the label normalizer for the document arm, whose labels are
// a CONNECTION rather than an array.
func normalizeDocLabels(in *docLabels) []forgeapi.Label {
	if in == nil || len(in.Nodes) == 0 {
		return nil
	}
	out := make([]forgeapi.Label, 0, len(in.Nodes))
	for _, l := range in.Nodes {
		out = append(out, forgeapi.Label{Name: l.Name, Color: l.Color, Description: l.Description})
	}
	return out
}

// fold is one folded check collection: the verdict, the per-member counts, the
// context rows held, where the collection continues, and whether the counts describe
// the WHOLE collection or only the rows held.
//
// exact is what lets a further page be joined honestly: counts taken from the
// connection's own per-state arrays describe the collection, so a second page must not
// add them again, where counts folded over nodes are the page's and must.
type fold struct { //nolint:govet // fieldalignment: the field order is the fold's own reading order, the rows and the cursor first and the counts it computed after, and one value of it is allocated per read
	contexts  []forgeapi.CheckContext
	cursor    string
	passing   int
	failing   int
	pending   int
	neutral   int
	unknown   int
	total     int
	state     forgeapi.CheckState
	exact     bool
	truncated bool
}

// verdict is the fold itself over the counts held. The worst state present wins,
// because one failing check makes the collection failing however many passed, and the
// UNKNOWN arm sits second, above pending, passing and neutral: a value this family's
// tables did not map is a check whose state was not read, and passing means every
// check reported succeeded.
func (f *fold) verdict() forgeapi.CheckState {
	switch {
	case f.failing > 0:
		return forgeapi.CheckFailing
	case f.unknown > 0:
		return forgeapi.CheckUnknown
	case f.pending > 0:
		return forgeapi.CheckPending
	case f.passing > 0:
		return forgeapi.CheckPassing
	case f.neutral > 0:
		return forgeapi.CheckNeutral
	}
	return forgeapi.CheckUnknown
}

// add counts one member into the fold.
func (f *fold) add(state forgeapi.CheckState, n int) {
	switch state {
	case forgeapi.CheckPassing:
		f.passing += n
	case forgeapi.CheckFailing:
		f.failing += n
	case forgeapi.CheckPending:
		f.pending += n
	case forgeapi.CheckNeutral:
		f.neutral += n
	case forgeapi.CheckUnknown:
		f.unknown += n
	}
	f.total += n
}

// foldRollup folds one rollup's contexts connection.
//
// The counts come from the connection's own per-state ARRAYS where it carries them,
// because those describe the WHOLE collection: measured byte-identical across three
// pages of a 446-context collection and summing to its total on every row of a list,
// which is what makes a bounded fold exact on this product and leaves the bound
// truncating the check NAMES rather than the verdict. Where the connection
// carries no counts the fold is over the nodes held, which is the whole collection
// exactly when the connection reports no next page.
//
// A null rollup is nothing to fold, and it is the one source on this product that
// tells a commit with no CI from a commit whose checks all passed: measured, the REST
// combined status answers the two identically.
func (c *Client) foldRollup(rollup *docRollup) fold {
	const field = "checks (document)"
	var out fold
	if rollup == nil || rollup.Contexts == nil {
		out.state = forgeapi.CheckUnknown
		return out
	}
	ctxs := rollup.Contexts
	for i := range ctxs.Nodes {
		out.contexts = append(out.contexts, c.normalizeDocContext(field, &ctxs.Nodes[i]))
	}
	if counted := c.countArrays(field, ctxs); counted.total > 0 {
		counted.contexts = out.contexts
		counted.exact = true
		out = counted
	} else {
		for i := range ctxs.Nodes {
			out.add(c.docContextState(field, &ctxs.Nodes[i]), 1)
		}
	}
	out.truncated = ctxs.PageInfo.HasNextPage
	out.cursor = ctxs.PageInfo.EndCursor
	out.state = out.verdict()
	return out
}

// countArrays is the fold taken from the connection's whole-collection count arrays,
// with a zero total where the connection carried none.
func (c *Client) countArrays(field string, ctxs *docContexts) fold {
	var out fold
	for _, row := range ctxs.CheckRunCountsByState {
		out.add(c.countState(field+" check-run count", row.State), row.Count)
	}
	for _, row := range ctxs.StatusContextCountsByState {
		out.add(c.checkState(field+" status count", row.State), row.Count)
	}
	return out
}

// docContextState is one union node's folded state, read from the half its own
// discriminator names.
func (c *Client) docContextState(field string, node *docContext) forgeapi.CheckState {
	if isCheckRun(node) {
		return c.runState(field+" check run", node.Status, node.Conclusion)
	}
	return c.checkState(field+" status context", node.State)
}

// normalizeDocContext is one union node as a published context row. The two halves
// spell the name and the link differently, which is what the discriminator decides.
func (c *Client) normalizeDocContext(field string, node *docContext) forgeapi.CheckContext {
	if isCheckRun(node) {
		return forgeapi.CheckContext{
			Name:      node.Name,
			TargetURL: node.DetailsURL,
			State:     c.runState(field+" check run", node.Status, node.Conclusion),
		}
	}
	return forgeapi.CheckContext{
		Name:        node.Context,
		Description: node.Description,
		TargetURL:   node.TargetURL,
		State:       c.checkState(field+" status context", node.State),
	}
}

// isCheckRun reports which half of the union one node is. The discriminator is the
// selected type name, and a node that names neither type is read as a status context,
// which is the half whose keys a nodeless capture carries.
func isCheckRun(node *docContext) bool { return node.Typename == "CheckRun" }

// normalizeDocPull is the pull-request normalizer for the document arm, which is the
// only arm that carries mergeability, the merge state, the queue entry and a folded
// verdict at all.
func (c *Client) normalizeDocPull(n *docPullRequest, repo forgeapi.RepoRef) forgeapi.PullRequest {
	author := ""
	if n.Author != nil {
		author = n.Author.Login
	}
	checks := c.foldRollup(rollupOf(n))
	action := forgeapi.ActionState{
		Mergeable:      c.mergeableSupport("mergeable (document)", n.Mergeable),
		Checks:         checks.state,
		ChecksPassing:  checks.passing,
		ChecksFailing:  checks.failing,
		ChecksPending:  checks.pending,
		ChecksNeutral:  checks.neutral,
		ChecksUnknown:  checks.unknown,
		ChecksTotal:    checks.total,
		AutoMergeArmed: supportOf(n.AutoMergeRequest != nil),
		QueueState:     forgeapi.QueueNone,
		QueuePosition:  forgeapi.QueuePositionUnknown,
		MergeBlocked:   c.blockReason("merge state status (document)", n.MergeStateStatus),
	}
	if n.MergeQueueEntry != nil {
		action.QueueState = c.queueState("merge queue entry state (document)", n.MergeQueueEntry.State)
		if n.MergeQueueEntry.Position != nil {
			action.QueuePosition = *n.MergeQueueEntry.Position
		}
	}
	out := forgeapi.PullRequest{
		Ref:          forgeapi.PRRef{Number: n.Number, Sigil: sigil},
		Repo:         repo,
		Title:        n.Title,
		Body:         n.Body,
		Author:       author,
		SourceBranch: n.HeadRefName,
		SourceRepo:   sourceRepo(n.HeadRepository.name(), repo),
		TargetBranch: n.BaseRefName,
		WebURL:       n.URL,
		HeadSHA:      n.HeadRefOID,
		Labels:       normalizeDocLabels(n.Labels),
		CreatedAt:    n.CreatedAt,
		UpdatedAt:    n.UpdatedAt,
		State:        c.pullState("pull request state (document)", n.State, false),
		Draft:        n.IsDraft,
		Action:       action,
	}
	return out
}

// rollupOf is the status rollup of a pull request's head commit, which the documents
// select as the last commit of the pull request's own commit connection.
func rollupOf(n *docPullRequest) *docRollup {
	if n.Commits == nil || len(n.Commits.Nodes) == 0 {
		return nil
	}
	commit := n.Commits.Nodes[len(n.Commits.Nodes)-1].Commit
	if commit == nil {
		return nil
	}
	return commit.StatusCheckRollup
}

// normalizeRESTPull is the pull-request normalizer for the REST arm, which serves the
// mutations, through [Client.normalizeMutatedPull], and the DEGRADED read a connection
// whose document was refused falls back to.
//
// Three action fields are what this arm cannot answer rather than what it answers
// falsely. There is no check state and no count among the keys this route returns, so
// the verdict is unknown with zero counts; and there is no merge-queue key of any
// kind, so the queue verdict is the neutral member and the position unknown. What it
// does carry is the merge-state field under this product's OWN REST spelling, whose
// member set is the document enumeration's lower-snake form.
func (c *Client) normalizeRESTPull(r *restPull, repo forgeapi.RepoRef) forgeapi.PullRequest {
	author := ""
	if r.User != nil {
		author = r.User.Login
	}
	head, base := "", ""
	sha, source := "", ""
	if r.Head != nil {
		head, sha = r.Head.Ref, r.Head.SHA
		if r.Head.Repo != nil {
			source = r.Head.Repo.FullName
		}
	}
	if r.Base != nil {
		base = r.Base.Ref
	}
	return forgeapi.PullRequest{
		Ref:          forgeapi.PRRef{Number: r.Number, Sigil: sigil},
		Repo:         repo,
		Title:        r.Title,
		Body:         r.Body,
		Author:       author,
		SourceBranch: head,
		SourceRepo:   sourceRepo(source, repo),
		TargetBranch: base,
		WebURL:       r.WebURL,
		HeadSHA:      sha,
		Labels:       normalizeLabels(r.Labels),
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		State:        c.pullState("pull request state (rest)", r.State, r.Merged),
		Draft:        r.Draft,
		Action: forgeapi.ActionState{
			Mergeable:      supportOfPointer(r.Mergeable),
			Checks:         forgeapi.CheckUnknown,
			AutoMergeArmed: supportOf(r.AutoMerge != nil),
			QueueState:     forgeapi.QueueNone,
			QueuePosition:  forgeapi.QueuePositionUnknown,
			MergeBlocked:   c.blockReason("mergeable state (rest)", r.MergeableState),
		},
	}
}

// normalizeMutatedPull is the pull request a mutation's own answer carries. Its
// mergeable flag and merge state are the asynchronous check's at the instant of the
// transition, null and unknown before the check settles and a settled member after
// it, so no caller can rely on either and both answer unknown whatever they name.
// The merge state is still read, so a member the table does not carry is counted.
func (c *Client) normalizeMutatedPull(r *restPull, repo forgeapi.RepoRef) forgeapi.PullRequest {
	item := c.normalizeRESTPull(r, repo)
	item.Action.Mergeable = forgeapi.SupportUnknown
	item.Action.MergeBlocked = forgeapi.MergeBlockUnknown
	return item
}

// normalizeIssue is the issue normalizer.
func (c *Client) normalizeIssue(i *restIssue, repo forgeapi.RepoRef) forgeapi.Issue {
	author := ""
	if i.User != nil {
		author = i.User.Login
	}
	return forgeapi.Issue{
		Ref:       forgeapi.IssueRef{Number: i.Number},
		Repo:      repo,
		Title:     i.Title,
		Body:      i.Body,
		Author:    author,
		WebURL:    i.WebURL,
		Labels:    normalizeLabels(i.Labels),
		CreatedAt: i.CreatedAt,
		UpdatedAt: i.UpdatedAt,
		State:     c.issueState("issue state (rest)", i.State),
	}
}

// normalizeDocIssue is the issue normalizer for the document arm, whose row carries
// its own repository because the call that read it addressed none.
func (c *Client) normalizeDocIssue(n *docIssue) forgeapi.Issue {
	author := ""
	if n.Author != nil {
		author = n.Author.Login
	}
	return forgeapi.Issue{
		Ref:       forgeapi.IssueRef{Number: n.Number},
		Repo:      rowRepo(n.Repository),
		Title:     n.Title,
		Body:      n.Body,
		Author:    author,
		WebURL:    n.URL,
		Labels:    normalizeDocLabels(n.Labels),
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
		State:     c.issueState("issue state (document)", n.State),
	}
}

// normalizeRun is the workflow-run normalizer. The run's status and conclusion are
// this product's check-run vocabularies, which is what the runs route's own status
// filter documents, so they fold through the table the check runs fold through and
// a completed run's verdict is its conclusion's.
func (c *Client) normalizeRun(r *restWorkflowRun, repo forgeapi.RepoRef) forgeapi.Run {
	return forgeapi.Run{
		Repo:      repo,
		Name:      r.Name,
		Branch:    r.HeadBranch,
		HeadSHA:   r.HeadSHA,
		WebURL:    r.WebURL,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
		State:     c.runState("workflow run (rest)", r.Status, r.Conclusion),
	}
}

// normalizeRelease is the release normalizer. The published instant is a POINTER on
// the wire and null on a draft, which is a release with no publication rather than
// one published at the zero time.
func normalizeRelease(r *restRelease) forgeapi.Release {
	out := forgeapi.Release{
		TagName:    r.TagName,
		Name:       r.Name,
		Body:       r.Body,
		WebURL:     r.WebURL,
		Draft:      r.Draft,
		Prerelease: r.Prerelease,
	}
	if r.PublishedAt != nil {
		out.PublishedAt = *r.PublishedAt
	}
	return out
}

// normalizeRepo is the repository normalizer.
func (c *Client) normalizeRepo(r *restRepo) forgeapi.Repository {
	return forgeapi.Repository{
		Ref:         repoRef(selectorOf(r)),
		Description: r.Description,
		WebURL:      r.WebURL,
		CloneURL:    r.CloneURL,
		UpdatedAt:   r.UpdatedAt,
		Affordances: normalizeAffordances(r),
		Private:     r.Private,
		Archived:    r.Archived,
		Fork:        r.Fork,
	}
}

// selectorOf is one repository record's canonical selector, its own full name where
// it carries one and the owner and name pair otherwise, because that pair is what
// every route of this product takes.
func selectorOf(r *restRepo) string {
	if r.FullName != "" {
		return r.FullName
	}
	if r.Owner == nil {
		return r.Name
	}
	return r.Owner.Login + "/" + r.Name
}

// normalizeAffordances reads one repository's affordances off its own record.
//
// The merge strategies are the record's own flags, so a LISTING row answers an empty
// set: measured, this product's minimal repository representation omits all three,
// which is why they are pointers here and why the evidence for them says default
// rather than response body. The merge TRAIN has no key on either route, because a
// merge queue is a ruleset property on this product and the only read that answers it
// costs one request per ruleset the budget prices nowhere, so it is unknown with
// default evidence.
func normalizeAffordances(r *restRepo) forgeapi.RepoAffordances {
	strategies := exposedStrategies(r)
	ev := map[forgeapi.Capability]forgeapi.Evidence{
		forgeapi.CapHasIssues:  affordanceEvidence(r.HasIssues != nil, "has_issues"),
		forgeapi.CapCanPush:    affordanceEvidence(r.Permissions != nil, "permissions.push"),
		forgeapi.CapMergeTrain: {Source: forgeapi.EvidenceDefault, Detail: "a merge queue is a ruleset property on this product, so no repository record carries a key for it on either route"},
	}
	return forgeapi.RepoAffordances{
		MergeStrategies: strategies,
		HasIssues:       supportOfPointer(r.HasIssues),
		CanPush:         pushSupport(r.Permissions),
		MergeTrain:      forgeapi.SupportUnknown,
		Ev:              ev,
		DefaultBranch:   r.DefaultBranch,
	}
}

// affordanceEvidence names where one affordance was read, which is the record's own
// body where the record carried the key and the default where it did not: this
// product's listing row omits keys its full record carries, so the source is per
// field rather than per call.
func affordanceEvidence(present bool, key string) forgeapi.Evidence {
	if present {
		return forgeapi.Evidence{Source: forgeapi.EvidenceResponseBody, Detail: "the repository record's own " + key}
	}
	return forgeapi.Evidence{Source: forgeapi.EvidenceDefault, Detail: "the repository record this call read carries no " + key + " key"}
}

// exposedStrategies is the merge strategies this repository's record exposes, in the
// spelling this product's own merge route takes, so a caller can hand one back as
// [forgeapi.MergeRequest.Strategy].
func exposedStrategies(r *restRepo) []string {
	var out []string
	for _, pair := range []struct {
		flag *bool
		name string
	}{
		{r.AllowMergeCommit, "merge"},
		{r.AllowSquashMerge, "squash"},
		{r.AllowRebaseMerge, "rebase"},
	} {
		if pair.flag != nil && *pair.flag {
			out = append(out, pair.name)
		}
	}
	return out
}

// pushSupport reads the push affordance off the permission object, unknown where the
// record carries none rather than a confident no: an anonymous read and a read by a
// credential with no permission are not the same answer.
func pushSupport(p *restPermissions) forgeapi.Support {
	if p == nil {
		return forgeapi.SupportUnknown
	}
	return supportOf(p.Push)
}

// signal reads this product's budget signal, which rides EVERY response on both
// surfaces: the remaining units for the credential's own quota and the instant that
// window renews, as epoch seconds.
//
// It is read from the response just received and NEVER from the rate-limit endpoint,
// which is measured to report a full pool with a fresh window while the same
// credential's document budget was demonstrably drawn down. The two
// surfaces draw on separate windows, so what is reported is the window the last call
// drew on, which is the one the header names.
func signal(header http.Header) (remaining int, reset time.Time, ok bool) {
	raw := header.Get("X-RateLimit-Remaining")
	if raw == "" {
		return 0, time.Time{}, false
	}
	remaining, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, time.Time{}, false
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Reset")), 10, 64)
	if err != nil {
		return remaining, time.Time{}, true
	}
	return remaining, time.Unix(seconds, 0).UTC(), true
}

// mapper is this connection's error mapping: the mapping below, over the one fact
// about the connection that a response cannot carry, which is whether the request
// that was refused carried the version pin. No cell reads the body's error member:
// this product's refusals carry message text only.
func (c *Client) mapper(op string, status int, header http.Header, _ string) (kind forgeapi.ErrorKind, code string) {
	return mapping(op, status, header, c.sendsPin())
}

// mapping is this family's error mapping, by operation plus status plus the headers
// that decide a cell, which the reading below takes first.
//
// Cells turn on the OPERATION where one route answers a status for causes of its own.
// The merge's 400 names no cause: a draft, a closed pull request and a head pin naming
// another commit answer one body structure with different human text, measured. Its
// 409 reaches this mapping only where the body names no merge in flight, since one
// that does is the outcome the merge reads for itself.
//
// One cell turns on the connection instead: pinned says the refused request carried
// the version pin, which is the only state in which a 400 can be that version's fault.
//
// An unmapped combination stays upstream and is never guessed.
func mapping(op string, status int, header http.Header, pinned bool) (kind forgeapi.ErrorKind, code string) {
	if mapped, ok := headerMapped(status, header, pinned); ok {
		return mapped.kind, mapped.code
	}
	switch status {
	case http.StatusBadRequest:
		if op == opMergePR {
			return forgeapi.KindNotMergeable, forgeapi.CodeNotMergeable
		}
		return forgeapi.KindUpstream, ""
	case http.StatusUnauthorized:
		return forgeapi.KindUnauthorized, ""
	case http.StatusForbidden:
		if op == opMergePR {
			return forgeapi.KindForbidden, forgeapi.CodeNoMergePermission
		}
		return forgeapi.KindForbidden, ""
	case http.StatusNotFound:
		return forgeapi.KindNotFound, forgeapi.CodeRepoOrPRNotVisible
	case http.StatusMethodNotAllowed:
		return forgeapi.KindNotMergeable, forgeapi.CodeNotMergeable
	case http.StatusConflict:
		return forgeapi.KindConflict, ""
	case http.StatusGone:
		if op == opListIssues {
			return forgeapi.KindNotFound, forgeapi.CodeCapabilityUnsupported
		}
		return forgeapi.KindNotFound, ""
	case http.StatusUnprocessableEntity:
		if op == opMergePR {
			return forgeapi.KindNotMergeable, forgeapi.CodeBranchCannotMerge
		}
		return forgeapi.KindUpstream, forgeapi.CodeValidation
	case http.StatusTooManyRequests:
		return forgeapi.KindRateLimited, ""
	}
	return forgeapi.KindUpstream, ""
}

// verdict is one mapped answer, which is a pair rather than two returns because the two
// readings below hand it back through one predicate.
type verdict struct {
	code string
	kind forgeapi.ErrorKind
}

// headerMapped is the part of the mapping a HEADER decides rather than the status, which
// is two cells on this product and is why the mapper is given the headers at all.
//
// A 403 whose remaining-budget header reads zero is a THROTTLE rather than a permission,
// which is how this product reports both its primary and its secondary limits, so
// reading that status as forbidden alone would tell a user to fix their token while the
// quota refills. And a 400 answering a request that CARRIED the version pin and did not
// answer under it is that version being outside the instance's reach rather than a bad
// request, which is the one status a dated pin can produce.
//
// The pin is what makes the second cell readable at all. A request that carried no pin
// is answered under the instance's own default, so the echo names a version other than
// the pinned one on every response including an ordinary refusal, and a mapping that
// read the echo there would report every 400 on such a connection as the pin's own.
func headerMapped(status int, header http.Header, pinned bool) (verdict, bool) {
	switch {
	case status == http.StatusForbidden && throttled(header):
		return verdict{kind: forgeapi.KindRateLimited}, true
	case status == http.StatusBadRequest && pinned && retiredVersion(header):
		return verdict{kind: forgeapi.KindUpstream, code: forgeapi.CodeAPIVersionRetired}, true
	}
	return verdict{}, false
}

// throttled reports whether a refusal is this product's quota rather than a
// permission, which the remaining-budget header is what says: measured, both the
// primary and the secondary limit answer 403 with that header at zero, where a
// permission refusal leaves it above zero.
func throttled(header http.Header) bool {
	remaining, _, ok := signal(header)
	return ok && remaining <= 0
}

// retiredVersion reports whether a response did NOT answer under the version this
// family pins, which is what a version outside an instance's reach looks like on the
// wire on a request that carried the pin.
//
// Both readings are measured. An instance that serves the pin names it in the echo on
// every response including a refusal, a 404 and a 422 among them, so a refusal that
// carries the echo is an ordinary one. A version the instance does not serve is
// refused BEFORE one is selected, so that refusal carries no echo at all; and an
// instance that answered under a different version names that one. The absent echo
// and the other name are therefore the same fact, which is why one comparison reads
// both, and the caller is what holds this reading to the requests that carried a pin.
func retiredVersion(header http.Header) bool {
	return strings.TrimSpace(header.Get(headerVersionSelected)) != APIVersion
}
