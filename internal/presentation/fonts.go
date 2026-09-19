package presentation

// FontStacks maps a font family name to its CSS font stack.
var FontStacks = map[string]string{
	"jetbrains mono": `"JetBrains Mono", ui-monospace, monospace`,
	"system mono":    `ui-monospace, "SF Mono", Menlo, Consolas, monospace`,
	"courier":        `"Courier New", Courier, monospace`,
	"system sans":    `system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`,
	"helvetica":      `"Helvetica Neue", Helvetica, Arial, sans-serif`,
	"verdana":        `Verdana, Geneva, sans-serif`,
	"georgia":        `Georgia, "Times New Roman", serif`,
	"palatino":       `Palatino, "Palatino Linotype", "Book Antiqua", serif`,
	"charter":        `Charter, "Bitstream Charter", Cambria, serif`,
}

// FontsMono, FontsSans and FontsSerif list the selectable font families by kind.
var (
	FontsMono  = []string{"jetbrains mono", "system mono", "courier"}
	FontsSans  = []string{"system sans", "helvetica", "verdana"}
	FontsSerif = []string{"georgia", "palatino", "charter"}
)

// ValidFont reports whether name names a selectable font family.
func ValidFont(name string) bool {
	_, ok := FontStacks[name]
	return ok
}
