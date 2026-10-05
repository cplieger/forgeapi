package creds

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// The OAuth error members this package acts on, and the two grant types it sends.
const (
	// errBadRefreshToken is GitHub's terminal refresh answer, sent inside a 200.
	errBadRefreshToken = "bad_refresh_token"
	// errInvalidGrant is the terminal refresh answer of RFC 6749 section 5.2,
	// which GitLab sends.
	errInvalidGrant = "invalid_grant"
	// The device grant's answers, RFC 8628 section 3.5.
	errPending      = "authorization_pending"
	errSlowDown     = "slow_down"
	errAccessDenied = "access_denied"
	errExpiredToken = "expired_token"

	deviceGrantType  = "urn:ietf:params:oauth:grant-type:device_code"
	refreshGrantType = "refresh_token"

	// formClientID names the application on every request this package sends.
	formClientID = "client_id"
)

// endpoints are one family's OAuth endpoints, as paths under the instance's web
// base: where a device grant asks for its codes, and where a grant or a refresh is
// exchanged for a token.
type endpoints struct {
	codes string
	grant string
}

// endpointsOf answers the endpoints of a family whose products document a device
// grant and a rotating OAuth credential. The Gitea family has none: its credential
// is a token the consumer supplies and records as static.
func endpointsOf(family forgeapi.Family) (endpoints, bool) {
	switch family {
	case forgeapi.FamilyGitHub:
		return endpoints{codes: "/login/device/code", grant: "/login/oauth/access_token"}, true
	case forgeapi.FamilyGitLab:
		return endpoints{codes: "/oauth/authorize_device", grant: "/oauth/token"}, true
	}
	return endpoints{}, false
}

// answer is every field either product sends from its token endpoint or its
// device-code endpoint. The two lifetimes are pointers because an absent count and
// a stated zero decide different kinds.
type answer struct {
	ExpiresIn             *int64 `json:"expires_in"`
	RefreshTokenExpiresIn *int64 `json:"refresh_token_expires_in"`
	AccessToken           string `json:"access_token"`
	Scope                 string `json:"scope"`
	RefreshToken          string `json:"refresh_token"`
	Error                 string `json:"error"`
	DeviceCode            string `json:"device_code"`
	UserCode              string `json:"user_code"`
	VerificationURI       string `json:"verification_uri"`
	Interval              int64  `json:"interval"`
}

// post sends one form to one endpoint of a connection and reads the answer. A body
// that does not decode answers the zero answer, which no caller reads as a token. A
// failure answers the status beside it where the instance answered one whose body
// did not arrive, and zero where no status answered.
func post(ctx context.Context, ep *transport.Conn, op, path string, form url.Values) (answer, int, error) {
	resp, err := ep.PostForm(ctx, op, path, form)
	if err != nil {
		if resp != nil {
			return answer{}, resp.Status, err
		}
		return answer{}, 0, err
	}
	var a answer
	if decodeErr := transport.Decode(resp.Body, &a); decodeErr != nil {
		return answer{}, resp.Status, nil
	}
	return a, resp.Status, nil
}

// granted reports whether an answer is a token.
func (a *answer) granted(status int) bool {
	return a.succeeded(status) && a.AccessToken != ""
}

// succeeded reports whether an answer is a success carrying no error member, which
// from a token endpoint is an issued token (RFC 6749 section 5.1) whether or not
// its body could be read: a body that does not decode reads as the zero answer.
func (a *answer) succeeded(status int) bool {
	return status < http.StatusMultipleChoices && a.Error == ""
}

// issuedCodes reports whether an answer carries every field RFC 8628 section 3.2
// requires of a device-code answer, the interval aside, which defaults.
func (a *answer) issuedCodes(status int) bool {
	return status < http.StatusMultipleChoices && a.Error == "" && a.DeviceCode != "" &&
		a.UserCode != "" && a.VerificationURI != "" && a.ExpiresIn != nil && *a.ExpiresIn > 0
}

// readable reports whether a token answer states one of the two kinds: a positive
// expiry, with a positive refresh expiry where it states one, which is rotating; or
// no expiry, no refresh token and no refresh expiry, which is static. Every other
// answer is refused rather than read as either, because a lifetime read wrongly is
// wrong in the dangerous direction.
func (a *answer) readable() bool {
	if a.ExpiresIn == nil {
		return a.RefreshToken == "" && a.RefreshTokenExpiresIn == nil
	}
	return *a.ExpiresIn > 0 && (a.RefreshTokenExpiresIn == nil || *a.RefreshTokenExpiresIn > 0)
}

