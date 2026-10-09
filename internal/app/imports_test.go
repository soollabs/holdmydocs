package app_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The fixed dependency direction is enforced across the whole transitive graph,
// not just direct imports:
//
//	adapters (web, httpapi, mcp) -> application operations (api) -> lower level
//
// Adapters never import one another or the composition root, api imports no
// adapter, and no transitive path lets a lower-level package route back through
// an adapter or app.
func TestImportBoundaries(t *testing.T) {
	const (
		appPath  = "hmd/internal/app"
		apiPath  = "hmd/internal/api"
		webPath  = "hmd/internal/web"
		httpPath = "hmd/internal/httpapi"
		mcpPath  = "hmd/internal/mcp"
	)
	adapters := []string{webPath, httpPath, mcpPath}
	lowerLevel := []string{
		"hmd/internal/auth",
		"hmd/internal/config",
		"hmd/internal/store",
		"hmd/internal/search",
		"hmd/internal/wiki",
		"hmd/internal/presentation",
		"hmd/internal/export",
		"hmd/internal/httpmiddleware",
	}

	// Every adapter funnels through the shared application operations and can
	// reach none of the other adapters or the composition root, directly or
	// transitively.
	for _, adapter := range adapters {
		output, err := exec.Command("go", "list", "-f", "{{join .Imports \"\\n\"}}", adapter).CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v: %s", adapter, err, output)
		}
		for _, forbidden := range []string{"hmd/internal/store", "hmd/internal/search"} {
			if contains(strings.Fields(string(output)), forbidden) {
				t.Errorf("%s must access %s through api, not import it directly", adapter, forbidden)
			}
		}
		forbidden := []string{appPath}
		for _, other := range adapters {
			if other != adapter {
				forbidden = append(forbidden, other)
			}
		}
		assertNoTransitiveImports(t, adapter, forbidden)
		if deps := listDeps(t, adapter); !contains(deps, apiPath) {
			t.Errorf("%s must depend on %s, transitively", adapter, apiPath)
		}
	}

	// The application boundary imports no adapter and not app.
	assertNoTransitiveImports(t, apiPath, append([]string{appPath}, adapters...))

	// Lower-level packages cannot route back through an adapter, api or app.
	for _, pkg := range lowerLevel {
		assertNoTransitiveImports(t, pkg, append([]string{appPath, apiPath}, adapters...))
	}
}

// TestAdapterTestImportBoundaries enforces adapter mutual isolation for test
// imports as well.
//
// Documented test-only exception: the browser integration suite in
// internal/web composes internal/httpapi to build the full route tree
// so end-to-end assertions across the web tests exercise the real browser+JSON
// boundary. That is a test-only composition dependency; it cannot create a
// production dependency (TestImportBoundaries above still forbids web's
// transitive imports of httpapi). This exception is deliberate. Web tests do
// not import internal/mcp, so it remains forbidden.
func TestAdapterTestImportBoundaries(t *testing.T) {
	for pkg, forbidden := range map[string][]string{
		"hmd/internal/web":     {"hmd/internal/app", "hmd/internal/mcp"},
		"hmd/internal/httpapi": {"hmd/internal/app", "hmd/internal/web", "hmd/internal/mcp"},
		"hmd/internal/mcp":     {"hmd/internal/app", "hmd/internal/web", "hmd/internal/httpapi"},
	} {
		output, err := exec.Command("go", "list", "-f", "{{join .TestImports \"\\n\"}}{{\"\\n\"}}{{join .XTestImports \"\\n\"}}", pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v: %s", pkg, err, output)
		}
		imports := "\n" + string(output)
		for _, path := range forbidden {
			if strings.Contains(imports, "\n"+path+"\n") {
				t.Errorf("%s test files must not import %s", pkg, path)
			}
		}
	}
}

// assertNoTransitiveImports fails when pkg, or anything it transitively
// depends on, is one of the forbidden import paths.
func assertNoTransitiveImports(t *testing.T, pkg string, forbidden []string) {
	t.Helper()
	deps := listDeps(t, pkg)
	for _, path := range forbidden {
		if path != pkg && contains(deps, path) {
			t.Errorf("%s must not import %s (directly or transitively)", pkg, path)
		}
	}
}

// listDeps returns pkg and every package it transitively depends on.
func listDeps(t *testing.T, pkg string) []string {
	t.Helper()
	output, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", pkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v: %s", pkg, err, output)
	}
	return strings.Fields(string(output))
}

func contains(paths []string, want string) bool {
	return slices.Contains(paths, want)
}
