package main

// A skin is the one presentation knob: it decides how the app looks
// (typography, spacing, borders, markers — the :root[data-skin] blocks in
// web/static/skins.css), how it is composed (which widgets mount where,
// where "/" lands, whether ctrl-j is live, which statusline segments show),
// and which colour palette it arrives with.
//
// That last one is a pairing, not a merge: skins.css still contains no
// colour at all (TestSkinsNoRawHex), and Palette here is just the name of
// the preset a skin looks right in. Choosing a skin resets the palette to
// it — a newspaper that opened in terminal green wasn't a newspaper — and
// the palette picker stays live afterwards for anyone who wants a different
// one.
type skin struct {
	Label   string
	Note    string                  // one-line hint in the settings picker
	Palette string                  // default colour preset; choosing this skin resets to it
	Widgets map[widgetSlot][]string // ordered widget ids per slot

	Landing  string // route for "/": home | daily
	DailyKey string // what ctrl-j opens; "" = disabled
	Status   string // statusline variant: full | write | quiet
}

// defaultSkin is what an unset or unknown skin name resolves to.
const defaultSkin = "phosphor"

var skinNames = []string{"phosphor", "newsprint", "journal", "soft", "bare"}

var skins = map[string]skin{
	"phosphor": {
		Label:   "phosphor",
		Note:    "terminal green, monospace, # markers — the default",
		Palette: "phosphor",
		Widgets: map[widgetSlot][]string{
			slotSidebar:  {"identity", "pages", "tags", "log", "keys"},
			slotRail:     {"outline"},
			slotPageHead: {"page-meta"},
			slotPageFoot: {"backlinks"},
		},
		Landing:  "home",
		DailyKey: "daily",
		Status:   "full",
	},
	"newsprint": {
		Label:   "newsprint",
		Note:    "broadsheet — masthead, serif, justified columns, ink on paper",
		Palette: "solarized",
		Widgets: map[widgetSlot][]string{
			slotSidebar:  {"identity", "pages", "tags", "keys"},
			slotRail:     {"outline"},
			slotPageHead: {"page-meta"},
			slotPageFoot: {"backlinks"},
		},
		Landing:  "home",
		DailyKey: "daily",
		Status:   "full",
	},
	"journal": {
		Label:   "journal",
		Note:    "writing first — serif, wide measure, no chrome; lands on today's entry",
		Palette: "everforest",
		Widgets: map[widgetSlot][]string{
			slotSidebar:  {"identity", "calendar", "writing-stats", "pages", "keys"},
			slotPageFoot: {"prev-entries"},
		},
		Landing:  "daily",
		DailyKey: "daily",
		Status:   "write",
	},
	"soft": {
		Label:   "soft",
		Note:    "rounded and low-contrast — warm sans, roomy leading, filled panels",
		Palette: "rosé pine",
		Widgets: map[widgetSlot][]string{
			slotSidebar:  {"identity", "pages", "tags", "keys"},
			slotRail:     {"outline"},
			slotPageHead: {"page-meta"},
			slotPageFoot: {"backlinks"},
		},
		Landing:  "home",
		DailyKey: "daily",
		Status:   "full",
	},
	"bare": {
		Label:   "bare",
		Note:    "subtraction only — no borders, no markers, wide margins",
		Palette: "one dark",
		Widgets: map[widgetSlot][]string{
			slotSidebar: {"identity", "pages", "keys"},
		},
		Landing:  "home",
		DailyKey: "",
		Status:   "quiet",
	},
}

// resolveSkin returns the skin for name, falling back to phosphor for an
// unknown or empty name. Unlike the old contract this always resolves to a
// real skin: the name now decides widget composition too, so there is no
// "render no attribute" state to fall back to.
func resolveSkin(name string) skin {
	if s, ok := skins[name]; ok {
		return s
	}
	return skins[defaultSkin]
}

// skinName returns the resolved name (not the struct) for name.
func skinName(name string) string {
	if _, ok := skins[name]; ok {
		return name
	}
	return defaultSkin
}

// effectiveSkin resolves the skin in effect for a user: their own pref if
// set, else the install-wide default from config.
func effectiveSkin(cfg Config, prefs userRecord) (string, skin) {
	name := prefs.Skin
	if name == "" {
		name = cfg.Skin
	}
	return skinName(name), resolveSkin(name)
}

// effectivePalette is the colour preset in force: the user's own choice if
// they have one, else the skin's default. Without this fallback an install
// whose config sets a non-default skin would serve that skin's structure in
// phosphor's colours until every user visited /settings once.
func effectivePalette(prefs userRecord, s skin) string {
	if prefs.Palette != "" {
		return prefs.Palette
	}
	return s.Palette
}

// skinPalettes is the settings page's skin -> default palette map,
// JSON-encoded so choosing a skin can move the palette selection
// client-side to match what the server will store.
func skinPalettes() map[string]string {
	m := make(map[string]string, len(skins))
	for name, s := range skins {
		m[name] = s.Palette
	}
	return m
}

// skinWidgetIDs is the settings page's skin -> widget ids map, JSON-encoded
// so choosing a skin can re-tick the widget checklist client-side without a
// round trip.
func skinWidgetIDs() map[string][]string {
	m := make(map[string][]string, len(skins))
	for name, s := range skins {
		ids := []string{}
		for _, id := range widgetIDs {
			for _, slotIDs := range s.Widgets {
				for _, mounted := range slotIDs {
					if mounted == id {
						ids = append(ids, id)
					}
				}
			}
		}
		m[name] = ids
	}
	return m
}
