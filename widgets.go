package main

import (
	"time"
)

type widgetSlot string

const (
	slotSidebar  widgetSlot = "sidebar"   // left rail, ordered
	slotRail     widgetSlot = "rail"      // right rail (≥1200px only)
	slotPageHead widgetSlot = "page-head" // between title and article
	slotPageFoot widgetSlot = "page-foot" // after article
	// The statusline is not a slot: its segments are fixed per skin.Status
	// rather than composable, so there is nothing for a registry to hold.
)

type widget struct {
	ID          string
	Title       string     // "" = renders no <h2>
	Slot        widgetSlot // default slot; a skin may override
	Description string     // one line, shown on the settings page
}

var widgetIDs = []string{
	"search", "pages", "pinned", "tags", "log", "health",
	"calendar", "writing-stats",
	"outline", "page-meta", "backlinks", "prev-entries",
}

var widgets = map[string]widget{
	"search": {ID: "search", Title: "SEARCH", Slot: slotSidebar, Description: "a quick search box for the wiki."},
	"pages":  {ID: "pages", Title: "PAGES", Slot: slotSidebar, Description: "recently edited pages, most recent first."},
	"pinned": {ID: "pinned", Title: "PINNED", Slot: slotSidebar, Description: "pages you've pinned for quick access."},
	"tags":   {ID: "tags", Title: "TAGS", Slot: slotSidebar, Description: "every tag in the wiki, with page counts."},
	"log":    {ID: "log", Title: "LOG", Slot: slotSidebar, Description: "the last few commits to the page you're viewing."},
	"health": {ID: "health", Title: "HEALTH", Slot: slotSidebar, Description: "missing links and orphaned pages, one click from a full report."},

	"calendar":      {ID: "calendar", Title: "", Slot: slotSidebar, Description: "a month grid of daily pages, with entries highlighted."},
	"writing-stats": {ID: "writing-stats", Title: "THIS MONTH", Slot: slotSidebar, Description: "days written, streak, and word count for this month."},

	"outline":      {ID: "outline", Title: "ON THIS PAGE", Slot: slotRail, Description: "a table of contents built from the headings on the page you're viewing."},
	"page-meta":    {ID: "page-meta", Title: "", Slot: slotPageHead, Description: "tags, last editor, and revision count for the page you're viewing."},
	"backlinks":    {ID: "backlinks", Title: "linked from", Slot: slotPageFoot, Description: "other pages that link to this one."},
	"prev-entries": {ID: "prev-entries", Title: "earlier", Slot: slotPageFoot, Description: "the daily entries just before this one."},
}

// widgetsForSlot resolves a flat namespace widget-id list to the ordered
// *widgets that render in slot: the list controls membership and
// within-slot order, but which slot a widget lands in always comes from the
// registry, not from where the id sits in ids.
func widgetsForSlot(slot widgetSlot, ids []string) []*widget {
	result := make([]*widget, 0, len(ids))
	for _, id := range ids {
		w, ok := widgets[id]
		if !ok || w.Slot != slot {
			continue
		}
		wCopy := w
		result = append(result, &wCopy)
	}
	return result
}

// hasWidget reports whether id appears in list.
func hasWidget(list []*widget, id string) bool {
	for _, w := range list {
		if w.ID == id {
			return true
		}
	}
	return false
}

// populateWidgetData fills only the data needed by mounted widgets and the
// statusline.
func (app *App) populateWidgetData(data *TemplateData, s skin) {
	all := append(append(append(append([]*widget{}, data.SidebarWidgets...), data.RailWidgets...), data.PageHeadWidgets...), data.PageFootWidgets...)

	needs := func(id string) bool {
		return hasWidget(all, id) || (s.Status == "write" && id == "writing-stats")
	}

	ns, _ := namespaceFor(data.Slug)
	needsDates := needs("calendar") || needs("writing-stats") || needs("prev-entries")
	var dates []string
	if needsDates {
		titles := app.Index.Titles()
		slugs := make([]string, 0, len(titles))
		for slug := range titles {
			slugs = append(slugs, slug)
		}
		dates = datesInNamespace(ns, slugs)
	}

	bodyWords := func(slug string) int {
		content, _, err := app.Store.Read(pageFile(slug))
		if err != nil {
			return 0
		}
		return countWords(ParsePage(slug, content).Body)
	}
	firstLine := func(slug string) string {
		content, _, err := app.Store.Read(pageFile(slug))
		if err != nil {
			return ""
		}
		return firstNonEmptyLine(ParsePage(slug, content).Body)
	}

	now := time.Now()
	if needs("calendar") {
		data.Calendar = buildCalendarMonth(now, ns, dates)
	}
	if needs("writing-stats") {
		data.WritingStats = buildWritingStats(now, ns, dates, bodyWords)
	}
	if needs("prev-entries") {
		data.PrevEntries = buildPrevEntries(data.Slug, dates, 3, firstLine)
	}
	if needs("pinned") {
		data.PinnedPages = app.Index.PinnedPages()
	}
}
