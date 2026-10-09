package wiki

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"hmd/internal/presentation"

	"github.com/goccy/go-yaml"
)

// NamespaceConfigFile is the per-namespace configuration filename.
const NamespaceConfigFile = ".namespace.yaml"

// MaxNamespaceTitleRunes bounds a namespace's published title.
const MaxNamespaceTitleRunes = 256

// MaxNamespaceDescriptionRunes bounds a namespace's description.
const MaxNamespaceDescriptionRunes = 255

// DefaultNewPageTemplate is the template page name a namespace uses when it
// names none of its own.
const DefaultNewPageTemplate = "template"

// ReservedNamespace is the top-level path segment reserved for application routes.
const ReservedNamespace = "_"

// BuiltinNamespaceWidgets is the default widget set for a namespace that does
// not list its own.
var BuiltinNamespaceWidgets = []string{"pages", "namespaces", "tags", "log", "page-meta", "backlinks"}

// NewPageConfig describes how the quick-create shortcut creates a page in a
// namespace.
type NewPageConfig struct {
	Template string `yaml:"template" json:"template" jsonschema:"one-segment page name whose body is used as the creation template"`
	Slug     string `yaml:"slug" json:"slug" jsonschema:"Go template that renders the new page's one-segment name; this is a naming pattern, not a page identifier"`
}

// NamespaceConfig is the parsed shape of <namespace>/.namespace.yaml.
type NamespaceConfig struct {
	Widgets     []string       `yaml:"widgets,omitempty"`
	Public      bool           `yaml:"public,omitempty"`
	Title       string         `yaml:"title,omitempty"`
	Description string         `yaml:"description,omitempty" json:"description,omitempty"`
	Skin        string         `yaml:"skin,omitempty"`    // structural skin shown to anonymous/public viewers; empty = default skin
	Palette     string         `yaml:"palette,omitempty"` // colour preset shown to anonymous/public viewers; empty = skin's own default
	New         *NewPageConfig `yaml:"new,omitempty"`
	Export      ExportConfig   `yaml:"export,omitempty" json:"export,omitempty"`

	// Index names a page in this namespace (one segment), e.g.
	Index string `yaml:"index,omitempty" json:"index,omitempty"`

	// Tree lists page or folder paths in their preferred tree order.
	Tree []string `yaml:"tree,omitempty" json:"tree,omitempty"`

	// Configured reports whether this config came from a .namespace.yaml on disk.
	Configured bool   `yaml:"-"`
	LoadError  string `yaml:"-"`
}

// Encode marshals c as namespace YAML.
func (c NamespaceConfig) Encode() ([]byte, error) {
	return yaml.Marshal(c)
}

// NamespaceConfigPath returns the repository-relative config path for a namespace.
func NamespaceConfigPath(namespace string) string {
	return namespace + "/" + NamespaceConfigFile
}

// DefaultNamespaceConfig is the config used for a namespace without a file.
func DefaultNamespaceConfig() NamespaceConfig {
	return NamespaceConfig{Widgets: BuiltinNamespaceWidgets}
}

// ParseNamespaceConfig parses the YAML body of a namespace config.
func ParseNamespaceConfig(data []byte) (NamespaceConfig, error) {
	var cfg NamespaceConfig
	if err := yaml.UnmarshalWithOptions(data, &cfg, yaml.Strict()); err != nil {
		return NamespaceConfig{}, fmt.Errorf("parsing namespace config: %w", err)
	}
	return cfg, nil
}

// LoadNamespaceConfig reads and normalises a namespace's config, falling back
// to defaults for a missing, malformed or invalid file.
func LoadNamespaceConfig(dir, name string) NamespaceConfig {
	return LoadNamespaceConfigWith(dir, name, os.ReadFile)
}

// LoadNamespaceConfigWith is LoadNamespaceConfig with an injectable reader.
func LoadNamespaceConfigWith(dir, name string, readFile func(string) ([]byte, error)) NamespaceConfig {
	path := filepath.Join(dir, NamespaceConfigFile)
	data, err := readFile(path)
	if err != nil {
		return DefaultNamespaceConfig()
	}
	// Preserve the configured flag so malformed files remain visible and replaceable in settings.
	broken := func(reason string) NamespaceConfig {
		cfg := DefaultNamespaceConfig()
		cfg.Configured, cfg.LoadError = true, reason
		return cfg
	}

	cfg, err := ParseNamespaceConfig(data)
	if err != nil {
		slog.Warn("malformed namespace config, using defaults", "namespace", name, "err", err)
		return broken(err.Error())
	}
	cfg, err = NormaliseNamespaceConfigBase(name, cfg)
	if err != nil {
		slog.Warn("invalid namespace config, using defaults", "namespace", name, "err", err)
		return broken(err.Error())
	}
	cfg.Configured = true
	return cfg
}

