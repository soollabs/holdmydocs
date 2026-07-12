package main

import (
	"strings"
	"testing"
)

func TestParseThemeDefaults(t *testing.T) {
	css := []byte(`
:root, :root[data-theme="dark"] {
	--bg:        #0b0f14;
	--panel:     #0e141b;
	--fg-bright: #e6edf3;
	--accent: var(--blue);
}
:root[data-theme="light"] {
	--bg: #f7f8f6;
	--panel: #eef1ec;
	--fg-bright: #1c2226;
}
`)
	dark, light := parseThemeDefaults(css)
	if dark["bg"] != "#0b0f14" {
		t.Errorf("dark bg = %q, want #0b0f14", dark["bg"])
	}
	if dark["panel"] != "#0e141b" {
		t.Errorf("dark panel = %q, want #0e141b", dark["panel"])
	}
	if dark["fg-bright"] != "#e6edf3" {
		t.Errorf("dark fg-bright = %q, want #e6edf3", dark["fg-bright"])
	}
	if _, ok := dark["accent"]; ok {
		t.Error("accent should not be captured (it's a var() reference, not a hex)")
	}
	if light["bg"] != "#f7f8f6" {
		t.Errorf("light bg = %q, want #f7f8f6", light["bg"])
	}
	if light["fg-bright"] != "#1c2226" {
		t.Errorf("light fg-bright = %q, want #1c2226", light["fg-bright"])
	}
}

func TestSnapshotThemeAllMatch(t *testing.T) {
	defaults := map[string]string{
		"bg":    "#0b0f14",
		"panel": "#0e141b",
	}
	submitted := map[string]string{
		"bg":    "#0b0f14",
		"panel": "#0e141b",
	}
	result := snapshotTheme(submitted, defaults)
	if result != nil {
		t.Errorf("expected nil snapshot when all match defaults, got %v", result)
	}
}

func TestSnapshotThemeOneChanged(t *testing.T) {
	defaults := map[string]string{
		"bg":    "#0b0f14",
		"panel": "#0e141b",
	}
	submitted := map[string]string{
		"bg":    "#111111",
		"panel": "#0e141b",
	}
	result := snapshotTheme(submitted, defaults)
	if result == nil {
		t.Fatal("expected non-nil snapshot when one colour changed")
	}
	if result["bg"] != "#111111" {
		t.Errorf("snapshot bg = %q, want #111111", result["bg"])
	}
	if result["panel"] != "#0e141b" {
		t.Errorf("snapshot should include unchanged default for panel, got %q", result["panel"])
	}
}

func TestSnapshotThemeWithEmpty(t *testing.T) {
	defaults := map[string]string{
		"bg":    "#0b0f14",
		"panel": "#0e141b",
	}
	submitted := map[string]string{
		"bg":    "",
		"panel": "#ffffff",
	}
	result := snapshotTheme(submitted, defaults)
	if result == nil {
		t.Fatal("expected non-nil snapshot when one colour changed")
	}
	if result["bg"] != "#0b0f14" {
		t.Errorf("empty submitted should fall back to default, got %q", result["bg"])
	}
	if result["panel"] != "#ffffff" {
		t.Errorf("snapshot panel = %q, want #ffffff", result["panel"])
	}
}

func TestBuildThemeStyle(t *testing.T) {
	cfg := Config{
		ThemeDark: map[string]string{
			"bg":    "#111111",
			"panel": "#222222",
		},
	}
	css := string(buildThemeStyle(cfg))
	if css == "" {
		t.Fatal("expected non-empty CSS")
	}
	if !strings.Contains(css, `:root, :root[data-theme="dark"]{`) {
		t.Errorf("expected dark selector in CSS, got: %s", css)
	}
	if !strings.Contains(css, "--bg:#111111;") {
		t.Error("expected --bg override in CSS")
	}
	if !strings.Contains(css, "--panel:#222222;") {
		t.Error("expected --panel override in CSS")
	}
}

func TestBuildThemeStyleEmpty(t *testing.T) {
	cfg := Config{}
	css := buildThemeStyle(cfg)
	if css != "" {
		t.Errorf("expected empty CSS when no theme overrides, got %q", css)
	}
}

func TestBuildThemeStyleLightOnly(t *testing.T) {
	cfg := Config{
		ThemeLight: map[string]string{"bg": "#ffffff"},
	}
	css := string(buildThemeStyle(cfg))
	if strings.Contains(css, `[data-theme="dark"]`) {
		t.Error("should not contain dark selector when only light is overridden")
	}
	if !strings.Contains(css, `:root[data-theme="light"]{`) {
		t.Error("expected light selector in CSS")
	}
}
