package credfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/cplieger/go/filesafe"
)

// The refusal names the mode filesafe's check read, never one found by looking at
// the directory again: the directory here is 0700 while the check saw the row's mode.
func TestDirRefusal_names_the_mode_the_check_read(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("Setup: Chmod = error %v", err)
	}
	for _, tc := range []struct {
		name string
		mode fs.FileMode
		want string
	}{
		{name: "group readable", mode: 0o750, want: "mode 0750 grants"},
		{name: "world writable", mode: 0o777, want: "mode 0777 grants"},
		{name: "setgid", mode: fs.ModeSetgid | 0o770, want: "mode 02770 grants"},
		{name: "sticky", mode: fs.ModeSticky | 0o707, want: "mode 01707 grants"},
		{name: "setuid", mode: fs.ModeSetuid | 0o705, want: "mode 04705 grants"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checked := fmt.Errorf("repair suffix: %w", &filesafe.ModeTooOpenError{Name: dir, Mode: tc.mode})
			err := dirRefusal("credential directory "+dir, checked)
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("dirRefusal(mode %v) = %q, want its text to contain %q", tc.mode, err, tc.want)
			}
			if !errors.Is(err, ErrNotPrivate) {
				t.Errorf("dirRefusal(mode %v) = %v, want one matching ErrNotPrivate", tc.mode, err)
			}
			if errors.Is(err, filesafe.ErrModeTooOpen) {
				t.Errorf("dirRefusal(mode %v) = %v, want no match for filesafe.ErrModeTooOpen", tc.mode, err)
			}
			if _, ok := errors.AsType[*filesafe.ModeTooOpenError](err); ok {
				t.Errorf("dirRefusal(mode %v) = %v, want no *filesafe.ModeTooOpenError in its chain", tc.mode, err)
			}
		})
	}
}

// A refusal with no typed mode keeps the dependency's own text.
func TestDirRefusal_keeps_the_text_of_a_refusal_without_a_mode(t *testing.T) {
	const path = "/srv/creds"
	err := dirRefusal("credential directory "+path, fmt.Errorf("%w: owned by uid 1", filesafe.ErrNotOwned))
	if want := "credential directory " + path + ": " + filesafe.ErrNotOwned.Error() + ": owned by uid 1"; err.Error() != want {
		t.Errorf("dirRefusal(ErrNotOwned) = %q, want %q", err, want)
	}
	if !errors.Is(err, ErrNotPrivate) {
		t.Errorf("dirRefusal(ErrNotOwned) = %v, want one matching ErrNotPrivate", err)
	}
}
