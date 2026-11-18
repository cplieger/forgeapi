package forgeapi_test

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	modulePath    = "github.com/cplieger/forgeapi/v3"
	transportPath = modulePath + "/internal/transport"
)

// diagnosticFields are the fields, by [sinkTypeName], that carry text out of
// this library to a consumer's error, rendered verdict or log.
var diagnosticFields = map[string][]string{
	"Error":             {"Op", "Code", "Message", "Pool", "Needs"},
	"Evidence":          {"Detail"},
	"transport.Request": {"Op"},
	"transport.Verdict": {"Code"},
}

// transportSinks is each transport function whose plain-text argument, by
// position, becomes the named diagnostic field as given.
var transportSinks = map[string]map[int]string{
	"transport.Local":         {1: "Error.Message"},
	"transport.AfterRemovals": {1: "Error.Message"},
	"Conn.Fail":               {1: "Error.Op"},
	"Conn.FailOn":             {1: "Error.Op"},
	"Conn.FailBody":           {1: "Error.Op"},
	"Conn.FailDecode":         {1: "Error.Op"},
	"Conn.Refuse":             {1: "Error.Op"},
}

// configured is each function, method, field or parameter, by package path and
// name ([sinkCheck.nameOf]), whose text a consumer configured or passed rather
// than upstream text, with why.
var configured = map[string]string{
	modulePath + "/creds.hostOf":                     "the host of a stored record's web base URL, which the consumer stored",
	modulePath + "/gitcred.hostOf":                   "the host of a stored record's web base URL, which the consumer stored",
	modulePath + "/creds.AppSource.instance":         "the host of the API base URL the consumer configured the App source with",
	modulePath + ".RepoRef.Selector":                 "the repository selector the consumer passed, which a refusal of it names",
	modulePath + ".MergeRequest.Strategy":            "the merge strategy the consumer asked for, which a refusal of it names",
	modulePath + "/github.Client.RemoveLabels.names": "the label names the consumer asked to remove, which a refusal names as removed",
	modulePath + "/gitea.Client.RemoveLabels.names":  "the label names the consumer asked to remove, which a refusal names as removed",
	modulePath + "/gitcred.Helper.keys":              "the key list the consumer handed NewFor, or the store's Keys, whose error is theirs",
	modulePath + "/creds.Store.Load":                 "a store's read, whose error is the consumer's backend's or the file store's, which names a file and never a record's content",
}

// libraryText is each function of another package whose result is text its own
// package wrote, never its argument's.
var libraryText = map[string]bool{
	"net/http.StatusText": true,
}

// carried is each function of another package whose result holds no text but
// its own and its arguments'.
var carried = map[string]bool{
	"errors.New":                     true,
	"fmt.Sprintf":                    true,
	transportPath + ".DistinctNames": true,
}

// TestEveryDiagnosticIsBuiltFromQuotedOrLibraryText type-checks every package of
// this module but the transport, which quotes what it writes itself and whose
// plain-text parameters ([transportSinks]) are checked at each call, and fails on
// each diagnostic built from text neither this library wrote nor a quoter saw: a
// [diagnosticFields] field, a Counters label and a value a log line carries. A
// root field stays a string because a consumer's own credential source builds an
// Error too, so no Go type can admit the transport's writes and refuse a family's.
func TestEveryDiagnosticIsBuiltFromQuotedOrLibraryText(t *testing.T) {
	imp := moduleImporter(t)
	used := map[string]bool{}
	checked := 0
	for _, pkg := range run(t, "go", "list", "-f", `{{.ImportPath}}{{"\t"}}{{.Dir}}{{"\t"}}{{join .GoFiles " "}}`, "./...") {
		path, rest, _ := strings.Cut(pkg, "\t")
		dir, names, _ := strings.Cut(rest, "\t")
		if path == transportPath {
			continue
		}
		var files []string
		for name := range strings.FieldsSeq(names) {
			files = append(files, filepath.Join(dir, name))
		}
		findings, reached := unquotedSinks(t, imp, path, files)
		for _, finding := range findings {
			t.Error(finding)
		}
		for _, name := range reached {
			used[name] = true
		}
		checked++
	}
	for name, why := range configured {
		if !used[name] {
			t.Errorf("configured names %s (%s), which no diagnostic is built from, so the row admits nothing", name, why)
		}
	}
	if checked < 8 {
		t.Fatalf("Setup: checked %d packages, want every package of the module but the transport", checked)
	}
}

