package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadOnlyConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("read_only: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HMD_CONFIG_FILE", path)
	t.Setenv("HMD_READ_ONLY", "")
	cfg, err := LoadConfig()
	if err != nil || !cfg.ReadOnly {
		t.Fatalf("YAML read_only = %v, %v", cfg.ReadOnly, err)
	}
	if err := SaveFileConfig(path, cfg.toFileConfig()); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig()
	if err != nil || !cfg.ReadOnly {
		t.Fatalf("round-trip read_only = %v, %v", cfg.ReadOnly, err)
	}
	t.Setenv("HMD_READ_ONLY", "false")
	cfg, err = LoadConfig()
	if err != nil || cfg.ReadOnly || cfg.EnvOverrides["ReadOnly"] != "HMD_READ_ONLY" {
		t.Fatalf("environment override = %v, %v", cfg.ReadOnly, err)
	}
	t.Setenv("HMD_READ_ONLY", "invalid")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("invalid boolean must fail")
	}
}
