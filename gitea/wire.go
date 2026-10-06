package gitea

import (
	"cmp"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// The wire shapes this family reads. They are declared here and nowhere else, so
// nothing in a forgeapi signature spells a field of this product's API: the root
// never learns "allow_fast_forward_only_merge" any more than it learns another
// family's internal number.
type (
	wireUser struct {
		Login    string   `json:"login"`
		FullName string   `json:"full_name"`
		Email    string   `json:"email"`
		WebURL   string   `json:"html_url"`
		Scopes   []string `json:"scopes"`
	}

	wireLabel struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
		// ID is the numeric identifier both products' creation options take in
		// place of the name: the published signature carries names, the wire
		// takes an array of these, so a creation naming labels resolves them
		// through the repository's own label list first.
		ID int64 `json:"id"`
	}

	wirePermissions struct {
		Admin bool `json:"admin"`
		Push  bool `json:"push"`
		Pull  bool `json:"pull"`
	}

	wireRepo struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		FullName      string           `json:"full_name"`
		Name          string           `json:"name"`
		Owner         wireUser         `json:"owner"`
		Description   string           `json:"description"`
		WebURL        string           `json:"html_url"`
		CloneURL      string           `json:"clone_url"`
		UpdatedAt     time.Time        `json:"updated_at"`
		DefaultBranch string           `json:"default_branch"`
		MergeStyle    string           `json:"default_merge_style"`
		Permissions   *wirePermissions `json:"permissions"`
		HasIssues     *bool            `json:"has_issues"`
		Private       bool             `json:"private"`
		Archived      bool             `json:"archived"`
		Fork          bool             `json:"fork"`

		// The merge-strategy flags. An absent flag reads false, which is its
		// meaning: a product whose record does not carry a style's flag does not
		// offer that style.
		AllowMerge          bool `json:"allow_merge_commits"`
		AllowSquash         bool `json:"allow_squash_merge"`
		AllowRebase         bool `json:"allow_rebase"`
		AllowRebaseExplicit bool `json:"allow_rebase_explicit"`
		AllowFastForward    bool `json:"allow_fast_forward_only_merge"`
	}

	// wireRepoMeta is the repository a CROSS-REPOSITORY row carries, which is the
	// document's RepositoryMeta rather than its Repository: the owner is a login
	// STRING there where a repository record carries a user object, so one struct
	// cannot serve both and a shared one decodes neither. Both products declare it
	// identically, and it is the reason a row of the issue-search route is read
	// through its own type.
	wireRepoMeta struct {
		FullName string `json:"full_name"`
		Name     string `json:"name"`
		Owner    string `json:"owner"`
	}

	// wirePullMeta is the pull-request object a CROSS-REPOSITORY row carries, the
	// document's PullRequestMeta, and it is where that row's draft flag lives:
	// both products declare `draft` on this object and neither declares one on the
	// Issue the route answers, so a read of the row's top level finds nothing and
	// every row would report a draft pull request as ready.
	wirePullMeta struct {
		Draft bool `json:"draft"`
	}

	wireBranch struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		Ref  string    `json:"ref"`
		SHA  string    `json:"sha"`
		Repo *wireRepo `json:"repo"`
	}

	wirePull struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		Number       int           `json:"number"`
		State        string        `json:"state"`
		Title        string        `json:"title"`
		Body         string        `json:"body"`
		User         wireUser      `json:"user"`
		WebURL       string        `json:"html_url"`
		CreatedAt    time.Time     `json:"created_at"`
		UpdatedAt    time.Time     `json:"updated_at"`
		Labels       []wireLabel   `json:"labels"`
		Head         wireBranch    `json:"head"`
		Base         wireBranch    `json:"base"`
		Draft        bool          `json:"draft"`
		Merged       bool          `json:"merged"`
		Mergeable    *bool         `json:"mergeable"`
		Repository   *wireRepoMeta `json:"repository"`
		PullRequest  *wirePullMeta `json:"pull_request"`
		MergedCommit string        `json:"merge_commit_sha"`
	}

	wireIssue struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		Number     int           `json:"number"`
		State      string        `json:"state"`
		Title      string        `json:"title"`
		Body       string        `json:"body"`
		User       wireUser      `json:"user"`
		WebURL     string        `json:"html_url"`
		CreatedAt  time.Time     `json:"created_at"`
		UpdatedAt  time.Time     `json:"updated_at"`
		Labels     []wireLabel   `json:"labels"`
		Repository *wireRepoMeta `json:"repository"`
	}

	wireRelease struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		Body        string    `json:"body"`
		WebURL      string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Target      string    `json:"target_commitish"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
	}

	wireStatus struct {
		Context     string `json:"context"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
		Status      string `json:"status"`
		State       string `json:"state"`
	}

	wireCombined struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		SHA        string       `json:"sha"`
		State      string       `json:"state"`
		TotalCount int          `json:"total_count"`
		CommitURL  string       `json:"commit_url"`
		Statuses   []wireStatus `json:"statuses"`
	}

	wireVersion struct {
		Version string `json:"version"`
	}

	// wireSettings is the part of the instance's API settings this family reads:
	// the most rows one page of a list serves.
	wireSettings struct {
		MaxResponseItems int `json:"max_response_items"`
	}

	// wireRun is one Actions run, in both products' spellings at once: the two
	// answer the same envelope with a different object inside it, Gitea the
	// GitHub-shaped ActionWorkflowRun its document declares and Forgejo the
	// ActionRun its own declares, which share only the id, the page and the
	// status key. Each pair below is one fact under two spellings, and only one
	// member of it arrives from either product.
	wireRun struct { //nolint:govet // fieldalignment: the field order is the wire document's own, so a reader compares this type against the response it decodes
		ID         int64     `json:"id"`
		HeadSHA    string    `json:"head_sha"`
		CommitSHA  string    `json:"commit_sha"`
		Status     string    `json:"status"`
		Conclusion string    `json:"conclusion"`
		Path       string    `json:"path"`
		WorkflowID string    `json:"workflow_id"`
		HeadBranch string    `json:"head_branch"`
		PrettyRef  string    `json:"prettyref"`
		WebURL     string    `json:"html_url"`
		CreatedAt  time.Time `json:"created_at"`
		Created    time.Time `json:"created"`
		UpdatedAt  time.Time `json:"updated_at"`
		Updated    time.Time `json:"updated"`
	}

	// wireRuns is the run listing's envelope. Its total is a pointer because an
	// envelope that omits it is not one stating a total of zero: the total is what
	// decides the listing's continuation.
	wireRuns struct {
		TotalCount   *int      `json:"total_count"`
		WorkflowRuns []wireRun `json:"workflow_runs"`
	}
)

