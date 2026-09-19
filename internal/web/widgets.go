package web

import (
	"context"
	"sort"
	"time"

	"hmd/internal/api"
	"hmd/internal/presentation"
	"hmd/internal/wiki"
)

// The widget catalogue and its validation live in internal/presentation; these
// local names keep the rendering code readable.
type widgetSlot = presentation.WidgetSlot

const (
	slotSidebar  = presentation.SlotSidebar
	slotRail     = presentation.SlotRail
	slotPageHead = presentation.SlotPageHead
	slotPageFoot = presentation.SlotPageFoot
)

type widget = presentation.Widget

var widgetIDs = presentation.WidgetIDs

var widgets = presentation.Widgets

type widgetSlotGroup = presentation.WidgetSlotGroup

func widgetSlotGroups() []widgetSlotGroup { return presentation.WidgetSlotGroups() }

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

func namespaceNav(summaries []api.NamespaceSummary, current string) []NamespaceNavEntry {
	entries := make([]NamespaceNavEntry, 0, len(summaries))
	for _, summary := range summaries {
		entries = append(entries, NamespaceNavEntry{Name: summary.Name, Count: summary.Count, Active: summary.Name == current})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

func (app *App) populateWidgetData(ctx context.Context, data *TemplateData, s skin) {
	all := append(append(append(append([]*widget{}, data.SidebarWidgets...), data.RailWidgets...), data.PageHeadWidgets...), data.PageFootWidgets...)

	needs := func(id string) bool {
		return hasWidget(all, id) || (s.Status == "write" && id == "writing-stats")
	}

	ns, _ := wiki.NamespaceFor(data.Slug)
	var titles map[string]string
	if needs("namespaces") || ns != "" {
		titles = app.apiClient().PageTitles()
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
		view, err := app.apiClient().ViewPage(ctx, slug)
		if err != nil {
			return 0
		}
		return countWords(view.Body)
	}
	firstLine := func(slug string) string {
		view, err := app.apiClient().ViewPage(ctx, slug)
		if err != nil {
			return ""
		}
		return firstNonEmptyLine(view.Body)
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
		data.PinnedPages = app.apiClient().PinnedPages()
	}
	if needs("namespaces") {
		data.NamespaceNav = namespaceNav(app.apiClient().NamespaceSummaries(), ns)
	}
	// Filter these entries in render(), before flattening the tree to HTML.
	if ns != "" {
		summary := app.apiClient().NamespaceSummary(ctx, ns)
		if summary != nil {
			data.SidebarTreeNS = ns
			data.SidebarTreeEntries = summary.Pages
		}
	}
}
