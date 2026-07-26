package main

import (
	"io"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestWidgetToggleUI checks that unchecking a default widget and checking a
// non-default one on the settings form persists as WidgetsRemove/WidgetsAdd,
// and that the change is reflected in the rendered page's widget set — a
// user who removes tags keeps every other behaviour of their skin.
//
// The widget added here is "inbox" on purpose: no skin mounts it any more,
// so this is also the regression test for the claim in skins.go that the
// orphaned inbox/sources/source-card widgets stay reachable as opt-ins.
func TestWidgetToggleUI(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	// Switch to soft, which defaults to {identity, pages, tags, keys} in the
	// sidebar.
	resp, err := client.PostForm(server.URL+"/settings/appearance", url.Values{"skin": {"soft"}})
	if err != nil {
		t.Fatalf("setting skin: %v", err)
	}
	resp.Body.Close()

	// Now save again with "tags" unchecked and "inbox" (not a soft default)
	// checked, keeping skin=soft. The settings form submits every widget id
	// across every slot, not just the sidebar, so the checked set here must
	// mirror soft's full default arrangement (sidebar + rail + page-head +
	// page-foot) minus tags, plus inbox.
	checked := []string{
		"identity", "pages", "keys", "inbox", // sidebar (tags dropped, inbox added)
		"outline",   // rail
		"page-meta", // page-head
		"backlinks", // page-foot
	}
	form := url.Values{"skin": {"soft"}}
	for _, id := range checked {
		form.Add("widgets", id)
	}
	resp2, err := client.PostForm(server.URL+"/settings/appearance", form)
	if err != nil {
		t.Fatalf("toggling widgets: %v", err)
	}
	resp2.Body.Close()

	rec := app.Auth.prefs("admin")
	if len(rec.WidgetsRemove) != 1 || rec.WidgetsRemove[0] != "tags" {
		t.Errorf("WidgetsRemove = %v, want [tags]", rec.WidgetsRemove)
	}
	if len(rec.WidgetsAdd) != 1 || rec.WidgetsAdd[0] != "inbox" {
		t.Errorf("WidgetsAdd = %v, want [inbox]", rec.WidgetsAdd)
	}

	// Every other soft widget/behaviour should still be present.
	sidebar := widgetsForSlot(slotSidebar, resolveSkin("soft"), rec.WidgetsAdd, rec.WidgetsRemove)
	got := make(map[string]bool)
	for _, w := range sidebar {
		got[w.ID] = true
	}
	for _, id := range []string{"identity", "pages", "keys", "inbox"} {
		if !got[id] {
			t.Errorf("expected widget %q still present after toggle, sidebar = %v", id, sidebar)
		}
	}
	if got["tags"] {
		t.Error("expected 'tags' to be removed")
	}
}

// TestWidgetToggleRemovingAbsentIsNoOp checks that unchecking a widget id
// that isn't in the skin in the first place doesn't produce a spurious
// WidgetsRemove entry (only ids actually in the skin can be "removed").
func TestWidgetToggleRemovingAbsentIsNoOp(t *testing.T) {
	// phosphor defaults to {identity, pages, tags, log, keys} in the sidebar
	// plus outline/page-meta/backlinks in the other slots — "calendar" isn't
	// one of them, so submitting the form without "calendar" checked (it
	// never was) must not add "calendar" to WidgetsRemove.
	phosphorFullDefaults := []string{"identity", "pages", "tags", "log", "keys", "outline", "page-meta", "backlinks"}
	add, remove := computeWidgetOverrides(resolveSkin("phosphor"), phosphorFullDefaults)
	if len(add) != 0 {
		t.Errorf("add = %v, want none", add)
	}
	if len(remove) != 0 {
		t.Errorf("remove = %v, want none", remove)
	}
}

// TestSkinSwitchDiscardsStaleCheckboxes is the regression test for the bug
// that made every skin switch a no-op: the widget checklist is rendered
// against the skin the user is *on*, so a POST that also changes skin
// carries the old skin's checked set. Diffing that against the new skin's
// defaults wrote every widget defining the new skin into widgets_remove —
// picking "journal" from phosphor gave you phosphor with journal's fonts.
func TestSkinSwitchDiscardsStaleCheckboxes(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	// The form as the browser would submit it while sitting on phosphor:
	// phosphor's own widget set, ticked, plus a new skin choice.
	form := url.Values{"skin": {"journal"}}
	for _, id := range []string{"identity", "pages", "tags", "log", "keys", "outline", "page-meta", "backlinks"} {
		form.Add("widgets", id)
	}
	resp, err := client.PostForm(server.URL+"/settings/appearance", form)
	if err != nil {
		t.Fatalf("switching skin: %v", err)
	}
	resp.Body.Close()

	rec := app.Auth.prefs("admin")
	if len(rec.WidgetsAdd) != 0 || len(rec.WidgetsRemove) != 0 {
		t.Fatalf("skin switch stored overrides add=%v remove=%v, want none", rec.WidgetsAdd, rec.WidgetsRemove)
	}

	sidebar := widgetsForSlot(slotSidebar, resolveSkin("journal"), rec.WidgetsAdd, rec.WidgetsRemove)
	got := make(map[string]bool)
	for _, w := range sidebar {
		got[w.ID] = true
	}
	for _, id := range []string{"calendar", "writing-stats"} {
		if !got[id] {
			t.Errorf("journal widget %q missing after switching to journal, sidebar = %v", id, got)
		}
	}
}

// TestStatuslineDataSurvivesWidgetRemoval: the write/clip statuslines read
// WritingStats/Inbox directly, so unmounting the matching widget (which the
// widget checklist allows) must not silently zero the statusline.
func TestStatuslineDataSurvivesWidgetRemoval(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	authorName, authorEmail := app.gitAuthor("admin")
	today := time.Now().Format("2006-01-02")
	page := Page{Slug: "daily/" + today, Title: today, Body: "one two three four five"}
	if _, err := app.Store.Save("daily/"+today+".md", page.Encode(), "seed", authorName, authorEmail); err != nil {
		t.Fatalf("seeding daily page: %v", err)
	}

	// journal, then a second save that unticks writing-stats.
	resp, err := client.PostForm(server.URL+"/settings/appearance", url.Values{"skin": {"journal"}})
	if err != nil {
		t.Fatalf("setting skin: %v", err)
	}
	resp.Body.Close()

	form := url.Values{"skin": {"journal"}}
	for _, id := range []string{"identity", "calendar", "pages", "keys", "prev-entries"} {
		form.Add("widgets", id)
	}
	resp2, err := client.PostForm(server.URL+"/settings/appearance", form)
	if err != nil {
		t.Fatalf("removing writing-stats: %v", err)
	}
	resp2.Body.Close()

	resp3, err := client.Get(server.URL + "/page/daily/" + today)
	if err != nil {
		t.Fatalf("GET daily page: %v", err)
	}
	defer resp3.Body.Close()
	body, _ := io.ReadAll(resp3.Body)
	if strings.Contains(string(body), ">0 words today<") {
		t.Error("words-today read 0 with the writing-stats widget unmounted; statusline data must not depend on the widget")
	}
	if !strings.Contains(string(body), "words today") {
		t.Error("journal statusline should still show a words-today segment")
	}
}

// TestSkinSwitchResetsPalette: a skin ships with the colours it was designed
// for. Switching skin therefore also moves the palette, even if the user had
// picked one — a broadsheet that opened in terminal green isn't a broadsheet.
// Picking a palette afterwards (same skin) still sticks.
func TestSkinSwitchResetsPalette(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	resp, err := client.PostForm(server.URL+"/settings/appearance", url.Values{
		"skin": {"phosphor"}, "palette": {"dracula"},
	})
	if err != nil {
		t.Fatalf("setting palette: %v", err)
	}
	resp.Body.Close()
	if got := app.Auth.prefs("admin").Palette; got != "dracula" {
		t.Fatalf("palette = %q, want dracula", got)
	}

	// Switching skin overrides the stale palette from the form.
	resp2, err := client.PostForm(server.URL+"/settings/appearance", url.Values{
		"skin": {"newsprint"}, "palette": {"dracula"},
	})
	if err != nil {
		t.Fatalf("switching skin: %v", err)
	}
	resp2.Body.Close()
	if got := app.Auth.prefs("admin").Palette; got != skins["newsprint"].Palette {
		t.Errorf("palette after skin switch = %q, want %q", got, skins["newsprint"].Palette)
	}
	rendered, err := client.Get(server.URL + "/settings")
	if err != nil {
		t.Fatalf("rendering switched skin: %v", err)
	}
	body, err := io.ReadAll(rendered.Body)
	rendered.Body.Close()
	if err != nil {
		t.Fatalf("reading switched skin: %v", err)
	}
	if !strings.Contains(string(body), `data-skin="newsprint"`) ||
		!strings.Contains(string(body), `--primary:#268bd2;`) {
		t.Errorf("newsprint response did not render its skin and Solarized primary")
	}

	// Staying on the same skin leaves the choice alone.
	resp3, err := client.PostForm(server.URL+"/settings/appearance", url.Values{
		"skin": {"newsprint"}, "palette": {"gruvbox"},
	})
	if err != nil {
		t.Fatalf("repicking palette: %v", err)
	}
	resp3.Body.Close()
	if got := app.Auth.prefs("admin").Palette; got != "gruvbox" {
		t.Errorf("palette = %q, want gruvbox — an explicit pick on the same skin must stick", got)
	}
}

func TestSkinSwitchKeepsExplicitPaletteChoice(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	resp, err := client.PostForm(server.URL+"/settings/appearance", url.Values{
		"skin": {"newsprint"}, "palette": {"gruvbox"}, "palette_explicit": {"1"},
	})
	if err != nil {
		t.Fatalf("switching skin with explicit palette: %v", err)
	}
	resp.Body.Close()
	if got := app.Auth.prefs("admin").Palette; got != "gruvbox" {
		t.Errorf("palette = %q, want gruvbox chosen after the skin", got)
	}
	prefs := app.Auth.prefs("admin")
	if len(prefs.WidgetsAdd) != 0 || len(prefs.WidgetsRemove) != 0 {
		t.Errorf("skin switch stored widget overrides add=%v remove=%v, want none", prefs.WidgetsAdd, prefs.WidgetsRemove)
	}
}

// TestSkinDefaultPalettesExist guards the pairing: every skin names a real
// preset, so no skin can ship pointing at a palette that was renamed away.
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
