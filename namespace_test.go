package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseNamespaceConfig(t *testing.T) {
	yaml := []byte(`
widgets: [calendar, writing-stats, prev-entries]
public: true
new:
  template: entry
  slug: '{{.Now.Format "2006-01-02"}}'
`)
	cfg, err := parseNamespaceConfig(yaml)
	if err != nil {
		t.Fatalf("parseNamespaceConfig: %v", err)
	}
	if len(cfg.Widgets) != 3 || cfg.Widgets[0] != "calendar" {
		t.Errorf("Widgets = %v", cfg.Widgets)
	}
	if !cfg.Public {
		t.Error("Public = false, want true")
	}
	if cfg.New == nil || cfg.New.Template != "entry" {
		t.Errorf("New = %+v", cfg.New)
	}
}

func TestParseNamespaceConfigUnknownKeyRejected(t *testing.T) {
	yaml := []byte("widgets: [calendar]\nbogus: true\n")
	if _, err := parseNamespaceConfig(yaml); err == nil {
		t.Error("expected error for unknown key, got nil")
	}
}

func TestLoadNamespaceConfigMissingFileIsDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg := loadNamespaceConfig(dir, "blog")
	if cfg.Public {
		t.Error("missing config should default to private")
	}
	if len(cfg.Widgets) != len(builtinWidgets) {
		t.Errorf("Widgets = %v, want built-in defaults", cfg.Widgets)
	}
}

func TestLoadNamespaceConfigMalformedYAMLFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, namespaceConfigFile), []byte("widgets: [oops\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := loadNamespaceConfig(dir, "blog")
	if cfg.Public {
		t.Error("malformed config should fall back to private default")
	}
	if len(cfg.Widgets) != len(builtinWidgets) {
		t.Errorf("Widgets = %v, want built-in defaults", cfg.Widgets)
	}
}

func TestLoadNamespaceConfigUnknownWidgetIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, namespaceConfigFile), []byte("widgets: [not-a-real-widget]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := loadNamespaceConfig(dir, "blog")
	if len(cfg.Widgets) != len(builtinWidgets) {
		t.Errorf("Widgets = %v, want built-in defaults after unknown id", cfg.Widgets)
	}
}

func TestNamespaceFor(t *testing.T) {
	tests := []struct {
		slug   string
		wantNS string
		wantR  string
	}{
		{"blog/drafts/post", "blog", "drafts/post"},
		{"readme", "", "readme"},
		{"journal/2026-07-27", "journal", "2026-07-27"},
	}
	for _, tt := range tests {
		ns, rest := namespaceFor(tt.slug)
		if ns != tt.wantNS || rest != tt.wantR {
			t.Errorf("namespaceFor(%q) = (%q, %q), want (%q, %q)", tt.slug, ns, rest, tt.wantNS, tt.wantR)
		}
	}
}

func TestValidNamespaceNameRejectsReserved(t *testing.T) {
	if validNamespaceName("_") {
		t.Error("_ must be rejected as a namespace name")
	}
	if validNamespaceName(".hidden") {
		t.Error("dot-prefixed directories must be rejected as namespaces")
	}
	if !validNamespaceName("blog") {
		t.Error("blog should be a valid namespace name")
	}
}

func TestBuildNamespaceRegistry(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "blog"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blog", namespaceConfigFile), []byte("public: true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Reserved and dot-prefixed directories must not become namespaces.
	if err := os.Mkdir(filepath.Join(dir, "_"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	reg, err := BuildNamespaceRegistry(dir)
	if err != nil {
		t.Fatalf("BuildNamespaceRegistry: %v", err)
	}
	if _, ok := reg["_"]; ok {
		t.Error("reserved namespace _ must not appear in the registry")
	}
	if _, ok := reg[".git"]; ok {
		t.Error("dot-prefixed directory must not appear in the registry")
	}
	if !reg.IsPublic("blog/post") {
		t.Error("blog/post should resolve to the public blog namespace")
	}
	if reg.IsPublic("readme") {
		t.Error("root namespace should default to private")
	}
	if reg.IsPublic("unknown/page") {
		t.Error("a namespace not in the registry should default to private")
	}
}

func TestNamespaceRegistryResolveUnknownNamespaceDefaults(t *testing.T) {
	reg := NamespaceRegistry{"": defaultNamespaceConfig()}
	cfg := reg.Resolve("nope/page")
	if cfg.Public {
		t.Error("unknown namespace should default to private")
	}
	if len(cfg.Widgets) != len(builtinWidgets) {
		t.Errorf("Widgets = %v, want built-in defaults", cfg.Widgets)
	}
}

func TestNamespaceRegistryNamesRootFirst(t *testing.T) {
	reg := NamespaceRegistry{"": {}, "zeta": {}, "alpha": {}}
	names := reg.Names()
	if len(names) != 3 || names[0] != "" || names[1] != "alpha" || names[2] != "zeta" {
		t.Errorf("Names() = %v", names)
	}
}
