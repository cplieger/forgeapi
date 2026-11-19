// Package credfile keeps files in one private directory held open as a root: the
// custody the credential store's file needs, and the one package of this module
// that names the atomic-file dependency.
//
// Its errors keep the dependency's text, save the mode refusal's, and wrap only
// this package's sentinels and the platform's errno, never the dependency's values,
// so nothing a consumer matches with errors.Is or errors.As ties it to that
// dependency's next major.
package credfile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"syscall"

	"github.com/cplieger/go/filesafe"
)

const (
	// fileMode is the mode every file here is written at and enforced to.
	fileMode = 0o600
	// maxFileBytes bounds a file both ways, so this package never writes a
	// file its own read would refuse.
	maxFileBytes = 1 << 20
)

var (
	// ErrNotPrivate is a directory with group or other access or another owner.
	ErrNotPrivate = errors.New("creds: the credential directory is not private to this process")
	// ErrNotSynced is an Open failure that left the directory in place; calling Open
	// again is safe.
	ErrNotSynced = errors.New("creds: the credential directory's parent was not synced; opening it again is safe")
)

// Dir is one private directory under a retained root handle.
type Dir struct {
	root   *os.Root
	logger *slog.Logger
	path   string
}

// Open establishes custody of one directory level: created at 0700 where absent,
// refused with ErrNotPrivate where it pre-exists with group or other access or
// another owner, and then held as a root whose own handle is checked again,
// because opening the root by name re-resolves the path the custody check vouched
// for. A failure matching ErrNotSynced leaves the directory in place, and calling
// Open again is safe. Every call into the dependency carries logger, since the
// dependency otherwise logs to a sink this library does not own.
func Open(dir string, logger *slog.Logger) (*Dir, error) {
	if _, err := filesafe.EnsurePrivateDir(dir, filesafe.WithLogger(logger)); err != nil {
		return nil, dirRefusal("credential directory "+dir, err)
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

// verify refuses the directory, through the retained handle, with ErrNotPrivate
// unless this process owns it and no group or other access is granted. It runs
// before every read and write, so a directory widened after Open fails the next
// access rather than holding a credential in the open.
func (d *Dir) verify() error {
	if err := filesafe.CheckPrivateDirInRoot(d.root, "."); err != nil {
		return dirRefusal("credential directory "+d.path, err)
	}
	return nil
}

// Read answers name's bytes, and false with no error where the directory holds no
// such file.
func (d *Dir) Read(ctx context.Context, name string) (data []byte, found bool, err error) {
	if verifyErr := d.verify(); verifyErr != nil {
		return nil, false, verifyErr
	}
	data, err = filesafe.ReadBoundedInRoot(ctx, d.root, name, maxFileBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, detach("credential file "+name, err)
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
	res, err := filesafe.WriteFileInRoot(ctx, d.root, name, data,
		filesafe.WithMode(fileMode), filesafe.WithMaxBytes(maxFileBytes), filesafe.WithLogger(d.logger))
	if err != nil {
		return false, detach("credential file "+name, err)
	}
	return res.Durable, nil
}

// detached is a dependency failure as this package returns it.
type detached struct {
	text    string
	wrapped []error
}

func (e *detached) Error() string   { return e.text }
func (e *detached) Unwrap() []error { return e.wrapped }

// detach answers err, a failure about subject, wrapping this package's sentinel
// for each dependency sentinel the store documents and the errno beneath it.
func detach(subject string, err error) *detached {
	e := &detached{text: subject + ": " + err.Error()}
	switch {
	case errors.Is(err, filesafe.ErrModeTooOpen), errors.Is(err, filesafe.ErrNotOwned):
		e.wrapped = append(e.wrapped, ErrNotPrivate)
	case errors.Is(err, filesafe.ErrParentNotSynced):
		e.wrapped = append(e.wrapped, ErrNotSynced)
	}
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		e.wrapped = append(e.wrapped, errno)
	}
	return e
}

// dirRefusal is detach for a check of the directory itself. Its mode refusal names
// the mode the check read and one remedy for Open and every access, because the
// dependency's text at Open names an option no caller here can pass.
func dirRefusal(subject string, err error) error {
	e := detach(subject, err)
	if tooOpen, ok := errors.AsType[*filesafe.ModeTooOpenError](err); ok {
		e.text = fmt.Sprintf("%s: mode %#o grants group or other access; narrow it to 0700", subject, chmodBits(tooOpen.Mode))
	}
	return e
}

// chmodBits answers mode in chmod(1)'s numbering, special bits included.
func chmodBits(mode fs.FileMode) uint32 {
	bits := uint32(mode.Perm())
	if mode&fs.ModeSetuid != 0 {
		bits |= 0o4000
	}
	if mode&fs.ModeSetgid != 0 {
		bits |= 0o2000
	}
	if mode&fs.ModeSticky != 0 {
		bits |= 0o1000
	}
	return bits
}
