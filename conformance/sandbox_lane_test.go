package conformance

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/internal/spec"
	"github.com/cplieger/httpx/v5"
	"github.com/cplieger/runesafe/v2"
)

// The lane's marks. Everything a live mutation makes in a sandbox carries one
// at the start of its name or title, which is what lets a later run find and remove
// what an aborted one left. A branch the sandbox's own CI must run on takes the
// second, because that CI runs on a push to a branch named forgeapi-ci-*; every
// other branch, tag, release, pull request and issue takes the first.
const (
	laneMark   = "forgeapi-live-"
	laneCIMark = "forgeapi-ci-live-"
)

// The lane's bounds: one request, one removal after a case, and the wait for a pull
// request's mergeability, which every family computes after the opening answer
// rather than inside it.
const (
	laneRequestBound   = 30 * time.Second
	laneCleanupBound   = 2 * time.Minute
	laneMergeableBound = time.Minute
	lanePoll           = 5 * time.Second
	laneRunWaitDefault = "5m"
)

// laneOwned reports whether a name or a title is one the lane made, so the sweep
// may remove it. It is the only thing standing between a sweep and a sandbox's own
// objects, which is why it reads a mark at the START of the text and nowhere else.
func laneOwned(name string) bool {
	return strings.HasPrefix(name, laneMark) || strings.HasPrefix(name, laneCIMark)
}

// nonName is what a run id may not carry once it becomes part of a branch name, a
// tag name and a title on every product.
var nonName = regexp.MustCompile(`[^a-z0-9-]+`)

// laneRun is this process's run id, which every name the run makes carries after
// its mark: the workflow's own run where FORGEAPI_LIVE_RUN names one, and the
// process's start otherwise. It is cut short, so a name built on it stays well
// inside every product's ref-name limit.
var laneRun = sync.OnceValue(func() string {
	run := strings.Trim(nonName.ReplaceAllString(strings.ToLower(os.Getenv("FORGEAPI_LIVE_RUN")), "-"), "-")
	if run == "" {
		run = time.Now().UTC().Format("20060102-150405")
	}
	if len(run) > 40 {
		run = strings.Trim(run[:40], "-")
	}
	return run
})

// sameRepository is the selector equality the derived repository id states: two
// selectors name one repository exactly when they agree with case folded, which on
// the character class a selector admits is the lowercasing the id is derived by.
func sameRepository(a, b string) bool {
	return strings.EqualFold(a, b)
}

// sandboxGate is why a mutation may not write to the subject's repository, and it
// is empty where it may: the product's own sandbox variable names that repository.
// The variable is read per product, so a sandbox named for one product admits no
// write to another.
func sandboxGate(p spec.Product, s subject) string {
	name := liveVar(p, "SANDBOX")
	sandbox := os.Getenv(name)
	switch {
	case sandbox == "":
		return fmt.Sprintf("%s is unset, so no repository on %s is named as a sandbox and a mutation writes nowhere; the offline case covers it", name, p)
	case !sameRepository(sandbox, s.repo.Selector):
		return fmt.Sprintf("%s names %q and the case would write to %q, so it writes nowhere: a mutation runs only on the repository named as the sandbox", name, sandbox, s.repo.Selector)
	}
	return ""
}

// lane is the live lane's reach into one sandbox: the routes it uses to make what a
// mutation writes against, to remove what the mutation made, and to list the
// sandbox before and after a run.
//
// It speaks each product's REST API directly rather than through the library, for
// two reasons. No operation creates or deletes a branch, a tag or a release, so
// there is nothing of the library's to go through. And a removal going through the
// library under test would leave the sandbox dirty exactly when that library is
// wrong, which is when the lane most needs it clean.
type lane struct {
	client  *http.Client
	product spec.Product
	api     string
	repo    string
	token   string
	trunk   string
}

// newLane is the lane for one product's sandbox, addressed by the instance's web
// base the way the family derives its API root from it.
func newLane(p spec.Product, base, token, repo string) *lane {
	return &lane{
		client:  &http.Client{Timeout: laneRequestBound, Transport: &http.Transport{}},
		product: p,
		api:     laneAPIRoot(p, base),
		repo:    repo,
		token:   token,
	}
}

