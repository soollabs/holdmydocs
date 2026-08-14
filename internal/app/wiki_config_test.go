package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWikiConfig(t *testing.T) {
	repoDir := t.TempDir()

	cfg, exists, err := LoadWikiConfig(repoDir)
	if err != nil {
		t.Fatalf("LoadWikiConfig missing: %v", err)
	}
	if exists || cfg.SiteName != defaultSiteName || cfg.Landing != "" {
		t.Errorf("missing config = %#v, exists=%v", cfg, exists)
	}

	data := []byte("landing: notes/\nsite_name: Shared Wiki\n")
	if err := os.WriteFile(filepath.Join(repoDir, wikiConfigFile), data, 0644); err != nil {
		t.Fatalf("writing %s: %v", wikiConfigFile, err)
	}
	cfg, exists, err = LoadWikiConfig(repoDir)
	if err != nil {
		t.Fatalf("LoadWikiConfig: %v", err)
	}
	if !exists || cfg != (WikiConfig{Landing: "notes/", SiteName: "Shared Wiki"}) {
		t.Errorf("LoadWikiConfig = %#v, exists=%v", cfg, exists)
	}

	if err := os.WriteFile(filepath.Join(repoDir, wikiConfigFile), []byte("unknown: value\n"), 0644); err != nil {
		t.Fatalf("writing invalid config: %v", err)
	}
	if _, _, err := LoadWikiConfig(repoDir); err == nil {
		t.Fatal("LoadWikiConfig accepted an unknown key")
	}
}
