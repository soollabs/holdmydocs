package main

// A skin is the presentation knob: it decides how the app looks (typography,
// spacing, borders, markers — the :root[data-skin] blocks in
// web/static/skins.css), which statusline segments show, and which colour
// palette it arrives with. Widget composition is a namespace property
// (namespace.go); where "/" lands is a config property (Config.Landing);
// whether ctrl-j is live depends on whether the journal namespace has a
// `new:` template (namespace.go) — none of those are a skin's decision.
//
// The palette pairing is not a merge: skins.css still contains no colour at
// all (TestSkinsNoRawHex), and Palette here is just the name of the preset a
// skin looks right in. Choosing a skin resets the palette to it — a
// newspaper that opened in terminal green wasn't a newspaper — and the
// palette picker stays live afterwards for anyone who wants a different one.
type skin struct {
	Label   string
	Note    string // one-line hint in the settings picker
	Palette string // default colour preset; choosing this skin resets to it

	Status string // statusline variant: full | write | quiet
}

// defaultSkin is what an unset or unknown skin name resolves to.
const defaultSkin = "phosphor"

var skinNames = []string{"phosphor", "newsprint", "journal", "soft", "bare"}

var skins = map[string]skin{
	"phosphor": {
		Label:   "phosphor",
		Note:    "terminal green, monospace, # markers — the default",
		Palette: "phosphor",
		Status:  "full",
	},
	"newsprint": {
		Label:   "newsprint",
		Note:    "broadsheet — masthead, serif, justified columns, ink on paper",
		Palette: "solarized",
		Status:  "full",
	},
	"journal": {
		Label:   "journal",
		Note:    "writing first — serif, wide measure, no chrome",
		Palette: "everforest",
		Status:  "write",
	},
	"soft": {
		Label:   "soft",
		Note:    "rounded and low-contrast — warm sans, roomy leading, filled panels",
		Palette: "rosé pine",
		Status:  "full",
	},
	"bare": {
		Label:   "bare",
		Note:    "subtraction only — no borders, no markers, wide margins",
		Palette: "one dark",
		Status:  "quiet",
	},
}

// resolveSkin returns the skin for name, falling back to phosphor for an
// unknown or empty name.
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
