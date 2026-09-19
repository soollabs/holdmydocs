package web

import (
	"fmt"
	"html/template"
	"log/slog"
	"regexp"
	"strings"

	"hmd/internal/auth"
	"hmd/internal/presentation"
)

var themeVarNames = []string{
	"bg", "surface", "surface-raised", "border", "border-strong",
	"fg", "fg-strong", "fg-muted", "fg-faint",
	"primary", "primary-muted", "accent",
	"warning", "warning-bg",
	"danger", "danger-muted", "danger-bg",
	"accent-fg",
}

var themeVarSet = func() map[string]bool {
	m := make(map[string]bool, len(themeVarNames))
	for _, n := range themeVarNames {
		m[n] = true
	}
	return m
}()

// defaultDark and defaultLight hold parsed theme defaults.
var (
	defaultDark  = map[string]string{}
	defaultLight = map[string]string{}
)

var themeHexRe = regexp.MustCompile(`--([\w-]+):\s*(#[0-9a-fA-F]{3,8})\s*;`)

var validThemeColour = regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$`)

func loadThemeDefaults() {
	css, err := webFS.ReadFile("web/static/style.css")
	if err != nil {
		slog.Warn("reading embedded style.css for theme defaults", "err", err)
		return
	}
	defaultDark, defaultLight = parseThemeDefaults(css)
}

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

// The font catalogue lives in internal/presentation; these local names keep
// the settings and rendering code readable.
var (
	fontsMono  = presentation.FontsMono
	fontsSans  = presentation.FontsSans
	fontsSerif = presentation.FontsSerif
)

var fontStacks = presentation.FontStacks

func buildThemeStyle(prefs auth.UserRecord) template.CSS {
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
