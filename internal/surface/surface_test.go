package surface_test

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/internal/surface"
)

const fixtureModule = "module example.com/fx\n\ngo 1.27\n"

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("Setup: MkdirAll(%q): %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("Setup: WriteFile(%q): %v", path, err)
		}
	}
	return dir
}

func generate(t *testing.T, dir string) string {
	t.Helper()
	got, err := surface.Generate(dir)
	if err != nil {
		t.Fatalf("surface.Generate(%q) = %v, want a rendering", dir, err)
	}
	return string(got)
}

func TestGenerate_renders_each_constant_and_variable_with_what_its_group_gives_it(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"go.mod": fixtureModule,
		"values.go": `package fx

type Level int

const (
	LevelLow Level = iota
	LevelMid
	_
	LevelTop
)

type Flag uint

const (
	FlagA Flag = 1 << iota
	FlagB
)

const (
	Seven = 7
	Again
	Pinned = iota
	Next
)

var Plain int

var (
	Start = 1
	Later int
)

var hidden = 3
`,
	})
	want := surface.Header + `
## package fx (./.)

const Again = 7
type Flag uint
const FlagA Flag = 1 << iota
const FlagB Flag = 1 << iota // iota = 1
var Later int
type Level int
const LevelLow Level = iota
const LevelMid Level = iota + 1
const LevelTop Level = iota + 3
const Next = iota + 1
const Pinned = iota
var Plain int
const Seven = 7
var Start = 1
`
	if got := generate(t, dir); got != want {
		t.Errorf("surface.Generate(values fixture) =\n%s\nwant\n%s", got, want)
	}
}

func TestGenerate_renders_exported_members_and_marks_what_it_cannot_render(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"go.mod": fixtureModule,
		"types.go": "package fx\n\n" +
			"import (\n\t\"io\"\n\n\t\"example.com/fx/internal/core\"\n\talias \"example.com/fx/sub\"\n)\n\n" +
			"type Empty struct{}\n\n" +
			"type Record struct {\n" +
			"\tName   string `json:\"name\"`\n" +
			"\tID, id int\n" +
			"\tsecret string\n" +
			"\tShared\n" +
			"\t*alias.Base\n" +
			"\tcore.Engine\n" +
			"\tinner\n" +
			"}\n\n" +
			"type Shared struct{}\n\n" +
			"type inner struct{}\n\n" +
			"type Reader interface {\n" +
			"\tio.Reader\n" +
			"\tRead2(p []byte) (int, error)\n" +
			"\terror\n" +
			"\thidden()\n" +
			"\tcore.Driver\n" +
			"\tlocal\n" +
			"}\n\n" +
			"type local interface{}\n\n" +
			"type Pair[K comparable, V any] struct {\n\tKey K\n\tVal V\n}\n\n" +
			"type Alias = Record\n\n" +
			"type Names []string\n",
		"sub/base.go": "package sub\n\ntype Base struct{ N int }\n",
	})
	want := surface.Header + "\n## package fx (./.)\n\n" +
		"type Alias = Record\n" +
		"type Empty struct{}\n" +
		"type Names []string\n" +
		"type Pair[K comparable, V any] struct {\n\tKey K\n\tVal V\n}\n" +
		"type Reader interface {\n" +
		"\t// embeds io.Reader\n" +
		"\tRead2(p []byte) (int, error)\n" +
		"\terror\n" +
		"\t// embeds core.Driver\n" +
		"\t// embeds local\n" +
		"\t// contains unexported members\n" +
		"}\n" +
		"type Record struct {\n" +
		"\tName string `json:\"name\"`\n" +
		"\tID int\n" +
		"\tShared\n" +
		"\t*alias.Base\n" +
		"\t// embeds core.Engine\n" +
		"\t// embeds inner\n" +
		"\t// contains unexported members\n" +
		"}\n" +
		"type Shared struct{}\n" +
		"\n## package sub (./sub)\n\n" +
		"type Base struct {\n\tN int\n}\n"
	if got := generate(t, dir); got != want {
		t.Errorf("surface.Generate(types fixture) =\n%s\nwant\n%s", got, want)
	}
}