// laneAPIRoot is one product's REST root under an instance's web base: the hosted
// GitHub serves it on a host of its own, an appliance under /api/v3, and the other
// families under their versioned roots.
func laneAPIRoot(p spec.Product, base string) string {
	base = strings.TrimSuffix(base, "/")
	switch p {
	case spec.GitHub:
		if u, err := url.Parse(base); err == nil && u.Hostname() == "github.com" {
			return "https://api.github.com"
		}
		return base + "/api/v3"
	case spec.GitLab:
		return base + "/api/v4"
	}
	return base + "/api/v1"
}

// sandboxLane is the lane for one product where the live lane may write there: an
// instance and a token are named and the sandbox variable names the live subject.
func sandboxLane(p spec.Product) (*lane, bool) {
	base := os.Getenv(liveURLVar(p))
	token := os.Getenv(liveTokenVar(p))
	s := liveSubject(p)
	if base == "" || token == "" || s.repo.Selector == "" || sandboxGate(p, s) != "" {
		return nil, false
	}
	return newLane(p, base, token, s.repo.Selector), true
}

// name is a branch, tag or release name this run owns, for one purpose.
func (l *lane) name(kind string) string { return laneMark + laneRun() + "-" + kind }

// ciName is a branch name this run owns on which the sandbox's own CI runs.
func (l *lane) ciName(kind string) string { return laneCIMark + laneRun() + "-" + kind }

// titlePrefix is the text every title this run gives starts with.
func (l *lane) titlePrefix() string { return laneMark + laneRun() + " " }

// repoPath is the sandbox repository's own route on this product with rest after it.
func (l *lane) repoPath(rest string) string {
	if l.product == spec.GitLab {
		return "/projects/" + url.PathEscape(l.repo) + rest
	}
	return "/repos/" + l.repo + rest
}

// call sends one request and decodes a 2xx answer into out where out is not nil. A
// status listed in also is an answer rather than a failure, which is how a removal
// reads an object that is already gone.
func (l *lane) call(ctx context.Context, method, path string, body, out any, also ...int) (int, error) {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("%s %s: encoding the body: %w", method, path, err)
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, l.api+path, payload)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch l.product {
	case spec.Gitea, spec.Forgejo:
		req.Header.Set("Authorization", "token "+l.token)
	default:
		req.Header.Set("Authorization", "Bearer "+l.token)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFixtureBody))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("%s %s: reading the answer: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if slices.Contains(also, resp.StatusCode) {
			return resp.StatusCode, nil
		}
		return resp.StatusCode, fmt.Errorf("%s %s = status %d: %s", method, path, resp.StatusCode, laneSnippet(raw, l.token))
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: decoding the answer: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// laneSnippetBytes bounds the refusal text a lane failure quotes.
const laneSnippetBytes = 300

// laneSnippet is the start of a refusal's body on one line, enough to name what the
// instance objected to, and safe to print beside a token the request carried.
//
// The body is the instance's text, which can echo the request's credential, so
// the token is redacted, the text made safe for one line, and the token redacted
// again, because the sanitizer can rewrite a byte inside a match the first pass
// missed; the cut comes last and on a rune boundary, so it neither leaves a
// partial token the needle no longer matches nor splits a rune.
func laneSnippet(raw []byte, token string) string {
	secret := httpx.Secret(token)
	s := httpx.RedactSecretString(string(raw), secret)
	s = strings.Join(strings.Fields(runesafe.SanitizeSingleLine(s)), " ")
	s = httpx.RedactSecretString(s, secret)
	if len(s) > laneSnippetBytes {
		s = runesafe.CapBytes(s, laneSnippetBytes) + "..."
	}
	return s
}

// defaultBranch is the sandbox's default branch, read once and only when a case
// first needs it, so a case that makes no branch sends nothing for it.
func (l *lane) defaultBranch(ctx context.Context) (string, error) {
	if l.trunk != "" {
		return l.trunk, nil
	}
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if _, err := l.call(ctx, http.MethodGet, l.repoPath(""), nil, &r); err != nil {
		return "", err
	}
	if r.DefaultBranch == "" {
		return "", fmt.Errorf("the repository record of %s names no default branch", l.repo)
	}
	l.trunk = r.DefaultBranch
	return l.trunk, nil
}

// githubHead is the commit a GitHub branch points at.
func (l *lane) githubHead(ctx context.Context, branch string) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if _, err := l.call(ctx, http.MethodGet, l.repoPath("/git/ref/heads/"+branch), nil, &ref); err != nil {
		return "", err
	}
	if ref.Object.SHA == "" {
		return "", fmt.Errorf("the ref heads/%s of %s names no commit", branch, l.repo)
	}
	return ref.Object.SHA, nil
}

