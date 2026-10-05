package forgeapi_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/internal/surface"
)

const (
	goldenPath = "api/forgeapi.txt"
	regenerate = "UPDATE_GOLDEN=1 go test ./... -run TestExportedSurfaceMatchesGolden"
)

// TestExportedSurfaceMatchesGolden regenerates the surface from the current
// source and fails on any drift, so a change to an exported declaration cannot
// land without the golden file moving with it.
//
// The golden file is the reviewed assertion and its diff is the review surface:
// a normal run never writes it.
func TestExportedSurfaceMatchesGolden(t *testing.T) {
	got, err := surface.Generate(".")
	if err != nil {
		t.Fatalf("Setup: surface.Generate(%q): %v", ".", err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, got, 0o600); err != nil {
			t.Fatalf("Setup: writing %s: %v", goldenPath, err)
		}
		// A regenerating run FAILS, deliberately: it wrote the file it is
		// supposed to be comparing against, so reporting success would make
		// the gate disarmable by one environment variable, and a run with
		// UPDATE_GOLDEN set anywhere in the environment would be
		// indistinguishable from a surface that had not drifted.
		t.Fatalf("regenerated %s, %d bytes: review the diff and re-run without UPDATE_GOLDEN to gate the surface", goldenPath, len(got))
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("Setup: reading %s (regenerate with %s): %v", goldenPath, regenerate, err)
	}
	if bytes.Equal(got, want) {
		return
	}
	line, gotLine, wantLine := firstDifference(string(got), string(want))
	t.Errorf("surface.Generate(%q) differs from %s at line %d:\n  got:  %s\n  want: %s\nregenerate with: %s",
		".", goldenPath, line, gotLine, wantLine, regenerate)
}

// TestSurfaceIsNotVacuous pins a handful of declarations the generator must
// find, so a generator that silently stopped emitting cannot pass by matching an
// equally empty golden file. Every package a consumer can import is named here,
// which is what makes "the golden covers the whole surface" a checked claim
// rather than an incidental property of a tree walk.
func TestSurfaceIsNotVacuous(t *testing.T) {
	got, err := surface.Generate(".")
	if err != nil {
		t.Fatalf("Setup: surface.Generate(%q): %v", ".", err)
	}
	rendered := string(got)
	for _, want := range []string{
		"## package forgeapi (./.)",
		"## package creds (./creds)",
		"## package families (./families)",
		"## package gitcred (./gitcred)",
		"## package gitea (./gitea)",
		"## package github (./github)",
		"## package gitlab (./gitlab)",
		"type Core interface {",
		"type Support int",
		"const SupportUnknown Support = iota",
		"const SupportNo Support = iota + 2",
		"type Page[T any] struct {",
		"func DecodeRepoRef(id string, family Family) (RepoRef, error)",
		"func (r RepoRef) Encode() string",
		"func WithRotationCursor(c RotationCursor) Option",
		"\tRotationCursor RotationCursor\n",
		// The two fields CodeCapabilityUnsupported's payload is made of. They
		// are named here because that refusal is ruled to CARRY the
		// capability and the evidence: a struct that lost them
		// would still satisfy every other test on this surface, and the
		// refusal would degrade to a bare code with the fact in prose.
		"\tCapability Capability\n",
		"\tEvidence Evidence\n",
		"func DefaultBudget() Budget",
		"\tHeaders []Header\n",
		"func ReservedHeaders() []string",
		"MergePR(ctx context.Context, repo RepoRef, pr PRRef, req MergeRequest) (MergeOutcome, error)",
		"func Open(ctx context.Context, conn forgeapi.Connection, opts ...forgeapi.Option) (forgeapi.Core, forgeapi.Family, error)",
		"func (c *Client) MergePR(ctx context.Context, repo forgeapi.RepoRef, pr forgeapi.PRRef, req forgeapi.MergeRequest) (forgeapi.MergeOutcome, error)",
		"func NewSource(store Store, key string, conn forgeapi.Connection, opts ...forgeapi.Option) (*Source, error)",
		"func New(store creds.Store, opts ...forgeapi.Option) *Helper",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("surface.Generate(%q) does not render %q", ".", want)
		}
	}
}

