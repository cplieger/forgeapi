// Package gen renders everything forgeapi publishes about per-product behaviour
// from the expectation table, and nothing else states those facts.
//
// Two outputs. It replaces the per-forge block of every role method's godoc in
// roles.go, and it renders SUPPORT.md, the operation-by-product support matrix.
// TestGeneratedFilesAreCurrent owns both files: go generate runs it with -update
// to write them, and a plain run fails when either would move, which is the
// stale-output gate.
//
// A method's block is the span from the "Per forge:" line to the end of that doc
// comment, and the span is this package's alone: everything above the marker is
// written by hand and is never touched.
package gen

import (
	"fmt"
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
		line := strings.TrimSuffix(lines[i], "\n")
		if found := interfaceLine.FindStringSubmatch(line); found != nil {
			role = found[1]
		}
		if line != marker {
			out.WriteString(lines[i])
			continue
		}
		end, name, err := span(lines, i)
		if err != nil {
			return nil, err
		}
		method := role + "." + name
		rendered, err := block(method)
		if err != nil {
			return nil, err
		}
		out.WriteString(rendered)
		seen[method] = true
		i = end - 1
	}
	if err := covered(seen); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}

// span finds the end of the generated span opening at lines[start]: the index of
// the signature line that closes it, and the name of the method it declares.
func span(lines []string, start int) (end int, name string, err error) {
	end = start
	for end < len(lines) && strings.HasPrefix(lines[end], comment) {
		end++
	}
	if end >= len(lines) {
		return 0, "", fmt.Errorf("%s: a %q block at the end of the file", rolesFile, marker)
	}
	found := signature.FindStringSubmatch(lines[end])
	if found == nil {
		return 0, "", fmt.Errorf("%s: the block above %q opens no method", rolesFile, strings.TrimSpace(lines[end]))
	}
	return end, found[1], nil
}

// covered fails on the first table method roles.go carries no block for.
func covered(seen map[string]bool) error {
	for _, method := range methods() {
		if !seen[method] {
			return fmt.Errorf("%s: no %q block for %s", rolesFile, marker, method)
		}
	}
	return nil
}

// methods lists the table's operations once, in table order.
func methods() []string {
	var out []string
	for i := range spec.Table {
		if method := spec.Table[i].Method; !slices.Contains(out, method) {
			out = append(out, method)
		}
	}
	return out
}

// entries returns one method's four entries, in the product column order.
func entries(method string) ([]spec.Entry, error) {
	out := make([]spec.Entry, 0, len(spec.Products))
	for _, product := range spec.Products {
		entry := lookup(method, product)
		if entry == nil {
			return nil, fmt.Errorf("the table has no %s entry for %s", product, method)
		}
		out = append(out, *entry)
	}
	return out, nil
}

// lookup returns the table's entry for one method on one product, or nil when the
// table has none.
func lookup(method string, product spec.Product) *spec.Entry {
	for i := range spec.Table {
		if entry := &spec.Table[i]; entry.Method == method && entry.Product == product {
			return entry
		}
	}
	return nil
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
	for i := range found {
		fmt.Fprintf(&out, "%s\t%-*s  %s\n", comment, width, found[i].Product, exercises(&found[i]))
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
		for i := start; i < end; i++ {
			names = append(names, string(found[i].Product))
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
		fmt.Fprintf(&out, "%s\t%-*s  %-*s  %s\n", comment, field, row.Field, where, row.Where, row.Says)
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
func exercises(entry *spec.Entry) string {
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

func needs(entry *spec.Entry) string {
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
		fmt.Fprintf(&out, "%s\t%-*s  %-*s  %s\n", comment, subject, subjects[i], kind, g.kind, g.says)
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
			for i := range spec.Table {
				if entry := &spec.Table[i]; entry.Method == method && entry.Product == product {
					cell = string(entry.Support)
				}
			}
			out.WriteString(" " + cell + " |")
		}
		out.WriteString("\n")
	}
	return []byte(out.String())
}
