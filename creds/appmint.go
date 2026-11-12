package creds

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/internal/transport"
)

// failClass is what one failed App request does to the source's standing.
type failClass int

const (
	// failTransient is what no human can repair: it moves the standing only
	// by re-arming a standing terminal refusal's re-probe wait.
	failTransient failClass = iota
	// failLimited sets the instant before which nothing is sent.
	failLimited
	// failRefused is a non-2xx outside the classes the source reads, which a
	// resend would meet again, so it sets the instant as a stateless limit does.
	failRefused
	// failEnded is terminal until a re-probe succeeds.
	failEnded
)

// failure is one failed App request: its error, the status GitHub answered,
// zero where none did, and for a rate limit the wait GitHub stated, zero where
// it stated none.
type failure struct {
	err    error
	wait   time.Duration
	status int
	class  failClass
}

// renewOwned mints one token under the source's owner ([AppSource.retrying]).
// A re-probe after a terminal answer forgets the installation ID first
// ([AppSource.forget]). A caller's own cancel moves no standing: GitHub
// answered nothing, and the source's one re-probe is not spent on it.
func (s *AppSource) renewOwned(ctx context.Context, op string) (issued, error) {
	s.mu.Lock()
	if s.ended != nil {
		s.forget()
	}
	s.mu.Unlock()
	ep, err := s.open()
	if err != nil {
		return issued{}, err
	}
	defer ep.Close()
	var next issued
	var status int
	f := s.retrying(ctx, ep, op, func(ctx context.Context) (f *failure) {
		next, status, f = s.mintOnce(ctx, ep, op)
		return f
	})
	if f == nil {
		s.mu.Lock()
		s.ended, s.endedN, s.limitN, s.refusedN = nil, 0, 0, 0
		s.mu.Unlock()
		s.renew.outcome(forgeapi.FamilyGitHub, s.instance, forgeapi.CredValid, status, "")
		return next, nil
	}
	if cancelled(ctx, f) {
		return issued{}, f.err
	}
	reached, err := s.settle(f)
	s.renew.outcome(forgeapi.FamilyGitHub, s.instance, reached, f.status, outcomeText(f.class))
	return issued{}, err
}

// cancelled reports whether f is the caller's own cancel. A deadline is not
// one: under equal budgets a client's deadline is the source's bound, and a
// hanging GitHub must still re-arm the re-probe's wait.
func cancelled(ctx context.Context, f *failure) bool {
	return errors.Is(ctx.Err(), context.Canceled) && errors.Is(f.err, context.Canceled)
}

// retrying runs try under op's operation bound, and again after each failure
// no human can repair, up to the budget's retries ([AppSource.backOff]); each
// request inside try is sent once ([transport.Request.Credential]). It answers
// nil once a try succeeds, else GitHub's last answer: a pause the bound cannot
// hold is skipped, and a try the bound cuts short answers the try before it,
// so a context error reaches the caller only where ctx itself ended, or where
// the first try ran out the bound with no earlier answer to give.
func (s *AppSource) retrying(ctx context.Context, ep *transport.Conn, op string, try func(context.Context) *failure) *failure {
	bound, cancel := ep.Bound(transport.Call(ctx, op))
	defer cancel()
	var last *failure
	for attempt := range s.set.Budget.Retries + 1 {
		if attempt > 0 && !s.backOff(bound, op, attempt) {
			if ctx.Err() != nil {
				return &failure{err: ctx.Err()}
			}
			return last
		}
		f := try(bound)
		switch {
		case f == nil:
			return nil
		case last != nil && errors.Is(f.err, context.DeadlineExceeded) && ctx.Err() == nil:
			return last
		case f.class != failTransient || !retryable(f.err):
			return f
		}
		last = f
	}
	return last
}