// createBranch makes a branch at the default branch's head, with no commit of its
// own: a base a pull request targets, so a merge moves nothing the run did not make.
func (l *lane) createBranch(ctx context.Context, name string) error {
	trunk, err := l.defaultBranch(ctx)
	if err != nil {
		return err
	}
	switch l.product {
	case spec.GitHub:
		head, err := l.githubHead(ctx, trunk)
		if err != nil {
			return err
		}
		_, err = l.call(ctx, http.MethodPost, l.repoPath("/git/refs"), map[string]string{"ref": "refs/heads/" + name, "sha": head}, nil)
		return err
	case spec.GitLab:
		_, err := l.call(ctx, http.MethodPost, l.repoPath("/repository/branches"), map[string]string{"branch": name, "ref": trunk}, nil)
		return err
	}
	_, err = l.call(ctx, http.MethodPost, l.repoPath("/branches"), map[string]string{"new_branch_name": name, "old_branch_name": trunk}, nil)
	return err
}

// createBranchWithCommit makes a branch one new commit ahead of the default branch
// and answers that commit. The branch and its commit arrive in ONE push on every
// product, so the sandbox's own CI runs once, on that commit, where a branch made
// first and committed to after would run it twice.
func (l *lane) createBranchWithCommit(ctx context.Context, name string) (string, error) {
	trunk, err := l.defaultBranch(ctx)
	if err != nil {
		return "", err
	}
	path := "forgeapi-live/" + name + ".md"
	content := "Written by the forgeapi live lane, run " + laneRun() + ".\n"
	message := l.titlePrefix() + "commit for " + name
	switch l.product {
	case spec.GitHub:
		return l.githubCommit(ctx, trunk, name, path, content, message)
	case spec.GitLab:
		var commit struct {
			ID string `json:"id"`
		}
		_, err := l.call(ctx, http.MethodPost, l.repoPath("/repository/commits"), map[string]any{
			"branch":         name,
			"start_branch":   trunk,
			"commit_message": message,
			"actions":        []map[string]string{{"action": "create", "file_path": path, "content": content}},
		}, &commit)
		if err == nil && commit.ID == "" {
			err = fmt.Errorf("the commit on %s answered no id", name)
		}
		return commit.ID, err
	}
	var file struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	_, err = l.call(ctx, http.MethodPost, l.repoPath("/contents/"+path), map[string]string{
		"branch":     trunk,
		"new_branch": name,
		"content":    base64.StdEncoding.EncodeToString([]byte(content)),
		"message":    message,
	}, &file)
	if err == nil && file.Commit.SHA == "" {
		err = fmt.Errorf("the commit on %s answered no sha", name)
	}
	return file.Commit.SHA, err
}

// githubCommit builds one commit over the default branch's tree through the git
// data routes and points a new branch at it.
func (l *lane) githubCommit(ctx context.Context, trunk, name, path, content, message string) (string, error) {
	head, err := l.githubHead(ctx, trunk)
	if err != nil {
		return "", err
	}
	var parent struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if _, err := l.call(ctx, http.MethodGet, l.repoPath("/git/commits/"+head), nil, &parent); err != nil {
		return "", err
	}
	var tree, commit struct {
		SHA string `json:"sha"`
	}
	if _, err := l.call(ctx, http.MethodPost, l.repoPath("/git/trees"), map[string]any{
		"base_tree": parent.Tree.SHA,
		"tree":      []map[string]string{{"path": path, "mode": "100644", "type": "blob", "content": content}},
	}, &tree); err != nil {
		return "", err
	}
	if _, err := l.call(ctx, http.MethodPost, l.repoPath("/git/commits"), map[string]any{
		"message": message,
		"tree":    tree.SHA,
		"parents": []string{head},
	}, &commit); err != nil {
		return "", err
	}
	if _, err := l.call(ctx, http.MethodPost, l.repoPath("/git/refs"), map[string]string{"ref": "refs/heads/" + name, "sha": commit.SHA}, nil); err != nil {
		return "", err
	}
	return commit.SHA, nil
}

// laneNumber is the number a creation answers, under the name each product gives
// it: GitLab numbers within a project as iid.
type laneNumber struct {
	Number int `json:"number"`
	IID    int `json:"iid"`
}

func (n laneNumber) of(p spec.Product) int {
	if p == spec.GitLab {
		return n.IID
	}
	return n.Number
}

