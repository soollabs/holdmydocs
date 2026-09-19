package web

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"hmd/internal/auth"
)

func TestThemeVariablesAreSemantic(t *testing.T) {
	forbidden := regexp.MustCompile(`--(?:black|white|gr[ae]y|red|orange|yellow|green|cyan|blue|purple|magenta|pink|brown|amber|teal)(?:-[\w-]+)?\b`)
	customProperty := regexp.MustCompile(`--([\w-]+):\s*([^;]+);`)

	for _, path := range []string{"web/static/style.css", "web/static/skins.css"} {
		body, err := webFS.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if match := forbidden.Find(body); match != nil {
			t.Errorf("%s contains hue-named variable %q", path, match)
		}
		for _, match := range customProperty.FindAllSubmatch(body, -1) {
			if bytes.Contains(match[2], []byte("var(--"+string(match[1])+")")) {
				t.Errorf("%s contains self-referential variable %q", path, match[0])
			}
		}
	}

	settings, err := webFS.ReadFile("web/templates/settings.html")
	if err != nil {
		t.Fatalf("reading settings template: %v", err)
	}
	if match := regexp.MustCompile(`['"](?:green|blue|amber|red)(?:-[\w-]+)?['"]`).Find(settings); match != nil {
		t.Errorf("settings template contains hue-named palette key %q", match)
	}

	for _, name := range themeVarNames {
		if forbidden.MatchString("--" + name) {
			t.Errorf("theme variable %q names a hue instead of its purpose", name)
		}
	}

	want := "bg,surface,surface-raised,border,border-strong,fg,fg-strong,fg-muted,fg-faint,primary,primary-muted,accent,warning,warning-bg,danger,danger-muted,danger-bg,accent-fg"
	if got := strings.Join(themeVarNames, ","); got != want {
		t.Errorf("theme variables = %q, want %q", got, want)
	}
}

func TestParseThemeDefaults(t *testing.T) {
	css := []byte(`
:root, :root[data-theme="dark"] {
	--bg:        #0b0f14;
	--surface:   #0e141b;
	--fg-strong: #e6edf3;
	--marker-colour: var(--primary);
}
:root[data-theme="light"] {
	--bg: #f7f8f6;
	--surface: #eef1ec;
	--fg-strong: #1c2226;
}
`)
	dark, light := parseThemeDefaults(css)
	if dark["bg"] != "#0b0f14" {
		t.Errorf("dark bg = %q, want #0b0f14", dark["bg"])
	}
	if dark["surface"] != "#0e141b" {
		t.Errorf("dark surface = %q, want #0e141b", dark["surface"])
	}
	if dark["fg-strong"] != "#e6edf3" {
		t.Errorf("dark fg-strong = %q, want #e6edf3", dark["fg-strong"])
	}
	if _, ok := dark["marker-colour"]; ok {
		t.Error("marker-colour should not be captured (it's a var() reference, not a hex)")
	}
	if light["bg"] != "#f7f8f6" {
		t.Errorf("light bg = %q, want #f7f8f6", light["bg"])
	}
	if light["fg-strong"] != "#1c2226" {
		t.Errorf("light fg-strong = %q, want #1c2226", light["fg-strong"])
	}
}

func TestBuildThemeStyle(t *testing.T) {
	prefs := auth.UserRecord{
		Palette: "gruvbox",
	}
	css := string(buildThemeStyle(prefs))
	if css == "" {
		t.Fatal("expected non-empty CSS")
	}
	if !strings.Contains(css, `:root, :root[data-theme="dark"]{`) {
		t.Errorf("expected dark selector in CSS, got: %s", css)
	}
	if !strings.Contains(css, `:root[data-theme="light"]{`) {
		t.Errorf("expected light selector in CSS, got: %s", css)
	}
	if !strings.Contains(css, "--bg:") {
		t.Error("expected --bg override in CSS")
	}
}

func TestBuildThemeStyleEmpty(t *testing.T) {
	css := buildThemeStyle(auth.UserRecord{})
	if css != "" {
		t.Errorf("expected empty CSS when no palette is set, got %q", css)
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
			for _, k := range themeVarNames {
				if !validThemeColour.MatchString(defaults[k]) {
					t.Errorf("default %s: variable %q missing or invalid (%q)", mode, k, defaults[k])
				}
				if !validThemeColour.MatchString(vars[k]) {
					t.Errorf("preset %s %s: variable %q missing or invalid (%q)", name, mode, k, vars[k])
				}
			}
			for k := range vars {
				if !themeVarSet[k] {
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
	css := string(buildThemeStyle(auth.UserRecord{FontUI: "georgia", FontMono: "system mono"}))
	if !strings.Contains(css, `--font-ui:Georgia, "Times New Roman", serif;`) ||
		!strings.Contains(css, `--font-mono:ui-monospace,`) {
		t.Errorf("font overrides missing from style: %q", css)
	}
	if got := buildThemeStyle(auth.UserRecord{FontUI: "nope"}); got != "" {
		t.Errorf("unknown font name should emit nothing, got %q", got)
	}
}
