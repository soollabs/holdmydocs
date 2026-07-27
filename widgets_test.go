package main

import (
	"io"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestWidgetRegistry checks every widgetIDs entry has a registry
// definition, and vice versa.
func TestWidgetRegistry(t *testing.T) {
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

// TestWidgetsForSlotSlotAssignment checks that widgetsForSlot resolves a
// flat id list to the correct slot, preserving within-slot order, and that
// an id belonging to a different slot (per the registry) never leaks in —
// a page-foot id listed alongside sidebar ids must not appear in the
// sidebar.
func TestWidgetsForSlotSlotAssignment(t *testing.T) {
	ids := []string{"tags", "backlinks", "search", "log"}

	sidebar := widgetsForSlot(slotSidebar, ids)
	var got []string
	for _, w := range sidebar {
		got = append(got, w.ID)
	}
	want := []string{"tags", "search", "log"}
	if len(got) != len(want) {
		t.Fatalf("sidebar = %v, want %v", got, want)
	}
	for i, id := range want {
		if got[i] != id {
			t.Errorf("sidebar[%d] = %q, want %q (order should match input list)", i, got[i], id)
		}
	}
	for _, w := range sidebar {
		if w.ID == "backlinks" {
			t.Error("backlinks is a page-foot widget and must not appear in the sidebar")
		}
	}

	pageFoot := widgetsForSlot(slotPageFoot, ids)
	if len(pageFoot) != 1 || pageFoot[0].ID != "backlinks" {
		t.Errorf("page-foot = %v, want [backlinks]", pageFoot)
	}
}

// TestWidgetsForSlotUnknownIDIgnored checks an id with no registry
// definition is silently dropped rather than erroring.
func TestWidgetsForSlotUnknownIDIgnored(t *testing.T) {
	got := widgetsForSlot(slotSidebar, []string{"search", "not-a-real-widget"})
	if len(got) != 1 || got[0].ID != "search" {
		t.Errorf("widgetsForSlot with unknown id = %v, want [search]", got)
	}
}

// TestStatuslineDataSurvivesWidgetRemoval: the write statusline reads
// WritingStats directly, so a namespace whose widget list doesn't mount
// writing-stats must not silently zero the statusline.
func TestStatuslineDataSurvivesWidgetRemoval(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	authorName, authorEmail := app.gitAuthor("admin")
	today := time.Now().Format("2006-01-02")
	page := Page{Slug: "daily/" + today, Title: today, Body: "one two three four five"}
	if _, err := app.Store.Save("daily/"+today+".md", page.Encode(), "seed", authorName, authorEmail); err != nil {
		t.Fatalf("seeding daily page: %v", err)
	}

	resp, err := client.PostForm(server.URL+"/_/settings/appearance", url.Values{"skin": {"journal"}})
	if err != nil {
		t.Fatalf("setting skin: %v", err)
	}
	closeTestBody(t, resp.Body)

	// journal's Status is "write"; the root namespace's widget list (no
	// .namespace.yaml) is the built-in default, which doesn't include
	// writing-stats — confirming the statusline still reads it regardless.
	resp2, err := client.Get(server.URL + "/daily/" + today)
	if err != nil {
		t.Fatalf("GET daily page: %v", err)
	}
	defer func() {
		if err := resp2.Body.Close(); err != nil {
			t.Errorf("closing daily response body: %v", err)
		}
	}()
	body, _ := io.ReadAll(resp2.Body)
	if strings.Contains(string(body), ">0 words today<") {
		t.Error("words-today read 0 with the writing-stats widget unmounted; statusline data must not depend on the widget")
	}
	if !strings.Contains(string(body), "words today") {
		t.Error("journal statusline should still show a words-today segment")
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
