package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/go/masked"
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
		Headers:    []forgeapi.Header{{Name: "X-Gateway-Key", Value: masked.NewSecret(gateway)}},
	}, forgeapi.WithCredentialSource(heldCredential(token)))

	_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("Do against a 400 = %v, want a *forgeapi.Error", err)
	}
	return fe
}

// proxyPassword is the password the proxy tests below hand a connection, and
// proxyBasic its RFC 7617 section 2 Basic credentials beside the user-id
// "forge-client".
const proxyPassword = "proxy-secret-0123"

var proxyBasic = base64.StdEncoding.EncodeToString([]byte("forge-client:" + proxyPassword))

// TestAProxysAnswerCarriesNoneOfTheProxyCredentials holds a plaintext request's
// refusal free of the proxy password and of the Basic credentials net/http sends
// the proxy, since a proxy answers such a request itself.
func TestAProxysAnswerCarriesNoneOfTheProxyCredentials(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		body := `{"message":"refused ` + proxyPassword + ` sent as ` + r.Header.Get("Proxy-Authorization") + `"}`
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("Setup: writing the proxy's refusal: %v", err)
		}
	}))
	t.Cleanup(proxy.Close)
	var buf bytes.Buffer
	c := openProxiedConn(t, "http://forge.example.com", proxy.URL, &buf)

	_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("Do against a proxy's 400 = %v, want a *forgeapi.Error", err)
	}
	assertNoProxyCredential(t, fe, &buf)
}

// TestAProxysConnectRefusalCarriesNoneOfTheProxyCredentials holds the transport
// error of a refused CONNECT free of the proxy password and its Basic
// credentials, on the error and on every line logged for the request, its retry
// included: net/http returns the proxy's reason phrase as the error's text.
func TestAProxysConnectRefusalCarriesNoneOfTheProxyCredentials(t *testing.T) {
	proxy := rawListener(t, func(req *http.Request) string {
		return "HTTP/1.1 403 refused " + proxyPassword + " sent as " + req.Header.Get("Proxy-Authorization") + "\r\nContent-Length: 0\r\n\r\n"
	})
	var buf bytes.Buffer
	c := openProxiedConn(t, "https://forge.example.com", "http://"+proxy, &buf, forgeapi.WithRetries(1))

	_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("Do through a proxy refusing CONNECT = %v, want a *forgeapi.Error", err)
	}
	if !strings.Contains(buf.String(), `"retried_on"`) {
		t.Fatalf("the log = %s, want a line naming what the retry was on", buf.String())
	}
	assertNoProxyCredential(t, fe, &buf)
}

// TestALostAnswerCarriesNoSecretTheRequestHeld holds the failure of an answer
// whose body could not be read free of every secret the request carried: net/http
// quotes a malformed chunked trailer line in its error, and a proxy or an instance
// may echo the proxy password, its Basic credentials, the credential or a
// connection header value there.
func TestALostAnswerCarriesNoSecretTheRequestHeld(t *testing.T) {
	const token, gateway = "token-secret-0123", "gateway-secret-0123"
	proxy := rawListener(t, func(req *http.Request) string {
		return "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n" +
			"2\r\n{}\r\n0\r\nrefused " + proxyPassword + " " + req.Header.Get("Proxy-Authorization") + " " +
			req.Header.Get("Authorization") + " " + req.Header.Get("X-Gateway-Key") + "\r\n\r\n"
	})
	var buf bytes.Buffer
	conn := forgeapi.Connection{
		WebBaseURL: "http://forge.example.com", Proxy: "http://" + proxy,
		ProxyUsername: "forge-client", ProxyPassword: masked.NewSecret(proxyPassword),
		Headers: []forgeapi.Header{{Name: "X-Gateway-Key", Value: masked.NewSecret(gateway)}},
	}
	c, err := Open(&conn, settingsFor(
		forgeapi.WithCredentialSource(heldCredential(token)),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithRetries(0),
		forgeapi.WithLogger(recordingLogger(&buf)),
	), testOptions())
	if err != nil {
		t.Fatalf("Setup: Open over proxy %q: %v", proxy, err)
	}
	t.Cleanup(c.Close)

	_, err = c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("Do against an answer with a malformed trailer = %v, want a *forgeapi.Error", err)
	}
	assertNoProxyCredential(t, fe, &buf)
	assertAbsent(t, fe, &buf, token, gateway)
}

