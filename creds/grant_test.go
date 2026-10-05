package creds_test

import (
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// deviceArms are the two families whose products document a device grant: the path
// each answers the device-code request on, the path its token endpoint answers on,
// and how each spells an answer that is not yet a token.
var deviceArms = []struct {
	codePath    string
	tokenPath   string
	family      forgeapi.Family
	errorStatus int
}{
	{family: forgeapi.FamilyGitHub, codePath: "/login/device/code", tokenPath: "/login/oauth/access_token", errorStatus: http.StatusOK},
	{family: forgeapi.FamilyGitLab, codePath: "/oauth/authorize_device", tokenPath: "/oauth/token", errorStatus: http.StatusBadRequest},
}

// deviceCode is a device-code answer valid for expiresIn seconds at a five-second
// interval.
func deviceCode(expiresIn string) string {
	return `{"device_code":"example-device-code","user_code":"ABCD-1234","verification_uri":"https://forge.example/login/device","expires_in":` +
		expiresIn + `,"interval":5}`
}

// deviceFlow answers the device-code request with code, and every token request in
// turn with the next of tokens, the last one repeating.
func deviceFlow(code string, tokens ...answerBody) answer {
	var mu sync.Mutex
	return func(_ int, x exchange) (int, string) {
		if strings.HasSuffix(x.path, "/device/code") || strings.HasSuffix(x.path, "/authorize_device") {
			return http.StatusOK, code
		}
		mu.Lock()
		defer mu.Unlock()
		next := tokens[0]
		if len(tokens) > 1 {
			tokens = tokens[1:]
		}
		return next.status, next.body
	}
}

// answerBody is one canned token-endpoint answer.
type answerBody struct {
	body   string
	status int
}

var scopes = []string{"repo", "read:org"}

func startGrant(t *testing.T, ep *endpoint, family forgeapi.Family) *creds.DeviceGrant {
	t.Helper()
	g, err := creds.StartDeviceGrant(t.Context(), ep.conn(), family,
		creds.GrantRequest{ClientID: clientID, Scopes: scopes}, ep.options()...)
	if err != nil {
		t.Fatalf("StartDeviceGrant(%v) = error %v, want a grant", family, err)
	}
	return g
}

func TestStartDeviceGrant_asks_for_a_code_for_the_consumers_client_and_scopes(t *testing.T) {
	for _, arm := range deviceArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: arm.errorStatus, body: `{"error":"authorization_pending"}`}))
			before := time.Now()
			g := startGrant(t, ep, arm.family)

			reqs := ep.requests()
			if len(reqs) != 1 {
				t.Fatalf("StartDeviceGrant sent %d request(s), want 1", len(reqs))
			}
			x := reqs[0]
			if x.method != http.MethodPost || x.path != arm.codePath {
				t.Errorf("the device-code request was %s %s, want POST %s", x.method, x.path, arm.codePath)
			}
			if got := x.form.Get("client_id"); got != clientID {
				t.Errorf("form client_id = %q, want %q", got, clientID)
			}
			if got := x.form.Get("scope"); got != "repo read:org" {
				t.Errorf("form scope = %q, want the scopes space-separated, %q", got, "repo read:org")
			}
			if x.form.Has("client_secret") || x.query.Has("client_secret") {
				t.Error("the device-code request sent client_secret, want none")
			}
			if g.UserCode != "ABCD-1234" || g.VerificationURI != "https://forge.example/login/device" {
				t.Errorf("grant = (%q, %q), want the user code and verification address the answer carried", g.UserCode, g.VerificationURI)
			}
			if want := before.Add(900 * time.Second); !near(g.Expires, want, time.Minute) {
				t.Errorf("grant Expires = %v, want about %v, from expires_in", g.Expires, want)
			}
			if g.Interval != 5*time.Second {
				t.Errorf("grant Interval = %v, want 5s, from interval", g.Interval)
			}
		})
	}
}

func TestStartDeviceGrant_refuses_the_gitea_family_before_any_request(t *testing.T) {
	ep := newEndpoint(t, unreachable(t))

	_, err := creds.StartDeviceGrant(t.Context(), ep.conn(), forgeapi.FamilyGitea,
		creds.GrantRequest{ClientID: clientID, Scopes: scopes}, ep.options()...)
	if code := codeOf(err); code != forgeapi.CodeGrantUnsupported {
		t.Errorf("StartDeviceGrant(Gitea) = error %v (code %q), want code %q", err, code, forgeapi.CodeGrantUnsupported)
	}
}

