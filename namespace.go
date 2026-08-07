package main

import (
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	Template string `yaml:"template" json:"template"`
	Slug     string `yaml:"slug" json:"slug"`
}

// NamespaceConfig is the parsed shape of <namespace>/.namespace.yaml (or the
// repo-root .namespace.yaml for root-level pages). Every field is optional;
// its absence is not an error, it just means the built-in defaults apply.
type NamespaceConfig struct {
	Widgets []string       `yaml:"widgets,omitempty"`
	Public  bool           `yaml:"public,omitempty"`
	New     *NewPageConfig `yaml:"new,omitempty"`

	// Index names a page in this namespace (one segment, e.g. "home") that
	// takes over /{namespace}/ in place of the built-in page-list view. Empty
	// means no override — the default listing serves the index.
	Index string `yaml:"index,omitempty" json:"index,omitempty"`

	// Configured records whether this config came from a .namespace.yaml on
	// disk or is just the built-in defaults — the settings UI needs to know
	// which namespaces actually have a file it could remove. LoadError holds
	// why a file that does exist was ignored in favour of the defaults, so
	// that's visible in the UI instead of only in the logs. Neither is ever
	// serialised: they're provenance, not configuration.
	Configured bool   `yaml:"-"`
	LoadError  string `yaml:"-"`
}

// Encode marshals cfg back to .namespace.yaml bytes — the write half of
// parseNamespaceConfig, used by the admin namespaces form.
func (c NamespaceConfig) Encode() ([]byte, error) {
	return yaml.Marshal(c)
}

// defaultNewPageTemplate is the template page every namespace configured
// through the UI uses: hidden page "<ns>/template", i.e. the file
// `.<ns>/template.md`. The name is a convention, not a choice — the settings
// form never asks for one. A hand-written .namespace.yaml naming something
// else still works, and the form preserves whatever it finds.
const defaultNewPageTemplate = "template"

// slugPreset is one option in the settings form's "slug" select: a named
// pattern for what ctrl-j calls the page it creates. Anything the presets
// don't cover is still writable as a custom pattern (or by hand in
// .namespace.yaml) — these are the shapes worth one click.
type slugPreset struct {
	Key     string // form value
	Label   string // what the option says
	Pattern string // the Go template written to new.slug
}

// slugPresetCustom is the select's escape hatch, which reveals the raw
// pattern field instead of naming a preset.
const slugPresetCustom = "custom"

var slugPresets = []slugPreset{
	{"daily", "one page per day", `{{.Now.Format "2006-01-02"}}`},
	{"monthly", "one page per month", `{{.Now.Format "2006-01"}}`},
	{"timestamped", "one page per keystroke, date and time", `{{.Now.Format "2006-01-02-1504"}}`},
	{"daily-per-user", "one page per day, per user", `{{.User}}-{{.Now.Format "2006-01-02"}}`},
}

// slugPresetFor returns the preset key matching pattern, or slugPresetCustom
// if no preset produces it — how the form decides which option to select for
// a namespace's existing config.
func slugPresetFor(pattern string) string {
	for _, p := range slugPresets {
		if p.Pattern == pattern {
			return p.Key
		}
	}
	return slugPresetCustom
}

// slugPresetView is one rendered option of the slug select: the label plus
// what the pattern produces right now, so the choice is concrete ("one page
// per day — 2026-07-28") instead of asking anyone to read a Go template.
type slugPresetView struct {
	Key     string
	Label   string
	Example string
}

// slugPresetViews renders every preset for the acting user. No preset
// mentions .Namespace, so one list serves every namespace's form.
func slugPresetViews(user string) []slugPresetView {
	data := newPageTemplateData{Now: time.Now(), User: user}
	views := make([]slugPresetView, 0, len(slugPresets))
	for _, p := range slugPresets {
		example, err := renderNewPageText(p.Pattern, data)
		if err != nil {
			example = p.Pattern // never happens for a built-in preset
		}
		views = append(views, slugPresetView{Key: p.Key, Label: p.Label, Example: example})
	}
	return views
}

// slugPatternFor resolves a submitted preset key to its pattern. The custom
// key (and any unknown one) resolves to nothing, leaving the caller to use
// the form's custom field.
func slugPatternFor(key string) string {
	for _, p := range slugPresets {
		if p.Key == key {
			return p.Pattern
		}
	}
	return ""
}

// namespaceConfigPath is the repo-relative path of ns's config file ("" is
// the root namespace, whose config sits at the repo root).
func namespaceConfigPath(ns string) string {
	if ns == "" {
		return namespaceConfigFile
	}
	return ns + "/" + namespaceConfigFile
}