// TestFamilyPackagesExportNoTypeButTheClient is the mechanical form of the rule
// that a family's own types stay inside it: the normalizers, the wire shapes and
// the family's own vocabulary are facts about one upstream API, and the day one of
// them is exported it becomes something a consumer can name and this library has
// to keep.
//
// The complementary half needs no test: the root package cannot import a family
// package without a cycle, so no family type can reach the root's surface at all.
func TestFamilyPackagesExportNoTypeButTheClient(t *testing.T) {
	got, err := surface.Generate(".")
	if err != nil {
		t.Fatalf("Setup: surface.Generate(%q): %v", ".", err)
	}
	for _, pkg := range []string{"github", "gitlab", "gitea"} {
		section := packageSection(string(got), pkg)
		if section == "" {
			t.Errorf("surface.Generate(%q) renders no section for package %s", ".", pkg)
			continue
		}
		for line := range strings.SplitSeq(section, "\n") {
			name, ok := typeName(line)
			if !ok {
				continue
			}
			if name != "Client" {
				t.Errorf("package %s declares exported type %q, want no exported type but Client: a family's own types stay inside the family package", pkg, name)
			}
		}
	}
}

// typeName returns the type name a rendered declaration line declares, and
// reports whether the line declares one at all.
//
// It reads the identifier rather than comparing the whole line, because the
// rendering of one type is not stable under a change that is nobody's business
// here: a struct with no members renders as "type Client struct{}" and one with
// unexported members as "type Client struct {", so a line comparison would
// accuse a family package of exporting some other type on the day Client's last
// field moves into a shared core.
func typeName(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "type ")
	if !ok {
		return "", false
	}
	name := rest
	if i := strings.IndexAny(name, " [{="); i >= 0 {
		name = name[:i]
	}
	return name, name != ""
}

// TestNoExportedTypeEmbedsAnUnrenderedType closes the one drift class the golden
// file cannot see on its own.
//
// An embedded type promotes ITS exported methods onto the embedding type, and the
// generator renders declarations, so where the embedded type's own methods are
// rendered nowhere they reach a consumer while every declaration in the golden
// stays byte for byte the same. Both shapes are measured. An unexported core
// carrying RawDo left the golden identical and all gates green, which is how the
// Raw() escape hatch this design forbids would arrive; and an EXPORTED type in an
// internal package did the same, which is the shape a transport core shared by
// three family packages has to take, since it must be nameable there. The
// interface half is the sharpest of the three: a method added to an interface a
// consumer implements breaks every implementer, and an unrendered embed is the one
// way that addition leaves no trace here.
//
// The generator renders such an embed as its own line, so its arrival is drift;
// this names the failure rather than leaving a reader to interpret the diff. When
// a shared core does land, the promoted surface is what has to be reviewed here,
// not the line.
func TestNoExportedTypeEmbedsAnUnrenderedType(t *testing.T) {
	got, err := surface.Generate(".")
	if err != nil {
		t.Fatalf("Setup: surface.Generate(%q): %v", ".", err)
	}
	for line := range strings.SplitSeq(string(got), "\n") {
		trimmed := strings.TrimPrefix(line, "\t")
		if strings.HasPrefix(trimmed, "// embeds ") {
			t.Errorf("surface.Generate(%q) renders %q: an exported type embeds a type whose own methods this generator does not render, so a method added to it promotes onto the surface with the golden unchanged", ".", trimmed)
		}
	}
}

// packageSection returns the rendered surface of one package, empty when the
// rendering holds no section for it.
func packageSection(rendered, pkg string) string {
	head := "\n## package " + pkg + " (./" + pkg + ")\n"
	_, rest, found := strings.Cut(rendered, head)
	if !found {
		return ""
	}
	if section, _, more := strings.Cut(rest, "\n## package "); more {
		return section
	}
	return rest
}

