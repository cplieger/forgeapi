// Package surface renders a module's exported Go surface as text, so a golden
// file can pin it and a test can fail on any drift.
//
// It parses and never type-checks, with the standard library alone, so the pin
// adds no dependency to the module. Its one use of go/types is a name lookup in
// the universe scope, which is a table rather than a type checker.
package surface

import (
	"bytes"
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Header is the first line of the rendered surface. It names the generator so a
// reader of the golden file knows the file is not hand-maintained.
const Header = "# forgeapi exported surface, rendered by internal/surface. Regenerate with:\n" +
	"#   UPDATE_GOLDEN=1 go test ./... -run TestExportedSurfaceMatchesGolden\n"

// entry is one rendered top-level declaration.
type entry struct {
	key  string
	text string
}

// Generate renders every exported declaration of every package under dir. It
// sorts top-level declarations by symbol path, a method as "Type.Method", so
// reordering a file changes nothing.
func Generate(dir string) ([]byte, error) {
	dirs, err := packageDirs(dir)
	if err != nil {
		return nil, err
	}
	rendered := renderedPaths(dir, dirs)
	var buf bytes.Buffer
	buf.WriteString(Header)
	for _, pkgDir := range dirs {
		name, entries, err := packageSurface(pkgDir, rendered)
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			continue
		}
		rel, err := filepath.Rel(dir, pkgDir)
		if err != nil {
			return nil, fmt.Errorf("relative path of %s: %w", pkgDir, err)
		}
		slices.SortFunc(entries, func(a, b entry) int {
			return cmp.Or(strings.Compare(a.key, b.key), strings.Compare(a.text, b.text))
		})
		fmt.Fprintf(&buf, "\n## package %s (./%s)\n\n", name, filepath.ToSlash(rel))
		for _, e := range entries {
			buf.WriteString(e.text)
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes(), nil
}

// renderedPaths returns the import path of every package this run renders, which
// is what decides whether an embedded type's own methods are visible in the
// output at all.
//
// It is EMPTY when the module path cannot be read, which marks every qualified
// embed rather than trusting one: a set built from a guess would clear exactly
// the embeds this marker exists for.
func renderedPaths(dir string, dirs []string) map[string]bool {
	mod := modulePath(dir)
	if mod == "" {
		return nil
	}
	paths := make(map[string]bool, len(dirs))
	for _, pkgDir := range dirs {
		rel, err := filepath.Rel(dir, pkgDir)
		if err != nil {
			continue
		}
		path := mod
		if rel != "." {
			path += "/" + filepath.ToSlash(rel)
		}
		paths[path] = true
	}
	return paths
}

// modulePath reads the module path from dir's go.mod, empty where there is none
// to read. It reads the one line it needs rather than taking a module parser as a
// dependency, which this module's own rule forbids.
func modulePath(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(path)
		}
	}
	return ""
}

// packageDirs returns every directory under dir holding a non-test Go file, in
// lexical order, skipping the directories [skipDir] names.
func packageDirs(dir string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != dir && skipDir(d.Name()) {
			return fs.SkipDir
		}
		hasGo, err := hasSourceFile(path)
		if err != nil {
			return err
		}
		if hasGo {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking %s: %w", dir, err)
	}
	return dirs, nil
}

// skipDir reports whether a directory is outside the surface: internal, which no
// consumer can import, so a declaration there breaks nobody; testdata; and any
// name beginning with "." or "_". A type from internal that an exported type
// embeds is marked instead, see [scope.rendersMethodsOf].
func skipDir(name string) bool {
	if name == "internal" || name == "testdata" {
		return true
	}
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func hasSourceFile(dir string) (bool, error) {
	names, err := sourceFiles(dir)
	if err != nil {
		return false, err
	}
	return len(names) > 0, nil
}

// sourceFiles returns the non-test Go files in dir, in lexical order.
func sourceFiles(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var names []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, filepath.Join(dir, name))
	}
	slices.Sort(names)
	return names, nil
}

// packageSurface renders one package's exported declarations.
func packageSurface(dir string, rendered map[string]bool) (string, []entry, error) {
	names, err := sourceFiles(dir)
	if err != nil {
		return "", nil, err
	}
	fset := token.NewFileSet()
	var (
		pkgName string
		entries []entry
	)
	for _, name := range names {
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			return "", nil, fmt.Errorf("parsing %s: %w", name, err)
		}
		pkgName = file.Name.Name
		sc := scope{imports: fileImports(file), rendered: rendered}
		entries = append(entries, fileSurface(fset, file, sc)...)
	}
	return pkgName, entries, nil
}

