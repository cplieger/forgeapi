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

	"github.com/cplieger/httpx/v5"
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
	// not-modified answer forces when nothing held may serve it ([readExchange]).
	cacheUnconditional
)

// verdict is what [judge] decided an answer does: [readExchange] acts on a resend,
// and its owner on the rest.
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
	// answered says the instance's answer was read as its owner reads one, which
	// [readCache.settle] counts.
	answered bool
	// disowned says a resend ([readExchange]) was not keyed as the attempt it
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

// wireGuard sits between the request its owner keyed and the wire, beneath every
// observer [Conn] installs. A request that differs from the one its key was computed
// from leaves without validators, because the held entry answers only for the keyed
// request (RFC 9110 section 8.8.1), and an answer reporting that next sent another
// request, or naming none, is not vouched for.
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
				if !sentAsKeyed(check, resp) {
					check.diverged.Store(true)
				}
				check.record(resp)
			}
			return resp, err
		}
		check.diverged.Store(true)
	}
	if !carriesValidator(r.Header) {
		return g.next.RoundTrip(r)
	}
	stripped := r.Clone(r.Context())
	maps.DeleteFunc(stripped.Header, func(name string, _ []string) bool { return isValidator(name) })
	return g.next.RoundTrip(stripped)
}

// sentAsKeyed reports whether resp answers the request check keyed, by the request
// resp says next sent, as net/http's Transport reports it. An answer naming no
// request vouches for none, since next may have rewritten the one it was given.
func sentAsKeyed(check *wireCheck, resp *http.Response) bool {
	return resp.Request != nil && check.matches(resp.Request)
}

// isValidator reports whether name is If-None-Match or If-Modified-Since, however
// it is cased.
func isValidator(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "If-None-Match", "If-Modified-Since":
		return true
	}
	return false
}

// carriesValidator reports whether h has a validator line with text under any
// name net/http sends it by; an empty line names nothing (RFC 9110 section 5.6.1).
func carriesValidator(h http.Header) bool {
	for name, lines := range h {
		if isValidator(name) && hasText(lines) {
			return true
		}
	}
	return false
}

