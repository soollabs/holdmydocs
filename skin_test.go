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
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Status = %d, want 400", resp.StatusCode)
	}

	resp2, err := client.Get(server.URL + "/settings")
	if err != nil {
		t.Fatalf("GET /settings failed: %v", err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if bytes.Contains(body, []byte(`value="nope" selected`)) {
		t.Error("rejected skin should not have been persisted")
	}
}

func TestSkinPersists(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	form := url.Values{"skin": {"blueprint"}}
	req, _ := http.NewRequest("POST", server.URL+"/settings/appearance", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /settings/appearance failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("Status = %d, want 303", resp.StatusCode)
	}

	resp2, err := client.Get(server.URL + "/page/readme")
	if err != nil {
		t.Fatalf("GET /page/readme failed: %v", err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if !bytes.Contains(body, []byte(`data-skin="blueprint"`)) {
		t.Errorf("expected data-skin=\"blueprint\" on rendered page, body: %s", body)
	}
}

func TestUnknownStoredSkinIgnored(t *testing.T) {
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
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if bytes.Contains(body, []byte(`data-skin=`)) {
		t.Errorf("unknown stored skin should render no data-skin attribute, body: %s", body)
	}
}
