package creds_test

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
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

// Root bypasses every directory permission, so a root suite runs these opens in a
// child process as nobody, in a tree whose path it names in nobodyBase.
const (
	nobody       = 65534
	nobodyBase   = "FORGEAPI_TEST_NOBODY_BASE"
	nobodyOpened = "forgeapi-test: the store is open"
)

// Another user's directory at 0700 refuses this process's open before any owner
// check can run, and it is still a directory this process does not own. Moving a
// directory to another owner after the store opened takes root, so the unprivileged
// run covers the refusal at the open only.
func TestFileStore_refuses_a_closed_directory_another_user_owns_as_not_private(t *testing.T) {
	if base := os.Getenv(nobodyBase); base != "" {
		refuseClosedForeignDirAsNobody(t, base)
		return
	}
	if os.Geteuid() != 0 {
		refuseClosedForeignDir(t, closedForeignDir(t))
		return
	}
	base, bin := nobodyTree(t)
	foreign := filepath.Join(base, "foreign")
	mine := filepath.Join(base, "mine")
	for _, dir := range []string{foreign, mine} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("Setup: Mkdir = error %v", err)
		}
	}
	if err := os.Chown(mine, nobody, nobody); err != nil {
		t.Fatalf("Setup: Chown = error %v", err)
	}
	runAsNobody(t, bin, base, func() {
		if err := os.Chown(mine, 0, 0); err != nil {
			t.Errorf("Setup: Chown(%q) back to root = error %v", mine, err)
		}
	})
}

// refuseClosedForeignDirAsNobody is the child's half: foreign is root's at 0700
// from the start, and mine becomes root's at 0700 once the store over it is open.
func refuseClosedForeignDirAsNobody(t *testing.T, base string) {
	refuseClosedForeignDir(t, filepath.Join(base, "foreign"))

	mine := filepath.Join(base, "mine")
	store, err := creds.OpenFileStore(mine)
	if err != nil {
		t.Fatalf("Setup: OpenFileStore(%q) = error %v", mine, err)
	}
	rec := rotating(forgeapi.FamilyGitHub, "https://forge.example", 8*time.Hour, 8*time.Hour)
	save(t, store, "conn", rec)
	awaitParent(t)

	_, _, err = store.Load("conn")
	assertNotPrivate(t, "Load after the directory moved to another owner", err)
	assertNotPrivate(t, "Save after the directory moved to another owner", store.Save("conn", rec))
}

func refuseClosedForeignDir(t *testing.T, dir string) {
	_, err := creds.OpenFileStore(dir)
	assertNotPrivate(t, "OpenFileStore over "+dir+", another user's at 0700", err)
}

func assertNotPrivate(t *testing.T, call string, err error) {
	t.Helper()
	if !errors.Is(err, creds.ErrDirNotPrivate) {
		t.Errorf("%s = error %v, want one matching ErrDirNotPrivate", call, err)
	}
	if errors.Is(err, creds.ErrDirNotSynced) {
		t.Errorf("%s = error %v, which matches ErrDirNotSynced; a refusal is not worth a retry", call, err)
	}
	assertNoFilesafeRefusal(t, call, err)
}

func assertNoFilesafeRefusal(t *testing.T, call string, err error) {
	t.Helper()
	for _, sentinel := range []error{filesafe.ErrNotOwned, filesafe.ErrModeTooOpen} {
		if errors.Is(err, sentinel) {
			t.Errorf("%s = error %v, which matches %v; the store's errors carry none of the dependency's values", call, err, sentinel)
		}
	}
}

// closedForeignDir answers /root, which every Linux keeps at 0700 for root.
func closedForeignDir(t *testing.T) string {
	t.Helper()
	const dir = "/root"
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("Setup: Lstat(%s) = (%v, %v), want a directory closed to group and other", dir, info, err)
	}
	if f, err := os.Open(dir); !errors.Is(err, fs.ErrPermission) {
		if f != nil {
			_ = f.Close()
		}
		t.Fatalf("Setup: Open(%s) = error %v, want it refused to this user", dir, err)
	}
	return dir
}

