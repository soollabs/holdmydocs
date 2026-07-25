package main

import (
	"sort"
	"time"
)

type widgetSlot string

const (
	slotSidebar    widgetSlot = "sidebar"    // left rail, ordered
	slotRail       widgetSlot = "rail"       // right rail (≥1200px only)
	slotPageHead   widgetSlot = "page-head"  // between title and article
	slotPageFoot   widgetSlot = "page-foot"  // after article
	slotStatusline widgetSlot = "statusline" // one segment
)

type widget struct {
	ID    string
	Title string     // "" = renders no <h2>
	Slot  widgetSlot // default slot; a profile may override
}

var widgetIDs = []string{
	"identity", "search", "pages", "pinned", "tags", "log", "health", "keys",
	"calendar", "writing-stats", "inbox", "sources",
	"outline", "source-card", "page-meta", "backlinks", "prev-entries",
}

var widgets = map[string]widget{
	"identity": {ID: "identity", Title: "", Slot: slotSidebar},
	"search":   {ID: "search", Title: "SEARCH", Slot: slotSidebar},
	"pages":    {ID: "pages", Title: "PAGES", Slot: slotSidebar},
	"pinned":   {ID: "pinned", Title: "PINNED", Slot: slotSidebar},
	"tags":     {ID: "tags", Title: "TAGS", Slot: slotSidebar},
	"log":      {ID: "log", Title: "LOG", Slot: slotSidebar},
	"health":   {ID: "health", Title: "HEALTH", Slot: slotSidebar},
	"keys":     {ID: "keys", Title: "", Slot: slotSidebar},

	"calendar":      {ID: "calendar", Title: "", Slot: slotSidebar},
	"writing-stats": {ID: "writing-stats", Title: "THIS MONTH", Slot: slotSidebar},
	"inbox":         {ID: "inbox", Title: "INBOX", Slot: slotSidebar},
	"sources":       {ID: "sources", Title: "SOURCES", Slot: slotSidebar},

	"outline":      {ID: "outline", Title: "ON THIS PAGE", Slot: slotRail},
	"source-card":  {ID: "source-card", Title: "", Slot: slotPageHead},
	"page-meta":    {ID: "page-meta", Title: "", Slot: slotPageHead},
	"backlinks":    {ID: "backlinks", Title: "linked from", Slot: slotPageFoot},
	"prev-entries": {ID: "prev-entries", Title: "earlier", Slot: slotPageFoot},
}

// widgetsForSlot returns the widgets that render in a given slot, in order,
// with add/remove overrides from the user's own prefs applied. add is
// appended after the profile's own list (deduplicated); remove drops ids
// present in the profile list. Removing an id that isn't there is a no-op.
func widgetsForSlot(slot widgetSlot, p profile, add, remove []string) []*widget {
	ids := append([]string{}, p.Widgets[slot]...)
	removeSet := make(map[string]bool, len(remove))
	for _, id := range remove {
		removeSet[id] = true
	}
	have := make(map[string]bool, len(ids))
	for _, id := range ids {
		have[id] = true
	}
	for _, id := range add {
		if _, ok := widgets[id]; ok && !have[id] {
			ids = append(ids, id)
			have[id] = true
		}
	}

	result := make([]*widget, 0, len(ids))
	for _, id := range ids {
		if removeSet[id] {
			continue
		}
		if w, ok := widgets[id]; ok {
			w := w
			result = append(result, &w)
		}
	}
	return result
}

// profileDefaultIDs returns the set of every widget id p mounts, across all
// slots.
func profileDefaultIDs(p profile) map[string]bool {
	set := make(map[string]bool)
	for _, ids := range p.Widgets {
		for _, id := range ids {
			set[id] = true
		}
	}
	return set
}

// computeWidgetOverrides diffs the checked widget ids (from the settings
// form) against p's own defaults, producing the WidgetsAdd/WidgetsRemove
// pair to store: ids checked but not in the profile go to add; ids in the
// profile but not checked go to remove. A widget id that isn't a real
// widget is dropped rather than stored.
func computeWidgetOverrides(p profile, checked []string) (add, remove []string) {
	defaults := profileDefaultIDs(p)
	checkedSet := make(map[string]bool, len(checked))
	for _, id := range checked {
		if _, ok := widgets[id]; !ok {
			continue
		}
		checkedSet[id] = true
		if !defaults[id] {
			add = append(add, id)
		}
	}
	for id := range defaults {
		if !checkedSet[id] {
			remove = append(remove, id)
		}
	}
	sort.Strings(add)
	sort.Strings(remove)
	return add, remove
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

// populateWidgetData fills in the data-heavy widget fields on data — only
// the ones whose widget id is actually present in the resolved profile, so
// a profile that doesn't use e.g. calendar never pays for Store.DailyPages.
func (app *App) populateWidgetData(data *TemplateData) {
	all := append(append(append(append([]*widget{}, data.SidebarWidgets...), data.RailWidgets...), data.PageHeadWidgets...), data.PageFootWidgets...)

	needsDaily := hasWidget(all, "calendar") || hasWidget(all, "writing-stats") || hasWidget(all, "prev-entries")
	var dailySlugs []string
	if needsDaily {
		dailySlugs, _ = app.Store.DailyPages()
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
	if hasWidget(all, "calendar") {
		data.Calendar = buildCalendarMonth(now, dailySlugs)
	}
	if hasWidget(all, "writing-stats") {
		data.WritingStats = buildWritingStats(now, dailySlugs, bodyWords)
	}
	if hasWidget(all, "prev-entries") {
		data.PrevEntries = buildPrevEntries(data.Slug, dailySlugs, 3, firstLine)
	}
	if hasWidget(all, "inbox") {
		data.Inbox = app.Index.UnreadPages()
	}
	if hasWidget(all, "sources") {
		data.Sources = app.Index.SourceCounts()
	}
	if hasWidget(all, "pinned") {
		data.PinnedPages = app.Index.PinnedPages()
	}
	if hasWidget(all, "source-card") && data.Slug != "" {
		data.SourceMeta = app.Index.MetaFor(data.Slug)
	}
}
