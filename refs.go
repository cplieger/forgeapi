package forgeapi

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// Family is one implementation package. There are three, because Gitea and
// Forgejo share one; the four PRODUCTS an instance can be are a different
// vocabulary, and a family serves one or two of them.
type Family int

// The Family members.
const (
	// FamilyUnknown is the zero value: no family was detected.
	FamilyUnknown Family = iota
	// FamilyGitHub serves github.com and Enterprise Server.
	FamilyGitHub
	// FamilyGitLab serves gitlab.com and self-managed instances.
	FamilyGitLab
	// FamilyGitea serves Gitea and Forgejo, until the first response-shape
	// divergence splits it.
	FamilyGitea
)

// String returns the member's spelling, which is what a log line and a wire
// field carry: "unknown", "github", "gitlab" or "gitea".
func (f Family) String() string {
	return nameOf([]string{nameUnknown, "github", "gitlab", "gitea"}, f)
}

var _ fmt.Stringer = Family(0)

// RepoIDPrefix is the version prefix every derived repository identifier
// carries, so the derivation can change later without a consumer's stored
// identifier becoming ambiguous.
const RepoIDPrefix = "v1."

// RepoRef addresses one repository.
//
// ID is DERIVED from Selector and never allocated, which three consumers force:
// a poller that builds its addressing from a local git remote cannot mint an
// identifier without a round trip it does not make, a notification carries an
// identity across process restarts, and no mapping table has to survive a
// restart. The derivation is [RepoIDPrefix] followed by the hex encoding of the
// ASCII-lowercased canonical selector: an encoding, reversibly, never a digest,
// because resolution has to recover the selector and a digest would need the
// lookup table the scheme exists to avoid. Lowercasing first stops a
// one-character case difference amplifying into two different identifiers for
// one repository, and lower-hex rather than base64url because a consumer's row
// selector matches case-insensitively.
//
// A rename or a transfer changes the selector and therefore the ID: a stale one
// resolves to [CodeRepoRefStale], carrying the successor only where the
// connection could discover it.
type RepoRef struct { //nolint:govet // fieldalignment: field order here is declared order, and it is API for an unkeyed composite literal
	// ID is the wire-facing derived identifier. It is not authorization:
	// every resolution re-checks the connection's grant, and opacity is a
	// consumer convenience.
	ID string
	// Family is the family that owns Selector's spelling. It is not recoverable
	// from ID, which encodes the selector alone.
	Family Family
	// Selector is family-private: the exact string that family's API takes in a
	// path. Two families spell it "owner/repo"; one takes a full namespace path
	// whose separators are encoded once at request time rather than stored
	// pre-encoded.
	Selector string
	// DisplayPath is what a consumer shows a human.
	DisplayPath string
}

// Encode returns the derived wire identifier for this reference. It reads
// Selector alone, which is why [DecodeRepoRef] has to be told the family, and it
// ignores ID, so it is also how ID is produced.
//
// It validates nothing and cannot: one result and no error. A selector minted
// locally is refused by [ValidateSelector] before it is encoded, which is the
// call [DecodeRepoRef] makes on the way back in.
func (r RepoRef) Encode() string {
	return RepoIDPrefix + hex.EncodeToString([]byte(lowerASCII(r.Selector)))
}

// DecodeRepoRef recovers a reference from a wire identifier, validating the
// decoded selector before it can reach a URL.
//
// family is a parameter because the derivation encodes the selector alone, so a
// decoded identifier carries no family of its own; a consumer's route knows it
// from the connection the identifier arrived on. A validation failure is
// [CodeRepoRefInvalid] and never a request: a derived identifier is
// consumer-controlled input that gets interpolated into a path. An id that is not
// the canonical encoding of the selector it decodes to, upper-case hex or the hex
// of a selector carrying ASCII upper case, is refused the same way, so an accepted
// id is [RepoRef.Encode] of the answer and one repository has one identifier.
func DecodeRepoRef(id string, family Family) (RepoRef, error) {
	rest, ok := strings.CutPrefix(id, RepoIDPrefix)
	if !ok {
		return RepoRef{}, localError(CodeRepoRefInvalid, "repository id carries no known version prefix")
	}
	if len(rest) > 2*maxPathValueBytes {
		return RepoRef{}, localError(CodeRepoRefInvalid, "repository id is over the selector's byte cap")
	}
	raw, err := hex.DecodeString(rest)
	if err != nil {
		return RepoRef{}, localError(CodeRepoRefInvalid, "repository id is not hex encoded")
	}
	selector := string(raw)
	if err := ValidateSelector(family, selector); err != nil {
		return RepoRef{}, err
	}
	ref := RepoRef{ID: id, Family: family, Selector: selector, DisplayPath: selector}
	if ref.Encode() != id {
		return RepoRef{}, localError(CodeRepoRefInvalid, "repository id is not the canonical encoding of its selector")
	}
	return ref, nil
}

