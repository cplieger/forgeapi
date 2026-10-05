package gitea

import (
	"context"
	"net/http"
	"strings"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// The evidence order on this family runs version body, then swagger, then the
// default. Neither product sends a version response header, measured on both, so
// the cheapest evidence here is the version BODY, whose Gitea substring separates
// the two products; the header order above it matters only if a future release
// restores one.
const (
	// forgejoMarker is the substring in the version body that separates the two
	// products this family serves: the one that is not Gitea reports the Gitea
	// version it is compatible with alongside its own.
	forgejoMarker = "+gitea-"

	// rerunVerb is the swagger path fragment that settles whether this instance
	// carries the re-run of failed checks at all.
	rerunVerb = "rerun-failed-jobs"

	// The two product names the API document titles itself with, measured as
	// "Gitea API" on gitea.com and "Forgejo API" on codeberg.org. The document is
	// a witness of the FAMILY only where it names one of them: a document is a
	// thing any service can serve, and its presence alone identifies nobody.
	titleGitea   = "Gitea"
	titleForgejo = "Forgejo"

	// The two headers a product of this family would name itself with, in the
	// precedence order the evidence rule states. Neither product sends one today,
	// measured on both, which is why the version body is this family's evidence
	// source; they are read because a release that restored one would be the
	// cheapest witness there is.
	headerForgejoVersion = "X-Forgejo-Version"
	headerGiteaVersion   = "X-Gitea-Version"

	// releaseComponents is how many dot-separated numeric components both
	// products open their version body with, measured on gitea.com and
	// codeberg.org: 1.27.0+dev-954-g1f3981a301 and
	// 16.0.0-dev-753-6bcc6da0+gitea-1.22.0.
	releaseComponents = 3
)

// swaggerDoc is the part of the instance's swagger document this family reads: the
// paths, so a capability nothing cheaper answered can be settled by whether the
// verb exists, and the title, which is the product mark detection reads it for.
// Everything else in the document is skipped, which is what keeps a near-megabyte
// read bounded.
type swaggerDoc struct {
	Paths map[string]map[string]struct {
		OperationID string `json:"operationId"`
	} `json:"paths"`
	Info struct {
		Title string `json:"title"`
	} `json:"info"`
}

// ConnectionCaps implements [forgeapi.Capabilities].
func (c *Client) ConnectionCaps(ctx context.Context) (forgeapi.ConnectionCaps, error) {
	const op = "ConnectionCaps"
	ctx = c.core.Call(ctx, op)
	if caps := c.cachedCaps(); caps != nil {
		return *caps, nil
	}
	// One owner per connection for the whole resolution, held across the reads
	// rather than around the cache checks: the swagger document is near a
	// megabyte and its published price is one fetch per connection, which two
	// concurrent first callers would otherwise double.
	if err := c.takeResolve(ctx); err != nil {
		return forgeapi.ConnectionCaps{}, err
	}
	defer c.giveResolve()
	if caps := c.cachedCaps(); caps != nil {
		return *caps, nil
	}
	if err := c.resolveVersion(ctx); err != nil {
		return forgeapi.ConnectionCaps{}, err
	}
	swagger, err := c.resolveSwagger(ctx)
	if err != nil {
		return forgeapi.ConnectionCaps{}, err
	}
	// The family is established before any capability state is cached, because
	// every route, enumeration table and error mapping below this point is per
	// family: a server this one cannot identify must not be served as if it had
	// been.
	if err := c.establishFamily(ctx, swagger); err != nil {
		return forgeapi.ConnectionCaps{}, err
	}
	// The instance's stated maximum page size is the last of setup's reads, and setup
	// does not fail on its answer: only a page-numbered list needs the fact, and a list
	// on a connection holding none reads it again before its first page, so a refusal
	// or a malformed answer here leaves the connection holding nothing. The caller's
	// context ending in it still ends setup with its sentinel.
	_, _ = c.resolveMaximum(ctx)
	if err := ctx.Err(); err != nil {
		return forgeapi.ConnectionCaps{}, err
	}
	rerun, ev := rerunCapability(swagger)
	caps := forgeapi.ConnectionCaps{
		Caps: map[forgeapi.Capability]forgeapi.Support{forgeapi.CapRerunChecks: rerun},
		Ev:   map[forgeapi.Capability]forgeapi.Evidence{forgeapi.CapRerunChecks: ev},
	}
	c.mu.Lock()
	c.caps = &caps
	c.mu.Unlock()
	return caps, nil
}

// rerunCapability is the verdict and the evidence behind it. An absent document is
// unknown rather than an error: an administrator can switch the document off on a
// working instance, and on one of the two products can switch the whole API off,
// which is unknown with evidence rather than a capability read as no.
func rerunCapability(doc *swaggerDoc) (forgeapi.Support, forgeapi.Evidence) {
	if doc == nil {
		return forgeapi.SupportUnknown, forgeapi.Evidence{
			Source: forgeapi.EvidenceSwagger,
			Detail: "no swagger document on this instance",
		}
	}
	for path := range doc.Paths {
		if strings.Contains(path, rerunVerb) {
			return forgeapi.SupportYes, forgeapi.Evidence{
				Source: forgeapi.EvidenceSwagger,
				Detail: "swagger declares " + rerunVerb,
			}
		}
	}
	return forgeapi.SupportNo, forgeapi.Evidence{
		Source: forgeapi.EvidenceSwagger,
		Detail: "swagger declares no " + rerunVerb + " verb",
	}
}

func (c *Client) cachedCaps() *forgeapi.ConnectionCaps {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps
}

// resolveVersion reads the version body once per connection and holds it with the
// client. It is what identifies the product, so every capability that turns on
// which of the two answered reads it rather than repeating the request.
//
// What it accepts is the FORM both products report, three dot-separated numeric
// components and whatever each appends to them, because a bare string is no witness
// at all: any service answering this route with a version key would otherwise be
// served as this family, and the routes, tables and mappings selected from here on
// are one family's. It also records the version header, which neither product sends
// today and either could restore, and whether the Gitea marker inside the body names
// the other product.
func (c *Client) resolveVersion(ctx context.Context) error {
	const op = "ConnectionCaps"
	c.mu.Lock()
	known := c.version != ""
	c.mu.Unlock()
	if known {
		return nil
	}
	resp, err := c.core.Do(ctx, &transport.Request{
		Op: op, Method: http.MethodGet, Path: "/version",
	})
	if err != nil {
		return err
	}
	var body wireVersion
	if err := transport.Decode(resp.Body, &body); err != nil {
		return c.core.FailBody(ctx, op, transport.REST(http.MethodGet), resp.Status)
	}
	version := strings.TrimSpace(body.Version)
	if !numericRelease(version) {
		return c.undetected(ctx, op, resp.Status,
			"the version body carries no release this family's products report")
	}
	c.mu.Lock()
	c.version = version
	c.forgejo = strings.Contains(version, forgejoMarker)
	c.namedBy = namingHeader(resp.Header)
	c.versionStatus = resp.Status
	c.mu.Unlock()
	return nil
}

// namingHeader is the version header a product of this family named itself with,
// empty where the response carried neither, which is what both products do today.
func namingHeader(header http.Header) string {
	for _, name := range []string{headerForgejoVersion, headerGiteaVersion} {
		if value := header.Get(name); value != "" {
			return name
		}
	}
	return ""
}

// numericRelease reports whether a version body opens with the release form both
// products report. It is the first of the two witnesses detection needs.
func numericRelease(version string) bool {
	rest := version
	for i := range releaseComponents {
		digits := 0
		for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
			digits++
		}
		if digits == 0 {
			return false
		}
		rest = rest[digits:]
		if i == releaseComponents-1 {
			return true
		}
		if !strings.HasPrefix(rest, ".") {
			return false
		}
		rest = rest[1:]
	}
	return true
}

