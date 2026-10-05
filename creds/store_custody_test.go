package creds_test

import (
	"os"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// The directory is checked through the store's own handle at every access, so one
// widened after the store opened holds no credential readable in the open.
func TestFileStore_fails_every_access_once_its_directory_is_widened(t *testing.T) {
	store, dir := openStore(t)
	rec := rotating(forgeapi.FamilyGitHub, "https://forge.example", 8*time.Hour, 8*time.Hour)
	save(t, store, "conn", rec)
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatalf("Setup: Chmod = error %v", err)
	}

	if _, _, err := store.Load("conn"); err == nil {
		t.Error("Load over a directory widened to 0750 = nil error, want the load failed")
	}
	if err := store.Save("conn", rec); err == nil {
		t.Error("Save over a directory widened to 0750 = nil error, want the write failed")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(%q) = error %v", dir, err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Errorf("the directory is at %v after the refusal, want 0750 left as the operator set it", info.Mode().Perm())
	}
}