// TestTheSinkCheckRefusesRawTextAndAdmitsQuotedText holds the check above to its
// rule on a probe package: each sink built from a decoded field, a parameter or a
// local carrying one, a recursion or a map the package writes is refused by name,
// as is a Counters field read uncalled, and each built from library constants,
// numbers, a Quoted, a library enum, a field, a variadic parameter or a read-only
// map the package only gives those, or an earlier diagnostic is admitted.
func TestTheSinkCheckRefusesRawTextAndAdmitsQuotedText(t *testing.T) {
	const probe = `package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/transport"
)

type answer struct{ Version, Kind string }

type hold struct{ message string }

const lead = "gitlab "

var errHeld = errors.New(lead + "held")

var codes = map[string]string{"merge": forgeapi.CodeNotMergeable}

var rewritten = map[string]string{"merge": forgeapi.CodeNotMergeable}

func shown(a answer) string { return a.Version }

func raw(version string) string { return lead + version }

func quoted(version string) string { return lead + version }

func looped(s string) string { return looping(s) }

func looping(s string) string { return looped(s) }

func chosen(ok bool) string {
	text := "no version"
	if ok {
		text = lead + http.StatusText(http.StatusNotFound)
	}
	return text
}

func refusedf(format string, args ...any) *forgeapi.Error {
	return transport.Local(forgeapi.CodeValidation, fmt.Sprintf(format, args...))
}

func keptf(format string, args ...any) *forgeapi.Error {
	return transport.Local(forgeapi.CodeValidation, fmt.Sprintf(format, args...))
}

func sinks(ctx context.Context, a answer, q transport.Quoted, fe *forgeapi.Error, counters forgeapi.Counters, log *slog.Logger, conn *transport.Conn, resp *transport.Response, err error, s fmt.Stringer) []any {
	h := hold{message: "held"}
	counters.EnvelopeError(forgeapi.FamilyGitLab, a.Kind)
	counters.EnvelopeError(forgeapi.FamilyGitLab, q.String())
	count := counters.EnvelopeError
	count(forgeapi.FamilyGitLab, lead)
	if counters.EnvelopeError != nil {
		counters.EnvelopeError(forgeapi.FamilyGitLab, lead)
	}
	log.Warn("probe", "value", a.Kind, "family", forgeapi.FamilyGitLab.String())
	log.Warn("probe", "value", q, "held", h.message)
	log.Warn("probe", "error", err, "named", s)
	log.Warn("probe", "error", errHeld, "level", slog.LevelWarn, "family", forgeapi.FamilyGitLab, "count", len(a.Kind))
	fe.Message = shown(a)
	fe.Needs = fe.Message
	fe.Op = a.Kind
	rewritten[a.Kind] = a.Version
	var removed []string
	for _, name := range []string{lead, "held"} {
		removed = append(removed, name)
	}
	x, y := lead, lead
	for range 2 {
		y = x
		x = y + a.Version
	}
	ex := transport.REST(http.MethodGet)
	return []any{
		&forgeapi.Error{Op: x},
		&forgeapi.Error{Message: y},
		forgeapi.Evidence{Detail: raw(a.Version)},
		forgeapi.Evidence{Detail: quoted(q.String())},
		&forgeapi.Error{Message: chosen(true), Pool: a.Kind},
		forgeapi.Evidence{forgeapi.EvidenceMetadata, a.Version},
		transport.Local(forgeapi.CodeValidation, a.Version+lead),
		transport.Local(forgeapi.CodeValidation, lead+http.StatusText(http.StatusNotFound)),
		refusedf("probe %s", a.Kind),
		keptf("probe %s at %d", lead, 3),
		transport.AfterRemovals(fe, []string{a.Kind}),
		transport.AfterRemovals(fe, removed),
		conn.Fail(ctx, a.Kind, ex, resp, "", forgeapi.KindUpstream, a.Version),
		conn.FailOn(ctx, a.Version, ex, fe, "", forgeapi.KindUpstream, a.Kind),
		conn.FailBody(ctx, shown(a), ex, resp, errHeld),
		conn.FailDecode(ctx, raw(a.Kind), ex, resp, err),
		conn.Refuse(ctx, lead+a.Kind, "", forgeapi.SupportNo, forgeapi.Evidence{}),
		conn.Fail(ctx, lead, ex, resp, "", forgeapi.KindUpstream, a.Version),
		&transport.Request{Op: a.Version, Path: a.Kind},
		&transport.Request{Op: lead},
		forgeapi.Evidence{Detail: looped(lead)},
		transport.Verdict{Code: a.Kind, Needs: a.Version},
		transport.Verdict{Code: codes[a.Kind]},
		&forgeapi.Error{Code: a.Version},
		&forgeapi.Error{Code: codes["merge"]},
		&forgeapi.Error{Code: rewritten["merge"]},
	}
}
`
	dir := t.TempDir()
	file := filepath.Join(dir, "probe.go")
	if err := os.WriteFile(file, []byte(probe), 0o600); err != nil {
		t.Fatalf("Setup: writing the probe: %v", err)
	}
	got, _ := unquotedSinks(t, moduleImporter(t), "probe", []string{file})
	var missing []string
	want := []string{
		"a Counters label is built from a.Kind",
		"a log line's string is built from a.Kind",
		"Error.Message is built from shown(a)",
		"Error.Op is built from a.Kind",
		"Evidence.Detail is built from raw(a.Version)",
		"Error.Pool is built from a.Kind",
		"a forgeapi.Evidence literal names its fields by position",
		"a Counters field, counters.EnvelopeError, is read rather than called",
		"a log line's value is built from err,",
		"a log line's value is built from s,",
		"Error.Message through transport.Local is built from a.Version + lead",
		"Error.Message through transport.Local is built from fmt.Sprintf(format, args...)",
		"Error.Message through transport.AfterRemovals is built from []string{…}",
		"Error.Op through Conn.Fail is built from a.Kind",
		"Error.Op through Conn.FailOn is built from a.Version",
		"Error.Op through Conn.FailBody is built from shown(a)",
		"Error.Op through Conn.FailDecode is built from raw(a.Kind)",
		"Error.Op through Conn.Refuse is built from lead + a.Kind",
		"transport.Request.Op is built from a.Version",
		"Evidence.Detail is built from looped(lead)",
		"transport.Verdict.Code is built from a.Kind",
		"Error.Code is built from a.Version",
		`Error.Code is built from rewritten["merge"]`,
		"Error.Op is built from x",
		"Error.Message is built from y",
	}
	for _, w := range want {
		if !slices.ContainsFunc(got, func(f string) bool { return strings.Contains(f, w) }) {
			missing = append(missing, w)
		}
	}
	if len(missing) > 0 {
		t.Errorf("the check over the probe reported no finding containing %q; it reported %q", missing, got)
	}
	if len(got) != len(want) {
		t.Errorf("the check over the probe reported %d findings, want %d: %q", len(got), len(want), got)
	}
}

