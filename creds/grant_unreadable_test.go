package creds_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// grantedPair is a token endpoint's success for a device grant, the body a poll
// that the user approved answers with.
const grantedPair = `{"access_token":"token-granted","token_type":"bearer","scope":"repo","expires_in":28800,"refresh_token":"refresh-granted","refresh_token_expires_in":15897600}`

// pollAnswer is how the token endpoint below answers a poll: the status, the body,
// and whether the connection drops after the status line and half the body the
// headers declare. A cut answer with status zero drops before any status line.
type pollAnswer struct {
	body   string
	status int
	cut    bool
}

// grantEndpoint answers the device-code request whole and every poll as the case
// says, recording every request.
func grantEndpoint(t *testing.T, poll pollAnswer) *endpoint {
	t.Helper()
	ep := &endpoint{}
	ep.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Errorf("Setup: reading the request body: %v", err)
		}
		form, err := url.ParseQuery(string(raw))
		if err != nil {
			t.Errorf("Setup: the request body %q is not a form: %v", raw, err)
		}
		ep.mu.Lock()
		ep.seen = append(ep.seen, exchange{header: r.Header.Clone(), form: form, query: r.URL.Query(), method: r.Method, path: r.URL.Path})
		ep.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/device/code") || strings.HasSuffix(r.URL.Path, "/authorize_device") {
			w.Header().Set("Content-Type", "application/json")
			if _, err := io.WriteString(w, deviceCode("900")); err != nil {
				t.Errorf("Setup: writing the device code: %v", err)
			}
			return
		}
		if !poll.cut {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(poll.status)
			if _, err := io.WriteString(w, poll.body); err != nil {
				t.Errorf("Setup: writing the poll's answer: %v", err)
			}
			return
		}
		cutAnswer(t, w, poll)
	}))
	t.Cleanup(ep.srv.Close)
	return ep
}

// cutAnswer drops the connection inside the poll's answer.
func cutAnswer(t *testing.T, w http.ResponseWriter, poll pollAnswer) {
	t.Helper()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		t.Errorf("Setup: the test server's writer cannot be hijacked")
		return
	}
	conn, out, err := hijacker.Hijack()
	if err != nil {
		t.Errorf("Setup: hijacking the connection: %v", err)
		return
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Errorf("Setup: closing the hijacked connection: %v", err)
		}
	}()
	if poll.status == 0 {
		return
	}
	if _, err := fmt.Fprintf(out, "HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		poll.status, http.StatusText(poll.status), len(poll.body), poll.body[:len(poll.body)/2]); err != nil {
		t.Errorf("Setup: writing the cut answer: %v", err)
	}
	if err := out.Flush(); err != nil {
		t.Errorf("Setup: flushing the cut answer: %v", err)
	}
}

// checkGrantEnded holds a grant to the end an unreadable success gives it: the
// validation code with the success's own status and the family, no record, and the
// same answer on the next poll with nothing sent, since the instance spent the
// device code on a token this library cannot read.
func checkGrantEnded(t *testing.T, ep *endpoint, g *creds.DeviceGrant, family forgeapi.Family, status int, answer string) {
	t.Helper()
	rec, done, err := g.Poll(t.Context())
	fe, isForge := errors.AsType[*forgeapi.Error](err)
	switch {
	case done || rec.Token != "":
		t.Errorf("Poll() over %s = (token %q, %v, %v), want no record", answer, rec.Token, done, err)
	case !isForge || fe.Code != forgeapi.CodeValidation:
		t.Errorf("Poll() over %s = error %v, want code %q: the instance issued a token this library cannot read", answer, err, forgeapi.CodeValidation)
	case fe.Status != status || fe.Family != family:
		t.Errorf("Poll() over %s = (status %d, family %v), want (%d, %v): the refusal carries the answer's real status", answer, fe.Status, fe.Family, status, family)
	}
	sent := len(ep.requests())
	if _, _, again := g.Poll(t.Context()); codeOf(again) != forgeapi.CodeValidation {
		t.Errorf("a second Poll() over %s = %v, want code %q again: the grant ended", answer, again, forgeapi.CodeValidation)
	}
	if n := len(ep.requests()); n != sent {
		t.Errorf("a second Poll() over %s sent %d request(s), want none: a spent device code is not sent again", answer, n-sent)
	}
}

