package transport

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cplieger/forgeapi"
)

const (
	// entryOverhead is the fixed cost charged to every entry beside its bytes:
	// the list element, the map slot and the entry's own fields.
	entryOverhead = 512

	// maxValidatorBytes bounds a validator this library replays, so a hostile
	// instance cannot make every request carry an unbounded header.
	maxValidatorBytes = 1 << 10

	// entryShare is the fraction of the bound one entry may take: a larger one is
	// not kept, so one body cannot flush every other entry.
	entryShare = 4
)

// readCache is one connection's read cache, a byte-bounded LRU in memory, and its
// counters. An entry is immutable once kept, so a reader holds it past the lock.
//
// gen is the generation [readCache.clear] ends: an exchange begun in an earlier one
// keeps no entry, so a read in flight across [Conn.Close] leaves nothing held.
type readCache struct {
	index  map[string]*list.Element
	order  *list.List
	max    int64
	bytes  int64
	gen    uint64
	hits   atomic.Int64
	misses atomic.Int64
	saved  atomic.Int64
	mu     sync.Mutex
}

// cacheEntry is one kept entry with what it is charged.
type cacheEntry struct {
	page *pageEntry
	key  string
	cost int64
}

// heldFields are the only header fields an entry keeps: those a family reads from a
// served answer. Every other field of a served answer, the rate headers included, is
// the live 304's own. A field the 304 carries replaces the held value. One it omits
// keeps the held value, unless required is set: then a held value the 304 omits
// makes the entry unservable, and the read is sent once more without a validator.
// GitHub's 304 omits Link (measured); [Response.Revalidated] tells the family so.
var heldFields = [...]struct {
	name     string
	required bool
}{
	{name: "Link"},
	{name: "X-Next-Page", required: true},
	{name: "X-Total-Count", required: true},
	{name: "X-Gitlab-Meta", required: true},
	{name: "X-Gitea-Version", required: true},
	{name: "X-Forgejo-Version", required: true},
}

// pageEntry is one revalidatable answer: the family's decoded projection of a 200
// body, its [heldFields] in that order, and the validator the instance sent. Every
// string is its own copy, so [pageCost] charges all the entry holds.
type pageEntry struct {
	ETag         string
	LastModified string
	Fields       [len(heldFields)]string
	Body         []byte
}

// holdFields copies an answer's [heldFields] into storage of their own, each
// field's lines combined into one value (RFC 9110 section 5.3).
func holdFields(h http.Header) [len(heldFields)]string {
	var out [len(heldFields)]string
	for i, f := range heldFields {
		out[i] = strings.Clone(strings.Join(h.Values(f.name), ", "))
	}
	return out
}

// refreshed is the entry a not-modified answer leaves: each held field it carries
// replaced (RFC 9111 section 4.3.4), the body and validators unchanged.
func (p *pageEntry) refreshed(h http.Header) *pageEntry {
	fresh := holdFields(h)
	out := *p
	for i, f := range heldFields {
		if len(h.Values(f.name)) > 0 {
			out.Fields[i] = fresh[i]
		}
	}
	return &out
}

// served is the header a served answer carries: the live 304's, and each held
// field it omits.
func (p *pageEntry) served(live http.Header) http.Header {
	out := live.Clone()
	for i, f := range heldFields {
		if len(out.Values(f.name)) == 0 && p.Fields[i] != "" {
			out.Set(f.name, p.Fields[i])
		}
	}
	return out
}

// answers reports whether a not-modified answer may serve this entry. Its validators
// must name the held representation: its ETag weakly equal to the held one (RFC
// 9110 section 8.8.3.2), or none well-formed where none is held, and its
// Last-Modified the held date (RFC 9111 section 4.3.4). It must also carry every
// required [heldFields] value the entry holds.
func (p *pageEntry) answers(h http.Header) bool {
	for i, f := range heldFields {
		if f.required && p.Fields[i] != "" && len(h.Values(f.name)) == 0 {
			return false
		}
	}
	tag := entityTag(h)
	switch {
	case p.ETag == "" && tag != "":
		return false
	case p.ETag != "" && len(h.Values("ETag")) > 0 && strings.TrimPrefix(tag, "W/") != strings.TrimPrefix(p.ETag, "W/"):
		return false
	}
	modified := h.Get("Last-Modified")
	if modified == "" || p.LastModified == "" {
		return true
	}
	fresh, err := http.ParseTime(modified)
	held, heldErr := http.ParseTime(p.LastModified)
	return err == nil && heldErr == nil && fresh.Equal(held)
}

// newReadCache builds a connection's cache, nil where its bound switches it off.
func newReadCache(set *forgeapi.Settings) *readCache {
	if set.ReadCacheBytes <= 0 {
		return nil
	}
	return &readCache{index: map[string]*list.Element{}, order: list.New(), max: set.ReadCacheBytes}
}

