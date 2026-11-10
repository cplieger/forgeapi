package creds_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/creds"
)

// jwtParts splits a compact JWT into its decoded header and claims and its raw
// signature, failing the test where it is not three base64url segments.
func jwtParts(t *testing.T, jwt string) (header, claims, sig []byte) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("the JWT has %d segments, want 3", len(parts))
	}
	decoded := make([][]byte, 3)
	for i, part := range parts {
		b, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			t.Fatalf("segment %d is not unpadded base64url: %v", i, err)
		}
		decoded[i] = b
	}
	return decoded[0], decoded[1], decoded[2]
}

func TestAppKey_JWT(t *testing.T) {
	key := appPrivateKey(t)
	// The synctest clock starts at 2000-01-01T00:00:00Z, Unix 946684800, so
	// GitHub's rule reads iat 60 seconds earlier and exp 9 minutes later.
	const iat, exp = 946684740, 946685340
	for _, tc := range []struct {
		name   string
		issuer string
		parse  func() (*creds.AppKey, error)
	}{
		{"client ID from PKCS #1", "Iv23liAbCdEf012345", func() (*creds.AppKey, error) {
			return creds.ParseAppKeyPEM("Iv23liAbCdEf012345", pkcs1PEM(key))
		}},
		{"numeric App ID from PKCS #8", "123456", func() (*creds.AppKey, error) {
			return creds.ParseAppKeyPEM("123456", pkcs8PEM(t, key))
		}},
		{"client ID through an opaque signer", "Iv23liAbCdEf012345", func() (*creds.AppKey, error) {
			return creds.NewAppKey("Iv23liAbCdEf012345", opaqueSigner{key: key})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				k, err := tc.parse()
				if err != nil {
					t.Fatalf("building the key: %v", err)
				}
				jwt, err := k.JWT(t.Context())
				if err != nil {
					t.Fatalf("JWT: %v", err)
				}
				header, claims, sig := jwtParts(t, jwt)
				if want := `{"alg":"RS256","typ":"JWT"}`; string(header) != want {
					t.Errorf("header = %s, want %s", header, want)
				}
				var got map[string]any
				dec := json.NewDecoder(bytes.NewReader(claims))
				dec.UseNumber()
				if err := dec.Decode(&got); err != nil {
					t.Fatalf("claims %s do not decode: %v", claims, err)
				}
				want := map[string]any{"iat": json.Number("946684740"), "exp": json.Number("946685340"), "iss": tc.issuer}
				if len(got) != len(want) {
					t.Errorf("claims = %s, want exactly iat %d, exp %d and iss %q", claims, iat, exp, tc.issuer)
				}
				for name, v := range want {
					if got[name] != v {
						t.Errorf("claim %s = %v, want %v", name, got[name], v)
					}
				}
				signed := jwt[:strings.LastIndexByte(jwt, '.')]
				digest := sha256.Sum256([]byte(signed))
				if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
					t.Errorf("the signature does not verify under the App's public key: %v", err)
				}
			})
		})
	}
}

func TestAppKey_JWT_failures(t *testing.T) {
	key := appPrivateKey(t)
	t.Run("an ended context", func(t *testing.T) {
		k, err := creds.NewAppKey("123456", key)
		if err != nil {
			t.Fatalf("NewAppKey: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := k.JWT(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("JWT under a cancelled context = %v, want context.Canceled", err)
		}
	})
	t.Run("a failing signer", func(t *testing.T) {
		const leak = "kms said: secret-key-material"
		k, err := creds.NewAppKey("123456", opaqueSigner{key: key, err: errors.New(leak)})
		if err != nil {
			t.Fatalf("NewAppKey: %v", err)
		}
		jwt, err := k.JWT(t.Context())
		if jwt != "" {
			t.Errorf("JWT answered %q beside its failure", jwt)
		}
		fe, ok := errors.AsType[*forgeapi.Error](err)
		if !ok {
			t.Fatalf("JWT error = %v, want a *forgeapi.Error", err)
		}
		if fe.Kind != forgeapi.KindTransient || !fe.Retryable {
			t.Errorf("kind %v retryable %v, want transient and retryable", fe.Kind, fe.Retryable)
		}
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error %q quotes the signer's own text", err)
		}
	})
}