// moduleImporter resolves every import of this module's packages from the export
// data the go command builds for them.
func moduleImporter(t *testing.T) types.Importer {
	t.Helper()
	exports := map[string]string{}
	for _, line := range run(t, "go", "list", "-export", "-deps", "-f", `{{.ImportPath}}{{"\t"}}{{.Export}}`, "./...") {
		path, export, _ := strings.Cut(line, "\t")
		exports[path] = export
	}
	return importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
		return os.Open(exports[path])
	})
}

// unquotedSinks type-checks files as the package path and names each diagnostic
// they build from text no quoter saw.
func unquotedSinks(t *testing.T, imp types.Importer, path string, names []string) (findings, reached []string) {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range names {
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("Setup: parsing %s: %v", name, err)
		}
		files = append(files, f)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	if _, err := (&types.Config{Importer: imp}).Check(path, fset, files, info); err != nil {
		t.Fatalf("Setup: type-checking %s: %v", path, err)
	}
	c := newSinkCheck(fset, info, files)
	c.walk()
	return c.findings, slices.Sorted(maps.Keys(c.reached))
}

// source is one value a variable is given: an expression, the index-th result of
// a call, an element of a slice (a range's value), or a value the check cannot
// follow (a range's key, an address taken, a map read), which is never safe.
type source struct {
	expr  ast.Expr
	tuple *ast.CallExpr
	elem  ast.Expr
	index int
}

// sinkCheck follows each sink's text back through one package: a local or
// parameter to every value it is given, a call of the package's own function to
// every value it returns.
type sinkCheck struct {
	fset     *token.FileSet
	info     *types.Info
	files    []*ast.File
	funcs    map[types.Object]*ast.FuncDecl
	params   map[types.Object]paramOf
	calls    map[types.Object][]*ast.CallExpr
	values   map[types.Object]bool
	escaped  map[types.Object]bool
	given    map[types.Object][]source
	verdicts map[any]bool
	assumed  []any
	owners   map[*types.Var]string
	reached  map[string]bool
	findings []string
	assuming int
}

// paramOf is a parameter's function and position; variadic marks the last of a
// variadic function, whose calls the check does not follow.
type paramOf struct {
	fn       types.Object
	index    int
	variadic bool
}

