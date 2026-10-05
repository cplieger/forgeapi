package gitlab

import (
	"context"
	"net/http"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// headerMeta is the header this product names itself with. It is the family witness
// here, and the reason it can be one is that the NAME is this product's own: a
// release number identifies nobody, and every other service answering a metadata
// route would have to send a header called after this product to be mistaken for it.
//
// Measured on gitlab.com: it rides every response including a 401, which is what lets
// a connection whose credential cannot read the metadata endpoint still establish its
// family.
const headerMeta = "X-Gitlab-Meta"

// ConnectionCaps implements [forgeapi.Capabilities].
//
// It is connection setup, and it is where everything this connection learns once
// about its instance is learned: TWO requests on the first call, the metadata read
// and the one bounded schema question that settles whether this instance accepts the
// documents this family ships, and zero on every later call because both answers are
// held with the client. The family is established from the header the metadata read
// carries rather than from its body, and the capability is answered from the version
// the body reports.
//
// Those two requests are priced HERE rather than on the operations that need what
// they learn. That is what lets every operation publish an exact figure: a merge
// -state read is one request, a pull-request list is one per page, and a re-run costs
// the same on a connection that has just been opened as on one that has been used,
// because no operation resolves a connection property of its own.
//
// A metadata read the credential cannot make is not a failure here, and that is a
// decision rather than a swallow: the header still names the product, so the family
// IS established, and the capability it could not read is reported unknown with the
// status in its evidence. An unknown renders a disabled control with its reason,
// where an error at this accessor would make every other operation unreachable on a
// connection whose public reads work. A response carrying no such header is REFUSED
// with [forgeapi.CodeFamilyUndetected], because every route, enumeration table and
// error mapping below this point is one family's.
func (c *Client) ConnectionCaps(ctx context.Context) (forgeapi.ConnectionCaps, error) {
	const op = "ConnectionCaps"
	ctx = c.core.Call(ctx, op)
	if caps := c.cachedCaps(); caps != nil {
		return *caps, nil
	}
	var meta restMetadata
	header, err := c.readJSON(ctx, op, "/metadata", nil, &meta)
	if sentinel := transport.ContextSentinel(err); sentinel != nil {
		return forgeapi.ConnectionCaps{}, sentinel
	}
	if header.Get(headerMeta) == "" {
		return forgeapi.ConnectionCaps{}, c.undetected(ctx, op, err)
	}
	rerun, ev := rerunCapability(&meta, err)
	caps := forgeapi.ConnectionCaps{
		Caps: map[forgeapi.Capability]forgeapi.Support{forgeapi.CapRerunChecks: rerun},
		Ev:   map[forgeapi.Capability]forgeapi.Evidence{forgeapi.CapRerunChecks: ev},
	}
	// The schema question sits above the cache write: asked once on the connection
	// whose family this read established, again where the caller's context ended
	// inside it, and never on an instance that answered no witness of this family.
	c.askSchema(ctx, op)
	if err := ctx.Err(); err != nil {
		return forgeapi.ConnectionCaps{}, err
	}
	c.mu.Lock()
	c.caps = &caps
	c.mu.Unlock()
	return caps, nil
}

// rerunCapability is the verdict on the re-run of failed checks and the evidence
// behind it.
//
// This product carries the verb on every version in the supported set, so a metadata
// read that succeeded answers yes with the version it read. A read the credential
// could not make answers UNKNOWN with the status, rather than yes on the strength of a
// header: the header establishes the family and says nothing about what the verb needs.
func rerunCapability(meta *restMetadata, err error) (forgeapi.Support, forgeapi.Evidence) {
	if err != nil {
		var fe *forgeapi.Error
		detail := "the metadata endpoint refused this credential"
		if asForgeError(err, &fe) {
			detail = "the metadata endpoint answered " + http.StatusText(fe.Status)
		}
		return forgeapi.SupportUnknown, forgeapi.Evidence{Source: forgeapi.EvidenceMetadata, Detail: detail}
	}
	if meta.Version == "" {
		return forgeapi.SupportUnknown, forgeapi.Evidence{
			Source: forgeapi.EvidenceMetadata,
			Detail: "the metadata endpoint reported no version",
		}
	}
	return forgeapi.SupportYes, forgeapi.Evidence{
		Source: forgeapi.EvidenceMetadata,
		Detail: "gitlab " + meta.Version + ", whose pipeline retry verb is on every version this library covers",
	}
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
		"no witness of this family on the instance: the metadata response carries no "+headerMeta+
			" header, which this product sends on every response including a refusal")
}