// A success carrying no error member is an issued token, so a poll whose success
// arrived cut off spent the device code on a token that never reached this library:
// the grant ends rather than asking again with a code that can no longer succeed.
func TestDeviceGrant_ends_on_a_success_whose_body_did_not_arrive_whole(t *testing.T) {
	for _, arm := range deviceArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			ep := grantEndpoint(t, pollAnswer{status: http.StatusOK, body: grantedPair, cut: true})
			g := startGrant(t, ep, arm.family)

			checkGrantEnded(t, ep, g, arm.family, http.StatusOK, "a 200 cut off mid-body")
		})
	}
}

// A success whose body arrived whole and does not decode, or decodes and holds no
// access token, is a token the instance issued and this library cannot read, so the
// grant ends the same way.
func TestDeviceGrant_ends_on_a_success_it_cannot_read_a_token_from(t *testing.T) {
	bodies := map[string]string{
		"not_json":         `<html><body>Signed in</body></html>`,
		"not_an_object":    `["token-granted"]`,
		"no_access_token":  `{"token_type":"bearer","scope":"repo","expires_in":28800}`,
		"empty_object":     `{}`,
		"empty_token":      `{"access_token":"","token_type":"bearer","scope":"repo"}`,
		"truncated_object": `{"access_token":"token-granted","token_type":`,
	}
	statuses := map[forgeapi.Family][]int{
		forgeapi.FamilyGitHub: {http.StatusOK},
		forgeapi.FamilyGitLab: {http.StatusOK, http.StatusCreated},
	}
	for _, arm := range deviceArms {
		for _, status := range statuses[arm.family] {
			for name, body := range bodies {
				t.Run(arm.family.String()+"_"+strconv.Itoa(status)+"_"+name, func(t *testing.T) {
					ep := grantEndpoint(t, pollAnswer{status: status, body: body})
					g := startGrant(t, ep, arm.family)

					checkGrantEnded(t, ep, g, arm.family, status, body)
				})
			}
		}
	}
}

// An instance that answered a failure status, or answered nothing, issued nothing,
// so the device code is still good whatever reached the body: the poll answers the
// failure and the next one asks again.
func TestDeviceGrant_stays_open_after_a_poll_that_issued_nothing_whatever_reached_its_body(t *testing.T) {
	answers := map[string]pollAnswer{
		"failure_status_cut_off": {status: http.StatusServiceUnavailable, body: `{"error":"temporarily_unavailable","error_description":"The service is unavailable."}`, cut: true},
		"no_status":              {cut: true},
	}
	for _, arm := range deviceArms {
		for name, answer := range answers {
			t.Run(arm.family.String()+"_"+name, func(t *testing.T) {
				ep := grantEndpoint(t, answer)
				g := startGrant(t, ep, arm.family)

				rec, done, err := g.Poll(t.Context())
				if err == nil || done || rec.Token != "" {
					t.Errorf("Poll() over a poll that issued nothing = (token %q, %v, %v), want no record and the failure", rec.Token, done, err)
				}
				if code := codeOf(err); code == forgeapi.CodeValidation || code == forgeapi.CodeGrantDenied || code == forgeapi.CodeGrantExpired {
					t.Errorf("Poll() over a poll that issued nothing = code %q, want an answer that leaves the grant open", code)
				}
				sent := len(ep.requests())
				if _, _, again := g.Poll(t.Context()); again == nil {
					t.Errorf("a second Poll() over a poll that issued nothing = nil error, want the same failure again")
				}
				if n := len(ep.requests()); n != sent+1 {
					t.Errorf("a second Poll() sent %d request(s), want 1: the device code is still good, so the grant asks again", n-sent)
				}
			})
		}
	}
}