// openPR opens a pull request from head into base.
func (l *lane) openPR(ctx context.Context, head, base, title string) (int, error) {
	var got laneNumber
	var err error
	if l.product == spec.GitLab {
		_, err = l.call(ctx, http.MethodPost, l.repoPath("/merge_requests"), map[string]string{
			"source_branch": head, "target_branch": base, "title": title, "description": prBody,
		}, &got)
	} else {
		_, err = l.call(ctx, http.MethodPost, l.repoPath("/pulls"), map[string]string{
			"head": head, "base": base, "title": title, "body": prBody,
		}, &got)
	}
	if err == nil && got.of(l.product) <= 0 {
		err = fmt.Errorf("opening a pull request from %s answered no number", head)
	}
	return got.of(l.product), err
}

// prPath is one pull request's own route.
func (l *lane) prPath(n int) string {
	if l.product == spec.GitLab {
		return l.repoPath("/merge_requests/" + strconv.Itoa(n))
	}
	return l.repoPath("/pulls/" + strconv.Itoa(n))
}

// closePR closes one pull request.
func (l *lane) closePR(ctx context.Context, n int) error {
	if l.product == spec.GitLab {
		_, err := l.call(ctx, http.MethodPut, l.prPath(n), map[string]string{"state_event": "close"}, nil)
		return err
	}
	_, err := l.call(ctx, http.MethodPatch, l.prPath(n), map[string]string{"state": "closed"}, nil)
	return err
}

// closePRIfOpen closes one pull request where it is still open, so a removal after
// a merge or a close reads the state rather than refusing a request the product
// answers with an error for a merged pull request.
func (l *lane) closePRIfOpen(ctx context.Context, n int) error {
	var pr struct {
		State string `json:"state"`
	}
	if _, err := l.call(ctx, http.MethodGet, l.prPath(n), nil, &pr); err != nil {
		return err
	}
	if pr.State != "open" && pr.State != "opened" {
		return nil
	}
	return l.closePR(ctx, n)
}

// issuePath is one issue's own route.
func (l *lane) issuePath(n int) string { return l.repoPath("/issues/" + strconv.Itoa(n)) }

// openIssue opens an issue.
func (l *lane) openIssue(ctx context.Context, title string) (int, error) {
	field := "body"
	if l.product == spec.GitLab {
		field = "description"
	}
	var got laneNumber
	_, err := l.call(ctx, http.MethodPost, l.repoPath("/issues"), map[string]string{"title": title, field: issueBody}, &got)
	if err == nil && got.of(l.product) <= 0 {
		err = errors.New("opening an issue answered no number")
	}
	return got.of(l.product), err
}

// closeIssue closes one issue. Closing a closed issue is an answer on every
// family, so this removal needs no read first.
func (l *lane) closeIssue(ctx context.Context, n int) error {
	if l.product == spec.GitLab {
		_, err := l.call(ctx, http.MethodPut, l.issuePath(n), map[string]string{"state_event": "close"}, nil)
		return err
	}
	_, err := l.call(ctx, http.MethodPatch, l.issuePath(n), map[string]string{"state": "closed"}, nil)
	return err
}

// The statuses a removal reads as "already gone": GitHub answers a missing ref with
// 422 as well as 404.
var (
	goneGitHub = []int{http.StatusNotFound, http.StatusUnprocessableEntity}
	gone       = []int{http.StatusNotFound}
)

// deleteBranch removes one branch.
func (l *lane) deleteBranch(ctx context.Context, name string) error {
	var err error
	switch l.product {
	case spec.GitHub:
		_, err = l.call(ctx, http.MethodDelete, l.repoPath("/git/refs/heads/"+name), nil, nil, goneGitHub...)
	case spec.GitLab:
		_, err = l.call(ctx, http.MethodDelete, l.repoPath("/repository/branches/"+url.PathEscape(name)), nil, nil, gone...)
	default:
		_, err = l.call(ctx, http.MethodDelete, l.repoPath("/branches/"+name), nil, nil, gone...)
	}
	return err
}

// deleteTag removes one tag.
func (l *lane) deleteTag(ctx context.Context, name string) error {
	var err error
	switch l.product {
	case spec.GitHub:
		_, err = l.call(ctx, http.MethodDelete, l.repoPath("/git/refs/tags/"+name), nil, nil, goneGitHub...)
	case spec.GitLab:
		_, err = l.call(ctx, http.MethodDelete, l.repoPath("/repository/tags/"+url.PathEscape(name)), nil, nil, gone...)
	default:
		_, err = l.call(ctx, http.MethodDelete, l.repoPath("/tags/"+name), nil, nil, gone...)
	}
	return err
}