// checkStates is this family's commit-status enumeration, mapped TOTALLY: a table
// over every value its own documentation declares. A value outside it yields
// [forgeapi.CheckUnknown], a counter increment and a log line naming it, which is
// what tells a maintainer the upstream enumeration grew.
var checkStates = map[string]forgeapi.CheckState{
	"success": forgeapi.CheckPassing,
	"failure": forgeapi.CheckFailing,
	"error":   forgeapi.CheckFailing,
	"pending": forgeapi.CheckPending,
	"warning": forgeapi.CheckNeutral,
	"skipped": forgeapi.CheckNeutral,
	"":        forgeapi.CheckUnknown,
}

// prStates is this family's pull-request state enumeration, mapped totally: the two
// members its own swagger closes the field at, and the empty string. A merged pull
// request answers the CLOSED spelling with the merged flag set, which is why the
// normalizer reads the pair rather than the string alone, and why no third member
// belongs here: a spelling the document does not declare would be admitted as known
// and would bypass the unknown-value signal if a release ever sent it meaning
// something else.
var prStates = map[string]forgeapi.PRState{
	stateOpen:   forgeapi.PRStateOpen,
	stateClosed: forgeapi.PRStateClosed,
	"":          forgeapi.PRStateUnknown,
}

// issueStates is this family's issue state enumeration, mapped totally.
var issueStates = map[string]forgeapi.IssueState{
	stateOpen:   forgeapi.IssueStateOpen,
	stateClosed: forgeapi.IssueStateClosed,
	"":          forgeapi.IssueStateUnknown,
}