// TestALostEndpointAnswerCarriesNoValueTheFormHeld holds the failure of an OAuth
// endpoint answer whose body could not be read free of the refresh token the form
// carried, as sent and as form-encoded on the wire, and keeps the form's other
// fields, which hold no secret.
func TestALostEndpointAnswerCarriesNoValueTheFormHeld(t *testing.T) {
	const refresh, clientID = "refresh+secret/0123=", "Iv1.client0123"
	web := rawListener(t, func(req *http.Request) string {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n"
		}
		sent, err := url.ParseQuery(string(raw))
		if err != nil {
			return "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n"
		}
		return "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n" +
			"2\r\n{}\r\n0\r\nrefused " + string(raw) + " " + sent.Get("refresh_token") + "\r\n\r\n"
	})
	var buf bytes.Buffer
	c, err := OpenEndpoint(&forgeapi.Connection{WebBaseURL: "http://" + web}, settingsFor(
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(recordingLogger(&buf)),
	), forgeapi.FamilyGitHub)
	if err != nil {
		t.Fatalf("Setup: OpenEndpoint(%q): %v", web, err)
	}
	t.Cleanup(c.Close)

	form := url.Values{"client_id": {clientID}, "grant_type": {"refresh_token"}, "refresh_token": {refresh}}
	_, err = c.PostForm(t.Context(), "RefreshToken", "/login/oauth/access_token", form, masked.NewSecret(refresh))
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("PostForm against an answer with a malformed trailer = %v, want a *forgeapi.Error", err)
	}
	for _, kept := range []string{"refused", "grant_type=refresh_token", "client_id=" + clientID} {
		if !strings.Contains(fe.Message, kept) {
			t.Errorf("PostForm failed with message %q, want it to keep %q: words that hold no secret reach the caller", fe.Message, kept)
		}
	}
	assertAbsent(t, fe, &buf, refresh, url.QueryEscape(refresh))
}

// TestAMalformedHeadCarriesNoSecretTheRequestHeld holds the transport error of an
// answer whose head net/http cannot parse free of the credential and the
// connection's header values, on the error and on every line logged for the
// request, its retry included: net/http quotes the offending header line or
// status code in its error.
func TestAMalformedHeadCarriesNoSecretTheRequestHeld(t *testing.T) {
	const token, gateway = "token-secret-0123", "gateway-secret-0123"
	for _, test := range []struct {
		head   func(*http.Request) string
		name   string
		kept   string
		absent []string
	}{
		{
			name: "header_line_without_a_colon",
			head: func(req *http.Request) string {
				return "HTTP/1.1 200 OK\r\nrefused " + req.Header.Get("Authorization") + " " + req.Header.Get("X-Gateway-Key") + "\r\n\r\n"
			},
			kept: "refused", absent: []string{token, gateway},
		},
		{
			name: "status_code_not_three_digits",
			head: func(*http.Request) string { return "HTTP/1.1 " + token + "\r\n\r\n" },
			kept: "malformed HTTP status code", absent: []string{token},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			web := rawListener(t, test.head)
			var buf bytes.Buffer
			c, err := Open(&forgeapi.Connection{
				WebBaseURL: "http://" + web,
				Headers:    []forgeapi.Header{{Name: "X-Gateway-Key", Value: masked.NewSecret(gateway)}},
			}, settingsFor(
				forgeapi.WithCredentialSource(heldCredential(token)),
				forgeapi.WithPlaintextHTTP(true),
				forgeapi.WithPrivateAddresses(true),
				forgeapi.WithRetries(1),
				forgeapi.WithLogger(recordingLogger(&buf)),
			), testOptions())
			if err != nil {
				t.Fatalf("Setup: Open(%q): %v", web, err)
			}
			t.Cleanup(c.Close)

			_, err = c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
			var fe *forgeapi.Error
			if !asError(err, &fe) {
				t.Fatalf("Do against a malformed head = %v, want a *forgeapi.Error", err)
			}
			if !strings.Contains(buf.String(), `"retried_on"`) {
				t.Fatalf("the log = %s, want a line naming what the retry was on", buf.String())
			}
			if !strings.Contains(fe.Message, test.kept) {
				t.Errorf("Do failed with message %q, want it to keep %q", fe.Message, test.kept)
			}
			assertAbsent(t, fe, &buf, test.absent...)
		})
	}
}

