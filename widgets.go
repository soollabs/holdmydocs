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
	Tmpl  string     // template name in web/templates/widgets/
}

var widgetIDs = []string{
	"identity", "search", "pages", "pinned", "tags", "log", "health", "keys",
	"calendar", "writing-stats", "inbox", "sources",
	"outline", "source-card", "page-meta", "backlinks", "prev-entries",
}

var widgets = map[string]widget{
	"identity": {ID: "identity", Title: "", Slot: slotSidebar, Tmpl: "widget-identity"},
	"search":   {ID: "search", Title: "SEARCH", Slot: slotSidebar, Tmpl: "widget-search"},
	"pages":    {ID: "pages", Title: "PAGES", Slot: slotSidebar, Tmpl: "widget-pages"},
	"pinned":   {ID: "pinned", Title: "PINNED", Slot: slotSidebar, Tmpl: "widget-pinned"},
	"tags":     {ID: "tags", Title: "TAGS", Slot: slotSidebar, Tmpl: "widget-tags"},
	"log":      {ID: "log", Title: "LOG", Slot: slotSidebar, Tmpl: "widget-log"},
	"health":   {ID: "health", Title: "HEALTH", Slot: slotSidebar, Tmpl: "widget-health"},
	"keys":     {ID: "keys", Title: "", Slot: slotSidebar, Tmpl: "widget-keys"},

	"calendar":       {ID: "calendar", Title: "", Slot: slotSidebar, Tmpl: "widget-calendar"},
	"writing-stats":  {ID: "writing-stats", Title: "THIS MONTH", Slot: slotSidebar, Tmpl: "widget-writing-stats"},
	"inbox":          {ID: "inbox", Title: "INBOX", Slot: slotSidebar, Tmpl: "widget-inbox"},
	"sources":        {ID: "sources", Title: "SOURCES", Slot: slotSidebar, Tmpl: "widget-sources"},

	"outline":       {ID: "outline", Title: "ON THIS PAGE", Slot: slotRail, Tmpl: "widget-outline"},
	"source-card":   {ID: "source-card", Title: "", Slot: slotPageHead, Tmpl: "widget-source-card"},
	"page-meta":     {ID: "page-meta", Title: "", Slot: slotPageHead, Tmpl: "widget-page-meta"},
	"backlinks":     {ID: "backlinks", Title: "linked from", Slot: slotPageFoot, Tmpl: "widget-backlinks"},
	"prev-entries":  {ID: "prev-entries", Title: "earlier", Slot: slotPageFoot, Tmpl: "widget-prev-entries"},
}

// defaultProfile is the set of widgets for each built-in profile.
// widget implementation builds a no-op profile — profile implementation will add the five profiles.
var defaultProfile = map[widgetSlot][]string{
	slotSidebar:    {"identity", "search", "pages", "tags", "log", "health", "keys"},
	slotRail:       {"outline"},
	slotPageHead:   {"page-meta"},
	slotPageFoot:   {"backlinks"},
	slotStatusline: {}, // populated by statusline widget
}

type widgetRow struct {
	widget *widget
	slot   widgetSlot
}

// widgetsForSlot returns the widgets that render in a given slot, in order.
func widgetsForSlot(slot widgetSlot, profile map[widgetSlot][]string) []*widget {
	ids := profile[slot]
	result := make([]*widget, 0, len(ids))
	for _, id := range ids {
		if w, ok := widgets[id]; ok {
			result = append(result, &w)
		}
	}
	return result
}
