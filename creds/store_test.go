package creds_test

import (
	"bytes"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// credentialFile is the one file the store keeps its records in.
const credentialFile = "credentials.json"

func TestOpenFileStore_creates_its_directory_private(t *testing.T) {
	_, dir := openStore(t)

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(%q) = error %v, want the store's directory", dir, err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Errorf("the store's directory is %v, want a directory at 0700", info.Mode())
	}
}

func TestFileStore_writes_its_one_file_private(t *testing.T) {
	store, dir := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, "https://forge.example", 8*time.Hour, 8*time.Hour))

	info, err := os.Stat(filepath.Join(dir, credentialFile))
	if err != nil {
		t.Fatalf("Stat(%s) = error %v, want the credential file", credentialFile, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("%s is at %v, want 0600", credentialFile, info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q) = error %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{credentialFile}) {
		t.Errorf("the store's directory holds %v, want only %s", names, credentialFile)
	}
}

// The umask is process-wide, so this test must never call t.Parallel.
func TestFileStore_writes_its_file_private_under_an_open_umask(t *testing.T) {
	previous := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(previous) })
	store, dir := openStore(t)
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, "https://forge.example", 8*time.Hour, 8*time.Hour))

	info, err := os.Stat(filepath.Join(dir, credentialFile))
	if err != nil {
		t.Fatalf("Stat(%s) = error %v, want the credential file", credentialFile, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("under umask 000, %s is at %v, want 0600", credentialFile, got)
	}
}

// A credential directory somebody widened is exactly the case a failed load exists
// for, so it is refused rather than repaired.
func TestFileStore_refuses_a_widened_directory_without_repairing_it(t *testing.T) {
	for _, mode := range []fs.FileMode{0o750, 0o705, 0o770, 0o777} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "credentials")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatalf("Setup: Mkdir = error %v", err)
			}
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatalf("Setup: Chmod = error %v", err)
			}

			store, err := creds.OpenFileStore(dir)
			if err == nil {
				_, _, err = store.Load("conn")
			}
			if err == nil {
				t.Errorf("OpenFileStore and Load over a directory at %v both succeeded, want the load failed", mode)
			}
			info, statErr := os.Stat(dir)
			if statErr != nil {
				t.Fatalf("Stat(%q) = error %v", dir, statErr)
			}
			if info.Mode().Perm() != mode {
				t.Errorf("the directory is at %v after the refusal, want %v left as the operator set it", info.Mode().Perm(), mode)
			}
		})
	}
}

func TestFileStore_answers_a_saved_record_as_it_was_saved(t *testing.T) {
	store, _ := openStore(t)
	want := rotating(forgeapi.FamilyGitLab, "https://forge.example", 2*time.Hour, time.Hour)
	save(t, store, "conn", want)

	if field := sameRecord(load(t, store, "conn"), want); field != "" {
		t.Errorf("Load(conn) differs from the saved record at %s", field)
	}
}

func TestFileStore_answers_absence_for_a_key_it_holds_no_record_for(t *testing.T) {
	store, _ := openStore(t)

	rec, ok, err := store.Load("never-connected")
	if err != nil || ok {
		t.Errorf("Load(never-connected) = (%+v, %v, %v), want (zero, false, nil)", rec, ok, err)
	}
}

func TestFileStore_lists_the_keys_it_holds(t *testing.T) {
	store, _ := openStore(t)
	save(t, store, "one", rotating(forgeapi.FamilyGitHub, "https://forge.example", 8*time.Hour, 8*time.Hour))
	save(t, store, "two", rotating(forgeapi.FamilyGitLab, "https://other.example", 2*time.Hour, 2*time.Hour))

	keys, err := store.Keys()
	if err != nil {
		t.Fatalf("Keys() = error %v", err)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"one", "two"}) {
		t.Errorf("Keys() = %v, want [one two]", keys)
	}
}

func TestFileStore_forgets_a_deleted_record(t *testing.T) {
	store, _ := openStore(t)
	save(t, store, "one", rotating(forgeapi.FamilyGitHub, "https://forge.example", 8*time.Hour, 8*time.Hour))
	save(t, store, "two", rotating(forgeapi.FamilyGitLab, "https://other.example", 2*time.Hour, 2*time.Hour))

	if err := store.Delete("one"); err != nil {
		t.Fatalf("Delete(one) = error %v", err)
	}
	if _, ok, err := store.Load("one"); err != nil || ok {
		t.Errorf("Load(one) after Delete = (_, %v, %v), want (_, false, nil)", ok, err)
	}
	keys, err := store.Keys()
	if err != nil || !slices.Equal(keys, []string{"two"}) {
		t.Errorf("Keys() after Delete = (%v, %v), want ([two], nil)", keys, err)
	}
}

func TestFileStore_keeps_its_records_for_the_next_store_opened_on_the_directory(t *testing.T) {
	store, dir := openStore(t)
	want := rotating(forgeapi.FamilyGitHub, "https://forge.example", 8*time.Hour, 8*time.Hour)
	save(t, store, "conn", want)

	reopened, err := creds.OpenFileStore(dir)
	if err != nil {
		t.Fatalf("OpenFileStore(%q) again = error %v", dir, err)
	}
	if field := sameRecord(load(t, reopened, "conn"), want); field != "" {
		t.Errorf("the reopened store's record differs at %s", field)
	}
}

// The store hands its logger to the file custody beneath it, so a directory mode
// repaired at creation is reported there. The umask is process-wide, so this test
// must never call t.Parallel.
func TestOpenFileStore_reports_a_repaired_directory_to_its_logger(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	previous := syscall.Umask(0o277)
	t.Cleanup(func() { syscall.Umask(previous) })
	var logged bytes.Buffer

	if _, err := creds.OpenFileStore(dir, forgeapi.WithLogger(slog.New(slog.NewTextHandler(&logged, nil)))); err != nil {
		t.Fatalf("OpenFileStore(%q) under umask 277 = error %v, want the store", dir, err)
	}
	if line := logged.String(); !strings.Contains(line, "did not keep the requested mode; repaired") {
		t.Errorf("the store's logger recorded %q, want the directory's repaired mode", line)
	}
}

// A write the file's bound refuses is the write's own failure: the save fails, the
// stored record stays, and nothing reports the write as merely not proved durable.
func TestFileStore_fails_a_save_its_file_bound_refuses_as_that_failure(t *testing.T) {
	nonDurable := 0
	dir := filepath.Join(t.TempDir(), "credentials")
	store, err := creds.OpenFileStore(dir, forgeapi.WithCounters(forgeapi.Counters{
		NonDurableCredentialWrite: func(forgeapi.Family) { nonDurable++ },
	}))
	if err != nil {
		t.Fatalf("Setup: OpenFileStore(%q) = error %v", dir, err)
	}
	rec := rotating(forgeapi.FamilyGitHub, "https://forge.example", 8*time.Hour, 8*time.Hour)
	save(t, store, "conn", rec)
	oversized := rec
	oversized.Token = strings.Repeat("x", 1<<20)

	if err := store.Save("conn", oversized); err == nil {
		t.Error("Save(a record over the file's 1 MiB bound) = nil error, want the write refused")
	}
	if nonDurable != 0 {
		t.Errorf("NonDurableCredentialWrite fired %d time(s), want none: the write failed, it was not merely unproved", nonDurable)
	}
	if got := load(t, store, "conn"); got.Token != rec.Token {
		t.Errorf("Load after the refused save = token of %d bytes, want the stored record's", len(got.Token))
	}
}
