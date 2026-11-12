package creds_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"sync"
	"testing"
)

// appRSAKey is one 2048-bit RSA key the package's tests share, since
// generating one per test would dominate the suite's run time.
var appRSAKey = sync.OnceValues(func() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 2048)
})

// smallRSAKey is a 1024-bit key, under GitHub's 2048-bit floor.
var smallRSAKey = sync.OnceValues(func() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 1024)
})

func smallPrivateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := smallRSAKey()
	if err != nil {
		t.Fatalf("Setup: rsa.GenerateKey(1024): %v", err)
	}
	return key
}

func appPrivateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := appRSAKey()
	if err != nil {
		t.Fatalf("Setup: rsa.GenerateKey: %v", err)
	}
	return key
}

// pkcs1PEM is key as GitHub's downloaded App key file holds it.
func pkcs1PEM(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func pkcs8PEM(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("Setup: x509.MarshalPKCS8PrivateKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// opaqueSigner is a crypto.Signer that is not an *rsa.PrivateKey, the shape a
// KMS or HSM signer has.
type opaqueSigner struct {
	key *rsa.PrivateKey
	err error
}

func (s opaqueSigner) Public() crypto.PublicKey { return s.key.Public() }

func (s opaqueSigner) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.key.Sign(r, digest, opts)
}
