package creds_test

import (
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/creds"
)

// GitHub separates the scopes it granted with commas.
func TestDeviceGrant_reads_comma_separated_scopes(t *testing.T) {
	const granted = `{"access_token":"token-granted","token_type":"bearer","scope":"repo,read:org"}`
	ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: http.StatusOK, body: granted}))
	g := startGrant(t, ep, forgeapi.FamilyGitHub)

	rec, done, err := g.Poll(t.Context())
	if err != nil || !done {
		t.Fatalf("Poll() = (_, %v, %v), want the record and true", done, err)
	}
	if want := []string{"repo", "read:org"}; !slices.Equal(rec.Scopes, want) {
		t.Errorf("Scopes = %v, want %v", rec.Scopes, want)
	}
}

func TestDeviceGrant_answers_an_approved_grant_again_without_a_request(t *testing.T) {
	const granted = `{"access_token":"token-granted","token_type":"bearer","scope":"repo"}`
	ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: http.StatusOK, body: granted}))
	g := startGrant(t, ep, forgeapi.FamilyGitHub)

	if _, done, err := g.Poll(t.Context()); err != nil || !done {
		t.Fatalf("first Poll() = (_, %v, %v), want the record and true", done, err)
	}
	rec, done, err := g.Poll(t.Context())
	if err != nil || !done || rec.Token.Reveal() != "token-granted" {
		t.Errorf("second Poll() = (token %q, %v, %v), want the same record and true", rec.Token.Reveal(), done, err)
	}
	if n := len(ep.requests()); n != 2 {
		t.Errorf("the grant sent %d request(s), want the device-code request and one poll: a used device code is not sent again", n)
	}
}

func TestStartDeviceGrant_refuses_an_answer_carrying_no_codes(t *testing.T) {
	ep := newEndpoint(t, func(int, exchange) (int, string) {
		return http.StatusOK, `{"error":"unauthorized_client"}`
	})

	g, err := creds.StartDeviceGrant(t.Context(), ep.conn(), forgeapi.FamilyGitHub,
		creds.GrantRequest{ClientID: clientID, Scopes: scopes}, ep.options()...)
	if g != nil || codeOf(err) != forgeapi.CodeValidation {
		t.Errorf("StartDeviceGrant over an error answer = (%v, %v), want no grant and code %q", g, err, forgeapi.CodeValidation)
	}
}

func TestDeviceGrant_ends_on_a_token_whose_lifetime_reads_as_neither_kind(t *testing.T) {
	for name, body := range unreadableTokens {
		t.Run(name, func(t *testing.T) {
			ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: http.StatusOK, body: body}))
			g := startGrant(t, ep, forgeapi.FamilyGitHub)

			rec, done, err := g.Poll(t.Context())
			if code := codeOf(err); code != forgeapi.CodeValidation || done || !rec.Token.IsZero() {
				t.Errorf("Poll() over %s = (token %q, %v, %v), want no record and code %q", body, rec.Token.Reveal(), done, err, forgeapi.CodeValidation)
			}
			if _, _, again := g.Poll(t.Context()); codeOf(again) != forgeapi.CodeValidation {
				t.Errorf("a second Poll() = %v, want the same refusal: the instance spent the device code", again)
			}
			if n := len(ep.requests()); n != 2 {
				t.Errorf("the grant sent %d request(s), want the device-code request and one poll", n)
			}
		})
	}
}

func TestDeviceGrant_does_not_quote_an_error_member_no_document_defines(t *testing.T) {
	ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: http.StatusBadRequest, body: `{"error":"example-device-code"}`}))
	g := startGrant(t, ep, forgeapi.FamilyGitLab)

	_, _, err := g.Poll(t.Context())
	if err == nil {
		t.Fatal("Poll() = nil error, want the refusal of an answer that is no token")
	}
	if strings.Contains(err.Error(), "example-device-code") {
		t.Errorf("Poll() = error %q, want it free of the device code the endpoint echoed", err)
	}
	if msg := messageOf(err); !strings.Contains(msg, "no OAuth document defines") {
		t.Errorf("Poll() = message %q, want it to say the error member is one no OAuth document defines", msg)
	}
}

// RFC 8628 section 3.2: the answer's interval is the one polled at, five seconds
// where it states none.
func TestStartDeviceGrant_polls_at_the_interval_the_answer_states(t *testing.T) {
	const codes = `{"device_code":"example-device-code","user_code":"ABCD-1234","verification_uri":"https://forge.example/login/device","expires_in":900`
	for name, test := range map[string]struct {
		body string
		want time.Duration
	}{
		"stated":   {body: codes + `,"interval":7}`, want: 7 * time.Second},
		"unstated": {body: codes + `}`, want: 5 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			ep := newEndpoint(t, deviceFlow(test.body, answerBody{status: http.StatusOK, body: `{"error":"authorization_pending"}`}))
			if g := startGrant(t, ep, forgeapi.FamilyGitHub); g.Interval != test.want {
				t.Errorf("grant Interval over %s = %v, want %v", test.body, g.Interval, test.want)
			}
		})
	}
}