func newSinkCheck(fset *token.FileSet, info *types.Info, files []*ast.File) *sinkCheck {
	c := &sinkCheck{
		fset: fset, info: info, files: files,
		funcs: map[types.Object]*ast.FuncDecl{}, params: map[types.Object]paramOf{},
		calls: map[types.Object][]*ast.CallExpr{}, values: map[types.Object]bool{},
		escaped: map[types.Object]bool{},
		given:   map[types.Object][]source{}, verdicts: map[any]bool{},
		owners: map[*types.Var]string{}, reached: map[string]bool{},
	}
	callees := map[*ast.Ident]bool{}
	indexed, written := map[*ast.Ident]bool{}, map[*ast.Ident]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				c.declare(n)
			case *ast.TypeSpec:
				c.own(n)
			case *ast.CompositeLit:
				c.fill(n)
			case *ast.CallExpr:
				if id := calleeIdent(n); id != nil {
					callees[id] = true
					c.calls[info.Uses[id]] = append(c.calls[info.Uses[id]], n)
				}
			case *ast.AssignStmt:
				c.assign(n.Lhs, n.Rhs)
				for _, lhs := range n.Lhs {
					written[indexedIdent(lhs)] = true
				}
			case *ast.IncDecStmt:
				written[indexedIdent(n.X)] = true
			case *ast.IndexExpr:
				indexed[indexedIdent(n)] = true
			case *ast.ValueSpec:
				c.assignSpec(n)
				if len(n.Names) == 1 && n.Names[0].Name == "_" {
					// A value only the blank identifier is given is never called.
					for _, v := range n.Values {
						if id := calleeIdent(&ast.CallExpr{Fun: v}); id != nil {
							callees[id] = true
						}
					}
				}
			case *ast.RangeStmt:
				c.give(n.Key, source{})
				c.give(n.Value, source{elem: n.X})
			case *ast.UnaryExpr:
				if n.Op == token.AND {
					c.give(n.X, source{})
				}
			}
			return true
		})
	}
	for id, obj := range info.Uses {
		if _, ok := c.funcs[obj]; ok && !callees[id] {
			c.values[obj] = true
		}
		if !indexed[id] || written[id] {
			c.escaped[obj] = true
		}
	}
	return c
}

// indexedIdent is the variable e indexes, nil where e is no index of one.
func indexedIdent(e ast.Expr) *ast.Ident {
	if index, ok := ast.Unparen(e).(*ast.IndexExpr); ok {
		id, _ := ast.Unparen(index.X).(*ast.Ident)
		return id
	}
	return nil
}

func (c *sinkCheck) declare(fd *ast.FuncDecl) {
	fn := c.info.Defs[fd.Name]
	c.funcs[fn] = fd
	index := 0
	for i, field := range fd.Type.Params.List {
		_, variadic := field.Type.(*ast.Ellipsis)
		variadic = variadic && i == len(fd.Type.Params.List)-1
		for _, name := range field.Names {
			c.params[c.info.Defs[name]] = paramOf{fn: fn, index: index, variadic: variadic}
			index++
		}
		if len(field.Names) == 0 {
			index++
		}
	}
}

func (c *sinkCheck) assign(lhs, rhs []ast.Expr) {
	if len(lhs) == len(rhs) {
		for i := range lhs {
			c.give(lhs[i], source{expr: rhs[i]})
		}
		return
	}
	call, _ := ast.Unparen(rhs[0]).(*ast.CallExpr)
	for i := range lhs {
		c.give(lhs[i], source{tuple: call, index: i})
	}
}

func (c *sinkCheck) assignSpec(spec *ast.ValueSpec) {
	switch {
	case len(spec.Values) == 0:
		for _, name := range spec.Names {
			c.give(name, source{expr: &ast.BasicLit{Kind: token.STRING, Value: `""`}})
		}
	default:
		lhs := make([]ast.Expr, len(spec.Names))
		for i, name := range spec.Names {
			lhs[i] = name
		}
		c.assign(lhs, spec.Values)
	}
}

// give records that the variable e names is given s; a non-variable e is not
// followed.
func (c *sinkCheck) give(e ast.Expr, s source) {
	var obj types.Object
	switch e := ast.Unparen(e).(type) {
	case *ast.Ident:
		if obj = c.info.Defs[e]; obj == nil {
			obj = c.info.Uses[e]
		}
	case *ast.SelectorExpr:
		if sel := c.info.Selections[e]; sel != nil && sel.Kind() == types.FieldVal {
			obj = sel.Obj()
		}
	}
	if v, ok := obj.(*types.Var); ok && v.Name() != "_" {
		c.given[v] = append(c.given[v], s)
	}
}