// scope is what rendering one file needs to know beyond that file: which package
// a type's qualifier names, and which of those packages this run renders.
type scope struct {
	imports  map[string]string
	rendered map[string]bool
}

// fileImports maps each import's local name to its path, which is how a qualifier
// in an embedded type resolves to the package it names. A path whose last element
// is not the package's own name resolves to nothing, which marks the embed rather
// than clearing it.
func fileImports(file *ast.File) map[string]string {
	imports := make(map[string]string, len(file.Imports))
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := path
		if i := strings.LastIndexByte(path, '/'); i >= 0 {
			name = path[i+1:]
		}
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = path
	}
	return imports
}

// rendersMethodsOf reports whether the exported methods of an embedded type are
// rendered by this run, which is what decides whether a method ADDED to that type
// later shows up here as drift.
//
// Three answers, and the middle one is why exportedness alone is not the test. An
// unexported type is not rendered, so neither are its methods. An exported type
// from a package this run does not render (anything under internal, anything
// outside this module) has its methods rendered nowhere, so a shared core's
// growth would be invisible while its promoted surface reached consumers. An
// exported type from a package this run does render carries its own methods in the
// output, so the addition is a diff of its own.
func (s scope) rendersMethodsOf(e ast.Expr) bool {
	qualifier, name, ok := embeddedName(e)
	if !ok {
		// Not a named type: a constraint element such as a union or a ~T
		// promotes nothing, so there is nothing this generator cannot see.
		return true
	}
	if qualifier == "" {
		return ast.IsExported(name) || predeclaredType(name)
	}
	if !ast.IsExported(name) {
		return false
	}
	return s.rendered[s.imports[qualifier]]
}

// predeclaredType reports whether name is one of the language's own type names,
// whose method set is fixed by the specification rather than by this module: error
// carries one method that cannot move, and comparable, any and the basic types
// carry none at all, so none of them is an unrendered embed. It is a lookup in the
// universe scope rather than a list this file would have to maintain.
func predeclaredType(name string) bool {
	_, ok := types.Universe.Lookup(name).(*types.TypeName)
	return ok
}

func fileSurface(fset *token.FileSet, file *ast.File, sc scope) []entry {
	var entries []entry
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			entries = append(entries, genDeclEntries(fset, d, sc)...)
		case *ast.FuncDecl:
			if e, ok := funcEntry(fset, d); ok {
				entries = append(entries, e)
			}
		}
	}
	return entries
}

func genDeclEntries(fset *token.FileSet, d *ast.GenDecl, sc scope) []entry {
	var (
		entries []entry
		group   valueGroup
	)
	for i, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if s.Name.IsExported() {
				entries = append(entries, entry{key: s.Name.Name, text: typeText(fset, s, sc)})
			}
		case *ast.ValueSpec:
			entries = append(entries, group.entries(fset, d.Tok, i, s)...)
		}
	}
	return entries
}

// valueGroup is the type and value the specifications of one const or var group
// inherit from the last specification that stated them, with the index that
// stated them so an iota offset can be counted from it.
type valueGroup struct {
	typ    ast.Expr
	values []ast.Expr
	start  int
}

// entries renders the exported names of one specification, updating what the
// specifications after it in the same group inherit.
func (g *valueGroup) entries(fset *token.FileSet, tok token.Token, i int, s *ast.ValueSpec) []entry {
	switch {
	case len(s.Values) > 0:
		*g = valueGroup{typ: s.Type, values: s.Values, start: i}
	case s.Type != nil:
		*g = valueGroup{typ: s.Type, start: i}
	}
	var entries []entry
	for _, name := range s.Names {
		if !name.IsExported() {
			continue
		}
		text := valueText(fset, tok, name.Name, s, g.typ, g.values, i-g.start)
		entries = append(entries, entry{key: name.Name, text: text})
	}
	return entries
}