// hasText reports whether any of a field's lines is not empty.
func hasText(lines []string) bool {
	return slices.ContainsFunc(lines, func(line string) bool { return line != "" })
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
// on. A document is a POST; [readCache.revalidate] leaves a no-store request uncached.
func (c *Conn) cacheable(ctx context.Context, req *Request) bool {
	return c.cache != nil && req.Method == http.MethodGet && !req.Document && !writes(ctx) && req.Into != nil
}

// revalidate keys a cacheable request by its credential (rv.token) and shape, and in
// the conditional mode sets the held entry's validator on it. It answers the
// request to send, carrying the [wireCheck] [wireGuard] holds every attempt and hop
// to. Its owner calls it after every other header is written, so the key folds the
// request as it leaves and nothing replaces the validator. A request carrying
// no-store is left uncached (RFC 9111 section 5.2.1.5).
func (rc *readCache) revalidate(r *http.Request, shape string, mode cacheMode, rv *revalidation) *http.Request {
	if noStore(r.Header) {
		return r
	}
	rv.cacheable = true
	rv.key = rc.key(rv.token, shape, r)
	if mode == cacheConditional {
		rc.condition(r, rv)
	}
	rv.check = newWireCheck(r)
	return r.WithContext(context.WithValue(r.Context(), wireCheckKey{}, rv.check))
}

// condition sets the validator of the entry held for rv's key on r, and notes that
// entry as the one a 304 may serve.
func (rc *readCache) condition(r *http.Request, rv *revalidation) {
	e := rc.lookup(rv.key)
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

// readExchange is one read through a cache, the sequence [Conn] and [CacheTransport]
// share: the first attempt in the cache mode the read takes, in the generation
// current as it begins, each answer judged by [judge], and a 304 nothing held may
// serve sent once more without a validator, whether or not the read may be stored.
// The resend belongs to the first attempt's exchange, whose key and generation the
// owner settles once ([readCache.settle]).
type readExchange struct {
	cache *readCache
	send  func(mode cacheMode, gen uint64) (*http.Response, revalidation, error)
	// discarded takes the header of an answer the exchange drops: a resent 304's,
	// on a cacheable read only where the wire vouches for it, and a 304 that fails
	// the exchange.
	discarded  func(http.Header)
	cacheable  bool
	resendable bool
}

func (x readExchange) run() (*http.Response, revalidation, error) {
	mode, gen := cacheOff, uint64(0)
	if x.cacheable {
		mode, gen = cacheConditional, x.cache.generation()
	}
	resp, rv, err := x.attempt(mode, gen, nil)
	if err != nil || rv.verdict != verdictResend {
		return resp, rv, err
	}
	if rv.check == nil || rv.check.vouches(resp) {
		x.discarded(resp.Header)
	}
	if mode == cacheConditional {
		mode = cacheUnconditional
	}
	return x.attempt(mode, gen, &rv)
}

// attempt is one send of the read and the verdict [judge] gives its answer. first is
// the attempt a resend replaces, nil on the first one, which alone may be resent.
func (x readExchange) attempt(mode cacheMode, gen uint64, first *revalidation) (*http.Response, revalidation, error) {
	resp, rv, err := x.send(mode, gen)
	if first != nil && first.cacheable && rv.key != first.key {
		rv.key, rv.disowned = first.key, true
	}
	if err != nil {
		return resp, rv, err
	}
	rv.verdict, err = judge(x.cache, &rv, resp, first == nil && x.resendable)
	if err != nil {
		x.discarded(resp.Header)
		return nil, rv, err
	}
	return resp, rv, nil
}

// judge is the one decision of what a 304 does, and [readExchange.attempt] its one
// caller; any other answer is [verdictNone]. It reads a 304's body in full and puts
// it back for the owner. Only the wire's own 304, in the generation the send began
// in, to a validator a held entry sent and naming that entry ([pageEntry.answers])
// serves. Any other is resent where resend allows, and refused otherwise; a body
// that fails to read is resent likewise, and is otherwise the exchange's failure.
func judge(rc *readCache, rv *revalidation, resp *http.Response, resend bool) (verdict, error) {
	if resp.StatusCode != http.StatusNotModified {
		return verdictNone, nil
	}
	body, err := httpx.ReadLimitedBody(resp.Body, maxBodyBytes)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	switch {
	case err == nil && rv.held != nil && rv.vouched(resp, body) && rv.held.answers(rc.held, resp.Header) && rc.current(rv.gen):
		return verdictServe, nil
	case resend:
		return verdictResend, nil
	}
	return verdictRefuse, err
}

// serve answers a not-modified exchange [judge] found the held entry serves: the
// live header with each held field it omits, a copy of the held body, and the entry
// kept from here on, nil where that header says no-store, which forgets the entry
// after this answer (RFC 9111 section 5.2.2.5).
func (rc *readCache) serve(rv *revalidation, live http.Header) (http.Header, []byte, *pageEntry) {
	p := rv.held.refreshed(rc.held, live)
	if rc.freeNotMod && rv.token != "" {
		rc.saved.Add(1)
	}
	header := p.served(rc.held, live)
	if noStore(header) {
		return header, slices.Clone(p.Body), nil
	}
	return header, slices.Clone(p.Body), p
}

// serve is [readCache.serve] onto a call's answer, marked as revalidated.
func (c *Conn) serve(rv *revalidation, out *Response) *pageEntry {
	header, body, kept := c.cache.serve(rv, out.Header)
	out.Header, out.Body, out.Status, out.Revalidated = header, body, http.StatusOK, true
	return kept
}

// keepable is the entry a vouched 200 leaves for a later revalidation, its body what
// body answers, nil where the answer carries no well-formed validator, says
// no-store, or body fails.
func (rc *readCache) keepable(resp *http.Response, body func() ([]byte, error)) *pageEntry {
	if resp.StatusCode != http.StatusOK || noStore(resp.Header) {
		return nil
	}
	etag, modified := entityTag(resp.Header), httpDate(resp.Header)
	if etag == "" && modified == "" {
		return nil
	}
	b, err := body()
	if err != nil {
		return nil
	}
	return &pageEntry{
		ETag: strings.Clone(etag), LastModified: strings.Clone(modified), Body: b, Fields: rc.held.hold(resp.Header),
	}
}

// shapeOf names the Go type a body decodes into, which is what the kept projection
// answers for.
func shapeOf(v any) string {
	return fmt.Sprintf("%T", v)
}
