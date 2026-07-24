package main

import (
	"testing"
)

// TestWidgetRegistry checks every id referenced by a profile exists in the
// widgets registry, and every widgetIDs entry has a registry definition.
func TestWidgetRegistry(t *testing.T) {
	for pname, p := range profiles {
		for slot, ids := range p.Widgets {
			for _, id := range ids {
				if _, ok := widgets[id]; !ok {
					t.Errorf("profile %q slot %q references unknown widget %q", pname, slot, id)
				}
			}
		}
	}
	for _, id := range widgetIDs {
		if _, ok := widgets[id]; !ok {
			t.Errorf("widgetIDs entry %q has no registry definition", id)
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
	p := profiles["docs"]
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

func TestResolveProfileFallback(t *testing.T) {
	if got := resolveProfile("bogus").Name; got != "docs" {
		t.Errorf("resolveProfile(bogus) = %q, want docs", got)
	}
	if got := resolveProfile("").Name; got != "docs" {
		t.Errorf("resolveProfile(\"\") = %q, want docs", got)
	}
	if got := resolveProfile("journal").Name; got != "journal" {
		t.Errorf("resolveProfile(journal) = %q, want journal", got)
	}
}