// valueText renders one constant or variable, resolving the type and value a
// specification inherits from earlier in its group. A constant's value is
// rendered, iota offset included, because changing an untyped constant's value
// breaks every caller it is compiled into.
func valueText(fset *token.FileSet, tok token.Token, name string, s *ast.ValueSpec, groupType ast.Expr, groupValue []ast.Expr, offset int) string {
	typ := s.Type
	values := s.Values
	if typ == nil {
		typ = groupType
	}
	if len(values) == 0 {
		values = groupValue
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", tok, name)
	if typ != nil {
		fmt.Fprintf(&b, " %s", exprText(fset, typ))
	}
	switch {
	case len(values) == 0:
		// A variable declared by type alone.
	case offset == 0:
		fmt.Fprintf(&b, " = %s", exprList(fset, values))
	case isBareIota(values):
		fmt.Fprintf(&b, " = iota + %d", offset)
	case mentionsIota(values):
		fmt.Fprintf(&b, " = %s // iota = %d", exprList(fset, values), offset)
	default:
		fmt.Fprintf(&b, " = %s", exprList(fset, values))
	}
	return b.String()
}

func isBareIota(values []ast.Expr) bool {
	if len(values) != 1 {
		return false
	}
	id, ok := values[0].(*ast.Ident)
	return ok && id.Name == "iota"
}

// mentionsIota reports whether an inherited value READS iota without being iota
// alone, which is the shape whose members all render identically otherwise: the
// expression is the same text for every member of the group, so `1 << iota`
// three times over renders three indistinguishable lines and swapping two
// members, which changes both their values, moves nothing in the golden file.
// The offset is what tells them apart.
func mentionsIota(values []ast.Expr) bool {
	if len(values) != 1 {
		return false
	}
	found := false
	ast.Inspect(values[0], func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "iota" {
			found = true
		}
		return !found
	})
	return found
}

func funcEntry(fset *token.FileSet, d *ast.FuncDecl) (entry, bool) {
	if !d.Name.IsExported() {
		return entry{}, false
	}
	sig := funcSig(fset, d.Type)
	if d.Recv == nil {
		return entry{key: d.Name.Name, text: "func " + d.Name.Name + sig}, true
	}
	recv, base, ok := receiverText(fset, d.Recv)
	if !ok {
		return entry{}, false
	}
	return entry{
		key:  base + "." + d.Name.Name,
		text: fmt.Sprintf("func (%s) %s%s", recv, d.Name.Name, sig),
	}, true
}

// receiverText renders a receiver and reports the exported base type it is on.
func receiverText(fset *token.FileSet, recv *ast.FieldList) (text, base string, ok bool) {
	if recv == nil || len(recv.List) != 1 {
		return "", "", false
	}
	f := recv.List[0]
	typ := exprText(fset, f.Type)
	base = strings.TrimPrefix(typ, "*")
	if i := strings.IndexByte(base, '['); i >= 0 {
		base = base[:i]
	}
	if !ast.IsExported(base) {
		return "", "", false
	}
	if len(f.Names) == 0 {
		return typ, base, true
	}
	return f.Names[0].Name + " " + typ, base, true
}

func typeText(fset *token.FileSet, s *ast.TypeSpec, sc scope) string {
	name := s.Name.Name + typeParamsText(fset, s.TypeParams)
	switch t := s.Type.(type) {
	case *ast.StructType:
		return blockText("type "+name+" struct", fieldLines(fset, t.Fields, sc))
	case *ast.InterfaceType:
		return blockText("type "+name+" interface", methodLines(fset, t.Methods, sc))
	default:
		sep := " "
		if s.Assign.IsValid() {
			sep = " = "
		}
		return "type " + name + sep + exprText(fset, s.Type)
	}
}

func blockText(head string, lines []string) string {
	if len(lines) == 0 {
		return head + "{}"
	}
	var b strings.Builder
	b.WriteString(head)
	b.WriteString(" {\n")
	for _, l := range lines {
		b.WriteString("\t")
		b.WriteString(l)
		b.WriteString("\n")
	}
	b.WriteString("}")
	return b.String()
}

// fieldLines renders the exported fields of a struct, in source order, because
// field order is surface for an unkeyed composite literal.
func fieldLines(fset *token.FileSet, fields *ast.FieldList, sc scope) []string {
	if fields == nil {
		return nil
	}
	var (
		lines    []string
		filtered bool
	)
	for _, f := range fields.List {
		line, hidden := fieldLine(fset, f, sc)
		if line != "" {
			lines = append(lines, line)
		}
		filtered = filtered || hidden
	}
	if filtered {
		lines = append(lines, unexportedMarker)
	}
	return lines
}

// fieldLine renders one field, empty where nothing about it is exported, and
// reports whether the field holds a member this file does not render.
func fieldLine(fset *token.FileSet, f *ast.Field, sc scope) (string, bool) {
	typ := exprText(fset, f.Type)
	if len(f.Names) == 0 {
		if !sc.rendersMethodsOf(f.Type) {
			return embeddedMarkerPrefix + typ, false
		}
		return typ + tagText(f), false
	}
	var (
		names    []string
		filtered bool
	)
	for _, n := range f.Names {
		if n.IsExported() {
			names = append(names, n.Name)
		} else {
			filtered = true
		}
	}
	if len(names) == 0 {
		return "", filtered
	}
	return strings.Join(names, ", ") + " " + typ + tagText(f), filtered
}

