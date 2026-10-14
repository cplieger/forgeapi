// Package readcache revalidates the GET requests of an HTTP client you build
// yourself, for routes no forgeapi operation covers, with the same read cache
// every forgeapi client keeps. The guide is docs/read-cache.md, at
// https://github.com/cplieger/forgeapi/blob/dev/docs/read-cache.md.
package readcache

import (
	"net/http"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// Transport is an [http.RoundTripper] that revalidates the GET requests it carries
// against what it read before, and answers one from a held answer only where the
// server answers its validator with 304 Not Modified. A served answer reaches the
// caller as a 200 with the held body. It is safe for concurrent use.
type Transport struct {
	core *transport.CacheTransport
}

// Option configures a [Transport].
type Option interface {
	apply(*transport.CacheConfig)
}

type optionFunc func(*transport.CacheConfig)

func (f optionFunc) apply(c *transport.CacheConfig) { f(c) }

// NewTransport builds a [Transport] that sends every request through next, which
// is what an attempt observer wraps to see each request that reaches the server.
// An answer from next is served from or kept only where its Request is the request
// next was given, as [http.Transport] reports it, so a round tripper that sets a
// credential or another request header wraps the Transport instead.
// It refuses a nil next with [forgeapi.CodeConnectionInvalid] and a negative
// [WithMaxBytes] with [forgeapi.CodeBudgetInvalid].
func NewTransport(next http.RoundTripper, opts ...Option) (*Transport, error) {
	cfg := transport.CacheConfig{MaxBytes: forgeapi.DefaultReadCacheBytes}
	for _, o := range opts {
		o.apply(&cfg)
	}
	if next == nil {
		return nil, transport.Local(forgeapi.CodeConnectionInvalid, "the read cache has no round tripper to send through")
	}
	if cfg.MaxBytes < 0 {
		return nil, transport.Local(forgeapi.CodeBudgetInvalid, "the read cache bound is negative")
	}
	return &Transport{core: transport.NewCacheTransport(next, cfg)}, nil
}

// RoundTrip sends r, revalidating it where it is a GET carrying no validator of
// its own.
func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.core.RoundTrip(r)
}

// State is what the cache has done since it was built, and the bytes it holds now.
func (t *Transport) State() forgeapi.ReadCacheState { return t.core.State() }

// CloseIdleConnections closes next's idle connections where next has a
// CloseIdleConnections method, so [http.Client.CloseIdleConnections] reaches them.
func (t *Transport) CloseIdleConnections() { t.core.CloseIdleConnections() }

// WithMaxBytes bounds the bytes the cache holds, evicting the least recently used
// entry first. Zero holds nothing and adds no validator, though a 304 to a GET is
// still sent once more without one. A negative maxBytes is refused by
// [NewTransport] with [forgeapi.CodeBudgetInvalid].
//
// Default: [forgeapi.DefaultReadCacheBytes].
func WithMaxBytes(maxBytes int64) Option {
	return optionFunc(func(c *transport.CacheConfig) { c.MaxBytes = maxBytes })
}

// WithFreeNotModified states whether the server bills nothing for a 304 answering
// a conditional request that carries an Authorization header, as GitHub's REST API
// does. It decides only what [forgeapi.ReadCacheState.Saved] counts.
//
// Default: false, so Saved counts nothing.
func WithFreeNotModified(free bool) Option {
	return optionFunc(func(c *transport.CacheConfig) { c.FreeNotModified = free })
}

// WithHeldFields names the response header fields the caller reads from a served
// answer, such as Link for paging. A held field the 304 carries takes the 304's
// value; one it omits keeps the held value, which the 304 did not confirm. A field
// sent on several lines keeps each. Every other field of a served answer is the
// 304's own. Each call replaces the last.
//
// Default: none.
func WithHeldFields(names ...string) Option {
	return optionFunc(func(c *transport.CacheConfig) { c.Held = append([]string(nil), names...) })
}
