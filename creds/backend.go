package creds

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/credfile"
)

// Backend is the storage under a [Store]: records by key, and a compare-and-swap
// a rotation commits through. Every method is durable before it returns, because
// a rotated pair has no other copy. A failed Swap is retried by a later token call
// with the same prev and next, so a write that landed before its error is found
// and kept. It must be safe for concurrent use.
type Backend interface {
	// Load answers the record under key, and false with no error where none is.
	Load(key string) (Record, bool, error)
	// Keys answers every key that holds a record, in no stated order.
	Keys() ([]string, error)
	// Put stores rec under key in place of any record there.
	Put(key string, rec Record) error
	// Delete removes the record under key. Deleting a key that holds no record
	// is not an error.
	Delete(key string) error
	// Swap stores next under key only when the stored record is prev, compared
	// with [Record.Equal], and reports whether it did. A key holding no record
	// is not prev.
	Swap(key string, prev, next Record) (bool, error)
}

// fileName is the one file a [FileStore] keeps its records in.
const fileName = "credentials.json"

// errNotDurable is a write that reached the file and could not be proved to
// survive a crash. A rotated pair has no other copy, so it is a failed write.
var errNotDurable = errors.New("creds: the credential file was written but not proved durable, so the write is treated as failed")

// fileBackend is the [Backend] over one file in a private directory.
type fileBackend struct {
	dir      *credfile.Dir
	logger   *slog.Logger
	counters forgeapi.Counters
	// mu serializes the file's read-modify-write.
	mu sync.Mutex
}

var _ Backend = (*fileBackend)(nil)

func (b *fileBackend) Load(key string) (Record, bool, error) {
	recs, err := b.read()
	if err != nil {
		return Record{}, false, err
	}
	rec, ok := recs[key]
	return rec, ok, nil
}

func (b *fileBackend) Keys() ([]string, error) {
	recs, err := b.read()
	if err != nil {
		return nil, err
	}
	return slices.Collect(maps.Keys(recs)), nil
}

//nolint:gocritic // hugeParam: the record by value is the Backend method's own parameter
func (b *fileBackend) Put(key string, rec Record) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	recs, err := b.read()
	if err != nil {
		return err
	}
	recs[key] = rec
	return b.write(recs, rec.Family)
}

func (b *fileBackend) Delete(key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	recs, err := b.read()
	if err != nil {
		return err
	}
	rec, ok := recs[key]
	if !ok {
		return nil
	}
	delete(recs, key)
	return b.write(recs, rec.Family)
}

//nolint:gocritic // hugeParam: the records by value are the Backend method's own parameters
func (b *fileBackend) Swap(key string, prev, next Record) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	recs, err := b.read()
	if err != nil {
		return false, err
	}
	stored, ok := recs[key]
	if !ok || !stored.Equal(&prev) {
		return false, nil
	}
	recs[key] = next
	if err := b.write(recs, next.Family); err != nil {
		return false, err
	}
	return true, nil
}

// fileContents is the whole credential file.
type fileContents struct {
	Records map[string]fileRecord `json:"records"`
}

// read answers every stored record, none where the file does not exist yet. A file
// that does not decode is reported without the decoder's text, which can quote it.
func (b *fileBackend) read() (map[string]Record, error) {
	data, found, err := b.dir.Read(context.Background(), fileName)
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
func (b *fileBackend) write(recs map[string]Record, family forgeapi.Family) error {
	contents := fileContents{Records: make(map[string]fileRecord, len(recs))}
	for key := range recs {
		rec := recs[key]
		contents.Records[key] = toFile(&rec)
	}
	data, err := json.Marshal(contents)
	if err != nil {
		return errors.New("creds: the credential file does not encode")
	}
	durable, err := b.dir.Write(context.Background(), fileName, data)
	if err != nil {
		return err
	}
	if !durable {
		if b.counters.NonDurableCredentialWrite != nil {
			b.counters.NonDurableCredentialWrite(family)
		}
		b.logger.Warn("forgeapi credential write not proved durable", "family", family.String())
		return errNotDurable
	}
	return nil
}
