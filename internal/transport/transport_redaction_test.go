package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// heldCredential is a credential source holding one given token.
type heldCredential string

func (c heldCredential) Token(context.Context) (string, error) { return string(c), nil }
func (heldCredential) Kind() forgeapi.CredKind                 { return forgeapi.CredKindStaticPAT }
func (heldCredential) State() forgeapi.CredState               { return forgeapi.CredValid }

// TestARefusalCarriesNoSecretTheRequestHeld holds a refusal's text free of the
// credential and of each connection header value, whichever member echoes them and
// however the sanitizer rewrites the text around them.
//
// The control-byte row's token is whole only once the sanitizer turns the control
// into its space; the tab row's token is whole only as sent, since the sanitizer
// rewrites its tab, and its tail is what would survive.
func TestARefusalCarriesNoSecretTheRequestHeld(t *testing.T) {
	const gateway = "gateway-secret-0123"
	for _, test := range []struct {
		name   string
		token  string
		body   string
		absent string
		kept   string
	}{
		{
			name: "message_member_echoing_the_header", token: "held-token-0123",
			body:   `{"message":"cannot use credential Bearer held-token-0123"}`,
			absent: "held-token-0123", kept: "cannot use credential",
		},
		{
			name: "description_echoing_the_token", token: "held-token-0123",
			body:   `{"error":"invalid_token","error_description":"token held-token-0123 is revoked"}`,
			absent: "held-token-0123", kept: "is revoked",
		},
		{
			name: "message_object_carrying_the_token_as_JSON_escapes_it", token: `held"<token>-0123`,
			body:   `{"message":{"credential":["held\"\u003ctoken\u003e-0123 refused"]}}`,
			absent: "token", kept: "refused",
		},
		{
			name: "control_byte_the_sanitizer_turns_into_the_tokens_space", token: "held token-0123",
			body:   `{"message":"refused held\u001btoken-0123 here"}`,
			absent: "held token-0123", kept: "refused",
		},
		{
			name: "tab_inside_the_token_the_sanitizer_rewrites", token: "held\ttoken-0123",
			body:   `{"message":"refused held\ttoken-0123 here"}`,
			absent: "token-0123", kept: "refused",
		},
		{
			name: "connection_header_value", token: "held-token-0123",
			body:   `{"message":"gateway refused key ` + gateway + `"}`,
			absent: gateway, kept: "gateway refused key",
		},
		{
			name: "secret_past_the_bound", token: "held-token-0123",
			body:   `{"message":"` + strings.Repeat("x", 500) + ` held-token-0123"}`,
			absent: "held-t", kept: "xxx",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fe := echoedRefusal(t, test.token, gateway, test.body)
			for _, text := range []string{fe.Message, fe.Error()} {
				if strings.Contains(text, test.absent) {
					t.Errorf("Do refused with %q, want no %q: the instance's text echoed a secret the request carried", text, test.absent)
				}
			}
			if !strings.Contains(fe.Message, test.kept) {
				t.Errorf("Do refused with message %q, want it to keep %q: the instance's own words reach the caller", fe.Message, test.kept)
			}
		})
	}
}

// TestARefusalRedactsEveryRuneASecretTouches holds a refusal's text to one mark over
// every rune an occurrence of a secret covers, in the text as sent or as sanitized,
// so no part of a secret survives beside the mark.
func TestARefusalRedactsEveryRuneASecretTouches(t *testing.T) {
	for _, test := range []struct {
		name    string
		token   string
		gateway string
		body    string
		want    string
	}{
		{
			name: "occurrence_the_sanitizer_completes_over_an_earlier_one", token: "a a", gateway: "gateway-secret-0123",
			body: `{"message":"refused a\ra a"}`,
			want: "refused [redacted]",
		},
		{
			name: "header_values_JSON_form_overlapping_the_token", token: "key-0123", gateway: `gw"key`,
			body: `{"message":{"credential":["refused gw\"key-0123"]}}`,
			want: `{"credential":["refused [redacted]"]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fe := echoedRefusal(t, test.token, test.gateway, test.body)
			if fe.Message != test.want {
				t.Errorf("Do refused with message %q, want %q", fe.Message, test.want)
			}
		})
	}
}

// TestARefusalCarriesNoSecretAsTheInstanceReceivedIt holds a refusal's text free of a
// secret echoed as it arrived, its edge whitespace trimmed off the field, and of a
// secret an object message spells with any of JSON's escapes.
func TestARefusalCarriesNoSecretAsTheInstanceReceivedIt(t *testing.T) {
	for _, test := range []struct {
		name    string
		token   string
		gateway string
		answer  func(*http.Request) string
		absent  []string
	}{
		{
			name: "header_value_echoed_without_its_edge_whitespace", token: "held-token-0123", gateway: " \tgateway-secret-0123\t ",
			answer: func(r *http.Request) string { return `{"message":"key ` + r.Header.Get("X-Gateway-Key") + ` refused"}` },
			absent: []string{"gateway-secret", "0123"},
		},
		{
			name: "token_echoed_without_its_trailing_whitespace", token: "held-token-0123 \t", gateway: "gateway-secret-0123",
			answer: func(r *http.Request) string { return `{"message":"` + r.Header.Get("Authorization") + ` refused"}` },
			absent: []string{"held-token", "0123"},
		},
		{
			name: "object_message_escaping_the_solidus", token: "held-token-0123", gateway: "gateway/secret",
			answer: func(*http.Request) string { return `{"message":{"key":["gateway\/secret refused"]}}` },
			absent: []string{"gateway", "secret"},
		},
		{
			name: "object_message_with_an_upper_case_unicode_escape", token: "held-token-0123", gateway: "gateway/secret",
			answer: func(*http.Request) string { return `{"message":{"key":["gateway\u002Fsecret refused"]}}` },
			absent: []string{"gateway", "secret"},
		},
		{
			name: "object_message_with_a_surrogate_pair", token: "held-token-0123", gateway: "gateway\U0001F600secret",
			answer: func(*http.Request) string { return `{"message":{"key":["gateway\uD83D\uDE00secret refused"]}}` },
			absent: []string{"gateway", "secret"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fe := refusalTo(t, test.token, test.gateway, test.answer)
			for _, absent := range test.absent {
				if strings.Contains(fe.Message, absent) {
					t.Errorf("Do refused with message %q, want no %q: the instance's text echoed a secret the request carried", fe.Message, absent)
				}
			}
			if !strings.Contains(fe.Message, "refused") {
				t.Errorf("Do refused with message %q, want it to keep %q: the instance's own words reach the caller", fe.Message, "refused")
			}
		})
	}
}

// echoedRefusal is the error a connection holding token and sending gateway as its own
// header value answers with when the instance refuses with body.
func echoedRefusal(t *testing.T, token, gateway, body string) *forgeapi.Error {
	t.Helper()
	return refusalTo(t, token, gateway, func(*http.Request) string { return body })
}

// refusalTo is echoedRefusal with the refusal's body built from the request the
// instance received.
func refusalTo(t *testing.T, token, gateway string, answer func(*http.Request) string) *forgeapi.Error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		if _, err := io.WriteString(w, answer(r)); err != nil {
			t.Errorf("Setup: writing the refusal: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	c := openTestConn(t, forgeapi.Connection{
		WebBaseURL: srv.URL,
		Headers:    []forgeapi.Header{{Name: "X-Gateway-Key", Value: gateway}},
	}, forgeapi.WithCredentialSource(heldCredential(token)))

	_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("Do against a 400 = %v, want a *forgeapi.Error", err)
	}
	return fe
}
