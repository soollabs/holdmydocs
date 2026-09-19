package app_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestImportBoundaries enforces the fixed dependency direction: lower-level
// packages stay independent, application packages never import adapters or the
// composition root, and adapters never import one another.
func TestImportBoundaries(t *testing.T) {
	for pkg, forbidden := range map[string][]string{
		"hmd/internal/auth":         {"hmd/internal/app", "hmd/internal/config", "hmd/internal/web", "hmd/internal/api", "hmd/internal/httpapi", "hmd/internal/mcp", "hmd/internal/presentation", "hmd/internal/wiki", "hmd/internal/store", "hmd/internal/search", "hmd/internal/export"},
		"hmd/internal/store":        {"hmd/internal/app", "hmd/internal/config", "hmd/internal/web", "hmd/internal/wiki", "hmd/internal/api", "hmd/internal/httpapi", "hmd/internal/mcp", "hmd/internal/presentation", "hmd/internal/search", "hmd/internal/export"},
		"hmd/internal/wiki":         {"hmd/internal/app", "hmd/internal/auth", "hmd/internal/store", "hmd/internal/web", "hmd/internal/api", "hmd/internal/httpapi", "hmd/internal/mcp", "hmd/internal/config", "hmd/internal/export"},
		"hmd/internal/search":       {"hmd/internal/app", "hmd/internal/auth", "hmd/internal/config", "hmd/internal/web", "hmd/internal/api", "hmd/internal/httpapi", "hmd/internal/mcp", "hmd/internal/presentation", "hmd/internal/export"},
		"hmd/internal/export":       {"hmd/internal/app", "hmd/internal/web", "hmd/internal/api", "hmd/internal/httpapi", "hmd/internal/mcp"},
		"hmd/internal/config":       {"hmd/internal/app", "hmd/internal/web", "hmd/internal/api", "hmd/internal/httpapi", "hmd/internal/mcp"},
		"hmd/internal/presentation": {"hmd/internal/app", "hmd/internal/auth", "hmd/internal/store", "hmd/internal/web", "hmd/internal/api", "hmd/internal/httpapi", "hmd/internal/mcp", "hmd/internal/config", "hmd/internal/export", "hmd/internal/search", "hmd/internal/wiki"},
		"hmd/internal/api":          {"hmd/internal/app", "hmd/internal/web", "hmd/internal/httpapi", "hmd/internal/mcp"},
		"hmd/internal/web":          {"hmd/internal/app", "hmd/internal/httpapi", "hmd/internal/mcp"},
		"hmd/internal/mcp":          {"hmd/internal/app", "hmd/internal/web", "hmd/internal/httpapi"},
		"hmd/internal/httpapi":      {"hmd/internal/app", "hmd/internal/web", "hmd/internal/mcp"},
	} {
		output, err := exec.Command("go", "list", "-f", "{{join .Imports \"\\n\"}}", pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v: %s", pkg, err, output)
		}
		imports := "\n" + string(output)
		for _, path := range forbidden {
			if strings.Contains(imports, "\n"+path+"\n") {
				t.Errorf("%s must not import %s", pkg, path)
			}
		}
	}
}

// TestAdapterTestImportBoundaries enforces adapter mutual isolation for test
// imports as well.
//
// Documented test-only exception: the browser integration suite in
// internal/web composes internal/httpapi to build the full browser integration route tree
// so ~45 end-to-end assertions across the web tests exercise the real
// browser+JSON boundary. That is a test-only composition dependency; it cannot
// create a production dependency (TestImportBoundaries above still forbids
// web's production imports of httpapi) and is recorded here so the architecture review
// audit can see it is deliberate rather than an oversight. internal/mcp is not
// imported by any web test, so it stays forbidden.
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