func TestStartDeviceGrant_sends_no_scope_where_none_was_asked_for(t *testing.T) {
	ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: http.StatusOK, body: `{"error":"authorization_pending"}`}))

	if _, err := creds.StartDeviceGrant(t.Context(), ep.conn(), forgeapi.FamilyGitHub,
		creds.GrantRequest{ClientID: clientID}, ep.options()...); err != nil {
		t.Fatalf("StartDeviceGrant(no scopes) = error %v, want a grant", err)
	}
	if reqs := ep.requests(); len(reqs) != 1 || reqs[0].form.Has("scope") {
		t.Errorf("StartDeviceGrant(no scopes) sent %+v, want one request carrying no scope member", reqs)
	}
}

// Every field RFC 8628 section 3.2 requires, in a success: a redirection status or a
// lifetime of zero is no device-code answer, whatever else it carries.
func TestStartDeviceGrant_refuses_an_answer_short_of_a_device_code_answer(t *testing.T) {
	for name, test := range map[string]struct {
		body   string
		status int
	}{
		"a_redirection_status": {status: http.StatusMultipleChoices, body: deviceCode("900")},
		"a_zero_lifetime":      {status: http.StatusOK, body: deviceCode("0")},
	} {
		t.Run(name, func(t *testing.T) {
			ep := newEndpoint(t, func(int, exchange) (int, string) { return test.status, test.body })

			g, err := creds.StartDeviceGrant(t.Context(), ep.conn(), forgeapi.FamilyGitHub,
				creds.GrantRequest{ClientID: clientID, Scopes: scopes}, ep.options()...)
			if g != nil || codeOf(err) != forgeapi.CodeValidation {
				t.Errorf("StartDeviceGrant over %d %s = (%v, %v), want no grant and code %q", test.status, test.body, g, err, forgeapi.CodeValidation)
			}
		})
	}
}

// A redirection status is no success, so it spends nothing and the grant stays open.
func TestDeviceGrant_stays_open_after_an_answer_with_a_redirection_status(t *testing.T) {
	ep := newEndpoint(t, deviceFlow(deviceCode("900"),
		answerBody{status: http.StatusMultipleChoices, body: `{}`},
		answerBody{status: http.StatusOK, body: `{"error":"authorization_pending"}`}))
	g := startGrant(t, ep, forgeapi.FamilyGitHub)

	if _, done, err := g.Poll(t.Context()); err == nil || done {
		t.Errorf("Poll() over a 300 = (%v, %v), want the refusal of an answer that is no token", done, err)
	}
	if rec, done, err := g.Poll(t.Context()); err != nil || done {
		t.Errorf("a second Poll() = (%+v, %v, %v), want (zero, false, nil) while authorization is pending", rec, done, err)
	}
	if n := len(ep.requests()); n != 3 {
		t.Errorf("the grant sent %d request(s), want the device-code request and two polls", n)
	}
}

func TestDeviceGrant_answers_a_server_failure_as_worth_another_attempt(t *testing.T) {
	ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: http.StatusInternalServerError, body: `{"error":"server_error"}`}))
	g := startGrant(t, ep, forgeapi.FamilyGitHub)

	_, _, err := g.Poll(t.Context())
	if fe, ok := errors.AsType[*forgeapi.Error](err); !ok || fe.Kind != forgeapi.KindTransient || !fe.Retryable {
		t.Errorf("Poll() over a 500 = error %v, want a retryable transient failure", err)
	}
}

// A token endpoint that timed out, throttled or failed spent nothing, so the grant
// stays pollable; a 400 naming no protocol outcome is not worth another attempt.
func TestATokenEndpointThatTimedOutOrThrottledLeavesTheGrantPollable(t *testing.T) {
	for _, test := range []struct {
		status    int
		kind      forgeapi.ErrorKind
		retryable bool
	}{
		{status: http.StatusRequestTimeout, kind: forgeapi.KindTransient, retryable: true},
		{status: http.StatusTooManyRequests, kind: forgeapi.KindRateLimited, retryable: true},
		{status: http.StatusServiceUnavailable, kind: forgeapi.KindTransient, retryable: true},
		{status: http.StatusBadRequest, kind: forgeapi.KindUpstream},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			ep := newEndpoint(t, deviceFlow(deviceCode("900"),
				answerBody{status: test.status, body: `{}`},
				answerBody{status: http.StatusOK, body: `{"error":"authorization_pending"}`}))
			g := startGrant(t, ep, forgeapi.FamilyGitHub)

			_, done, err := g.Poll(t.Context())
			fe, ok := errors.AsType[*forgeapi.Error](err)
			if !ok || done || fe.Kind != test.kind || fe.Retryable != test.retryable {
				t.Fatalf("Poll() over a %d = (%v, %v), want kind %v, retryable %v", test.status, done, err, test.kind, test.retryable)
			}
			if _, done, err := g.Poll(t.Context()); err != nil || done {
				t.Errorf("a second Poll() = (%v, %v), want (false, nil) while authorization is pending", done, err)
			}
		})
	}
}