// runCompleted is the Gitea run status whose outcome is the conclusion beside it
// rather than the status itself.
const runCompleted = "completed"

// runStates maps this family's Actions run status TOTALLY over what each product's
// serializer writes, since neither document closes the field: Gitea's status, or on
// a completed run its conclusion in the status's place, and Forgejo's status name,
// which carries the outcome. One table serves both because no spelling means two
// things across them. Cancelled and skipped are neutral, as on the other families:
// the run neither failed nor passed.
var runStates = map[string]forgeapi.CheckState{
	"success":     forgeapi.CheckPassing,
	"failure":     forgeapi.CheckFailing,
	"cancelled":   forgeapi.CheckNeutral,
	"skipped":     forgeapi.CheckNeutral,
	"queued":      forgeapi.CheckPending,
	"pending":     forgeapi.CheckPending,
	"requested":   forgeapi.CheckPending,
	"waiting":     forgeapi.CheckPending,
	"in_progress": forgeapi.CheckPending,
	"running":     forgeapi.CheckPending,
	"blocked":     forgeapi.CheckPending,
	"unknown":     forgeapi.CheckUnknown,
	"":            forgeapi.CheckUnknown,
}

// mergeStrategies is this family's closed set of merge-option spellings, which is
// what a caller's strategy is refused against locally, before any request: the
// styles its own swagger document declares for the merge option that merge the
// pull request. The swagger's manually-merged style is not one, because it records
// a merge made outside the forge and names that merge's commit in a field a
// [forgeapi.MergeRequest] does not carry.
var mergeStrategies = []string{
	styleMerge, "rebase", "rebase-merge", styleSquash, "fast-forward-only",
}

// The two merge-option spellings the intent resolves to without a caller naming a
// strategy of its own.
const (
	styleMerge  = "merge"
	styleSquash = "squash"
)

// strategyFlags pairs each merge-option spelling with the repository-record flag
// that switches it on, in the order a record lists them.
var strategyFlags = []struct { //nolint:govet // fieldalignment: the spelling leads because the pair reads as a name and how to read it
	name string
	read func(*wireRepo) bool
}{
	{styleMerge, func(r *wireRepo) bool { return r.AllowMerge }},
	{styleSquash, func(r *wireRepo) bool { return r.AllowSquash }},
	{"rebase", func(r *wireRepo) bool { return r.AllowRebase }},
	{"rebase-merge", func(r *wireRepo) bool { return r.AllowRebaseExplicit }},
	{"fast-forward-only", func(r *wireRepo) bool { return r.AllowFastForward }},
}

// support turns a flag the record may not carry into a three-valued answer, which
// is the reason these fields are not bools: an absent flag is unknown rather than
// false.
func support(flag *bool) forgeapi.Support {
	switch {
	case flag == nil:
		return forgeapi.SupportUnknown
	case *flag:
		return forgeapi.SupportYes
	}
	return forgeapi.SupportNo
}

// repoRef derives the reference for one repository record. The identifier is
// derived from the selector rather than allocated, so it is the same value a
// consumer mints from a git remote with no round trip.
func repoRef(fullName string) forgeapi.RepoRef {
	ref := forgeapi.RepoRef{Family: forgeapi.FamilyGitea, Selector: fullName, DisplayPath: fullName}
	ref.ID = ref.Encode()
	return ref
}

