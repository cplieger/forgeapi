package forgeapi_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// admitted is every module this library's dependency set may contain: the five the
// transport is assembled from, the one the file-backed credential store is, and the
// transitive closure those bring with them.
//
// It is an ALLOWLIST rather than a count, so a module arriving with an upgrade
// fails this test by name. Each row says why the module is here, because the
// interesting failure is not a new version but a new NAME: this library's exported
// surface speaks only its own types and stdlib types, so a dependency that reaches
// a signature would put that dependency's next major on this library's next major,
// and no release tool reports it because our own source did not move.
var admitted = map[string]string{
	"github.com/cplieger/forgeapi":      "the main module",
	"github.com/cplieger/httpx/v5":      "retry, jittered backoff, retry-after parsing, transient classification, the retrying round tripper, the client constructor, the bounded body read, the drain and secret redaction",
	"github.com/cplieger/ssrf/v4":       "the base transport: address policy, dialer control, per-phase attempt bounds and the port allowlist",
	"github.com/cplieger/jsoncap/v2":    "bounding what an untrusted response costs to decode",
	"github.com/cplieger/runesafe/v2":   "sanitizing and bounding untrusted upstream text at the emit site",
	"github.com/cplieger/urlform":       "one refusal at entry: a pasted instance URL whose browser reading differs from net/url's",
	"github.com/cplieger/atomicfile/v4": "the file-backed credential store, with its private-directory custody and enforced mode",
	"pgregory.net/rapid":                "a property-based testing dependency of the modules above, in the module graph and in no build of this library",
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

// TestNoDependencyIsLinkedOutsideTheTransport holds the placement half: the three
// dependency types the surface rules ban from a signature live under internal/, so
// no package a consumer imports names a dependency directly, and each reaches the
// wire only through the core.
//
// The rule is stated over every module path rather than over this account's, because
// the arrival it exists to catch is one ordinary `go get` followed by one import, and
// the likeliest arrival is a golang.org/x path a reviewer treats as quasi-stdlib. So
// the two carve-outs are the only ones the rule needs: the module's own packages,
// which are not dependencies, and internal/, which is where a dependency is allowed
// to be named.
//
// The packages it walks are DERIVED rather than listed, so a consumer-facing package
// added later is covered on the day it lands. A list would have to be remembered by
// whoever adds one, and nothing would remind them: the golden surface sees the new
// package, but nothing makes the hand updating that golden add a row here too.
func TestNoDependencyIsLinkedOutsideTheTransport(t *testing.T) {
	for _, pkg := range consumerFacingPackages(t) {
		for _, dep := range directImports(t, pkg) {
			if stdlib(dep) || ownPackage(dep) {
				continue
			}
			t.Errorf("package %s imports %q; the transport core under internal/ is what holds every dependency type, so a consumer-facing package importing one puts that dependency's next major on this library's surface", pkg, dep)
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
	const module = "github.com/cplieger/forgeapi"
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