// mintOnce resolves the installation where its ID is not remembered and asks
// GitHub for one token. A 404 from the token endpoint forgets a looked-up ID
// ([AppSource.forget]), which a reinstall changes.
func (s *AppSource) mintOnce(ctx context.Context, ep *transport.Conn, op string) (next issued, status int, f *failure) {
	id, f := s.installation(ctx, ep, op)
	if f != nil {
		return issued{}, 0, f
	}
	req := &transport.Request{
		Op: op, Method: http.MethodPost, Path: "/app/installations/" + strconv.FormatInt(id, 10) + "/access_tokens",
		Body: s.grant.body(), Answers: appAnswers, Credential: true,
	}
	sent := time.Now()
	resp, f := s.ask(ctx, ep, req)
	if f != nil {
		if f.status == http.StatusNotFound {
			s.mu.Lock()
			s.forget()
			s.mu.Unlock()
		}
		return issued{}, 0, f
	}
	answered := time.Now()
	var a struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if transport.Decode(resp.Body, &a) == nil && a.Token != "" {
		if expiry, err := time.Parse(time.RFC3339, a.ExpiresAt); err == nil && expiry.After(answered) {
			return issued{sent: sent, answered: answered, expiry: expiry, token: newSecret(a.Token)}, resp.Status, nil
		}
	}
	return issued{}, 0, malformed(ctx, ep, req, resp.Status, "GitHub answered the token request with no token or no future expiry")
}

// installation answers the installation ID, remembered or looked up.
func (s *AppSource) installation(ctx context.Context, ep *transport.Conn, op string) (int64, *failure) {
	if id, ok := s.known(); ok {
		return id, nil
	}
	req := s.install.lookup(op)
	resp, f := s.ask(ctx, ep, req)
	if f != nil {
		return 0, f
	}
	id, f := s.installationIn(ctx, ep, req, resp)
	if f != nil {
		return 0, f
	}
	s.mu.Lock()
	s.id = id
	s.mu.Unlock()
	return id, nil
}

// installationIn reads the ID out of a lookup's answer. An App that lists no
// installation, or more than one for [OnlyInstallation], needs a human to
// install it or to name one, so either is terminal.
func (s *AppSource) installationIn(ctx context.Context, ep *transport.Conn, req *transport.Request, resp *transport.Response) (int64, *failure) {
	if s.install.by != selectOnly {
		var one listed
		if transport.Decode(resp.Body, &one) != nil || one.ID <= 0 {
			return 0, malformed(ctx, ep, req, resp.Status, "GitHub answered the installation lookup with no installation ID")
		}
		return one.ID, nil
	}
	var list []listed
	if transport.Decode(resp.Body, &list) != nil {
		return 0, malformed(ctx, ep, req, resp.Status, "GitHub answered the installation list with no list")
	}
	switch len(list) {
	case 0:
		return 0, terminal(ctx, ep, req, resp, forgeapi.KindNotFound, "the GitHub App has no installation: install it on the account the source serves")
	case 1:
		if list[0].ID <= 0 {
			return 0, malformed(ctx, ep, req, resp.Status, "GitHub answered the installation list with no installation ID")
		}
		return list[0].ID, nil
	}
	if len(list) >= maxListedInstallations {
		return 0, terminal(ctx, ep, req, resp, forgeapi.KindNotFound,
			"the GitHub App has at least "+strconv.Itoa(len(list))+" installations, so OnlyInstallation names none; the first page lists "+accounts(list))
	}
	return 0, terminal(ctx, ep, req, resp, forgeapi.KindNotFound,
		"the GitHub App has "+strconv.Itoa(len(list))+" installations, so OnlyInstallation names none of "+accounts(list))
}

// ask sends one request and answers its 2xx response, or how it failed.
func (s *AppSource) ask(ctx context.Context, ep *transport.Conn, req *transport.Request) (*transport.Response, *failure) {
	resp, err := ep.Do(ctx, req)
	if err != nil {
		f := &failure{err: err}
		if fe, ok := errors.AsType[*forgeapi.Error](err); ok {
			f.status = fe.Status
			if fe.Status >= http.StatusMultipleChoices && !fe.Retryable {
				f.class = failRefused
			}
		}
		return nil, f
	}
	if resp.Status < http.StatusMultipleChoices {
		return resp, nil
	}
	return nil, s.refused(ctx, ep, req, resp)
}