// own names each field of a struct type the package declares by its type.
func (c *sinkCheck) own(spec *ast.TypeSpec) {
	st, ok := spec.Type.(*ast.StructType)
	if !ok {
		return
	}
	for _, field := range st.Fields.List {
		for _, name := range field.Names {
			if v, ok := c.info.Defs[name].(*types.Var); ok {
				c.owners[v] = spec.Name.Name + "." + name.Name
			}
		}
	}
}

// fill records each field a struct literal gives a value.
func (c *sinkCheck) fill(lit *ast.CompositeLit) {
	st, ok := c.info.TypeOf(lit).Underlying().(*types.Struct)
	if !ok {
		return
	}
	for i, elt := range lit.Elts {
		if kv, keyed := elt.(*ast.KeyValueExpr); keyed {
			if v, ok := c.info.Uses[kv.Key.(*ast.Ident)].(*types.Var); ok {
				c.given[v] = append(c.given[v], source{expr: kv.Value})
			}
		} else if i < st.NumFields() {
			c.given[st.Field(i)] = append(c.given[st.Field(i)], source{expr: elt})
		}
	}
}

// configured reports whether obj is a [configured] row, recording that it was
// reached.
func (c *sinkCheck) configured(obj types.Object) bool {
	return c.configuredName(c.nameOf(obj))
}

func (c *sinkCheck) configuredName(name string) bool {
	if _, ok := configured[name]; !ok {
		return false
	}
	c.reached[name] = true
	return true
}

// nameOf is obj as a [configured] row spells it: its package path, then the type
// a method or field belongs to or the function a parameter belongs to, then its
// name; empty where obj belongs to no package.
func (c *sinkCheck) nameOf(obj types.Object) string {
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	switch obj := obj.(type) {
	case *types.Func:
		if named := namedOf(recvType(obj)); named != nil {
			return obj.Pkg().Path() + "." + named.Obj().Name() + "." + obj.Name()
		}
	case *types.Var:
		if c.owners[obj] != "" {
			return obj.Pkg().Path() + "." + c.owners[obj]
		}
		if p, ok := c.params[obj]; ok {
			return c.nameOf(p.fn) + "." + obj.Name()
		}
	}
	return obj.Pkg().Path() + "." + obj.Name()
}

// selectedName is the field sel selects as a [configured] row spells it, by the
// type it is selected from.
func selectedName(sel *types.Selection) string {
	named := namedOf(sel.Recv())
	if named == nil || named.Obj().Pkg() == nil {
		return ""
	}
	return named.Obj().Pkg().Path() + "." + named.Obj().Name() + "." + sel.Obj().Name()
}

func recvType(fn *types.Func) types.Type {
	if recv := fn.Signature().Recv(); recv != nil {
		return recv.Type()
	}
	return nil
}

// walk reports each sink of the package built from text no quoter saw, and each
// Counters field read anywhere but where it is called, compared with nil or set.
func (c *sinkCheck) walk() {
	for _, f := range c.files {
		followed := map[ast.Expr]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CompositeLit:
				c.literal(n)
			case *ast.AssignStmt:
				for _, lhs := range n.Lhs {
					followed[ast.Unparen(lhs)] = true
				}
				if len(n.Lhs) == len(n.Rhs) {
					for i, lhs := range n.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && c.isDiagnosticField(sel) {
							c.require(n.Rhs[i], sinkTypeName(c.info.Selections[sel].Recv())+"."+sel.Sel.Name)
						}
					}
				}
			case *ast.BinaryExpr:
				if n.Op == token.EQL || n.Op == token.NEQ {
					followed[ast.Unparen(n.X)], followed[ast.Unparen(n.Y)] = true, true
				}
			case *ast.CallExpr:
				followed[ast.Unparen(n.Fun)] = true
				c.call(n)
			case *ast.SelectorExpr:
				if sel := c.info.Selections[n]; !followed[n] && sel != nil && sel.Kind() == types.FieldVal && rootTypeName(sel.Recv()) == "Counters" {
					c.report(n, "a Counters field, "+types.ExprString(n)+", is read rather than called, so the check cannot follow the labels it is called with; call it where it is read")
				}
			}
			return true
		})
	}
}