// TestRuledDefaultsAreStated pins the doc-comment obligations the golden file
// cannot see, because the generator renders declarations and never doc comments.
//
// A default is one of the few things a 1.0 can neither loosen nor tighten
// afterwards, and every row here states one the surface publishes: editing
// "Default: true" to say the opposite changes no declaration, so without this
// test each obligation rests on a review that happens once. The two
// security-shaped defaults are here for the same reason as the mutations gate,
// and that gate carries a second row because an option documented as advisory is
// a different contract from one enforced at the operation boundary.
//
// The population is EVERY option that has a ruled default, which since the one
// configuration door is the nine budget knobs plus the reserve, the rotation
// cursor, the two per-connection postures, the mutations gate, the credential
// source, the refresh lead and the page bound. Each of the nine numbers is one option whose absence
// means its Default* constant and whose zero means zero, so each owes a clause of
// its own here: nine clauses over one door is the accepted cost of an options-only
// surface, and this is the test that holds them. WithCredentialSource's is the default
// that decides whether a client with no credential connects at all, which the exported
// surface makes mandatory and answers with a local-refusal code.
//
// The rows hold three obligations and the difference is worth naming. Some state
// their default in PROSE ALONE, so the prose is the only thing holding it. Most
// name a constant instead, and that constant's VALUE is already in the golden
// file, so what those rows pin is not the value but WHICH constant the doc names:
// rewriting "Default: [DefaultPageBound]." to "Default: the first page." changes
// no declaration and leaves the golden byte-identical. And the nine knobs plus
// the reserve additionally owe a sentence saying what a ZERO passed to them
// means, because the one-door rule turns on that zero being the literal zero and
// nothing in a declaration says so; zeroStated is that row. WithCredentialSource's
// and WithRotationCursor's clauses WRAP mid-sentence, so those rows pin the value
// and the punctuation that follows it rather than the wrapped prose, which is what
// keeps them from going red on a re-flow that changes nothing and from passing a
// bare "Default: none." should the clause ever be shortened.
//
// The zero clause is held as its own EXPECTED TEXT per row rather than as the
// presence of the token "Zero ". Presence is satisfied by a doc stating the
// opposite of what is ruled: measured, rewriting WithRetries' clause to
// "Zero is refused with [CodeBudgetInvalid]" left its Default: clause intact and
// this test green, which is the whole obligation the row exists to hold. So
// zeroClause names the ruled meaning, and a row with no clause is one of the six
// options whose zero nothing rules.
//
// The default is held as the OPERATIVE statement rather than as a substring
// anywhere in the doc: "Default:" appears exactly once, opening a line, and that
// line opens with the ruled value. A substring search accepts a doc that states the
// wrong default first and keeps the ruled phrase as history, which is the shape a
// contradictory edit takes; measured, "Default: false; prior docs said Default:
// true." passed, and so did "Default: false. Default: true." on one line under a
// count of lines alone. The enforcement-code names stay substring checks, since a
// code is named rather than asserted.
func TestRuledDefaultsAreStated(t *testing.T) {
	const file = "options.go"
	const defaultPrefix = "Default:"
	for _, test := range []struct {
		fn            string
		defaultClause string
		zeroClause    string
		names         []string
	}{
		{fn: "WithMutations", defaultClause: "Default: true.", names: []string{"CodeMutationsDisabled"}},
		{fn: "WithPrivateAddresses", defaultClause: "Default: false."},
		{fn: "WithPlaintextHTTP", defaultClause: "Default: false."},
		{fn: "WithListPages", defaultClause: "Default: [DefaultListPages].", zeroClause: "Zero traverses none."},
		{fn: "WithStatusPages", defaultClause: "Default: [DefaultStatusPages].", zeroClause: "Zero traverses none."},
		{fn: "WithStatusReadsPerInterval", defaultClause: "Default: [DefaultStatusReadsPerInterval].", zeroClause: "Zero issues none,"},
		{fn: "WithRetries", defaultClause: "Default: [DefaultRetries].", zeroClause: "Zero means no retries,"},
		{fn: "WithReadConcurrency", defaultClause: "Default: [DefaultReadConcurrency].", zeroClause: "Zero admits no read at all, so it is REFUSED", names: []string{"CodeBudgetInvalid"}},
		{fn: "WithMutationConcurrency", defaultClause: "Default: [DefaultMutationConcurrency].", zeroClause: "Zero admits no mutation at all, so it is REFUSED", names: []string{"CodeBudgetInvalid"}},
		{fn: "WithOperationTimeout", defaultClause: "Default: [DefaultOperationTimeout].", zeroClause: "Zero is a deadline that has already passed."},
		{fn: "WithStatusTimePerInterval", defaultClause: "Default: [DefaultStatusTimePerInterval].", zeroClause: "Zero allows none."},
		{fn: "WithMutationInterval", defaultClause: "Default: [DefaultMutationInterval].", zeroClause: "Zero leaves no gap,"},
		{fn: "WithMutationReserve", defaultClause: "Default: [DefaultMutationReserve].", zeroClause: "Zero holds nothing back,", names: []string{"CodeBudgetInvalid"}},
		{fn: "WithRotationCursor", defaultClause: "Default: the empty cursor,", names: []string{"CodeCursorInvalid"}},
		{fn: "WithCredentialSource", defaultClause: "Default: none,"},
		{fn: "WithRefreshLead", defaultClause: "Default: [DefaultRefreshLead].", zeroClause: "Zero refreshes a token only once it has expired.", names: []string{"CodeBudgetInvalid"}},
		{fn: "WithPageBound", defaultClause: "Default: [DefaultPageBound].", names: []string{"CodePageBoundInvalid"}},
	} {
		t.Run(test.fn, func(t *testing.T) {
			doc := funcDoc(t, file, test.fn)
			var stated []string
			for line := range strings.SplitSeq(doc, "\n") {
				if strings.HasPrefix(line, defaultPrefix) {
					stated = append(stated, line)
				}
			}
			if n := strings.Count(doc, defaultPrefix); len(stated) != 1 || n != 1 {
				t.Fatalf("funcDoc(%q, %q) states %q %d time(s), %d of them opening a line, want exactly once, opening a line and stating %q: a second one is a contradiction a reader resolves by guessing", file, test.fn, defaultPrefix, n, len(stated), test.defaultClause)
			}
			if !strings.HasPrefix(stated[0], test.defaultClause) {
				t.Errorf("funcDoc(%q, %q) states %q, want it to open with %q: this default cannot be changed compatibly after 1.0", file, test.fn, stated[0], test.defaultClause)
			}
			for _, want := range test.names {
				if !strings.Contains(doc, want) {
					t.Errorf("funcDoc(%q, %q) does not contain %q, want it stated: the surface file cannot pin a doc comment, so this assertion is what holds it", file, test.fn, want)
				}
			}
			// The zero clause is looked for in the doc with its wrapping
			// collapsed, because "Zero" falls at the end of a line in some of
			// these comments and a re-flow moving it must not turn this red.
			// What is compared is the ruled MEANING and not the token, so a
			// clause stating the opposite fails rather than passing.
			if test.zeroClause != "" && !strings.Contains(strings.Join(strings.Fields(doc), " "), test.zeroClause) {
				t.Errorf("funcDoc(%q, %q) does not state %q, want that clause: with one configuration door a zero is the literal zero with this option's own meaning, and no declaration can say either", file, test.fn, test.zeroClause)
			}
		})
	}
}

