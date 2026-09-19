package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestSkinsNoRawHex checks that skins use palette variables.
func TestSkinsNoRawHex(t *testing.T) {
	css, err := webFS.ReadFile("web/static/skins.css")
	if err != nil {
		t.Fatalf("reading embedded skins.css: %v", err)
	}
	if m := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`).FindString(string(css)); m != "" {
		t.Errorf("skins.css contains a raw hex colour %q; skins must derive colour from tokens", m)
	}
}

// TestNonPhosphorSkinsHideWikiLinkBrackets checks bracket hiding.
func TestNonPhosphorSkinsHideWikiLinkBrackets(t *testing.T) {
	css, err := webFS.ReadFile("web/static/skins.css")
	if err != nil {
		t.Fatalf("reading embedded skins.css: %v", err)
	}
	if !strings.Contains(string(css), `:root:not([data-skin="phosphor"]) #page-content .wiki .br {
	display: none;
}`) {
		t.Error("non-phosphor skins must hide wiki-link brackets")
	}
}

// TestSkinsKeepOutlineRail checks outline rail visibility.
func TestSkinsKeepOutlineRail(t *testing.T) {
	css, err := webFS.ReadFile("web/static/skins.css")
	if err != nil {
		t.Fatalf("reading embedded skins.css: %v", err)
	}
	for _, name := range skinNames {
		if strings.Contains(string(css), `:root[data-skin="`+name+`"] .toc-rail`) {
			t.Errorf("%s skin hides the fixed outline rail", name)
		}
	}

	for _, widgets := range [][]string{nil, []string{"outline"}} {
		rail := widgetsForSlot(slotRail, widgets)
		if len(rail) != 1 || rail[0].ID != "outline" {
			t.Errorf("outline rail not fixed for widgets %v", widgets)
		}
	}
}

// TestNoSkinTogglesSettingsWidget checks that settings is not toggleable.
func TestNoSkinTogglesSettingsWidget(t *testing.T) {
	if _, ok := widgets["keys"]; ok {
		t.Error("\"keys\" must not be a toggleable widget; settings must always be visible")
	}
}

// TestSettingsLinkAlwaysRendered checks that settings remains visible.
func TestSettingsLinkAlwaysRendered(t *testing.T) {
	_, server, client := newTestAppFull(t)
	defer server.Close()

	form := url.Values{"skin": {"bare"}}
	resp, err := postJSON(t, client, server.URL+"/_/api/settings/appearance", appearanceJSON(form))
	if err != nil {
		t.Fatalf("setting skin: %v", err)
	}
	closeTestBody(t, resp.Body)

	rendered, err := client.Get(server.URL + "/_/settings")
	if err != nil {
		t.Fatalf("GET /settings: %v", err)
	}
	body, err := io.ReadAll(rendered.Body)
	closeTestBody(t, rendered.Body)
	if err != nil {
		t.Fatalf("reading settings response: %v", err)
	}
	if !bytes.Contains(body, []byte(`href="/_/settings"`)) {
		t.Error("settings link missing from rendered sidebar even with the bare skin's minimal widget set")
	}
}

func TestStaticAssetsUseNetworkFirstCache(t *testing.T) {
	script, err := webFS.ReadFile("web/static/sw.js")
	if err != nil {
		t.Fatalf("reading service worker: %v", err)
	}
	body := string(script)
	for _, asset := range []string{"style.css?v=5", "skins.css?v=2", "app.js?v=6", "page.js?v=1", "editor.js?v=2"} {
		if !strings.Contains(body, asset) {
			t.Errorf("service worker does not precache the requested URL for %s", asset)
		}
	}
	if !strings.Contains(body, "if (response.ok)") {
		t.Error("service worker must not replace valid cached assets with failed responses")
	}
	fetchAt := strings.Index(body, "fetch(e.request)")
	fallbackAt := strings.LastIndex(body, "caches.match(e.request)")
	if fetchAt == -1 || fallbackAt == -1 || fetchAt > fallbackAt {
		t.Error("static asset requests must try the network before the cache fallback")
	}
	if strings.Contains(body, "cached || fetch(e.request)") {
		t.Error("cache-first static assets keep obsolete CSS across deployments")
	}
}

func TestStaticAssetsAllowBrowserCaching(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	server.Close()
	req := httptest.NewRequest(http.MethodGet, "/_/static/style.css?v=3", nil)
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)

	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Fatalf("Cache-Control = %q, want browser caching", got)
	}
}

func TestStaticAssetURLsUseVersionedPaths(t *testing.T) {
	for path, assets := range map[string][]string{
		"web/templates/base.html": {"/_/static/style.css?v=5", "/_/static/skins.css?v=2", "/_/static/app.js?v=6", "/_/static/page.js?v=1"},
		"web/templates/edit.html": {"/_/static/editor.js?v=2"},
	} {
		body, err := webFS.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, asset := range assets {
			if !bytes.Contains(body, []byte(asset)) {
				t.Errorf("%s does not request versioned asset %s", path, asset)
			}
		}
	}
}

func TestSettingsSkinPreviewAppliesPalette(t *testing.T) {
	template, err := webFS.ReadFile("web/templates/settings.html")
	if err != nil {
		t.Fatalf("reading settings template: %v", err)
	}
	body := string(template)
	for _, required := range []string{
		"function previewPalette(name)",
		"root.style.setProperty('--' + role, values[role])",
		"previewPalette(input.value)",
		"previewPalette(skinPalettes[input.value])",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("settings skin preview missing %q", required)
		}
	}
}

func TestSettingsPalettePreviewTracksThemeAndExplicitChoice(t *testing.T) {
	template, err := webFS.ReadFile("web/templates/settings.html")
	if err != nil {
		t.Fatalf("reading settings template: %v", err)
	}
	body := string(template)
	for _, required := range []string{
		`name="palette_explicit"`,
		`paletteExplicit.value = '1'`,
		`paletteExplicit.value = ''`,
		`window.addEventListener('load'`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("settings palette preview missing %q", required)
		}
	}
}

func TestThemeConsumersUseSemanticBackgrounds(t *testing.T) {
	css, err := webFS.ReadFile("web/static/style.css")
	if err != nil {
		t.Fatalf("reading style.css: %v", err)
	}
	body := string(css)
	for _, green := range []string{"#16261b", "rgba(86, 211, 100, .12)"} {
		if strings.Contains(body, green) {
			t.Errorf("theme consumer retains the phosphor palette's hard-coded green %s", green)
		}
	}

	base, err := webFS.ReadFile("web/templates/base.html")
	if err != nil {
		t.Fatalf("reading base template: %v", err)
	}
	if !strings.Contains(string(base), "window.HMDSyncThemeColor") {
		t.Error("browser theme colour does not follow the active palette background")
	}
}

func TestSkinPickerInputsAreContained(t *testing.T) {
	css, err := webFS.ReadFile("web/static/style.css")
	if err != nil {
		t.Fatalf("reading style.css: %v", err)
	}
	body := string(css)
	if !strings.Contains(body, ".skin-card,\n.palette-card {\n\tposition: relative;") {
		t.Error("skin card inputs need a positioned containing block")
	}
	if !strings.Contains(body, ".skin-card input,\n.palette-card input {\n\tposition: absolute;\n\tinset: 0;") {
		t.Error("hidden skin inputs must remain inside their cards instead of extending page overflow")
	}
}

// TestNoRawHexInWidgetCSS ensures widget styles use palette variables.
func TestNoRawHexInWidgetCSS(t *testing.T) {
	css, err := webFS.ReadFile("web/static/style.css")
	if err != nil {
		t.Fatalf("reading embedded style.css: %v", err)
	}
	body := string(css)
	start := strings.Index(body, "/* Calendar widget")
	end := strings.Index(body, "/* History page checkboxes")
	if start == -1 || end == -1 || end <= start {
		t.Fatal("could not find the new widget CSS block in style.css (markers moved?)")
	}
	block := body[start:end]
	if m := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`).FindString(block); m != "" {
		t.Errorf("new widget CSS contains a raw hex colour %q; must derive colour from tokens", m)
	}
}

