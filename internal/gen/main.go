// Command gen renders everything forgeapi publishes about per-product behaviour
// from the expectation table, and nothing else states those facts.
//
// Two outputs. It replaces the per-forge block of every role method's godoc in
// roles.go, and it writes SUPPORT.md, the operation-by-product support matrix.
// Run it through go generate from the module root; gen_test.go fails when either
// output would move, which is the stale-output gate.
//
// A method's block is the span from the "Per forge:" line to the end of that doc
// comment, and the span is this command's alone: everything above the marker is
// written by hand and is never touched.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/cplieger/forgeapi/internal/spec"
)

const (
	rolesFile   = "roles.go"
	supportFile = "SUPPORT.md"

	// marker opens the generated span of a method's doc comment. It is matched
	// on a whole line, so a mention of it in prose above cannot open a span.
	marker = "\t// Per forge:"

	// comment is the prefix of every line of an interface method's doc comment,
	// which is what ends the span: the first line without it is the signature.
	comment = "\t//"
)

// interfaceLine and signature read roles.go's shape. A role is one interface
// declaration and every method of it sits at one tab.
var (
	interfaceLine = regexp.MustCompile(`^type ([A-Z]\w*) interface \{$`)
	signature     = regexp.MustCompile(`^\t(\w+)\(`)
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gen: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	current, err := os.ReadFile(filepath.Join(root, rolesFile))
	if err != nil {
		return err
	}
	roles, err := renderRoles(current)
	if err != nil {
		return err
	}
	if err := write(filepath.Join(root, rolesFile), roles); err != nil {
		return err
	}
	return write(filepath.Join(root, supportFile), renderSupport())
}

// write leaves a file whose content already matches untouched, so a generate run
// over an up-to-date tree changes no timestamp.
func write(path string, want []byte) error {
	got, err := os.ReadFile(path)
	if err == nil && bytes.Equal(got, want) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(path, want, 0o600)
}

// moduleRoot walks up from the working directory to the directory holding go.mod,
// so the command runs the same under go generate at the root and under go test in
// this package.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the working directory")
		}
		dir = parent
	}
}

// renderRoles replaces every method's generated span in src and returns the file.
//
// It fails rather than skipping when the file and the table disagree about which
// methods exist, in either direction: a method the table does not cover would
// publish nothing about its products, and an entry no method carries would be a
// row nobody reads.
func renderRoles(src []byte) ([]byte, error) {
	lines := strings.SplitAfter(string(src), "\n")
	var out strings.Builder
	role := ""
	seen := map[string]bool{}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if found := interfaceLine.FindStringSubmatch(strings.TrimSuffix(line, "\n")); found != nil {
			role = found[1]
		}
		if strings.TrimSuffix(line, "\n") != marker {
			out.WriteString(line)
			continue
		}
		end := i
		for end < len(lines) && strings.HasPrefix(lines[end], comment) {
			end++
		}
		if end >= len(lines) {
			return nil, fmt.Errorf("%s: a %q block at the end of the file", rolesFile, marker)
		}
		found := signature.FindStringSubmatch(lines[end])
		if found == nil {
			return nil, fmt.Errorf("%s: the block above %q opens no method", rolesFile, strings.TrimSpace(lines[end]))
		}
		method := role + "." + found[1]
		block, err := block(method)
		if err != nil {
			return nil, err
		}
		out.WriteString(block)
		seen[method] = true
		i = end - 1
	}
	for _, method := range methods() {
		if !seen[method] {
			return nil, fmt.Errorf("%s: no %q block for %s", rolesFile, marker, method)
		}
	}
	return []byte(out.String()), nil
}

// methods lists the table's operations once, in table order.
func methods() []string {
	var out []string
	for _, entry := range spec.Table {
		if !slices.Contains(out, entry.Method) {
			out = append(out, entry.Method)
		}
	}
	return out
}

