package creds

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/internal/transport"
)

// GitHub refuses an App JWT whose exp lies more than 10 minutes ahead and
// recommends an iat 60 seconds in the past, so a minute of clock skew either
// way leaves a 9-minute lifetime inside the limit:
// https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app
const (
	jwtBackdate = 60 * time.Second
	jwtLifetime = 9 * time.Minute
)

// minAppKeyBits is the smallest RSA modulus an AppKey accepts: GitHub issues
// 2048-bit App keys, so a smaller signer is a misconfiguration.
const minAppKeyBits = 2048

// jwtHeader is every App JWT's header segment, RS256 being the one algorithm
// GitHub accepts for an App JWT.
var jwtHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))

// AppKey is one GitHub App's identity: the issuer GitHub names it by, and
// the signer holding its private key. It signs the App's JWT and nothing else,
// and no error, log line or method answers the key. It is safe for concurrent
// use where its signer is. The zero value is not usable: build one with
// [NewAppKey] or [ParseAppKeyPEM].
type AppKey struct {
	signer crypto.Signer
	issuer string
}

// NewAppKey builds the key of the App GitHub names issuer, its client ID or
// its numeric App ID, signing through signer, so a key held in a KMS or HSM
// never enters the process. It refuses with [forgeapi.CodeAppKeyInvalid] an
// issuer that is empty or holds whitespace or a non-printable character, a
// nil signer, and a signer whose public key is not RSA or is under 2048 bits.
func NewAppKey(issuer string, signer crypto.Signer) (*AppKey, error) {
	if err := validIssuer(issuer); err != nil {
		return nil, err
	}
	if nilSigner(signer) {
		return nil, appKeyInvalid("the App key has no signer")
	}
	pub, ok := signer.Public().(*rsa.PublicKey)
	if !ok || pub == nil || pub.N == nil {
		return nil, appKeyInvalid("the App key is not an RSA key")
	}
	if pub.N.BitLen() < minAppKeyBits {
		return nil, appKeyInvalid("the App key's RSA modulus is under 2048 bits")
	}
	return &AppKey{signer: signer, issuer: issuer}, nil
}

// ParseAppKeyPEM builds the key of the App GitHub names issuer from the
// private key file GitHub issued it: one PEM block holding a PKCS #1 or PKCS #8
// RSA key. It refuses with [forgeapi.CodeAppKeyInvalid] whatever
// [NewAppKey] refuses, and PEM that is malformed, holds anything after its one
// block, or holds any other kind of key. No refusal quotes the input.
func ParseAppKeyPEM(issuer string, pemBytes []byte) (*AppKey, error) {
	if err := validIssuer(issuer); err != nil {
		return nil, err
	}
	block, rest := pem.Decode(pemBytes)
	if block == nil {
		return nil, appKeyInvalid("the App key is not PEM")
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, appKeyInvalid("the App key's PEM holds more than one block")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, appKeyInvalid("the App key's PKCS #1 block does not parse")
		}
		key = parsed
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, appKeyInvalid("the App key's PKCS #8 block does not parse")
		}
		rsaKey, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, appKeyInvalid("the App key is not an RSA key")
		}
		key = rsaKey
	default:
		return nil, appKeyInvalid("the App key's PEM block is not an RSA private key")
	}
	return NewAppKey(issuer, key)
}

// JWT answers a fresh RS256 App JWT: iss the issuer, iat 60 seconds in the
// past, exp 9 minutes ahead. Its signature matches actions/scaleset's
// JWTProviderFunc, so scaleset.JWTProviderFunc(key.JWT) is that package's
// JWTProvider. It fails with ctx's error where ctx has ended, and as
// transient and retryable where the signer fails.
func (k *AppKey) JWT(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	now := time.Now()
	// A struct of two integers and a validated string always marshals.
	claims, _ := json.Marshal(struct {
		Issuer    string `json:"iss"`
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
	}{k.issuer, now.Add(-jwtBackdate).Unix(), now.Add(jwtLifetime).Unix()})
	signing := jwtHeader + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := k.signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return "", signFailed()
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// validIssuer refuses an issuer GitHub could not read as a client ID or an
// App ID, without quoting it.
func validIssuer(issuer string) error {
	if issuer == "" {
		return appKeyInvalid("the App key's issuer is empty")
	}
	if !utf8.ValidString(issuer) {
		return appKeyInvalid("the App key's issuer is not UTF-8")
	}
	for _, r := range issuer {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return appKeyInvalid("the App key's issuer holds whitespace or a non-printable character")
		}
	}
	return nil
}

func appKeyInvalid(message string) error {
	return transport.Local(forgeapi.CodeAppKeyInvalid, message)
}

// signFailed carries none of the signer's own text, which is the signer's to
// report and may come from a remote KMS.
func signFailed() error {
	return &forgeapi.Error{
		Kind: forgeapi.KindTransient, Retryable: transport.Retryable(forgeapi.KindTransient, 0),
		Message: "the App key's signer could not sign the App JWT",
	}
}

// nilSigner reports whether signer is nil, a typed nil included: the standard
// library's keys dereference themselves in Public, so a nil *rsa.PrivateKey
// would panic there rather than be refused.
func nilSigner(signer crypto.Signer) bool {
	if signer == nil {
		return true
	}
	v := reflect.ValueOf(signer)
	switch v.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return false
}