func (c *sinkCheck) literal(lit *ast.CompositeLit) {
	name := sinkTypeName(c.info.TypeOf(lit))
	fields, ok := diagnosticFields[name]
	if !ok {
		return
	}
	shown := name
	if !strings.Contains(name, ".") {
		shown = "forgeapi." + name
	}
	for _, elt := range lit.Elts {
		kv, keyed := elt.(*ast.KeyValueExpr)
		if !keyed {
			c.report(lit, "a "+shown+" literal names its fields by position, so the check cannot tell a diagnostic from the rest; name each field")
			return
		}
		if key, ok := kv.Key.(*ast.Ident); ok && slices.Contains(fields, key.Name) {
			c.require(kv.Value, name+"."+key.Name)
		}
	}
}

func (c *sinkCheck) call(call *ast.CallExpr) {
	id := calleeIdent(call)
	if id == nil {
		return
	}
	if fun, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok {
		if sel := c.info.Selections[fun]; sel != nil && sel.Kind() == types.FieldVal && rootTypeName(sel.Recv()) == "Counters" {
			c.requireText(call, "a Counters label", "")
		}
	}
	fn, ok := c.info.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return
	}
	switch fn.Pkg().Path() {
	case "log/slog":
		c.requireText(call, "a log line's string", "a log line's value")
	case transportPath:
		who := "transport." + fn.Name()
		if named := namedOf(recvType(fn)); named != nil {
			who = named.Obj().Name() + "." + fn.Name()
		}
		for index, field := range transportSinks[who] {
			if index < len(call.Args) {
				c.require(call.Args[index], field+" through "+who)
			}
		}
	}
}

// requireText requires each string argument of call as text and, where value
// names a sink, each error, fmt.Stringer, empty-interface or spread argument as
// value.
func (c *sinkCheck) requireText(call *ast.CallExpr, text, value string) {
	for i, arg := range call.Args {
		t := c.info.TypeOf(arg)
		switch {
		case isString(t):
			c.require(arg, text)
		case value != "" && (call.Ellipsis.IsValid() && i == len(call.Args)-1 || carriesText(t)):
			c.require(arg, value)
		}
	}
}

var (
	errorType    = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)
	stringerType = types.NewInterfaceType([]*types.Func{types.NewFunc(token.NoPos, nil, "String",
		types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(types.NewVar(token.NoPos, nil, "", types.Typ[types.String])), false))}, nil).Complete()
)

func isString(t types.Type) bool {
	basic, ok := t.Underlying().(*types.Basic)
	return ok && basic.Info()&types.IsString != 0
}

// carriesText reports whether a log handler can print text from a value of type
// t: an error, a fmt.Stringer or a value of any type.
func carriesText(t types.Type) bool {
	if iface, ok := t.Underlying().(*types.Interface); ok && iface.Empty() {
		return true
	}
	return types.Implements(t, errorType) || types.Implements(t, stringerType)
}

// require reports sink where e is built from text no quoter saw.
func (c *sinkCheck) require(e ast.Expr, sink string) {
	if !c.safe(e) {
		c.report(e, sink+" is built from "+types.ExprString(e)+", which neither this library wrote nor a quoter saw; build it from constants, a transport.Quoted's String, or an answer's Quote")
	}
}

func (c *sinkCheck) report(n ast.Node, text string) {
	c.findings = append(c.findings, c.fset.Position(n.Pos()).String()+": "+text)
}

// safe reports whether e is text this library wrote or a quoter saw: a constant,
// nil, a number, a Quoted, or text built from those alone.
func (c *sinkCheck) safe(e ast.Expr) bool {
	if tv, ok := c.info.Types[e]; ok && (tv.Value != nil || tv.IsNil()) {
		return true
	}
	if t := c.info.TypeOf(e); t != nil && (isNumber(t) || isQuoted(t)) {
		return true
	}
	switch e := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		return true
	case *ast.BinaryExpr:
		return e.Op == token.ADD && c.safe(e.X) && c.safe(e.Y)
	case *ast.CallExpr:
		return c.safeCall(e)
	case *ast.CompositeLit:
		return c.safeElements(e)
	case *ast.IndexExpr:
		return c.safeIndex(e)
	case *ast.Ident:
		return c.safeVar(c.info.Uses[e])
	case *ast.SelectorExpr:
		if sel := c.info.Selections[e]; sel != nil {
			return sel.Kind() == types.FieldVal && (c.isDiagnosticField(e) || c.configuredName(selectedName(sel)) || c.safeVar(sel.Obj()))
		}
		return c.safeVar(c.info.Uses[e.Sel])
	}
	return false
}

