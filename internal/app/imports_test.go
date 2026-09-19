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
