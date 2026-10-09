package github

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// versionsPath is where this product publishes the REST API versions an instance
// serves, which is the one-time fact the version pin turns on.
const versionsPath = "/versions"

// ConnectionCaps implements [forgeapi.Capabilities].
//
// It is connection setup, and what it costs is one request or two: the version set
// this instance serves is read here, because which REST API version this connection
// runs under is a one-time fact about the instance and this is the row that prices
// it, and the family is established from a header this product sends on every
// response including a refusal, so a connection that has already sent anything needs
// no request for that half and one that has not reads the metadata endpoint to get
// it. Every answer is held with the client, so every later call costs nothing.
//
// The one capability this scope carries is the re-run of failed checks, and it is
// UNKNOWN on this product by measurement rather than by omission: the workflow runner
// is enabled per REPOSITORY rather than per instance or per version, no
// connection-scope read names it, the read that answers it for one repository needs
// admin, and a workflow-runs read answers 200 whether the runner is enabled or merely
// unused. So the verdict is unknown with its evidence saying why, which renders a
// control with its reason, and what refuses a re-run in practice is the instance's own
// answer to it.
//
// A metadata read the credential cannot make is not a failure here, and that is a
// decision rather than a swallow: the header still names the product, so the family
// IS established, and what could not be read was a version this scope's verdict does
// not turn on. A response carrying no such header and no prior witness is REFUSED
// with [forgeapi.CodeFamilyUndetected], because every route, enumeration table and
// error mapping below this point is one family's.
func (c *Client) ConnectionCaps(ctx context.Context) (forgeapi.ConnectionCaps, error) {
	const op = "ConnectionCaps"
	ctx = transport.Call(ctx, op)
	if caps := c.cachedCaps(); caps != nil {
		return *caps, nil
	}
	if c.sawWitness() {
		c.askVersions(ctx, op)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return forgeapi.ConnectionCaps{}, ctxErr
		}
		return c.holdCaps(rerunEvidence(forgeapi.EvidenceResponseHeader,
			"a response on this connection carried "+headerRequestID+", which this product sends on every response including a refusal")), nil
	}
	var meta restMeta
	_, err := c.readJSON(ctx, op, "/meta", nil, &meta)
	if sentinel := transport.ContextSentinel(err); sentinel != nil {
		return forgeapi.ConnectionCaps{}, sentinel
	}
	if !c.sawWitness() {
		return forgeapi.ConnectionCaps{}, c.undetected(ctx, op, err)
	}
	c.askVersions(ctx, op)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return forgeapi.ConnectionCaps{}, ctxErr
	}
	source, detail := forgeapi.EvidenceMetadata, "the metadata endpoint answered for "+productName(meta.InstalledVersion)
	if err != nil {
		var fe *forgeapi.Error
		source, detail = forgeapi.EvidenceResponseHeader, "the metadata endpoint refused this credential, and "+headerRequestID+" established the family"
		if asForgeError(err, &fe) {
			detail = "the metadata endpoint answered " + http.StatusText(fe.Status) + ", and " + headerRequestID + " established the family"
		}
	}
	return c.holdCaps(rerunEvidence(source, detail)), nil
}

// askVersions resolves whether this instance serves the version this family pins,
// which decides whether every later REST request of this connection carries the pin,
// and it is asked on the connection whose family has just been established, again
// only where the caller's context ended inside it.
//
// A read the credential cannot make, a route an instance does not serve and a set the
// pinned version is absent from all leave the pin OFF, which is why none of them is a
// failure of connection setup: the instance then answers under its own default, and
// that is the arm an appliance below the pinned version takes.
func (c *Client) askVersions(ctx context.Context, op string) {
	var served []string
	if _, err := c.readJSON(ctx, op, versionsPath, nil, &served); err != nil {
		return
	}
	c.holdPin(slices.Contains(served, APIVersion))
}