// TestSkinsDefined ensures Go and CSS define the same skin names.
func TestSkinsDefined(t *testing.T) {
	css, err := webFS.ReadFile("web/static/skins.css")
	if err != nil {
		t.Fatalf("reading embedded skins.css: %v", err)
	}
	body := string(css)

	for _, name := range skinNames {
		if _, ok := skins[name]; !ok {
			t.Errorf("skin %q listed in skinNames but missing from skins map", name)
		}
		if !strings.Contains(body, `[data-skin="`+name+`"]`) {
			t.Errorf("skin %q listed in skinNames but has no [data-skin=%q] block in skins.css", name, name)
		}
	}
	if len(skinNames) != len(skins) {
		t.Errorf("skinNames has %d entries, skins has %d", len(skinNames), len(skins))
	}

	for _, m := range regexp.MustCompile(`\[data-skin="([\w-]+)"\]`).FindAllStringSubmatch(body, -1) {
		name := m[1]
		found := slices.Contains(skinNames, name)
		if !found {
			t.Errorf("skins.css references unknown skin %q not in skinNames", name)
		}
	}
}

func TestSkinRejectsUnknown(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	form := url.Values{"skin": {"nope"}}
	resp, err := postJSON(t, client, server.URL+"/_/api/settings/appearance", appearanceJSON(form))
	if err != nil {
		t.Fatalf("POST appearance failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Status = %d, want 400", resp.StatusCode)
	}

	resp2, err := client.Get(server.URL + "/_/settings")
	if err != nil {
		t.Fatalf("GET /settings failed: %v", err)
	}
	defer func() {
		if err := resp2.Body.Close(); err != nil {
			t.Errorf("closing settings response body: %v", err)
		}
	}()
	body, _ := io.ReadAll(resp2.Body)
	if bytes.Contains(body, []byte(`value="nope" selected`)) {
		t.Error("rejected skin should not have been persisted")
	}
}