func TestDeviceGrant_polls_once_per_call_while_authorization_is_pending(t *testing.T) {
	for _, arm := range deviceArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: arm.errorStatus, body: `{"error":"authorization_pending"}`}))
			g := startGrant(t, ep, arm.family)

			rec, done, err := g.Poll(t.Context())
			if err != nil || done {
				t.Errorf("Poll() = (%+v, %v, %v), want (zero, false, nil) while authorization is pending", rec, done, err)
			}
			reqs := ep.requests()
			if len(reqs) != 2 {
				t.Fatalf("the grant sent %d request(s), want the device-code request and one poll", len(reqs))
			}
			x := reqs[1]
			if x.method != http.MethodPost || x.path != arm.tokenPath {
				t.Errorf("the poll was %s %s, want POST %s", x.method, x.path, arm.tokenPath)
			}
			want := map[string]string{
				"client_id":   clientID,
				"device_code": "example-device-code",
				"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
			}
			for name, value := range want {
				if got := x.form.Get(name); got != value {
					t.Errorf("poll form %s = %q, want %q", name, got, value)
				}
			}
			if x.form.Has("client_secret") || x.query.Has("client_secret") {
				t.Error("the poll sent client_secret, want none")
			}
		})
	}
}

func TestDeviceGrant_widens_the_interval_on_slow_down(t *testing.T) {
	tests := []struct {
		body   string
		family forgeapi.Family
		status int
		want   time.Duration
	}{
		// GitHub's slow_down carries the new interval, which is the one taken.
		{family: forgeapi.FamilyGitHub, status: http.StatusOK, body: `{"error":"slow_down","interval":12}`, want: 12 * time.Second},
		// An answer carrying none widens the interval by five seconds.
		{family: forgeapi.FamilyGitLab, status: http.StatusBadRequest, body: `{"error":"slow_down"}`, want: 10 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.family.String(), func(t *testing.T) {
			ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: tc.status, body: tc.body}))
			g := startGrant(t, ep, tc.family)

			rec, done, err := g.Poll(t.Context())
			if err != nil || done {
				t.Errorf("Poll() = (%+v, %v, %v), want (zero, false, nil) on slow_down", rec, done, err)
			}
			if g.Interval != tc.want {
				t.Errorf("Interval after slow_down = %v, want %v", g.Interval, tc.want)
			}
		})
	}
}

func TestDeviceGrant_ends_terminally_when_the_user_denies_it(t *testing.T) {
	for _, arm := range deviceArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: arm.errorStatus, body: `{"error":"access_denied"}`}))
			g := startGrant(t, ep, arm.family)

			for call := 1; call <= 2; call++ {
				if _, done, err := g.Poll(t.Context()); done || codeOf(err) != forgeapi.CodeGrantDenied {
					t.Errorf("Poll() call %d = (done %v, error %v), want code %q", call, done, err, forgeapi.CodeGrantDenied)
				}
			}
			if n := len(ep.requests()); n != 2 {
				t.Errorf("the grant sent %d request(s), want the device-code request and one poll: a denied grant is not polled again", n)
			}
		})
	}
}

func TestDeviceGrant_ends_terminally_when_the_codes_expire_upstream(t *testing.T) {
	for _, arm := range deviceArms {
		t.Run(arm.family.String(), func(t *testing.T) {
			ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: arm.errorStatus, body: `{"error":"expired_token"}`}))
			g := startGrant(t, ep, arm.family)

			if _, done, err := g.Poll(t.Context()); done || codeOf(err) != forgeapi.CodeGrantExpired {
				t.Errorf("Poll() = (done %v, error %v), want code %q", done, err, forgeapi.CodeGrantExpired)
			}
		})
	}
}

func TestDeviceGrant_ends_terminally_when_polled_after_the_codes_own_expiry(t *testing.T) {
	ep := newEndpoint(t, deviceFlow(deviceCode("1"), answerBody{status: http.StatusOK, body: `{"error":"authorization_pending"}`}))
	g := startGrant(t, ep, forgeapi.FamilyGitHub)

	// The codes live one second; the poll comes after it.
	time.Sleep(time.Until(g.Expires) + 100*time.Millisecond)
	if _, done, err := g.Poll(t.Context()); done || codeOf(err) != forgeapi.CodeGrantExpired {
		t.Errorf("Poll() after the codes expired = (done %v, error %v), want code %q", done, err, forgeapi.CodeGrantExpired)
	}
}

