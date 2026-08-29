package web

type skin struct {
	Label   string
	Note    string
	Palette string // Palette is applied when the skin is selected.

	Status string // statusline variant: full | write | quiet
}

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

func resolveSkin(name string) skin {
	if s, ok := skins[name]; ok {
		return s
	}
	return skins[defaultSkin]
}

func skinName(name string) string {
	if _, ok := skins[name]; ok {
		return name
	}
	return defaultSkin
}

func effectiveSkin(cfg Config, prefs userRecord) (string, skin) {
	name := prefs.Skin
	if name == "" {
		name = cfg.Skin
	}
	return skinName(name), resolveSkin(name)
}

func effectivePalette(prefs userRecord, s skin) string {
	if prefs.Palette != "" {
		return prefs.Palette
	}
	return s.Palette
}

func skinPalettes() map[string]string {
	m := make(map[string]string, len(skins))
	for name, s := range skins {
		m[name] = s.Palette
	}
	return m
}
