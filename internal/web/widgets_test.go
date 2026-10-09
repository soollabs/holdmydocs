package web

import (
	"hmd/internal/testhttp"

	"io"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"hmd/internal/wiki"
)

// TestWidgetRegistry checks every widgetIDs entry has a registry definition, and vice versa.
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
		found := slices.Contains(widgetIDs, id)
		if !found {
			t.Errorf("widget %q defined in registry but missing from widgetIDs", id)
		}
	}
}

// TestWidgetsForSlotSlotAssignment ensures widgets are filtered and ordered by slot.
func TestWidgetsForSlotSlotAssignment(t *testing.T) {
	ids := []string{"tags", "backlinks", "pages", "log"}

	sidebar := widgetsForSlot(slotSidebar, ids)
	var got []string
	for _, w := range sidebar {
		got = append(got, w.ID)
	}
	want := []string{"tags", "pages", "log"}
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

// TestWidgetsForSlotUnknownIDIgnored ensures unknown widget IDs are ignored.
func TestWidgetsForSlotUnknownIDIgnored(t *testing.T) {
	got := widgetsForSlot(slotSidebar, []string{"pages", "not-a-real-widget"})
	if len(got) != 1 || got[0].ID != "pages" {
		t.Errorf("widgetsForSlot with unknown id = %v, want [pages]", got)
	}
}

func TestTreeIsRequiredBelowAppBar(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	if err := writeNamespaceConfig(t, app, testNS, "widgets: [pages]\n"); err != nil {
		t.Fatalf("configuring widgets: %v", err)
	}
	seedPage(t, app, wiki.Page{Slug: testNS + "/guides", Title: "Guides", Body: "overview"})
	seedPage(t, app, wiki.Page{Slug: testNS + "/guides/setup", Title: "Setup", Body: "steps"})

	resp, err := testhttp.Get(t, client, server.URL+"/"+testNS+"/guides")
	if err != nil {
		t.Fatalf("GET page: %v", err)
	}
	{
		response := resp
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}
		}()
	}
	body, _ := io.ReadAll(resp.Body)
	page := string(body)

	tree := strings.Index(page, "<summary>TREE</summary>")
	pages := strings.Index(page, `class="sidebar-section sidebar-pages"`)
	if !strings.Contains(page, `id="topbar-search"`) || strings.Contains(page, `class="sidebar-section sidebar-search"`) {
		t.Error("search should live only in the app bar")
	}
	if tree == -1 || pages == -1 || tree > pages {
		t.Errorf("tree should precede pages in the sidebar: tree=%d pages=%d", tree, pages)
	}
	if !strings.Contains(page, `href="/notes/guides/setup"`) {
		t.Error("required tree must include nested pages")
	}
}

// TestStatuslineDataSurvivesWidgetRemoval ensures statusline data is independent of mounted widgets.
func TestStatuslineDataSurvivesWidgetRemoval(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	authorName, authorEmail := app.gitAuthor("admin")
	today := time.Now().Format("2006-01-02")
	page := wiki.Page{Slug: "daily/" + today, Title: today, Body: "one two three four five"}
	if _, err := app.Store.Save("daily/"+today+".md", page.Encode(), "seed", authorName, authorEmail); err != nil {
		t.Fatalf("seeding daily page: %v", err)
	}

	resp, err := postJSON(t, client, server.URL+"/_/api/settings/appearance", appearanceJSON(url.Values{"skin": {"journal"}}))
	if err != nil {
		t.Fatalf("setting skin: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}

	resp2, err := testhttp.Get(t, client, server.URL+"/daily/"+today)
	if err != nil {
		t.Fatalf("GET daily page: %v", err)
	}
	{
		response := resp2
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing daily response body: %v", err)
			}
		}()
	}
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
