package presentation

type ThemePreset struct {
	Dark  map[string]string `json:"dark"`
	Light map[string]string `json:"light"`
}

var ThemePresetNames = []string{
	"neutral", "phosphor", "dracula", "everforest", "flexoki", "gruvbox",
	"nord", "rosé pine", "solarized", "tokyo night",
}

var ThemePresets = map[string]ThemePreset{
	"neutral": {
		Dark: map[string]string{
			"bg": "#0d1117", "surface": "#151b23", "surface-raised": "#212830",
			"border": "#3d444d", "border-strong": "#4d555e",
			"fg": "#e6edf3", "fg-strong": "#f0f6fc", "fg-muted": "#9198a1", "fg-faint": "#878e98",
			"primary": "#e6edf3", "primary-muted": "#3d444d", "accent": "#4493f8",
			"warning": "#d29922", "warning-bg": "#3b2e1a",
			"danger": "#f8544c", "danger-muted": "#6e2b2b", "danger-bg": "#2d1a1d",
			"accent-fg": "#0d1117",
		},
		Light: map[string]string{
			"bg": "#ffffff", "surface": "#f6f8fa", "surface-raised": "#eff2f5",
			"border": "#d1d9e0", "border-strong": "#c4ccd4",
			"fg": "#1f2328", "fg-strong": "#0d1117", "fg-muted": "#59636e", "fg-faint": "#676f79",
			"primary": "#1f2328", "primary-muted": "#d1d9e0", "accent": "#0969da",
			"warning": "#966400", "warning-bg": "#fff8c5",
			"danger": "#d1242f", "danger-muted": "#ffcecb", "danger-bg": "#ffebe9",
			"accent-fg": "#ffffff",
		},
	},
	"phosphor": {
		Dark: map[string]string{
			"bg": "#0b0f14", "surface": "#0e141b", "surface-raised": "#121924",
			"border": "#1d2733", "border-strong": "#2a3644",
			"fg": "#c6d0da", "fg-strong": "#e6edf3", "fg-muted": "#8b98a5", "fg-faint": "#71818f",
			"primary": "#56d364", "primary-muted": "#2e5b3a", "accent": "#79b8ff",
			"warning": "#e3b341", "warning-bg": "#3a3113",
			"danger": "#f47067", "danger-muted": "#6e3a3f", "danger-bg": "#2c1618",
			"accent-fg": "#0b0f14",
		},
		Light: map[string]string{
			"bg": "#f7f8f6", "surface": "#eef1ec", "surface-raised": "#e6eae4",
			"border": "#d8ded6", "border-strong": "#c4cdc6",
			"fg": "#2d3438", "fg-strong": "#1c2226", "fg-muted": "#525e64", "fg-faint": "#637076",
			"primary": "#1a7d36", "primary-muted": "#a4c7ab", "accent": "#306bc6",
			"warning": "#946300", "warning-bg": "#fff0c2",
			"danger": "#cf222e", "danger-muted": "#e0a9ad", "danger-bg": "#fbe9e9",
			"accent-fg": "#ffffff",
		},
	},

	// Flexoki by Steph Ango, MIT licence: https://stephango.com/flexoki
	"flexoki": {
		Dark: map[string]string{
			"bg": "#100f0f", "surface": "#1c1b1a", "surface-raised": "#282726",
			"border": "#343331", "border-strong": "#403e3c",
			"fg": "#cecdc3", "fg-strong": "#f2f0e5", "fg-muted": "#9f9d96", "fg-faint": "#878580",
			"primary": "#3aa99f", "primary-muted": "#1f4541", "accent": "#66a0c8",
			"warning": "#d0a215", "warning-bg": "#362c10",
			"danger": "#e8705f", "danger-muted": "#5d2823", "danger-bg": "#2d1816",
			"accent-fg": "#100f0f",
		},
		Light: map[string]string{
			"bg": "#fffcf0", "surface": "#f2f0e5", "surface-raised": "#e6e4d9",
			"border": "#dad8ce", "border-strong": "#cecdc3",
			"fg": "#1c1b1a", "fg-strong": "#100f0f", "fg-muted": "#575653", "fg-faint": "#6a6964",
			"primary": "#1c6c66", "primary-muted": "#b2d2c7", "accent": "#205ea6",
			"warning": "#8a6801", "warning-bg": "#faeec6",
			"danger": "#af3029", "danger-muted": "#e3b5aa", "danger-bg": "#ffe1d5",
			"accent-fg": "#fffcf0",
		},
	},
	"gruvbox": {
		Dark: map[string]string{
			"bg": "#282828", "surface": "#1d2021", "surface-raised": "#32302f",
			"border": "#3c3836", "border-strong": "#504945",
			"fg": "#ebdbb2", "fg-strong": "#fbf1c7", "fg-muted": "#a89984", "fg-faint": "#928374",
			"primary": "#b8bb26", "primary-muted": "#4e5a2a", "accent": "#83a598",
			"warning": "#fabd2f", "warning-bg": "#4a3f1c",
			"danger": "#fb4934", "danger-muted": "#6e3128", "danger-bg": "#3c2422",
			"accent-fg": "#282828",
		},
		Light: map[string]string{
			"bg": "#fbf1c7", "surface": "#f2e5bc", "surface-raised": "#ebdbb2",
			"border": "#d5c4a1", "border-strong": "#bdae93",
			"fg": "#3c3836", "fg-strong": "#282828", "fg-muted": "#5e544d", "fg-faint": "#71655b",
			"primary": "#6e690d", "primary-muted": "#b9c48a", "accent": "#076678",
			"warning": "#8e5c10", "warning-bg": "#f4e0a8",
			"danger": "#9d0006", "danger-muted": "#dfa8a2", "danger-bg": "#f2d5cd",
			"accent-fg": "#ffffff",
		},
	},
	"nord": {
		Dark: map[string]string{
			"bg": "#2e3440", "surface": "#3b4252", "surface-raised": "#434c5e",
			"border": "#434c5e", "border-strong": "#4c566a",
			"fg": "#d8dee9", "fg-strong": "#eceff4", "fg-muted": "#a1acc0", "fg-faint": "#6f7d98",
			"primary": "#a3be8c", "primary-muted": "#46543e", "accent": "#88c0d0",
			"warning": "#ebcb8b", "warning-bg": "#4a4433",
			"danger": "#bf616a", "danger-muted": "#6a3f47", "danger-bg": "#3d2b31",
			"accent-fg": "#2e3440",
		},
		Light: map[string]string{
			"bg": "#eceff4", "surface": "#e5e9f0", "surface-raised": "#d8dee9",
			"border": "#d8dee9", "border-strong": "#c2c9d6",
			"fg": "#2e3440", "fg-strong": "#242933", "fg-muted": "#4c566a", "fg-faint": "#5d6982",
			"primary": "#517140", "primary-muted": "#b8ccab", "accent": "#4b6a91",
			"warning": "#80652f", "warning-bg": "#f3e6c8",
			"danger": "#ae4750", "danger-muted": "#e2b6bb", "danger-bg": "#f6e4e6",
			"accent-fg": "#ffffff",
		},
	},
	"dracula": {
		Dark: map[string]string{
			"bg": "#282a36", "surface": "#21222c", "surface-raised": "#343746",
			"border": "#44475a", "border-strong": "#565a75",
			"fg": "#f8f8f2", "fg-strong": "#ffffff", "fg-muted": "#a8abbe", "fg-faint": "#6272a4",
			"primary": "#50fa7b", "primary-muted": "#29473a", "accent": "#8be9fd",
			"warning": "#f1fa8c", "warning-bg": "#46442a",
			"danger": "#ff5555", "danger-muted": "#6e3438", "danger-bg": "#3a2528",
			"accent-fg": "#282a36",
		},
		Light: map[string]string{
			"bg": "#f8f8f2", "surface": "#efefe9", "surface-raised": "#e5e5df",
			"border": "#d5d5cf", "border-strong": "#c0c0ba",
			"fg": "#1f1f2e", "fg-strong": "#14141f", "fg-muted": "#55556a", "fg-faint": "#5b6a9c",
			"primary": "#14710a", "primary-muted": "#a9d2a4", "accent": "#036a96",
			"warning": "#806b14", "warning-bg": "#f4ecc0",
			"danger": "#c73929", "danger-muted": "#e5aca5", "danger-bg": "#f7e0dc",
			"accent-fg": "#ffffff",
		},
	},
	"everforest": {
		Dark: map[string]string{
			"bg": "#2d353b", "surface": "#272e33", "surface-raised": "#343f44",
			"border": "#414b50", "border-strong": "#56635f",
			"fg": "#d3c6aa", "fg-strong": "#f2efdf", "fg-muted": "#9da9a0", "fg-faint": "#859289",
			"primary": "#a7c080", "primary-muted": "#425047", "accent": "#7fbbb3",
			"warning": "#dbbc7f", "warning-bg": "#45443c",
			"danger": "#e67e80", "danger-muted": "#6b3a3d", "danger-bg": "#3c2b2e",
			"accent-fg": "#2d353b",
		},
		Light: map[string]string{
			"bg": "#fdf6e3", "surface": "#f4f0d9", "surface-raised": "#efebd4",
			"border": "#e0dcc7", "border-strong": "#bdc3af",
			"fg": "#414b51", "fg-strong": "#303a3f", "fg-muted": "#525e52", "fg-faint": "#647062",
			"primary": "#657301", "primary-muted": "#c4ce9c", "accent": "#2d739a",
			"warning": "#8e6600", "warning-bg": "#f5e3c0",
			"danger": "#d90d09", "danger-muted": "#f0b1ac", "danger-bg": "#fbdedb",
			"accent-fg": "#ffffff",
		},
	},

	"rosé pine": {
		Dark: map[string]string{
			"bg": "#191724", "surface": "#1f1d2e", "surface-raised": "#26233a",
			"border": "#403d52", "border-strong": "#524f67",
			"fg": "#e0def4", "fg-strong": "#f2f0fa", "fg-muted": "#908caa", "fg-faint": "#6e6a86",
			"primary": "#9ccfd8", "primary-muted": "#2f4b52", "accent": "#c4a7e7",
			"warning": "#f6c177", "warning-bg": "#45392a",
			"danger": "#eb6f92", "danger-muted": "#5f2f42", "danger-bg": "#33212b",
			"accent-fg": "#191724",
		},
		Light: map[string]string{
			"bg": "#faf4ed", "surface": "#fffaf3", "surface-raised": "#f2e9e1",
			"border": "#dfdad9", "border-strong": "#cecacd",
			"fg": "#4d496b", "fg-strong": "#3c3954", "fg-muted": "#5e5b75", "fg-faint": "#726d82",
			"primary": "#286983", "primary-muted": "#aecdd6", "accent": "#7e649b",
			"warning": "#a46611", "warning-bg": "#f7e4c6",
			"danger": "#ad546d", "danger-muted": "#e0b7c2", "danger-bg": "#f5e2e7",
			"accent-fg": "#ffffff",
		},
	},
	"tokyo night": {
		Dark: map[string]string{
			"bg": "#1a1b26", "surface": "#16161e", "surface-raised": "#1f2335",
			"border": "#292e42", "border-strong": "#3b4261",
			"fg": "#a9b1d6", "fg-strong": "#c0caf5", "fg-muted": "#7e829d", "fg-faint": "#5c6692",
			"primary": "#9ece6a", "primary-muted": "#33452b", "accent": "#7aa2f7",
			"warning": "#e0af68", "warning-bg": "#40381f",
			"danger": "#f7768e", "danger-muted": "#6d3541", "danger-bg": "#372028",
			"accent-fg": "#1a1b26",
		},
		Light: map[string]string{
			"bg": "#e1e2e7", "surface": "#d5d6db", "surface-raised": "#cbccd1",
			"border": "#b9bac1", "border-strong": "#a1a6c5",
			"fg": "#343b58", "fg-strong": "#24283b", "fg-muted": "#494c5d", "fg-faint": "#595d6f",
			"primary": "#485e30", "primary-muted": "#b3c9a2", "accent": "#145aba",
			"warning": "#805413", "warning-bg": "#f0e0bb",
			"danger": "#ba093c", "danger-muted": "#e8a9b7", "danger-bg": "#f7dde3",
			"accent-fg": "#ffffff",
		},
	},
	"solarized": {
		Dark: map[string]string{
			"bg": "#002b36", "surface": "#073642", "surface-raised": "#06333e",
			"border": "#0e4c5c", "border-strong": "#16606f",
			"fg": "#839496", "fg-strong": "#93a1a1", "fg-muted": "#7c929a", "fg-faint": "#5f767e",
			"primary": "#268bd2", "primary-muted": "#244d5f", "accent": "#6c71c4",
			"warning": "#c09100", "warning-bg": "#3a3413",
			"danger": "#dc322f", "danger-muted": "#6e2a28", "danger-bg": "#2f1a19",
			"accent-fg": "#002b36",
		},
		Light: map[string]string{
			"bg": "#fdf6e3", "surface": "#eee8d5", "surface-raised": "#e4ddc8",
			"border": "#d9d2bc", "border-strong": "#c8c0a8",
			"fg": "#3a484d", "fg-strong": "#073642", "fg-muted": "#495a5f", "fg-faint": "#5e6b6b",
			"primary": "#1e6da5", "primary-muted": "#b2cfe0", "accent": "#585ebc",
			"warning": "#846400", "warning-bg": "#f5e8b8",
			"danger": "#ca2522", "danger-muted": "#e8b0a8", "danger-bg": "#f8e2dc",
			"accent-fg": "#ffffff",
		},
	},
}

// ValidPalette reports whether name names a known colour palette.
func ValidPalette(name string) bool {
	_, ok := ThemePresets[name]
	return ok
}
