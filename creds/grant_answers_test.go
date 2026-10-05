package creds_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

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
}
