package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// cacheMode is how one send treats the read cache.
type cacheMode int

const (
	// cacheOff sends the request as built: not a cacheable read, or the cache is
	// switched off.
	cacheOff cacheMode = iota
	// cacheConditional looks the request up and sends the held entry's validator.
	cacheConditional
	// cacheUnconditional keys the request without looking it up, for the resend a
	// not-modified answer forces when nothing held may serve it ([Conn.sendRead]).
	cacheUnconditional
)

// verdict is what [Conn.judge] decided an answer does: [Conn.sendRead] acts on a
// resend, and [Conn.answer] on the rest.
type verdict int

const (
	// verdictNone is any answer but a 304, which its status settles.
	verdictNone verdict = iota
	// verdictServe answers a 304 from the held entry.
	verdictServe
	// verdictResend sends the read once more without a validator.
	verdictResend
	// verdictRefuse maps a 304 as an unexpected status.
	verdictRefuse
)

// revalidation is what one send knows of its request in the read cache.
type revalidation struct {
	held      *pageEntry
	check     *wireCheck
	token     string
	key       string
	gen       uint64
	verdict   verdict
	cacheable bool
	// answered is what [Conn.answer] found: the instance's answer read in full.
	answered bool
	// disowned says a resend ([Conn.sendRead]) was not keyed as the attempt it
	// replaced: key is that attempt's, which the exchange forgets, keeping nothing.
	disowned bool
}

// vouched reports whether the wire vouches for resp and body as the keyed
// exchange's own: only such an answer is served or kept.
func (rv *revalidation) vouched(resp *http.Response, body []byte) bool {
	return rv.cacheable && rv.check.vouches(resp) && rv.check.carried(body)
}

// wireCheckKey carries a cacheable request's [wireCheck] to [wireGuard].
type wireCheckKey struct{}

// wireCheck is the request a cacheable send keyed, as [wireGuard] compares every
// attempt and redirect hop against it, and the last answer the wire gave it.
type wireCheck struct {
	header   http.Header
	answered http.Header
	answer   *http.Response
	body     *wireBody
	method   string
	target   string
	host     string
	status   int
	mu       sync.Mutex
	reached  atomic.Bool
	diverged atomic.Bool
}

func newWireCheck(r *http.Request) *wireCheck {
	return &wireCheck{header: r.Header.Clone(), method: r.Method, target: r.URL.String(), host: effectiveHost(r)}
}

// effectiveHost is the Host a request is sent with: net/http sends Request.Host
// over the URL's host, and a hook can set it with the URL and Header unchanged.
func effectiveHost(r *http.Request) string {
	if r.Host != "" {
		return r.Host
	}
	return r.URL.Host
}

func (w *wireCheck) matches(r *http.Request) bool {
	return r.Method == w.method && r.URL.String() == w.target && effectiveHost(r) == w.host &&
		maps.EqualFunc(r.Header, w.header, slices.Equal[[]string])
}

// record notes resp as the wire's answer to the latest attempt, its body hashed as
// the exchange reads it.
func (w *wireCheck) record(resp *http.Response) {
	body := &wireBody{ReadCloser: resp.Body, sum: sha256.New()}
	resp.Body = body
	w.mu.Lock()
	defer w.mu.Unlock()
	w.answer, w.status, w.answered, w.body = resp, resp.StatusCode, resp.Header.Clone(), body
}