// establishFamily is the second witness, taken after the version body's form is the
// first: the Gitea marker inside the body, which names the product that is not Gitea;
// a version header, which neither product sends today and either could restore; or
// the API document's own TITLE, which names the product on both.
//
// Each of the three names a product, and that is what makes it a witness. A release
// number does not, because any service can answer one, and neither does a document
// any service can serve: two observations of which neither identifies anybody are
// still no identification, so the document counts only where its title says whose it
// is. A connection with none of the three is REFUSED rather than served.
//
// Accepted cost, stated because it is a real loss: a Gitea-family instance whose API
// document is switched off, or whose document titles itself something else, carries
// no product mark on these two reads and is reported undetected rather than taken on
// the version string alone.
func (c *Client) establishFamily(ctx context.Context, doc *swaggerDoc) error {
	c.mu.Lock()
	forgejo, namedBy, status := c.forgejo, c.namedBy, c.versionStatus
	c.mu.Unlock()
	if forgejo || namedBy != "" || productNamed(doc) {
		return nil
	}
	return c.undetected(ctx, "ConnectionCaps", status,
		"no second witness of this family on the instance: the version body carries neither product's own marker, no version header names one, and no API document of this family's own names the product")
}

// productNamed reports whether an API document titles itself with one of the two
// products this family serves, which is the only thing in it detection reads as a
// mark. An absent document names nobody, and so does one titled for something else.
func productNamed(doc *swaggerDoc) bool {
	if doc == nil {
		return false
	}
	title := doc.Info.Title
	return strings.Contains(title, titleGitea) || strings.Contains(title, titleForgejo)
}

