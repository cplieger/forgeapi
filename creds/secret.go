package creds

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// mask is what every printing route renders for a Secret holding a value.
const mask = "[redacted]"

// InstallationToken is one minted installation token and the instant GitHub
// stops honouring it.
type InstallationToken struct {
	Token  Secret
	Expiry time.Time
}

// Secret holds a token that must not reach a log by accident. The value is
// held masked in memory, so a printer walking fields by reflection finds only
// masked bytes. Format prints "[redacted]" for every verb, %#v included, and
// String, LogValue and MarshalJSON print the same; the zero Secret prints
// nothing. == does not compile on a Secret, and only Reveal answers the value.
type Secret struct {
	_ [0]func()
	p *masked
}

// newSecret masks token; the empty token is the zero Secret.
func newSecret(token string) Secret {
	if token == "" {
		return Secret{}
	}
	return Secret{p: maskOf(token)}
}

// Reveal answers the token, or "" for the zero Secret.
func (s Secret) Reveal() string { return s.p.open() }

// IsZero reports whether s holds no token.
func (s Secret) IsZero() bool { return s.p == nil }

func (s Secret) render() string {
	if s.IsZero() {
		return ""
	}
	return mask
}

// Format writes "[redacted]", or nothing for the zero Secret, whatever the verb.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.render()) }

// String answers "[redacted]", or "" for the zero Secret.
func (s Secret) String() string { return s.render() }

// LogValue answers the rendering String does, as a string value.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.render()) }

// MarshalJSON encodes the rendering String does as a JSON string.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.render()) }

// maskBlock is the process's masking cipher, keyed once from crypto/rand and
// held nowhere a masked value can reach.
var maskBlock = sync.OnceValue(func() cipher.Block {
	key := make([]byte, 32)
	rand.Read(key) // never returns an error (Go 1.24+); it crashes instead
	// aes.NewCipher errors only on a key length other than 16, 24 or 32.
	block, _ := aes.NewCipher(key)
	return block
})

// masked is iv || AES-256-CTR(text), with a fresh random iv per value.
type masked struct {
	b []byte
}

func maskOf(text string) *masked {
	iv := make([]byte, aes.BlockSize)
	rand.Read(iv) // never returns an error (Go 1.24+); it crashes instead
	b := make([]byte, aes.BlockSize+len(text))
	copy(b, iv)
	cipher.NewCTR(maskBlock(), iv).XORKeyStream(b[aes.BlockSize:], []byte(text))
	return &masked{b: b}
}

func (m *masked) open() string {
	if m == nil {
		return ""
	}
	out := make([]byte, len(m.b)-aes.BlockSize)
	cipher.NewCTR(maskBlock(), m.b[:aes.BlockSize]).XORKeyStream(out, m.b[aes.BlockSize:])
	return string(out)
}