// ValidateSelector refuses a decoded selector that is not safe to interpolate
// into that family's API path: a byte cap, an allowed character class, no
// empty, "." or ".." segment, no leading or trailing separator, and no
// separator beyond the family's own.
//
// A failure is [CodeRepoRefInvalid], the same code [DecodeRepoRef] answers,
// because it is the same refusal reached without the decode.
//
// The segment rules are spelled over slash-delimited path fragments here rather
// than borrowed from a filesystem-path library: a cleaned-path identity check
// rewrites "owner/repo" on any platform whose separator is not "/", so every
// valid selector would be refused there.
func ValidateSelector(family Family, selector string) error {
	if err := validatePathValue(selector, CodeRepoRefInvalid); err != nil {
		return err
	}
	want := 1
	if family == FamilyGitLab {
		want = maxSelectorSeparators
	}
	if strings.Count(selector, "/") > want {
		return localError(CodeRepoRefInvalid, "repository selector carries a separator this family's paths do not take")
	}
	return nil
}

// maxSelectorSeparators bounds the namespace depth one family's selector may
// carry. It is a depth bound rather than an absence of one, because that family
// takes a nested group path and an unbounded one is a path built from input.
const maxSelectorSeparators = 20

// maxPathValueBytes bounds a selector, a ref or a cursor. It is well above every
// name a forge issues and well below what an interpolated path should carry.
const maxPathValueBytes = 512

// validatePathValue holds a value that gets interpolated into a request path to
// its form: a byte cap, the allowed class, no empty, "." or ".." segment, and no
// leading or trailing separator. It is spelled over slash-delimited fragments
// rather than borrowed from a filesystem-path library, which would rewrite
// "owner/repo" wherever the platform separator is not "/".
func validatePathValue(value, code string) error {
	switch {
	case value == "":
		return localError(code, "value is empty")
	case len(value) > maxPathValueBytes:
		return localError(code, "value is over the byte cap")
	case strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/"):
		return localError(code, "value carries a leading or trailing separator")
	}
	for segment := range strings.SplitSeq(value, "/") {
		switch segment {
		case "":
			return localError(code, "value carries an empty segment")
		case ".", "..":
			return localError(code, "value carries a dot or dot-dot segment")
		}
	}
	for i := range len(value) {
		if !isPathByte(value[i]) {
			return localError(code, "value carries a byte outside the allowed class")
		}
	}
	return nil
}

// isPathByte reports whether one byte is in the class a path value may carry:
// unreserved ASCII plus the separator, the dot and the three punctuation bytes
// forge names use.
func isPathByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	switch b {
	case '-', '_', '.', '/', '+', '~':
		return true
	}
	return false
}

// lowerASCII lowercases ASCII and leaves every other byte alone, which is the
// whole of the case folding a derived identifier may perform: folding over
// Unicode would map two distinct selectors onto one identifier.
func lowerASCII(s string) string {
	out := []byte(s)
	for i, b := range out {
		if b >= 'A' && b <= 'Z' {
			out[i] = b + ('a' - 'A')
		}
	}
	return string(out)
}

// ValidateRef refuses a commit ref, a branch name or a SHA, that is not safe to
// interpolate into an API path: the selector rule minus the separator ban. A
// byte cap, an allowed character class, no empty, "." or ".." segment, no
// leading or trailing separator; and no bound on separators, because a ref
// legitimately contains "/".
//
// A failure is [CodeRefInvalid], answered before any request with an empty
// [Error.Op] and [FamilyUnknown], as every local refusal reads. The gap it
// closes is measured rather than supposed: the HTTP stack forwards ".."
// segments in a request URI verbatim, so a ref of "../../../user" would reach
// the instance as a different endpoint.
//
// It is traversal and form only, never git's grammar. The forms git
// additionally rejects, a ".lock" suffix, a leading "-", "@{", are rare, each
// would be a published refusal a 1.0 cannot loosen, and the forge is the
// authority on which names a repository holds: such a name reaches the instance
// and comes back as upstream's own refusal, mapped to [KindNotFound] with the ref
// in the message so the caller sees the name it sent.
func ValidateRef(ref string) error {
	return validatePathValue(ref, CodeRefInvalid)
}

