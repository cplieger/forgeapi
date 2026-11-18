package creds_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/creds"
	"github.com/cplieger/go/masked"
	"github.com/cplieger/go/masked/maskedtest"
)

// mintedToken is one InstallationToken a source minted, holding ghs_token1.
func mintedToken(t *testing.T) creds.InstallationToken {
	t.Helper()
	var tok creds.InstallationToken
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, mintsAt(t, 42, time.Hour))
		var err error
		if tok, err = appSource(t, g, creds.InstallationByID(42)).Mint(t.Context()); err != nil {
			t.Fatalf("Setup: Mint() = %v", err)
		}
	})
	return tok
}

// A source holds its cached token as a masked.Secret, so printing or logging the
// source carries none of it.
func TestAppSource_prints_no_part_of_its_cached_token(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, mintsAt(t, 42, time.Hour))
		s := appSource(t, g, creds.InstallationByID(42))
		const secret = "ghs_token1"
		if got, err := s.Token(t.Context()); err != nil || got != secret {
			t.Fatalf("Setup: Token() = %q, %v, want %s", got, err, secret)
		}
		for printer, out := range rendered(s) {
			if maskedtest.Leak(out, secret) {
				t.Errorf("%s of the source carries the token: %q", printer, out)
			}
		}
	})
}

// printed is every rendering of v a log line, an error report or a dump can
// carry: the [rendered] ones and what a printer walking v's fields by
// reflection reaches, as go-spew or go-cmp does.
func printed(t *testing.T, v any) map[string]string {
	t.Helper()
	out := rendered(v)
	out["reflection"] = reflected(v)
	return out
}

// rendered is v under the fmt verbs, slog's two handlers and encoding/json.
func rendered(v any) map[string]string {
	out := map[string]string{}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		out[verb] = fmt.Sprintf(verb, v)
	}
	var text, structured bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("printed", "value", v)
	slog.New(slog.NewJSONHandler(&structured, nil)).Info("printed", "value", v)
	out["slog text"], out["slog JSON"] = text.String(), structured.String()
	if b, err := json.Marshal(v); err == nil {
		out["json.Marshal"] = string(b)
	}
	return out
}

// reflected is every string v reaches through its fields, unexported ones
// included, with each byte sequence as its bytes and each word sequence as
// the number its words spell, so a parsed private key's D shows in decimal.
func reflected(v any) string {
	var b strings.Builder
	seen := map[uintptr]bool{}
	var walk func(reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() && !seen[v.Pointer()] {
				seen[v.Pointer()] = true
				walk(v.Elem())
			}
		case reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Struct:
			for _, f := range v.Fields() {
				walk(f)
			}
		case reflect.Map:
			for it := v.MapRange(); it.Next(); {
				walk(it.Key())
				walk(it.Value())
			}
		case reflect.Slice, reflect.Array:
			switch v.Type().Elem().Kind() {
			case reflect.Uint8:
				for i := range v.Len() {
					b.WriteByte(byte(v.Index(i).Uint()))
				}
			case reflect.Uint, reflect.Uintptr:
				words := make([]big.Word, v.Len())
				for i := range words {
					words[i] = big.Word(v.Index(i).Uint())
				}
				b.WriteString(new(big.Int).SetBits(words).String())
			default:
				for i := range v.Len() {
					walk(v.Index(i))
				}
			}
		case reflect.String:
			b.WriteString(v.String())
		}
		b.WriteByte('\n')
	}
	walk(reflect.ValueOf(v))
	return b.String()
}

// noLeak fails the case for every rendering of v that carries one of texts.
func noLeak(t *testing.T, name string, v any, texts ...string) {
	t.Helper()
	for printer, out := range printed(t, v) {
		for _, text := range texts {
			if maskedtest.Leak(out, text) {
				t.Errorf("%s of %s carries %q: %q", printer, name, text, out)
			}
		}
	}
}