// key is one request's key. The credential is folded as its digest, never as
// itself, and every header the request carries that varies the answer is folded
// in: the credential, the validators and the constant User-Agent are not.
// [wireGuard] holds the request that reaches the wire to the one keyed here.
func (*readCache) key(token, shape, target string, header http.Header) string {
	fields := make([]string, 0, 4+2*len(header))
	fields = append(fields, digest([]byte(token)), http.MethodGet, target, shape)
	names := make([]string, 0, len(header))
	for name := range header {
		switch http.CanonicalHeaderKey(name) {
		case "Authorization", "If-None-Match", "If-Modified-Since", "User-Agent":
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		fields = append(fields, http.CanonicalHeaderKey(name), strings.Join(header.Values(name), "\n"))
	}
	return digest([]byte(lengthPrefixed(fields...)))
}

func lengthPrefixed(fields ...string) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	return b.String()
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// generation is the generation an exchange beginning now belongs to.
func (rc *readCache) generation() uint64 {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.gen
}

// current reports whether gen is the generation the cache holds entries for.
func (rc *readCache) current(gen uint64) bool {
	return rc.generation() == gen
}

// lookup is the entry kept under key, nil where none is. Recency is set where the
// exchange ends: [readCache.finish] puts a kept or served entry at the front.
func (rc *readCache) lookup(key string) *cacheEntry {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	el, ok := rc.index[key]
	if !ok {
		return nil
	}
	e, _ := el.Value.(*cacheEntry)
	return e
}

// finish is the one change a cacheable exchange makes to the cache: it keeps the
// entry the exchange vouched for, and forgets the key on every other outcome. An
// exchange of an ended generation changes nothing.
func (rc *readCache) finish(key string, gen uint64, kept *pageEntry) {
	var e *cacheEntry
	if kept != nil {
		e = &cacheEntry{key: key, page: kept, cost: pageCost(key, kept)}
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if gen != rc.gen {
		return
	}
	if e == nil {
		rc.removeLocked(key)
		return
	}
	rc.putLocked(e)
}

// tally counts one cacheable read the instance answered in full: a hit where the
// held answer served the call, a miss on every other outcome.
func (rc *readCache) tally(hit bool) {
	if hit {
		rc.hits.Add(1)
		return
	}
	rc.misses.Add(1)
}

// putLocked holds e, evicting from the back until the bound holds. An entry over
// its share of the bound is not held, and the key's older entry is dropped with it
// rather than left to answer.
func (rc *readCache) putLocked(e *cacheEntry) {
	rc.removeLocked(e.key)
	if e.cost > rc.max/entryShare {
		return
	}
	rc.index[e.key] = rc.order.PushFront(e)
	rc.bytes += e.cost
	for rc.bytes > rc.max {
		back := rc.order.Back()
		victim, _ := back.Value.(*cacheEntry)
		rc.removeLocked(victim.key)
	}
}

func (rc *readCache) removeLocked(key string) {
	el, ok := rc.index[key]
	if !ok {
		return
	}
	e, _ := el.Value.(*cacheEntry)
	rc.order.Remove(el)
	delete(rc.index, key)
	rc.bytes -= e.cost
}

// clear empties the cache and ends its generation.
func (rc *readCache) clear() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.gen++
	rc.index = map[string]*list.Element{}
	rc.order.Init()
	rc.bytes = 0
}

// state is the counters a [forgeapi.BudgetState] reports.
func (rc *readCache) state() forgeapi.ReadCacheState {
	if rc == nil {
		return forgeapi.ReadCacheState{}
	}
	rc.mu.Lock()
	held := rc.bytes
	rc.mu.Unlock()
	return forgeapi.ReadCacheState{
		Hits:   int(rc.hits.Load()),
		Misses: int(rc.misses.Load()),
		Saved:  int(rc.saved.Load()),
		Bytes:  held,
	}
}

// pageCost is what a page entry is charged: its key, its body's backing array, its
// validators and held fields, and the fixed overhead.
func pageCost(key string, p *pageEntry) int64 {
	n := len(key) + cap(p.Body) + len(p.ETag) + len(p.LastModified) + entryOverhead
	for _, v := range p.Fields {
		n += len(v)
	}
	return int64(n)
}

// ownedBody encodes v and copies it out of the encoder's backing array.
func ownedBody(v any) ([]byte, error) {
	body, err := marshal(v)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(body), nil
}

// entityTag is a response's ETag as If-None-Match replays it, empty unless it is
// exactly one entity-tag (RFC 9110 section 8.8.3): `*` or a list replayed would be
// a condition matching other representations.
func entityTag(h http.Header) string {
	tags := h.Values("ETag")
	if len(tags) != 1 || len(tags[0]) > maxValidatorBytes {
		return ""
	}
	opaque := strings.TrimPrefix(tags[0], "W/")
	if len(opaque) < 2 || opaque[0] != '"' || opaque[len(opaque)-1] != '"' {
		return ""
	}
	for i := 1; i < len(opaque)-1; i++ {
		if b := opaque[i]; b < 0x21 || b == '"' || b == 0x7f {
			return ""
		}
	}
	return tags[0]
}

// httpDate is a response's Last-Modified as If-Modified-Since replays it, empty
// unless it is one HTTP date (RFC 9110 section 8.8.2).
func httpDate(h http.Header) string {
	dates := h.Values("Last-Modified")
	if len(dates) != 1 || len(dates[0]) > maxValidatorBytes || !validFieldValue(dates[0]) {
		return ""
	}
	if _, err := http.ParseTime(dates[0]); err != nil {
		return ""
	}
	return dates[0]
}

// noStore reports whether a request or a response carries the no-store directive,
// which forbids keeping any part of the exchange (RFC 9111 sections 5.2.1.5 and
// 5.2.2.5).
func noStore(h http.Header) bool {
	for _, value := range h.Values("Cache-Control") {
		for directive := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(directive), "no-store") {
				return true
			}
		}
	}
	return false
}
