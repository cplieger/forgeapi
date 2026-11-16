package creds

import (
	"log/slog"
	"sync"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/credfile"
)

// Store holds the credential records of a consumer's connections under keys that
// are the consumer's own connection identities, opaque here.
//
// Its unexported methods are a refresh's custody of a key: the single owner a
// rotation runs under and its compare-and-swap over the whole record. So a store
// comes from [OpenFileStore] or [NewStore], and from nowhere else.
type Store interface {
	// Load answers the record under key, and false with no error where none is.
	Load(key string) (Record, bool, error)
	// Save stores rec under key in place of any record there: connect and
	// reconnect.
	Save(key string, rec Record) error
	// Delete removes the record under key, which is a disconnect. Deleting a
	// key that holds no record is not an error.
	Delete(key string) error
	// Keys answers every key that holds a record, in no stated order.
	Keys() ([]string, error)

	acquire(key string) *rotation
	peek(key string) *rotation
	release(r *rotation, gone bool)
}

// NewStore returns the [Store] over b, with the library's custody around it: one
// rotation per key, the rotated pair held where b could not take it, and the mark
// owed where b could not take that. The custody lives in the returned store, not
// in b: call NewStore once per backend and share the store between every
// [StoredSource] over it, since two stores over one backend can lose a rotated pair.
//
// Exactly one process may refresh from b, the one calling [StoredSource.Token];
// readers such as the git credential helper may share it. Nothing detects a second
// refresher, which can lose a rotated pair.
func NewStore(b Backend) Store {
	return newCustodian(b)
}

// custodian is the [Store] over a [Backend]: the backend holds the records and
// the custodian each key's refresh custody.
type custodian struct {
	backend Backend
	custody map[string]*rotation
	// mu guards custody alone, so a refresh in flight never holds it while it
	// waits on the network or the backend.
	mu sync.Mutex
}

var _ Store = (*custodian)(nil)

func newCustodian(b Backend) *custodian {
	return &custodian{backend: b, custody: map[string]*rotation{}}
}

func (c *custodian) Load(key string) (Record, bool, error) { return c.backend.Load(key) }

//nolint:gocritic // hugeParam: the record by value is the Store method's own parameter
func (c *custodian) Save(key string, rec Record) error { return c.backend.Put(key, rec) }

func (c *custodian) Keys() ([]string, error) { return c.backend.Keys() }

// Delete removes the record under key and marks the key's refresh custody gone,
// freed after its last holder unless a rotated pair or an owed mark is pending.
func (c *custodian) Delete(key string) error {
	if err := c.backend.Delete(key); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, held := c.custody[key]; held {
		r.gone = true
		c.free(r)
	}
	return nil
}

// swap replaces the record under key with next only where the stored record is
// still prev, and reports whether it did. A record a consumer saved while the
// refresh was in flight is newer than the rotation, so it stays.
func (c *custodian) swap(key string, prev, next *Record) (bool, error) {
	return c.backend.Swap(key, *prev, *next)
}

// acquire answers key's refresh custody, creating it, and counts the caller as a
// holder until its release. Only a token call acquires, so a state read cannot
// recreate the custody of a deleted key.
func (c *custodian) acquire(key string) *rotation {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.custody[key]
	if !ok {
		r = &rotation{owner: newOwner(), store: c, key: key}
		c.custody[key] = r
	}
	r.holders++
	return r
}

// peek answers key's refresh custody where one exists, and otherwise a fresh one
// that holds nothing and is never stored.
func (c *custodian) peek(key string) *rotation {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.custody[key]; ok {
		return r
	}
	return &rotation{owner: newOwner(), store: c, key: key}
}

// release ends one holder's use of r; gone records that the holder found no
// record under the key.
func (c *custodian) release(r *rotation, gone bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r.holders--
	if gone {
		r.gone = true
	}
	c.free(r)
}

// free deletes a gone custody nobody holds, unless it holds the only copy of a
// rotated pair or a mark the store still owes. It is deleted only at zero holders,
// else a paused token call and a later one would own one key twice. Called under
// mu.
func (c *custodian) free(r *rotation) {
	if r.holders > 0 || !r.gone || c.custody[r.key] != r {
		return
	}
	if _, _, pending := r.held(); pending || r.owes() {
		return
	}
	delete(c.custody, r.key)
}

// FileStore is the [Store] over one file, credentials.json at mode 0600, in a
// directory at mode 0700 this process owns. Every read re-reads the file, so two
// stores on one directory, a server's and a git credential helper's, each read what
// the other last wrote. It is safe for concurrent use.
type FileStore struct {
	store *custodian
}

var _ Store = (*FileStore)(nil)

