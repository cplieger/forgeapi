package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

func clientPair(t *testing.T) (certPEM, keyPEM []byte) {
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

func TestAConnectionsClientCertificateIsPresentedToAnInstanceThatAsksForOne(t *testing.T) {
	presented := &atomic.Int64{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented.Store(int64(len(r.TLS.PeerCertificates)))
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	certPEM, keyPEM := clientPair(t)
	c, err := Open(&forgeapi.Connection{
		WebBaseURL: srv.URL,
		CABytes:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}),
		ClientCert: certPEM,
		ClientKey:  keyPEM,
	}, settingsFor(
		forgeapi.WithCredentialSource(stubCredential{}),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(discardLogger()),
	), testOptions())
	if err != nil {
		t.Fatalf("Open with a client certificate pair = %v, want a connection", err)
	}
	t.Cleanup(c.Close)
	if _, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
		t.Fatalf("a read from an instance requiring a client certificate = %v, want nil", err)
	}
	if got := presented.Load(); got != 1 {
		t.Errorf("the instance saw %d client certificate(s), want 1", got)
	}
}

func TestAHalfPresentClientCertificatePairIsRefusedAsHalfPresent(t *testing.T) {
	certPEM, keyPEM := clientPair(t)
	for _, test := range []struct {
		name string
		conn forgeapi.Connection
	}{
		{name: "a_certificate_alone", conn: forgeapi.Connection{WebBaseURL: "https://forge.example.com", ClientCert: certPEM}},
		{name: "a_key_alone", conn: forgeapi.Connection{WebBaseURL: "https://forge.example.com", ClientKey: keyPEM}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Open(&test.conn, settingsFor(
				forgeapi.WithCredentialSource(stubCredential{}),
				forgeapi.WithLogger(discardLogger()),
			), testOptions())
			var fe *forgeapi.Error
			if !asError(err, &fe) || fe.Code != forgeapi.CodeConnectionInvalid {
				t.Fatalf("Open with %s = %v, want code %q", test.name, err, forgeapi.CodeConnectionInvalid)
			}
			if !strings.Contains(fe.Message, "half present") {
				t.Errorf("Open with %s = message %q, want it to name the pair as half present", test.name, fe.Message)
			}
		})
	}
}