// refused classifies one of [appAnswers]. GitHub documents one description for
// a 422 from the token endpoint whatever its cause, "Validation failed, or the
// endpoint has been spammed", so no message text is matched there:
// https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app
func (s *AppSource) refused(ctx context.Context, ep *transport.Conn, req *transport.Request, resp *transport.Response) *failure {
	said := githubMessage(resp.Body)
	if rateLimited(resp.Status, resp.Header, said) {
		if s.set.Counters.RateLimited != nil {
			s.set.Counters.RateLimited(forgeapi.FamilyGitHub)
		}
		fe := ep.Fail(ctx, req.Op, transport.REST(req.Method), "", forgeapi.KindRateLimited, resp.Status,
			resp.Quote(joined("GitHub rate-limited the App's request", said)))
		return &failure{err: fe, wait: limitWait(resp.Header, time.Now()), status: resp.Status, class: failLimited}
	}
	kind, why := forgeapi.KindUpstream, "GitHub refused the request as invalid: the grant's repositories or permissions are the likely cause"
	switch resp.Status {
	case http.StatusUnauthorized:
		kind, why = forgeapi.KindUnauthorized, "GitHub rejected the App JWT: the key is not this App's, or this host's clock is beyond GitHub's skew limit"
	case http.StatusNotFound:
		kind, why = forgeapi.KindNotFound, "GitHub found no installation of the App for the source's selector"
	case http.StatusForbidden:
		kind, why = forgeapi.KindForbidden, "GitHub refused the App: the installation is suspended or lacks a permission the grant asks for"
	}
	return terminal(ctx, ep, req, resp, kind, joined(why, said))
}

// terminal is a refusal only a human can repair, on GitHub or on the host.
func terminal(ctx context.Context, ep *transport.Conn, req *transport.Request, resp *transport.Response, kind forgeapi.ErrorKind, message string) *failure {
	fe := ep.Fail(ctx, req.Op, transport.REST(req.Method), forgeapi.CodeReconnectRequired, kind, resp.Status, resp.Quote(message))
	return &failure{err: fe, status: resp.Status, class: failEnded}
}

// malformed is a 2xx answer this package cannot read, which costs one more
// request and no human can repair.
func malformed(ctx context.Context, ep *transport.Conn, req *transport.Request, status int, message string) *failure {
	fe := ep.Fail(ctx, req.Op, transport.REST(req.Method), forgeapi.CodeValidation, forgeapi.KindTransient, status, message)
	return &failure{err: fe, status: status}
}

// settle applies a failure to the source's standing and answers the error the
// caller returns, with the state it reached. A limit's or an unclassified
// refusal's error carries the wait in force, which an earlier, longer hold may
// exceed. A failure that is not terminal while a terminal refusal stands
// re-arms the re-probe's current wait, so a GitHub outage does not turn every
// call into a re-probe.
func (s *AppSource) settle(f *failure) (forgeapi.CredState, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.class != failEnded && s.ended != nil {
		s.extend(hold{until: now.Add(doubling(reprobeFirst, reprobeCeiling, max(s.endedN-1, 0)))})
	}
	switch f.class {
	case failEnded:
		fe, _ := errors.AsType[*forgeapi.Error](f.err)
		s.ended = copyOf(fe)
		s.extend(hold{until: now.Add(doubling(reprobeFirst, reprobeCeiling, s.endedN))})
		s.endedN++
	case failLimited:
		wait := f.wait
		if wait <= 0 {
			wait = doubling(limitFirst, limitCeiling, s.limitN)
			s.limitN++
		}
		s.wake(f, now, hold{
			until: now.Add(wait), kind: forgeapi.KindRateLimited,
			message: "GitHub rate-limited the App source, so it sends nothing until the wait GitHub asked for has passed",
		})
	case failRefused:
		s.wake(f, now, hold{
			until: now.Add(doubling(limitFirst, limitCeiling, s.refusedN)), kind: forgeapi.KindUpstream,
			message: "GitHub refused the App's request with a status the source does not classify, so it waits before sending again",
		})
		s.refusedN++
	}
	if s.ended != nil {
		return forgeapi.CredReconnectRequired, f.err
	}
	return forgeapi.CredRefreshDue, f.err
}