// grantedAnswer is a token answer holding grantedAccess and grantedRefresh.
const (
	grantedAccess  = "gho_printer_check_access"
	grantedRefresh = "ghr_printer_check_refresh"
	grantedAnswer  = `{"access_token":"` + grantedAccess + `","token_type":"bearer","expires_in":28800,"refresh_token":"` +
		grantedRefresh + `","refresh_token_expires_in":15897600,"scope":"repo"}`
)

func TestRecord_printers_show_none_of_its_tokens(t *testing.T) {
	ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: http.StatusOK, body: grantedAnswer}))
	rec, ok, err := startGrant(t, ep, forgeapi.FamilyGitHub).Poll(t.Context())
	if err != nil || !ok {
		t.Fatalf("Setup: Poll() = (_, %v, %v), want the record", ok, err)
	}
	store, _ := openStore(t)
	save(t, store, "conn", rec)
	loaded := load(t, store, "conn")
	noLeak(t, "the granted record", rec, grantedAccess, grantedRefresh)
	noLeak(t, "a pointer to the granted record", &rec, grantedAccess, grantedRefresh)
	noLeak(t, "the stored record", loaded, grantedAccess, grantedRefresh)
}

func TestDeviceGrant_printers_show_no_device_code(t *testing.T) {
	ep := newEndpoint(t, deviceFlow(deviceCode("900"), answerBody{status: http.StatusOK, body: `{"error":"authorization_pending"}`}))
	g := startGrant(t, ep, forgeapi.FamilyGitHub)
	defer g.Close()
	// Not printed: walking the grant's live HTTP client races its transport.
	for printer, out := range rendered(g) {
		if maskedtest.Leak(out, "example-device-code") {
			t.Errorf("%s of the grant carries the device code: %q", printer, out)
		}
	}
}

func TestAppKey_printers_show_no_part_of_a_parsed_private_key(t *testing.T) {
	private := appPrivateKey(t)
	key, err := creds.ParseAppKeyPEM("Iv23liAbCdEf012345", pkcs1PEM(private))
	if err != nil {
		t.Fatalf("Setup: ParseAppKeyPEM: %v", err)
	}
	noLeak(t, "the App key", key, private.D.String(), private.Primes[0].String(), private.Primes[1].String(),
		string(x509.MarshalPKCS1PrivateKey(private)))
}

func TestInstallationToken_printers_show_none_of_the_token(t *testing.T) {
	noLeak(t, "the minted token", mintedToken(t), "ghs_token1")
}

// An instance can echo the refresh token it was sent in any member of its
// refusal, so no error a failed refresh answers may carry either token.
func TestStoredSource_errors_show_none_of_the_tokens(t *testing.T) {
	for _, body := range []string{
		`{"error":"refresh-old","error_description":"refresh-old token-old"}`,
		`{"error":"invalid_grant","error_description":"refresh-old token-old"}`,
	} {
		t.Run(body, func(t *testing.T) {
			store, _ := openStore(t)
			ep := newEndpoint(t, func(int, exchange) (int, string) { return http.StatusBadRequest, body })
			save(t, store, "conn", rotating(forgeapi.FamilyGitLab, ep.srv.URL, 2*time.Hour, -time.Minute))
			_, err := source(t, store, "conn", ep).Token(t.Context())
			if err == nil {
				t.Fatal("Token() after a refused refresh of an expired token = no error, want the refusal")
			}
			noLeak(t, "the refusal", err, "refresh-old", "token-old")
			noLeak(t, "the refusal's text", err.Error(), "refresh-old", "token-old")
		})
	}
}

// An instance can echo the device code it was sent in its answer to a poll.
func TestDeviceGrant_errors_show_no_device_code(t *testing.T) {
	ep := newEndpoint(t, deviceFlow(deviceCode("900"),
		answerBody{status: http.StatusBadRequest, body: `{"error":"example-device-code","error_description":"example-device-code"}`}))
	g := startGrant(t, ep, forgeapi.FamilyGitLab)
	defer g.Close()
	_, _, err := g.Poll(t.Context())
	if err == nil {
		t.Fatal("Poll() on an answer naming no OAuth outcome = no error, want the refusal")
	}
	noLeak(t, "the refusal", err, "example-device-code")
}

