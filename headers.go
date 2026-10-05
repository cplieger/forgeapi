package forgeapi

// Header is one consumer-set request header on a [Connection]: a name and a
// value, and nothing else.
//
// The value is a LITERAL the consumer supplies from wherever it keeps such
// things. A gateway secret is the realistic case and this library does not
// resolve it: [CredentialSource] is consumer-implemented, so a consumer already
// holds every secret this library could obtain, and a key it chose for a store it
// owns would ask this library to resolve that key into a value the consumer had
// in its hand. What the library does owe the value is redaction wherever it
// logs, which it does whatever the value's provenance.
//
// An entry with an empty Name, with a Name that is not a valid HTTP field name,
// or with a Value that is not a valid field value (net/http's own rules: no CR,
// LF or other control byte, no space in a name), is a malformed connection and is
// refused at connect time with [CodeConnectionInvalid], because the transport
// would refuse every request carrying it and a connection that can never succeed
// is the catch-all's case; a Name in [ReservedHeaders] is refused with
// [CodeHeaderReserved]. Names are compared case-insensitively, as HTTP does.
type Header struct {
	// Name is the header's field name.
	Name string
	// Value is the value to send.
	Value string
}

// ReservedHeaders returns the request header names a [Connection] may not set
// through [Connection.Headers]: the ones something beneath the consumer already
// writes, which is this library for its credential injection, its API-version pin
// and the three it sends on every request, and Go's HTTP stack for the names it
// controls itself; plus the two request validators, which this library never
// writes, because every read it issues is unconditional, and which it refuses for
// the reason the next paragraphs give.
//
// The set is DERIVED rather than curated, and the instrument is one bounded
// observation: a request driven through this library's own transport to a test
// server, whose ARRIVING header set is what this list is held to. It reads an
// OUTPUT because the alternative, that no code anywhere in this module writes a
// header, is an unbounded claim over the language and cannot be gated.
//
// Three of the names are not observable, for two different reasons. The
// API-version pin belongs to one family's requests and is sent only to an instance
// known to carry that version, so it becomes observable when that family lands.
// The two request validators never become observable: conditional requests are a
// transport LAYER this library does not build, so no read of its own carries one.
// They are reserved anyway, because a validator a CONSUMER set would be answered
// with a not-modified status, which this library maps as the unexpected status it
// is rather than as an answer, so the header could only ever break the read it was
// set on. Refusing it by name at connect time says so where an operator reads it,
// and the refusal stays correct on the day the caching layer above exists, since
// that layer would write the same two names itself.
//
// The stack's half is three plus two. Accept-Encoding, Content-Length and
// User-Agent arrive as header-map entries, and the first of those is the one a
// consumer could otherwise pass through and break every response with: the
// transport writes it to negotiate compression and decodes the body
// transparently, so a caller-set value turns that decoding off and hands the
// decoder gzip bytes. Host and Transfer-Encoding never arrive as headers at all,
// reaching a server through fields of its own request, so they are here for the
// stated reason that a caller setting either would be silently IGNORED rather
// than honoured, which is worse than a refusal. Connection is here on the same
// ground and arrives on neither shape of request.
//
// Nothing on the set is a judgement about what a gateway may legitimately need.
// Everything not on it passes through, which is the point of a denylist over an
// allowlist here: a public library cannot foresee its users' gateways, and a
// release per unforeseen header is the wrong trade.
//
// The names are in canonical MIME header case where that case is the
// conventional one, and a connection's entry is compared against them
// case-insensitively. Each call returns a fresh slice, so a caller may keep or
// sort it.
func ReservedHeaders() []string {
	return []string{
		"Accept",
		"Authorization",
		"Content-Type",
		"If-Modified-Since",
		"If-None-Match",
		"User-Agent",
		"X-GitHub-Api-Version",
		"Accept-Encoding",
		"Connection",
		"Content-Length",
		"Host",
		"Transfer-Encoding",
	}
}