// vouches reports whether every request of the exchange reached the wire as keyed
// and resp is the wire's last answer, its status and headers, as it gave it. An
// observer that changed a request or those, a redirect hop, or a request that lost
// the context carrying the check is not vouched for; [wireCheck.carried] vouches
// for the body.
func (w *wireCheck) vouches(resp *http.Response) bool {
	if !w.reached.Load() || w.diverged.Load() {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return resp == w.answer && resp.StatusCode == w.status && maps.EqualFunc(resp.Header, w.answered, slices.Equal[[]string])
}

// carried reports whether body is all the wire's last answer carried, byte for byte.
func (w *wireCheck) carried(body []byte) bool {
	w.mu.Lock()
	b := w.body
	w.mu.Unlock()
	if b == nil || !b.done {
		return false
	}
	sum := sha256.Sum256(body)
	return bytes.Equal(b.sum.Sum(nil), sum[:])
}

// wireBody hashes an answer's body as it is read, to the end.
type wireBody struct {
	io.ReadCloser
	sum  hash.Hash
	done bool
}

func (b *wireBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.sum.Write(p[:n])
	if errors.Is(err, io.EOF) {
		b.done = true
	}
	return n, err
}

// wireGuard sits beneath both observers and above the wire. A request that differs
// from the one its key was computed from leaves without validators, because the
// held entry answers only for the keyed request (RFC 9110 section 8.8.1).
type wireGuard struct {
	next http.RoundTripper
}

func (g wireGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	check, _ := r.Context().Value(wireCheckKey{}).(*wireCheck)
	if check != nil {
		check.reached.Store(true)
		if check.matches(r) {
			resp, err := g.next.RoundTrip(r)
			if err == nil {
				check.record(resp)
			}
			return resp, err
		}
		check.diverged.Store(true)
	}
	if r.Header.Get("If-None-Match") == "" && r.Header.Get("If-Modified-Since") == "" {
		return g.next.RoundTrip(r)
	}
	stripped := r.Clone(r.Context())
	stripped.Header.Del("If-None-Match")
	stripped.Header.Del("If-Modified-Since")
	return g.next.RoundTrip(stripped)
}

// cacheNoteKey carries the per-request record's cache attribute.
type cacheNoteKey struct{}

// The values of the record's cache attribute.
const (
	cacheMissed int32 = iota + 1
	cacheHit
)

func noteCache(ctx context.Context, v int32) {
	if note, ok := ctx.Value(cacheNoteKey{}).(*atomic.Int32); ok {
		note.Store(v)
	}
}

// cacheNoted is the record's cache attribute, as [readCache.tally] counts: hit for a
// read the held answer served, miss for any other cacheable read the instance
// answered in full, off for every other line, a failure in transit included.
func cacheNoted(ctx context.Context) string {
	note, ok := ctx.Value(cacheNoteKey{}).(*atomic.Int32)
	if !ok {
		return "off"
	}
	switch note.Load() {
	case cacheHit:
		return "hit"
	case cacheMissed:
		return "miss"
	}
	return "off"
}

// cacheable reports whether a request is a read the cache may revalidate: a GET of
// a REST route naming the value it decodes into, outside a write, with the cache
// on. A document is a POST; [Conn.revalidate] leaves a no-store request uncached.
func (c *Conn) cacheable(ctx context.Context, req *Request) bool {
	return c.cache != nil && req.Method == http.MethodGet && !req.Document && !writes(ctx) && req.Into != nil
}

// revalidate keys a cacheable request and, in the conditional mode, sets the held
// entry's validator on it. It runs after every other header is written, so the key
// folds the request as it leaves this package and nothing here replaces the
// validator; [wireGuard] holds every attempt and hop to that request. A request
// carrying no-store is left uncached (RFC 9111 section 5.2.1.5).
func (c *Conn) revalidate(req *Request, r *http.Request, mode cacheMode, rv *revalidation) {
	if noStore(r.Header) {
		return
	}
	rv.cacheable = true
	rv.key = c.cache.key(rv.token, shapeOf(req.Into), r.URL.String(), r.Header)
	if mode != cacheConditional {
		return
	}
	e := c.cache.lookup(rv.key)
	if e == nil {
		return
	}
	if e.page.ETag != "" {
		r.Header.Set("If-None-Match", e.page.ETag)
	} else {
		r.Header.Set("If-Modified-Since", e.page.LastModified)
	}
	rv.held = e.page
}

// serve answers a not-modified exchange [Conn.judge] found the held entry serves: its body, and
// the exchange's own headers with the held fields they omit ([heldFields]). It
// answers the entry kept from here on, nil where the exchange says no-store, which
// forgets the entry after this answer (RFC 9111 section 5.2.2.5).
func (c *Conn) serve(rv revalidation, out *Response) *pageEntry {
	p := rv.held.refreshed(out.Header)
	if c.freeNotMod && rv.token != "" {
		c.cache.saved.Add(1)
	}
	out.Header = p.served(out.Header)
	out.Status = http.StatusOK
	out.Body = slices.Clone(p.Body)
	out.Revalidated = true
	if noStore(out.Header) {
		return nil
	}
	return p
}

// keepable is the entry a vouched 200 leaves for a later revalidation, its body the
// projection decoded holds, nil where the answer carries no well-formed validator or
// says no-store.
func keepable(resp *http.Response, decoded any) *pageEntry {
	if resp.StatusCode != http.StatusOK || noStore(resp.Header) {
		return nil
	}
	etag, modified := entityTag(resp.Header), httpDate(resp.Header)
	if etag == "" && modified == "" {
		return nil
	}
	body, err := ownedBody(decoded)
	if err != nil {
		return nil
	}
	return &pageEntry{
		ETag: strings.Clone(etag), LastModified: strings.Clone(modified), Body: body, Fields: holdFields(resp.Header),
	}
}

// shapeOf names the Go type a body decodes into, which is what the kept projection
// answers for.
func shapeOf(v any) string {
	return fmt.Sprintf("%T", v)
}
