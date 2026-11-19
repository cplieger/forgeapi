package transport

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/go/httpclient"
	"github.com/cplieger/go/ssrf"
)

// Retryable is the one rule behind [forgeapi.Error.Retryable]: another attempt
// may succeed after a transport failure or a throttle, and on a status
// httpclient.IsRetryableStatus accepts.
func Retryable(kind forgeapi.ErrorKind, status int) bool {
	return kind == forgeapi.KindTransient || kind == forgeapi.KindRateLimited || httpclient.IsRetryableStatus(status)
}

// failureOf classifies a failure that reached no status by what another attempt
// can change. A TLS verdict ([httpclient.CauseTLS]: a certificate, a fatal alert
// or a plain-HTTP answer) and an address-policy refusal need a changed
// connection; everything else the network did may pass on its own.
func failureOf(err error) (code string, kind forgeapi.ErrorKind) {
	if httpclient.CauseOf(err) == httpclient.CauseTLS {
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

// transportError maps a failure that never reached a status, its text quoted by
// q, the connection's where q is nil ([causeText]). The operation's own context
// having ended is returned unchanged; a live context's sentinel is a dial timeout,
// classified as unreachable so its host is kept. A *forgeapi.Error from below the
// call (the redirect policy's, a credential source's) is recovered, keeping its code
// and successor, and answered as a copy, since a source's may be a shared sentinel;
// q quotes its message unless [sourceError] or [policyError] marks it. An attempt
// the attempt hook refused is answered first, since its error may be one too.
func (c *Conn) transportError(ctx context.Context, req *Request, err error, started time.Time, q *quoter) error {
	q = c.orAmbient(q)
	if ctxErr := ctx.Err(); ctxErr != nil && contextSentinel(err) != nil {
		c.log(ctx, req, exchange(req), 0, "", "", forgeapi.KindUnknown, nil, started, own(ctxErr.Error()), true)
		return ctxErr
	}
	if refused, ok := errors.AsType[*refusedAttemptError](err); ok {
		said := causeText(refused.err, q)
		fe := c.fail(req.Op, forgeapi.CodeAttemptRefused, forgeapi.KindUnknown, 0, said)
		fe.Refusal = refused.err
		c.log(ctx, req, exchange(req), 0, fe.Code, fe.DiagID, fe.Kind, nil, started, said, true)
		return fe
	}
	if below, ok := errors.AsType[*forgeapi.Error](err); ok {
		fe := *below
		if fe.Op == "" {
			fe.Op = req.Op
		}
		if fe.DiagID == "" {
			fe.DiagID = diagID()
		}
		said := q.quote(fe.Message)
		if _, hop := errors.AsType[*policyError](err); hop {
			said = own(fe.Message)
		} else if source, sourced := errors.AsType[*sourceError](err); sourced {
			said = c.ambient().with(source.token).quote(fe.Message)
		}
		fe.Message = said.String()
		c.log(ctx, req, exchange(req), fe.Status, fe.Code, fe.DiagID, fe.Kind, nil, started, said, true)
		return &fe
	}
	code, kind := failureOf(err)
	cause := httpclient.LogSafeError(err).Error()
	if code == forgeapi.CodeUnreachable {
		cause = "cannot reach " + hostOf(c.urlFor(req), err) + ": " + cause
	}
	said := q.quote(cause)
	fe := c.fail(req.Op, code, kind, 0, said)
	c.log(ctx, req, exchange(req), 0, fe.Code, fe.DiagID, fe.Kind, nil, started, said, true)
	return fe
}

// bodyError maps a response body that could not be read. The instance answered,
// so the failure carries its status and figures, read off answer's header; a body
// over the bound is the instance's answer and the same again next time, a body cut
// short is the network's.
func (c *Conn) bodyError(ctx context.Context, req *Request, answer *Response, err error, started time.Time) error {
	q := c.quoterOf(answer)
	if ctxErr := ctx.Err(); ctxErr != nil && contextSentinel(err) != nil {
		c.log(ctx, req, exchange(req), answer.Status, "", "", forgeapi.KindUnknown, nil, started, own(ctxErr.Error()), true)
		return ctxErr
	}
	code, kind, cause := forgeapi.CodeResponseTooLarge, forgeapi.KindUpstream, err.Error()
	if _, tooLarge := errors.AsType[*httpclient.ResponseTooLargeError](err); !tooLarge {
		code, kind, cause = "", forgeapi.KindTransient, "lost the answer from "+hostOf(c.urlFor(req), err)+": "+cause
	}
	said := q.quote(cause)
	fe := c.answered(req.Op, code, kind, answer, said)
	c.log(ctx, req, exchange(req), answer.Status, fe.Code, fe.DiagID, fe.Kind, nil, started, said, true)
	return fe
}

// hostOf is the host a failed request went to: the one net/http names where the
// failure carries a URL, which is the hop that failed, else target's.
func hostOf(target string, err error) string {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		if u, parseErr := url.Parse(ue.URL); parseErr == nil && u.Host != "" {
			return u.Hostname()
		}
	}
	if u, parseErr := url.Parse(target); parseErr == nil {
		return u.Hostname()
	}
	return ""
}