// minted applies a token answer [answer.readable] admits to rec. An answer stating
// an expiry is a rotating credential with both expiries and the refresh token taken
// from it; one stating none is static. The scopes are the answer's where it states
// any, and base's otherwise, since a refresh cannot change them.
func minted(base *Record, a *answer, now time.Time) Record {
	rec := *base
	rec.Token, rec.Issued = a.AccessToken, now
	rec.Kind, rec.Expiry, rec.RefreshToken, rec.RefreshExpiry = forgeapi.CredKindStaticPAT, time.Time{}, "", time.Time{}
	if a.ExpiresIn != nil {
		rec.Kind, rec.Expiry, rec.RefreshToken = forgeapi.CredKindRotatingOAuth, now.Add(seconds(*a.ExpiresIn)), a.RefreshToken
		if a.RefreshTokenExpiresIn != nil {
			rec.RefreshExpiry = now.Add(seconds(*a.RefreshTokenExpiresIn))
		}
	}
	// GitHub separates the granted scopes with commas and GitLab with spaces.
	if scopes := strings.FieldsFunc(a.Scope, func(r rune) bool { return r == ',' || r == ' ' }); len(scopes) > 0 {
		rec.Scopes = scopes
	}
	return rec
}

// seconds is an upstream count of seconds as a duration, capped where the product
// would overflow a duration, since the count is the instance's to send.
func seconds(n int64) time.Duration {
	return time.Duration(min(n, math.MaxInt64/int64(time.Second))) * time.Second
}

// unreadable is the refusal of a token answer [answer.readable] does not admit.
func unreadable(family forgeapi.Family, status int) *forgeapi.Error {
	return &forgeapi.Error{
		Code: forgeapi.CodeValidation, Family: family, Status: status, Kind: forgeapi.KindUpstream,
		Message: "the OAuth endpoint answered a token whose lifetime states neither a rotating nor a static credential",
	}
}

// tokenless is the refusal of a success answer that carries no token this package
// can read: a body that does not decode or did not arrive, or one with no access
// token.
func tokenless(family forgeapi.Family, status int) *forgeapi.Error {
	return &forgeapi.Error{
		Code: forgeapi.CodeValidation, Family: family, Status: status, Kind: forgeapi.KindUpstream,
		Message: "the OAuth endpoint answered a success that carries no token this package can read",
	}
}

// documentedErrors are the error members RFC 6749 section 5.2, RFC 8628 section 3.5
// and the two products' own OAuth pages define. A refusal names the member only when
// it is one of these, because the member is the instance's text and an instance can
// echo the refresh token or the device code it was just sent.
var documentedErrors = map[string]bool{
	"invalid_request": true, "invalid_client": true, errInvalidGrant: true, "unauthorized_client": true,
	"unsupported_grant_type": true, "invalid_scope": true, "server_error": true, "temporarily_unavailable": true,
	errPending: true, errSlowDown: true, errAccessDenied: true, errExpiredToken: true,
	errBadRefreshToken: true, "incorrect_client_credentials": true, "incorrect_device_code": true,
	"device_flow_disabled": true, "unsupported_token_type": true,
}

// unanswered is an endpoint answer that is neither a token nor a protocol outcome
// this package acts on. A server failure is worth another attempt; anything else
// names the documented error member the instance sent, so an operator reads what
// it said.
func unanswered(family forgeapi.Family, status int, a *answer) *forgeapi.Error {
	if status >= http.StatusInternalServerError {
		return &forgeapi.Error{
			Family: family, Status: status, Kind: forgeapi.KindTransient, Retryable: true,
			Message: "the OAuth endpoint answered a server failure",
		}
	}
	message := "the OAuth endpoint answered no token"
	switch {
	case documentedErrors[a.Error]:
		message = "the OAuth endpoint answered " + a.Error
	case a.Error != "":
		message = "the OAuth endpoint answered an error member no OAuth document defines"
	}
	return &forgeapi.Error{Code: forgeapi.CodeValidation, Family: family, Status: status, Kind: forgeapi.KindUpstream, Message: message}
}