// normalizeRepo is the repository normalizer.
func (c *Client) normalizeRepo(r *wireRepo) forgeapi.Repository {
	return forgeapi.Repository{
		Ref:         repoRef(r.FullName),
		Description: r.Description,
		WebURL:      r.WebURL,
		CloneURL:    r.CloneURL,
		UpdatedAt:   r.UpdatedAt,
		Affordances: c.normalizeAffordances(r),
		Private:     r.Private,
		Archived:    r.Archived,
		Fork:        r.Fork,
	}
}

// normalizeAffordances reads one repository's affordances off its own record.
//
// [forgeapi.RepoAffordances.MergeTrain] is [forgeapi.SupportNo] on every
// repository this family serves, and its evidence is the version body rather than
// the record: no field of the record answers it, and what does is the product the
// version identified.
func (c *Client) normalizeAffordances(r *wireRepo) forgeapi.RepoAffordances {
	body := forgeapi.Evidence{Source: forgeapi.EvidenceResponseBody, Detail: "repository record"}
	return forgeapi.RepoAffordances{
		MergeStrategies: exposedStrategies(r),
		HasIssues:       support(r.HasIssues),
		CanPush:         pushSupport(r.Permissions),
		MergeTrain:      forgeapi.SupportNo,
		Ev: map[forgeapi.Capability]forgeapi.Evidence{
			forgeapi.CapHasIssues:  body,
			forgeapi.CapCanPush:    body,
			forgeapi.CapMergeTrain: {Source: forgeapi.EvidenceVersion, Detail: c.productDetail()},
		},
		DefaultBranch: r.DefaultBranch,
	}
}

// exposedStrategies is the merge-option spellings this repository allows, in the
// record's own order: a style whose flag the record switches on.
func exposedStrategies(r *wireRepo) []string {
	var out []string
	for _, f := range strategyFlags {
		if f.read(r) {
			out = append(out, f.name)
		}
	}
	return out
}

func pushSupport(p *wirePermissions) forgeapi.Support {
	if p == nil {
		return forgeapi.SupportUnknown
	}
	return support(&p.Push)
}

// normalizeLabels is the label normalizer.
func normalizeLabels(in []wireLabel) []forgeapi.Label {
	if len(in) == 0 {
		return nil
	}
	out := make([]forgeapi.Label, 0, len(in))
	for _, l := range in {
		out = append(out, forgeapi.Label{Name: l.Name, Color: l.Color, Description: l.Description})
	}
	return out
}

// normalizePull is the pull-request normalizer, without the folded check verdict:
// there is no document to fold it into this call, so the fold is a read of its own
// and the caller decides whether to pay for it.
func (c *Client) normalizePull(p *wirePull, repo forgeapi.RepoRef) forgeapi.PullRequest {
	return forgeapi.PullRequest{
		Ref:          forgeapi.PRRef{Number: p.Number, Sigil: "#"},
		Repo:         repo,
		Title:        p.Title,
		Body:         p.Body,
		Author:       p.User.Login,
		SourceBranch: p.Head.Ref,
		SourceRepo:   sourceRepo(p.Head.Repo, p.Base.Repo, repo),
		TargetBranch: p.Base.Ref,
		WebURL:       p.WebURL,
		HeadSHA:      p.Head.SHA,
		Labels:       normalizeLabels(p.Labels),
		CreatedAt:    p.CreatedAt,
		UpdatedAt:    p.UpdatedAt,
		Action:       c.normalizeAction(p),
		State:        c.pullState(p),
		Draft:        pullDraft(p),
	}
}

// normalizeMutatedPull is the pull request a mutation's own answer carries. Its
// mergeable flag is false while the instance holds the pull request queued for its
// asynchronous check, which a push to either branch re-queues, so a false there
// cannot be told from a conflict and a true says nothing the next check keeps: the
// flag answers unknown either way.
func (c *Client) normalizeMutatedPull(p *wirePull, repo forgeapi.RepoRef) forgeapi.PullRequest {
	item := c.normalizePull(p, repo)
	item.Action.Mergeable = forgeapi.SupportUnknown
	return item
}