// undetected is the refusal a connection whose family cannot be established answers.
// It carries the real status, because the request WAS made and identified no family,
// which is what separates this code from the refusals reached before anything was
// sent.
func (c *Client) undetected(ctx context.Context, op string, status int, message string) *forgeapi.Error {
	return c.core.Fail(ctx, op, transport.REST(http.MethodGet), forgeapi.CodeFamilyUndetected, forgeapi.KindUnknown, status, message)
}

// resolveSwagger fetches the document once per connection and caches it, never per
// call and never per repository: a repeated read of a document this size would
// dominate every other figure in the budget. Its absence is unknown rather than an
// error.
func (c *Client) resolveSwagger(ctx context.Context) (*swaggerDoc, error) {
	c.mu.Lock()
	cached := c.swagger
	c.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	target := strings.TrimSuffix(c.core.WebBase().String(), "/") + "/swagger.v1.json"
	resp, err := c.core.Do(ctx, &transport.Request{
		Op: "ConnectionCaps", Method: http.MethodGet, AbsoluteURL: target,
		MaxBytes: maxSwaggerBytes,
	})
	if err != nil {
		var fe *forgeapi.Error
		if ok := asForgeError(err, &fe); ok && fe.Kind == forgeapi.KindNotFound {
			return nil, nil
		}
		return nil, err
	}
	var doc swaggerDoc
	if err := transport.Decode(resp.Body, &doc); err != nil {
		return nil, nil
	}
	c.mu.Lock()
	c.swagger = &doc
	c.mu.Unlock()
	return &doc, nil
}

// maxSwaggerBytes bounds the one document that is legitimately large: the two
// products' documents measure near a megabyte, so a bound below that would refuse
// a working instance.
const maxSwaggerBytes = 4 << 20

// settingsPath is the route both products state their API settings on, the most
// rows one page of a list serves among them.
const settingsPath = "/settings/api"

// statedMaximum is the most rows one page of a list serves on this instance, read
// once per connection and held with the client.
//
// Both products cap a page at the administrator's max_response_items whatever limit
// is asked for, so a page short of a limit the instance never honoured would read as
// the end of the list. A connection holding no maximum, one never set up or one whose
// setup could not read it, reads it here, under the connection's one resolution
// owner, and that read is the connection's discovery rather than the call's. Where it
// still fails, the caller fails with the read's own refusal and sends nothing, since
// no limit it could send has a short page it could read; no default stands in for
// the instance's statement, because an administrator can set the maximum below the
// products' shipped one.
func (c *Client) statedMaximum(ctx context.Context) (int, error) {
	if held := c.heldMaximum(); held > 0 {
		return held, nil
	}
	if err := c.takeResolve(ctx); err != nil {
		return 0, err
	}
	defer c.giveResolve()
	return c.resolveMaximum(ctx)
}

// takeResolve makes the caller the connection's resolution owner, waiting no longer
// than ctx allows, so a call behind a setup read that has not answered ends at its
// own deadline with the context's own error.
func (c *Client) takeResolve(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.resolve <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) giveResolve() { <-c.resolve }

func (c *Client) heldMaximum() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxItems
}

