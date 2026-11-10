package creds_test

import (
	"strings"
	"testing"
	"unicode"

	"github.com/cplieger/forgeapi/v2"
	"github.com/cplieger/forgeapi/v2/creds"
)

// FuzzParseAppKeyPEM_never_echoes_its_input holds both inputs to the
// key-custody rule: a refusal never quotes the PEM or the issuer, and an
// accepted issuer holds no whitespace or non-printable character.
func FuzzParseAppKeyPEM_never_echoes_its_input(f *testing.F) {
	key, err := appRSAKey()
	if err != nil {
		f.Fatalf("Setup: rsa.GenerateKey: %v", err)
	}
	good := pkcs1PEM(key)
	f.Add("Iv23liAbCdEf012345", good)
	f.Add("123456", good)
	f.Add("Iv23 li", good)
	f.Add("Iv23\u00a0li", good)
	f.Add("Iv23\u200bli", good)
	f.Add("123456", []byte("-----BEGIN RSA PRIVATE KEY-----\nbm90LWEta2V5\n-----END RSA PRIVATE KEY-----\n"))
	f.Add("123456", good[:len(good)/2])
	f.Fuzz(func(t *testing.T, issuer string, pemBytes []byte) {
		// The marker opens both inputs and no refusal's own wording, so an
		// echo of either input's start shows in the message. pem.Decode skips
		// text before a block, so the PEM still parses as it would bare.
		const marker = "zqx"
		issuer = marker + issuer
		pemBytes = append([]byte(marker+"\n"), pemBytes...)
		k, err := creds.ParseAppKeyPEM(issuer, pemBytes)
		if err == nil {
			if k == nil {
				t.Fatal("ParseAppKeyPEM answered neither a key nor an error")
			}
			for _, r := range issuer {
				if unicode.IsSpace(r) || !unicode.IsPrint(r) {
					t.Fatalf("accepted issuer %q holds %U", issuer, r)
				}
			}
			return
		}
		if got := codeOf(err); got != forgeapi.CodeAppKeyInvalid {
			t.Fatalf("refusal code = %q, want %q", got, forgeapi.CodeAppKeyInvalid)
		}
		if msg := err.Error(); strings.Contains(msg, marker) || strings.Contains(msg, "-----") {
			t.Fatalf("the refusal %q quotes its input", msg)
		}
	})
}
