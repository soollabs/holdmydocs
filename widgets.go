package main

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