// laneRelease is one release as the listing reads it: the tag it is named by
// everywhere, and the id GitHub and the Gitea family delete it by.
type laneRelease struct {
	tag string
	id  int64
}

// deleteRelease removes one release, leaving its tag, which deleteTag removes.
func (l *lane) deleteRelease(ctx context.Context, r laneRelease) error {
	var err error
	if l.product == spec.GitLab {
		_, err = l.call(ctx, http.MethodDelete, l.repoPath("/releases/"+url.PathEscape(r.tag)), nil, nil, gone...)
	} else {
		_, err = l.call(ctx, http.MethodDelete, l.repoPath("/releases/"+strconv.FormatInt(r.id, 10)), nil, nil, gone...)
	}
	return err
}

// removeRelease removes the release named by one tag, then the tag, in that order:
// the Gitea family refuses to delete a tag a release still names. The release is read
// by its tag rather than found in the listing, because GitHub's listing omits a
// release for a second or more after its creation answered, and the read by tag finds
// it at once. GitLab deletes a release by its tag, so it needs no read.
func (l *lane) removeRelease(ctx context.Context, tag string) error {
	r := laneRelease{tag: tag}
	if l.product != spec.GitLab {
		var row laneRow
		status, err := l.call(ctx, http.MethodGet, l.repoPath("/releases/tags/"+url.PathEscape(tag)), nil, &row, gone...)
		if err != nil {
			return err
		}
		if status == http.StatusNotFound {
			return l.deleteTag(ctx, tag)
		}
		if r.id, err = strconv.ParseInt(string(row.ID), 10, 64); err != nil {
			return fmt.Errorf("the release named by %s answered no id: %w", tag, err)
		}
	}
	return errors.Join(l.deleteRelease(ctx, r), l.deleteTag(ctx, tag))
}

// laneItem is one open pull request or issue as the listing reads it.
type laneItem struct {
	title  string
	number int
}

// laneState is a sandbox as the lane lists it: what is open, and every branch, tag
// and release. Closed items and finished CI runs are history a run leaves behind on
// purpose, so the listing does not read them.
type laneState struct {
	pulls    []laneItem
	issues   []laneItem
	branches []string
	tags     []string
	releases []laneRelease
}

// laneRow is the one row shape every listing route is decoded into: each product
// fills the members it has. The id stays raw, because the Gitea family's tag row
// answers its id as the commit's sha where its release row answers a number.
type laneRow struct {
	PullRequest json.RawMessage `json:"pull_request"`
	ID          json.RawMessage `json:"id"`
	Title       string          `json:"title"`
	Name        string          `json:"name"`
	TagName     string          `json:"tag_name"`
	laneNumber
}

// pageQuery is the query asking one product for its largest page, and that size:
// the Gitea family's own maximum is smaller than the others'.
func (l *lane) pageQuery() (string, int) {
	switch l.product {
	case spec.Gitea, spec.Forgejo:
		return "limit=50", 50
	}
	return "per_page=100", 100
}

// rows reads one listing route on one page. A page that comes back full is refused,
// because the lane could not then say it saw everything, and a listing that may have
// missed a row cannot prove the sandbox was left as found.
func (l *lane) rows(ctx context.Context, path string) ([]laneRow, error) {
	query, size := l.pageQuery()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	var rows []laneRow
	if _, err := l.call(ctx, http.MethodGet, path+sep+query, nil, &rows); err != nil {
		return nil, err
	}
	if len(rows) >= size {
		return nil, fmt.Errorf("GET %s answered a full page of %d row(s), so the lane cannot say it saw every row", path, size)
	}
	return rows, nil
}