// pullDraft reads the row's draft flag, whose two arms are the two ROUTES' own
// shapes: a pull-request row carries the flag at its top
// level, and a cross-repository row is an Issue, which carries it on the
// pull-request object both documents declare it on. Either arm is a bool with no
// unknown member, so a flag read in the wrong place answers a confident false on
// every row of the poller's own call.
func pullDraft(p *wirePull) bool {
	return p.Draft || (p.PullRequest != nil && p.PullRequest.Draft)
}

// sourceRepo is the repository a head branch lives in, from the head's own
// repository: the row's reference itself where head and base name one repository,
// whatever the case, and the zero reference where the head names none, which is a
// deleted fork (measured on Forgejo: repo null, repo_id -1). Sameness is read off
// the answer, because a list that followed a rename addresses the old name while
// both branches name the current one; the addressed selector decides it only on a
// row that carries no base repository.
func sourceRepo(head, base *wireRepo, repo forgeapi.RepoRef) forgeapi.RepoRef {
	switch {
	case head == nil || head.FullName == "":
		return forgeapi.RepoRef{}
	case base != nil && base.FullName != "":
		if strings.EqualFold(head.FullName, base.FullName) {
			return repo
		}
	case strings.EqualFold(head.FullName, repo.Selector):
		return repo
	}
	return repoRef(head.FullName)
}

// metaRepo is the reference a cross-repository row's own repository meta names,
// and the family's empty reference where the row carries none.
func metaRepo(m *wireRepoMeta) forgeapi.RepoRef {
	if m != nil && m.FullName != "" {
		return repoRef(m.FullName)
	}
	return forgeapi.RepoRef{Family: forgeapi.FamilyGitea}
}

// pullState reads the state and the merged flag together, because a merged pull
// request answers the closed spelling with the flag set. A spelling the table does
// not carry answers the unknown member, counted and named, exactly as the
// commit-status fold does: an enumeration that grew upstream has to be visible as
// that rather than as a pull request in no state.
func (c *Client) pullState(p *wirePull) forgeapi.PRState {
	if p.Merged {
		return forgeapi.PRStateMerged
	}
	state, ok := prStates[p.State]
	if ok {
		return state
	}
	c.unmapped("pull request state", p.State)
	return forgeapi.PRStateUnknown
}

// issueState maps one issue's state totally, under the same rule.
func (c *Client) issueState(raw string) forgeapi.IssueState {
	state, ok := issueStates[raw]
	if ok {
		return state
	}
	c.unmapped("issue state", raw)
	return forgeapi.IssueStateUnknown
}

// unmapped is what every one of this family's totality tables does with a value it
// does not carry: count it by family and field, and log the value, which is what
// tells a maintainer the upstream enumeration grew rather than leaving an unknown
// with no cause. The value is the instance's own text, so it is bounded and
// rune-sanitized before it reaches a handler this library cannot see.
func (c *Client) unmapped(field, value string) {
	if counters := c.core.Counters(); counters.UnknownEnumValue != nil {
		counters.UnknownEnumValue(forgeapi.FamilyGitea, field)
	}
	c.core.Logger().Warn("forgeapi unmapped enumeration value",
		"family", forgeapi.FamilyGitea.String(), "field", field, "value", transport.Sanitize(value))
}

// normalizeAction is the per-item action state. Four of its fields are what this
// product cannot supply and each carries the neutral value rather than a guess:
// there is no auto-merge key, so the armed flag is unknown; there is no queue and
// no train, so the queue state is none and the position unknown; and the record
// carries a mergeable flag and a draft flag without a reason, so the block reason
// is unknown rather than one of the seven causes invented from the pair.
func (c *Client) normalizeAction(p *wirePull) forgeapi.ActionState {
	return forgeapi.ActionState{
		Mergeable:      support(p.Mergeable),
		Checks:         forgeapi.CheckUnknown,
		AutoMergeArmed: forgeapi.SupportUnknown,
		QueueState:     forgeapi.QueueNone,
		QueuePosition:  forgeapi.QueuePositionUnknown,
		MergeBlocked:   forgeapi.MergeBlockUnknown,
	}
}