// TestAMalformedEndpointHeadCarriesNoValueTheFormHeld holds the transport error of
// an OAuth endpoint answer whose head net/http cannot parse free of the values the
// form carried, and its one attempt, which a retry budget does not extend, since
// the lost answer may have spent the refresh token.
func TestAMalformedEndpointHeadCarriesNoValueTheFormHeld(t *testing.T) {
	const refresh = "refresh-secret-0123"
	var attempts atomic.Int32
	web := rawListener(t, func(req *http.Request) string {
		attempts.Add(1)
		if err := req.ParseForm(); err != nil {
			return "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n"
		}
		return "HTTP/1.1 200 OK\r\nrefused " + req.PostForm.Get("refresh_token") + "\r\n\r\n"
	})
	var buf bytes.Buffer
	c, err := OpenEndpoint(&forgeapi.Connection{WebBaseURL: "http://" + web}, settingsFor(
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithRetries(1),
		forgeapi.WithLogger(recordingLogger(&buf)),
	), forgeapi.FamilyGitHub)
	if err != nil {
		t.Fatalf("Setup: OpenEndpoint(%q): %v", web, err)
	}
	t.Cleanup(c.Close)

	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}
	_, err = c.PostForm(t.Context(), "RefreshToken", "/login/oauth/access_token", form, masked.NewSecret(refresh))
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("PostForm against a malformed head = %v, want a *forgeapi.Error", err)
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("PostForm sent %d attempts, want 1", n)
	}
	if !strings.Contains(fe.Message, "refused") {
		t.Errorf("PostForm failed with message %q, want it to keep %q: the instance's own words reach the caller", fe.Message, "refused")
	}
	assertAbsent(t, fe, &buf, refresh)
}

// rawListener answers every request on a fresh loopback address with the raw
// HTTP/1.1 response answer builds from it, then closes the connection, and
// returns the address.
func rawListener(t *testing.T, answer func(*http.Request) string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Setup: listening: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			if req, err := http.ReadRequest(bufio.NewReader(conn)); err == nil {
				_, _ = io.WriteString(conn, answer(req))
			}
			_ = conn.Close()
		}
	}()
	return ln.Addr().String()
}

// assertAbsent fails when fe or the log in buf carries any of secrets.
func assertAbsent(t *testing.T, fe *forgeapi.Error, buf *bytes.Buffer, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(fe.Error(), secret) {
			t.Errorf("the failure = %q, want no %q: the answer echoed a secret the request carried", fe.Error(), secret)
		}
		if strings.Contains(buf.String(), secret) {
			t.Errorf("the log = %s, want no %q", buf.String(), secret)
		}
	}
}