// An OAuth endpoint's refusal carries the figures its answer states, read as the
// family's client reads them, and on a limit the wait its Retry-After asks for:
// GitLab's throttles name themselves on every answer, and a GitHub answer is a
// limit by the same reading its API's answers take.
func TestAnOAuthEndpointsRefusalCarriesItsAnswersFigures(t *testing.T) {
	reset := time.Unix(1790000000, 0)
	for _, test := range []struct {
		header    http.Header
		reset     time.Time
		name      string
		body      string
		pool      string
		family    forgeapi.Family
		status    int
		remaining int
		wait      time.Duration
		kind      forgeapi.ErrorKind
		secondary bool
	}{
		{
			name: "a_github_429_asking_a_wait", family: forgeapi.FamilyGitHub, status: http.StatusTooManyRequests,
			header: http.Header{"Retry-After": {"30"}}, body: `{}`,
			kind: forgeapi.KindRateLimited, remaining: forgeapi.BudgetRemainingUnknown, wait: 30 * time.Second, secondary: true,
		},
		{
			name: "a_github_400_beside_a_retry_after", family: forgeapi.FamilyGitHub, status: http.StatusBadRequest,
			header: http.Header{"Retry-After": {"30"}}, body: `{}`,
			kind: forgeapi.KindUpstream, remaining: forgeapi.BudgetRemainingUnknown,
		},
		{
			name: "a_github_403_with_the_budget_spent", family: forgeapi.FamilyGitHub, status: http.StatusForbidden,
			header: http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"1790000000"}, "X-Ratelimit-Resource": {"core"}},
			body:   `{"message":"API rate limit exceeded"}`,
			kind:   forgeapi.KindRateLimited, pool: "core", reset: reset,
		},
		{
			name: "a_github_403_carrying_the_secondary_message", family: forgeapi.FamilyGitHub, status: http.StatusForbidden,
			header: http.Header{"X-Ratelimit-Remaining": {"4000"}, "X-Ratelimit-Reset": {"1790000000"}, "X-Ratelimit-Resource": {"core"}},
			body:   `{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`,
			kind:   forgeapi.KindRateLimited, pool: "core", remaining: 4000, reset: reset, secondary: true,
		},
		{
			name: "a_gitlab_throttle", family: forgeapi.FamilyGitLab, status: http.StatusTooManyRequests,
			header: http.Header{
				"Retry-After": {"30"}, "Ratelimit-Name": {"throttle_unauthenticated_web"},
				"Ratelimit-Remaining": {"0"}, "Ratelimit-Reset": {"1790000000"},
			},
			body: `{}`, kind: forgeapi.KindRateLimited, pool: "throttle_unauthenticated_web", reset: reset, wait: 30 * time.Second,
		},
		{
			name: "a_gitlab_refusal_with_budget_left", family: forgeapi.FamilyGitLab, status: http.StatusBadRequest,
			header: http.Header{"Ratelimit-Name": {"throttle_unauthenticated_web"}, "Ratelimit-Remaining": {"10"}, "Ratelimit-Reset": {"1790000000"}},
			body:   `{}`, kind: forgeapi.KindUpstream, pool: "throttle_unauthenticated_web", remaining: 10, reset: reset,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				maps.Copy(w.Header(), test.header)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				if _, err := io.WriteString(w, test.body); err != nil {
					t.Errorf("Setup: writing the answer: %v", err)
				}
			}))
			t.Cleanup(srv.Close)
			ep := &endpoint{srv: srv}
			_, err := creds.StartDeviceGrant(t.Context(), ep.conn(), test.family,
				creds.GrantRequest{ClientID: clientID, Scopes: scopes}, ep.options()...)
			fe, ok := errors.AsType[*forgeapi.Error](err)
			if !ok || fe.Status != test.status || fe.Kind != test.kind {
				t.Fatalf("StartDeviceGrant() = %v, want a *forgeapi.Error of kind %v at status %d", err, test.kind, test.status)
			}
			if fe.Pool != test.pool || fe.Remaining != test.remaining || !fe.Reset.Equal(test.reset) {
				t.Errorf("StartDeviceGrant() = pool %q remaining %d reset %v, want the answer's %q, %d, %v",
					fe.Pool, fe.Remaining, fe.Reset, test.pool, test.remaining, test.reset)
			}
			if fe.RetryAfter != test.wait || fe.Secondary != test.secondary {
				t.Errorf("StartDeviceGrant() = retry after %v secondary %t, want %v, %t", fe.RetryAfter, fe.Secondary, test.wait, test.secondary)
			}
		})
	}
}
