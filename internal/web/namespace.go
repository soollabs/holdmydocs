package web

import (
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
)

const namespaceConfigFile = ".namespace.yaml"

const maxNamespaceDescriptionRunes = 255

const attachmentsDir = "attachments"

const reservedNamespace = "_"

var reservedTopLevel = map[string]bool{reservedNamespace: true, attachmentsDir: true}

// NewPageConfig describes how Ctrl-J / POST /_/new?ns=<namespace> creates a page in this namespace.
type NewPageConfig struct {
	Template string `yaml:"template" json:"template"`
	Slug     string `yaml:"slug" json:"slug"`
}

// NamespaceConfig is the parsed shape of <namespace>/.namespace.yaml.
type NamespaceConfig struct {
	Widgets     []string       `yaml:"widgets,omitempty"`
	Public      bool           `yaml:"public,omitempty"`
	Title       string         `yaml:"title,omitempty"`
	Description string         `yaml:"description,omitempty" json:"description,omitempty"`
	Skin        string         `yaml:"skin,omitempty"`    // structural skin shown to anonymous/public viewers; empty = defaultSkin
	Palette     string         `yaml:"palette,omitempty"` // colour preset shown to anonymous/public viewers; empty = skin's own default
	New         *NewPageConfig `yaml:"new,omitempty"`

	// Index names a page in this namespace (one segment, e.g.
	Index string `yaml:"index,omitempty" json:"index,omitempty"`

	// Tree lists page or folder paths in their preferred tree order.
	Tree []string `yaml:"tree,omitempty" json:"tree,omitempty"`

	// Configured reports whether this config came from a .namespace.yaml on disk.
	Configured bool   `yaml:"-"`
	LoadError  string `yaml:"-"`
}

// Encode marshals cfg back to .namespace.yaml bytes — the write half of parseNamespaceConfig, used by the
// admin namespaces form.
func (c NamespaceConfig) Encode() ([]byte, error) {
	return yaml.Marshal(c)
}

const defaultNewPageTemplate = "template"

type slugPreset struct {
	Key     string // form value
	Label   string // what the option says
	Pattern string // the Go template written to new.slug
}

const slugPresetCustom = "custom"

var slugPresets = []slugPreset{
	{"daily", "one page per day", `{{.Now.Format "2006-01-02"}}`},
	{"monthly", "one page per month", `{{.Now.Format "2006-01"}}`},
	{"timestamped", "one page per keystroke, date and time", `{{.Now.Format "2006-01-02-1504"}}`},
	{"daily-per-user", "one page per day, per user", `{{.User}}-{{.Now.Format "2006-01-02"}}`},
}

func slugPresetFor(pattern string) string {
	for _, p := range slugPresets {
		if p.Pattern == pattern {
			return p.Key
		}
	}
	return slugPresetCustom
}

type slugPresetView struct {
	Key     string
	Label   string
	Example string
}

func slugPresetViews(user string) []slugPresetView {
	data := newPageTemplateData{Now: time.Now(), User: user}
	views := make([]slugPresetView, 0, len(slugPresets))
	for _, p := range slugPresets {
		example, err := renderNewPageText(p.Pattern, data)
		if err != nil {
			example = p.Pattern
		}
		views = append(views, slugPresetView{Key: p.Key, Label: p.Label, Example: example})
	}
	return views
}

func slugPatternFor(key string) string {
	for _, p := range slugPresets {
		if p.Key == key {
			return p.Pattern
		}
	}
	return ""
}

func namespaceConfigPath(ns string) string {
	return ns + "/" + namespaceConfigFile
}

var builtinWidgets = []string{"pages", "namespaces", "tags", "log", "page-meta", "backlinks"}

func defaultNamespaceConfig() NamespaceConfig {
	return NamespaceConfig{Widgets: builtinWidgets}
}

func parseNamespaceConfig(data []byte) (NamespaceConfig, error) {
	var cfg NamespaceConfig
	if err := yaml.UnmarshalWithOptions(data, &cfg, yaml.Strict()); err != nil {
		return NamespaceConfig{}, fmt.Errorf("parsing namespace config: %w", err)
	}
	return cfg, nil
}