// openProxiedConn opens a connection to web over proxy with the user-id
// "forge-client" and proxyPassword, logging to buf.
func openProxiedConn(t *testing.T, web, proxy string, buf *bytes.Buffer, extra ...forgeapi.Option) *Conn {
	t.Helper()
	conn := forgeapi.Connection{
		WebBaseURL: web, Proxy: proxy,
		ProxyUsername: "forge-client", ProxyPassword: masked.NewSecret(proxyPassword),
	}
	c, err := Open(&conn, settingsFor(append([]forgeapi.Option{
		forgeapi.WithCredentialSource(stubCredential{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(recordingLogger(buf)),
	}, extra...)...), testOptions())
	if err != nil {
		t.Fatalf("Setup: Open over proxy %q: %v", proxy, err)
	}
	t.Cleanup(c.Close)
	return c
}

// assertNoProxyCredential fails when fe or the log in buf carries the proxy
// password or its Basic credentials, or when fe lost the proxy's own words.
func assertNoProxyCredential(t *testing.T, fe *forgeapi.Error, buf *bytes.Buffer) {
	t.Helper()
	for _, absent := range []string{proxyPassword, proxyBasic} {
		if strings.Contains(fe.Error(), absent) {
			t.Errorf("Do failed with %q, want no %q: the proxy's text echoed a credential the request carried", fe.Error(), absent)
		}
		if strings.Contains(buf.String(), absent) {
			t.Errorf("the log = %s, want no %q", buf.String(), absent)
		}
	}
	if !strings.Contains(fe.Message, "refused") {
		t.Errorf("Do failed with message %q, want it to keep %q: the proxy's own words reach the caller", fe.Message, "refused")
	}
}

// failingCredential is a credential source whose failure says the text given, as a
// consumer's source may when it quotes what its own store answered.
type failingCredential string

func (c failingCredential) Token(context.Context) (string, error) { return "", errors.New(string(c)) }

func (failingCredential) Kind() forgeapi.CredKind   { return forgeapi.CredKindStaticPAT }
func (failingCredential) State() forgeapi.CredState { return forgeapi.CredValid }

// TestACredentialSourcesFailureCarriesNoConnectionSecret holds the error a failing
// credential source becomes free of a connection header value its text quotes,
// since that text is the consumer's, not this library's.
func TestACredentialSourcesFailureCarriesNoConnectionSecret(t *testing.T) {
	const gateway = "gateway-secret-0123"
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example", Headers: gatewayHeader(gateway)},
		forgeapi.WithCredentialSource(failingCredential("store refused key "+gateway)))
	_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("a read whose credential source failed = %v, want a *forgeapi.Error", err)
	}
	if strings.Contains(fe.Message, gateway) || strings.Contains(fe.Error(), gateway) {
		t.Errorf("the failure = %q, want the connection header value redacted", fe.Message)
	}
	if !strings.Contains(fe.Message, "store refused key") {
		t.Errorf("the failure = %q, want the source's own words kept", fe.Message)
	}
}

// spentCredential is a credential source that answers token once and fails naming
// it after that, as a consumer's source may when its store revoked a token it
// issued. It asks for a resend after every 401.
type spentCredential struct {
	token string
	asked atomic.Int32
}

func (s *spentCredential) Token(context.Context) (string, error) {
	if s.asked.Add(1) == 1 {
		return s.token, nil
	}
	return "", errors.New("the store revoked " + s.token)
}

func (*spentCredential) Kind() forgeapi.CredKind   { return forgeapi.CredKindStaticPAT }
func (*spentCredential) State() forgeapi.CredState { return forgeapi.CredValid }
func (*spentCredential) Discard(string) bool       { return true }

// TestACredentialSourcesFailureCarriesNoTokenItAnsweredBefore holds the error a
// credential source becomes free of a token the source answered earlier, on an
// earlier read or on the attempt a 401 sent back, where its failure quotes it.
func TestACredentialSourcesFailureCarriesNoTokenItAnsweredBefore(t *testing.T) {
	const token = "spent-token-0123"
	for _, test := range []struct {
		name     string
		statuses []int
		reads    int
	}{
		{name: "on_a_later_read", statuses: []int{http.StatusOK}, reads: 2},
		{name: "on_the_resend_a_401_asked_for", statuses: []int{http.StatusUnauthorized, http.StatusOK}, reads: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dc := openDiscardCase(t, &spentCredential{token: token}, newScriptedWire(test.statuses...))
				var err error
				for range test.reads {
					_, err = dc.conn.Do(t.Context(), whoami())
				}
				assertSourceFailureOmits(t, err, dc.buf, token, "the store revoked")
			})
		})
	}
}

