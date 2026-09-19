package presentation

// SlugPreset is one option in the namespace editor's "quick-create page" slug
// picker. It maps a form value to the Go template written into a namespace's
// new.slug field; the neutral catalogue lives here so browser and HTTP adapters
// resolve the same pattern without importing each other.
type SlugPreset struct {
	Key     string // form value
	Label   string // what the option says
	Pattern string // the Go template written to new.slug
}

// SlugPresetCustom is the slug preset key for a hand-written pattern.
const SlugPresetCustom = "custom"

// SlugPresets lists the quick-create naming patterns in form order.
var SlugPresets = []SlugPreset{
	{"daily", "one page per day", `{{.Now.Format "2006-01-02"}}`},
	{"monthly", "one page per month", `{{.Now.Format "2006-01"}}`},
	{"timestamped", "one page per keystroke, date and time", `{{.Now.Format "2006-01-02-1504"}}`},
	{"daily-per-user", "one page per day, per user", `{{.User}}-{{.Now.Format "2006-01-02"}}`},
}

// SlugPatternFor returns the Go template for a preset key, or "" when the key
// is unknown (including "custom", whose pattern is supplied separately).
func SlugPatternFor(key string) string {
	for _, p := range SlugPresets {
		if p.Key == key {
			return p.Pattern
		}
	}
	return ""
}

// SlugPresetFor returns the preset key matching a pattern, or SlugPresetCustom.
func SlugPresetFor(pattern string) string {
	for _, p := range SlugPresets {
		if p.Pattern == pattern {
			return p.Key
		}
	}
	return SlugPresetCustom
}