// NormaliseNamespaceConfigBase validates and normalises the parts of a
// namespace config that need no rendered new-page slug. Empty name means the
// root namespace.
func NormaliseNamespaceConfigBase(name string, cfg NamespaceConfig) (NamespaceConfig, error) {
	if name != "" && !ValidNamespaceName(name) {
		return NamespaceConfig{}, fmt.Errorf("invalid namespace name %q", name)
	}
	if len(cfg.Widgets) == 0 {
		cfg.Widgets = BuiltinNamespaceWidgets
	}
	for _, id := range cfg.Widgets {
		if presentation.IsFixedWidget(id) {
			return NamespaceConfig{}, fmt.Errorf("widget %q is fixed application chrome", id)
		}
		if !presentation.ValidWidget(id) {
			return NamespaceConfig{}, fmt.Errorf("unknown widget %q", id)
		}
	}
	cfg.Index = strings.TrimSpace(cfg.Index)
	if cfg.Index != "" && !ValidPageSegment(cfg.Index) {
		return NamespaceConfig{}, fmt.Errorf("%q is not a valid index page name", cfg.Index)
	}
	seenTreePaths := make(map[string]bool, len(cfg.Tree))
	for i, path := range cfg.Tree {
		path = strings.TrimSpace(path)
		if !ValidPagePath(path) {
			return NamespaceConfig{}, fmt.Errorf("%q is not a valid tree path", path)
		}
		if seenTreePaths[path] {
			return NamespaceConfig{}, fmt.Errorf("tree path %q is repeated", path)
		}
		seenTreePaths[path] = true
		cfg.Tree[i] = path
	}
	cfg.Title = strings.TrimSpace(cfg.Title)
	if !utf8.ValidString(cfg.Title) || utf8.RuneCountInString(cfg.Title) > MaxNamespaceTitleRunes {
		return NamespaceConfig{}, fmt.Errorf("namespace title must be at most %d characters", MaxNamespaceTitleRunes)
	}
	cfg.Description = strings.TrimSpace(cfg.Description)
	if utf8.RuneCountInString(cfg.Description) > MaxNamespaceDescriptionRunes {
		return NamespaceConfig{}, fmt.Errorf("namespace description must be at most %d characters", MaxNamespaceDescriptionRunes)
	}
	cfg.Skin = strings.TrimSpace(cfg.Skin)
	if cfg.Skin != "" && !presentation.ValidSkin(cfg.Skin) {
		return NamespaceConfig{}, fmt.Errorf("unknown skin %q", cfg.Skin)
	}
	cfg.Palette = strings.TrimSpace(cfg.Palette)
	if cfg.Palette != "" && !presentation.ValidPalette(cfg.Palette) {
		return NamespaceConfig{}, fmt.Errorf("unknown palette %q", cfg.Palette)
	}
	var err error
	cfg.Export, err = NormaliseExportConfig(cfg.Export, false)
	if err != nil {
		return NamespaceConfig{}, err
	}
	return cfg, nil
}

// NamespaceDisplayTitle returns a namespace's published title, falling back to its name.
func NamespaceDisplayTitle(name string, cfg NamespaceConfig) string {
	if title := strings.TrimSpace(cfg.Title); title != "" {
		return title
	}
	return name
}

// NamespaceRegistry maps a namespace name to its resolved config.
type NamespaceRegistry map[string]NamespaceConfig

// Resolve returns the config for the namespace slug belongs to, or the
// built-in defaults if that namespace isn't in the registry.
func (r NamespaceRegistry) Resolve(slug string) NamespaceConfig {
	ns, _ := NamespaceFor(slug)
	if cfg, ok := r[ns]; ok {
		return cfg
	}
	return DefaultNamespaceConfig()
}

// IsPublic reports whether slug lives in a namespace with public: true.
func (r NamespaceRegistry) IsPublic(slug string) bool {
	return r.Resolve(slug).Public
}

// Names returns every namespace name in the registry, sorted.
func (r NamespaceRegistry) Names() []string {
	names := make([]string, 0, len(r))
	for name := range r {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// IndexSlug returns the full slug of ns's configured index page, or "" if it has none.
func (r NamespaceRegistry) IndexSlug(ns string) string {
	if cfg, ok := r[ns]; ok && cfg.Index != "" {
		return NamespaceSlug(ns, cfg.Index)
	}
	return ""
}

// IndexSlugs returns every namespace's index-page slug.
func (r NamespaceRegistry) IndexSlugs() []string {
	slugs := make([]string, 0, len(r))
	for ns := range r {
		if slug := r.IndexSlug(ns); slug != "" {
			slugs = append(slugs, slug)
		}
	}
	sort.Strings(slugs)
	return slugs
}