// pairedCredential is a credential source that answers its token beside an error
// naming it, a pair Go's convention makes no answer.
type pairedCredential string

func (c pairedCredential) Token(context.Context) (string, error) {
	return string(c), errors.New("the store revoked " + string(c))
}

func (pairedCredential) Kind() forgeapi.CredKind   { return forgeapi.CredKindStaticPAT }
func (pairedCredential) State() forgeapi.CredState { return forgeapi.CredValid }

// TestACredentialSourcesFailureCarriesNoTokenItAnsweredBesideIt holds the error a
// credential source becomes free of the token it answered beside that error,
// though no request carried it.
func TestACredentialSourcesFailureCarriesNoTokenItAnsweredBesideIt(t *testing.T) {
	const token = "paired-token-0123"
	synctest.Test(t, func(t *testing.T) {
		dc := openDiscardCase(t, pairedCredential(token), newScriptedWire(http.StatusOK))
		_, err := dc.conn.Do(t.Context(), whoami())
		assertSourceFailureOmits(t, err, dc.buf, token, "the store revoked")
	})
}

// rotatingCredential is a credential source that answers each of tokens in turn
// and, after the last, fails naming revoked.
type rotatingCredential struct {
	revoked string
	tokens  []string
	asked   atomic.Int32
}

func (s *rotatingCredential) Token(context.Context) (string, error) {
	if i := int(s.asked.Add(1)) - 1; i < len(s.tokens) {
		return s.tokens[i], nil
	}
	return "", errors.New("the store revoked " + s.revoked)
}

func (*rotatingCredential) Kind() forgeapi.CredKind   { return forgeapi.CredKindStaticPAT }
func (*rotatingCredential) State() forgeapi.CredState { return forgeapi.CredValid }

// TestATokenCarriedAgainOutlastsTheTokensCarriedBeforeIt holds a token the
// connection's requests carried again as recently carried: after eight distinct
// tokens, the first again and a ninth, a source's failure naming the first still
// has it redacted.
func TestATokenCarriedAgainOutlastsTheTokensCarriedBeforeIt(t *testing.T) {
	const reused = "reused-token-0123"
	tokens := []string{reused}
	for i := 1; i < 8; i++ {
		tokens = append(tokens, fmt.Sprintf("other-token-%d", i))
	}
	tokens = append(tokens, reused, "ninth-token")
	synctest.Test(t, func(t *testing.T) {
		dc := openDiscardCase(t, &rotatingCredential{tokens: tokens, revoked: reused}, newScriptedWire(http.StatusOK))
		var err error
		for range len(tokens) + 1 {
			_, err = dc.conn.Do(t.Context(), whoami())
		}
		assertSourceFailureOmits(t, err, dc.buf, reused, "the store revoked")
	})
}

// assertSourceFailureOmits holds err, a read's credential source failure, to a
// *forgeapi.Error whose Message, Error() and every logged error omit token, and
// whose Message keeps words.
func assertSourceFailureOmits(t *testing.T, err error, buf *bytes.Buffer, token, words string) *forgeapi.Error {
	t.Helper()
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("a read whose credential source failed = %v, want a *forgeapi.Error", err)
	}
	for _, said := range []string{fe.Message, fe.Error()} {
		if strings.Contains(said, token) {
			t.Errorf("the failure = %q, want the token the source answered redacted", said)
		}
	}
	if !strings.Contains(fe.Message, words) {
		t.Errorf("the failure = %q, want the source's own words %q kept", fe.Message, words)
	}
	carried := 0
	for _, line := range records(t, buf) {
		said, ok := line["error"].(string)
		if !ok {
			continue
		}
		carried++
		if strings.Contains(said, token) {
			t.Errorf("the line's error = %q, want the token redacted", said)
		}
	}
	if carried == 0 {
		t.Error("no line carries the failure, want the read's line to")
	}
	return fe
}

