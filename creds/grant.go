package creds

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// defaultInterval is the polling interval of a device-code answer that states
// none, RFC 8628 section 3.2.
const defaultInterval = 5 * time.Second

// slowDownStep is how far a slow_down answer that states no interval widens the
// one in use, RFC 8628 section 3.5.
const slowDownStep = 5 * time.Second

// GrantRequest is what a device grant asks for: the consumer's own OAuth
// application, which the record the grant answers names, and its scopes.
type GrantRequest struct {
	ClientID string
	Scopes   []string
}

// DeviceGrant is one OAuth device grant in progress: what the consumer renders, and
// what it polls. The device code stays unexported, because it is a bearer secret for
// the grant's whole life and nothing a consumer renders needs it. One grant is polled
// by one loop; [DeviceGrant.Close] may come from any other goroutine.
type DeviceGrant struct { //nolint:govet // fieldalignment: the exported fields lead in the order a consumer renders them
	// UserCode is what the user types at VerificationURI.
	UserCode        string
	VerificationURI string
	// Expires is when the codes expire; a poll after it ends the grant.
	Expires time.Time
	// Interval is how long to wait between polls, widened by slow_down.
	Interval time.Duration

	ep         *transport.Conn
	granted    *Record
	ended      error
	deviceCode string
	clientID   string
	webBase    string
	grantPath  string
	family     forgeapi.Family
	// mu is held by Poll for its whole call and by Close, so a cancel from another
	// goroutine than the poll loop waits for the poll in flight.
	mu sync.Mutex
}

// StartDeviceGrant sends the device-code request for req to conn's web base and
// answers the grant to render and poll. The Gitea family is refused before any
// request with [forgeapi.CodeGrantUnsupported]. The grant's requests go through
// conn's trust material, proxy and address policy, read from opts as a client's.
// A caller that abandons the grant before Poll answers a terminal result calls
// [DeviceGrant.Close] to release its endpoint.
//
//nolint:gocritic // hugeParam: the connection by value is the published signature's own parameter
func StartDeviceGrant(ctx context.Context, conn forgeapi.Connection, family forgeapi.Family, req GrantRequest, opts ...forgeapi.Option) (*DeviceGrant, error) {
	ends, ok := endpointsOf(family)
	if !ok {
		return nil, transport.Local(forgeapi.CodeGrantUnsupported, "this library runs no device grant for the "+family.String()+" family")
	}
	set := forgeapi.Resolve(opts...)
	ep, err := transport.OpenEndpoint(&conn, &set, family)
	if err != nil {
		return nil, err
	}
	form := url.Values{formClientID: {req.ClientID}}
	if len(req.Scopes) > 0 {
		form.Set("scope", strings.Join(req.Scopes, " "))
	}
	a, status, err := post(ctx, ep, "StartDeviceGrant", ends.codes, form)
	if err == nil && !a.issuedCodes(status) {
		err = unanswered(family, status, &a)
	}
	if err != nil {
		ep.Close()
		return nil, err
	}
	interval := seconds(a.Interval)
	if interval <= 0 {
		interval = defaultInterval
	}
	return &DeviceGrant{
		UserCode: a.UserCode, VerificationURI: a.VerificationURI,
		Expires: time.Now().Add(seconds(*a.ExpiresIn)), Interval: interval,
		ep: ep, deviceCode: a.DeviceCode, clientID: req.ClientID,
		webBase: conn.WebBaseURL, grantPath: ends.grant, family: family,
	}, nil
}

// Poll sends one token request and answers the record and true once the user
// approves, the zero record and false while approval is pending or after slow_down.
// A denial ([forgeapi.CodeGrantDenied]), an expiry ([forgeapi.CodeGrantExpired]) and
// a success holding no readable token ([forgeapi.CodeValidation] at its status: a
// body cut off, undecodable or tokenless, or a lifetime of neither kind) end the
// grant, since each spends or voids the device code; a context ending inside such a
// success answers its sentinel. A later Poll answers an end or the record again and
// sends nothing; a failure status, or no status at all, leaves the grant open.
func (g *DeviceGrant) Poll(ctx context.Context) (Record, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case g.granted != nil:
		return *g.granted, true, nil
	case g.ended != nil:
		return Record{}, false, g.ended
	case !time.Now().Before(g.Expires):
		return Record{}, false, g.end(g.refusal(forgeapi.CodeGrantExpired, forgeapi.KindUnauthorized, 0,
			"the device codes expired before the grant was approved"))
	}
	form := url.Values{formClientID: {g.clientID}, "device_code": {g.deviceCode}, "grant_type": {deviceGrantType}}
	a, status, err := post(ctx, g.ep, "Poll", g.grantPath, form)
	switch {
	case err != nil && status != 0 && a.succeeded(status):
		// The spent code ends the grant even where the context ended the read, whose
		// own answer stays the context's sentinel under the three-way contract.
		end := g.end(tokenless(g.family, status))
		if transport.ContextSentinel(err) != nil {
			return Record{}, false, err
		}
		return Record{}, false, end
	case err != nil:
		return Record{}, false, err
	case a.granted(status) && !a.readable():
		return Record{}, false, g.end(unreadable(g.family, status))
	case a.granted(status):
		rec := minted(&Record{Family: g.family, WebBaseURL: g.webBase, ClientID: g.clientID}, &a, time.Now())
		g.granted = &rec
		g.ep.Close()
		return rec, true, nil
	case a.succeeded(status):
		return Record{}, false, g.end(tokenless(g.family, status))
	}
	return Record{}, false, g.outcome(&a, status)
}

// Close ends a pending grant on the consumer's own word and releases its endpoint,
// sending nothing: the next Poll answers [forgeapi.CodeGrantDenied] at status 0, the
// end being the consumer's and no instance's. On a grant already granted or ended
// it changes nothing, so Poll keeps answering the record or that end. A Close
// during a Poll in flight returns after it, and a grant that poll completed stays
// granted.
func (g *DeviceGrant) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.granted != nil || g.ended != nil {
		return
	}
	g.ended = g.refusal(forgeapi.CodeGrantDenied, forgeapi.KindForbidden, 0,
		"the consumer ended the grant before it was approved")
	g.ep.Close()
}

// outcome reads an answer that issued nothing: nil while approval is pending or
// after slow_down, the grant's end on a denial or an expiry, the failure otherwise.
func (g *DeviceGrant) outcome(a *answer, status int) error {
	switch a.Error {
	case errPending:
		return nil
	case errSlowDown:
		g.Interval += slowDownStep
		if a.Interval > 0 {
			g.Interval = seconds(a.Interval)
		}
		return nil
	case errAccessDenied:
		return g.end(g.refusal(forgeapi.CodeGrantDenied, forgeapi.KindForbidden, status,
			"the user declined the grant"))
	case errExpiredToken:
		return g.end(g.refusal(forgeapi.CodeGrantExpired, forgeapi.KindUnauthorized, status,
			"the device codes expired before the grant was approved"))
	}
	return unanswered(g.family, status, a)
}

func (g *DeviceGrant) refusal(code string, kind forgeapi.ErrorKind, status int, message string) *forgeapi.Error {
	return &forgeapi.Error{Code: code, Family: g.family, Status: status, Kind: kind, Message: message}
}

// end makes err the grant's terminal answer and releases its connection.
func (g *DeviceGrant) end(err error) error {
	g.ended = err
	g.ep.Close()
	return err
}
