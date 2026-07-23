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

func TestCollectThemeRejectsNonHexValues(t *testing.T) {
	form := map[string][]string{
		"theme_dark_bg":    {`red}</style><script>alert(1)</script><style>{`},
		"theme_dark_panel": {"#222222"},
	}
	m := collectTheme(form, "theme_dark_")
	if _, ok := m["bg"]; ok {
		t.Error("expected non-hex bg value to be dropped, got it in the map")
	}
	if m["panel"] != "#222222" {
		t.Errorf("panel = %q, want #222222", m["panel"])
	}
}

func TestBuildThemeStyle(t *testing.T) {
	prefs := userRecord{
		ThemeDark: map[string]string{
			"bg":    "#111111",
			"panel": "#222222",
		},
	}
	css := string(buildThemeStyle(prefs))
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
	css := buildThemeStyle(userRecord{})
	if css != "" {
		t.Errorf("expected empty CSS when no theme overrides, got %q", css)
	}
}

func TestBuildThemeStyleLightOnly(t *testing.T) {
	prefs := userRecord{
		ThemeLight: map[string]string{"bg": "#ffffff"},
	}
	css := string(buildThemeStyle(prefs))
	if strings.Contains(css, `[data-theme="dark"]`) {
		t.Error("should not contain dark selector when only light is overridden")
	}
	if !strings.Contains(css, `:root[data-theme="light"]{`) {
		t.Error("expected light selector in CSS")
	}
}

func TestThemePresetsValidAndComplete(t *testing.T) {
	loadThemeDefaults()
	if len(defaultDark) == 0 || len(defaultLight) == 0 {
		t.Fatal("no theme defaults parsed from embedded style.css")
	}
	for _, name := range themePresetNames {
		p, ok := themePresets[name]
		if !ok {
			t.Fatalf("preset %q listed in themePresetNames but not defined", name)
		}
		for mode, vars := range map[string]map[string]string{"dark": p.Dark, "light": p.Light} {
			defaults := defaultDark
			if mode == "light" {
				defaults = defaultLight
			}
			for k := range defaults {
				if !validThemeColour.MatchString(vars[k]) {
					t.Errorf("preset %s %s: variable %q missing or invalid (%q)", name, mode, k, vars[k])
				}
			}
			for k := range vars {
				if _, ok := defaults[k]; !ok {
					t.Errorf("preset %s %s: unknown variable %q", name, mode, k)
				}
			}
		}
	}
	if len(themePresetNames) != len(themePresets) {
		t.Errorf("themePresetNames has %d entries, themePresets has %d", len(themePresetNames), len(themePresets))
	}
}

func TestFontStacksMatchGroups(t *testing.T) {
	grouped := map[string]bool{}
	for _, g := range [][]string{fontsMono, fontsSans, fontsSerif} {
		for _, n := range g {
			if grouped[n] {
				t.Errorf("font %q listed in more than one group", n)
			}
			grouped[n] = true
			if _, ok := fontStacks[n]; !ok {
				t.Errorf("font %q grouped but has no stack", n)
			}
		}
	}
	for n := range fontStacks {
		if !grouped[n] {
			t.Errorf("font %q has a stack but is in no group", n)
		}
	}
}

func TestBuildThemeStyleFonts(t *testing.T) {
	css := string(buildThemeStyle(userRecord{FontUI: "georgia", FontMono: "system mono"}))
	if !strings.Contains(css, `--font-ui:Georgia, "Times New Roman", serif;`) ||
		!strings.Contains(css, `--font-mono:ui-monospace,`) {
		t.Errorf("font overrides missing from style: %q", css)
	}
	if got := buildThemeStyle(userRecord{FontUI: "nope"}); got != "" {
		t.Errorf("unknown font name should emit nothing, got %q", got)
	}
}

func TestMatchingPreset(t *testing.T) {
	if got := matchingPreset(themePresets["nord"].Dark, "dark"); got != "nord" {
		t.Errorf("matchingPreset(nord dark) = %q, want nord", got)
	}
	if got := matchingPreset(themePresets["nord"].Light, "dark"); got != "" {
		t.Errorf("light palette shouldn't match under dark mode, got %q", got)
	}
	if got := matchingPreset(defaultDark, "dark"); got != "" {
		t.Errorf("default theme shouldn't match any preset, got %q", got)
	}
	tweaked := mergeTheme(themePresets["gruvbox"].Dark, map[string]string{"bg": "#123456"})
	if got := matchingPreset(tweaked, "dark"); got != "" {
		t.Errorf("hand-tweaked palette shouldn't match its source preset, got %q", got)
	}
}
