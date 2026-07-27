package main

import (
	"io"
	"strings"
	"testing"
)

// TestWidgetRegistry checks every id referenced by a skin exists in the
// widgets registry, and every widgetIDs entry has a registry definition.
func TestWidgetRegistry(t *testing.T) {
	for sname, sk := range skins {
		for slot, ids := range sk.Widgets {
			for _, id := range ids {
				if _, ok := widgets[id]; !ok {
					t.Errorf("skin %q slot %q references unknown widget %q", sname, slot, id)
				}
			}
		}
	}
	for _, id := range widgetIDs {
		w, ok := widgets[id]
		if !ok {
			t.Errorf("widgetIDs entry %q has no registry definition", id)
			continue
		}
		if w.Description == "" {
			t.Errorf("widget %q has no Description (shown on the settings page)", id)
		}
	}
	for id := range widgets {
		found := false
		for _, wid := range widgetIDs {
			if wid == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("widget %q defined in registry but missing from widgetIDs", id)
		}
	}
}

func TestWidgetsForSlotAddRemove(t *testing.T) {
	p := skins["phosphor"]
	got := widgetsForSlot(slotSidebar, p, nil, []string{"log"})
	for _, w := range got {
		if w.ID == "log" {
			t.Error("expected 'log' to be removed")
		}
	}

	got = widgetsForSlot(slotSidebar, p, []string{"inbox"}, nil)
	found := false
	for _, w := range got {
		if w.ID == "inbox" {
			found = true
		}
	}
	if !found {
		t.Error("expected 'inbox' to be added")
	}

	// Removing an id that isn't present is a no-op, not an error.
	got = widgetsForSlot(slotSidebar, p, nil, []string{"nonexistent"})
	if len(got) != len(p.Widgets[slotSidebar]) {
		t.Errorf("removing a nonexistent id changed the count: got %d, want %d", len(got), len(p.Widgets[slotSidebar]))
	}
}

// TestPopulateWidgetPreviews checks that the settings page's widget preview
// data is built from the wiki's real home page and doesn't depend on
// whether the corresponding widget is actually mounted — the whole point of
// letting a user preview widgets they haven't turned on yet.
func TestPopulateWidgetPreviews(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	var data TemplateData
	app.populateWidgetPreviews(&data)

	if data.Calendar.MonthName == "" {
		t.Error("Calendar not populated (should always be, regardless of mounted widgets)")
	}
	if data.RevisionCount != 1 {
		t.Errorf("RevisionCount = %d, want 1 (readme.md's single seed commit)", data.RevisionCount)
	}
	if data.HeadAuthor != "test" {
		t.Errorf("HeadAuthor = %q, want %q", data.HeadAuthor, "test")
	}
	if len(data.RecentCommits) != 1 {
		t.Errorf("RecentCommits = %v, want 1 entry", data.RecentCommits)
	}
	if data.PrevEntries != nil {
		t.Errorf("PrevEntries = %v, want nil (no daily pages exist)", data.PrevEntries)
	}
}

// TestSettingsAppearanceFormNotNested is the regression test for a bug
// where the widget checklist's live preview boxes (which reuse the real
// widget partials verbatim) put the "search" widget's own <form> inside
// #appearance-form. A browser silently detaches everything after a nested
// <form>, including the save button — so clicking "save appearance" did
// nothing. The fix moved the widgets fieldset outside #appearance-form and
// associated its checkboxes/button via form="appearance-form" instead of
// DOM nesting; this guards against a future widget partial reintroducing
// the same nesting.
func TestSettingsAppearanceFormNotNested(t *testing.T) {
	_, server, client := newTestAppFull(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/_/settings")
	if err != nil {
		t.Fatalf("GET /settings: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	html := string(body)

	start := strings.Index(html, `id="appearance-form"`)
	if start == -1 {
		t.Fatal(`no element with id="appearance-form" found`)
	}
	end := strings.Index(html[start:], "</form>")
	if end == -1 {
		t.Fatal("no closing </form> found after #appearance-form")
	}
	if inner := html[start : start+end]; strings.Count(inner, "<form") > 0 {
		t.Error("#appearance-form contains a nested <form> — this breaks the save button in real browsers")
	}

	if !strings.Contains(html, `form="appearance-form"`) {
		t.Error(`expected a form="appearance-form" attribute associating the save button/checkboxes with #appearance-form`)
	}
}

func TestResolveSkinFallback(t *testing.T) {
	if got := skinName("bogus"); got != defaultSkin {
		t.Errorf("skinName(bogus) = %q, want %q", got, defaultSkin)
	}
	if got := skinName(""); got != defaultSkin {
		t.Errorf("skinName(\"\") = %q, want %q", got, defaultSkin)
	}
	if got := skinName("journal"); got != "journal" {
		t.Errorf("skinName(journal) = %q, want journal", got)
	}
	if got := resolveSkin("bogus").Label; got != skins[defaultSkin].Label {
		t.Errorf("resolveSkin(bogus).Label = %q, want %q", got, skins[defaultSkin].Label)
	}
}
