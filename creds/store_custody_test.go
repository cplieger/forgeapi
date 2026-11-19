package creds_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/creds"
	"github.com/cplieger/go/filesafe"
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

	_, _, err := store.Load("conn")
	if !errors.Is(err, creds.ErrDirNotPrivate) {
		t.Errorf("Load over a directory widened to 0750 = error %v, want one matching ErrDirNotPrivate", err)
	}
	if err != nil && !strings.Contains(err.Error(), "mode 0750 grants") {
		t.Errorf("Load over a directory widened to 0750 = error %q, want its text to name the mode as %q", err, "mode 0750 grants")
	}
	if err := store.Save("conn", rec); !errors.Is(err, creds.ErrDirNotPrivate) {
		t.Errorf("Save over a directory widened to 0750 = error %v, want one matching ErrDirNotPrivate", err)
	}
	info, statErr := os.Stat(dir)
	if statErr != nil {
		t.Fatalf("Stat(%q) = error %v", dir, statErr)
	}
	if info.Mode().Perm() != 0o750 {
		t.Errorf("the directory is at %v after the refusal, want 0750 left as the operator set it", info.Mode().Perm())
	}
}

// Only root can hand a fresh directory to another user, so an unprivileged run
// takes /, which root owns.
func TestOpenFileStore_refuses_a_directory_another_user_owns_as_not_private(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("Setup: Mkdir = error %v", err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, 65534, 65534); err != nil {
			t.Fatalf("Setup: Chown = error %v", err)
		}
	} else {
		dir = "/"
	}

	_, err := creds.OpenFileStore(dir)
	if !errors.Is(err, creds.ErrDirNotPrivate) {
		t.Errorf("OpenFileStore over %s, owned by another user = error %v, want one matching ErrDirNotPrivate", dir, err)
	}
	assertNoFilesafeRefusal(t, "OpenFileStore over "+dir+", owned by another user", err)
}

// unsyncedChild is set in the child process that runs the unsynced open.
const unsyncedChild = "FORGEAPI_TEST_UNSYNCED_CHILD"

// The descriptor limit is process-wide, and a connection another test left closing
// frees a descriptor at any moment, so the open runs alone in a child process.
func TestOpenFileStore_answers_a_retryable_error_while_its_parent_cannot_be_synced(t *testing.T) {
	if os.Getenv(unsyncedChild) != "" {
		openUnsynced(t)
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.count=1",
		"-test.run=^TestOpenFileStore_answers_a_retryable_error_while_its_parent_cannot_be_synced$")
	cmd.Env = append(os.Environ(), unsyncedChild+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("the child process running the unsynced open = error %v, output:\n%s", err, out)
	}
}

// openUnsynced holds every descriptor but one, which the directory's own open
// takes, so the open of its parent for the sync fails.
func openUnsynced(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	release := holdAllDescriptorsButOne(t)
	_, err := creds.OpenFileStore(dir)
	release()

	if !errors.Is(err, creds.ErrDirNotSynced) {
		t.Fatalf("OpenFileStore with one descriptor free = error %v, want one matching ErrDirNotSynced", err)
	}
	if errors.Is(err, filesafe.ErrParentNotSynced) {
		t.Errorf("OpenFileStore with one descriptor free = error %v, which matches filesafe.ErrParentNotSynced; the store's errors carry none of the dependency's values", err)
	}
	if !errors.Is(err, syscall.EMFILE) {
		t.Errorf("OpenFileStore with one descriptor free = error %v, want the platform's EMFILE beneath it", err)
	}
	if info, statErr := os.Stat(dir); statErr != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("after the unsynced open, Stat(%q) = (%v, %v), want the directory left in place at 0700", dir, info, statErr)
	}
	store, err := creds.OpenFileStore(dir)
	if err != nil {
		t.Fatalf("OpenFileStore retried = error %v, want the store", err)
	}
	save(t, store, "conn", rotating(forgeapi.FamilyGitHub, "https://forge.example", 8*time.Hour, 8*time.Hour))
}

// holdAllDescriptorsButOne lowers the descriptor limit and fills every slot under
// it but one, and answers the release that frees them and restores the limit.
func holdAllDescriptorsButOne(t *testing.T) (release func()) {
	t.Helper()
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatalf("Setup: Getrlimit = error %v", err)
	}
	// lowest is the lowest free descriptor, so the lowered limit leaves 32 slots to fill.
	warm, err := os.OpenFile(t.TempDir(), os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("Setup: OpenFile = error %v", err)
	}
	lowest := warm.Fd()
	_ = warm.Close()
	lowered := limit
	lowered.Cur = uint64(lowest) + 32
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lowered); err != nil {
		t.Fatalf("Setup: Setrlimit = error %v", err)
	}
	var held []*os.File
	done := false
	release = func() {
		if done {
			return
		}
		done = true
		for _, f := range held {
			_ = f.Close()
		}
		_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit)
	}
	t.Cleanup(release)
	for {
		f, err := os.Open(os.DevNull)
		if err != nil {
			if !errors.Is(err, syscall.EMFILE) {
				release()
				t.Fatalf("Setup: Open(%s) = error %v, want EMFILE once the limit is reached", os.DevNull, err)
			}
			break
		}
		held = append(held, f)
	}
	if len(held) == 0 {
		release()
		t.Fatal("Setup: the lowered limit left no descriptor to free")
	}
	_ = held[len(held)-1].Close()
	held = held[:len(held)-1]
	return release
}
