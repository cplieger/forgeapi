package transport

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/cplieger/forgeapi"
)

// CacheConfig is what a [CacheTransport] is built from.
type CacheConfig struct {
	// Held are the response header fields the owner reads from a served answer.
	Held []string
	// MaxBytes bounds what the cache holds; zero switches it off.
	MaxBytes int64
	// FreeNotModified says the product bills nothing for a not-modified answer to
	// an authorized conditional request.
	FreeNotModified bool
}

// CacheTransport is the read cache over a caller's own round tripper, with the
// code [Conn]'s cache runs: [readExchange] sends, [judge] decides a 304,
// [readCache.serve] and [readCache.keepable] answer and keep, and [readCache.conclude]
// settles. It keeps the raw body, since nothing here decodes it, and the request's
// Authorization lines are its credential.
type CacheTransport struct {
	next  http.RoundTripper
	cache *readCache
}

// rawShape is the shape a [CacheTransport] keys every read by: the body as sent.
const rawShape = "[]byte"

// errNotKept is a body [CacheTransport] reads and does not keep.
var errNotKept = errors.New("body not kept")

// NewCacheTransport builds a [CacheTransport] sending through next.
func NewCacheTransport(next http.RoundTripper, cfg CacheConfig) *CacheTransport {
	held := heldTable{}
	for _, name := range cfg.Held {
		name = strings.Clone(http.CanonicalHeaderKey(name))
		if !slices.ContainsFunc(held, func(f heldField) bool { return f.name == name }) {
			held = append(held, heldField{name: name})
		}
	}
	return &CacheTransport{next: next, cache: newReadCache(cfg.MaxBytes, held, cfg.FreeNotModified)}
}

// RoundTrip revalidates a GET that carries no validator of its own, and sends
// every other request through next as given. [readCache.conclude] owns what a read
// whose context ended answers.
func (t *CacheTransport) RoundTrip(orig *http.Request) (*http.Response, error) {
	if orig.Method != http.MethodGet {
		return t.next.RoundTrip(orig)
	}
	r := orig.Clone(orig.Context())
	r.Header = canonicalHeader(orig.Header)
	if carriesValidator(r.Header) {
		return t.next.RoundTrip(orig)
	}
	token := strings.Join(r.Header.Values("Authorization"), "\n")
	resp, rv, err := readExchange{
		cache: t.cache,
		send: func(mode cacheMode, gen uint64) (*http.Response, revalidation, error) {
			rv := revalidation{gen: gen, token: token}
			sent := r.Clone(r.Context())
			if mode != cacheOff {
				sent = t.cache.revalidate(sent, rawShape, mode, &rv)
			}
			resp, err := wireGuard{next: t.next}.RoundTrip(sent)
			return resp, rv, err
		},
		discarded:  func(http.Header) {},
		cacheable:  t.cache != nil,
		resendable: true,
	}.run()
	if err != nil {
		if _, ended := t.cache.conclude(r.Context(), &rv, nil, false); ended != nil {
			return nil, ended
		}
		return nil, err
	}
	out, kept, ok := t.answer(resp, &rv)
	out.Request = orig
	if _, err := t.cache.conclude(r.Context(), &rv, kept, ok); err != nil {
		out.Body.Close()
		return nil, err
	}
	return out, nil
}

// canonicalHeader is h with every field under its canonical name, its lines in
// sorted-name order, the order HTTP/1.1 writes them in. Every reader of a request's
// header looks a field up by that name and the key folds one name per field, while
// net/http sends a field under any name.
func canonicalHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for _, name := range slices.Sorted(maps.Keys(h)) {
		canonical := http.CanonicalHeaderKey(name)
		out[canonical] = append(out[canonical], h[name]...)
	}
	return out
}

// answer is what the caller receives for the exchange's last answer by [judge]'s
// verdict, the entry to keep, and whether the exchange succeeded: a served 304 is
// the held answer, a refused one reaches the caller as the 304 it is, and any
// other answer is the instance's, read here only where it may be kept.
func (t *CacheTransport) answer(resp *http.Response, rv *revalidation) (*http.Response, *pageEntry, bool) {
	rv.answered = rv.cacheable
	switch rv.verdict {
	case verdictServe:
		header, body, kept := t.cache.serve(rv, resp.Header)
		return served(resp, header, body), kept, true
	case verdictRefuse:
		return resp, nil, false
	}
	if !rv.cacheable {
		return resp, nil, true
	}
	var readErr error
	kept := t.cache.keepable(resp, func() ([]byte, error) {
		// A served answer carries no coding ([served]), so an encoded body would
		// reach the caller undecodable.
		if len(resp.Header.Values("Content-Encoding")) > 0 {
			return nil, errNotKept
		}
		body, complete, err := readKept(resp, t.cache.max/entryShare)
		if readErr = err; err != nil || !complete || !rv.vouched(resp, body) {
			return nil, errNotKept
		}
		return bytes.Clone(body), nil
	})
	rv.answered = readErr == nil
	return resp, kept, readErr == nil
}

// readKept reads up to limit bytes of resp's body, reporting whether that was all
// of it, and puts back a body that replays them followed by the rest, or by the
// read's error, so the caller reads exactly what the instance sent.
func readKept(resp *http.Response, limit int64) (body []byte, complete bool, err error) {
	orig := resp.Body
	body, err = io.ReadAll(io.LimitReader(orig, limit+1))
	var tail io.Reader = orig
	if err != nil {
		tail = &failedReader{err: err}
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(body), tail), orig}
	return body, int64(len(body)) <= limit, err
}

// failedReader answers its error to every read.
type failedReader struct{ err error }

func (f *failedReader) Read([]byte) (int, error) { return 0, f.err }

// served is the 200 a served 304 reaches the caller as: header, the held body, and
// its length and identity coding in place of the 304's framing. The held body is
// as next delivered it, decoded where next decoded it, and [CacheTransport.answer]
// keeps no body next left coded.
func served(notModified *http.Response, header http.Header, body []byte) *http.Response {
	header.Del("Content-Length")
	header.Del("Content-Encoding")
	return &http.Response{
		Status: "200 OK", StatusCode: http.StatusOK,
		Proto: notModified.Proto, ProtoMajor: notModified.ProtoMajor, ProtoMinor: notModified.ProtoMinor,
		Header: header, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)),
		TLS: notModified.TLS,
	}
}

// State is what the cache has done and holds now.
func (t *CacheTransport) State() forgeapi.ReadCacheState { return t.cache.state() }

// CloseIdleConnections closes next's idle connections where next can.
func (t *CacheTransport) CloseIdleConnections() {
	if c, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
