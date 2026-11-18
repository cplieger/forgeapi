package forgeapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/go/masked"
	"github.com/cplieger/go/masked/maskedtest"
)

// connectionReviewed are the text fields of a connection, each with why it
// holds no secret.
var connectionReviewed = map[maskedtest.Field]string{
	{In: reflect.TypeFor[forgeapi.Connection](), Name: "WebBaseURL"}:    "the instance's origin, refused when it carries userinfo",
	{In: reflect.TypeFor[forgeapi.Connection](), Name: "APIBaseURL"}:    "the instance's API origin, refused when it carries userinfo",
	{In: reflect.TypeFor[forgeapi.Connection](), Name: "CABytes"}:       "the trust anchors' public certificates",
	{In: reflect.TypeFor[forgeapi.Connection](), Name: "ClientCert"}:    "the client's public certificate",
	{In: reflect.TypeFor[forgeapi.Connection](), Name: "Proxy"}:         "the proxy's URL, refused when it carries userinfo",
	{In: reflect.TypeFor[forgeapi.Connection](), Name: "ProxyUsername"}: "the account the proxy knows the connection as, the password beside it masked",
	{In: reflect.TypeFor[forgeapi.Header](), Name: "Name"}:              "a header's field name",
}

func TestUnmasked_a_connection_carries_no_secret_in_clear(t *testing.T) {
	for _, reason := range connectionReviewed {
		if strings.TrimSpace(reason) == "" {
			t.Fatal("an allow entry has no reason")
		}
	}
	got := maskedtest.Unmasked(maskedtest.Allow{Fields: slices.Collect(maps.Keys(connectionReviewed))},
		reflect.TypeFor[forgeapi.Connection]())
	if len(got) > 0 {
		t.Errorf("Unmasked reported:\n%s", strings.Join(got, "\n"))
	}
}

func TestSecretNamed_a_connection_names_no_credential_in_clear(t *testing.T) {
	got := maskedtest.SecretNamed(maskedtest.NameAllow{}, reflect.TypeFor[forgeapi.Connection]())
	if len(got) > 0 {
		t.Errorf("SecretNamed reported:\n%s", strings.Join(got, "\n"))
	}
}

func TestConnection_printers_show_none_of_its_secrets(t *testing.T) {
	const (
		clientKey     = "test-client-key-material"
		gatewaySecret = "test-gateway-secret-value"
		proxyPassword = "test-proxy-password-value"
	)
	conn := forgeapi.Connection{
		WebBaseURL:    "https://forge.example.com",
		ClientCert:    []byte("test-client-certificate"),
		ClientKey:     masked.NewSecret(clientKey),
		Proxy:         "http://proxy.example.com:3128",
		ProxyUsername: "forge-client",
		ProxyPassword: masked.NewSecret(proxyPassword),
		Headers:       []forgeapi.Header{{Name: "X-Gateway-Secret", Value: masked.NewSecret(gatewaySecret)}},
	}
	for _, v := range []any{conn, &conn} {
		out := map[string]string{}
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
			out[verb] = fmt.Sprintf(verb, v)
		}
		var text, structured bytes.Buffer
		slog.New(slog.NewTextHandler(&text, nil)).Info("printed", "value", v)
		slog.New(slog.NewJSONHandler(&structured, nil)).Info("printed", "value", v)
		out["slog text"], out["slog JSON"] = text.String(), structured.String()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("json.Marshal(%T) = %v", v, err)
		}
		out["json.Marshal"] = string(b)
		for printer, got := range out {
			for _, secret := range []string{clientKey, gatewaySecret, proxyPassword} {
				if maskedtest.Leak(got, secret) {
					t.Errorf("%s of a %T carries %q: %q", printer, v, secret, got)
				}
			}
		}
	}
}
