package gitlab

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// The operation names this family spells, which are the role methods' own. They are
// constants because each of them decides an error-mapping cell or a list refusal, so
// a typo there would silently change a refusal.
const (
	opMergePR      = "MergePR"
	opListIssues   = "ListIssues"
	opListMyPRs    = "ListMyPRs"
	opListMyIssues = "ListMyIssues"
)

// developerAccess is the project role integer at which a credential may push, which
// is what the push affordance reports. This product answers a role INTEGER rather
// than a boolean, so no permission and no visibility are not the same answer.
const developerAccess = 30

// The GraphQL wire shapes. They are declared SEPARATELY from the REST shapes below
// and no struct serves both, which is a measured rule rather than a stylistic one:
// a merge request's own number is an integer over REST and a digit-only STRING over
// GraphQL, its labels are a connection here and an array of names there, and its
// enumeration members are screaming snake here and lower snake there. One shared
// struct across the two transports decodes neither.
type (
	docPermissions struct {
		PushCode         bool `json:"pushCode"`
		ReadMergeRequest bool `json:"readMergeRequest"`
	}

	docLabel struct {
		Title       string `json:"title"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}

	docPageInfo struct {
		EndCursor   string `json:"endCursor"`
		HasNextPage bool   `json:"hasNextPage"`
	}

	docLabels struct {
		PageInfo docPageInfo `json:"pageInfo"`
		Nodes    []docLabel  `json:"nodes"`
		Count    int         `json:"count"`
	}

	docPipeline struct {
		Status string `json:"status"`
	}

	docMergeRequest struct { //nolint:govet // fieldalignment: the field order is the document's own selection order, so a reader compares this type against the response it decodes
		IID                 string       `json:"iid"`
		Title               string       `json:"title"`
		Description         string       `json:"description"`
		Author              *docAuthor   `json:"author"`
		SourceBranch        string       `json:"sourceBranch"`
		SourceProject       *docProject  `json:"sourceProject"`
		TargetBranch        string       `json:"targetBranch"`
		WebURL              string       `json:"webUrl"`
		DiffHeadSHA         string       `json:"diffHeadSha"`
		CreatedAt           time.Time    `json:"createdAt"`
		UpdatedAt           time.Time    `json:"updatedAt"`
		State               string       `json:"state"`
		Draft               bool         `json:"draft"`
		MergedAt            *time.Time   `json:"mergedAt"`
		Mergeable           *bool        `json:"mergeable"`
		DetailedMergeStatus string       `json:"detailedMergeStatus"`
		AutoMergeEnabled    *bool        `json:"autoMergeEnabled"`
		Labels              *docLabels   `json:"labels"`
		HeadPipeline        *docPipeline `json:"headPipeline"`
	}

	docAuthor struct {
		Username string `json:"username"`
	}

	docConnection struct {
		PageInfo docPageInfo       `json:"pageInfo"`
		Nodes    []docMergeRequest `json:"nodes"`
	}

	docProject struct { //nolint:govet // fieldalignment: the field order is the document's own selection order, so a reader compares this type against the query it decodes
		FullPath        string           `json:"fullPath"`
		UserPermissions *docPermissions  `json:"userPermissions"`
		MergeRequests   *docConnection   `json:"mergeRequests"`
		MergeRequest    *docMergeRequest `json:"mergeRequest"`
	}

	docComplexity struct {
		Score int `json:"score"`
	}

	// docPayload is the decoded `data` of either document. One type serves both
	// because the two documents differ in WHICH selection they populate rather
	// than in the vocabulary they speak: a list fills mergeRequests and a read
	// fills mergeRequest, and a caller of one reads nothing of the other's. The
	// rule that forbids a shared struct is about the two TRANSPORTS, where the
	// same field arrives in two types.
	docPayload struct {
		Project         *docProject    `json:"project"`
		QueryComplexity *docComplexity `json:"queryComplexity"`
	}
)

// complexity is what the endpoint charged for the document that filled this payload,
// which is the figure this product bills a call in and therefore the one the governor
// publishes as the last call's cost. Zero where the payload carries none.
func (p *docPayload) complexity() int {
	if p.QueryComplexity == nil {
		return 0
	}
	return p.QueryComplexity.Score
}

// The REST wire shapes.
type (
	restUser struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		Email    string `json:"email"`
		WebURL   string `json:"web_url"`
	}

	restAccess struct {
		AccessLevel int `json:"access_level"`
	}

	restPermissions struct {
		ProjectAccess *restAccess `json:"project_access"`
		GroupAccess   *restAccess `json:"group_access"`
	}

	restProject struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		PathWithNamespace string           `json:"path_with_namespace"`
		Description       string           `json:"description"`
		WebURL            string           `json:"web_url"`
		CloneURL          string           `json:"http_url_to_repo"`
		LastActivityAt    time.Time        `json:"last_activity_at"`
		Visibility        string           `json:"visibility"`
		DefaultBranch     string           `json:"default_branch"`
		IssuesEnabled     *bool            `json:"issues_enabled"`
		IssuesAccessLevel string           `json:"issues_access_level"`
		Permissions       *restPermissions `json:"permissions"`
		MergeMethod       string           `json:"merge_method"`
		SquashOption      string           `json:"squash_option"`
		MergeTrains       *bool            `json:"merge_trains_enabled"`
		Archived          bool             `json:"archived"`
		ForkedFrom        *restProjectRef  `json:"forked_from_project"`
	}

	restProjectRef struct{}

	restLabel struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}

	restReferences struct {
		Full string `json:"full"`
	}

	restPipeline struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		ID        int64     `json:"id"`
		SHA       string    `json:"sha"`
		Ref       string    `json:"ref"`
		Status    string    `json:"status"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		WebURL    string    `json:"web_url"`
		Name      string    `json:"name"`
	}

	restMergeRequest struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		IID         int       `json:"iid"`
		State       string    `json:"state"`
		Title       string    `json:"title"`
		Description string    `json:"description"`
		Author      *restUser `json:"author"`
		WebURL      string    `json:"web_url"`
		CreatedAt   time.Time `json:"created_at"`
		UpdatedAt   time.Time `json:"updated_at"`
		Draft       bool      `json:"draft"`
		// Labels is DEFERRED rather than typed, because this product answers the
		// same key in two shapes: an array of NAMES on a mutation's own answer, and
		// an array of label OBJECTS on a read that asked for the details, which is
		// what every list here asks for. Measured against the recorded capture of
		// this route: the issues read answers objects under this key and declares no
		// separate detail key at all, so a wire type stating either shape decodes
		// nothing on the other.
		Labels       json.RawMessage `json:"labels"`
		SourceBranch string          `json:"source_branch"`
		TargetBranch string          `json:"target_branch"`
		// SourceProjectID and TargetProjectID name the head's project and the
		// addressed one by numeric id alone; the source is null once a fork is
		// deleted.
		SourceProjectID     *int64          `json:"source_project_id"`
		TargetProjectID     *int64          `json:"target_project_id"`
		SHA                 string          `json:"sha"`
		MergeStatus         string          `json:"merge_status"`
		DetailedMergeStatus string          `json:"detailed_merge_status"`
		MergedAt            *time.Time      `json:"merged_at"`
		References          *restReferences `json:"references"`
		HeadPipeline        *restPipeline   `json:"head_pipeline"`
		// MergeWhenPipelineSucceeds is this product's REST answer for an armed
		// auto-merge, on a list row, a mutation's answer and the merge's answer
		// alike. The ANSWER keeps the name the merge's request parameter
		// deprecated, and the document's autoMergeEnabled has no REST twin, so
		// this is the key a REST record states it under.
		MergeWhenPipelineSucceeds *bool `json:"merge_when_pipeline_succeeds"`
	}

	restIssue struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		IID         int             `json:"iid"`
		State       string          `json:"state"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
		Author      *restUser       `json:"author"`
		WebURL      string          `json:"web_url"`
		CreatedAt   time.Time       `json:"created_at"`
		UpdatedAt   time.Time       `json:"updated_at"`
		Labels      json.RawMessage `json:"labels"`
		References  *restReferences `json:"references"`
	}

	restLinks struct {
		Self string `json:"self"`
	}

	restRelease struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		TagName     string     `json:"tag_name"`
		Name        string     `json:"name"`
		Description string     `json:"description"`
		ReleasedAt  time.Time  `json:"released_at"`
		Links       *restLinks `json:"_links"`
	}

	restStatus struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
		Status      string `json:"status"`
		SHA         string `json:"sha"`
		// ID is the discriminator the de-duplication keeps the latest row by. This
		// route answers the HISTORY of a context rather than its current state, and
		// this identifier is monotonic per instance and present on every row, where
		// the finish time is null on a row that is still running.
		ID int64 `json:"id"`
	}

	restMetadata struct {
		Version string `json:"version"`
	}
)

// pipelineStates is this product's pipeline and job status enumeration, mapped
// TOTALLY over every member its schema declares. Both transports send the same
// member set in two cases, so the table is keyed on the lower-snake spelling and the
// document's screaming-snake value is folded to it before the lookup; a member added
// upstream in either case therefore misses the table and is counted and named rather
// than folded into a verdict.
//
// Measured by introspection on gitlab.com, 2026-09-20: PipelineStatusEnum and
// CiJobStatus declare the same thirteen members and none is deprecated.
var pipelineStates = map[string]forgeapi.CheckState{
	"success":              forgeapi.CheckPassing,
	"failed":               forgeapi.CheckFailing,
	"created":              forgeapi.CheckPending,
	"waiting_for_resource": forgeapi.CheckPending,
	"preparing":            forgeapi.CheckPending,
	"waiting_for_callback": forgeapi.CheckPending,
	"pending":              forgeapi.CheckPending,
	"running":              forgeapi.CheckPending,
	"canceling":            forgeapi.CheckPending,
	"scheduled":            forgeapi.CheckPending,
	"canceled":             forgeapi.CheckNeutral,
	"skipped":              forgeapi.CheckNeutral,
	"manual":               forgeapi.CheckNeutral,
	"":                     forgeapi.CheckUnknown,
}

// detailedMergeStatuses is this product's detailed merge status, mapped totally over
// the twenty-four members its schema declares, plus the empty string an instance too
// old to send the field leaves. It feeds the block reason on the documents; a REST
// answer names none, and is looked up only so a member the table lacks is counted.
//
// Four of the members are the status still being COMPUTED rather than a cause, so
// they answer the unknown member: a reason invented from "not checked yet" is the
// wrong green this discipline exists to catch.
var detailedMergeStatuses = map[string]forgeapi.MergeBlockReason{
	"mergeable":                      forgeapi.MergeBlockNone,
	"unchecked":                      forgeapi.MergeBlockUnknown,
	"checking":                       forgeapi.MergeBlockUnknown,
	"preparing":                      forgeapi.MergeBlockUnknown,
	"approvals_syncing":              forgeapi.MergeBlockUnknown,
	"draft_status":                   forgeapi.MergeBlockDraft,
	"conflict":                       forgeapi.MergeBlockConflicts,
	"need_rebase":                    forgeapi.MergeBlockBehind,
	"ci_must_pass":                   forgeapi.MergeBlockChecksFailing,
	"external_status_checks":         forgeapi.MergeBlockChecksFailing,
	"ci_still_running":               forgeapi.MergeBlockChecksRunning,
	"security_policy_pipeline_check": forgeapi.MergeBlockChecksRunning,
	"commits_status":                 forgeapi.MergeBlockBlocked,
	"discussions_not_resolved":       forgeapi.MergeBlockBlocked,
	"not_open":                       forgeapi.MergeBlockBlocked,
	"not_approved":                   forgeapi.MergeBlockBlocked,
	"blocked_status":                 forgeapi.MergeBlockBlocked,
	"jira_association":               forgeapi.MergeBlockBlocked,
	"locked_paths":                   forgeapi.MergeBlockBlocked,
	"locked_lfs_files":               forgeapi.MergeBlockBlocked,
	"merge_time":                     forgeapi.MergeBlockBlocked,
	"security_policies_violations":   forgeapi.MergeBlockBlocked,
	"title_not_matching":             forgeapi.MergeBlockBlocked,
	"requested_changes":              forgeapi.MergeBlockBlocked,
	"":                               forgeapi.MergeBlockUnknown,
}

// mergeStatuses is the OLDER merge status, read over REST alone and reached only
// where the detailed field is absent, mapped over the five members its own schema
// declares. A REST answer names no block reason, so the reason it maps to reaches no
// field; it stays total so a member outside it is counted.
//
// It is never selected in a document, because its GraphQL twin carries a deprecation
// reason (measured 2026-09-20: MergeRequest.mergeStatus is flagged "Renamed. Use
// MergeRequest.mergeStatusEnum instead. Deprecated in GitLab 14.0") and the schema
// gate fails on one.
var mergeStatuses = map[string]forgeapi.MergeBlockReason{
	"can_be_merged":            forgeapi.MergeBlockNone,
	"cannot_be_merged":         forgeapi.MergeBlockConflicts,
	"unchecked":                forgeapi.MergeBlockUnknown,
	"checking":                 forgeapi.MergeBlockUnknown,
	"cannot_be_merged_recheck": forgeapi.MergeBlockUnknown,
	"":                         forgeapi.MergeBlockUnknown,
}

// prStates is this product's merge-request state enumeration, mapped totally over
// every member its schema declares. Two of them need their reading stated: `locked`
// is an open merge request the instance has locked while it merges, so it is open
// rather than a fourth published state; and `all` is a FILTER member that can never
// be one row's state, so it answers the unknown member rather than being left out of
// a table this discipline calls total.
var prStates = map[string]forgeapi.PRState{
	stateOpened: forgeapi.PRStateOpen,
	"locked":    forgeapi.PRStateOpen,
	stateClosed: forgeapi.PRStateClosed,
	stateMerged: forgeapi.PRStateMerged,
	stateAll:    forgeapi.PRStateUnknown,
	"":          forgeapi.PRStateUnknown,
}

// issueStates is this product's issue state enumeration, mapped totally under the
// same rule.
var issueStates = map[string]forgeapi.IssueState{
	stateOpened: forgeapi.IssueStateOpen,
	"locked":    forgeapi.IssueStateOpen,
	stateClosed: forgeapi.IssueStateClosed,
	stateAll:    forgeapi.IssueStateUnknown,
	"":          forgeapi.IssueStateUnknown,
}

// supportOf turns a boolean this product did send into a three-valued answer.
func supportOf(b bool) forgeapi.Support {
	if b {
		return forgeapi.SupportYes
	}
	return forgeapi.SupportNo
}

// supportOfPointer turns a flag the record may not carry into a three-valued answer,
// which is why these fields are pointers: an absent flag is unknown rather than
// false.
func supportOfPointer(flag *bool) forgeapi.Support {
	if flag == nil {
		return forgeapi.SupportUnknown
	}
	return supportOf(*flag)
}

// repoRef derives the reference for one project path. The identifier is derived from
// the selector rather than allocated, so it is the same value a consumer mints from a
// git remote with no round trip, and the selector is stored UNENCODED because it is
// what the derivation is computed over.
func repoRef(path string) forgeapi.RepoRef {
	ref := forgeapi.RepoRef{Family: forgeapi.FamilyGitLab, Selector: path, DisplayPath: path}
	ref.ID = ref.Encode()
	return ref
}

// unmapped is what every one of this family's totality tables does with a value it
// does not carry: count it by family and field, and log the value, which is what
// tells a maintainer the upstream enumeration grew rather than leaving an unknown
// with no cause. The value is the instance's own text, so it is bounded and
// rune-sanitized before it reaches a handler this library cannot see. The field names the TRANSPORT as well as the enumeration, because
// the two send the same members in two cases and which one grew is the first thing a
// maintainer needs.
func (c *Client) unmapped(field, value string) {
	if counters := c.core.Counters(); counters.UnknownEnumValue != nil {
		counters.UnknownEnumValue(forgeapi.FamilyGitLab, field)
	}
	c.core.Logger().Warn("forgeapi unmapped enumeration value",
		"family", forgeapi.FamilyGitLab.String(), "field", field, "value", transport.Sanitize(value))
}

// checkState maps one pipeline or job status onto the folded verdict, totally.
func (c *Client) checkState(field, raw string) forgeapi.CheckState {
	state, ok := pipelineStates[strings.ToLower(raw)]
	if ok {
		return state
	}
	c.unmapped(field, raw)
	return forgeapi.CheckUnknown
}

// blockReason maps the detailed merge status onto the block reason, falling back to
// the older status where the detailed field is absent, which is the one field of this
// product read over REST alone.
func (c *Client) blockReason(field, detailed, older string) forgeapi.MergeBlockReason {
	if detailed != "" {
		reason, ok := detailedMergeStatuses[strings.ToLower(detailed)]
		if ok {
			return reason
		}
		c.unmapped(field, detailed)
		return forgeapi.MergeBlockUnknown
	}
	if older == "" {
		return forgeapi.MergeBlockUnknown
	}
	reason, ok := mergeStatuses[strings.ToLower(older)]
	if ok {
		return reason
	}
	c.unmapped("merge status (rest)", older)
	return forgeapi.MergeBlockUnknown
}

// pullState maps one merge request's state, totally.
func (c *Client) pullState(field, raw string) forgeapi.PRState {
	state, ok := prStates[strings.ToLower(raw)]
	if ok {
		return state
	}
	c.unmapped(field, raw)
	return forgeapi.PRStateUnknown
}

// issueState maps one issue's state, totally.
func (c *Client) issueState(raw string) forgeapi.IssueState {
	state, ok := issueStates[strings.ToLower(raw)]
	if ok {
		return state
	}
	c.unmapped("issue state (rest)", raw)
	return forgeapi.IssueStateUnknown
}

// mergedSupport is this product's answer to "is it merged", derived from the PAIR the
// documents and the REST records both carry, because neither has a merged boolean: a
// merged timestamp or the merged state member is yes, a state member that mapped to
// something else is no, and a state nothing mapped is unknown rather than a wrong no.
func (c *Client) mergedSupport(field, state string, mergedAt *time.Time) forgeapi.Support {
	if mergedAt != nil && !mergedAt.IsZero() {
		return forgeapi.SupportYes
	}
	switch c.pullState(field, state) {
	case forgeapi.PRStateMerged:
		return forgeapi.SupportYes
	case forgeapi.PRStateOpen, forgeapi.PRStateClosed:
		return forgeapi.SupportNo
	case forgeapi.PRStateUnknown:
		return forgeapi.SupportUnknown
	}
	return forgeapi.SupportUnknown
}

// normalizeDocLabels is the label normalizer for the document arm, whose members are
// spelled `title` rather than `name`.
func normalizeDocLabels(in *docLabels) []forgeapi.Label {
	if in == nil || len(in.Nodes) == 0 {
		return nil
	}
	out := make([]forgeapi.Label, 0, len(in.Nodes))
	for _, l := range in.Nodes {
		out = append(out, forgeapi.Label{Name: l.Title, Color: l.Color, Description: l.Description})
	}
	return out
}

// normalizeRESTLabels is the label normalizer for the REST arm, whose one key arrives
// in TWO shapes on this product: the label objects where the read asked for the
// details, and bare NAMES where it did not, which is what a mutation's own answer
// gives whatever a list asked for.
//
// Both arms are tried and neither is a fallback for a decode that merely failed: the
// objects first, because that is the richer answer and the shape every read here asks
// for, then the names, so a row carrying only names publishes the names rather than
// nothing. A key that is neither, which no capture carries, yields no labels rather
// than a guess.
func normalizeRESTLabels(raw json.RawMessage) []forgeapi.Label {
	if len(raw) == 0 {
		return nil
	}
	var details []restLabel
	if err := json.Unmarshal(raw, &details); err == nil && len(details) > 0 && details[0].Name != "" {
		out := make([]forgeapi.Label, 0, len(details))
		for _, l := range details {
			out = append(out, forgeapi.Label{Name: l.Name, Color: l.Color, Description: l.Description})
		}
		return out
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil || len(names) == 0 {
		return nil
	}
	out := make([]forgeapi.Label, 0, len(names))
	for _, name := range names {
		out = append(out, forgeapi.Label{Name: name})
	}
	return out
}

// labelCut marks a merge request whose label connection names a further page: the
// page holds part of a set this product does not cap, and the count says how much
// of it the item does not carry. A further page leaves at least one label out
// whatever the count states.
func (c *Client) labelCut(in *docLabels) *forgeapi.Partial {
	if in == nil || !in.PageInfo.HasNextPage {
		return nil
	}
	cut := c.partial(forgeapi.PartialPaginationCap, len(in.Nodes))
	cut.OmittedAtLeast = max(in.Count-len(in.Nodes), 1)
	return cut
}

// normalizeDocPull is the pull-request normalizer for the document arm.
//
// Four of the action fields are what this product answers rather than what the
// contract's shape suggests, and each is stated rather than guessed. The folded
// verdict is the head pipeline's ONE scalar status, so the six per-state counts are
// absent and stay zero and the verdict cannot truncate; the item's partial marker
// is the label cut alone. The queue state is [forgeapi.QueueNone] and the
// position unknown, because the only fields that would answer otherwise are the
// deprecated merge-train connection the schema gate refuses.
func (c *Client) normalizeDocPull(n *docMergeRequest, repo forgeapi.RepoRef) forgeapi.PullRequest {
	number, _ := strconv.Atoi(n.IID)
	author := ""
	if n.Author != nil {
		author = n.Author.Username
	}
	checks := forgeapi.CheckUnknown
	if n.HeadPipeline != nil {
		checks = c.checkState("pipeline status (document)", n.HeadPipeline.Status)
	}
	return forgeapi.PullRequest{
		Ref:          forgeapi.PRRef{Number: number, Sigil: sigil},
		Repo:         repo,
		Title:        n.Title,
		Body:         n.Description,
		Author:       author,
		SourceBranch: n.SourceBranch,
		SourceRepo:   sourcePath(n.SourceProject, repo),
		TargetBranch: n.TargetBranch,
		WebURL:       n.WebURL,
		HeadSHA:      n.DiffHeadSHA,
		Labels:       normalizeDocLabels(n.Labels),
		CreatedAt:    n.CreatedAt,
		UpdatedAt:    n.UpdatedAt,
		Partial:      c.labelCut(n.Labels),
		State:        c.pullState("merge request state (document)", n.State),
		Draft:        n.Draft,
		Action: forgeapi.ActionState{
			Mergeable:      supportOfPointer(n.Mergeable),
			Checks:         checks,
			AutoMergeArmed: supportOfPointer(n.AutoMergeEnabled),
			QueueState:     forgeapi.QueueNone,
			QueuePosition:  forgeapi.QueuePositionUnknown,
			MergeBlocked:   c.blockReason("detailed merge status (document)", n.DetailedMergeStatus, ""),
		},
	}
}

// sigil is how a human writes this product's merge-request number. It is a rendering
// concern and never enters a request path.
const sigil = "!"

// restPath is which of the REST arm's three paths answered a merge request, which
// decides whether its armed auto-merge is mapped. No path names a block reason, for
// the reason each constant states.
type restPath int

const (
	// restListed is the cross-repository list, which reads REST on every connection
	// and whose merge status this product documents as possibly unrefreshed, so the
	// block reason is unknown. The armed auto-merge, the merge request's own setting,
	// is mapped.
	restListed restPath = iota
	// restMutated is a mutation's own answer, whose detailed status is the
	// asynchronous check's at the instant of the transition, which the transition
	// resets: no caller can rely on it, so the block reason is unknown whatever it
	// names. The armed auto-merge is mapped.
	restMutated
	// restDegraded is the read a connection whose documents were refused falls back
	// to. This product documents that listing merge requests may not refresh the
	// merge status, and the recalculation parameter is refusable by role, so a value
	// read there is indistinguishable from a fresh one and both fields are unknown.
	restDegraded
)

// normalizeRESTPull is the pull-request normalizer for the REST arm; path decides
// which action fields are trusted.
func (c *Client) normalizeRESTPull(r *restMergeRequest, repo forgeapi.RepoRef, path restPath) forgeapi.PullRequest {
	if repo.Selector == "" {
		repo = referencedRepo(r.References, sigil)
	}
	author := ""
	if r.Author != nil {
		author = r.Author.Username
	}
	checks := forgeapi.CheckUnknown
	if r.HeadPipeline != nil {
		checks = c.checkState("pipeline status (rest)", r.HeadPipeline.Status)
	}
	// The reason is discarded on every path; the lookup still counts a member the
	// tables do not carry.
	_ = c.blockReason("detailed merge status (rest)", r.DetailedMergeStatus, r.MergeStatus)
	action := forgeapi.ActionState{
		Checks:         checks,
		Mergeable:      forgeapi.SupportUnknown,
		AutoMergeArmed: supportOfPointer(r.MergeWhenPipelineSucceeds),
		QueueState:     forgeapi.QueueNone,
		QueuePosition:  forgeapi.QueuePositionUnknown,
		MergeBlocked:   forgeapi.MergeBlockUnknown,
	}
	if path == restDegraded {
		action.AutoMergeArmed = forgeapi.SupportUnknown
	}
	return forgeapi.PullRequest{
		Ref:          forgeapi.PRRef{Number: r.IID, Sigil: sigil},
		Repo:         repo,
		Title:        r.Title,
		Body:         r.Description,
		Author:       author,
		SourceBranch: r.SourceBranch,
		SourceRepo:   sameProject(r, repo),
		TargetBranch: r.TargetBranch,
		WebURL:       r.WebURL,
		HeadSHA:      r.SHA,
		Labels:       normalizeRESTLabels(r.Labels),
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		State:        c.pullState("merge request state (rest)", r.State),
		Draft:        r.Draft,
		Action:       action,
	}
}

// sourcePath is the repository a document's head branch lives in: the addressed
// reference itself where both name one project, whatever the case, and the zero
// reference where the document answers null, which is a deleted fork.
func sourcePath(source *docProject, repo forgeapi.RepoRef) forgeapi.RepoRef {
	switch {
	case source == nil || source.FullPath == "":
		return forgeapi.RepoRef{}
	case strings.EqualFold(source.FullPath, repo.Selector):
		return repo
	}
	return repoRef(source.FullPath)
}

// sameProject is the source a REST answer can name from its own fields: the
// addressed project where both ids name it, and the zero reference otherwise,
// because a fork's path is not on this answer ([Client.resolveForks] buys it on
// the one list that pays for it).
func sameProject(r *restMergeRequest, repo forgeapi.RepoRef) forgeapi.RepoRef {
	if r.SourceProjectID == nil || r.TargetProjectID == nil || *r.SourceProjectID != *r.TargetProjectID {
		return forgeapi.RepoRef{}
	}
	return repo
}

// issueSigil is how this product's full reference separates an issue's project from
// its number, where a merge request's reference uses [sigil].
const issueSigil = "#"

// referencedRepo recovers a REST row's own project, which the cross-repository reads
// need because their rows span projects and each has to carry the addressing a
// consumer acts on.
//
// The witness is the row's own full REFERENCE, which this product spells as the
// project path, the sigil of the row's kind and the number; the numeric project id
// beside it is not a canonical selector and no identifier can be derived from it.
func referencedRepo(refs *restReferences, rowSigil string) forgeapi.RepoRef {
	if refs != nil {
		if path, _, ok := strings.Cut(refs.Full, rowSigil); ok && path != "" {
			return repoRef(path)
		}
	}
	return forgeapi.RepoRef{Family: forgeapi.FamilyGitLab}
}

// normalizeIssue is the issue normalizer. A row read with no repository addressed is
// one of the cross-repository list's, and carries its own.
func (c *Client) normalizeIssue(i *restIssue, repo forgeapi.RepoRef) forgeapi.Issue {
	if repo.Selector == "" {
		repo = referencedRepo(i.References, issueSigil)
	}
	author := ""
	if i.Author != nil {
		author = i.Author.Username
	}
	return forgeapi.Issue{
		Ref:       forgeapi.IssueRef{Number: i.IID},
		Repo:      repo,
		Title:     i.Title,
		Body:      i.Description,
		Author:    author,
		WebURL:    i.WebURL,
		Labels:    normalizeRESTLabels(i.Labels),
		CreatedAt: i.CreatedAt,
		UpdatedAt: i.UpdatedAt,
		State:     c.issueState(i.State),
	}
}

// normalizeRun is the run normalizer, over one pipeline of the project the listing
// addressed. The pipeline row carries no repository key but its numeric project id,
// so the run belongs to the repository the call named.
func (c *Client) normalizeRun(p *restPipeline, repo forgeapi.RepoRef) forgeapi.Run {
	return forgeapi.Run{
		Repo:      repo,
		Name:      p.Name,
		Branch:    p.Ref,
		HeadSHA:   p.SHA,
		WebURL:    p.WebURL,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
		State:     c.checkState("pipeline status (rest)", p.Status),
	}
}

// normalizeRelease is the release normalizer.
//
// Two of its fields are what this product does NOT have, and each reports the neutral
// value rather than a guess: there is no draft concept on a release here, and there is
// no prerelease flag either. The `upcoming_release` key beside them says the release
// date is in the future, which is a schedule rather than a prerelease, so it is not
// read as one.
func normalizeRelease(r *restRelease) forgeapi.Release {
	web := ""
	if r.Links != nil {
		web = r.Links.Self
	}
	return forgeapi.Release{
		TagName:     r.TagName,
		Name:        r.Name,
		Body:        r.Description,
		WebURL:      web,
		PublishedAt: r.ReleasedAt,
	}
}

// normalizeLabels is the repository-label normalizer.
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

// normalizeRepo is the repository normalizer.
func normalizeRepo(r *restProject) forgeapi.Repository {
	return forgeapi.Repository{
		Ref:         repoRef(r.PathWithNamespace),
		Description: r.Description,
		WebURL:      r.WebURL,
		CloneURL:    r.CloneURL,
		UpdatedAt:   r.LastActivityAt,
		Affordances: normalizeAffordances(r),
		Private:     r.Visibility != "" && r.Visibility != "public",
		Archived:    r.Archived,
		Fork:        r.ForkedFrom != nil,
	}
}

// normalizeAffordances reads one project's affordances off its own record.
//
// Two of the three three-valued answers are three-valued FOR THIS PRODUCT: whether
// issues are enabled varies with the caller's permission, so an absent flag is
// unknown rather than disabled, and push is reported as a role INTEGER, from which no
// permission and no visibility are not the same answer.
//
// The evidence map is EMPTY here, and that is the expectation table's own row rather
// than an omission: measured anonymously, this product's project record carries none
// of the permission, issue-access, merge-method or train keys the three capabilities
// would name a source for, so a map keyed by those capabilities would state a source
// for values the record did not supply.
func normalizeAffordances(r *restProject) forgeapi.RepoAffordances {
	return forgeapi.RepoAffordances{
		MergeStrategies: exposedStrategies(r),
		HasIssues:       issuesSupport(r),
		CanPush:         pushSupport(r.Permissions),
		MergeTrain:      supportOfPointer(r.MergeTrains),
		DefaultBranch:   r.DefaultBranch,
	}
}

// exposedStrategies is the merge strategies this project's record exposes, which on
// this product is DISPLAY TEXT rather than a selector: its merge endpoint takes a
// squash flag and an auto-merge flag and no strategy at all, so a caller naming one is
// refused locally. The set is the project's own merge method, plus squash where the
// project's squash option admits it.
func exposedStrategies(r *restProject) []string {
	var out []string
	if r.MergeMethod != "" {
		out = append(out, r.MergeMethod)
	}
	if squash := strings.ToLower(r.SquashOption); squash != "" && squash != "never" {
		out = append(out, "squash")
	}
	return out
}

// issuesSupport reads whether issues are enabled from either of the two keys this
// product answers it with, the boolean and the access level, and reports unknown
// where the record carries neither, which is what an anonymous read gets.
func issuesSupport(r *restProject) forgeapi.Support {
	if r.IssuesEnabled != nil {
		return supportOf(*r.IssuesEnabled)
	}
	switch strings.ToLower(r.IssuesAccessLevel) {
	case "enabled", "private":
		return forgeapi.SupportYes
	case "disabled":
		return forgeapi.SupportNo
	}
	return forgeapi.SupportUnknown
}

// pushSupport reads the push affordance off the role INTEGER this product answers,
// taking the higher of the project and group grants, and reports unknown where the
// record carries neither rather than a confident no.
func pushSupport(p *restPermissions) forgeapi.Support {
	if p == nil {
		return forgeapi.SupportUnknown
	}
	level := 0
	seen := false
	for _, access := range []*restAccess{p.ProjectAccess, p.GroupAccess} {
		if access == nil {
			continue
		}
		seen = true
		level = max(level, access.AccessLevel)
	}
	if !seen {
		return forgeapi.SupportUnknown
	}
	return supportOf(level >= developerAccess)
}

// signal reads this product's budget signal, which rides EVERY response including a
// refusal: the remaining figure for the credential's own quota and the instant that
// window renews, as epoch seconds.
func signal(header http.Header) (remaining int, reset time.Time, ok bool) {
	raw := header.Get("RateLimit-Remaining")
	if raw == "" {
		return 0, time.Time{}, false
	}
	remaining, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, time.Time{}, false
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(header.Get("RateLimit-Reset")), 10, 64)
	if err != nil {
		return remaining, time.Time{}, true
	}
	return remaining, time.Unix(seconds, 0).UTC(), true
}

// conflictCodes is the code a 409 carries per operation, empty elsewhere: on the merge
// it is the documented SHA mismatch, optimistic concurrency rather than a content
// conflict; on a creation it is the merge request that already exists for the source
// branch, a request upstream rejected as invalid.
var conflictCodes = map[string]string{opMergePR: forgeapi.CodeStaleHead, "CreatePR": forgeapi.CodeValidation}

// mapper is this family's error mapping, by operation plus status plus the headers
// that decide a cell.
//
// Two cells are the MERGE's rather than this product's, which is why the operation is
// read: this product answers 400 to a merge sent without the head commit its own
// setting can make compulsory, and 401 to a merge by a credential that may read the
// merge request and not merge it. On every other operation those two statuses mean
// what they ordinarily mean, so a list's bad parameter is not reported as an
// unmergeable pull request and a dead credential is not reported as a permission.
//
// One cell is the cross-repository lists': a 404 there is the owner the call named,
// a not-found answer under the owner code. Under an owner those lists read the
// group's own route, which answers 404 for a group the instance does not resolve and
// for a user's own namespace alike; their default scope reads an instance-wide list
// that addresses nothing it could fail to find.
//
// One cell reads the body's machine-readable error member, the 403 [forbidden] answers.
//
// A 409 is mapped per operation, by [conflictCodes]. An unmapped combination stays
// upstream and is never guessed.
func mapper(op string, status int, _ http.Header, bodyError string) (kind forgeapi.ErrorKind, code string) {
	if status == http.StatusNotFound && (op == opListMyPRs || op == opListMyIssues) {
		return forgeapi.KindNotFound, forgeapi.CodeOwnerUnresolved
	}
	switch status {
	case http.StatusBadRequest:
		if op == opMergePR {
			return forgeapi.KindNotMergeable, forgeapi.CodeMissingSHA
		}
		return forgeapi.KindUpstream, ""
	case http.StatusUnauthorized:
		if op == opMergePR {
			return forgeapi.KindForbidden, forgeapi.CodeNoMergePermission
		}
		return forgeapi.KindUnauthorized, ""
	case http.StatusForbidden:
		return forbidden(bodyError)
	case http.StatusNotFound:
		return forgeapi.KindNotFound, forgeapi.CodeRepoOrPRNotVisible
	case http.StatusMethodNotAllowed:
		return forgeapi.KindNotMergeable, forgeapi.CodeNotMergeable
	case http.StatusConflict:
		return forgeapi.KindConflict, conflictCodes[op]
	case http.StatusUnprocessableEntity:
		return forgeapi.KindNotMergeable, forgeapi.CodeBranchCannotMerge
	case http.StatusTooManyRequests:
		return forgeapi.KindRateLimited, ""
	}
	return forgeapi.KindUpstream, ""
}

// The two error members a 403 of this product names a missing scope with: its own,
// for a fine-grained token without the resource permission and for a legacy token on a
// group that requires fine-grained ones, and RFC 6750's, for a legacy token without
// the scope.
const (
	errorGranularScope = "insufficient_granular_scope"
	errorScope         = "insufficient_scope"
)

// forbidden is the 403 cell. A refusal naming a scope the credential lacks is the
// scope refusal rather than the plain forbidden a role refusal answers, because its
// remedy is the credential's permission set and never a project role. The member is a
// code the instance states; the description beside it is the message and is never
// mapped on.
func forbidden(bodyError string) (kind forgeapi.ErrorKind, code string) {
	if bodyError == errorGranularScope || bodyError == errorScope {
		return forgeapi.KindForbidden, forgeapi.CodeScopeInsufficient
	}
	return forgeapi.KindForbidden, ""
}
