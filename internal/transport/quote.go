package transport

import (
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"sync"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/go/masked"
	"github.com/cplieger/go/textsafe"
)

// Quoted is text an instance, a proxy, the network or a credential source
// supplied, made safe to carry on an error or a log line. Only [quoter.quote]
// mints one, so a sink that takes a Quoted cannot be handed text no quoter saw.
type Quoted struct{ text string }

// String is the quoted text.
func (q Quoted) String() string { return q.text }

// LogValue is the quoted text, which a line carries as a string.
func (q Quoted) LogValue() slog.Value { return slog.StringValue(q.text) }

// quoter redacts every secret one exchange can carry out of a text, which it makes
// one line and bounds. A secret is matched as given, as received with a field
// value's edge whitespace trimmed (RFC 9110 section 5.5, and net/http trims it on
// write), as [jsonForm] spells each, since a message member that is not a string
// is quoted as its JSON, and as a form body encodes each ([url.QueryEscape]),
// since an endpoint answer can echo the body it was sent. Each spelling is held
// masked and revealed only for the redaction. A nil quoter holds no secret.
type quoter struct{ secrets []masked.Secret }

// connectionQuoter holds the secrets every exchange of a connection carries: each
// value of its own headers, and its proxy password with the Basic credentials
// net/http builds from it.
func connectionQuoter(headers []forgeapi.Header, proxyUser string, proxyPass masked.Secret) *quoter {
	given := make([]masked.Secret, 0, len(headers)+3)
	for _, h := range headers {
		given = append(given, h.Value)
	}
	if !proxyPass.IsZero() {
		basic := base64.StdEncoding.EncodeToString([]byte(proxyUser + ":" + proxyPass.Reveal()))
		given = append(given, proxyPass, masked.NewSecret("Basic "+basic), masked.NewSecret(basic))
	}
	return (&quoter{}).with(given...)
}

// with is q holding carried as well, the values one exchange carries beside the
// connection's own; q itself where carried adds no spelling.
func (q *quoter) with(carried ...masked.Secret) *quoter {
	var held []masked.Secret
	if q != nil {
		held = q.secrets
	}
	secrets := slices.Clone(held)
	for _, secret := range carried {
		given := secret.Reveal()
		received := textproto.TrimString(given)
		var spelled []string
		for _, spelling := range []string{
			given, jsonForm(given), url.QueryEscape(given),
			received, jsonForm(received), url.QueryEscape(received),
		} {
			if spelling != "" && !slices.Contains(spelled, spelling) {
				spelled = append(spelled, spelling)
				secrets = append(secrets, masked.NewSecret(spelling))
			}
		}
	}
	if q != nil && len(secrets) == len(held) {
		return q
	}
	return &quoter{secrets: secrets}
}

// own is text this library wrote, which carries no secret, so it is not redacted:
// a short connection secret would otherwise rewrite the library's own words.
func own(text string) Quoted { return (*quoter)(nil).quote(text) }

func (q *quoter) quote(text string) Quoted {
	var secrets []string
	if q != nil {
		secrets = make([]string, len(q.secrets))
		for i, secret := range q.secrets {
			secrets[i] = secret.Reveal()
		}
	}
	out, _ := textsafe.RedactSingleLineCapped(text, maxMessageBytes, "...", secrets...)
	return Quoted{text: out}
}

// recentTokens is the last [recentTokenCount] distinct tokens a connection's
// requests carried, least recently carried first, which [Conn.Quote] redacts from
// text quoted with no exchange in hand.
type recentTokens struct {
	held []masked.Secret
	mu   sync.Mutex
}

const recentTokenCount = 8

// add makes token the most recently carried, so the next eviction takes the least
// recently carried token, never one a request carried again.
func (r *recentTokens) add(token string) {
	if token == "" {
		return
	}
	secret := masked.NewSecret(token)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.held = append(slices.DeleteFunc(r.held, secret.Equal), secret)
	if len(r.held) > recentTokenCount {
		r.held = slices.Delete(r.held, 0, 1)
	}
}

func (r *recentTokens) all() []masked.Secret {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.held)
}

// quoterFor is the quoter of an exchange carrying token.
func (c *Conn) quoterFor(token string) *quoter { return c.quotes.with(masked.NewSecret(token)) }

// ambient is the quoter of text no exchange is in hand for: the connection's own
// secrets and the tokens its requests carried lately.
func (c *Conn) ambient() *quoter { return c.quotes.with(c.recent.all()...) }

// orAmbient is q, or [Conn.ambient] where q is nil.
func (c *Conn) orAmbient(q *quoter) *quoter {
	if q != nil {
		return q
	}
	return c.ambient()
}

// Quote is text with no exchange in hand made safe to carry, as [Conn.ambient]
// quotes it: a family's value from an answer it no longer holds.
func (c *Conn) Quote(text string) Quoted { return c.ambient().quote(text) }

// quotedPool is the family's pool reader with the pool's name quoted by q, nil
// where the family reads no pool.
func (c *Conn) quotedPool(q *quoter) budgetPool {
	if c.pool == nil {
		return nil
	}
	return func(header http.Header) (string, int) {
		name, limit := c.pool(header)
		return q.quote(name).String(), limit
	}
}
