package githublimit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// matchedHere is the one package that matches GitHub's secondary-limit phrase.
const matchedHere = "internal/githublimit"

// dataHere is the packages that may spell the phrase as data, which import
// nothing a match needs: the published cases, which send it in bodies, and the
// expectation table, which states it.
var dataHere = map[string]bool{"github/githubtest": true, "internal/spec": true}

// TestOneSecondaryMatch holds the module to one match of the secondary-limit
// message: no non-test file outside this package spells the phrase, but the
// data packages, which import nothing a match needs.
func TestOneSecondaryMatch(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("Setup: the module root holds no go.mod: %v", err)
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return skipDir(root, path, d.Name())
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(rel)
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		checkFile(t, fset, pkg, file)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}
}

// skipDir skips what the go tool skips and a nested module.
func skipDir(root, path, name string) error {
	if path == root {
		return nil
	}
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" {
		return filepath.SkipDir
	}
	if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
		return filepath.SkipDir
	}
	return nil
}

// checkFile reports a literal spelling the phrase outside the packages that
// may, and an import of a matching package in a data package.
func checkFile(t *testing.T, fset *token.FileSet, pkg string, file *ast.File) {
	t.Helper()
	if dataHere[pkg] {
		for _, spec := range file.Imports {
			if path, _ := strconv.Unquote(spec.Path.Value); path == "strings" || path == "bytes" || path == "regexp" {
				t.Errorf("%s imports %s, want a data package to import nothing a match needs", fset.Position(spec.Pos()), path)
			}
		}
	}
	if pkg == matchedHere || dataHere[pkg] {
		return
	}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if value, err := strconv.Unquote(lit.Value); err == nil && strings.Contains(strings.ToLower(value), "secondary rate") {
			t.Errorf("%s spells %q, want the secondary-limit message matched in %s alone", fset.Position(lit.Pos()), value, matchedHere)
		}
		return true
	})
}