func TestDeviceGrant_answers_a_rotating_record_from_a_response_that_states_an_expiry(t *testing.T) {
	const granted = `{"access_token":"token-granted","token_type":"bearer","scope":"repo","expires_in":28800,"refresh_token":"refresh-granted","refresh_token_expires_in":15897600}`
	ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: http.StatusOK, body: granted}))
	g := startGrant(t, ep, forgeapi.FamilyGitHub)

	before := time.Now()
	rec, done, err := g.Poll(t.Context())
	if err != nil || !done {
		t.Fatalf("Poll() = (_, %v, %v), want the record and true", done, err)
	}
	if rec.Kind != forgeapi.CredKindRotatingOAuth {
		t.Errorf("Kind = %v, want %v: the response states an expiry", rec.Kind, forgeapi.CredKindRotatingOAuth)
	}
	if rec.Token != "token-granted" || rec.RefreshToken != "refresh-granted" {
		t.Errorf("pair = (%q, %q), want the granted pair", rec.Token, rec.RefreshToken)
	}
	if rec.Family != forgeapi.FamilyGitHub || rec.WebBaseURL != ep.srv.URL || rec.ClientID != clientID {
		t.Errorf("record names (%v, %q, %q), want the family, the web base and the client id the grant ran under",
			rec.Family, rec.WebBaseURL, rec.ClientID)
	}
	if !slices.Equal(rec.Scopes, []string{"repo"}) {
		t.Errorf("Scopes = %v, want [repo], as the response stated them", rec.Scopes)
	}
	if !near(rec.Issued, before, time.Minute) {
		t.Errorf("Issued = %v, want about %v", rec.Issued, before)
	}
	if want := before.Add(28800 * time.Second); !near(rec.Expiry, want, time.Minute) {
		t.Errorf("Expiry = %v, want about %v, from expires_in", rec.Expiry, want)
	}
	if want := before.Add(15897600 * time.Second); !near(rec.RefreshExpiry, want, time.Minute) {
		t.Errorf("RefreshExpiry = %v, want about %v, from refresh_token_expires_in", rec.RefreshExpiry, want)
	}
}

// What a GitHub Enterprise Server instance may answer, and an application with
// expiring tokens switched off does: no expiry and no refresh token.
func TestDeviceGrant_answers_a_static_record_from_a_response_that_states_no_expiry(t *testing.T) {
	const granted = `{"access_token":"token-granted","token_type":"bearer","scope":"repo"}`
	ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: http.StatusOK, body: granted}))
	g := startGrant(t, ep, forgeapi.FamilyGitHub)

	rec, done, err := g.Poll(t.Context())
	if err != nil || !done {
		t.Fatalf("Poll() = (_, %v, %v), want the record and true", done, err)
	}
	if rec.Kind != forgeapi.CredKindStaticPAT {
		t.Errorf("Kind = %v, want %v: the response states no expiry and no refresh token", rec.Kind, forgeapi.CredKindStaticPAT)
	}
	if !rec.Expiry.IsZero() || rec.RefreshToken != "" || !rec.RefreshExpiry.IsZero() {
		t.Errorf("record = (Expiry %v, RefreshToken %q, RefreshExpiry %v), want all three empty", rec.Expiry, rec.RefreshToken, rec.RefreshExpiry)
	}
}

// GitLab's documented device-grant answer states an expiry and carries no refresh
// token, so the record rotates by kind and has nothing to rotate with.
func TestDeviceGrant_answers_a_rotating_record_with_no_refresh_token_where_none_was_granted(t *testing.T) {
	const granted = `{"access_token":"token-granted","token_type":"Bearer","expires_in":7200,"scope":"repo","created_at":1790000000}`
	ep := newEndpoint(t, deviceFlow(deviceCode("300"), answerBody{status: http.StatusOK, body: granted}))
	g := startGrant(t, ep, forgeapi.FamilyGitLab)

	before := time.Now()
	rec, done, err := g.Poll(t.Context())
	if err != nil || !done {
		t.Fatalf("Poll() = (_, %v, %v), want the record and true", done, err)
	}
	if rec.Kind != forgeapi.CredKindRotatingOAuth || rec.RefreshToken != "" {
		t.Errorf("record = (Kind %v, RefreshToken %q), want a rotating record with no refresh token", rec.Kind, rec.RefreshToken)
	}
	if want := before.Add(7200 * time.Second); !near(rec.Expiry, want, time.Minute) {
		t.Errorf("Expiry = %v, want about %v, from expires_in", rec.Expiry, want)
	}
}
