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

// TestARefusalCarriesNoSecretTheRequestHeld holds the refusal text an instance
// answers to what the request carried: the credential and each of the connection's
// own header values are redacted out of it wherever it echoes them, whichever member
// it echoes them in and however the sanitizer rewrites the text around them.
//
// Two rows pin the two redactions apart. A token carrying a space, echoed with a
// control byte in that place, is only whole once the sanitizer has turned the
// control into a space, so only the redaction after the sanitizer finds it. A token
// carrying a tab, echoed verbatim, loses the tab to the sanitizer, so only the
// redaction before it finds it whole, and its tail is what would survive.
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
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				if _, err := io.WriteString(w, test.body); err != nil {
					t.Errorf("Setup: writing the refusal: %v", err)
				}
			}))
			defer srv.Close()
			c := openTestConn(t, forgeapi.Connection{
				WebBaseURL: srv.URL,
				Headers:    []forgeapi.Header{{Name: "X-Gateway-Key", Value: gateway}},
			}, forgeapi.WithCredentialSource(heldCredential(test.token)))

			_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
			var fe *forgeapi.Error
			if !asError(err, &fe) {
				t.Fatalf("Do against a 400 = %v, want a *forgeapi.Error", err)
			}
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