// safeElements reports whether lit is a slice or array literal of safe elements.
func (c *sinkCheck) safeElements(lit *ast.CompositeLit) bool {
	switch c.info.TypeOf(lit).Underlying().(type) {
	case *types.Slice, *types.Array:
	default:
		return false
	}
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			elt = kv.Value
		}
		if !c.safe(elt) {
			return false
		}
	}
	return true
}

// safeIndex reports whether e reads a map variable the package only reads by
// index and gives only literals whose every value is safe; the key picks one of
// those values or the zero value, so it is not followed.
func (c *sinkCheck) safeIndex(e *ast.IndexExpr) bool {
	id := indexedIdent(e)
	v, ok := c.info.Uses[id].(*types.Var)
	if !ok || c.escaped[v] {
		return false
	}
	if _, ok := v.Type().Underlying().(*types.Map); !ok || len(c.given[v]) == 0 {
		return false
	}
	for _, s := range c.given[v] {
		lit, ok := ast.Unparen(s.expr).(*ast.CompositeLit)
		if !ok {
			return false
		}
		for _, elt := range lit.Elts {
			if !c.safe(elt.(*ast.KeyValueExpr).Value) {
				return false
			}
		}
	}
	return true
}

// isNumber reports whether t is a number or a boolean, unnamed or of a type the
// root package or the standard library declares, so it prints digits or one of
// that package's own names.
func isNumber(t types.Type) bool {
	basic, ok := t.Underlying().(*types.Basic)
	if !ok || basic.Info()&(types.IsNumeric|types.IsBoolean) == 0 {
		return false
	}
	named := namedOf(t)
	if named == nil || named.Obj().Pkg() == nil {
		return true
	}
	pkg := named.Obj().Pkg().Path()
	return pkg == modulePath || !strings.Contains(strings.SplitN(pkg, "/", 2)[0], ".")
}

func isQuoted(t types.Type) bool {
	named := namedOf(t)
	return named != nil && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == transportPath && named.Obj().Name() == "Quoted"
}

func (c *sinkCheck) safeCall(call *ast.CallExpr) bool {
	if tv := c.info.Types[call.Fun]; tv.IsType() {
		return len(call.Args) == 1 && c.safe(call.Args[0])
	}
	id := calleeIdent(call)
	if id == nil {
		return false
	}
	if c.configured(c.info.Uses[id]) {
		return true
	}
	if b, ok := c.info.Uses[id].(*types.Builtin); ok && b.Name() == "append" {
		return c.safeArgs(call.Args)
	}
	fn, ok := c.info.Uses[id].(*types.Func)
	if !ok {
		return false
	}
	if fn.Pkg() != nil && libraryText[fn.Pkg().Path()+"."+fn.Name()] {
		return true
	}
	if fn.Pkg() != nil && carried[fn.Pkg().Path()+"."+fn.Name()] {
		return c.safeArgs(call.Args)
	}
	if recv := fn.Signature().Recv(); recv != nil && fn.Name() == "String" {
		if named := namedOf(recv.Type()); named != nil && named.Obj().Pkg() != nil {
			switch pkg := named.Obj().Pkg().Path(); {
			case pkg == transportPath && named.Obj().Name() == "Quoted":
				return true
			case pkg == modulePath:
				basic, ok := named.Underlying().(*types.Basic)
				return ok && basic.Info()&types.IsInteger != 0
			}
		}
	}
	return c.safeResult(fn, 0)
}

// safeResult reports whether every return of the package's own fn gives safe
// text at result index; a function of another package is never followed, and a
// recursive call is refused, so a cycle whose returns the check cannot read
// fails closed.
func (c *sinkCheck) safeResult(fn types.Object, index int) bool {
	fd, ok := c.funcs[fn]
	if !ok || fd.Body == nil {
		return false
	}
	key := struct {
		fn    types.Object
		index int
	}{fn, index}
	if verdict, ok := c.verdicts[key]; ok {
		return verdict
	}
	mark := len(c.assumed)
	c.verdicts[key] = false
	verdict := true
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			switch {
			case len(n.Results) == 0:
				verdict = verdict && c.safeVar(fn.Type().(*types.Signature).Results().At(index))
			case len(n.Results) == 1 && fn.Type().(*types.Signature).Results().Len() > 1:
				verdict = verdict && c.safeSource(source{tuple: callOf(n.Results[0]), index: index})
			default:
				verdict = verdict && c.safe(n.Results[index])
			}
		}
		return true
	})
	c.settle(key, verdict, mark)
	return verdict
}