func TestGenerate_renders_exported_functions_and_the_methods_of_exported_types(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"go.mod": fixtureModule,
		"funcs.go": `package fx

type Record struct{}

type Pair[K comparable, V any] struct{}

type inner struct{}

func Open(name string) (*Record, error) { return nil, nil }

func helper() {}

func Map[T, U any](in []T, f func(T) U) []U { return nil }

func (r *Record) Close() error { return nil }

func (Record) String() string { return "" }

func (r *Record) reset() {}

func (p Pair[K, V]) First() (k K) { return k }

func (i inner) Run() {}
`,
	})
	want := surface.Header + `
## package fx (./.)

func Map[T, U any](in []T, f func(T) U) []U
func Open(name string) (*Record, error)
type Pair[K comparable, V any] struct{}
func (p Pair[K, V]) First() (k K)
type Record struct{}
func (r *Record) Close() error
func (Record) String() string
`
	if got := generate(t, dir); got != want {
		t.Errorf("surface.Generate(funcs fixture) =\n%s\nwant\n%s", got, want)
	}
}

// Only what a consumer can import is surface: internal, testdata and dot or
// underscore directories are skipped, test files are not read, and a directory
// with no Go source renders no section.
func TestGenerate_renders_only_the_packages_a_consumer_can_import(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"go.mod":               fixtureModule,
		"root.go":              "package fx\n\nfunc Root() {}\n",
		"root_test.go":         "package fx\n\nfunc TestHelper() {}\n",
		"zeta/zeta.go":         "package zeta\n\nfunc Z() {}\n",
		"alpha/alpha.go":       "package alpha\n\nfunc A() {}\n",
		"alpha/deep/deep.go":   "package deep\n\nfunc D() {}\n",
		"quiet/quiet.go":       "package quiet\n\nfunc hidden() {}\n",
		"docs/README.md":       "# not Go\n",
		"internal/core/c.go":   "package core\n\nfunc Internal() {}\n",
		"alpha/internal/i.go":  "package internal\n\nfunc Nested() {}\n",
		"testdata/t/t.go":      "package t\n\nfunc Fixture() {}\n",
		"_draft/d.go":          "package draft\n\nfunc Draft() {}\n",
		".hidden/h.go":         "package hidden\n\nfunc Hidden() {}\n",
		"zeta/zeta_test.go":    "package zeta\n\nfunc TestZ() {}\n",
		"zeta/notes/notes.txt": "not Go\n",
	})
	want := surface.Header +
		"\n## package fx (./.)\n\nfunc Root()\n" +
		"\n## package alpha (./alpha)\n\nfunc A()\n" +
		"\n## package deep (./alpha/deep)\n\nfunc D()\n" +
		"\n## package zeta (./zeta)\n\nfunc Z()\n"
	if got := generate(t, dir); got != want {
		t.Errorf("surface.Generate(tree fixture) =\n%s\nwant\n%s", got, want)
	}
}

// With no module path to resolve a qualifier against, no qualified embed can be
// shown to have its methods rendered, so every one is marked.
func TestGenerate_marks_every_qualified_embed_where_the_module_path_cannot_be_read(t *testing.T) {
	files := map[string]string{
		"types.go":    "package fx\n\nimport alias \"example.com/fx/sub\"\n\ntype Record struct {\n\talias.Base\n}\n",
		"sub/base.go": "package sub\n\ntype Base struct{}\n",
	}
	for name, mod := range map[string]string{
		"no_go_mod":             "",
		"go_mod_naming_no_path": "go 1.27\n",
	} {
		t.Run(name, func(t *testing.T) {
			tree := maps.Clone(files)
			if mod != "" {
				tree["go.mod"] = mod
			}
			want := surface.Header +
				"\n## package fx (./.)\n\ntype Record struct {\n\t// embeds alias.Base\n}\n" +
				"\n## package sub (./sub)\n\ntype Base struct{}\n"
			if got := generate(t, writeTree(t, tree)); got != want {
				t.Errorf("surface.Generate(%s) =\n%s\nwant\n%s", name, got, want)
			}
		})
	}
}

func TestGenerate_refuses_a_tree_it_cannot_read(t *testing.T) {
	broken := writeTree(t, map[string]string{
		"go.mod": fixtureModule,
		"ok.go":  "package fx\n\nfunc Fine() {}\n",
		"bad.go": "package fx\n\nfunc Broken( {\n",
	})
	for name, dir := range map[string]string{
		"a_file_that_does_not_parse": broken,
		"a_directory_that_is_absent": filepath.Join(t.TempDir(), "absent"),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := surface.Generate(dir)
			if err == nil {
				t.Fatalf("surface.Generate(%s) = %q, nil, want an error", name, got)
			}
			if got != nil {
				t.Errorf("surface.Generate(%s) = %q with error %v, want no rendering", name, got, err)
			}
			if !strings.Contains(err.Error(), dir) {
				t.Errorf("surface.Generate(%s) error = %q, want it to name %s", name, err, dir)
			}
		})
	}
}