// resolveMaximum reads the settings route where the connection holds no maximum. Its
// caller holds the resolution owner. An answer carrying no positive maximum is
// refused as malformed, because it states nothing a page can be measured against.
func (c *Client) resolveMaximum(ctx context.Context) (int, error) {
	const op = "ConnectionCaps"
	if held := c.heldMaximum(); held > 0 {
		return held, nil
	}
	resp, err := c.core.Do(ctx, &transport.Request{
		Op: op, Method: http.MethodGet, Path: settingsPath,
	})
	if err != nil {
		return 0, err
	}
	var body wireSettings
	if err := transport.Decode(resp.Body, &body); err != nil {
		return 0, c.core.FailBody(ctx, op, transport.REST(http.MethodGet), resp.Status)
	}
	if body.MaxResponseItems <= 0 {
		return 0, c.core.Fail(ctx, op, transport.REST(http.MethodGet), forgeapi.CodeValidation, forgeapi.KindUpstream, resp.Status,
			"the API settings answered no positive max_response_items, which is what says whether a short page is the end of a list")
	}
	c.mu.Lock()
	c.maxItems = body.MaxResponseItems
	c.mu.Unlock()
	return body.MaxResponseItems, nil
}

// pageLimit is the limit one page of a page-numbered list asks for: the smaller of the
// call's page bound and the instance's stated maximum, so a page short of it is the
// end of the list.
func (c *Client) pageLimit(ctx context.Context, bound int) (int, error) {
	most, err := c.statedMaximum(ctx)
	if err != nil {
		return 0, err
	}
	return min(bound, most), nil
}

func asForgeError(err error, out **forgeapi.Error) bool {
	fe, ok := err.(*forgeapi.Error)
	if !ok {
		return false
	}
	*out = fe
	return true
}

// productDetail is the evidence detail for a verdict the product rather than the
// repository settles. It names the version body, which is this family's own
// evidence source, and it carries the Gitea compatibility number for the product
// that reports one: that number rides the evidence rather than a type, because
// publishing a product enumeration would hand back the axis the capability model
// rejects.
func (c *Client) productDetail() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.version == "" {
		return "version body not yet read"
	}
	return "version " + c.version
}

// GrantCaps implements [forgeapi.Capabilities].
//
// It costs no request and carries no evidence object, and both follow from the
// same fact: this product has no grant endpoint, and the one grant-dependent value
// on this surface, the merge state, rides the pull-request record's own mergeable
// flag, which every read of a pull request already carries. So a credential that can read a pull
// request can read its merge state, and there is nothing separate to probe.
//
//nolint:revive // unused-parameter: the context is the published signature's, and this accessor answers from the record every pull-request read already carries, so it performs no I/O
func (c *Client) GrantCaps(ctx context.Context) (forgeapi.GrantCaps, error) {
	return forgeapi.GrantCaps{
		Caps: map[forgeapi.Capability]forgeapi.Support{forgeapi.CapReadMergeState: forgeapi.SupportYes},
	}, nil
}

// RepoAffordances implements [forgeapi.Capabilities].
// [forgeapi.RepoAffordances.MergeTrain] is [forgeapi.SupportNo] on every
// repository of this family, which has neither a merge queue nor a merge train,
// and its evidence source is [forgeapi.EvidenceVersion] rather than the
// response body the other values come from: no field of the repository record
// answers it, and what does is the product the version body identified.
//
// A [forgeapi.RepoAffordances] carries no successor, so a repository that moved
// answers [forgeapi.CodeRepoRefStale] carrying it.
func (c *Client) RepoAffordances(ctx context.Context, repo forgeapi.RepoRef) (forgeapi.RepoAffordances, error) {
	const op = "RepoAffordances"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.RepoAffordances{}, err
	}
	addr := &address{named: repo}
	var record wireRepo
	if err := c.readAt(ctx, op, addr, "", nil, &record); err != nil {
		return forgeapi.RepoAffordances{}, err
	}
	if stale := c.moved(ctx, op, addr); stale != nil {
		return forgeapi.RepoAffordances{}, stale
	}
	return c.normalizeAffordances(&record), nil
}

// capability resolves one connection capability, so an operation gated on it
// refuses from detection rather than from a call that cannot work.
func (c *Client) capability(ctx context.Context, op string, want forgeapi.Capability) error {
	caps, err := c.ConnectionCaps(ctx)
	if err != nil {
		return err
	}
	if caps.Caps[want] == forgeapi.SupportYes {
		return nil
	}
	return c.core.Refuse(ctx, op, want, caps.Caps[want], caps.Ev[want])
}
