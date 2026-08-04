package main

// themePreset supplies values for all 17 theme variables in each mode.
// Presets are fill data for the settings colour inputs, not a stored mode:
// applying one just submits its values through the existing theme form.
type themePreset struct {
	Dark  map[string]string `json:"dark"`
	Light map[string]string `json:"light"`
}

// themePresetNames fixes the display order of the preset selector.
// phosphor (empty value) is the built-in default; it's included here for the
// UI but not stored when selected — an absent palette key renders phosphor.
var themePresetNames = []string{
	"phosphor", "catppuccin", "dracula", "everforest", "gruvbox", "monokai",
	"nord", "one dark", "rosé pine", "solarized", "tokyo night",
}

// Colour values come from the public palette specs (Catppuccin Mocha/Latte,
// Gruvbox, Nord, Solarized); muted and background variants are derived to
// match their semantic roles. phosphor is the built-in default.
var themePresets = map[string]themePreset{
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
			"fg": "#2d3438", "fg-strong": "#1c2226", "fg-muted": "#5c6a70", "fg-faint": "#647177",
			"primary": "#1a7f37", "primary-muted": "#a4c7ab", "accent": "#316dca",
			"warning": "#9a6700", "warning-bg": "#fff0c2",
			"danger": "#cf222e", "danger-muted": "#e0a9ad", "danger-bg": "#fbe9e9",
			"accent-fg": "#ffffff",
		},
	},
	"catppuccin": {
		Dark: map[string]string{
			"bg": "#1e1e2e", "surface": "#181825", "surface-raised": "#11111b",
			"border": "#313244", "border-strong": "#45475a",
			"fg": "#cdd6f4", "fg-strong": "#f5e0dc", "fg-muted": "#a6adc8", "fg-faint": "#7f849c",
			"primary": "#a6e3a1", "primary-muted": "#3c5e46", "accent": "#89b4fa",
			"warning": "#f9e2af", "warning-bg": "#45412e",
			"danger": "#f38ba8", "danger-muted": "#714252", "danger-bg": "#36222c",
			"accent-fg": "#1e1e2e",
		},
		Light: map[string]string{
			"bg": "#eff1f5", "surface": "#e6e9ef", "surface-raised": "#dce0e8",
			"border": "#ccd0da", "border-strong": "#bcc0cc",
			"fg": "#4c4f69", "fg-strong": "#303446", "fg-muted": "#6a6d82", "fg-faint": "#8c8fa1",
			"primary": "#40a02b", "primary-muted": "#a8d0a0", "accent": "#1e66f5",
			"warning": "#df8e1d", "warning-bg": "#f7e4c3",
			"danger": "#d20f39", "danger-muted": "#e4a3ae", "danger-bg": "#f6dbe0",
			"accent-fg": "#303446",
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
			"fg": "#3c3836", "fg-strong": "#282828", "fg-muted": "#665c54", "fg-faint": "#7c6f64",
			"primary": "#79740e", "primary-muted": "#b9c48a", "accent": "#076678",
			"warning": "#b57614", "warning-bg": "#f4e0a8",
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
			"fg": "#2e3440", "fg-strong": "#242933", "fg-muted": "#4c566a", "fg-faint": "#616e88",
			"primary": "#567844", "primary-muted": "#b8ccab", "accent": "#5e81ac",
			"warning": "#a2803c", "warning-bg": "#f3e6c8",
			"danger": "#bf616a", "danger-muted": "#e2b6bb", "danger-bg": "#f6e4e6",
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
			"fg": "#1f1f2e", "fg-strong": "#14141f", "fg-muted": "#55556a", "fg-faint": "#6272a4",
			"primary": "#14710a", "primary-muted": "#a9d2a4", "accent": "#036a96",
			"warning": "#846e15", "warning-bg": "#f4ecc0",
			"danger": "#cb3a2a", "danger-muted": "#e5aca5", "danger-bg": "#f7e0dc",
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
			"fg": "#5c6a72", "fg-strong": "#3a464c", "fg-muted": "#667466", "fg-faint": "#839181",
			"primary": "#8da101", "primary-muted": "#c4ce9c", "accent": "#3a94c5",
			"warning": "#dfa000", "warning-bg": "#f5e3c0",
			"danger": "#f85552", "danger-muted": "#f0b1ac", "danger-bg": "#fbdedb",
			"accent-fg": "#3a464c",
		},
	},
	"monokai": {
		Dark: map[string]string{
			"bg": "#272822", "surface": "#1e1f1c", "surface-raised": "#32332c",
			"border": "#3e3d32", "border-strong": "#524f3d",
			"fg": "#f8f8f2", "fg-strong": "#ffffff", "fg-muted": "#a59f85", "fg-faint": "#75715e",
			"primary": "#a6e22e", "primary-muted": "#3f4c1e", "accent": "#66d9ef",
			"warning": "#e6db74", "warning-bg": "#45432a",
			"danger": "#f92672", "danger-muted": "#6a2340", "danger-bg": "#391b28",
			"accent-fg": "#272822",
		},
		Light: map[string]string{
			"bg": "#faf6f0", "surface": "#f0ece5", "surface-raised": "#e6e2da",
			"border": "#d5d1c9", "border-strong": "#c0bcb2",
			"fg": "#2c292d", "fg-strong": "#16131a", "fg-muted": "#706b6e", "fg-faint": "#918f8a",
			"primary": "#269d69", "primary-muted": "#a9d6bf", "accent": "#1c8ca8",
			"warning": "#99621e", "warning-bg": "#f3e4c2",
			"danger": "#e14775", "danger-muted": "#eeadc0", "danger-bg": "#f9dfe7",
			"accent-fg": "#16131a",
		},
	},
	"one dark": {
		Dark: map[string]string{
			"bg": "#282c34", "surface": "#21252b", "surface-raised": "#2c313a",
			"border": "#3e4451", "border-strong": "#4b5263",
			"fg": "#abb2bf", "fg-strong": "#e6e6e6", "fg-muted": "#8d94a0", "fg-faint": "#6e7786",
			"primary": "#98c379", "primary-muted": "#35492d", "accent": "#61afef",
			"warning": "#e5c07b", "warning-bg": "#423a25",
			"danger": "#e06c75", "danger-muted": "#663036", "danger-bg": "#362225",
			"accent-fg": "#282c34",
		},
		Light: map[string]string{
			"bg": "#fafafa", "surface": "#f0f0f1", "surface-raised": "#e5e5e6",
			"border": "#d4d4d5", "border-strong": "#c0c0c2",
			"fg": "#383a42", "fg-strong": "#202227", "fg-muted": "#696c77", "fg-faint": "#909198",
			"primary": "#50a14f", "primary-muted": "#b0d2af", "accent": "#4078f2",
			"warning": "#986801", "warning-bg": "#f2e3bc",
			"danger": "#e45649", "danger-muted": "#efb3ad", "danger-bg": "#fae1de",
			"accent-fg": "#202227",
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
			"fg": "#575279", "fg-strong": "#464261", "fg-muted": "#716d8c", "fg-faint": "#908b9e",
			"primary": "#286983", "primary-muted": "#aecdd6", "accent": "#907aa9",
			"warning": "#ea9d34", "warning-bg": "#f7e4c6",
			"danger": "#b4637a", "danger-muted": "#e0b7c2", "danger-bg": "#f5e2e7",
			"accent-fg": "#464261",
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
			"fg": "#343b58", "fg-strong": "#24283b", "fg-muted": "#565a6e", "fg-faint": "#7a7f95",
			"primary": "#485e30", "primary-muted": "#b3c9a2", "accent": "#2e7de9",
			"warning": "#8f5e15", "warning-bg": "#f0e0bb",
			"danger": "#f52a65", "danger-muted": "#e8a9b7", "danger-bg": "#f7dde3",
			"accent-fg": "#24283b",
		},
	},
	"solarized": {
		Dark: map[string]string{
			"bg": "#002b36", "surface": "#073642", "surface-raised": "#06333e",
			"border": "#0e4c5c", "border-strong": "#16606f",
			"fg": "#839496", "fg-strong": "#93a1a1", "fg-muted": "#7c929a", "fg-faint": "#5f767e",
			"primary": "#268bd2", "primary-muted": "#244d5f", "accent": "#6c71c4",
			"warning": "#b58900", "warning-bg": "#3a3413",
			"danger": "#dc322f", "danger-muted": "#6e2a28", "danger-bg": "#2f1a19",
			"accent-fg": "#002b36",
		},
		Light: map[string]string{
			"bg": "#fdf6e3", "surface": "#eee8d5", "surface-raised": "#e4ddc8",
			"border": "#d9d2bc", "border-strong": "#c8c0a8",
			"fg": "#586e75", "fg-strong": "#073642", "fg-muted": "#5e737a", "fg-faint": "#809090",
			"primary": "#268bd2", "primary-muted": "#b2cfe0", "accent": "#6c71c4",
			"warning": "#b58900", "warning-bg": "#f5e8b8",
			"danger": "#dc322f", "danger-muted": "#e8b0a8", "danger-bg": "#f8e2dc",
			"accent-fg": "#073642",
		},
	},
}