// tagText renders a field's struct tag, which is part of the surface for every
// consumer that encodes the struct: a tag added, changed or removed changes what
// crosses that consumer's wire while every declaration stays byte for byte the
// same.
func tagText(f *ast.Field) string {
	if f.Tag == nil {
		return ""
	}
	return " " + f.Tag.Value
}

// unexportedMarker records that a struct or interface has members this file does
// not render. On an interface it is the load-bearing half: an interface with an
// unexported method cannot be implemented outside its own package, so rendering
// one as empty would say the opposite of what it means. The marker carries no
// member NAME, because renaming an unexported member breaks nobody and would
// only show up here as drift.
const unexportedMarker = "// contains unexported members"

// embeddedMarkerPrefix leads the line rendered for an embedded type whose own
// methods this run does not render, and unlike unexportedMarker it carries the
// type's name.
//
// An embedded type promotes its own exported methods onto the embedding type, so
// it adds to the surface while declaring nothing there: this generator reads
// declarations, so where the embedded type's methods are rendered nowhere they are
// invisible to it and a promoted Raw() would arrive with every gate green. Two
// shapes reach that state, an unexported type and a type from a package this run
// does not render, and the second is the one a shared transport core arrives as.
// It applies to an embedded INTERFACE on the same terms: a method added to an
// interface a consumer implements is the most frozen break this surface has, and
// nothing else here would see it.
//
// The name is rendered because the arrival of the embed is the only thing that CAN
// be detected, and a diff naming the type is what makes the promotion reviewable.
const embeddedMarkerPrefix = "// embeds "

// methodLines renders an interface's methods and embedded interfaces, in source
// order.
func methodLines(fset *token.FileSet, methods *ast.FieldList, sc scope) []string {
	if methods == nil {
		return nil
	}
	var (
		lines    []string
		filtered bool
	)
	for _, m := range methods.List {
		if len(m.Names) == 0 {
			typ := exprText(fset, m.Type)
			if !sc.rendersMethodsOf(m.Type) {
				lines = append(lines, embeddedMarkerPrefix+typ)
				continue
			}
			lines = append(lines, typ)
			continue
		}
		ft, ok := m.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		for _, n := range m.Names {
			if !n.IsExported() {
				filtered = true
				continue
			}
			lines = append(lines, n.Name+funcSig(fset, ft))
		}
	}
	if filtered {
		lines = append(lines, unexportedMarker)
	}
	return lines
}

// embeddedName reports the qualifier and the name of the type an embedded element
// names, and false where it names no type at all: a constraint element such as a
// union or a ~T promotes nothing, so it is not an embed in the sense this file
// cares about. The qualifier is empty for a type in the file's own package.
func embeddedName(e ast.Expr) (qualifier, name string, ok bool) {
	switch t := e.(type) {
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.IndexExpr:
		return embeddedName(t.X)
	case *ast.IndexListExpr:
		return embeddedName(t.X)
	case *ast.Ident:
		return "", t.Name, true
	case *ast.SelectorExpr:
		if id, isIdent := t.X.(*ast.Ident); isIdent {
			return id.Name, t.Sel.Name, true
		}
	}
	return "", "", false
}

func typeParamsText(fset *token.FileSet, params *ast.FieldList) string {
	if params == nil || len(params.List) == 0 {
		return ""
	}
	var parts []string
	for _, p := range params.List {
		var names []string
		for _, n := range p.Names {
			names = append(names, n.Name)
		}
		parts = append(parts, strings.Join(names, ", ")+" "+exprText(fset, p.Type))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// funcSig renders a signature without the func keyword, so it can follow either
// a function name or a method name.
func funcSig(fset *token.FileSet, ft *ast.FuncType) string {
	return strings.TrimPrefix(exprText(fset, ft), "func")
}

func exprList(fset *token.FileSet, exprs []ast.Expr) string {
	parts := make([]string, 0, len(exprs))
	for _, e := range exprs {
		parts = append(parts, exprText(fset, e))
	}
	return strings.Join(parts, ", ")
}

// exprText renders one expression on a single line. Positions in the source make
// the printer wrap, so the whitespace is collapsed afterwards: every expression
// this generator prints belongs on one line of the surface file.
func exprText(fset *token.FileSet, e ast.Expr) string {
	var b bytes.Buffer
	if err := printer.Fprint(&b, fset, e); err != nil {
		return fmt.Sprintf("<unprintable: %v>", err)
	}
	return collapse(b.String())
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
