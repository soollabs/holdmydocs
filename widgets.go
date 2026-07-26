package main

import (
	"sort"
	"strings"
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
	"identity", "search", "pages", "pinned", "tags", "log", "health",
	"calendar", "writing-stats", "inbox", "sources",
	"outline", "source-card", "page-meta", "backlinks", "prev-entries",
}

var widgets = map[string]widget{
	"identity": {ID: "identity", Title: "", Slot: slotSidebar, Description: "who you are and where — a shell prompt or masthead, depending on skin."},
	"search":   {ID: "search", Title: "SEARCH", Slot: slotSidebar, Description: "a quick search box for the wiki."},
	"pages":    {ID: "pages", Title: "PAGES", Slot: slotSidebar, Description: "recently edited pages, most recent first."},
	"pinned":   {ID: "pinned", Title: "PINNED", Slot: slotSidebar, Description: "pages you've pinned for quick access."},
	"tags":     {ID: "tags", Title: "TAGS", Slot: slotSidebar, Description: "every tag in the wiki, with page counts."},
	"log":      {ID: "log", Title: "LOG", Slot: slotSidebar, Description: "the last few commits to the page you're viewing."},
	"health":   {ID: "health", Title: "HEALTH", Slot: slotSidebar, Description: "missing links and orphaned pages, one click from a full report."},

	"calendar":      {ID: "calendar", Title: "", Slot: slotSidebar, Description: "a month grid of daily pages, with entries highlighted."},
	"writing-stats": {ID: "writing-stats", Title: "THIS MONTH", Slot: slotSidebar, Description: "days written, streak, and word count for this month."},
	"inbox":         {ID: "inbox", Title: "INBOX", Slot: slotSidebar, Description: "unread pages waiting for your attention."},
	"sources":       {ID: "sources", Title: "SOURCES", Slot: slotSidebar, Description: "pages grouped by source domain, for link-heavy wikis."},

	"outline":      {ID: "outline", Title: "ON THIS PAGE", Slot: slotRail, Description: "a table of contents built from the headings on the page you're viewing."},
	"source-card":  {ID: "source-card", Title: "", Slot: slotPageHead, Description: "the original URL, author, and read time for an imported article."},
	"page-meta":    {ID: "page-meta", Title: "", Slot: slotPageHead, Description: "tags, last editor, and revision count for the page you're viewing."},
	"backlinks":    {ID: "backlinks", Title: "linked from", Slot: slotPageFoot, Description: "other pages that link to this one."},
	"prev-entries": {ID: "prev-entries", Title: "earlier", Slot: slotPageFoot, Description: "the daily entries just before this one."},
}

