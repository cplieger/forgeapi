package forgeapi_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// admitted is every module this library's dependency set may contain: the six the
// transport is assembled from, the file-backed credential store, the secret type every
// secret is held in, and the transitive closure those bring with them.
//
// It is an ALLOWLIST with a reason per row, so a new module fails by name: a
// dependency type on the exported surface ties this library's next major to that
// module's, and no release tool reports it. Only [surfaceTypes] modules may appear there.
var admitted = map[string]string{
	"github.com/cplieger/forgeapi/v3":   "the main module",
	"github.com/cplieger/go/httpclient": "retry, jittered backoff, retry-after parsing, transient classification, the retrying round tripper, the client constructor, the bounded body read, the drain and secret redaction",
	"github.com/cplieger/go/httpcache":  "the read cache: the held answers, their revalidation and the guard that keeps an observed request out of them",
	"github.com/cplieger/go/ssrf":       "the base transport: address policy, dialer control, per-phase attempt bounds and the port allowlist",
	"github.com/cplieger/go/jsonx":      "bounding what an untrusted response costs to decode",
	"github.com/cplieger/go/textsafe":   "sanitizing and bounding untrusted upstream text at the emit site",
	"github.com/cplieger/go/urlform":    "one refusal at entry: a pasted instance URL whose browser reading differs from net/url's",
	"github.com/cplieger/go/filesafe":   "the file-backed credential store, with its private-directory custody and enforced mode",
	"github.com/cplieger/go/masked":     "the secret type every connection and credential secret is kept in",
	"pgregory.net/rapid":                "a property-based testing dependency of the modules above, in the module graph and in no build of this library",
}

// surfaceTypes are the dependencies a consumer-facing package may name, each with
// why its type belongs on this library's surface: its major is adopted on purpose,
// because translating it at the boundary would defeat what it is for.
var surfaceTypes = map[string]string{
	"github.com/cplieger/go/masked": "a secret crosses this surface still masked in both directions, a connection's key, proxy password and header values in and a credential out, so neither side reveals it to hand it over",
}

// TestDependencySetIsTheAdmittedOne holds the module graph to the allowlist above,
// in both directions: a module in the graph and not on the list fails, and a module
// on the list and not in the graph fails too, because a row for a dependency
// nothing links is a claim about this library nobody checks.
func TestDependencySetIsTheAdmittedOne(t *testing.T) {
	graph := moduleGraph(t)
	for _, module := range graph {
		if _, ok := admitted[module]; !ok {
			t.Errorf("the module graph carries %q, which this library admits no reason for; every dependency is named in the transport's own table and nothing else may enter", module)
		}
	}
	for module, reason := range admitted {
		if !slices.Contains(graph, module) {
			t.Errorf("this library admits %q for %q and the module graph does not carry it, so the row states a dependency nothing links", module, reason)
		}
	}
}

// TestNoDependencyIsLinkedOutsideTheTransport walks the direct imports of every
// consumer-facing package over every module path, golang.org/x included, and admits
// only stdlib, this module's own packages (internal/ is where a dependency is named)
// and the [surfaceTypes] modules; a surfaceTypes row no package imports fails as stale.
// The packages are DERIVED rather than listed, so one added later is covered the day
// it lands, with no row for its author to remember.
func TestNoDependencyIsLinkedOutsideTheTransport(t *testing.T) {
	named := map[string]bool{}
	for _, pkg := range consumerFacingPackages(t) {
		for _, dep := range directImports(t, pkg) {
			if _, ok := surfaceTypes[dep]; ok {
				named[dep] = true
				continue
			}
			if stdlib(dep) || ownPackage(dep) {
				continue
			}
			t.Errorf("package %s imports %q; the transport core under internal/ is what holds every dependency type, so a consumer-facing package importing one puts that dependency's next major on this library's surface", pkg, dep)
		}
	}
	for module, reason := range surfaceTypes {
		if !named[module] {
			t.Errorf("this library lets %q reach its surface for %q and no consumer-facing package imports it, so the row states a carve-out nothing uses", module, reason)
		}
	}
}

// stdlib reports whether an import path is a standard-library one, which is the one
// class the placement rule admits anywhere: a module path's first element is a
// domain and so carries a dot, and no standard-library path does.
func stdlib(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

// ownPackage reports whether an import path is this module's own, which includes
// internal/ and is the second class the rule admits: a package of this module is
// not a dependency of it.
func ownPackage(path string) bool {
	const module = "github.com/cplieger/forgeapi/v3"
	return path == module || strings.HasPrefix(path, module+"/")
}

// consumerFacingPackages is every package of this module a consumer can import,
// which is the population the placement rule binds: `go list ./...` less the
// internal/ tree, which is the one place a dependency type may be named.
func consumerFacingPackages(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, pkg := range run(t, "go", "list", "-f", "{{.ImportPath}}", "./...") {
		if !strings.Contains(pkg, "/internal/") {
			out = append(out, pkg)
		}
	}
	if len(out) == 0 {
		t.Fatal("Setup: go list ./... named no consumer-facing package, so this rule would hold nothing")
	}
	return out
}

// moduleGraph is `go list -m all`, which is the graph a consumer resolves.
func moduleGraph(t *testing.T) []string {
	t.Helper()
	out := run(t, "go", "list", "-m", "-f", "{{.Path}}", "all")
	return slices.Sorted(slices.Values(out))
}

// directImports is what one package imports itself, which is where the placement
// rule binds: the transport core imports the dependencies, so a package a consumer
// imports must reach them only THROUGH it.
func directImports(t *testing.T, pkg string) []string {
	t.Helper()
	return run(t, "go", "list", "-f", `{{join .Imports "\n"}}`, pkg)
}

func run(t *testing.T, name string, args ...string) []string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Setup: %s %v: %v", name, args, err)
	}
	var lines []string
	for line := range strings.Lines(string(out)) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}