func (c *Client) cachedCaps() *forgeapi.ConnectionCaps {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps
}

// GrantCaps implements [forgeapi.Capabilities].
//
// It costs NO request, and that follows from its own signature rather than from a
// choice: it names no repository, so it has no project path and no merge-request
// number to send a document with, and the permissions it reports are selected by
// every document a read of this product already sends. So it answers from what the
// last document reported.
//
// What it publishes is the merge state's FRESHNESS, which on this product is a grant
// fact: the field itself is selected by both documents and readable by any credential
// that can read the merge request, while the recalculation a caller can ask for is
// refusable below a project role. So a credential the instance reports as able to push
// answers yes with the permission pair as its evidence, one it reports otherwise
// answers UNKNOWN with the same pair, and a connection on which no document has run
// answers yes from this product's own default, because reporting unknown there would
// disable a control that works on every instance of it.
//
// Once any request on the connection has been refused with
// [forgeapi.CodeScopeInsufficient], the answer is UNKNOWN with that refusal as its
// evidence, naming the operation and what the credential lacks, whatever pair a
// document reports after it. This product checks a token's own permissions before
// the owner's access, and the pair reports only the owner's, so a credential missing
// a permission reads as able where it is not. The token route that would state the
// permission set is not read: it describes one token rather than the grant, and a
// fine-grained token without the permission to read it is refused there too.
//
//nolint:revive // unused-parameter: the context is the published signature's, and this accessor answers from the document every read of this product already sent, so it performs no I/O
func (c *Client) GrantCaps(ctx context.Context) (forgeapi.GrantCaps, error) {
	support, ev, held := c.heldGrant()
	if !held {
		support = forgeapi.SupportYes
		ev = forgeapi.Evidence{
			Source: forgeapi.EvidenceDefault,
			Detail: "no document has run on this connection yet; both read documents select the merge state, and the recalculation a caller can ask for is refusable by project role",
		}
	}
	return forgeapi.GrantCaps{
		Caps: map[forgeapi.Capability]forgeapi.Support{forgeapi.CapReadMergeState: support},
		Ev:   map[forgeapi.Capability]forgeapi.Evidence{forgeapi.CapReadMergeState: ev},
	}, nil
}

// RepoAffordances implements [forgeapi.Capabilities].
//
// Two of its answers are three-valued for this product's sake: whether issues are
// enabled varies with the caller's permission, and push is reported as a role
// integer, from which no permission and no visibility are not the same answer.
// Whether merging goes through a train is read here from the project's own
// setting, because inferring it from a refusal would report a queue-protected
// merge request as unmergeable.
//
// [forgeapi.RepoAffordances.Ev] is empty, which is the expectation table's own row:
// measured anonymously, this product's project record carries none of the four keys
// the three capabilities would name a source for, so the evidence map would state a
// source for values the record did not supply.
func (c *Client) RepoAffordances(ctx context.Context, repo forgeapi.RepoRef) (forgeapi.RepoAffordances, error) {
	const op = "RepoAffordances"
	ctx = c.core.Call(ctx, op)
	if err := c.checkRepo(repo); err != nil {
		return forgeapi.RepoAffordances{}, err
	}
	var record restProject
	if _, err := c.readJSON(ctx, op, projectRoute(repo, ""), nil, &record); err != nil {
		return forgeapi.RepoAffordances{}, err
	}
	return normalizeAffordances(&record), nil
}

// capability refuses an operation whose capability this connection's setup ruled
// out. It sends NOTHING and resolves nothing: the capabilities are the connection's
// and are read at setup, so an operation reads the verdict rather than buying it,
// which is what makes a re-run cost the same on a fresh connection as on a used one.
//
// A connection whose setup has not run holds no verdict, and the answer there is this
// product's own default rather than a refusal: the pipeline retry verb is on every
// version in the supported set, which is what the metadata arm answers whenever it
// can read one, so refusing here would disable a control that works on every instance
// of this product. That is the reading [Client.GrantCaps] takes for the permission
// pair it reports, for the same reason.
func (c *Client) capability(ctx context.Context, op string, want forgeapi.Capability) error {
	caps := c.cachedCaps()
	if caps == nil || caps.Caps[want] == forgeapi.SupportYes {
		return nil
	}
	return c.core.Refuse(ctx, op, want, caps.Caps[want], caps.Ev[want])
}