// state lists the sandbox.
func (l *lane) state(ctx context.Context) (laneState, error) {
	var routes struct{ pulls, issues, branches, tags, releases string }
	switch l.product {
	case spec.GitHub:
		routes.pulls, routes.issues = "/pulls?state=open", "/issues?state=open"
		routes.branches, routes.tags, routes.releases = "/branches", "/tags", "/releases"
	case spec.GitLab:
		routes.pulls, routes.issues = "/merge_requests?state=opened", "/issues?state=opened"
		routes.branches, routes.tags, routes.releases = "/repository/branches", "/repository/tags", "/releases"
	default:
		routes.pulls, routes.issues = "/pulls?state=open", "/issues?state=open&type=issues"
		routes.branches, routes.tags, routes.releases = "/branches", "/tags", "/releases"
	}
	var st laneState
	read := func(route string, keep func(laneRow)) error {
		rows, err := l.rows(ctx, l.repoPath(route))
		for _, r := range rows {
			keep(r)
		}
		return err
	}
	err := errors.Join(
		read(routes.pulls, func(r laneRow) { st.pulls = append(st.pulls, laneItem{title: r.Title, number: r.of(l.product)}) }),
		read(routes.issues, func(r laneRow) {
			// GitHub's issue list carries its pull requests too, each marked by
			// the member naming it one.
			if len(r.PullRequest) == 0 || string(r.PullRequest) == "null" {
				st.issues = append(st.issues, laneItem{title: r.Title, number: r.of(l.product)})
			}
		}),
		read(routes.branches, func(r laneRow) { st.branches = append(st.branches, r.Name) }),
		read(routes.tags, func(r laneRow) { st.tags = append(st.tags, r.Name) }),
		read(routes.releases, func(r laneRow) {
			id, _ := strconv.ParseInt(string(r.ID), 10, 64)
			st.releases = append(st.releases, laneRelease{tag: r.TagName, id: id})
		}),
	)
	return st, err
}

// rendered is the listing as the sets the lane compares, one per kind of object.
func (st laneState) rendered() map[string][]string {
	items := func(list []laneItem) []string {
		out := make([]string, 0, len(list))
		for _, it := range list {
			out = append(out, strconv.Itoa(it.number)+" "+it.title)
		}
		return out
	}
	releases := make([]string, 0, len(st.releases))
	for _, r := range st.releases {
		releases = append(releases, r.tag)
	}
	return map[string][]string{
		"open pull requests": items(st.pulls),
		"open issues":        items(st.issues),
		"branches":           st.branches,
		"tags":               st.tags,
		"releases":           releases,
	}
}

// laneDiff names every object one listing holds and the other does not, by kind,
// and is empty exactly when the two listings agree.
func laneDiff(before, after laneState) []string {
	b, a := before.rendered(), after.rendered()
	var out []string
	for _, kind := range []string{"open pull requests", "open issues", "branches", "tags", "releases"} {
		for _, v := range a[kind] {
			if !slices.Contains(b[kind], v) {
				out = append(out, fmt.Sprintf("%s: %q appeared", kind, v))
			}
		}
		for _, v := range b[kind] {
			if !slices.Contains(a[kind], v) {
				out = append(out, fmt.Sprintf("%s: %q disappeared", kind, v))
			}
		}
	}
	return out
}

