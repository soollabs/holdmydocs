package main

// profile is a named, ordered widget arrangement plus presentation defaults.
// Switching profile changes presentation only — storage (flat markdown, one
// commit per save, wiki-links, backlinks) is identical across profiles.
//
// Landing/DailyKey are recorded per profile but not yet wired to
// routing — "/" still always resolves to the configured home page, and
// ctrl-j is still hardcoded in app.js to today's daily/ page. Widget
// arrangement (the part under test) is fully wired; per-profile routing
// (journal landing in edit mode, clipper's disabled ctrl-j, differing
// statusline segments) is follow-up work.
type profile struct {
	Name     string
	Skin     string                  // default skin; user's own Skin still wins
	Widgets  map[widgetSlot][]string // ordered widget ids per slot
	Landing  string                  // route for "/" — not yet wired, see note above
	DailyKey string                  // what ctrl-j opens; "" = disabled — not yet wired
}

var profileNames = []string{"docs", "journal", "clipper", "research", "minimal"}

// profiles is the fixed composition matrix from specs/2026-07-25-profiles-widgets.md
// section 3a. Widget order within a slot is normative.
var profiles = map[string]profile{
	"docs": {
		Name: "docs",
		Skin: "phosphor",
		Widgets: map[widgetSlot][]string{
			slotSidebar:  {"identity", "pages", "tags", "log", "keys"},
			slotRail:     {"outline"},
			slotPageHead: {"page-meta"},
			slotPageFoot: {"backlinks"},
		},
		Landing:  "home",
		DailyKey: "daily",
	},
	"journal": {
		Name: "journal",
		Skin: "manuscript",
		Widgets: map[widgetSlot][]string{
			slotSidebar:  {"identity", "calendar", "writing-stats", "pages", "keys"},
			slotPageFoot: {"prev-entries"},
		},
		Landing:  "daily",
		DailyKey: "daily",
	},
	"clipper": {
		Name: "clipper",
		Skin: "index",
		Widgets: map[widgetSlot][]string{
			slotSidebar:  {"identity", "search", "inbox", "sources", "tags", "keys"},
			slotRail:     {"outline"},
			slotPageHead: {"source-card", "page-meta"},
			slotPageFoot: {"backlinks"},
		},
		Landing:  "inbox",
		DailyKey: "",
	},
	"research": {
		Name: "research",
		Skin: "phosphor",
		Widgets: map[widgetSlot][]string{
			slotSidebar:  {"identity", "search", "pages", "pinned", "sources", "tags", "log", "health", "keys"},
			slotRail:     {"outline"},
			slotPageHead: {"source-card", "page-meta"},
			slotPageFoot: {"backlinks"},
		},
		Landing:  "home",
		DailyKey: "daily",
	},
	"minimal": {
		Name:    "minimal",
		Skin:    "bare",
		Widgets: map[widgetSlot][]string{slotSidebar: {"identity", "pages"}},
		Landing: "home",
	},
}

// resolveProfile returns the profile for name, falling back to "docs" for
// an unknown or empty name (same fallback contract as Skin/Palette).
func resolveProfile(name string) profile {
	if p, ok := profiles[name]; ok {
		return p
	}
	return profiles["docs"]
}

// effectiveProfile resolves the profile in effect for a user: their own
// Profile pref if set, else the site-wide default.
func effectiveProfile(cfg Config, prefs userRecord) profile {
	name := prefs.Profile
	if name == "" {
		name = cfg.Profile
	}
	return resolveProfile(name)
}