// builtinWidgets is the default composition used when a namespace has no
// widgets: key — roughly today's phosphor composition, so a fresh wiki does
// not look broken.
var builtinWidgets = []string{"search", "tree", "pages", "namespaces", "tags", "log", "outline", "page-meta", "backlinks"}

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
	// The file exists from here on, so every fallback below still reports
	// itself as configured — a malformed file is a namespace the settings UI
	// must be able to show and overwrite, not one that looks untouched.
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
	if len(cfg.Widgets) == 0 {
		cfg.Widgets = builtinWidgets
	}
	for _, id := range cfg.Widgets {
		if _, ok := widgets[id]; !ok {
			slog.Warn("unknown widget id in namespace config, using defaults", "namespace", name, "widget", id)
			return broken(fmt.Sprintf("unknown widget %q — using the built-in widgets instead", id))
		}
	}
	cfg.Configured = true
	return cfg
}

// validNamespaceName reports whether name can be a namespace: not the
// reserved "_" segment, not dot-prefixed (ignored, like hidden files).
func validNamespaceName(name string) bool {
	return name != "" && name != "/" && name != `\` && name != reservedNamespace &&
		!strings.ContainsAny(name, `/\`) && !strings.HasPrefix(name, ".")
}

// validMCPPageSegment accepts one non-hidden page-name segment. It is kept
// separate from validMCPPageSlug because namespace templates and generated
// names must remain single-segment even though MCP pages may be namespaced.
func validMCPPageSegment(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\`) &&
		!strings.HasPrefix(name, ".") && !strings.HasPrefix(name, reservedNamespace)
}

// validPagePath accepts a page's path within its namespace: one or more
// slash-separated segments, each independently a valid page segment.
// Namespaces stay exactly one level deep (see BuildNamespaceRegistry), but
// the pages inside one can nest arbitrarily via slash-separated slugs —
// "guides/setup" is namespace "guides"'s own folder, not a namespace of its
// own — so every segment gets the same non-hidden, non-reserved check
// validMCPPageSegment already applies to a single one.
func validPagePath(rest string) bool {
	if rest == "" {
		return false
	}
	for _, seg := range strings.Split(rest, "/") {
		if !validMCPPageSegment(seg) {
			return false
		}
	}
	return true
}

// validMCPPageSlug accepts a root page or a namespace plus a (possibly
// nested) page path within it. MCP is a trust boundary, so unlike regular
// URL routing it rejects traversal-shaped, hidden and reserved path
// components explicitly.
func validMCPPageSlug(slug string) bool {
	ns, rest := namespaceFor(slug)
	if rest == "" {
		return false
	}
	if ns == "" {
		return validMCPPageSegment(rest)
	}
	return validNamespaceName(ns) && !strings.HasPrefix(ns, reservedNamespace) && validPagePath(rest)
}

// normaliseNamespaceConfig validates the shared namespace-settings shape used
// by both the web form and MCP before it is written to disk.
func normaliseNamespaceConfig(name string, cfg NamespaceConfig, data newPageTemplateData) (NamespaceConfig, error) {
	if name != "" && !validNamespaceName(name) {
		return NamespaceConfig{}, fmt.Errorf("invalid namespace name %q", name)
	}
	if len(cfg.Widgets) == 0 {
		cfg.Widgets = builtinWidgets
	}
	for _, id := range cfg.Widgets {
		if _, ok := widgets[id]; !ok {
			return NamespaceConfig{}, fmt.Errorf("unknown widget %q", id)
		}
	}
	cfg.Index = strings.TrimSpace(cfg.Index)
	if cfg.Index != "" && !validMCPPageSegment(cfg.Index) {
		return NamespaceConfig{}, fmt.Errorf("%q is not a valid index page name", cfg.Index)
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

// navNode is one entry in a namespace's page tree — used both by the live
// namespace index (handleNamespaceIndex) and the static export
// (ExportNamespace). Namespaces are exactly one level deep, but the pages
// inside one can still nest via slash-separated slugs — e.g.
// "docs/guides/setup" — and that's what the tree reflects: a folder per
// intermediate segment, a leaf per page, and a segment can be both (a page
// that also has children, like "guides" itself).
type navNode struct {
	Name     string // path segment
	Title    string
	Path     string // this node's slug remainder within the namespace, whether or not it's IsPage — a folder needs it too, to link "new page in this folder"
	IsPage   bool
	Children []*navNode
}

// buildPageTree builds the page hierarchy for namespace ns from entries
// already filtered to it (BacklinkEntry.Slug is a full slug, namespace
// prefix included).
func buildPageTree(entries []BacklinkEntry, ns string) *navNode {
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
	sortNavTree(root)
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

func sortNavTree(node *navNode) {
	sort.Slice(node.Children, func(i, j int) bool { return node.Children[i].Name < node.Children[j].Name })
	for _, c := range node.Children {
		sortNavTree(c)
	}
}

// renderLiveTree renders root's Children as a namespace's folder tree — used
// by both the namespace index page and the sidebar TREE widget — with plain
// absolute hrefs (/ns/path) since the live app always serves from its own
// root, not a relative-path static bundle. canWrite adds a "+" link per
// folder to create a page nested there — the same /ns/path/new?do=edit
// pattern the namespace index's own root-level "Create page" button uses,
// landing on the ordinary new-page editor with the filename field prefilled.
// currentPath (a page's slug remainder within ns, "" if not applicable)
// marks that page's link .current.
func renderLiveTree(root *navNode, ns string, canWrite bool, currentPath string) template.HTML {
	var b strings.Builder
	writeLiveTreeNodes(&b, root.Children, ns, canWrite, currentPath, true)
	return template.HTML(b.String())
}

func writeLiveTreeNodes(b *strings.Builder, nodes []*navNode, ns string, canWrite bool, currentPath string, top bool) {
	if top {
		b.WriteString(`<ul class="page-tree">`)
	} else {
		b.WriteString(`<ul class="page-tree branch">`)
	}
	for _, n := range nodes {
		b.WriteString("<li>")
		href := fmt.Sprintf("/%s/%s", ns, n.Path)
		class := "nav-link"
		if n.Path == currentPath {
			class += " current"
		}
		if len(n.Children) > 0 {
			b.WriteString("<details open><summary>")
			if n.IsPage {
				fmt.Fprintf(b, `<a class="%s" href="%s">%s</a>`, class, html.EscapeString(href), html.EscapeString(n.Title))
			} else {
				b.WriteString(`<span class="dir">` + html.EscapeString(n.Name) + `</span>`)
			}
			if canWrite {
				fmt.Fprintf(b, ` <a class="tree-new" href="%s/new?do=edit" title="New page in this folder">+</a>`, html.EscapeString(href))
			}
			b.WriteString("</summary>")
			writeLiveTreeNodes(b, n.Children, ns, canWrite, currentPath, false)
			b.WriteString("</details>")
		} else {
			fmt.Fprintf(b, `<a class="%s" href="%s">%s</a>`, class, html.EscapeString(href), html.EscapeString(n.Title))
		}
		b.WriteString("</li>")
	}
	b.WriteString("</ul>")
}

// NamespaceRegistry maps a namespace name ("" for root-level pages) to its
// resolved config. Rebuilt wholesale by BuildNamespaceRegistry — see
// search.go's pollFS, which already rescans the repo on a timer.
type NamespaceRegistry map[string]NamespaceConfig

// NamespaceSummary is the shared catalogue entry for one non-root namespace.
type NamespaceSummary struct {
	Name   string
	Config NamespaceConfig
	Count  int
	Pages  []BacklinkEntry
}

// namespaceSummaries combines configured namespaces with namespace prefixes
// found in the page index. Root-level pages never create a catalogue entry.
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
		if name != "" && cfg.Configured {
			include(name, cfg)
		}
	}
	for slug, title := range titles {
		if name, _ := namespaceFor(slug); name != "" {
			entry := include(name, reg.Resolve(slug))
			if title == "" {
				title = slug
			}
			entry.Pages = append(entry.Pages, BacklinkEntry{Slug: slug, Title: title})
		}
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

// namespaceSummaryFor returns name's catalogue entry, or nil if it has
// neither a config nor any pages.
func namespaceSummaryFor(reg NamespaceRegistry, titles map[string]string, name string) *NamespaceSummary {
	for _, entry := range namespaceSummaries(reg, titles) {
		if entry.Name == name {
			return &entry
		}
	}
	return nil
}

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

// NamespaceListEntry is one row of the namespace editor in system
// configuration: the resolved config of one namespace, as the form fields
// that POST back to /_/settings/namespaces.
type NamespaceListEntry struct {
	Name       string
	Widgets    []string
	Public     bool
	Configured bool   // has a .namespace.yaml — i.e. there is something to remove
	LoadError  string // why an existing .namespace.yaml was ignored, if it was
	Index      string // page name that replaces the page-list view at /{namespace}/, if any

	// New-page (ctrl-j) state. Template is carried through the form as a
	// hidden field rather than asked for: it's a convention, and a
	// hand-written config naming something else must survive a save here.
	NewEnabled   bool
	Template     string
	TemplateHref string // editor URL for the template page, as a hidden page
	SlugPreset   string // which slugPresets option matches, or slugPresetCustom
	SlugPattern  string // the raw pattern, for the custom field
	SlugExample  string // what SlugPattern renders to right now
}

// WidgetsCSV renders Widgets as the comma-separated value of the row's
// widgets input.
func (e NamespaceListEntry) WidgetsCSV() string {
	return strings.Join(e.Widgets, ", ")
}

// namespaceListEntries builds the settings-page namespace list from r,
// sorted the same way as Names (root first).
func namespaceListEntries(r NamespaceRegistry, user string) []NamespaceListEntry {
	names := r.Names()
	entries := make([]NamespaceListEntry, 0, len(names))
	for _, name := range names {
		cfg := r[name]
		e := NamespaceListEntry{
			Name:       name,
			Widgets:    cfg.Widgets,
			Public:     cfg.Public,
			Configured: cfg.Configured,
			LoadError:  cfg.LoadError,
			Index:      cfg.Index,
			Template:   defaultNewPageTemplate,
			SlugPreset: slugPresets[0].Key,
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
