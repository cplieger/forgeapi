package creds_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/forgeapi/creds"
)

// The usability marks under the short names the cases use.
const (
	markUnmarked          = creds.UsabilityUnmarked
	markSpent             = creds.UsabilitySpent
	markReconnectRequired = creds.UsabilityReconnectRequired
)

// markNames are the marks as a failure message names them.
var markNames = map[creds.Usability]string{
	markUnmarked:          "unmarked",
	markSpent:             "spent",
	markReconnectRequired: "reconnect required",
}

// withMark is rec carrying one usability mark.
func withMark(rec creds.Record, mark creds.Usability) creds.Record {
	rec.Usability = mark
	return rec
}

// reopen opens a second store on a directory, which is how another process, the
// git credential helper among them, reads the records this process wrote.
func reopen(t *testing.T, dir string) *creds.FileStore {
	t.Helper()
	store, err := creds.OpenFileStore(dir)
	if err != nil {
		t.Fatalf("Setup: OpenFileStore(%q) again = error %v", dir, err)
	}
	return store
}

// usabilityMember is the one member name the format rule fixes; every other member
// of the file is the store's own.
const usabilityMember = "usability"

// readCredentialFile decodes the store's file into a generic tree, keeping numbers
// as written, so a case can read and rewrite one member without knowing the rest.
func readCredentialFile(t *testing.T, dir string) any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, credentialFile))
	if err != nil {
		t.Fatalf("reading %s = error %v", credentialFile, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		t.Fatalf("%s is not JSON: %v", credentialFile, err)
	}
	return tree
}

// fileMarks is every value the file holds under the usability member, as written.
func fileMarks(t *testing.T, dir string) []string {
	t.Helper()
	var marks []string
	var walk func(node any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			if v, ok := n[usabilityMember]; ok {
				raw, err := json.Marshal(v)
				if err != nil {
					t.Fatalf("re-encoding the usability member %v: %v", v, err)
				}
				marks = append(marks, string(raw))
			}
			for _, child := range n {
				walk(child)
			}
		case []any:
			for _, child := range n {
				walk(child)
			}
		}
	}
	walk(readCredentialFile(t, dir))
	slices.Sort(marks)
	return marks
}

// rewriteMarks rewrites the usability member of every record in the file that
// carries one: to spelling, or removed where remove is set. It fails the case where
// no record carries the member, because the rewrite would then prove nothing.
func rewriteMarks(t *testing.T, dir string, spelling any, remove bool) {
	t.Helper()
	tree := readCredentialFile(t, dir)
	rewritten := 0
	var walk func(node any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			if _, ok := n[usabilityMember]; ok {
				rewritten++
				if remove {
					delete(n, usabilityMember)
				} else {
					n[usabilityMember] = spelling
				}
			}
			for _, child := range n {
				walk(child)
			}
		case []any:
			for _, child := range n {
				walk(child)
			}
		}
	}
	walk(tree)
	if rewritten == 0 {
		t.Fatalf("Setup: %s holds no %q member to rewrite, want the one a marked record is written with", credentialFile, usabilityMember)
	}
	raw, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("Setup: re-encoding %s: %v", credentialFile, err)
	}
	if err := os.WriteFile(filepath.Join(dir, credentialFile), raw, 0o600); err != nil {
		t.Fatalf("Setup: writing %s back: %v", credentialFile, err)
	}
}
