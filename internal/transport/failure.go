package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/httpx/v5"
	"github.com/cplieger/ssrf/v4"
)

// Retryable is the one rule behind [forgeapi.Error.Retryable]: another attempt
// may succeed after a transport failure or a throttle, and on a status saying the
// instance could not answer this time (408, 429, any 5xx).
func Retryable(kind forgeapi.ErrorKind, status int) bool {
	return kind == forgeapi.KindTransient || kind == forgeapi.KindRateLimited || httpx.IsRetryableStatus(status)
}

// failureOf classifies a failure that reached no status by what another attempt
// can change. A TLS verdict and an address-policy refusal need a changed
// connection; everything else the network did may pass on its own.
func failureOf(err error) (code string, kind forgeapi.ErrorKind) {
	if tlsRefused(err) {
		return forgeapi.CodeTLSRefused, forgeapi.KindUpstream
	}
	if se, ok := errors.AsType[*ssrf.Error](err); ok && se.Kind != ssrf.KindDNSFailed {
		switch se.Kind {
		case ssrf.KindNonPublicIP, ssrf.KindPolicyDenied:
			return forgeapi.CodePrivateAddressRefused, forgeapi.KindUpstream
		case ssrf.KindBadPort:
			return forgeapi.CodeRedirectPortRefused, forgeapi.KindUpstream
		}
		return "", forgeapi.KindUpstream
	}
	return forgeapi.CodeUnreachable, forgeapi.KindTransient
}

// tlsRefused reports a handshake no retry fixes. A fatal alert from the peer is
// an *net.OpError whose Op is "remote error", because crypto/tls keeps the alert
// type unexported (go1.27 crypto/tls/conn.go, readRecordOrCCS); only its text
// separates one alert from another, so every fatal alert counts.
func tlsRefused(err error) bool {
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return true
	}
	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return true
	}
	if errors.Is(err, http.ErrSchemeMismatch) {
		return true
	}
	op, ok := errors.AsType[*net.OpError](err)
	return ok && op.Op == "remote error"
}

// transportError maps a failure that never reached a status. The operation's own
// context having ended is the third arm of the three-way error contract, returned
// UNCHANGED; a sentinel in the chain while that context is live is a dial timeout
// (net's timeout error matches context.DeadlineExceeded) and is classified like
// any other unreachable instance, so its host is not lost.
//
// A refusal this library itself produced below the call is recovered rather than
// rebuilt. The redirect policy answers a *forgeapi.Error, net/http wraps whatever
// a policy returns in *url.Error, and stringifying that chain would turn a
// permanent containment refusal into a retryable unreachable one, and a stale
// reference would lose its successor.
func (c *Conn) transportError(ctx context.Context, req *Request, err error, started time.Time) error {
	if ctxErr := ctx.Err(); ctxErr != nil && contextSentinel(err) != nil {
		c.log(ctx, req, exchange(req), 0, "", "", forgeapi.KindUnknown, nil, started, ctxErr)
		return ctxErr
	}
	if own, ok := errors.AsType[*forgeapi.Error](err); ok {
		if own.Op == "" {
			own.Op = req.Op
		}
		if own.DiagID == "" {
			own.DiagID = diagID()
		}
		c.log(ctx, req, exchange(req), own.Status, own.Code, own.DiagID, own.Kind, nil, started, err)
		return own
	}
	code, kind := failureOf(err)
	message := sanitize(httpx.LogSafeError(err).Error())
	if code == forgeapi.CodeUnreachable {
		message = "cannot reach " + c.hostOf(req, err) + ": " + message
	}
	fe := c.fail(req.Op, code, kind, 0, message)
	c.log(ctx, req, exchange(req), 0, fe.Code, fe.DiagID, fe.Kind, nil, started, err)
	return fe
}

// bodyError maps a response body that could not be read. The instance answered,
// so the failure carries its status; a body over the bound is the instance's
// answer and the same again next time, a body cut short is the network's.
func (c *Conn) bodyError(ctx context.Context, req *Request, status int, err error, started time.Time) error {
	if ctxErr := ctx.Err(); ctxErr != nil && contextSentinel(err) != nil {
		c.log(ctx, req, exchange(req), status, "", "", forgeapi.KindUnknown, nil, started, ctxErr)
		return ctxErr
	}
	var fe *forgeapi.Error
	if _, tooLarge := errors.AsType[*httpx.ResponseTooLargeError](err); tooLarge {
		fe = c.fail(req.Op, forgeapi.CodeResponseTooLarge, forgeapi.KindUpstream, status, sanitize(err.Error()))
	} else {
		fe = c.fail(req.Op, "", forgeapi.KindTransient, status,
			"lost the answer from "+c.hostOf(req, err)+": "+sanitize(err.Error()))
	}
	c.log(ctx, req, exchange(req), status, fe.Code, fe.DiagID, fe.Kind, nil, started, err)
	return fe
}

// hostOf is the host a failed request went to: the one net/http names where the
// failure carries a URL, which is the hop that failed, else the request's own.
func (c *Conn) hostOf(req *Request, err error) string {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		if u, parseErr := url.Parse(ue.URL); parseErr == nil && u.Host != "" {
			return sanitize(u.Hostname())
		}
	}
	if u, parseErr := url.Parse(c.urlFor(req)); parseErr == nil {
		return sanitize(u.Hostname())
	}
	return ""
}