// widgetsForSlot returns the widgets that render in a given slot, in order,
// with add/remove overrides from the user's own prefs applied. add is
// appended after the skin's own list (deduplicated); remove drops ids
// present in the skin's list. Removing an id that isn't there is a no-op.
func widgetsForSlot(slot widgetSlot, s skin, add, remove []string) []*widget {
	ids := append([]string{}, s.Widgets[slot]...)
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

// skinDefaultIDs returns the set of every widget id s mounts, across all
// slots.
func skinDefaultIDs(s skin) map[string]bool {
	set := make(map[string]bool)
	for _, ids := range s.Widgets {
		for _, id := range ids {
			set[id] = true
		}
	}
	return set
}

// computeWidgetOverrides diffs the checked widget ids (from the settings
// form) against s's own defaults, producing the WidgetsAdd/WidgetsRemove
// pair to store: ids checked but not in the skin go to add; ids in the skin
// but not checked go to remove. A widget id that isn't a real widget is
// dropped rather than stored.
//
// Only meaningful when the checkboxes were rendered against this same skin
// — see handleSettingsAppearance, which discards them on a skin change.
func computeWidgetOverrides(s skin, checked []string) (add, remove []string) {
	defaults := skinDefaultIDs(s)
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

// populateWidgetData fills only the data needed by mounted widgets and the
// statusline.
func (app *App) populateWidgetData(data *TemplateData, s skin) {
	all := append(append(append(append([]*widget{}, data.SidebarWidgets...), data.RailWidgets...), data.PageHeadWidgets...), data.PageFootWidgets...)

	needs := func(id string) bool {
		return hasWidget(all, id) || (s.Status == "write" && id == "writing-stats")
	}

	needsDaily := needs("calendar") || needs("writing-stats") || needs("prev-entries")
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
	if needs("calendar") {
		data.Calendar = buildCalendarMonth(now, dailySlugs)
	}
	if needs("writing-stats") {
		data.WritingStats = buildWritingStats(now, dailySlugs, bodyWords)
	}
	if needs("prev-entries") {
		data.PrevEntries = buildPrevEntries(data.Slug, dailySlugs, 3, firstLine)
	}
	if needs("inbox") {
		data.Inbox = app.Index.UnreadPages()
	}
	if needs("sources") {
		data.Sources = app.Index.SourceCounts()
	}
	if needs("pinned") {
		data.PinnedPages = app.Index.PinnedPages()
	}
	if needs("source-card") && data.Slug != "" {
		data.SourceMeta = app.Index.MetaFor(data.Slug)
	}
}

// populateWidgetPreviews fills every optional widget's data field with a
// real, representative sample (the wiki's home page, and its most recent
// daily entry) so the settings page can show a live preview of each widget
// regardless of which ones are actually mounted for this user. Fields left
// untouched here (because the widget is already mounted) are populated by
// populateWidgetData instead, with the user's own real data taking
// precedence.
//
// Preview boxes are rendered inert (see .widget-preview in style.css), so
// it's fine that the slugs used here aren't the page actually being viewed.
func (app *App) populateWidgetPreviews(data *TemplateData) {
	dailySlugs, _ := app.Store.DailyPages()
	now := time.Now()
	data.Calendar = buildCalendarMonth(now, dailySlugs)

	bodyWords := func(slug string) int {
		content, _, err := app.Store.Read(pageFile(slug))
		if err != nil {
			return 0
		}
		return countWords(ParsePage(slug, content).Body)
	}
	data.WritingStats = buildWritingStats(now, dailySlugs, bodyWords)

	if len(dailySlugs) > 0 {
		firstLine := func(slug string) string {
			content, _, err := app.Store.Read(pageFile(slug))
			if err != nil {
				return ""
			}
			return firstNonEmptyLine(ParsePage(slug, content).Body)
		}
		latest := dailySlugs[len(dailySlugs)-1]
		data.PrevEntries = buildPrevEntries(latest, dailySlugs, 3, firstLine)
	}

	data.Inbox = app.Index.UnreadPages()
	data.Sources = app.Index.SourceCounts()
	data.PinnedPages = app.Index.PinnedPages()

	homeSlug := app.config().HomeSlug()
	data.SourceMeta = app.Index.MetaFor(homeSlug)

	if content, _, err := app.Store.Read(pageFile(homeSlug)); err == nil {
		page := ParsePage(homeSlug, content)
		for _, tag := range page.Tags {
			data.PageTags = append(data.PageTags, TagChip{Tag: tag, Slug: Slugify(tag)})
		}
	}
	titles := app.Index.Titles()
	for _, bslug := range app.Index.Backlinks(homeSlug) {
		data.Backlinks = append(data.Backlinks, BacklinkEntry{Slug: bslug, Title: titles[bslug]})
	}
	if history, err := app.Store.History(pageFile(homeSlug)); err == nil && len(history) > 0 {
		data.RevisionCount = len(history)
		data.HeadAuthor = history[0].Author
		data.HeadWhen = relativeTime(history[0].When)
		for _, c := range history[:min(3, len(history))] {
			data.RecentCommits = append(data.RecentCommits, LogEntry{
				Age:     relativeTime(c.When),
				Message: strings.TrimSpace(c.Message),
			})
		}
	}
}