// PRRef addresses one pull request within a repository.
type PRRef struct { //nolint:govet // fieldalignment: the number leads because that is how a human reads a pull-request reference, and the order is API for an unkeyed composite literal
	// Number is the number the family's own API takes. On one family that is
	// the request's per-project number rather than a global identifier.
	Number int
	// Sigil is how a human writes that number, "#" on two families and "!" on
	// one. It is a rendering concern and never enters a request path.
	Sigil string
}

// IssueRef addresses one issue within a repository.
type IssueRef struct {
	// Number is the issue number the family's own API takes.
	Number int
}

// Connection is one instance a client talks to: where it is, and what it is
// trusted with. A client is built per connection, because the port allowlist,
// the address policy, the proxy and the TLS material below are all per instance.
type Connection struct {
	// WebBaseURL is required: the clone origin, matching the HTML URLs the
	// instance serves. A credential helper registers against this, because it
	// is the origin a git remote carries. It is an origin and a path: a URL
	// carrying userinfo, a query or a fragment is refused with
	// [CodeConnectionInvalid], because userinfo names a host other than the
	// one a request reaches.
	WebBaseURL string
	// APIBaseURL is optional and derived per family when empty. It takes the
	// same form as WebBaseURL.
	APIBaseURL string
	// CABytes is an optional PEM bundle. It is COPIED rather than referenced,
	// so a path cannot change under a live connection, and it becomes the SOLE
	// trust anchor rather than an addition to the system pool: a self-hosted
	// instance presenting a private CA is the case this serves, and accepting
	// either is how a mis-issued public certificate stays accepted.
	CABytes []byte
	// ClientCert is an optional client certificate for mutual TLS. A
	// half-present pair is refused at entry rather than ignored.
	ClientCert []byte
	// ClientKey is the matching private key, handled as key material.
	ClientKey []byte
	// Proxy is the proxy THIS connection's traffic leaves through, a URL, empty
	// for a direct dial. It is one more per-connection statement rather than a
	// process-wide one, so one connection's proxy can never silently apply to
	// another and the connection record is the whole truth about where this
	// connection's traffic goes.
	//
	// It is NEVER read from the environment: HTTPS_PROXY, HTTP_PROXY and
	// NO_PROXY are not consulted at any level of the stack, because a
	// process-global setting is invisible in the record above and would apply to
	// every connection at once.
	//
	// The posture on a proxied connection is DELEGATED, which is the one
	// containment promise this field weakens and it is stated rather than
	// claimed: the socket goes to the proxy and the proxy resolves the
	// destination, so the resolved destination address is not checked here and
	// the party guaranteeing which destinations are reachable is the proxy the
	// operator named. The address policy applies to the PROXY's own address
	// instead, under the same private-range statement as a forge on one. What is
	// unchanged is everything above the dial: https unless plaintext was opted
	// into on this connection, credentials and Headers dropped on a hop whose
	// scheme, host or port differs from the connection's, the hop cap, URL form
	// validation, and the token's confidentiality end to end, because https
	// through a proxy is a CONNECT tunnel the proxy cannot read.
	//
	// A URL net/url refuses, or one carrying no scheme this library proxies
	// over, is refused at connect time with [CodeConnectionInvalid] rather than
	// dropped to a direct dial, for the reason the reserved-header refusal
	// gives: an operator whose setting was ignored believes something false
	// about their traffic.
	Proxy string
	// Headers are extra request headers sent on every request to this
	// connection, for the gateway in front of a self-hosted instance whose
	// canonical case is an identity-aware proxy taking a client id and a client
	// secret on every request. It is a list of [Header] entries, a name and a
	// value each, rather than a string map, so the order a consumer configured
	// is the order it reads back and a duplicated name is visible rather than
	// silently collapsed.
	//
	// What it accepts is partitioned by WHO WRITES the header. A name this
	// library or the HTTP stack beneath it writes itself, the set
	// [ReservedHeaders] returns, is refused at connect time with
	// [CodeHeaderReserved] naming the header, never dropped and never sent,
	// because an operator whose setting was ignored believes something false
	// about their traffic. Every other name passes through. Containment is the
	// redirect policy's job and not this list's: every entry is dropped on a
	// redirect hop whose scheme, host or port differs from the connection's, so
	// a header only ever reaches the instance the user typed.
	//
	// TWO LIMITS, stated so this is not read as enterprise coverage. Authorization
	// is RESERVED for the forge credential, so a front door that authenticates
	// with a bearer token in that header is not served by this field at all; a
	// gateway with header names of its own, which is the canonical case above,
	// is. And the commoner enterprise blocker is not a front door but an egress
	// PROXY, which no header this field carries reaches: the explicitly
	// configured one is Proxy above, and the transparent network-level one is
	// undetectable from inside the process and unsupported.
	Headers []Header
}
