// Package presentation holds neutral presentation catalogue data and
// validation: skin, palette, font and widget identifiers shared by the
// application and browser layers. It has no HTML, CSS, templates or
// transport dependencies.
package presentation

// Skin is a structural presentation skin together with the palette it selects
// by default and the statusline variant it uses.
type Skin struct {
	Label   string
	Note    string
	Palette string // Palette is applied when the skin is selected.
	Status  string // statusline variant: full | write | quiet
}

// DefaultSkin is the skin used when none is configured or a name is unknown.
const DefaultSkin = "phosphor"

// SkinNames lists every selectable skin in display order.
var SkinNames = []string{"phosphor", "newsprint", "journal", "soft", "bare"}

// Skins maps each skin name to its definition.
var Skins = map[string]Skin{
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

// ResolveSkin returns the named skin or the default when the name is unknown.
func ResolveSkin(name string) Skin {
	if s, ok := Skins[name]; ok {
		return s
	}
	return Skins[DefaultSkin]
}

// SkinName returns name when it names a known skin, otherwise the default.
func SkinName(name string) string {
	if _, ok := Skins[name]; ok {
		return name
	}
	return DefaultSkin
}

// ValidSkin reports whether name names a known skin.
func ValidSkin(name string) bool {
	_, ok := Skins[name]
	return ok
}

// SkinPalettes maps each skin to its default palette.
func SkinPalettes() map[string]string {
	m := make(map[string]string, len(Skins))
	for name, s := range Skins {
		m[name] = s.Palette
	}
	return m
}