// wake extends the hold to h and states the wait in force on f's error. It
// runs under s.mu.
func (s *AppSource) wake(f *failure, now time.Time, h hold) {
	s.extend(h)
	if fe, ok := errors.AsType[*forgeapi.Error](f.err); ok {
		fe.RetryAfter = s.wait.until.Sub(now)
	}
}

// outcomeText is a failed mint's log line, which never quotes GitHub.
func outcomeText(class failClass) string {
	switch class {
	case failEnded:
		return "GitHub refused the App's request terminally"
	case failLimited:
		return "GitHub rate-limited the App's request"
	case failRefused:
		return "GitHub refused the App's request with an unclassified status"
	}
	return "the mint failed"
}

// rateLimited reports whether a refusal is GitHub's quota rather than a
// permission: a 429, or a 403 carrying Retry-After, the remaining budget at
// zero, or the secondary limit's message:
// https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api#exceeding-the-rate-limit
func rateLimited(status int, header http.Header, said string) bool {
	switch status {
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		remaining, err := strconv.Atoi(strings.TrimSpace(header.Get("X-RateLimit-Remaining")))
		return header.Get("Retry-After") != "" || (err == nil && remaining <= 0) ||
			strings.Contains(strings.ToLower(said), "secondary rate limit")
	}
	return false
}

// limitWait is the wait a rate limit states: Retry-After, else the primary
// window's reset where the remaining budget is zero, capped at an hour; zero
// where it states neither. A reset already passed, or a Retry-After of 0, also
// answers zero: only host clock skew or the window's last second produces one,
// and the header-less schedule keeps a skewed host from resending into the limit.
func limitWait(header http.Header, now time.Time) time.Duration {
	if wait := transport.RetryAfter(header); wait > 0 {
		return min(wait, statedCeiling)
	}
	remaining, err := strconv.Atoi(strings.TrimSpace(header.Get("X-RateLimit-Remaining")))
	if err != nil || remaining > 0 {
		return 0
	}
	reset, err := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Reset")), 10, 64)
	if err != nil {
		return 0
	}
	return min(max(time.Unix(reset, 0).Sub(now), 0), statedCeiling)
}

// githubMessage is the message member of a GitHub refusal, empty where the
// body carries none. It is GitHub's text, so a caller quotes it through the
// exchange's [transport.Response.Quote].
func githubMessage(body []byte) string {
	var refusal struct {
		Message string `json:"message"`
	}
	if transport.Decode(body, &refusal) != nil {
		return ""
	}
	return refusal.Message
}

// joined is the package's own reason followed by GitHub's.
func joined(why, said string) string {
	if said == "" {
		return why
	}
	return why + ": " + said
}

// retryable reports whether another attempt of a failed mint may succeed.
func retryable(err error) bool {
	fe, ok := errors.AsType[*forgeapi.Error](err)
	return ok && fe.Retryable
}

// backOff waits before the attempt-th try, doubling from retryBase, and
// reports whether the try may be sent: not where ctx's deadline cannot hold
// the wait, nor where ctx ends during it.
func (s *AppSource) backOff(ctx context.Context, op string, attempt int) bool {
	wait := retryBase << (attempt - 1)
	if !transport.WaitFits(ctx, wait) {
		return false
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return false
	}
	if s.set.Counters.Retried != nil {
		s.set.Counters.Retried(forgeapi.FamilyGitHub, op)
	}
	return true
}