// sweep removes what an earlier run of the lane left under its marks: it closes the
// open pull requests and issues whose titles carry one and deletes the releases,
// tags and branches whose names do, in that order, so no branch is deleted under a
// pull request still open on it and no tag under a release still naming it.
func (l *lane) sweep(ctx context.Context) error {
	st, err := l.state(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, it := range st.pulls {
		if laneOwned(it.title) {
			errs = append(errs, l.closePR(ctx, it.number))
		}
	}
	for _, it := range st.issues {
		if laneOwned(it.title) {
			errs = append(errs, l.closeIssue(ctx, it.number))
		}
	}
	for _, r := range st.releases {
		if laneOwned(r.tag) {
			errs = append(errs, l.deleteRelease(ctx, r))
		}
	}
	for _, tag := range st.tags {
		if laneOwned(tag) {
			errs = append(errs, l.deleteTag(ctx, tag))
		}
	}
	for _, branch := range st.branches {
		if laneOwned(branch) {
			errs = append(errs, l.deleteBranch(ctx, branch))
		}
	}
	return errors.Join(errs...)
}

// laneWait sleeps one poll interval, or answers the context's error first.
func laneWait(ctx context.Context) error {
	timer := time.NewTimer(lanePoll)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// runVerdict is what the sandbox's own CI answered on one commit within a bound.
// refused is a run the instance failed before any job started, which is how
// gitlab.com answers every pipeline of an account whose identity it has not
// verified: there is a failed run and no failed job to re-run.
type runVerdict struct {
	summary  string
	finished bool
	failed   bool
	refused  bool
}

// waitForRuns waits up to bound for every CI run on one commit to finish, and
// reports whether one failed: the re-run needs a finished run with a failed job
// to re-run.
func (l *lane) waitForRuns(ctx context.Context, sha string, bound time.Duration) (runVerdict, error) {
	deadline := time.Now().Add(bound)
	for {
		v, err := l.runs(ctx, sha)
		if err != nil || v.finished || time.Now().After(deadline) {
			return v, err
		}
		if err := laneWait(ctx); err != nil {
			return v, err
		}
	}
}

// laneCIRun is one CI run on a commit as the lane reads it. GitLab spells a
// pipeline's outcome in its status and leaves its start out of the list, so its
// arm reads a failed pipeline once more for the start.
type laneCIRun struct {
	StartedAt  *string `json:"started_at"`
	Status     string  `json:"status"`
	Conclusion string  `json:"conclusion"`
	ID         int64   `json:"id"`
}

// runs reads the CI runs on one commit once.
func (l *lane) runs(ctx context.Context, sha string) (runVerdict, error) {
	var rows []laneCIRun
	var err error
	switch l.product {
	case spec.GitLab:
		rows, err = l.pipelines(ctx, sha)
	case spec.GitHub:
		rows, err = l.actionRuns(ctx, "/actions/runs?per_page=20&head_sha="+url.QueryEscape(sha))
	default:
		rows, err = l.actionRuns(ctx, "/actions/runs?page=1&limit=20&head_sha="+url.QueryEscape(sha))
	}
	if err != nil {
		return runVerdict{}, err
	}
	v := runVerdict{finished: len(rows) > 0, refused: len(rows) > 0}
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		failed := r.Conclusion == "failure" || r.Conclusion == "failed"
		v.finished = v.finished && r.Status == "completed"
		v.failed = v.failed || failed
		v.refused = v.refused && failed && r.StartedAt == nil && l.product == spec.GitLab
		parts = append(parts, r.Status+"/"+r.Conclusion)
	}
	v.summary = fmt.Sprintf("%d run(s) [%s]", len(rows), strings.Join(parts, ", "))
	return v, nil
}

// actionRuns reads one page of the Actions runs route of GitHub or the Gitea family.
func (l *lane) actionRuns(ctx context.Context, route string) ([]laneCIRun, error) {
	var page struct {
		Runs []laneCIRun `json:"workflow_runs"`
	}
	_, err := l.call(ctx, http.MethodGet, l.repoPath(route), nil, &page)
	return page.Runs, err
}

// pipelines reads GitLab's pipelines on one commit, each finished one with its
// outcome moved into the conclusion and each failed one with its start.
func (l *lane) pipelines(ctx context.Context, sha string) ([]laneCIRun, error) {
	var rows []laneCIRun
	if _, err := l.call(ctx, http.MethodGet, l.repoPath("/pipelines?per_page=20&sha="+url.QueryEscape(sha)), nil, &rows); err != nil {
		return nil, err
	}
	for i := range rows {
		switch rows[i].Status {
		case "success", "failed", "canceled", "skipped":
			rows[i].Conclusion, rows[i].Status = rows[i].Status, "completed"
		}
		if rows[i].Conclusion != "failed" {
			continue
		}
		var full laneCIRun
		if _, err := l.call(ctx, http.MethodGet, l.repoPath("/pipelines/"+strconv.FormatInt(rows[i].ID, 10)), nil, &full); err != nil {
			return nil, err
		}
		rows[i].StartedAt = full.StartedAt
	}
	return rows, nil
}

// awaitMerged waits a bounded time for one pull request to read merged. A merge the
// product accepted runs in the background and writes its base when it lands, so a
// base removed before then is written back and the run leaves a branch behind.
func (l *lane) awaitMerged(ctx context.Context, n int) error {
	deadline := time.Now().Add(laneMergeableBound)
	for {
		var pr struct {
			State  string `json:"state"`
			Merged bool   `json:"merged"`
		}
		if _, err := l.call(ctx, http.MethodGet, l.prPath(n), nil, &pr); err != nil {
			return err
		}
		if pr.Merged || pr.State == "merged" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the merge the product accepted on pull request %d had not landed within %s", n, laneMergeableBound)
		}
		if err := laneWait(ctx); err != nil {
			return err
		}
	}
}

