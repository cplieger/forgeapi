package transport

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/go/masked"
)

// The secrets every exchange below can carry: its credential, the connection's
// own header value, and the proxy password with its Basic credentials.
const (
	sinkToken   = "sink-token-0123"
	sinkGateway = "sink-gateway-0123"
)

// sinkSecrets is every secret an exchange of [openSinkConn]'s connection carries.
var sinkSecrets = []string{sinkToken, sinkGateway, proxyPassword, proxyBasic}

// planted is text an instance or a proxy answers with every secret the exchange
// carried in it, behind a word that shows the text reached its sink.
func planted(word string) string {
	return word + " " + strings.Join(sinkSecrets, " ")
}

// rawAnswer is an HTTP/1.1 response with status line status, the header lines
// header and body, its length stated.
func rawAnswer(status string, header []string, body string) string {
	return "HTTP/1.1 " + status + "\r\n" + strings.Join(header, "\r\n") + "\r\nContent-Length: " +
		strconv.Itoa(len(body)) + "\r\n\r\n" + body
}

// plantedRefusal is a JSON refusal body every member of which carries the secrets.
var plantedRefusal = `{"message":"` + planted("said") + `","error":"insufficient_scope","error_description":"` +
	planted("described") + `","scope":"` + planted("scoped") + `"}`

// plantedFigures are the budget headers, the pool's name carrying the secrets.
var plantedFigures = []string{"X-Remaining: 3", "X-Pool: " + planted("pool"), "Retry-After: 1"}

// TestNoSinkCarriesASecretAnExchangeHeld plants every secret an exchange carries in
// every input its instance or proxy controls (the status line, each header the
// transport reads, the body, a head or a body net/http cannot parse) and holds every
// sink the transport writes free of them: each field of the failure, its text, every
// attribute of every line logged for the request, and the budget state. Each row's
// kept words prove the planted text reached the sink it names.
func TestNoSinkCarriesASecretAnExchangeHeld(t *testing.T) {
	for _, test := range []struct {
		answer  func(*http.Request) string
		name    string
		web     string
		kept    []string
		into    bool
		failure bool
	}{
		{
			name: "refusal_naming_its_request_id_as_github_does",
			answer: func(*http.Request) string {
				return rawAnswer("401 "+planted("reason"), append([]string{
					"Content-Type: application/json",
					"X-GitHub-Request-Id: " + planted("id"),
				}, plantedFigures...), plantedRefusal)
			},
			kept: []string{"said", "id", "pool"}, failure: true,
		},
		{
			name: "throttle_naming_its_request_id_as_gitlab_does",
			answer: func(*http.Request) string {
				return rawAnswer("429 Too Many Requests", append([]string{
					"Content-Type: application/json",
					"X-Gitlab-Meta: " + planted("id"),
				}, plantedFigures...), plantedRefusal)
			},
			kept: []string{"said", "id", "pool"}, failure: true,
		},
		{
			name: "success_naming_its_request_id",
			answer: func(*http.Request) string {
				return rawAnswer("200 OK", append([]string{
					"Content-Type: application/json",
					"X-Request-Id: " + planted("id"),
				}, plantedFigures...), `{}`)
			},
			kept: []string{"id", "pool"}, into: true,
		},
		{
			name: "body_that_does_not_decode",
			answer: func(*http.Request) string {
				return rawAnswer("200 OK", []string{
					"Content-Type: application/json; " + planted("typed"),
					"X-Request-Id: " + planted("id"),
				}, `not json`)
			},
			kept: []string{"typed", "id"}, into: true, failure: true,
		},
		{
			name: "head_line_net_http_cannot_parse",
			answer: func(*http.Request) string {
				return "HTTP/1.1 200 OK\r\n" + planted("line") + "\r\n\r\n"
			},
			kept: []string{"line", "retried_on"}, failure: true,
		},
		{
			name: "status_code_net_http_cannot_parse",
			answer: func(*http.Request) string {
				return "HTTP/1.1 " + sinkToken + "\r\n\r\n"
			},
			kept: []string{"malformed HTTP status code"}, failure: true,
		},
		{
			name: "trailer_net_http_cannot_parse",
			answer: func(*http.Request) string {
				return "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n" +
					"2\r\n{}\r\n0\r\n" + planted("trailer") + "\r\n\r\n"
			},
			kept: []string{"trailer"}, failure: true,
		},
		{
			name: "proxy_refusing_connect", web: "https://forge.example.com",
			answer: func(*http.Request) string {
				return "HTTP/1.1 403 " + planted("reason") + "\r\nContent-Length: 0\r\n\r\n"
			},
			kept: []string{"reason", "retried_on"}, failure: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var buf bytes.Buffer
			c := openSinkConn(t, cmpOr(test.web, "http://forge.example.com"), rawListener(t, test.answer), &buf)
			req := &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}
			if test.into {
				req.Into = &struct{}{}
			}

			_, err := c.Do(t.Context(), req)
			var fe *forgeapi.Error
			if test.failure && !asError(err, &fe) {
				t.Fatalf("Do = %v, want a *forgeapi.Error", err)
			}
			if !test.failure && err != nil {
				t.Fatalf("Do = %v, want nil", err)
			}
			sinks := sinksOf(t, &buf, fe)
			sinks["budget state"] = fmt.Sprintf("%+v", c.BudgetState())
			assertNoSinkCarries(t, sinks, test.kept...)
		})
	}
}