// normalizeIssue is the issue normalizer. repo is the addressed repository, and a
// cross-repository row, which addresses none, carries its own as the meta shape.
func (c *Client) normalizeIssue(i *wireIssue, repo forgeapi.RepoRef) forgeapi.Issue {
	if repo.Selector == "" {
		repo = metaRepo(i.Repository)
	}
	return forgeapi.Issue{
		Ref:       forgeapi.IssueRef{Number: i.Number},
		Repo:      repo,
		Title:     i.Title,
		Body:      i.Body,
		Author:    i.User.Login,
		WebURL:    i.WebURL,
		Labels:    normalizeLabels(i.Labels),
		CreatedAt: i.CreatedAt,
		UpdatedAt: i.UpdatedAt,
		State:     c.issueState(i.State),
	}
}

// normalizeRelease is the release normalizer.
func normalizeRelease(r *wireRelease) forgeapi.Release {
	return forgeapi.Release{
		TagName:     r.TagName,
		Name:        r.Name,
		Body:        r.Body,
		WebURL:      r.WebURL,
		PublishedAt: r.PublishedAt,
		Draft:       r.Draft,
		Prerelease:  r.Prerelease,
	}
}

// normalizeRun is the run normalizer. Every field is read under whichever of the
// two products' spellings arrived, and the repository is the one the listing
// addressed, which is what the row's own repository object echoes.
func (c *Client) normalizeRun(r *wireRun, repo forgeapi.RepoRef) forgeapi.Run {
	return forgeapi.Run{
		Repo:      repo,
		Name:      r.name(),
		Branch:    cmp.Or(r.HeadBranch, r.PrettyRef),
		HeadSHA:   r.head(),
		WebURL:    r.WebURL,
		CreatedAt: firstTime(r.CreatedAt, r.Created),
		UpdatedAt: firstTime(r.UpdatedAt, r.Updated),
		State:     c.runState(r),
	}
}

// head is the commit the run ran on, under either product's spelling.
func (r *wireRun) head() string { return cmp.Or(r.HeadSHA, r.CommitSHA) }

// name is the workflow file the run came from, without its directory and its YAML
// extension, which is the display name a workflow carries where it declares none.
// Gitea spells the file as the path before the ref the run ran on, file@ref, and
// Forgejo names it alone.
func (r *wireRun) name() string {
	file := r.WorkflowID
	if r.Path != "" {
		file, _, _ = strings.Cut(r.Path, "@")
	}
	file = path.Base(file)
	if file == "." || file == "/" {
		return ""
	}
	for _, ext := range []string{".yaml", ".yml"} {
		if stem, ok := strings.CutSuffix(file, ext); ok {
			return stem
		}
	}
	return file
}

// firstTime is the first of two spellings of one timestamp that arrived.
func firstTime(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}

// runState folds one run's outcome totally. A completed Gitea run's outcome is its
// conclusion and every other status is its own outcome, which is the only spelling
// a Forgejo run carries.
func (c *Client) runState(r *wireRun) forgeapi.CheckState {
	field, raw := "actions run status", r.Status
	if r.Status == runCompleted {
		field, raw = "actions run conclusion", r.Conclusion
	}
	state, ok := runStates[raw]
	if ok {
		return state
	}
	c.unmapped(field, raw)
	return forgeapi.CheckUnknown
}

// checkState maps one status row totally, counting and naming a value the table
// does not carry rather than folding it into a verdict.
func (c *Client) checkState(raw string) forgeapi.CheckState {
	state, ok := checkStates[raw]
	if ok {
		return state
	}
	c.unmapped("commit status state", raw)
	return forgeapi.CheckUnknown
}

