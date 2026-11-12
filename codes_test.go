package forgeapi_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v2"
)

// TestCodes_ListsTheBlock parses errors.go for every Code constant and holds Codes to that
// block both ways, in declaration order.
func TestCodes_ListsTheBlock(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "errors.go", nil, 0)
	if err != nil {
		t.Fatalf("parse errors.go: %v", err)
	}
	var declared []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Code") || !name.IsExported() || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s is not a string literal", name.Name)
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: %v", name.Name, err)
				}
				declared = append(declared, v)
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("found no Code constants in errors.go")
	}
	if got := forgeapi.Codes(); !slices.Equal(got, declared) {
		t.Errorf("Codes() = %v\nerrors.go declares %v", got, declared)
	}
	if c := forgeapi.Codes(); len(slices.Compact(slices.Sorted(slices.Values(c)))) != len(c) {
		t.Errorf("Codes() repeats a spelling: %v", c)
	}
}