func TestSkinPersists(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	form := url.Values{"skin": {"soft"}}
	resp, err := postJSON(t, client, server.URL+"/_/api/settings/appearance", appearanceJSON(form))
	if err != nil {
		t.Fatalf("POST appearance failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.StatusCode)
	}

	resp2, err := client.Get(server.URL + "/" + testHome)
	if err != nil {
		t.Fatalf("GET /page/readme failed: %v", err)
	}
	defer func() {
		if err := resp2.Body.Close(); err != nil {
			t.Errorf("closing page response body: %v", err)
		}
	}()
	body, _ := io.ReadAll(resp2.Body)
	if !bytes.Contains(body, []byte(`data-skin="soft"`)) {
		t.Errorf("expected data-skin=\"soft\" on rendered page, body: %s", body)
	}
}

// TestUnknownStoredSkinFallsBack checks default fallback behavior.
func TestUnknownStoredSkinFallsBack(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	if err := app.Auth.SetPrefs("admin", "", "", "", "junk"); err != nil {
		t.Fatal(err)
	}

	resp, err := client.Get(server.URL + "/" + testHome)
	if err != nil {
		t.Fatalf("GET /page/readme failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte(`data-skin="`+defaultSkin+`"`)) {
		t.Errorf("unknown stored skin should render the default skin, body: %s", body)
	}
}

// TestSkinSwitchResetsPalette checks default palette selection.
func TestSkinSwitchResetsPalette(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	resp, err := postJSON(t, client, server.URL+"/_/api/settings/appearance", appearanceJSON(url.Values{
		"skin": {"phosphor"}, "palette": {"dracula"},
	}))
	if err != nil {
		t.Fatalf("setting palette: %v", err)
	}
	closeTestBody(t, resp.Body)
	if got := app.Auth.Prefs("admin").Palette; got != "dracula" {
		t.Fatalf("palette = %q, want dracula", got)
	}

	resp2, err := postJSON(t, client, server.URL+"/_/api/settings/appearance", appearanceJSON(url.Values{
		"skin": {"newsprint"}, "palette": {"dracula"},
	}))
	if err != nil {
		t.Fatalf("switching skin: %v", err)
	}
	if err := resp2.Body.Close(); err != nil {
		t.Fatalf("closing skin switch response body: %v", err)
	}
	if got := app.Auth.Prefs("admin").Palette; got != skins["newsprint"].Palette {
		t.Errorf("palette after skin switch = %q, want %q", got, skins["newsprint"].Palette)
	}
	rendered, err := client.Get(server.URL + "/_/settings")
	if err != nil {
		t.Fatalf("rendering switched skin: %v", err)
	}
	body, err := io.ReadAll(rendered.Body)
	if err := rendered.Body.Close(); err != nil {
		t.Fatalf("closing settings response body: %v", err)
	}
	if err != nil {
		t.Fatalf("reading switched skin: %v", err)
	}
	if !strings.Contains(string(body), `data-skin="newsprint"`) ||
		!strings.Contains(string(body), `--primary:#268bd2;`) {
		t.Errorf("newsprint response did not render its skin and Solarized primary")
	}

	resp3, err := postJSON(t, client, server.URL+"/_/api/settings/appearance", appearanceJSON(url.Values{
		"skin": {"newsprint"}, "palette": {"gruvbox"},
	}))
	if err != nil {
		t.Fatalf("repicking palette: %v", err)
	}
	if err := resp3.Body.Close(); err != nil {
		t.Fatalf("closing appearance response body: %v", err)
	}
	if got := app.Auth.Prefs("admin").Palette; got != "gruvbox" {
		t.Errorf("palette = %q, want gruvbox — an explicit pick on the same skin must stick", got)
	}
}

func TestSkinSwitchKeepsExplicitPaletteChoice(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	resp, err := postJSON(t, client, server.URL+"/_/api/settings/appearance", appearanceJSON(url.Values{
		"skin": {"newsprint"}, "palette": {"gruvbox"}, "palette_explicit": {"1"},
	}))
	if err != nil {
		t.Fatalf("switching skin with explicit palette: %v", err)
	}
	closeTestBody(t, resp.Body)
	if got := app.Auth.Prefs("admin").Palette; got != "gruvbox" {
		t.Errorf("palette = %q, want gruvbox chosen after the skin", got)
	}
}

// TestSkinDefaultPalettesExist checks that each skin has a valid palette.
func TestSkinDefaultPalettesExist(t *testing.T) {
	for name, s := range skins {
		if s.Palette == "" {
			t.Errorf("skin %q has no default palette", name)
			continue
		}
		if _, ok := themePresets[s.Palette]; !ok {
			t.Errorf("skin %q names palette %q, which is not a preset", name, s.Palette)
		}
	}
}

func TestNewsprintUsesSolarizedBlueAsPrimary(t *testing.T) {
	preset := themePresets[skins["newsprint"].Palette]
	for mode, colours := range map[string]map[string]string{"dark": preset.Dark, "light": preset.Light} {
		if got := colours["primary"]; got != "#268bd2" {
			t.Errorf("newsprint %s primary = %q, want Solarized blue", mode, got)
		}
		if got := colours["accent"]; got != "#6c71c4" {
			t.Errorf("newsprint %s accent = %q, want Solarized violet", mode, got)
		}
	}
}
