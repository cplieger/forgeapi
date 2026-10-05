package forgeapi_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// workingDocumentTokens are the spellings of documents and directories that do NOT
// ship with this library: the record this library was built from, its decision
// files, the directory they live in, the recordings the fixtures were cut from, the
// plans, the investigation and review records, and the running log of the work.
// They are STRING LITERALS here, which is the one place
// in the module they may appear, because a test that names them in a comment would
// be the thing it refuses.
//
// The last two entries are DESCRIPTIONS rather than names, and they are here because
// a description resolves to nothing for a reader exactly as a filename does, while
// being the substitution a reworder reaches for first. No published sentence in this
// tree needs either phrase: what a comment owes its reader is the fact, not where it
// was settled.
//
// The reason the rule is worth a gate rather than a habit: everything published here
// is read by someone who has only this repository, so a comment resolving to a file
// they do not have is a dead end, and a recorded body whose provenance names an
// unpublished path cannot be traced by anyone at all.
var workingDocumentTokens = []string{
	"ARCHITECTURE" + ".md",
	"DESIGN" + ".md",
	"ADR-00",
	"_scratch",
	"forge-audit",
	"spike/",
	"swagger/",
	"fixes" + ".md",
	"plan-" + "cutover",
	"PLAN" + ".md",
	"investigate" + "-",
	"review-" + "phase",
	"history/",
	"the " + "design",
	"the " + "spike",
}

// TestNoPublishedCommentPointsAtAWorkingDocument holds EVERY file the module ships
// to the rule above, whatever its extension.
//
// The reach is the whole shipped tree rather than the Go and Markdown subset, because the
// rule is about what a reader meets and a reader meets the workflow YAML, the golden
// surface file, the lint configuration and the recorded bodies too. A gate over the
// two extensions a maintainer thinks of as published leaves every other file free to
// carry the pointer, and the next file added to the tree is by default one of those.
//
// A Go file is held at its comments AND at its string literals, because a pointer in
// an error message or a log attribute is met by a reader exactly as a comment is, and
// this library's errors carry prose a consumer reads. THIS file is the one exemption,
// and only for its literals: the token list above has to be declarable somewhere. A
// non-Go file has no comment syntax this test could agree on, so its whole content is
// what a reader meets.
func TestNoPublishedCommentPointsAtAWorkingDocument(t *testing.T) {
	root := moduleRoot(t)
	goFiles, otherFiles := 0, 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if unshipped(root, path, d) {
			return fs.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".go" {
			goFiles++
			checkGo(t, root, path)
			return nil
		}
		otherFiles++
		checkText(t, root, path)
		return nil
	})
	if err != nil {
		t.Fatalf("Setup: walking %s: %v", root, err)
	}
	if goFiles == 0 {
		t.Fatal("the walk read no Go file, want the module's own: a gate over nothing passes for the wrong reason")
	}
	if otherFiles == 0 {
		t.Fatal("the walk read no file outside Go, want the module's Markdown, YAML, golden and recorded files: the widened reach is what this gate is for")
	}
}

// recordedDate is the date a recording was read, which every provenance line carries
// whatever verb it names the reading with.
var recordedDate = regexp.MustCompile(`20[0-9]{2}-[0-9]{2}-[0-9]{2}`)

// TestEveryRecordedBodyNamesItsRecordingWithoutAPath holds the provenance line of
// every recorded body this module ships, the conformance fixtures and each family's
// own captures alike: it names the recording and the date it was read, and it names
// no path, because a path outside this repository resolves to nothing for a reader.
func TestEveryRecordedBodyNamesItsRecordingWithoutAPath(t *testing.T) {
	root := moduleRoot(t)
	found := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if unshipped(root, path, d) {
			return fs.SkipDir
		}
		if d.IsDir() || filepath.Ext(path) != ".json" || !strings.Contains(path, "testdata") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var doc struct {
			Provenance string `json:"//"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil
		}
		if doc.Provenance == "" {
			return nil
		}
		found++
		rel := relative(root, path)
		if !recordedDate.MatchString(doc.Provenance) {
			t.Errorf("%s provenance %q names no date the recording was read on, want one", rel, doc.Provenance)
		}
		if strings.Contains(doc.Provenance, "/") {
			t.Errorf("%s provenance %q carries a path, want the recording named: a reader of this repository cannot resolve one", rel, doc.Provenance)
		}
		for _, token := range workingDocumentTokens {
			if strings.Contains(doc.Provenance, token) {
				t.Errorf("%s provenance %q names %q, which does not ship with this library", rel, doc.Provenance, token)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Setup: walking %s: %v", root, err)
	}
	if found == 0 {
		t.Fatal("the walk read no recorded body, want this module's own: a gate over nothing passes for the wrong reason")
	}
}

// checkGo holds one Go file's comments, the doc comments and the ordinary ones both,
// and its string literals, since the rule is about what a reader meets rather than
// about where it sits. This file's own literals are skipped, and nothing else's are:
// the list has to be declarable in one place.
func checkGo(t *testing.T, root, path string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Errorf("Setup: parsing %s: %v", relative(root, path), err)
		return
	}
	rel := relative(root, path)
	for _, group := range file.Comments {
		for _, line := range group.List {
			for _, name := range workingDocumentTokens {
				if strings.Contains(line.Text, name) {
					t.Errorf("%s:%d names %q in a comment, and that does not ship with this library: state the fact the comment needs instead",
						rel, fset.Position(line.Pos()).Line, name)
				}
			}
		}
	}
	if filepath.Base(path) == "publication_test.go" {
		return
	}
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		for _, name := range workingDocumentTokens {
			if strings.Contains(lit.Value, name) {
				t.Errorf("%s:%d names %q in a string a consumer can read, and that does not ship with this library: state the fact the string needs instead",
					rel, fset.Position(lit.Pos()).Line, name)
			}
		}
		return true
	})
}

// checkText holds one published file that is not Go source, whose whole content is
// what a reader meets: the Markdown, the workflow YAML, the lint configuration, the
// golden surface file and every recorded body.
func checkText(t *testing.T, root, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("Setup: reading %s: %v", relative(root, path), err)
		return
	}
	for _, token := range workingDocumentTokens {
		if strings.Contains(string(raw), token) {
			t.Errorf("%s names %q, and that does not ship with this library", relative(root, path), token)
		}
	}
}

// unshipped reports whether the walk has reached a directory this repository does
// not publish. A dot-directory below the root holds version-control state, tool
// caches or local scratch, none of which a reader of the repository receives; .github
// is the exception, because its workflows ship. testdata ships: it holds the golden
// surface file and the recorded bodies.
func unshipped(root, path string, d fs.DirEntry) bool {
	if !d.IsDir() || path == root {
		return false
	}
	name := d.Name()
	return strings.HasPrefix(name, ".") && name != ".github"
}

// moduleRoot is the directory holding go.mod, which is where the root package's own
// tests run.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("Setup: resolving the module root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("Setup: %s holds no go.mod, want the module root: %v", root, err)
	}
	return root
}

// relative is a path as a reader of this repository names it.
func relative(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}
