package creds_test

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
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
	if err != nil || !done || rec.Token != "token-granted" {
		t.Errorf("second Poll() = (token %q, %v, %v), want the same record and true", rec.Token, done, err)
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
			if code := codeOf(err); code != forgeapi.CodeValidation || done || rec.Token != "" {
				t.Errorf("Poll() over %s = (token %q, %v, %v), want no record and code %q", body, rec.Token, done, err, forgeapi.CodeValidation)
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
