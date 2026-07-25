package main

import (
	"net/url"
	"testing"
)

// TestWidgetToggleUI checks that unchecking a default widget and checking a
// non-default one on the settings form persists as WidgetsRemove/WidgetsAdd,
// and that the change is reflected in the rendered page's widget set — per
// the spec, "a user on clipper who removes sources keeps every other
// clipper behaviour."
func TestWidgetToggleUI(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	// Switch to clipper, which defaults to
	// {identity, search, inbox, sources, tags, keys} in the sidebar.
	resp, err := client.PostForm(server.URL+"/settings/appearance", url.Values{"profile": {"clipper"}})
	if err != nil {
		t.Fatalf("setting profile: %v", err)
	}
	resp.Body.Close()

	// Now save again with "sources" unchecked and "log" (not a clipper
	// default) checked, keeping profile=clipper. The settings form submits
	// every widget id across every slot, not just the sidebar, so the
	// checked set here must mirror clipper's full default arrangement
	// (sidebar + rail + page-head + page-foot) minus sources, plus log.
	checked := []string{
		"identity", "search", "inbox", "tags", "keys", "log", // sidebar (sources dropped, log added)
		"outline",                  // rail
		"source-card", "page-meta", // page-head
		"backlinks", // page-foot
	}
	form := url.Values{"profile": {"clipper"}}
	for _, id := range checked {
		form.Add("widgets", id)
	}
	resp2, err := client.PostForm(server.URL+"/settings/appearance", form)
	if err != nil {
		t.Fatalf("toggling widgets: %v", err)
	}
	resp2.Body.Close()

	rec := app.Auth.prefs("admin")
	if len(rec.WidgetsRemove) != 1 || rec.WidgetsRemove[0] != "sources" {
		t.Errorf("WidgetsRemove = %v, want [sources]", rec.WidgetsRemove)
	}
	if len(rec.WidgetsAdd) != 1 || rec.WidgetsAdd[0] != "log" {
		t.Errorf("WidgetsAdd = %v, want [log]", rec.WidgetsAdd)
	}

	// Every other clipper widget/behaviour should still be present.
	sidebar := widgetsForSlot(slotSidebar, resolveProfile("clipper"), rec.WidgetsAdd, rec.WidgetsRemove)
	got := make(map[string]bool)
	for _, w := range sidebar {
		got[w.ID] = true
	}
	for _, id := range []string{"identity", "search", "inbox", "tags", "keys", "log"} {
		if !got[id] {
			t.Errorf("expected widget %q still present after toggle, sidebar = %v", id, sidebar)
		}
	}
	if got["sources"] {
		t.Error("expected 'sources' to be removed")
	}
}

// TestWidgetToggleRemovingAbsentIsNoOp checks that unchecking a widget id
// that isn't in the profile in the first place doesn't produce a spurious
// WidgetsRemove entry (only ids actually in the profile can be "removed").
func TestWidgetToggleRemovingAbsentIsNoOp(t *testing.T) {
	// docs defaults to {identity, pages, tags, log, keys} in the sidebar
	// plus outline/page-meta/backlinks in the other slots — "calendar" isn't
	// one of them, so submitting the form without "calendar" checked (it
	// never was) must not add "calendar" to WidgetsRemove.
	docsFullDefaults := []string{"identity", "pages", "tags", "log", "keys", "outline", "page-meta", "backlinks"}
	add, remove := computeWidgetOverrides(resolveProfile("docs"), docsFullDefaults)
	if len(add) != 0 {
		t.Errorf("add = %v, want none", add)
	}
	if len(remove) != 0 {
		t.Errorf("remove = %v, want none", remove)
	}
}