func loadNamespaceConfig(dir, name string) NamespaceConfig {
	return loadNamespaceConfigWith(dir, name, os.ReadFile)
}

func loadNamespaceConfigWith(dir, name string, readFile func(string) ([]byte, error)) NamespaceConfig {
	path := filepath.Join(dir, namespaceConfigFile)
	data, err := readFile(path)
	if err != nil {
		return defaultNamespaceConfig()
	}
	// Preserve the configured flag so malformed files remain visible and replaceable in settings.
	broken := func(reason string) NamespaceConfig {
		cfg := defaultNamespaceConfig()
		cfg.Configured, cfg.LoadError = true, reason
		return cfg
	}

	cfg, err := parseNamespaceConfig(data)
	if err != nil {
		slog.Warn("malformed namespace config, using defaults", "namespace", name, "err", err)
		return broken(err.Error())
	}
	cfg, err = normaliseNamespaceConfigBase(name, cfg)
	if err != nil {
		slog.Warn("invalid namespace config, using defaults", "namespace", name, "err", err)
		return broken(err.Error())
	}
	cfg.Configured = true
	return cfg
}

func validNamespaceName(name string) bool {
	return name != "" && utf8.ValidString(name) && !strings.ContainsFunc(name, unicode.IsControl) && !reservedTopLevel[name] &&
		!strings.ContainsAny(name, `/\`) &&
		!strings.HasPrefix(name, ".") && !strings.HasPrefix(name, reservedNamespace)
}

func validMCPPageSegment(name string) bool {
	return name != "" && utf8.ValidString(name) && !strings.ContainsFunc(name, unicode.IsControl) && !strings.ContainsAny(name, `/\`) &&
		!strings.HasPrefix(name, ".") && !strings.HasPrefix(name, reservedNamespace)
}

func validPagePath(rest string) bool {
	if rest == "" {
		return false
	}
	for seg := range strings.SplitSeq(rest, "/") {
		if !validMCPPageSegment(seg) {
			return false
		}
	}
	return true
}

func validMCPPageSlug(slug string) bool {
	ns, rest := namespaceFor(slug)
	return validNamespaceName(ns) && validPagePath(rest)
}

func normaliseNamespaceConfigBase(name string, cfg NamespaceConfig) (NamespaceConfig, error) {
	if name != "" && !validNamespaceName(name) {
		return NamespaceConfig{}, fmt.Errorf("invalid namespace name %q", name)
	}
	if len(cfg.Widgets) == 0 {
		cfg.Widgets = builtinWidgets
	}
	for _, id := range cfg.Widgets {
		if id == "search" || id == "tree" || id == "outline" {
			return NamespaceConfig{}, fmt.Errorf("widget %q is fixed application chrome", id)
		}
		if _, ok := widgets[id]; !ok {
			return NamespaceConfig{}, fmt.Errorf("unknown widget %q", id)
		}
	}
	cfg.Index = strings.TrimSpace(cfg.Index)
	if cfg.Index != "" && !validMCPPageSegment(cfg.Index) {
		return NamespaceConfig{}, fmt.Errorf("%q is not a valid index page name", cfg.Index)
	}
	seenTreePaths := make(map[string]bool, len(cfg.Tree))
	for i, path := range cfg.Tree {
		path = strings.TrimSpace(path)
		if !validPagePath(path) {
			return NamespaceConfig{}, fmt.Errorf("%q is not a valid tree path", path)
		}
		if seenTreePaths[path] {
			return NamespaceConfig{}, fmt.Errorf("tree path %q is repeated", path)
		}
		seenTreePaths[path] = true
		cfg.Tree[i] = path
	}
	cfg.Title = strings.TrimSpace(cfg.Title)
	if !validRunes(cfg.Title, maxNamespaceTitleRunes) {
		return NamespaceConfig{}, fmt.Errorf("namespace title must be at most %d characters", maxNamespaceTitleRunes)
	}
	cfg.Description = strings.TrimSpace(cfg.Description)
	if utf8.RuneCountInString(cfg.Description) > maxNamespaceDescriptionRunes {
		return NamespaceConfig{}, fmt.Errorf("namespace description must be at most %d characters", maxNamespaceDescriptionRunes)
	}
	cfg.Skin = strings.TrimSpace(cfg.Skin)
	if cfg.Skin != "" && !slices.Contains(skinNames, cfg.Skin) {
		return NamespaceConfig{}, fmt.Errorf("unknown skin %q", cfg.Skin)
	}
	cfg.Palette = strings.TrimSpace(cfg.Palette)
	if cfg.Palette != "" {
		if _, ok := themePresets[cfg.Palette]; !ok {
			return NamespaceConfig{}, fmt.Errorf("unknown palette %q", cfg.Palette)
		}
	}
	return cfg, nil
}

func normaliseNamespaceConfig(name string, cfg NamespaceConfig, data newPageTemplateData) (NamespaceConfig, error) {
	var err error
	cfg, err = normaliseNamespaceConfigBase(name, cfg)
	if err != nil {
		return NamespaceConfig{}, err
	}
	if cfg.New == nil {
		return cfg, nil
	}
	cfg.New.Template = strings.TrimSpace(cfg.New.Template)
	if cfg.New.Template == "" {
		cfg.New.Template = defaultNewPageTemplate
	}
	if !validMCPPageSegment(cfg.New.Template) {
		return NamespaceConfig{}, fmt.Errorf("%q is not a valid template page name", cfg.New.Template)
	}
	cfg.New.Slug = strings.TrimSpace(cfg.New.Slug)
	if cfg.New.Slug == "" {
		return NamespaceConfig{}, fmt.Errorf("new-page slug pattern is required")
	}
	rendered, err := renderNewPageText(cfg.New.Slug, data)
	if err != nil {
		return NamespaceConfig{}, fmt.Errorf("slug pattern is not a valid template: %w", err)
	}
	if !validMCPPageSegment(rendered) {
		return NamespaceConfig{}, fmt.Errorf("slug pattern renders unusable page name %q", rendered)
	}
	return cfg, nil
}

func namespaceDisplayTitle(name string, cfg NamespaceConfig) string {
	if title := strings.TrimSpace(cfg.Title); title != "" {
		return title
	}
	return name
}

type navNode struct {
	Name     string // path segment
	Title    string
	Path     string // slug remainder within the namespace
	IsPage   bool
	Children []*navNode
}

func buildPageTree(entries []BacklinkEntry, ns, index string, tree []string) *navNode {
	root := &navNode{}
	for _, e := range entries {
		_, rest := namespaceFor(e.Slug)
		segments := strings.Split(rest, "/")
		node := root
		path := ""
		for i, seg := range segments {
			if i == 0 {
				path = seg
			} else {
				path += "/" + seg
			}
			node = navChild(node, seg)
			node.Path = path
			if i == len(segments)-1 {
				node.IsPage = true
				node.Title = e.Title
			}
		}
	}
	order := make(map[string]int, len(tree))
	for i, path := range tree {
		order[path] = i
	}
	sortNavTree(root, index, order)
	return root
}

func navChild(node *navNode, name string) *navNode {
	for _, c := range node.Children {
		if c.Name == name {
			return c
		}
	}
	c := &navNode{Name: name}
	node.Children = append(node.Children, c)
	return c
}

func sortNavTree(node *navNode, index string, order map[string]int) {
	sort.Slice(node.Children, func(i, j int) bool {
		iPath, jPath := node.Children[i].Path, node.Children[j].Path
		if iPath == index || jPath == index {
			return iPath == index
		}
		iOrder, iOK := order[iPath]
		jOrder, jOK := order[jPath]
		if iOK || jOK {
			if iOK != jOK {
				return iOK
			}
			return iOrder < jOrder
		}
		return node.Children[i].Name < node.Children[j].Name
	})
	for _, c := range node.Children {
		sortNavTree(c, index, order)
	}
}

func renderLiveTree(root *navNode, ns string, currentPath string) template.HTML {
	return renderTree(root, currentPath, func(pagePath string) string {
		return fmt.Sprintf("/%s/%s", ns, pagePath)
	})
}

func renderStaticTree(root *navNode, currentPath string, hrefFor func(string) string) template.HTML {
	return renderTree(root, currentPath, hrefFor)
}

func renderTree(root *navNode, currentPath string, hrefFor func(string) string) template.HTML {
	var b strings.Builder
	writeTreeNodes(&b, root.Children, currentPath, hrefFor, true)
	return template.HTML(b.String())
}

func writeTreeNodes(b *strings.Builder, nodes []*navNode, currentPath string, hrefFor func(string) string, top bool) {
	if top {
		b.WriteString(`<ul class="page-tree">`)
	} else {
		b.WriteString(`<ul class="page-tree branch">`)
	}
	for _, n := range nodes {
		b.WriteString("<li>")
		href := hrefFor(n.Path)
		class := "nav-link"
		if n.Path == currentPath {
			class += " current"
		}
		if len(n.Children) > 0 {
			b.WriteString("<details")
			if n.Path == currentPath || strings.HasPrefix(currentPath, n.Path+"/") {
				b.WriteString(" open")
			}
			b.WriteString("><summary>")
			if n.IsPage {
				fmt.Fprintf(b, `<a class="%s" href="%s">%s</a>`, class, html.EscapeString(href), html.EscapeString(n.Title))
			} else {
				b.WriteString(`<span class="dir">` + html.EscapeString(n.Name) + `</span>`)
			}
			b.WriteString("</summary>")
			writeTreeNodes(b, n.Children, currentPath, hrefFor, false)
			b.WriteString("</details>")
		} else {
			fmt.Fprintf(b, `<a class="%s" href="%s">%s</a>`, class, html.EscapeString(href), html.EscapeString(n.Title))
		}
		b.WriteString("</li>")
	}
	b.WriteString("</ul>")
}

// NamespaceRegistry maps a namespace name to its resolved config.
type NamespaceRegistry map[string]NamespaceConfig

// NamespaceSummary is the shared catalogue entry for one namespace.
type NamespaceSummary struct {
	Name   string
	Config NamespaceConfig
	Count  int
	Pages  []BacklinkEntry
}

func namespaceSummaries(reg NamespaceRegistry, titles map[string]string) []NamespaceSummary {
	entries := make(map[string]*NamespaceSummary)
	include := func(name string, cfg NamespaceConfig) *NamespaceSummary {
		if entry, ok := entries[name]; ok {
			return entry
		}
		entry := &NamespaceSummary{Name: name, Config: cfg}
		entries[name] = entry
		return entry
	}

	for name, cfg := range reg {
		if cfg.Configured {
			include(name, cfg)
		}
	}
	for slug, title := range titles {
		name, rest := namespaceFor(slug)
		if rest == "" {
			continue
		}
		entry := include(name, reg.Resolve(slug))
		if title == "" {
			title = slug
		}
		entry.Pages = append(entry.Pages, BacklinkEntry{Slug: slug, Title: title})
	}

	result := make([]NamespaceSummary, 0, len(entries))
	for _, entry := range entries {
		sort.Slice(entry.Pages, func(i, j int) bool {
			return entry.Pages[i].Slug < entry.Pages[j].Slug
		})
		entry.Count = len(entry.Pages)
		result = append(result, *entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func namespaceSummaryFor(reg NamespaceRegistry, titles map[string]string, name string) *NamespaceSummary {
	for _, entry := range namespaceSummaries(reg, titles) {
		if entry.Name == name {
			return &entry
		}
	}
	return nil
}

// BuildNamespaceRegistry scans repoDir for namespaces: one directory per non-dot-prefixed, non-reserved
// top-level subdirectory.
func BuildNamespaceRegistry(repoDir string) (NamespaceRegistry, error) {
	return buildNamespaceRegistry(repoDir, os.ReadFile)
}

// BuildNamespaceRegistryFromStore reads namespace configuration through the Store boundary so a synchronised
// repository cannot smuggle in a symlink.
func BuildNamespaceRegistryFromStore(store *Store) (NamespaceRegistry, error) {
	return buildNamespaceRegistry(store.Dir(), func(path string) ([]byte, error) {
		rel, err := filepath.Rel(store.Dir(), path)
		if err != nil {
			return nil, err
		}
		return store.ReadRepositoryFile(filepath.ToSlash(rel))
	})
}

func buildNamespaceRegistry(repoDir string, readFile func(string) ([]byte, error)) (NamespaceRegistry, error) {
	reg := NamespaceRegistry{}

	slog.Debug("namespace registry reading repository directory", "path", repoDir)
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return nil, fmt.Errorf("reading repo directory: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() || !validNamespaceName(e.Name()) {
			continue
		}
		slog.Debug("namespace registry loading namespace", "namespace", e.Name())
		reg[e.Name()] = loadNamespaceConfigWith(filepath.Join(repoDir, e.Name()), e.Name(), readFile)
	}
	slog.Debug("namespace registry built", "namespaces", len(reg))
	return reg, nil
}

func namespaceFor(slug string) (ns, rest string) {
	before, after, ok := strings.Cut(slug, "/")
	if !ok {
		return slug, ""
	}
	return before, after
}

func namespaceSlug(ns, rest string) string {
	return ns + "/" + rest
}

// Resolve returns the config for the namespace slug belongs to, or the built-in defaults if that namespace
// isn't in the registry (e.g. it has no .namespace.yaml and hasn't been scanned yet).
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
		return namespaceSlug(ns, cfg.Index)
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

// NamespaceListEntry is one row of the namespace editor in system configuration: the resolved config of one
// namespace, as the form fields that POST back to /_/settings/namespaces.
type NamespaceListEntry struct {
	Name        string
	Widgets     []string
	Public      bool
	Title       string   // published-site title; falls back to Name if empty
	Description string   // brief namespace summary
	Skin        string   // structural skin shown to public viewers; empty = defaultSkin
	Palette     string   // colour preset shown to public viewers; empty = skin's own default
	Configured  bool     // has a .namespace.yaml — i.e. there is something to remove
	LoadError   string   // why an existing .namespace.yaml was ignored, if it was
	Index       string   // page name that replaces the page-list view at /{namespace}/, if any
	Tree        []string // explicit page or folder order in the rendered tree

	// New-page (ctrl-j) state.
	NewEnabled   bool
	Template     string
	TemplateHref string // editor URL for the template page, as a hidden page
	SlugPreset   string // which slugPresets option matches, or slugPresetCustom
	SlugPattern  string // the raw pattern, for the custom field
	SlugExample  string // what SlugPattern renders to right now
}

// WidgetsCSV renders Widgets as the comma-separated value of the row's widgets input.
func (e NamespaceListEntry) WidgetsCSV() string {
	return strings.Join(e.Widgets, ", ")
}

func namespaceListEntries(r NamespaceRegistry, user string) []NamespaceListEntry {
	names := r.Names()
	entries := make([]NamespaceListEntry, 0, len(names))
	for _, name := range names {
		cfg := r[name]
		e := NamespaceListEntry{
			Name:        name,
			Widgets:     cfg.Widgets,
			Public:      cfg.Public,
			Title:       cfg.Title,
			Description: cfg.Description,
			Skin:        cfg.Skin,
			Palette:     cfg.Palette,
			Configured:  cfg.Configured,
			LoadError:   cfg.LoadError,
			Index:       cfg.Index,
			Tree:        cfg.Tree,
			Template:    defaultNewPageTemplate,
			SlugPreset:  slugPresets[0].Key,
		}
		if cfg.New != nil {
			e.NewEnabled = true
			e.Template = cfg.New.Template
			e.SlugPattern = cfg.New.Slug
			e.SlugPreset = slugPresetFor(cfg.New.Slug)
			// Best effort: a pattern that doesn't render has nothing to show
			// as an example, and the save path is what reports why.
			if rendered, err := renderNewPageText(cfg.New.Slug, newPageTemplateData{Now: time.Now(), User: user, Namespace: name}); err == nil {
				e.SlugExample = rendered
			}
		}
		e.TemplateHref = "/_/hidden/" + namespaceSlug(name, e.Template) + "?do=edit"
		entries = append(entries, e)
	}
	return entries
}

// TreeCSV renders Tree as the comma-separated value of the tree-order form field.
func (e NamespaceListEntry) TreeCSV() string {
	return strings.Join(e.Tree, ", ")
}
