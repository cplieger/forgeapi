package forgeapi

import (
	"context"
	"fmt"
)

// CredKind is what kind of credential a source holds, which decides whether it
// can be refreshed at all.
type CredKind int

// The CredKind members.
const (
	// CredKindUnknown is the zero value: the kind was not recorded. A
	// zero-valued record must not read as a valid static token that never
	// needs refreshing, which is the worst available default for a credential.
	CredKindUnknown CredKind = iota
	// CredKindStaticPAT is a personal access token: it does not rotate, so it
	// is never refreshed and it fails terminally when revoked or expired.
	CredKindStaticPAT
	// CredKindRotatingOAuth is an OAuth grant with an expiry, refreshed under
	// a single owner per connection. One in-scope product issues no refresh
	// token from its device flow at all, so a connection of this kind can
	// still reach [CredReconnectRequired] by construction.
	CredKindRotatingOAuth
)

// String returns the member's spelling, which is what a log line carries:
// "unknown", "static_pat" or "rotating_oauth".
func (k CredKind) String() string {
	return nameOf([]string{nameUnknown, "static_pat", "rotating_oauth"}, k)
}

var _ fmt.Stringer = CredKind(0)

// CredState is where one credential sits in the refresh state machine.
type CredState int

// The CredState members: the machine's four states plus the unknown zero.
const (
	// CredUnknown is the zero value: the state was not read. It is not a
	// state of the machine, and it exists so that an unpopulated record does
	// not read as valid.
	CredUnknown CredState = iota
	// CredValid means the credential works and is not near expiry.
	CredValid
	// CredRefreshDue means expiry is inside the refresh lead time.
	CredRefreshDue
	// CredRefreshing means a refresh is in flight under this connection's
	// single owner. Concurrent callers wait bounded by their own contexts and
	// then fail, rather than queueing indefinitely.
	CredRefreshing
	// CredReconnectRequired is terminal: the grant is gone and no further
	// attempt is made. It surfaces on the connection, and the remedy is a
	// human reconnecting rather than anything this library can retry.
	CredReconnectRequired
)

// String returns the member's spelling, which is what a log line and a counter
// dimension carry: "unknown", "valid", "refresh_due", "refreshing" or
// "reconnect_required".
func (s CredState) String() string {
	return nameOf([]string{nameUnknown, "valid", "refresh_due", "refreshing", "reconnect_required"}, s)
}

var _ fmt.Stringer = CredState(0)

// CredentialSource is where a connection's token comes from.
//
// It is a real interface rather than a struct of functions, and it is one of
// only two consumer-implementable shapes on this surface: a consumer supplying
// its own credential source is a genuine case, and this is deliberately sized
// at the minimum that serves it, because adding a method here is a breaking
// change for every implementer.
//
// It lives in this package rather than beside the refresh machine because the
// root's option vocabulary has to name it, and the root imports no subpackage.
//
// Implementations are called from every operation, so one must be safe for
// concurrent use.
type CredentialSource interface {
	// Token returns the token to authenticate with, refreshing first where
	// that is this source's job. It is called per request rather than cached
	// by the caller, so a rotation reaches the next request.
	Token(ctx context.Context) (string, error)

	// Kind reports whether this credential rotates.
	Kind() CredKind

	// State reports where the credential sits in the refresh machine, so a
	// consumer can render a connection that needs reconnecting before a
	// request fails.
	State() CredState
}
