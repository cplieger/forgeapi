package forgeapi

import "github.com/cplieger/go/masked"

// Header is one consumer-set request header on a [Connection]. The Value is a
// literal the consumer supplies, never a key this library resolves, because a
// consumer implementing [CredentialSource] already holds every secret a store could
// give. An empty or malformed Name, or a Value outside net/http's field grammar, is
// refused at connect time with [CodeConnectionInvalid]; a Name in [ReservedHeaders]
// is refused with [CodeHeaderReserved]. Names compare case-insensitively, as HTTP
// does.
type Header struct { //nolint:govet // fieldalignment: a header reads name then value, the declared order api/forgeapi.txt records
	// Name is the header's field name.
	Name string
	// Value is the value to send.
	Value masked.Secret
}

// ReservedHeaders returns the request header names a [Connection] may not set
// through [Connection.Headers], because something beneath the consumer writes them:
// this library (the credential, the proxy credentials, the API-version pin,
// Accept, Content-Type, User-Agent and the read cache's two validators), or Go's
// HTTP stack, which a caller-set Accept-Encoding, Connection, Content-Length, Host
// or Transfer-Encoding would break or be ignored by. Every other name passes through. Names compare
// case-insensitively, and each call returns a fresh slice.
func ReservedHeaders() []string {
	return []string{
		"Accept",
		"Authorization",
		"Proxy-Authorization",
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