// GitHub names a limit's pool in a response header, which an instance can fill
// with the refresh token it was sent, so neither the pool nor the text of the
// limit a refresh meets may carry it.
func TestStoredSource_limit_names_no_refresh_token_in_its_pool(t *testing.T) {
	ep := newEndpointWith(t, http.Header{"X-Ratelimit-Resource": {"refresh-old"}},
		func(int, exchange) (int, string) { return http.StatusTooManyRequests, `{"message":"rate limited"}` })
	store, _ := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, ep.srv.URL, 8*time.Hour, -time.Minute))
	_, err := source(t, store, "conn", ep).Token(t.Context())
	assertPoolRedacted(t, err, "refresh-old")
}

// The device-grant twin: a poll's limit may not carry the device code in its pool.
func TestDeviceGrant_limit_names_no_device_code_in_its_pool(t *testing.T) {
	ep := newEndpointWith(t, http.Header{"X-Ratelimit-Resource": {"example-device-code"}},
		deviceFlow(deviceCode("900"), answerBody{status: http.StatusTooManyRequests, body: `{"message":"rate limited"}`}))
	g := startGrant(t, ep, forgeapi.FamilyGitHub)
	defer g.Close()
	_, _, err := g.Poll(t.Context())
	assertPoolRedacted(t, err, "example-device-code")
}

// assertPoolRedacted fails unless err is a refusal naming a pool, and neither
// that pool nor err's text carries secret.
func assertPoolRedacted(t *testing.T, err error, secret string) {
	t.Helper()
	fe, ok := errors.AsType[*forgeapi.Error](err)
	if !ok {
		t.Fatalf("error = %v, want the limit as a *forgeapi.Error", err)
	}
	if fe.Pool == "" {
		t.Errorf("Pool = empty, want the pool the header named, redacted")
	}
	if strings.Contains(fe.Pool, secret) {
		t.Errorf("Pool = %q, want it free of the secret the instance echoed", fe.Pool)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("Error() = %q, want it free of the secret the instance echoed", err)
	}
}

// clientPairPEM is a self-signed client certificate and its private key.
func clientPairPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("Setup: generating a client key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "forgeapi test client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("Setup: signing a client certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("Setup: encoding a client key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// A source holds its connection by value, so printing the source prints the
// connection's client key, gateway header and proxy password unless each is
// masked.
func TestSources_print_none_of_their_connections_secrets(t *testing.T) {
	const (
		gatewaySecret = "test-gateway-secret-value"
		proxyPassword = "test-proxy-password-value"
	)
	certPEM, keyPEM := clientPairPEM(t)
	conn := forgeapi.Connection{
		WebBaseURL:    appWeb,
		ClientCert:    certPEM,
		ClientKey:     masked.NewSecret(string(keyPEM)),
		Proxy:         "http://proxy.example.com:3128",
		ProxyUsername: "forge-client",
		ProxyPassword: masked.NewSecret(proxyPassword),
		Headers:       []forgeapi.Header{{Name: "X-Gateway-Secret", Value: masked.NewSecret(gatewaySecret)}},
	}
	secrets := []string{string(keyPEM), gatewaySecret, proxyPassword}
	store, _ := openStore(t)
	stored, err := creds.NewStoredSource(store, "conn", conn)
	if err != nil {
		t.Fatalf("Setup: NewStoredSource: %v", err)
	}
	g := newFakeGitHub(t, mintsAt(t, 42, time.Hour))
	app, err := creds.NewAppSource(conn, appKey(t),
		creds.AppSourceConfig{Installation: creds.InstallationByID(42), Grant: readGrant()}, appOptions(g)...)
	if err != nil {
		t.Fatalf("Setup: NewAppSource: %v", err)
	}
	for name, source := range map[string]any{"the stored source": stored, "the App source": app} {
		for printer, out := range rendered(source) {
			for _, secret := range secrets {
				if maskedtest.Leak(out, secret) {
					t.Errorf("%s of %s carries %q: %q", printer, name, secret, out)
				}
			}
		}
	}
}