// settleMergeable waits a bounded time for the product to compute whether one pull
// request can merge, which every family computes after the opening answer. It reports
// nothing: a pull request that has not settled by the bound is handed to the case,
// whose own answer then says what the product made of it.
//
// On the Gitea family a mergeable read settles nothing alone: the instance tests a pull
// request as it is created and again for every push to its base it processes later,
// queueing the pull request unmergeable meanwhile, and the lane creates the base
// branch moments before the pull request. So the flag has to hold on two reads a lane
// poll apart, which gives the instance a poll to process that push and queue its test.
func (l *lane) settleMergeable(ctx context.Context, n int) {
	deadline := time.Now().Add(laneMergeableBound)
	held := false
	for time.Now().Before(deadline) {
		var pr struct {
			Mergeable           *bool  `json:"mergeable"`
			DetailedMergeStatus string `json:"detailed_merge_status"`
		}
		if _, err := l.call(ctx, http.MethodGet, l.prPath(n), nil, &pr); err != nil {
			return
		}
		switch l.product {
		case spec.GitHub:
			if pr.Mergeable != nil {
				return
			}
		case spec.GitLab:
			switch pr.DetailedMergeStatus {
			case "", "unchecked", "checking", "preparing", "approvals_syncing":
			default:
				return
			}
		default:
			mergeable := pr.Mergeable != nil && *pr.Mergeable
			if mergeable && held {
				return
			}
			held = mergeable
		}
		if laneWait(ctx) != nil {
			return
		}
	}
}

// later registers one removal to run after the test, under a context of its own:
// the test's context is cancelled before its cleanups run. A removal that fails is a
// failure of the case, because the lane's closing listing then differs from its
// opening one.
func (l *lane) later(t *testing.T, what string, remove func(context.Context) error) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), laneCleanupBound)
		defer cancel()
		if err := remove(ctx); err != nil {
			t.Errorf("Cleanup: %s in %s on %s: %v", what, l.repo, l.product, err)
		}
	})
}

// bracketSandbox opens the lane on one product's sandbox where the live lane may
// write there: it removes what an aborted run left under the lane's marks, lists the
// sandbox, and registers the listing that must match it once every case has run.
// A sandbox the lane cannot list is one it cannot promise to leave as found, so a
// failure here stops the run before any case writes.
//
// The opening listing is taken after that removal, so everything it holds is found
// state: the sandbox's own branches and its permanent read subject, an open pull
// request from a branch named seed and an open issue, none of which carries a lane
// mark. No case writes to them and the sweep never removes them, so the closing
// listing must hold them still open, and a run that closed or deleted one fails
// naming it.
func bracketSandbox(t *testing.T, p spec.Product) {
	t.Helper()
	l, ok := sandboxLane(p)
	if !ok {
		return
	}
	if err := l.sweep(t.Context()); err != nil {
		t.Fatalf("Setup: removing what an earlier run left under the lane's marks in %s on %s: %v", l.repo, p, err)
	}
	before, err := l.state(t.Context())
	if err != nil {
		t.Fatalf("Setup: listing the sandbox %s on %s: %v", l.repo, p, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), laneCleanupBound)
		defer cancel()
		after, err := l.restore(ctx, before)
		if err != nil {
			t.Errorf("the run left the sandbox %s on %s other than it found it: %v", l.repo, p, err)
			return
		}
		r := after.rendered()
		t.Logf("the run left the sandbox %s on %s as it found it: %d open pull request(s), %d open issue(s), %d branch(es), %d tag(s), %d release(s)",
			l.repo, p, len(r["open pull requests"]), len(r["open issues"]), len(r["branches"]), len(r["tags"]), len(r["releases"]))
	})
}

// restore holds the sandbox to the listing a run opened on, and returns it to that
// listing where the run left it otherwise. A write the instance committed while its
// answer was lost registers no removal of its own, yet the object it made carries
// the lane's marks, so the sweep that opens a run removes it here too, within the
// run that made it. The run still fails then, naming what it left, and fails with
// what the sweep could not remove where the sandbox still differs after it.
func (l *lane) restore(ctx context.Context, before laneState) (laneState, error) {
	after, err := l.state(ctx)
	if err != nil {
		return after, fmt.Errorf("listing the sandbox after the run: %w", err)
	}
	left := laneDiff(before, after)
	if len(left) == 0 {
		return after, nil
	}
	failure := fmt.Errorf("it left %s", strings.Join(left, "; "))
	if err := l.sweep(ctx); err != nil {
		return after, errors.Join(failure, fmt.Errorf("removing what it left under the lane's marks: %w", err))
	}
	swept, err := l.state(ctx)
	if err != nil {
		return after, errors.Join(failure, fmt.Errorf("listing the sandbox after removing what it left: %w", err))
	}
	if still := laneDiff(before, swept); len(still) > 0 {
		return swept, errors.Join(failure, fmt.Errorf("and after removing what carried the lane's marks it still differs: %s", strings.Join(still, "; ")))
	}
	return swept, errors.Join(failure, errors.New("what carried the lane's marks was removed, so the sandbox is as the run found it"))
}