// holdCaps records this connection's one-time answers and returns them.
func (c *Client) holdCaps(ev forgeapi.Evidence) forgeapi.ConnectionCaps {
	caps := forgeapi.ConnectionCaps{
		Caps: map[forgeapi.Capability]forgeapi.Support{forgeapi.CapRerunChecks: forgeapi.SupportUnknown},
		Ev:   map[forgeapi.Capability]forgeapi.Evidence{forgeapi.CapRerunChecks: ev},
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.caps = &caps
	return caps
}

// rerunEvidence is the evidence behind the re-run verdict: where the family was
// established, and why the verdict itself is unknown at this scope.
func rerunEvidence(source forgeapi.EvidenceSource, detail string) forgeapi.Evidence {
	return forgeapi.Evidence{
		Source: source,
		Detail: detail + "; the workflow runner is enabled per repository on this product, so no connection-scope read answers this capability and the per-repository read that would needs admin",
	}
}

// productName is which product of this family answered, which the metadata endpoint's
// version member is what says: an appliance reports its installed version and the
// hosted instance carries no such key.
func productName(version string) string {
	if version == "" {
		return "the hosted instance, which reports no installed version"
	}
	return "an appliance at " + version
}

// undetected is the refusal a connection whose family cannot be established answers.
// It carries the real status where a request was made and answered, because the
// request WAS made and identified no family, which is what separates this code from
// the refusals reached before anything was sent.
func (c *Client) undetected(ctx context.Context, op string, err error) *forgeapi.Error {
	status := http.StatusOK
	var fe *forgeapi.Error
	if asForgeError(err, &fe) {
		status = fe.Status
	}
	return c.core.Fail(ctx, op, transport.REST(http.MethodGet), forgeapi.CodeFamilyUndetected, forgeapi.KindUnknown, status,
		"no witness of this family on the instance: no response has carried the "+headerRequestID+
			" header, which this product sends on every response including a refusal")
}

func (c *Client) cachedCaps() *forgeapi.ConnectionCaps {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps
}

// GrantCaps implements [forgeapi.Capabilities].
//
// It costs AT MOST one request, the identity read whose response header carries what
// the credential is trusted with, and nothing on a connection that has already made
// that read.
//
// What it publishes is whether the merge STATE is readable, and on this product that
// is yes whatever the grant, measured: one document answered the merge state beside a
// viewer permission of READ and a viewer that could not update the pull request. So
// routing it through an unknown would render a disabled control with a reason that is
// not true, and the grant-scoped unknown belongs to the family whose recheck needs a
// project role. What the scope header does add is the EVIDENCE, which names the scopes
// the credential actually holds, and a fine-grained token reports an empty set there,
// which is why the detail says what was read rather than inferring from it.
func (c *Client) GrantCaps(ctx context.Context) (forgeapi.GrantCaps, error) {
	const op = "GrantCaps"
	ctx = transport.Call(ctx, op)
	if grant := c.cachedGrant(); grant != nil {
		return *grant, nil
	}
	_, scopes, err := c.readUser(ctx, op)
	if err != nil {
		return forgeapi.GrantCaps{}, err
	}
	return c.heldGrant(scopes), nil
}

// heldGrant records the grant verdict and its evidence and returns it.
func (c *Client) heldGrant(scopes []string) forgeapi.GrantCaps {
	detail := "the identity read's scope header carries " + scopeList(scopes) +
		", and the merge state this capability names is readable on this product whatever the grant"
	grant := forgeapi.GrantCaps{
		Caps: map[forgeapi.Capability]forgeapi.Support{forgeapi.CapReadMergeState: forgeapi.SupportYes},
		Ev: map[forgeapi.Capability]forgeapi.Evidence{
			forgeapi.CapReadMergeState: {Source: forgeapi.EvidenceResponseHeader, Detail: detail},
		},
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.grant = &grant
	return grant
}

// scopeList is the scope set as the evidence detail spells it, naming the empty case
// rather than rendering nothing: a fine-grained token is documented to report no
// scopes at all, so an empty list is a fact about the credential kind.
func scopeList(scopes []string) string {
	if len(scopes) == 0 {
		return "no scopes, which is what a fine-grained credential reports"
	}
	return strings.Join(scopes, ", ")
}

func (c *Client) cachedGrant() *forgeapi.GrantCaps {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.grant
}

// readUser is the identity read two operations share: the account body and the scopes
// its response header carries.
//
// The scopes are a HEADER on this product rather than a body field, which was measured
// on a classic credential, so a grant capability here rests on evidence rather than on
// inference. The account body's own email is the PUBLIC profile one and is null where
// the profile carries none.
func (c *Client) readUser(ctx context.Context, op string) (restUser, []string, error) {
	var user restUser
	resp, err := c.readJSON(ctx, op, "/user", nil, &user)
	if err != nil {
		return restUser{}, nil, err
	}
	return user, scopesOf(resp.Header), nil
}

// scopesOf reads the credential's own scopes off the response header that carries
// them, which is a comma-separated list this product writes on every authenticated
// response.
func scopesOf(header http.Header) []string {
	raw := strings.TrimSpace(header.Get("X-OAuth-Scopes"))
	if raw == "" {
		return nil
	}
	var out []string
	for field := range strings.SplitSeq(raw, ",") {
		if scope := strings.TrimSpace(field); scope != "" {
			out = append(out, scope)
		}
	}
	return out
}

// RepoAffordances implements [forgeapi.Capabilities].
//
// The repository's own authenticated record is what fills these values wherever it
// answers, which is the second-cheapest evidence source and the one that needs no
// probe. It is read on every call rather than held, because a repository cache would
// be a fourth kind of per-connection state and the closed list has three.
//
// The measurement found that it answers [forgeapi.RepoAffordances.MergeTrain] nowhere:
// a merge queue is a ruleset property on this product, so the record carries no field
// for it on either route, and the only read that would answer costs one request per
// ruleset on top. Until the budget prices that, this value is
// [forgeapi.SupportUnknown] with default evidence. The same measurement found the
// listing row carries none of the merge-strategy flags either, so a repository reached
// through a listing has an empty
// [forgeapi.RepoAffordances.MergeStrategies] with its evidence saying why, and a
// caller that needs the strategies pays this read.
func (c *Client) RepoAffordances(ctx context.Context, repo forgeapi.RepoRef) (forgeapi.RepoAffordances, error) {
	const op = "RepoAffordances"
	ctx = transport.Call(ctx, op)
	if err := checkRepo(repo); err != nil {
		return forgeapi.RepoAffordances{}, err
	}
	var record restRepo
	if err := c.readRepoJSON(ctx, op, repoRoute(repo, ""), nil, &record); err != nil {
		return forgeapi.RepoAffordances{}, err
	}
	return normalizeAffordances(&record), nil
}
