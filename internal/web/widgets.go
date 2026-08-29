package web

import (
	"sort"
	"time"
)

type widgetSlot string

const (
	slotSidebar  widgetSlot = "sidebar"   // left rail, ordered
	slotRail     widgetSlot = "rail"      // right rail (≥1200px only)
	slotPageHead widgetSlot = "page-head" // between title and article
	slotPageFoot widgetSlot = "page-foot" // after article
)

type widget struct {
	ID          string
	Title       string     // "" = renders no <h2>
	Slot        widgetSlot // default slot; a skin may override
	Description string     // one line, shown on the settings page
}

var widgetIDs = []string{
	"pages", "namespaces", "pinned", "tags", "log", "health",
	"calendar", "writing-stats",
	"outline", "page-meta", "backlinks", "prev-entries",
}

var widgets = map[string]widget{
	"pages":      {ID: "pages", Title: "PAGES", Slot: slotSidebar, Description: "recently edited pages in this namespace, most recent first."},
	"namespaces": {ID: "namespaces", Title: "NAMESPACES", Slot: slotSidebar, Description: "every namespace in the wiki, with page counts, linking to its index."},
	"pinned":     {ID: "pinned", Title: "PINNED", Slot: slotSidebar, Description: "pages you've pinned for quick access."},
	"tags":       {ID: "tags", Title: "TAGS", Slot: slotSidebar, Description: "tags used in this namespace, with page counts."},
	"log":        {ID: "log", Title: "LOG", Slot: slotSidebar, Description: "the last few commits to the page you're viewing."},
	"health":     {ID: "health", Title: "HEALTH", Slot: slotSidebar, Description: "missing links and orphaned pages, one click from a full report."},

	"calendar":      {ID: "calendar", Title: "", Slot: slotSidebar, Description: "a month grid of daily pages, with entries highlighted."},
	"writing-stats": {ID: "writing-stats", Title: "THIS MONTH", Slot: slotSidebar, Description: "days written, streak, and word count for this month."},

	"outline":      {ID: "outline", Title: "ON THIS PAGE", Slot: slotRail, Description: "a table of contents built from the headings on the page you're viewing."},
	"page-meta":    {ID: "page-meta", Title: "", Slot: slotPageHead, Description: "tags, last editor, and revision count for the page you're viewing."},
	"backlinks":    {ID: "backlinks", Title: "linked from", Slot: slotPageFoot, Description: "other pages that link to this one."},
	"prev-entries": {ID: "prev-entries", Title: "earlier", Slot: slotPageFoot, Description: "the daily entries just before this one."},
}

type widgetSlotGroup struct {
	Slot    widgetSlot
	Widgets []widget
}

func widgetSlotGroups() []widgetSlotGroup {
	groups := make([]widgetSlotGroup, 0, 4)
	for _, slot := range []widgetSlot{slotSidebar, slotRail, slotPageHead, slotPageFoot} {
		g := widgetSlotGroup{Slot: slot}
		for _, id := range widgetIDs {
			if id == "outline" {
				continue
			}
			if w := widgets[id]; w.Slot == slot {
				g.Widgets = append(g.Widgets, w)
			}
		}
		groups = append(groups, g)
	}
	return groups
}

func widgetsForSlot(slot widgetSlot, ids []string) []*widget {
	result := make([]*widget, 0, len(ids))
	if slot == slotRail {
		outline := widgets["outline"]
		result = append(result, &outline)
	}
	for _, id := range ids {
		if id == "outline" {
			continue
		}
		w, ok := widgets[id]
		if !ok || w.Slot != slot {
			continue
		}
		wCopy := w
		result = append(result, &wCopy)
	}
	return result
}

func hasWidget(list []*widget, id string) bool {
	for _, w := range list {
		if w.ID == id {
			return true
		}
	}
	return false
}

// NamespaceNavEntry is one row in the sidebar NAMESPACES list.
type NamespaceNavEntry struct {
	Name   string
	Count  int
	Active bool
}

func namespaceNav(summaries []NamespaceSummary, current string) []NamespaceNavEntry {
	entries := make([]NamespaceNavEntry, 0, len(summaries))
	for _, summary := range summaries {
		entries = append(entries, NamespaceNavEntry{Name: summary.Name, Count: summary.Count, Active: summary.Name == current})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

func (app *App) populateWidgetData(data *TemplateData, s skin) {
	all := append(append(append(append([]*widget{}, data.SidebarWidgets...), data.RailWidgets...), data.PageHeadWidgets...), data.PageFootWidgets...)

	needs := func(id string) bool {
		return hasWidget(all, id) || (s.Status == "write" && id == "writing-stats")
	}

	ns, _ := namespaceFor(data.Slug)
	var titles map[string]string
	if needs("namespaces") || ns != "" {
		titles = app.Index.Titles()
	}
	needsDates := needs("calendar") || needs("writing-stats") || needs("prev-entries")
	var dates []string
	if needsDates {
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
	if needs("namespaces") {
		data.NamespaceNav = namespaceNav(namespaceSummaries(app.Namespaces(), titles), ns)
	}
	// Token/PAT filtering happens back in render(), after this call, the
	// same way PinnedPages and NamespaceNav do — this only stages the raw
	// entries, since the tree can't be filtered after it's already flattened
	// to HTML. The tree is fixed sidebar chrome; a page outside any namespace
	// has no tree to show.
	if ns != "" {
		summary := namespaceSummaryFor(app.Namespaces(), titles, ns)
		if summary != nil {
			data.SidebarTreeNS = ns
			data.SidebarTreeEntries = summary.Pages
		}
	}
}
