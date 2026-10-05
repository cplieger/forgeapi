package creds

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/credfile"
)

// fileName is the one file a [FileStore] keeps its records in.
const fileName = "credentials.json"

// errNotDurable is a write that reached the file and could not be proved to
// survive a crash. A rotated pair has no other copy, so it is a failed write.
var errNotDurable = errors.New("creds: the credential file was written but not proved durable, so the write is treated as failed")

// Store holds the credential records of a consumer's connections under keys that
// are the consumer's own connection identities, opaque here.
//
// Its one unexported method is a refresh's custody of a key: the single owner a
// rotation runs under and its compare-and-swap over the whole record. So
// [FileStore] is its only implementation.
type Store interface {
	// Load answers the record under key, and false with no error where none is.
	Load(key string) (Record, bool, error)
	// Save stores rec under key in place of any record there: connect and
	// reconnect.
	Save(key string, rec Record) error
	// Delete removes the record under key, which is a disconnect. Deleting a
	// key that holds no record is not an error.
	Delete(key string) error
	// Keys answers every key that holds a record, in no stated order: what the
	// git credential helper matches against.
	Keys() ([]string, error)

	rotation(key string) *rotation
}

// FileStore is the [Store] over one file, credentials.json at mode 0600, in a
// directory at mode 0700 this process owns. Every read re-reads the file, so two
// stores on one directory, a server's and a git credential helper's, each read what
// the other last wrote. It is safe for concurrent use.
type FileStore struct {
	dir      *credfile.Dir
	logger   *slog.Logger
	custody  map[string]*rotation
	counters forgeapi.Counters
	// mu serializes the file's read-modify-write, and custodyMu the custody map,
	// so a refresh in flight holds neither while it waits on the network.
	mu        sync.Mutex
	custodyMu sync.Mutex
}

var _ Store = (*FileStore)(nil)

// OpenFileStore opens the store in dir, an absolute path, creating that one
// directory level at 0700 where it is absent. A directory that exists with group or
// other access, or that another user owns, is refused rather than repaired, and so
// is every later read or write once it is found that way. It reads the logger and
// the counters from opts.
//
// A store directory has exactly one refreshing process: the one whose
// [Source.Token] calls keep its records fresh. Another process may open the same
// directory to read it, as the git credential helper does, since a reader never
// refreshes. Two refreshing processes are outside what the store supports: a
// terminal answer one meets for a refresh token the other just spent can mark the
// record before the other stores the pair it rotated, and since a rotation's
// compare-and-swap is over the whole record, the mark included, that pair is never
// stored and the connection needs a reconnect. Nothing detects a second refreshing
// process.
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
	return &FileStore{dir: d, logger: logger, counters: set.Counters, custody: map[string]*rotation{}}, nil
}

// Load answers the record under key, and false with no error where none is.
func (s *FileStore) Load(key string) (Record, bool, error) {
	recs, err := s.read()
	if err != nil {
		return Record{}, false, err
	}
	rec, ok := recs[key]
	return rec, ok, nil
}

// Save stores rec under key in place of any record there.
//
//nolint:gocritic // hugeParam: the record by value is the published signature's own parameter
func (s *FileStore) Save(key string, rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.read()
	if err != nil {
		return err
	}
	recs[key] = rec
	return s.write(recs, rec.Family)
}

// Delete removes the record under key.
func (s *FileStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.read()
	if err != nil {
		return err
	}
	rec, ok := recs[key]
	if !ok {
		return nil
	}
	delete(recs, key)
	return s.write(recs, rec.Family)
}

// Keys answers every key that holds a record.
func (s *FileStore) Keys() ([]string, error) {
	recs, err := s.read()
	if err != nil {
		return nil, err
	}
	return slices.Collect(maps.Keys(recs)), nil
}