// funcDoc returns the doc comment of one top-level function in a file of this
// package.
func funcDoc(t *testing.T, file, name string) string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("Setup: parsing %s: %v", file, err)
	}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == name {
			return fn.Doc.Text()
		}
	}
	t.Fatalf("Setup: %s declares no func %s", file, name)
	return ""
}

// TestSurfaceExcludesInternal keeps the pin to what a consumer can import: a
// change under internal/ breaks nobody, so it must not show up as surface drift.
func TestSurfaceExcludesInternal(t *testing.T) {
	got, err := surface.Generate(".")
	if err != nil {
		t.Fatalf("Setup: surface.Generate(%q): %v", ".", err)
	}
	if strings.Contains(string(got), "## package surface") {
		t.Errorf("surface.Generate(%q) rendered the internal generator package, which is not part of the surface", ".")
	}
}

// firstDifference reports the 1-based line number where two renderings first
// disagree, with both lines.
func firstDifference(got, want string) (int, string, string) {
	gotLines := strings.Split(got, "\n")
	wantLines := strings.Split(want, "\n")
	for i := range max(len(gotLines), len(wantLines)) {
		g, w := at(gotLines, i), at(wantLines, i)
		if g != w {
			return i + 1, g, w
		}
	}
	return 0, "", ""
}

func at(lines []string, i int) string {
	if i >= len(lines) {
		return "<end of file>"
	}
	return lines[i]
}
