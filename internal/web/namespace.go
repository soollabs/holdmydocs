package web

import (
	"fmt"
	"html"
	"html/template"
	"sort"
	"strings"
	"time"

	"hmd/internal/api"
	"hmd/internal/wiki"
)

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
	data := api.NewPageTemplateData{Now: time.Now(), User: user}
	views := make([]slugPresetView, 0, len(slugPresets))
	for _, p := range slugPresets {
		example, err := api.RenderNewPageText(p.Pattern, data)
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
		_, rest := wiki.NamespaceFor(e.Slug)
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

// NamespaceSummary is the shared catalogue entry for one namespace.
type NamespaceSummary struct {
	Name   string
	Config wiki.NamespaceConfig
	Count  int
	Pages  []BacklinkEntry
}

func namespaceSummaries(reg wiki.NamespaceRegistry, titles map[string]string) []NamespaceSummary {
	entries := make(map[string]*NamespaceSummary)
	include := func(name string, cfg wiki.NamespaceConfig) *NamespaceSummary {
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
		name, rest := wiki.NamespaceFor(slug)
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

func namespaceSummaryFor(reg wiki.NamespaceRegistry, titles map[string]string, name string) *NamespaceSummary {
	for _, entry := range namespaceSummaries(reg, titles) {
		if entry.Name == name {
			return &entry
		}
	}
	return nil
}

// NamespaceListEntry is one row of the namespace editor in system configuration: the resolved config of one
// namespace, as the form fields that POST back to /_/settings/namespaces.
type NamespaceListEntry struct {
	Name        string
	Widgets     []string
	Public      bool
	Title       string   // published-site title; falls back to Name if empty
	Description string   // brief namespace summary
	Skin        string   // structural skin shown to public viewers; empty = default
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

func namespaceListEntries(r wiki.NamespaceRegistry, user string) []NamespaceListEntry {
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
			Template:    wiki.DefaultNewPageTemplate,
			SlugPreset:  slugPresets[0].Key,
		}
		if cfg.New != nil {
			e.NewEnabled = true
			e.Template = cfg.New.Template
			e.SlugPattern = cfg.New.Slug
			e.SlugPreset = slugPresetFor(cfg.New.Slug)
			// Best effort: a pattern that doesn't render has nothing to show
			// as an example, and the save path is what reports why.
			if rendered, err := api.RenderNewPageText(cfg.New.Slug, api.NewPageTemplateData{Now: time.Now(), User: user, Namespace: name}); err == nil {
				e.SlugExample = rendered
			}
		}
		e.TemplateHref = "/_/hidden/" + wiki.NamespaceSlug(name, e.Template) + "?do=edit"
		entries = append(entries, e)
	}
	return entries
}

// TreeCSV renders Tree as the comma-separated value of the tree-order form field.
func (e NamespaceListEntry) TreeCSV() string {
	return strings.Join(e.Tree, ", ")
}
