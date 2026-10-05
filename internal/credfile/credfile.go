// Package credfile keeps files in one private directory held open as a root: the
// custody the credential store's file needs, and the one package of this module
// that names the atomic-file dependency.
//
// Its errors carry the dependency's text and never its values, so nothing a
// consumer can match with errors.Is or errors.As reaches the published surface.
package credfile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"syscall"

	"github.com/cplieger/atomicfile/v4"
)

const (
	// fileMode is the mode every file here is written at and enforced to.
	fileMode = 0o600
	// maxFileBytes bounds a file both ways, so this package never writes a
	// file its own read would refuse.
	maxFileBytes = 1 << 20
)

// Dir is one private directory under a retained root handle.
type Dir struct {
	root   *os.Root
	logger *slog.Logger
	path   string
}

// Open establishes custody of one directory level: created at 0700 where absent,
// refused where it pre-exists with group or other access or another owner, and then
// held as a root whose own handle is checked again, because opening the root by
// name re-resolves the path the custody check vouched for. Every call into the
// dependency carries logger, since the dependency otherwise logs to a sink this
// library does not own.
func Open(dir string, logger *slog.Logger) (*Dir, error) {
	if _, err := atomicfile.EnsurePrivateDir(dir, atomicfile.WithLogger(logger)); err != nil {
		return nil, fmt.Errorf("credential directory %s: %v", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("credential directory %s: %w", dir, err)
	}
	d := &Dir{root: root, logger: logger, path: dir}
	if err := d.verify(); err != nil {
		_ = root.Close()
		return nil, err
	}
	return d, nil
}

// verify stats the directory through the retained handle and refuses it unless
// this process owns it and no group or other access is granted. It runs before
// every read and write, so a directory widened after Open fails the next access
// rather than holding a credential in the open.
func (d *Dir) verify() error {
	fi, err := d.root.Stat(".")
	if err != nil {
		return fmt.Errorf("credential directory %s: %w", d.path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("credential directory %s: not owned by this process", d.path)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("credential directory %s: mode %v grants group or other access; narrow it to 0700", d.path, perm)
	}
	return nil
}

// Read answers name's bytes, and false with no error where the directory holds no
// such file.
func (d *Dir) Read(ctx context.Context, name string) (data []byte, found bool, err error) {
	if verifyErr := d.verify(); verifyErr != nil {
		return nil, false, verifyErr
	}
	data, err = atomicfile.ReadBoundedInRoot(ctx, d.root, name, maxFileBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("credential file %s: %v", name, err)
	}
	return data, true, nil
}

// Write replaces name with data atomically at the private mode and reports whether
// the write was proved durable. A false with a nil error means the data is at the
// name and may not survive a crash.
func (d *Dir) Write(ctx context.Context, name string, data []byte) (bool, error) {
	if err := d.verify(); err != nil {
		return false, err
	}
	res, err := atomicfile.WriteFileInRoot(ctx, d.root, name, data,
		atomicfile.WithMode(fileMode), atomicfile.WithMaxBytes(maxFileBytes), atomicfile.WithLogger(d.logger))
	if err != nil {
		return false, fmt.Errorf("credential file %s: %v", name, err)
	}
	return res.Durable, nil
}