// statusState is one row's state, which this product spells under either of two
// keys on the combined read.
func statusState(s *wireStatus) string {
	if s.Status != "" {
		return s.Status
	}
	return s.State
}

// signal reads this family's budget signal. It reads the header where it is there
// rather than branching on the product marker, because reading a signal where it
// exists is cheaper and more honest than a second product branch: one of the two
// products sends a structured rate-limit header on every response and the other
// sends nothing at all.
func signal(header http.Header) (remaining int, reset time.Time, ok bool) {
	raw := header.Get("RateLimit")
	if raw == "" {
		return 0, time.Time{}, false
	}
	var seconds int
	var haveRemaining bool
	for part := range strings.SplitSeq(raw, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			continue
		}
		switch key {
		case "r":
			remaining, haveRemaining = n, true
		case "t":
			seconds = n
		}
	}
	if !haveRemaining {
		return 0, time.Time{}, false
	}
	return remaining, time.Now().Add(time.Duration(seconds) * time.Second), true
}

// scopedOps are the operations that read the issue-search route under a scope,
// which is where the mapper reads a 400 as the owner the call named.
var scopedOps = map[string]bool{"ListMyPRs": true, "ListMyIssues": true}

// conflictCodes is the code a 409 carries per operation, empty elsewhere. The merge
// route answers it for a head that moved ("head out of date"), for a merge that
// conflicts and for a merge asked to wait while one is already scheduled (which
// stands), in human text alone, so it names no cause; a creation's 409 is the pull
// request that already exists, a request upstream rejected as invalid.
var conflictCodes = map[string]string{"MergePR": forgeapi.CodeNotMergeable, "CreatePR": forgeapi.CodeValidation}

// mapper is this family's error mapping, by operation plus status plus the headers
// that decide a cell.
//
// A 409 is mapped per operation, by [conflictCodes]. An archived
// repository is a permanent refusal whose remedy is an operator unarchiving it,
// never retried and never reported as a conflict, because no rebase and no re-read
// reaches that state. A 405 is the bare merge refusal, as on the other two: this
// product answers it for a merge style the repository disallows and for every other
// refusal of its merge route alike, a pull request not yet mergeable among them, so
// the status names no cause.
//
// One cell is per OPERATION: a 400 on either cross-repository list is the owner the
// instance could not resolve, a not-found answer under the owner code, because the
// issue-search route answers 400 for an owner or a team it cannot resolve and those
// lists send no team.
//
// No cell reads the body's error member.
func mapper(op string, status int, header http.Header, _ string) (kind forgeapi.ErrorKind, code string) {
	if status == http.StatusBadRequest && scopedOps[op] {
		return forgeapi.KindNotFound, forgeapi.CodeOwnerUnresolved
	}
	switch status {
	case http.StatusUnauthorized:
		return forgeapi.KindUnauthorized, ""
	case http.StatusForbidden:
		if header.Get("RateLimit-Remaining") == "0" {
			return forgeapi.KindRateLimited, ""
		}
		return forgeapi.KindForbidden, ""
	case http.StatusNotFound:
		// One REST status carries no evidence separating a pull request that
		// does not exist from a repository the credential cannot see, so this
		// cell reports the UNION rather than manufacturing the difference.
		return forgeapi.KindNotFound, forgeapi.CodeRepoOrPRNotVisible
	case http.StatusMethodNotAllowed:
		return forgeapi.KindNotMergeable, forgeapi.CodeNotMergeable
	case http.StatusConflict:
		return forgeapi.KindConflict, conflictCodes[op]
	case http.StatusUnprocessableEntity:
		return forgeapi.KindNotMergeable, forgeapi.CodeValidation
	case http.StatusLocked:
		return forgeapi.KindForbidden, forgeapi.CodeRepoArchived
	case http.StatusTooManyRequests:
		return forgeapi.KindRateLimited, ""
	}
	if status >= 500 {
		return forgeapi.KindUpstream, ""
	}
	return forgeapi.KindUpstream, ""
}
