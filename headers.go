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
// through [Connection.Headers], because something beneath the consumer writes them:
// this library (the credential, the API-version pin, Accept, Content-Type,
// User-Agent and the read cache's two validators), or Go's HTTP stack, which a
// caller-set Accept-Encoding, Connection, Content-Length, Host or Transfer-Encoding
// would break or be ignored by. Every other name passes through. Names compare
// case-insensitively, and each call returns a fresh slice.
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
