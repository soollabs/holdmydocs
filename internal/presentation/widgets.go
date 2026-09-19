package presentation

// WidgetSlot names where a widget mounts in the page chrome.
type WidgetSlot string

const (
	SlotSidebar  WidgetSlot = "sidebar"
	SlotRail     WidgetSlot = "rail"      // right rail (≥1200px only)
	SlotPageHead WidgetSlot = "page-head" // between title and article
	SlotPageFoot WidgetSlot = "page-foot" // after article
)

// Widget is one mountable page widget.
type Widget struct {
	ID          string
	Title       string     // "" = renders no <h2>
	Slot        WidgetSlot // default slot; a skin may override
	Description string     // one line, shown on the settings page
}

// WidgetIDs lists every widget identifier in display order.
var WidgetIDs = []string{
	"pages", "namespaces", "pinned", "tags", "log", "health",
	"calendar", "writing-stats",
	"outline", "page-meta", "backlinks", "prev-entries",
}

// Widgets maps each widget identifier to its definition.
var Widgets = map[string]Widget{
	"pages":      {ID: "pages", Title: "PAGES", Slot: SlotSidebar, Description: "recently edited pages in this namespace, most recent first."},
	"namespaces": {ID: "namespaces", Title: "NAMESPACES", Slot: SlotSidebar, Description: "every namespace in the wiki, with page counts, linking to its index."},
	"pinned":     {ID: "pinned", Title: "PINNED", Slot: SlotSidebar, Description: "pages you've pinned for quick access."},
	"tags":       {ID: "tags", Title: "TAGS", Slot: SlotSidebar, Description: "tags used in this namespace, with page counts."},
	"log":        {ID: "log", Title: "LOG", Slot: SlotSidebar, Description: "the last few commits to the page you're viewing."},
	"health":     {ID: "health", Title: "HEALTH", Slot: SlotSidebar, Description: "missing links and orphaned pages, one click from a full report."},

	"calendar":      {ID: "calendar", Title: "", Slot: SlotSidebar, Description: "a month grid of daily pages, with entries highlighted."},
	"writing-stats": {ID: "writing-stats", Title: "THIS MONTH", Slot: SlotSidebar, Description: "days written, streak, and word count for this month."},

	"outline":      {ID: "outline", Title: "ON THIS PAGE", Slot: SlotRail, Description: "a table of contents built from the headings on the page you're viewing."},
	"page-meta":    {ID: "page-meta", Title: "", Slot: SlotPageHead, Description: "tags, last editor, and revision count for the page you're viewing."},
	"backlinks":    {ID: "backlinks", Title: "linked from", Slot: SlotPageFoot, Description: "other pages that link to this one."},
	"prev-entries": {ID: "prev-entries", Title: "earlier", Slot: SlotPageFoot, Description: "the daily entries just before this one."},
}

// fixedWidgets are application chrome: always mounted, never configurable in a
// namespace's widget list.
var fixedWidgets = map[string]bool{"search": true, "tree": true, "outline": true}

// IsFixedWidget reports whether id names fixed application chrome.
func IsFixedWidget(id string) bool { return fixedWidgets[id] }

// ValidWidget reports whether id names a namespace-configurable widget.
func ValidWidget(id string) bool {
	if fixedWidgets[id] {
		return false
	}
	_, ok := Widgets[id]
	return ok
}

// WidgetSlotGroup is one slot's selectable widgets, for the settings form.
type WidgetSlotGroup struct {
	Slot    WidgetSlot
	Widgets []Widget
}

// WidgetSlotGroups returns the configurable widgets grouped by slot.
func WidgetSlotGroups() []WidgetSlotGroup {
	groups := make([]WidgetSlotGroup, 0, 4)
	for _, slot := range []WidgetSlot{SlotSidebar, SlotRail, SlotPageHead, SlotPageFoot} {
		g := WidgetSlotGroup{Slot: slot}
		for _, id := range WidgetIDs {
			if id == "outline" {
				continue
			}
			if w := Widgets[id]; w.Slot == slot {
				g.Widgets = append(g.Widgets, w)
			}
		}
		groups = append(groups, g)
	}
	return groups
}