// OpenFileStore opens the store in dir, an absolute path, creating that one
// directory level at 0700 where it is absent. A directory with group or other
// access, or owned by another user, is refused, then and at every later read or
// write. It reads the logger and the counters from opts.
//
// Exactly one process may refresh from a directory, the one calling
// [StoredSource.Token]; readers such as the git credential helper may share it.
// Nothing detects a second refresher, which can lose a rotated pair.
func OpenFileStore(dir string, opts ...forgeapi.Option) (*FileStore, error) {
	set := forgeapi.Resolve(opts...)
	logger := set.Logger
	if logger == nil {
		logger = slog.Default()
	}
	d, err := credfile.Open(dir, logger)
	if err != nil {
		return nil, err
	}
	return &FileStore{store: newCustodian(&fileBackend{dir: d, logger: logger, counters: set.Counters})}, nil
}

// Load answers the record under key, and false with no error where none is.
func (s *FileStore) Load(key string) (Record, bool, error) { return s.store.Load(key) }

// Save stores rec under key in place of any record there.
//
//nolint:gocritic // hugeParam: the record by value is the published signature's own parameter
func (s *FileStore) Save(key string, rec Record) error { return s.store.Save(key, rec) }

// Delete removes the record under key, which is a disconnect. Deleting a key that
// holds no record is not an error.
func (s *FileStore) Delete(key string) error { return s.store.Delete(key) }

// Keys answers every key that holds a record.
func (s *FileStore) Keys() ([]string, error) { return s.store.Keys() }

func (s *FileStore) acquire(key string) *rotation   { return s.store.acquire(key) }
func (s *FileStore) peek(key string) *rotation      { return s.store.peek(key) }
func (s *FileStore) release(r *rotation, gone bool) { s.store.release(r, gone) }

// rotation is one key's refresh custody in one store: the owner a refresh runs
// under, whether one is in flight, the terminal outcome a refresh reached for one
// record, and a rotated pair the store could not take. That outcome is this
// process's copy of the mark it writes to the record: it reads as that mark until a
// different record is stored, and where the store could not take the mark, the mark
// is owed until a later write for the key succeeds.
type rotation struct {
	owner *owner
	store *custodian
	// ended is the record the outcome was reached for, and mark what the outcome
	// writes to it: UsabilityUnmarked for a refresh token past its own expiry,
	// which every reader derives from the record.
	ended *Record
	// base is the record a rotation started from whose write failed, and pair the
	// pair the instance issued in its place, live upstream and in this process
	// alone until the next call for the key stores it. Both are set and cleared
	// under the owner.
	base *Record
	pair *Record
	key  string
	mark Usability
	mu   sync.Mutex
	// holders and gone are the custodian's, under its mu.
	holders int
	owed    bool
	gone    bool
}

func (r *rotation) swap(prev, next *Record) (bool, error) { return r.store.swap(r.key, prev, next) }

// end records the terminal outcome reached for rec and the mark it writes, owed
// to the store where owed is set, and reports whether this call reached it first,
// which is the one that counts the outcome.
func (r *rotation) end(rec *Record, mark Usability, owed bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended != nil && r.ended.Equal(rec) {
		return false
	}
	ended := *rec
	r.ended, r.mark, r.owed = &ended, mark, owed
	return true
}

// hold keeps the rotated pair a write could not store, beside the record its
// rotation started from.
func (r *rotation) hold(base, pair *Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, p := *base, *pair
	r.base, r.pair = &b, &p
}

// held answers the pair the custody holds and the record its rotation started
// from, and false where none is held.
func (r *rotation) held() (base, pair Record, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pair == nil {
		return Record{}, Record{}, false
	}
	return *r.base, *r.pair, true
}

// holds reports whether the custody holds a pair rotated from rec, compared whole,
// the mark included.
func (r *rotation) holds(rec *Record) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.base != nil && r.base.Equal(rec)
}

// drop forgets the held pair, once it is stored or belongs to a record the key no
// longer holds.
func (r *rotation) drop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.base, r.pair = nil, nil
}

// owes reports whether a mark is owed to the store.
func (r *rotation) owes() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.owed
}

// markFor answers the mark of the outcome reached for rec, and UsabilityUnmarked
// where none was reached for that record.
func (r *rotation) markFor(rec *Record) Usability {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended == nil || !r.ended.Equal(rec) {
		return UsabilityUnmarked
	}
	return r.mark
}

// settle writes the mark the store owes, by compare-and-swap over the record it is
// owed for, so a record saved since is never marked. A swap that finds another
// record settles the debt too, since the mark was about a pair the key no longer
// holds; a write that fails leaves it owed.
func (r *rotation) settle() {
	r.mu.Lock()
	if !r.owed {
		r.mu.Unlock()
		return
	}
	prev, next := *r.ended, *r.ended
	next.Usability = r.mark
	r.mu.Unlock()

	if _, err := r.swap(&prev, &next); err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended != nil && r.ended.Equal(&prev) {
		r.owed = false
	}
}