// swap replaces the record under key with next only where the stored record is
// still prev, and reports whether it did. A record a consumer saved while the
// refresh was in flight is newer than the rotation, so it stays.
func (s *FileStore) swap(key string, prev, next *Record) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.read()
	if err != nil {
		return false, err
	}
	stored, ok := recs[key]
	if !ok || !same(&stored, prev) {
		return false, nil
	}
	recs[key] = *next
	if err := s.write(recs, next.Family); err != nil {
		return false, err
	}
	return true, nil
}

func (s *FileStore) rotation(key string) *rotation {
	s.custodyMu.Lock()
	defer s.custodyMu.Unlock()
	r, ok := s.custody[key]
	if !ok {
		r = &rotation{owner: make(chan struct{}, 1), store: s, key: key}
		s.custody[key] = r
	}
	return r
}

// fileContents is the whole credential file.
type fileContents struct {
	Records map[string]fileRecord `json:"records"`
}

// read answers every stored record, none where the file does not exist yet. A file
// that does not decode is reported without the decoder's text, which can quote it.
func (s *FileStore) read() (map[string]Record, error) {
	data, found, err := s.dir.Read(context.Background(), fileName)
	if err != nil || !found {
		return map[string]Record{}, err
	}
	var contents fileContents
	if err := json.Unmarshal(data, &contents); err != nil {
		return nil, errors.New("creds: the credential file does not decode")
	}
	recs := make(map[string]Record, len(contents.Records))
	for key := range contents.Records {
		f := contents.Records[key]
		recs[key] = fromFile(&f)
	}
	return recs, nil
}

// write replaces the file with recs. family is the family of the record the write
// is for, which the non-durable counter names.
func (s *FileStore) write(recs map[string]Record, family forgeapi.Family) error {
	contents := fileContents{Records: make(map[string]fileRecord, len(recs))}
	for key := range recs {
		rec := recs[key]
		contents.Records[key] = toFile(&rec)
	}
	data, err := json.Marshal(contents)
	if err != nil {
		return errors.New("creds: the credential file does not encode")
	}
	durable, err := s.dir.Write(context.Background(), fileName, data)
	if err != nil {
		return err
	}
	if !durable {
		if s.counters.NonDurableCredentialWrite != nil {
			s.counters.NonDurableCredentialWrite(family)
		}
		s.logger.Warn("forgeapi credential write not proved durable", "family", family.String())
		return errNotDurable
	}
	return nil
}

// rotation is one key's refresh custody in one store: the owner a refresh runs
// under, whether one is in flight, the terminal outcome a refresh reached for one
// record, and a rotated pair the store could not take. That outcome is this
// process's copy of the mark it writes to the record: it reads as that mark until a
// different record is stored, and where the store could not take the mark, the mark
// is owed until a later write for the key succeeds.
type rotation struct {
	owner chan struct{}
	store *FileStore
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
	busy bool
	owed bool
}

// take makes the caller the key's refresh owner, waiting no longer than ctx allows.
func (r *rotation) take(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case r.owner <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *rotation) give() { <-r.owner }

func (r *rotation) swap(prev, next *Record) (bool, error) { return r.store.swap(r.key, prev, next) }

func (r *rotation) setBusy(busy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.busy = busy
}

func (r *rotation) refreshing() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.busy
}

// end records the terminal outcome reached for rec and the mark it writes, owed
// to the store where owed is set, and reports whether this call reached it first,
// which is the one that counts the outcome.
func (r *rotation) end(rec *Record, mark Usability, owed bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended != nil && same(r.ended, rec) {
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
	return r.base != nil && same(r.base, rec)
}

// drop forgets the held pair, once it is stored or belongs to a record the key no
// longer holds.
func (r *rotation) drop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.base, r.pair = nil, nil
}

// markFor answers the mark of the outcome reached for rec, and UsabilityUnmarked
// where none was reached for that record.
func (r *rotation) markFor(rec *Record) Usability {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended == nil || !same(r.ended, rec) {
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
	if r.ended != nil && same(r.ended, &prev) {
		r.owed = false
	}
}
