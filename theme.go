package main

import (
	"fmt"
	"html/template"
	"log/slog"
	"regexp"
	"strings"
)

// themeVarNames is the ordered list of the 17 semantic colour roles supplied
// directly by every palette. CSS consumes these names without a hue-based
// variable or alias layer.
var themeVarNames = []string{
	"bg", "surface", "surface-raised", "border", "border-strong",
	"fg", "fg-strong", "fg-muted", "fg-faint",
	"primary", "primary-muted", "accent",
	"warning", "warning-bg",
	"danger", "danger-muted", "danger-bg",
}

var themeVarSet = func() map[string]bool {
	m := make(map[string]bool, len(themeVarNames))
	for _, n := range themeVarNames {
		m[n] = true
	}
	return m
}()

// defaultDark/defaultLight hold the base hex values parsed from the
// embedded style.css at startup. Empty maps mean no defaults could be
// parsed; the settings UI then shows empty colour inputs.
var (
	defaultDark  = map[string]string{}
	defaultLight = map[string]string{}
)

var themeHexRe = regexp.MustCompile(`--([\w-]+):\s*(#[0-9a-fA-F]{3,8})\s*;`)

// validThemeColour matches the hex colour syntax the <input type="color">
// widgets submit. Anything else is rejected rather than written into CSS.
var validThemeColour = regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$`)

// loadThemeDefaults reads the embedded style.css and populates
// defaultDark/defaultLight. Safe to call more than once.
func loadThemeDefaults() {
	css, err := webFS.ReadFile("web/static/style.css")
	if err != nil {
		slog.Warn("reading embedded style.css for theme defaults", "err", err)
		return
	}
	defaultDark, defaultLight = parseThemeDefaults(css)
}

// parseThemeDefaults extracts the 17 base hex values per theme from css.
// The dark block is everything before the light selector, the light block
// is everything from the light selector onward.
func parseThemeDefaults(css []byte) (dark, light map[string]string) {
	s := string(css)
	idx := strings.Index(s, `:root[data-theme="light"]`)
	if idx < 0 {
		return nil, nil
	}
	return parseThemeVars(s[:idx]), parseThemeVars(s[idx:])
}

func parseThemeVars(part string) map[string]string {
	m := map[string]string{}
	for _, match := range themeHexRe.FindAllStringSubmatch(part, -1) {
		name := match[1]
		if themeVarSet[name] {
			m[name] = match[2]
		}
	}
	return m
}

// mergeTheme returns a copy of defaults with overrides applied. Used to
// build the display values for the settings colour inputs.
// Font options for the settings UI. Values are fixed CSS stacks looked up
// by name — only these strings ever reach the inline <style> block, so no
// user-supplied CSS is emitted. Empty config = the style.css default
// (bundled JetBrains Mono for both roles).
var (
	fontsMono  = []string{"jetbrains mono", "system mono", "courier"}
	fontsSans  = []string{"system sans", "helvetica", "verdana"}
	fontsSerif = []string{"georgia", "palatino", "charter"}
)

// skins (the structural themes, and now the widget composition too) live in
// skins.go.

var fontStacks = map[string]string{
	"jetbrains mono": `"JetBrains Mono", ui-monospace, monospace`,
	"system mono":    `ui-monospace, "SF Mono", Menlo, Consolas, monospace`,
	"courier":        `"Courier New", Courier, monospace`,
	"system sans":    `system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`,
	"helvetica":      `"Helvetica Neue", Helvetica, Arial, sans-serif`,
	"verdana":        `Verdana, Geneva, sans-serif`,
	"georgia":        `Georgia, "Times New Roman", serif`,
	"palatino":       `Palatino, "Palatino Linotype", "Book Antiqua", serif`,
	"charter":        `Charter, "Bitstream Charter", Cambria, serif`,
}

// buildThemeStyle renders an inline CSS override block from a user's
// preferences (theme colours, fonts). The block is injected after
// style.css so it overrides the defaults. Returns "" when the user has no
// overrides configured.
func buildThemeStyle(prefs userRecord) template.CSS {
	var b strings.Builder
	preset, ok := themePresets[prefs.Palette]
	if ok {
		b.WriteString(`:root, :root[data-theme="dark"]{`)
		writeThemeVars(&b, preset.Dark)
		b.WriteString("}")
		b.WriteString(`:root[data-theme="light"]{`)
		writeThemeVars(&b, preset.Light)
		b.WriteString("}")
	}
	var fonts strings.Builder
	if s, ok := fontStacks[prefs.FontUI]; ok {
		fonts.WriteString("--font-ui:" + s + ";")
	}
	if s, ok := fontStacks[prefs.FontMono]; ok {
		fonts.WriteString("--font-mono:" + s + ";")
	}
	if fonts.Len() > 0 {
		b.WriteString(":root{" + fonts.String() + "}")
	}
	return template.CSS(b.String())
}

func writeThemeVars(b *strings.Builder, m map[string]string) {
	for _, n := range themeVarNames {
		if v, ok := m[n]; ok && v != "" {
			fmt.Fprintf(b, "--%s:%s;", n, v)
		}
	}
}
