package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadOnlyConfiguration pins read-only mode as environment-only: only
// HMD_SERVER_READ_ONLY sets it, config.yaml cannot, and exporting the
// configuration never writes it to the file.
func TestReadOnlyConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("HMD_CONFIG_FILE", path)
	t.Setenv("HMD_SERVER_READ_ONLY", "true")
	cfg, err := LoadConfig()
	if err != nil || !cfg.ReadOnly {
		t.Fatalf("environment read_only = %v, %v", cfg.ReadOnly, err)
	}
	if err := SaveFileConfig(path, cfg.toFileConfig()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HMD_SERVER_READ_ONLY", "")
	cfg, err = LoadConfig()
	if err != nil || cfg.ReadOnly {
		t.Fatalf("exported config kept read_only = %v, %v", cfg.ReadOnly, err)
	}
	t.Setenv("HMD_SERVER_READ_ONLY", "invalid")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("invalid boolean must fail")
	}
	t.Setenv("HMD_SERVER_READ_ONLY", "")
	if err := os.WriteFile(path, []byte("server:\n  read_only: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(); err == nil {
		t.Fatal("config.yaml must not accept read_only")
	}
}
