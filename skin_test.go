package main

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// TestSkinsNoRawHex is the colour-orthogonality guarantee: skins.css must
// never carry a literal colour, only var() references onto style.css's
// palette tokens.
func TestSkinsNoRawHex(t *testing.T) {
	css, err := webFS.ReadFile("web/static/skins.css")
	if err != nil {
		t.Fatalf("reading embedded skins.css: %v", err)
	}
	if m := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`).FindString(string(css)); m != "" {
		t.Errorf("skins.css contains a raw hex colour %q; skins must derive colour from tokens", m)
	}
}

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

func TestBareSkinIncludesSettingsWidget(t *testing.T) {
	if !skinDefaultIDs(skins["bare"])["keys"] {
		t.Error("bare skin must include the keys widget with the settings link")
	}
}

func TestStaticAssetsUseNetworkFirstCache(t *testing.T) {
	script, err := webFS.ReadFile("web/static/sw.js")
	if err != nil {
		t.Fatalf("reading service worker: %v", err)
	}
	body := string(script)
	for _, asset := range []string{"style.css?v=2", "skins.css?v=2", "app.js?v=2", "editor.js?v=2"} {
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

func TestStaticAssetURLsEscapeLegacyCache(t *testing.T) {
	for path, assets := range map[string][]string{
		"web/templates/base.html": {"/static/style.css?v=2", "/static/skins.css?v=2", "/static/app.js?v=2"},
		"web/templates/edit.html": {"/static/editor.js?v=2"},
	} {
		body, err := webFS.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, asset := range assets {
			if !bytes.Contains(body, []byte(asset)) {
				t.Errorf("%s does not request %s, so the legacy cache can serve an obsolete asset", path, asset)
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

// TestNoRawHexInWidgetCSS extends TestSkinsNoRawHex's discipline to the new
// widget rules in style.css (calendar/writing-stats/inbox/sources/
// source-card/prev-entries) added for specs/2026-07-25-profiles-widgets.md
// widget implementation — they must derive colour from the existing palette tokens so they
// look correct under any palette and skin, same as the rest of style.css's
// widget-facing rules.
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

// TestSkinsDefined catches typos in either direction: every name in
// skinNames must have a skins entry and appear in skins.css, and every
// [data-skin="x"] selector in skins.css must be a known name.
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
		found := false
		for _, n := range skinNames {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("skins.css references unknown skin %q not in skinNames", name)
		}
	}
}

func TestSkinRejectsUnknown(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	form := url.Values{"skin": {"nope"}}
	req, _ := http.NewRequest("POST", server.URL+"/settings/appearance", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /settings/appearance failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Status = %d, want 400", resp.StatusCode)
	}

	resp2, err := client.Get(server.URL + "/settings")
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
	req, _ := http.NewRequest("POST", server.URL+"/settings/appearance", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /settings/appearance failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("Status = %d, want 303", resp.StatusCode)
	}

	resp2, err := client.Get(server.URL + "/page/readme")
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

// TestUnknownStoredSkinFallsBack: a hand-edited users.json naming a skin
// that doesn't exist renders the default one. The skin now decides widget
// composition as well as looks, so there is no "render no attribute" state
// to fall back to — it resolves to phosphor like any other unknown name.
func TestUnknownStoredSkinFallsBack(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	app.Auth.mu.Lock()
	rec := app.Auth.users["admin"]
	rec.Skin = "junk"
	app.Auth.users["admin"] = rec
	app.Auth.mu.Unlock()

	resp, err := client.Get(server.URL + "/page/readme")
	if err != nil {
		t.Fatalf("GET /page/readme failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte(`data-skin="`+defaultSkin+`"`)) {
		t.Errorf("unknown stored skin should render the default skin, body: %s", body)
	}
}
