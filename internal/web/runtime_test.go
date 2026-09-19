package web

import (
	"os"
	"path/filepath"
	"testing"

	"hmd/internal/config"
)

func TestValidateWritableDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := config.ValidateWritableDataDir(dir); err != nil {
		t.Fatalf("validateWritableDataDir: %v", err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateWritableDataDir(dir); err == nil {
		t.Fatal("config.ValidateWritableDataDir accepted a non-private directory")
	}
}
