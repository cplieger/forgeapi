package gen

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// update is passed by the go:generate directive in doc.go.
var update = flag.Bool("update", false, "write the generated files instead of comparing them")

// moduleDir is the module root seen from this package's directory, where go test
// runs a test.
const moduleDir = "../.."

// TestGeneratedFilesAreCurrent is the stale-output gate: it renders both outputs
// into memory from the expectation table and fails when either differs from the
// file on disk, so a per-forge block edited by hand and a table changed without a
// regeneration are the same failure. With -update it writes them instead.
//
// It is the whole of the byte-identity gate, rather than a test of its own beside
// one: what a renderer-against-fixture gate compares is two renderings, and what this
// compares is the published documentation against the table it comes from.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	// An os.Root on the module keeps every read and write inside it.
	root, err := os.OpenRoot(moduleDir)
	if err != nil {
		t.Fatalf("Setup: os.OpenRoot(%q): %v", moduleDir, err)
	}
	defer root.Close()
	current, err := root.ReadFile(rolesFile)
	if err != nil {
		t.Fatalf("Setup: reading %s: %v", rolesFile, err)
	}
	outputs := map[string][]byte{supportFile: renderSupport()}
	roles, err := renderRoles(current)
	if err != nil {
		t.Errorf("renderRoles(%s) = %v, want a rendered file", rolesFile, err)
	} else {
		outputs[rolesFile] = roles
	}
	for name, want := range outputs {
		if *update {
			if err := write(root, name, want); err != nil {
				t.Errorf("write(%s) = %v, want the file written", name, err)
			}
			continue
		}
		held, err := root.ReadFile(name)
		if err != nil {
			t.Fatalf("Setup: reading %s: %v", name, err)
		}
		if !bytes.Equal(held, want) {
			t.Errorf("%s is stale: %s\n\trun go generate ./... from the module root", name, firstDifference(held, want))
		}
	}
}

// write leaves a file whose content already matches untouched, so a generate run
// over an up-to-date tree changes no timestamp.
func write(root *os.Root, name string, want []byte) error {
	got, err := root.ReadFile(name)
	if err == nil && bytes.Equal(got, want) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return root.WriteFile(name, want, 0o600)
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
		for line := range strings.SplitSeq(strings.TrimSuffix(got, "\n"), "\n") {
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

// TestPriceSpellsThePerNameFigures holds the price line to the figures a label
// change publishes, beside one figure of each other kind for contrast.
func TestPriceSpellsThePerNameFigures(t *testing.T) {
	cases := []struct {
		name     string
		want     string
		requests spec.Requests
	}{
		{name: "a_fixed_call", requests: spec.Requests{Min: 2, Max: 2}, want: "2 requests"},
		{name: "a_range_plus_one_per_name", requests: spec.Requests{Min: 0, Max: 1, PerName: 1}, want: "0 to 1 request plus one per label name"},
		{name: "a_range_plus_up_to_one_per_name", requests: spec.Requests{Min: 1, Max: 2, PerNameCeiling: 1}, want: "1 to 2 requests plus up to one per label name"},
		{name: "a_fixed_and_a_ceiling_per_name", requests: spec.Requests{Min: 1, Max: 1, PerName: 1, PerNameCeiling: 2}, want: "1 request plus one to 3 per label name"},
		{name: "per_name_alone", requests: spec.Requests{PerName: 1}, want: "one request per label name"},
		{name: "a_ceiling_per_name_alone", requests: spec.Requests{PerNameCeiling: 1}, want: "up to one request per label name"},
		{name: "two_per_name_alone", requests: spec.Requests{PerName: 2}, want: "2 requests per label name"},
		{name: "per_row_and_per_name", requests: spec.Requests{Min: 1, Max: 1, PerItem: 1, PerName: 1}, want: "1 request plus one per row plus one per label name"},
		{name: "nothing", requests: spec.Requests{}, want: "no request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := price(tc.requests); got != tc.want {
				t.Errorf("price(%+v) = %q, want %q", tc.requests, got, tc.want)
			}
		})
	}
}