// safeVar reports whether every value v is given is safe: a constant, a local or
// package variable whose every assignment is, or a parameter of the package's own
// unexported function whose every call passes safe text. While it is followed, v
// is assumed safe, so a variable given itself holds only its other values.
func (c *sinkCheck) safeVar(obj types.Object) bool {
	switch obj := obj.(type) {
	case *types.Const:
		return true
	case *types.Var:
		if c.configured(obj) {
			return true
		}
		if verdict, ok := c.verdicts[obj]; ok {
			return verdict
		}
		mark := len(c.assumed)
		c.verdicts[obj] = true
		c.assuming++
		verdict := c.followVar(obj)
		c.assuming--
		c.settle(obj, verdict, mark)
		return verdict
	}
	return false
}

// settle stores key's verdict. A safe one reached while a variable is assumed safe
// holds only if that assumption does, so it is recorded in assumed; an unsafe one
// drops every safe verdict recorded since mark, each reached while key was being
// decided, so each is followed again. An unsafe verdict stands, since assuming a
// variable safe only admits more.
func (c *sinkCheck) settle(key any, verdict bool, mark int) {
	if !verdict {
		for _, k := range c.assumed[mark:] {
			delete(c.verdicts, k)
		}
		c.assumed = c.assumed[:mark]
	}
	c.verdicts[key] = verdict
	switch {
	case c.assuming == 0:
		c.assumed = c.assumed[:0]
	case verdict:
		c.assumed = append(c.assumed, key)
	}
}

func (c *sinkCheck) followVar(v *types.Var) bool {
	if p, ok := c.params[v]; ok {
		calls := c.calls[p.fn]
		if p.fn.Exported() || c.values[p.fn] || len(calls) == 0 {
			return false
		}
		for _, call := range calls {
			switch {
			case p.variadic && p.index <= len(call.Args):
				if !c.safeArgs(call.Args[p.index:]) {
					return false
				}
			case p.variadic || p.index >= len(call.Args) || !c.safe(call.Args[p.index]):
				return false
			}
		}
		return true
	}
	if v.IsField() && (v.Exported() || c.owners[v] == "") {
		return false
	}
	given := c.given[v]
	if len(given) == 0 {
		return false
	}
	for _, s := range given {
		if !c.safeSource(s) {
			return false
		}
	}
	return true
}

func (c *sinkCheck) safeSource(s source) bool {
	switch {
	case s.expr != nil:
		return c.safe(s.expr)
	case s.elem != nil:
		return c.safeSlice(s.elem)
	case s.tuple != nil:
		id := calleeIdent(s.tuple)
		if id == nil {
			return false
		}
		return c.configured(c.info.Uses[id]) || c.safeResult(c.info.Uses[id], s.index)
	}
	return false
}

// safeSlice reports whether every element of e, a slice, is safe.
func (c *sinkCheck) safeSlice(e ast.Expr) bool {
	_, ok := c.info.TypeOf(e).Underlying().(*types.Slice)
	return ok && c.safe(e)
}

func (c *sinkCheck) safeArgs(args []ast.Expr) bool {
	for _, arg := range args {
		if !c.safe(arg) {
			return false
		}
	}
	return true
}

// isDiagnosticField reports whether sel selects a [diagnosticFields] field.
func (c *sinkCheck) isDiagnosticField(sel *ast.SelectorExpr) bool {
	s := c.info.Selections[sel]
	if s == nil || s.Kind() != types.FieldVal {
		return false
	}
	return slices.Contains(diagnosticFields[sinkTypeName(s.Recv())], s.Obj().Name())
}

// sinkTypeName is [rootTypeName], or a transport type's name qualified by its
// package.
func sinkTypeName(t types.Type) string {
	if named := namedOf(t); named != nil && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == transportPath {
		return "transport." + named.Obj().Name()
	}
	return rootTypeName(t)
}

// rootTypeName is the name of t, through a pointer, where it is a type of the
// module's root package, and empty otherwise.
func rootTypeName(t types.Type) string {
	named := namedOf(t)
	if named == nil || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != modulePath {
		return ""
	}
	return named.Obj().Name()
}

func namedOf(t types.Type) *types.Named {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, _ := types.Unalias(t).(*types.Named)
	return named
}

// calleeIdent is the identifier naming a call's function or method, nil where
// the call is of a value the check cannot name.
func calleeIdent(call *ast.CallExpr) *ast.Ident {
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		return fun
	case *ast.SelectorExpr:
		return fun.Sel
	case *ast.IndexExpr:
		if id, ok := fun.X.(*ast.Ident); ok {
			return id
		}
	}
	return nil
}

func callOf(e ast.Expr) *ast.CallExpr {
	call, _ := ast.Unparen(e).(*ast.CallExpr)
	return call
}
