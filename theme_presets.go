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
// Gruvbox, Nord, Solarized); the dim/bg accent shades are derived to match
// the roles the default theme uses them for. phosphor is the built-in default.
var themePresets = map[string]themePreset{
	"phosphor": {
		Dark: map[string]string{
			"bg": "#0b0f14", "panel": "#0e141b", "panel-2": "#121924",
			"border": "#1d2733", "border-2": "#2a3644",
			"fg": "#c6d0da", "fg-bright": "#e6edf3", "fg-muted": "#8b98a5", "fg-faint": "#71818f",
			"green": "#56d364", "green-dim": "#2e5b3a", "blue": "#79b8ff",
			"amber": "#e3b341", "amber-bg": "#3a3113",
			"red": "#f47067", "red-dim": "#6e3a3f", "red-bg": "#2c1618",
		},
		Light: map[string]string{
			"bg": "#f7f8f6", "panel": "#eef1ec", "panel-2": "#e6eae4",
			"border": "#d8ded6", "border-2": "#c4cdc6",
			"fg": "#2d3438", "fg-bright": "#1c2226", "fg-muted": "#5c6a70", "fg-faint": "#647177",
			"green": "#1a7f37", "green-dim": "#a4c7ab", "blue": "#316dca",
			"amber": "#9a6700", "amber-bg": "#fff0c2",
			"red": "#cf222e", "red-dim": "#e0a9ad", "red-bg": "#fbe9e9",
		},
	},
	"catppuccin": {
		Dark: map[string]string{
			"bg": "#1e1e2e", "panel": "#181825", "panel-2": "#11111b",
			"border": "#313244", "border-2": "#45475a",
			"fg": "#cdd6f4", "fg-bright": "#f5e0dc", "fg-muted": "#a6adc8", "fg-faint": "#7f849c",
			"green": "#a6e3a1", "green-dim": "#3c5e46", "blue": "#89b4fa",
			"amber": "#f9e2af", "amber-bg": "#45412e",
			"red": "#f38ba8", "red-dim": "#714252", "red-bg": "#36222c",
		},
		Light: map[string]string{
			"bg": "#eff1f5", "panel": "#e6e9ef", "panel-2": "#dce0e8",
			"border": "#ccd0da", "border-2": "#bcc0cc",
			"fg": "#4c4f69", "fg-bright": "#303446", "fg-muted": "#6c6f85", "fg-faint": "#8c8fa1",
			"green": "#40a02b", "green-dim": "#a8d0a0", "blue": "#1e66f5",
			"amber": "#df8e1d", "amber-bg": "#f7e4c3",
			"red": "#d20f39", "red-dim": "#e4a3ae", "red-bg": "#f6dbe0",
		},
	},
	"gruvbox": {
		Dark: map[string]string{
			"bg": "#282828", "panel": "#1d2021", "panel-2": "#32302f",
			"border": "#3c3836", "border-2": "#504945",
			"fg": "#ebdbb2", "fg-bright": "#fbf1c7", "fg-muted": "#a89984", "fg-faint": "#928374",
			"green": "#b8bb26", "green-dim": "#4e5a2a", "blue": "#83a598",
			"amber": "#fabd2f", "amber-bg": "#4a3f1c",
			"red": "#fb4934", "red-dim": "#6e3128", "red-bg": "#3c2422",
		},
		Light: map[string]string{
			"bg": "#fbf1c7", "panel": "#f2e5bc", "panel-2": "#ebdbb2",
			"border": "#d5c4a1", "border-2": "#bdae93",
			"fg": "#3c3836", "fg-bright": "#282828", "fg-muted": "#665c54", "fg-faint": "#7c6f64",
			"green": "#79740e", "green-dim": "#b9c48a", "blue": "#076678",
			"amber": "#b57614", "amber-bg": "#f4e0a8",
			"red": "#9d0006", "red-dim": "#dfa8a2", "red-bg": "#f2d5cd",
		},
	},
	"nord": {
		Dark: map[string]string{
			"bg": "#2e3440", "panel": "#3b4252", "panel-2": "#434c5e",
			"border": "#434c5e", "border-2": "#4c566a",
			"fg": "#d8dee9", "fg-bright": "#eceff4", "fg-muted": "#a1acc0", "fg-faint": "#616e88",
			"green": "#a3be8c", "green-dim": "#46543e", "blue": "#88c0d0",
			"amber": "#ebcb8b", "amber-bg": "#4a4433",
			"red": "#bf616a", "red-dim": "#6a3f47", "red-bg": "#3d2b31",
		},
		Light: map[string]string{
			"bg": "#eceff4", "panel": "#e5e9f0", "panel-2": "#d8dee9",
			"border": "#d8dee9", "border-2": "#c2c9d6",
			"fg": "#2e3440", "fg-bright": "#242933", "fg-muted": "#4c566a", "fg-faint": "#616e88",
			"green": "#567844", "green-dim": "#b8ccab", "blue": "#5e81ac",
			"amber": "#a2803c", "amber-bg": "#f3e6c8",
			"red": "#bf616a", "red-dim": "#e2b6bb", "red-bg": "#f6e4e6",
		},
	},
	"dracula": {
		Dark: map[string]string{
			"bg": "#282a36", "panel": "#21222c", "panel-2": "#343746",
			"border": "#44475a", "border-2": "#565a75",
			"fg": "#f8f8f2", "fg-bright": "#ffffff", "fg-muted": "#a8abbe", "fg-faint": "#6272a4",
			"green": "#50fa7b", "green-dim": "#29473a", "blue": "#8be9fd",
			"amber": "#f1fa8c", "amber-bg": "#46442a",
			"red": "#ff5555", "red-dim": "#6e3438", "red-bg": "#3a2528",
		},
		Light: map[string]string{
			"bg": "#f8f8f2", "panel": "#efefe9", "panel-2": "#e5e5df",
			"border": "#d5d5cf", "border-2": "#c0c0ba",
			"fg": "#1f1f2e", "fg-bright": "#14141f", "fg-muted": "#55556a", "fg-faint": "#6272a4",
			"green": "#14710a", "green-dim": "#a9d2a4", "blue": "#036a96",
			"amber": "#846e15", "amber-bg": "#f4ecc0",
			"red": "#cb3a2a", "red-dim": "#e5aca5", "red-bg": "#f7e0dc",
		},
	},
	"everforest": {
		Dark: map[string]string{
			"bg": "#2d353b", "panel": "#272e33", "panel-2": "#343f44",
			"border": "#414b50", "border-2": "#56635f",
			"fg": "#d3c6aa", "fg-bright": "#f2efdf", "fg-muted": "#9da9a0", "fg-faint": "#859289",
			"green": "#a7c080", "green-dim": "#425047", "blue": "#7fbbb3",
			"amber": "#dbbc7f", "amber-bg": "#45443c",
			"red": "#e67e80", "red-dim": "#6b3a3d", "red-bg": "#3c2b2e",
		},
		Light: map[string]string{
			"bg": "#fdf6e3", "panel": "#f4f0d9", "panel-2": "#efebd4",
			"border": "#e0dcc7", "border-2": "#bdc3af",
			"fg": "#5c6a72", "fg-bright": "#3a464c", "fg-muted": "#829181", "fg-faint": "#939f91",
			"green": "#8da101", "green-dim": "#c4ce9c", "blue": "#3a94c5",
			"amber": "#dfa000", "amber-bg": "#f5e3c0",
			"red": "#f85552", "red-dim": "#f0b1ac", "red-bg": "#fbdedb",
		},
	},
	"monokai": {
		Dark: map[string]string{
			"bg": "#272822", "panel": "#1e1f1c", "panel-2": "#32332c",
			"border": "#3e3d32", "border-2": "#524f3d",
			"fg": "#f8f8f2", "fg-bright": "#ffffff", "fg-muted": "#a59f85", "fg-faint": "#75715e",
			"green": "#a6e22e", "green-dim": "#3f4c1e", "blue": "#66d9ef",
			"amber": "#e6db74", "amber-bg": "#45432a",
			"red": "#f92672", "red-dim": "#6a2340", "red-bg": "#391b28",
		},
		Light: map[string]string{
			"bg": "#faf6f0", "panel": "#f0ece5", "panel-2": "#e6e2da",
			"border": "#d5d1c9", "border-2": "#c0bcb2",
			"fg": "#2c292d", "fg-bright": "#16131a", "fg-muted": "#706b6e", "fg-faint": "#918f8a",
			"green": "#269d69", "green-dim": "#a9d6bf", "blue": "#1c8ca8",
			"amber": "#99621e", "amber-bg": "#f3e4c2",
			"red": "#e14775", "red-dim": "#eeadc0", "red-bg": "#f9dfe7",
		},
	},
	"one dark": {
		Dark: map[string]string{
			"bg": "#282c34", "panel": "#21252b", "panel-2": "#2c313a",
			"border": "#3e4451", "border-2": "#4b5263",
			"fg": "#abb2bf", "fg-bright": "#e6e6e6", "fg-muted": "#828997", "fg-faint": "#5c6370",
			"green": "#98c379", "green-dim": "#35492d", "blue": "#61afef",
			"amber": "#e5c07b", "amber-bg": "#423a25",
			"red": "#e06c75", "red-dim": "#663036", "red-bg": "#362225",
		},
		Light: map[string]string{
			"bg": "#fafafa", "panel": "#f0f0f1", "panel-2": "#e5e5e6",
			"border": "#d4d4d5", "border-2": "#c0c0c2",
			"fg": "#383a42", "fg-bright": "#202227", "fg-muted": "#696c77", "fg-faint": "#a0a1a7",
			"green": "#50a14f", "green-dim": "#b0d2af", "blue": "#4078f2",
			"amber": "#986801", "amber-bg": "#f2e3bc",
			"red": "#e45649", "red-dim": "#efb3ad", "red-bg": "#fae1de",
		},
	},
	"rosé pine": {
		Dark: map[string]string{
			"bg": "#191724", "panel": "#1f1d2e", "panel-2": "#26233a",
			"border": "#403d52", "border-2": "#524f67",
			"fg": "#e0def4", "fg-bright": "#f2f0fa", "fg-muted": "#908caa", "fg-faint": "#6e6a86",
			"green": "#9ccfd8", "green-dim": "#2f4b52", "blue": "#c4a7e7",
			"amber": "#f6c177", "amber-bg": "#45392a",
			"red": "#eb6f92", "red-dim": "#5f2f42", "red-bg": "#33212b",
		},
		Light: map[string]string{
			"bg": "#faf4ed", "panel": "#fffaf3", "panel-2": "#f2e9e1",
			"border": "#dfdad9", "border-2": "#cecacd",
			"fg": "#575279", "fg-bright": "#464261", "fg-muted": "#797593", "fg-faint": "#9893a5",
			"green": "#286983", "green-dim": "#aecdd6", "blue": "#907aa9",
			"amber": "#ea9d34", "amber-bg": "#f7e4c6",
			"red": "#b4637a", "red-dim": "#e0b7c2", "red-bg": "#f5e2e7",
		},
	},
	"tokyo night": {
		Dark: map[string]string{
			"bg": "#1a1b26", "panel": "#16161e", "panel-2": "#1f2335",
			"border": "#292e42", "border-2": "#3b4261",
			"fg": "#a9b1d6", "fg-bright": "#c0caf5", "fg-muted": "#787c99", "fg-faint": "#565f89",
			"green": "#9ece6a", "green-dim": "#33452b", "blue": "#7aa2f7",
			"amber": "#e0af68", "amber-bg": "#40381f",
			"red": "#f7768e", "red-dim": "#6d3541", "red-bg": "#372028",
		},
		Light: map[string]string{
			"bg": "#e1e2e7", "panel": "#d5d6db", "panel-2": "#cbccd1",
			"border": "#b9bac1", "border-2": "#a1a6c5",
			"fg": "#343b58", "fg-bright": "#24283b", "fg-muted": "#565a6e", "fg-faint": "#7a7f95",
			"green": "#485e30", "green-dim": "#b3c9a2", "blue": "#2e7de9",
			"amber": "#8f5e15", "amber-bg": "#f0e0bb",
			"red": "#f52a65", "red-dim": "#e8a9b7", "red-bg": "#f7dde3",
		},
	},
	"solarized": {
		Dark: map[string]string{
			"bg": "#002b36", "panel": "#073642", "panel-2": "#06333e",
			"border": "#0e4c5c", "border-2": "#16606f",
			"fg": "#839496", "fg-bright": "#93a1a1", "fg-muted": "#657b83", "fg-faint": "#586e75",
			"green": "#859900", "green-dim": "#3a4d1f", "blue": "#268bd2",
			"amber": "#b58900", "amber-bg": "#3a3413",
			"red": "#dc322f", "red-dim": "#6e2a28", "red-bg": "#2f1a19",
		},
		Light: map[string]string{
			"bg": "#fdf6e3", "panel": "#eee8d5", "panel-2": "#e4ddc8",
			"border": "#d9d2bc", "border-2": "#c8c0a8",
			"fg": "#657b83", "fg-bright": "#073642", "fg-muted": "#839496", "fg-faint": "#93a1a1",
			"green": "#859900", "green-dim": "#c6cf9e", "blue": "#268bd2",
			"amber": "#b58900", "amber-bg": "#f5e8b8",
			"red": "#dc322f", "red-dim": "#e8b0a8", "red-bg": "#f8e2dc",
		},
	},
}