// TestAFamilysFailureCarriesNoSecretTheExchangeHeld holds each failure a family
// builds from an answer free of the secrets the exchange carried, on the failure and
// on its line, whatever text the family hands it: a family quotes the instance's
// words in its message and its cause.
func TestAFamilysFailureCarriesNoSecretTheExchangeHeld(t *testing.T) {
	ex := REST(http.MethodGet)
	for _, test := range []struct {
		fail func(c *Conn, answer *Response, said string) *forgeapi.Error
		name string
	}{
		{
			name: "fail_on_an_answer",
			fail: func(c *Conn, answer *Response, said string) *forgeapi.Error {
				return c.Fail(t.Context(), "Whoami", ex, answer, forgeapi.CodeValidation, forgeapi.KindUpstream, said)
			},
		},
		{
			name: "fail_on_an_earlier_failure",
			fail: func(c *Conn, answer *Response, said string) *forgeapi.Error {
				cause := c.Fail(t.Context(), "Whoami", ex, answer, forgeapi.CodeValidation, forgeapi.KindUpstream, "no answer")
				return c.FailOn(t.Context(), "Whoami", ex, cause, forgeapi.CodeFamilyUndetected, forgeapi.KindUnknown, said)
			},
		},
		{
			name: "body_that_is_no_answer",
			fail: func(c *Conn, answer *Response, said string) *forgeapi.Error {
				return c.FailBody(t.Context(), "Whoami", ex, answer, errors.New(said))
			},
		},
		{
			name: "body_that_does_not_decode",
			fail: func(c *Conn, answer *Response, said string) *forgeapi.Error {
				return c.FailDecode(t.Context(), "Whoami", ex, answer, errors.New(said))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var buf bytes.Buffer
			web := rawListener(t, func(*http.Request) string {
				return rawAnswer("200 OK", []string{"Content-Type: application/json"}, `"`+planted("said")+`"`)
			})
			c := openSinkConn(t, "http://forge.example.com", web, &buf)
			answer, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
			if err != nil {
				t.Fatalf("Setup: Do = %v, want nil", err)
			}

			fe := test.fail(c, answer, string(answer.Body))
			assertNoSinkCarries(t, sinksOf(t, &buf, fe), "said")
		})
	}
}

// sinksOf is every sink a request wrote, by name: each attribute of each line in
// buf, and each field of fe and its text where fe is not nil.
func sinksOf(t *testing.T, buf *bytes.Buffer, fe *forgeapi.Error) map[string]string {
	t.Helper()
	sinks := map[string]string{}
	for i, line := range records(t, buf) {
		for key, value := range line {
			sinks["line "+strconv.Itoa(i)+" "+key] = fmt.Sprint(value)
		}
	}
	if fe == nil {
		return sinks
	}
	v := reflect.ValueOf(*fe)
	for i := range v.NumField() {
		sinks["Error."+v.Type().Field(i).Name] = fmt.Sprintf("%+v", v.Field(i).Interface())
	}
	sinks["Error()"] = fe.Error()
	return sinks
}

// assertNoSinkCarries fails for each sink carrying a secret of [sinkSecrets], and
// for each of kept no sink names or carries, since a kept word proves the planted
// text reached the sinks.
func assertNoSinkCarries(t *testing.T, sinks map[string]string, kept ...string) {
	t.Helper()
	for name, text := range sinks {
		for _, secret := range sinkSecrets {
			if strings.Contains(text, secret) {
				t.Errorf("%s = %q, want no %q", name, text, secret)
			}
		}
	}
	for _, word := range kept {
		if !slices.ContainsFunc(slices.Collect(maps.Keys(sinks)), func(name string) bool { return strings.Contains(name+" "+sinks[name], word) }) {
			t.Errorf("no sink carries %q, want the planted text to reach one: %v", word, sinks)
		}
	}
}

// openSinkConn opens a connection to web over proxy, holding sinkToken, sending
// sinkGateway as its own header and the proxy credentials, with one retry, its
// pool read from X-Pool, logging every level to buf.
func openSinkConn(t *testing.T, web, proxy string, buf *bytes.Buffer) *Conn {
	t.Helper()
	conn := forgeapi.Connection{
		WebBaseURL: web, Proxy: "http://" + proxy,
		ProxyUsername: "forge-client", ProxyPassword: masked.NewSecret(proxyPassword),
		Headers: []forgeapi.Header{{Name: "X-Gateway-Key", Value: masked.NewSecret(sinkGateway)}},
	}
	opts := testOptions()
	opts.Pool = func(header http.Header) (string, int) { return header.Get("X-Pool"), 0 }
	c, err := Open(&conn, settingsFor(
		forgeapi.WithCredentialSource(heldCredential(sinkToken)),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithRetries(1),
		forgeapi.WithLogger(recordingLogger(buf)),
	), opts)
	if err != nil {
		t.Fatalf("Setup: Open over proxy %q: %v", proxy, err)
	}
	t.Cleanup(c.Close)
	return c
}

// cmpOr is s, or fallback where s is empty.
func cmpOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
