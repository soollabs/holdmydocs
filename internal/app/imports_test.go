package app_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestImportBoundaries(t *testing.T) {
	for pkg, forbidden := range map[string][]string{
		"hmd/internal/auth":   {"hmd/internal/app", "hmd/internal/config", "hmd/internal/web"},
		"hmd/internal/store":  {"hmd/internal/app", "hmd/internal/config", "hmd/internal/web", "hmd/internal/wiki"},
		"hmd/internal/wiki":   {"hmd/internal/app", "hmd/internal/auth", "hmd/internal/store", "hmd/internal/web"},
		"hmd/internal/search": {"hmd/internal/app", "hmd/internal/auth", "hmd/internal/config", "hmd/internal/web"},
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
