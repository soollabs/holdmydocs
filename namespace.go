package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// namespaceConfigFile is the name of the optional per-namespace config file.
const namespaceConfigFile = ".namespace.yaml"

// reservedNamespace is the one top-level segment a namespace may never take
// — the app's own routes live under it.
const reservedNamespace = "_"

// NewPageConfig describes how Ctrl-J / POST /_/new?ns=<namespace> creates a
// page in this namespace.
type NewPageConfig struct {
	Template string `yaml:"template"`
	Slug     string `yaml:"slug"`
}

// NamespaceConfig is the parsed shape of <namespace>/.namespace.yaml (or the
// repo-root .namespace.yaml for root-level pages). Every field is optional;
// its absence is not an error, it just means the built-in defaults apply.
type NamespaceConfig struct {
	Widgets []string       `yaml:"widgets"`
	Public  bool           `yaml:"public"`
	New     *NewPageConfig `yaml:"new"`
}

// builtinWidgets is the default composition used when a namespace has no
// widgets: key — roughly today's phosphor composition, so a fresh wiki does
// not look broken.
var builtinWidgets = []string{"search", "pages", "tags", "log", "outline", "page-meta", "backlinks"}

// defaultNamespaceConfig is what a namespace with no .namespace.yaml gets:
// built-in widgets, private.
func defaultNamespaceConfig() NamespaceConfig {
	return NamespaceConfig{Widgets: builtinWidgets}
}

// parseNamespaceConfig parses .namespace.yaml strictly: an unknown key is an
// error. Used at the write path (settings validation, any UI producing these
// files) — never at render time.
func parseNamespaceConfig(data []byte) (NamespaceConfig, error) {
	var cfg NamespaceConfig
	if err := yaml.UnmarshalWithOptions(data, &cfg, yaml.Strict()); err != nil {
		return NamespaceConfig{}, fmt.Errorf("parsing namespace config: %w", err)
	}
	return cfg, nil
}

// loadNamespaceConfig reads and parses <dir>/.namespace.yaml softly: a
// missing file is the built-in defaults, and malformed YAML or an unknown
// widget id logs a warning and falls back to defaults rather than failing.
// It never errors — a broken namespace config must never 500 a page or
// block the wiki.
func loadNamespaceConfig(dir, name string) NamespaceConfig {
	path := filepath.Join(dir, namespaceConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultNamespaceConfig()
	}
	cfg, err := parseNamespaceConfig(data)
	if err != nil {
		slog.Warn("malformed namespace config, using defaults", "namespace", name, "err", err)
		return defaultNamespaceConfig()
	}
	if len(cfg.Widgets) == 0 {
		cfg.Widgets = builtinWidgets
	}
	for _, id := range cfg.Widgets {
		if _, ok := widgets[id]; !ok {
			slog.Warn("unknown widget id in namespace config, using defaults", "namespace", name, "widget", id)
			return defaultNamespaceConfig()
		}
	}
	return cfg
}

// validNamespaceName reports whether name can be a namespace: not the
// reserved "_" segment, not dot-prefixed (ignored, like hidden files).
func validNamespaceName(name string) bool {
	return name != "" && name != reservedNamespace && !strings.HasPrefix(name, ".")
}

// NamespaceRegistry maps a namespace name ("" for root-level pages) to its
// resolved config. Rebuilt wholesale by BuildNamespaceRegistry — see
// search.go's pollFS, which already rescans the repo on a timer.
type NamespaceRegistry map[string]NamespaceConfig

// BuildNamespaceRegistry scans repoDir for namespaces: the root config plus
// one directory per non-dot-prefixed, non-reserved top-level subdirectory.
// Namespaces are exactly one level deep — nothing here walks further.
func BuildNamespaceRegistry(repoDir string) (NamespaceRegistry, error) {
	reg := NamespaceRegistry{"": loadNamespaceConfig(repoDir, "")}

	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return nil, fmt.Errorf("reading repo directory: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() || !validNamespaceName(e.Name()) {
			continue
		}
		reg[e.Name()] = loadNamespaceConfig(filepath.Join(repoDir, e.Name()), e.Name())
	}
	return reg, nil
}

// namespaceFor splits a slug into its namespace name and the remainder of
// the slug within that namespace. Namespaces are exactly one level deep:
// "blog/drafts/post" is in namespace "blog" with rest "drafts/post";
// subdirectories beyond the first segment are filing, not namespace
// structure. A slug with no "/" is a root-level page, namespace "".
func namespaceFor(slug string) (ns, rest string) {
	i := strings.Index(slug, "/")
	if i == -1 {
		return "", slug
	}
	return slug[:i], slug[i+1:]
}

// namespaceSlug builds a full page slug from a namespace name and the
// remainder of the slug within it — the inverse of namespaceFor.
func namespaceSlug(ns, rest string) string {
	if ns == "" {
		return rest
	}
	return ns + "/" + rest
}

// Resolve returns the config for the namespace slug belongs to, or the
// built-in defaults if that namespace isn't in the registry (e.g. it has no
// .namespace.yaml and hasn't been scanned yet).
func (r NamespaceRegistry) Resolve(slug string) NamespaceConfig {
	ns, _ := namespaceFor(slug)
	if cfg, ok := r[ns]; ok {
		return cfg
	}
	return defaultNamespaceConfig()
}

// IsPublic reports whether slug lives in a namespace with public: true.
func (r NamespaceRegistry) IsPublic(slug string) bool {
	return r.Resolve(slug).Public
}

// Names returns every namespace name in the registry, sorted, root ("")
// first if present. Used by the read-only namespace list in settings.
func (r NamespaceRegistry) Names() []string {
	names := make([]string, 0, len(r))
	for name := range r {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == "" {
			return true
		}
		if names[j] == "" {
			return false
		}
		return names[i] < names[j]
	})
	return names
}

// NamespaceListEntry is one row of the read-only namespace list shown in
// settings: name, resolved widgets, public flag and new-page template, with
// a link to edit the namespace's .namespace.yaml as a page.
type NamespaceListEntry struct {
	Name       string
	Widgets    []string
	Public     bool
	Template   string
	ConfigHref string
}

// namespaceListEntries builds the settings-page namespace list from r,
// sorted the same way as Names (root first).
func namespaceListEntries(r NamespaceRegistry) []NamespaceListEntry {
	names := r.Names()
	entries := make([]NamespaceListEntry, 0, len(names))
	for _, name := range names {
		cfg := r[name]
		template := ""
		if cfg.New != nil {
			template = cfg.New.Template
		}
		configPath := namespaceConfigFile
		if name != "" {
			configPath = name + "/" + namespaceConfigFile
		}
		entries = append(entries, NamespaceListEntry{
			Name:       name,
			Widgets:    cfg.Widgets,
			Public:     cfg.Public,
			Template:   template,
			ConfigHref: "/" + configPath + "?do=edit",
		})
	}
	return entries
}