func TestNewAppKey_refusals(t *testing.T) {
	key := appPrivateKey(t)
	small := smallPrivateKey(t)
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("Setup: ecdsa.GenerateKey: %v", err)
	}
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Setup: ed25519.GenerateKey: %v", err)
	}
	for _, tc := range []struct {
		signer crypto.Signer
		name   string
		issuer string
	}{
		{name: "empty issuer", issuer: "", signer: key},
		{name: "issuer with a space", issuer: "Iv23 li", signer: key},
		{name: "issuer with a newline", issuer: "Iv23li\n", signer: key},
		{name: "issuer with a control character", issuer: "Iv23\x00li", signer: key},
		{name: "issuer not UTF-8", issuer: "Iv23\xffli", signer: key},
		{name: "no signer", issuer: "123456", signer: nil},
		{name: "a nil RSA key", issuer: "123456", signer: (*rsa.PrivateKey)(nil)},
		{name: "a nil ECDSA key", issuer: "123456", signer: (*ecdsa.PrivateKey)(nil)},
		{name: "a nil Ed25519 key", issuer: "123456", signer: ed25519.PrivateKey(nil)},
		{name: "a 1024-bit RSA key", issuer: "123456", signer: small},
		{name: "a 1024-bit opaque signer", issuer: "123456", signer: opaqueSigner{key: small}},
		{name: "an ECDSA key", issuer: "123456", signer: ec},
		{name: "an Ed25519 key", issuer: "123456", signer: ed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, err := creds.NewAppKey(tc.issuer, tc.signer)
			if k != nil {
				t.Errorf("NewAppKey answered a key beside its refusal")
			}
			if got := codeOf(err); got != forgeapi.CodeAppKeyInvalid {
				t.Errorf("NewAppKey code = %q (%v), want %q", got, err, forgeapi.CodeAppKeyInvalid)
			}
			if tc.issuer != "" && err != nil && strings.Contains(err.Error(), tc.issuer) {
				t.Errorf("the refusal %q quotes the issuer", err)
			}
		})
	}
}

func TestParseAppKeyPEM_refusals(t *testing.T) {
	key := appPrivateKey(t)
	small := smallPrivateKey(t)
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("Setup: ecdsa.GenerateKey: %v", err)
	}
	ecDER, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		t.Fatalf("Setup: marshal EC key: %v", err)
	}
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Setup: ed25519.GenerateKey: %v", err)
	}
	good := pkcs1PEM(key)
	corrupt := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("not-a-der-key-but-plain-text")})
	for _, tc := range []struct {
		name   string
		issuer string
		pem    []byte
	}{
		{name: "empty issuer", issuer: "", pem: good},
		{name: "issuer with a tab", issuer: "123\t456", pem: good},
		{name: "not PEM", issuer: "123456", pem: []byte("not-a-pem-file-but-plain-text")},
		{name: "empty", issuer: "123456", pem: nil},
		{name: "two blocks", issuer: "123456", pem: append(bytes.Clone(good), good...)},
		{name: "a corrupt PKCS #1 block", issuer: "123456", pem: corrupt},
		{name: "a corrupt PKCS #8 block", issuer: "123456", pem: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not-a-der-key-but-plain-text")})},
		{name: "a public key block", issuer: "123456", pem: pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("x")})},
		{name: "a SEC 1 EC key", issuer: "123456", pem: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER})},
		{name: "a PKCS #8 EC key", issuer: "123456", pem: pkcs8PEM(t, ec)},
		{name: "a PKCS #8 Ed25519 key", issuer: "123456", pem: pkcs8PEM(t, ed)},
		{name: "a 1024-bit PKCS #1 key", issuer: "123456", pem: pkcs1PEM(small)},
		{name: "a 1024-bit PKCS #8 key", issuer: "123456", pem: pkcs8PEM(t, small)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, err := creds.ParseAppKeyPEM(tc.issuer, tc.pem)
			if k != nil {
				t.Errorf("ParseAppKeyPEM answered a key beside its refusal")
			}
			if got := codeOf(err); got != forgeapi.CodeAppKeyInvalid {
				t.Errorf("ParseAppKeyPEM code = %q (%v), want %q", got, err, forgeapi.CodeAppKeyInvalid)
			}
			if err != nil && strings.Contains(err.Error(), "plain-text") {
				t.Errorf("the refusal %q quotes the PEM input", err)
			}
		})
	}
}