// lapsedCredential is a credential source that answers token once and, after
// that, fails with its own deadline's error while the caller's context is live,
// wrapped in words naming the token where named is set.
type lapsedCredential struct {
	token string
	named bool
	asked atomic.Int32
}

func (s *lapsedCredential) Token(context.Context) (string, error) {
	if s.asked.Add(1) == 1 {
		return s.token, nil
	}
	if s.named {
		return "", fmt.Errorf("refreshing %s: %w", s.token, context.DeadlineExceeded)
	}
	return "", context.DeadlineExceeded
}

func (*lapsedCredential) Kind() forgeapi.CredKind   { return forgeapi.CredKindStaticPAT }
func (*lapsedCredential) State() forgeapi.CredState { return forgeapi.CredValid }

// TestACredentialSourcesOwnTimeoutIsTheSourcesFailure holds a source's deadline
// error on a live call as the source's failure: nothing was sent, so it is not an
// unreachable instance, and a token the source answered before is redacted from
// its words.
func TestACredentialSourcesOwnTimeoutIsTheSourcesFailure(t *testing.T) {
	const token = "lapsed-token-0123"
	for _, test := range []struct {
		name  string
		named bool
		words string
	}{
		{name: "wrapped_in_words_naming_a_token", named: true, words: "refreshing"},
		{name: "as_the_bare_sentinel", words: context.DeadlineExceeded.Error()},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dc := openDiscardCase(t, &lapsedCredential{token: token, named: test.named}, newScriptedWire(http.StatusOK))
				var err error
				for range 2 {
					_, err = dc.conn.Do(t.Context(), whoami())
				}
				fe := assertSourceFailureOmits(t, err, dc.buf, token, "the credential source failed: "+test.words)
				if fe.Code == forgeapi.CodeUnreachable || fe.Kind != forgeapi.KindTransient || !fe.Retryable {
					t.Errorf("the failure = code %q, kind %q, retryable %t, want the source's transient failure, not an unreachable instance",
						fe.Code, fe.Kind, fe.Retryable)
				}
			})
		})
	}
}

// sharingCredential is a credential source whose every failure is the one
// refusal it holds, a sentinel its consumer shares.
type sharingCredential struct{ refusal *forgeapi.Error }

func (c sharingCredential) Token(context.Context) (string, error) { return "", c.refusal }
func (sharingCredential) Kind() forgeapi.CredKind                 { return forgeapi.CredKindStaticPAT }
func (sharingCredential) State() forgeapi.CredState               { return forgeapi.CredValid }

// TestACredentialSourcesRefusalIsAnsweredAsACopy holds a source's own
// *forgeapi.Error unwritten across the reads it fails: each read answers a copy
// carrying its own operation and diagnostic id, so a sentinel the consumer shares
// keeps what the consumer put in it.
func TestACredentialSourcesRefusalIsAnsweredAsACopy(t *testing.T) {
	sentinel := &forgeapi.Error{Kind: forgeapi.KindUnauthorized, Message: "the store holds no token"}
	want := *sentinel
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"},
		forgeapi.WithCredentialSource(sharingCredential{refusal: sentinel}))
	var ids []string
	for range 2 {
		_, err := c.Do(t.Context(), whoami())
		var fe *forgeapi.Error
		if !asError(err, &fe) || fe == sentinel {
			t.Fatalf("a read the source refused = %v, want a copy of the source's refusal", err)
		}
		if fe.Op != "Whoami" || fe.Kind != want.Kind || fe.Message != want.Message || fe.DiagID == "" {
			t.Errorf("the answered refusal = %+v, want the source's kind and message with the read's operation and an id", fe)
		}
		ids = append(ids, fe.DiagID)
	}
	if !reflect.DeepEqual(*sentinel, want) {
		t.Errorf("the source's refusal = %+v after two reads, want it as the consumer built it, %+v", *sentinel, want)
	}
	if ids[0] == ids[1] {
		t.Errorf("both reads answered diagnostic id %q, want one each", ids[0])
	}
}