// entries returns one method's four entries, in the product column order.
func entries(method string) ([]spec.Entry, error) {
	out := make([]spec.Entry, 0, len(spec.Products))
	for _, product := range spec.Products {
		found := false
		for _, entry := range spec.Table {
			if entry.Method == method && entry.Product == product {
				out = append(out, entry)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("the table has no %s entry for %s", product, method)
		}
	}
	return out, nil
}

// block renders one method's generated span: the four exercise rows, then one
// departure list per product that has departures, products with identical lists
// folded into one heading.
func block(method string) (string, error) {
	found, err := entries(method)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString(marker + "\n")
	out.WriteString(comment + "\n")
	width := 0
	for _, product := range spec.Products {
		width = max(width, len(product))
	}
	for _, entry := range found {
		out.WriteString(fmt.Sprintf("%s\t%-*s  %s\n", comment, width, entry.Product, exercises(entry)))
	}
	group(&out, found, departures, func(names []string) string {
		return fmt.Sprintf("%s %s %s from the normalized contract:\n", comment, join(names), verb(len(names)))
	})
	group(&out, found, sends, func(names []string) string {
		return fmt.Sprintf("%s %s %s this, where a wrong default would change the request silently:\n",
			comment, join(names), sendVerb(len(names)))
	})
	return out.String(), nil
}

// group writes one section per run of products whose rows are identical, so a fact
// four products share is stated once and a fact one product has is stated where it
// belongs. A product whose rows are empty opens no section.
func group(out *strings.Builder, found []spec.Entry, rows func(*spec.Entry) string, heading func([]string) string) {
	for start := 0; start < len(found); {
		block := rows(&found[start])
		if block == "" {
			start++
			continue
		}
		end := start + 1
		for end < len(found) && rows(&found[end]) == block {
			end++
		}
		names := make([]string, 0, end-start)
		for _, entry := range found[start:end] {
			names = append(names, string(entry.Product))
		}
		out.WriteString(comment + "\n")
		out.WriteString(heading(names))
		out.WriteString(comment + "\n")
		out.WriteString(block)
		start = end
	}
}

// sends renders one product's request facts, or the empty string where this
// operation's request on that product carries no decision a consumer relies on.
//
// The part of the request each fact rides is stated beside it, because a filter a
// product takes in a query and a field another takes in a body are the same decision
// in two places, and a consumer reading only the body would miss one.
func sends(entry *spec.Entry) string {
	if len(entry.Sends) == 0 {
		return ""
	}
	field, where := 0, 0
	for _, row := range entry.Sends {
		field = max(field, len(row.Field))
		where = max(where, len(row.Where))
	}
	var out strings.Builder
	for _, row := range entry.Sends {
		out.WriteString(fmt.Sprintf("%s\t%-*s  %-*s  %s\n", comment, field, row.Field, where, row.Where, row.Says))
	}
	return out.String()
}

func sendVerb(count int) string {
	if count == 1 {
		return "sends"
	}
	return "send"
}

// exercises is one product's row: what the call reaches there, what it costs, and
// the capability a detection can refuse it on.
//
// An entry with no route is one of two things and they read differently: a product
// that carries no such verb, where the capability refuses the call and a departure
// row of that entry says what is absent, and a route nothing has settled yet,
// where the row says that rather than naming a plausible one.
func exercises(entry spec.Entry) string {
	switch {
	case entry.Exercises != "":
		return entry.Exercises + ", " + price(entry.Requests) + needs(entry)
	case entry.Support == spec.Unsupported:
		return "no route: this product carries no such verb, so the call is refused" + needs(entry)
	default:
		return "no route settled yet, so no price" + needs(entry)
	}
}

// price states a call's requests. A range reads as one, because the arms are a
// degradation path or a fold's pages rather than an estimate, and a paged figure
// says so, because what a whole list costs is that figure times the pages taken.
func price(requests spec.Requests) string {
	var out string
	switch {
	case requests.Max == 0:
		out = "no request"
	case requests.Min != requests.Max && requests.Max == 1:
		out = fmt.Sprintf("%d to 1 request", requests.Min)
	case requests.Min != requests.Max:
		out = fmt.Sprintf("%d to %d requests", requests.Min, requests.Max)
	case requests.Min == 1:
		out = "1 request"
	default:
		out = fmt.Sprintf("%d requests", requests.Min)
	}
	if requests.Paged {
		out += " per page"
	}
	switch {
	case requests.PerItem == 1:
		out += " plus one per row"
	case requests.PerItem > 1:
		out += fmt.Sprintf(" plus %d per row", requests.PerItem)
	}
	switch {
	case requests.PerItemCeiling == 1:
		out += " plus up to one per row"
	case requests.PerItemCeiling > 1:
		out += fmt.Sprintf(" plus up to %d per row", requests.PerItemCeiling)
	}
	return out
}

func needs(entry spec.Entry) string {
	if entry.Needs == "" {
		return ""
	}
	return "; needs " + entry.Needs
}

// departures renders one product's departure rows, or the empty string where the
// product delivers every field as the contract states it.
//
// Runs of fields that depart for the SAME stated reason fold into one row, and a
// reason covering every field the operation publishes reads as that, which is
// what keeps a wholly unmeasured column to one line instead of two dozen.
func departures(entry *spec.Entry) string {
	if len(entry.Departures) == 0 {
		return ""
	}
	type group struct {
		kind   spec.Kind
		says   string
		fields []string
	}
	var groups []group
	for _, row := range entry.Departures {
		if last := len(groups) - 1; last >= 0 && groups[last].kind == row.Kind && groups[last].says == row.Says {
			groups[last].fields = append(groups[last].fields, row.Field)
			continue
		}
		groups = append(groups, group{kind: row.Kind, says: row.Says, fields: []string{row.Field}})
	}
	subjects := make([]string, len(groups))
	for i, g := range groups {
		subjects[i] = list(g.fields)
	}
	if len(groups) == 1 && len(groups[0].fields) == spec.Fields[entry.Method] {
		subjects[0] = "every field"
	}
	subject, kind := 0, 0
	for i, g := range groups {
		subject = max(subject, len(subjects[i]))
		kind = max(kind, len(g.kind))
	}
	var out strings.Builder
	for i, g := range groups {
		out.WriteString(fmt.Sprintf("%s\t%-*s  %-*s  %s\n", comment, subject, subjects[i], kind, g.kind, g.says))
	}
	return out.String()
}

// listed bounds how many field names one departure row spells out. A column
// nobody has measured departs on two dozen fields for one reason, and spelling
// them all puts a 500-character line in a doc comment for no reader's benefit.
const listed = 4

// list names a group's fields, the first few and a count of the rest.
func list(fields []string) string {
	if len(fields) <= listed {
		return strings.Join(fields, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(fields[:listed], ", "), len(fields)-listed)
}

func join(names []string) string {
	switch len(names) {
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

func verb(count int) string {
	if count == 1 {
		return "departs"
	}
	return "depart"
}

// renderSupport writes the matrix a reader opens first: one row per operation,
// one column per product, each cell a support status.
func renderSupport() []byte {
	var out strings.Builder
	out.WriteString("# Support matrix\n\n")
	out.WriteString("<!-- Generated from internal/spec by internal/gen. Edit the table, then run go generate ./... -->\n\n")
	out.WriteString("This table has one row per operation and one column per forge product. ")
	out.WriteString("Each operation returns the same types on every product, and what those types promise is the normalized contract.\n\n")
	out.WriteString("`pending` says the library claims no support on that product yet. ")
	out.WriteString("Either no family implements the operation there, or the measured instance answered it in a way the normalized contract does not cover. ")
	out.WriteString("In that second case, the operation's godoc states the difference field by field.\n\n")
	out.WriteString("`unsupported` says the product has no such operation, and no implementation can change that.\n\n")
	out.WriteString("Where a product departs from the normalized contract, the operation's own godoc on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/forgeapi) lists each departure, so this table holds only the status.\n\n")
	out.WriteString("| Operation |")
	for _, product := range spec.Products {
		out.WriteString(" " + string(product) + " |")
	}
	// One space of padding on every cell, separator row included: the Markdown
	// linter's compact table style requires it, and a separator written without it
	// fails the published file's own lint.
	out.WriteString("\n| --- |")
	for range spec.Products {
		out.WriteString(" --- |")
	}
	out.WriteString("\n")
	for _, method := range methods() {
		out.WriteString("| `" + method + "` |")
		for _, product := range spec.Products {
			cell := ""
			for _, entry := range spec.Table {
				if entry.Method != method || entry.Product != product {
					continue
				}
				cell = string(entry.Support)
			}
			out.WriteString(" " + cell + " |")
		}
		out.WriteString("\n")
	}
	return []byte(out.String())
}
