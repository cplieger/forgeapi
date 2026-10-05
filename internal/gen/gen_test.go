package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedFilesAreCurrent is the stale-output gate: it renders both outputs
// into memory from the expectation table and fails when either differs from the
// file on disk, so a per-forge block edited by hand and a table changed without a
// regeneration are the same failure.
//
// It is the whole of the byte-identity gate, rather than a test of its own beside
// one: what a renderer-against-fixture gate compares is two renderings, and what this
// compares is the published documentation against the table it comes from.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("Setup: moduleRoot(): %v", err)
	}
	rolesPath := filepath.Join(root, rolesFile)
	current, err := os.ReadFile(rolesPath)
	if err != nil {
		t.Fatalf("Setup: reading %s: %v", rolesPath, err)
	}
	roles, err := renderRoles(current)
	if err != nil {
		t.Errorf("renderRoles(%s) = %v, want a rendered file", rolesFile, err)
	} else if !bytes.Equal(roles, current) {
		t.Errorf("%s is stale: %s\n\trun go generate ./... from %s", rolesFile, firstDifference(current, roles), root)
	}
	supportPath := filepath.Join(root, supportFile)
	held, err := os.ReadFile(supportPath)
	if err != nil {
		t.Fatalf("Setup: reading %s: %v", supportPath, err)
	}
	if support := renderSupport(); !bytes.Equal(support, held) {
		t.Errorf("%s is stale: %s\n\trun go generate ./... from %s", supportFile, firstDifference(held, support), root)
	}
}

// TestEveryEntryRenders exercises the renderers over the whole table rather than
// over the one file, so an entry whose rendering would fail is a failure here
// even while roles.go happens to match.
func TestEveryEntryRenders(t *testing.T) {
	for _, method := range methods() {
		got, err := block(method)
		if err != nil {
			t.Errorf("block(%q) = %v, want a rendered block", method, err)
			continue
		}
		if !strings.HasPrefix(got, marker+"\n") {
			t.Errorf("block(%q) does not open with %q", method, marker)
		}
		for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
			if !strings.HasPrefix(line, comment) {
				t.Errorf("block(%q) has the non-comment line %q", method, line)
			}
		}
	}
}

// firstDifference reports where two renderings part company, by line, because a
// byte offset in a 50,000-byte file tells the reader nothing about which method
// moved.
func firstDifference(held, want []byte) string {
	a := strings.Split(string(held), "\n")
	b := strings.Split(string(want), "\n")
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return "line " + itoa(i+1) + "\n\ton disk: " + quoted(a[i]) + "\n\trendered: " + quoted(b[i])
		}
	}
	return "on disk " + itoa(len(a)) + " lines, rendered " + itoa(len(b))
}

func quoted(line string) string {
	return "\"" + strings.TrimSpace(line) + "\""
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