// The sync of a parent this process can traverse but not read fails on every
// call, so it is a permission error and neither a refusal nor worth a retry.
func TestOpenFileStore_answers_a_permission_error_when_its_parent_cannot_be_read(t *testing.T) {
	if base := os.Getenv(nobodyBase); base != "" {
		openUnderUnreadableParent(t, filepath.Join(base, "parent"))
		return
	}
	if os.Geteuid() != 0 {
		parent := filepath.Join(t.TempDir(), "parent")
		unreadableParent(t, parent)
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		openUnderUnreadableParent(t, parent)
		return
	}
	base, bin := nobodyTree(t)
	parent := filepath.Join(base, "parent")
	unreadableParent(t, parent)
	if err := os.Chown(parent, nobody, nobody); err != nil {
		t.Fatalf("Setup: Chown = error %v", err)
	}
	runAsNobody(t, bin, base, nil)
}

func unreadableParent(t *testing.T, parent string) {
	t.Helper()
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatalf("Setup: Mkdir = error %v", err)
	}
	if err := os.Chmod(parent, 0o300); err != nil {
		t.Fatalf("Setup: Chmod = error %v", err)
	}
}

func openUnderUnreadableParent(t *testing.T, parent string) {
	_, err := creds.OpenFileStore(filepath.Join(parent, "credentials"))
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("OpenFileStore under a parent at 0300 = error %v, want one matching fs.ErrPermission", err)
	}
	if errors.Is(err, creds.ErrDirNotPrivate) {
		t.Errorf("OpenFileStore under a parent at 0300 = error %v, which matches ErrDirNotPrivate; the directory itself is private", err)
	}
	if errors.Is(err, creds.ErrDirNotSynced) {
		t.Errorf("OpenFileStore under a parent at 0300 = error %v, which matches ErrDirNotSynced; a retry fails the same way", err)
	}
}

// nobodyTree answers a directory under /tmp, readable by uid nobody so the store
// can sync it as a parent, and a copy of this test binary in it; the suite's
// TMPDIR may be closed to other users.
func nobodyTree(t *testing.T) (base, bin string) {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "forgeapi-nobody-")
	if err != nil {
		t.Fatalf("Setup: MkdirTemp = error %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatalf("Setup: Chmod = error %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("Setup: Executable = error %v", err)
	}
	bin = filepath.Join(base, "creds.test")
	// A copy written while another test forks can stay open in that child and
	// fail this exec with ETXTBSY (https://go.dev/issue/22315).
	syscall.ForkLock.RLock()
	err = copyExecutable(exe, bin)
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("Setup: copying the test binary = error %v", err)
	}
	return base, bin
}

func copyExecutable(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

// runAsNobody runs the calling test again as nobody over base. When the child
// prints nobodyOpened it waits until between has run.
func runAsNobody(t *testing.T, bin, base string, between func()) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), bin, "-test.count=1", "-test.run=^"+t.Name()+"$")
	cmd.Dir = base
	cmd.Env = append(os.Environ(), nobodyBase+"="+base)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: nobody, Gid: nobody}}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("Setup: StdinPipe = error %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("Setup: StdoutPipe = error %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("Setup: starting the child as nobody = error %v", err)
	}
	var out strings.Builder
	lines := bufio.NewScanner(stdout)
	for lines.Scan() {
		out.WriteString(lines.Text() + "\n")
		if lines.Text() == nobodyOpened && between != nil {
			between()
			_, _ = io.WriteString(stdin, "\n")
		}
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Errorf("the child as nobody = error %v, output:\n%s%s", err, out.String(), stderr.String())
	}
}

// awaitParent tells runAsNobody the store is open and waits for its answer.
func awaitParent(t *testing.T) {
	t.Helper()
	if _, err := os.Stdout.WriteString(nobodyOpened + "\n"); err != nil {
		t.Fatalf("Setup: telling the parent = error %v", err)
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatalf("Setup: waiting for the parent = error %v", err)
	}
}
