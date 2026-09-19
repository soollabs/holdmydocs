package web

import (
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"

	staticexport "hmd/internal/export"
	"hmd/internal/wiki"
)

var exportSemaphore = make(chan struct{}, 1)

func pageDisplayTitle(p Page) string {
	if p.Title != "" {
		return p.Title
	}
	_, rest := wiki.NamespaceFor(p.Slug)
	return rest
}

func staticPagePath(rest string) string {
	return staticexport.PagePath(rest)
}

func staticRelativePath(from, to string) string {
	return staticexport.RelativePath(from, to)
}

func staticPageHref(fromRest, toRest string) string {
	return staticRelativePath(staticPagePath(fromRest), staticPagePath(toRest))
}

func staticAssetHref(rest, asset string) string {
	return staticRelativePath(staticPagePath(rest), asset)
}

func staticAssetPrefix(rest string) string {
	return strings.TrimSuffix(staticAssetHref(rest, "style.css"), "style.css")
}

// StaticAssets returns the embedded browser asset tree copied into a static
// export. The composition root passes it to export.Namespace so the asset
// dependency stays explicit.
func StaticAssets() fs.FS { return webFS }

// StaticExporter renders namespace exports with the web templates, theme and
// widget machinery. It implements export.NamespaceRenderer; keeping it here
// makes the renderer, template and theme dependencies explicit at the caller.
type StaticExporter struct {
	Renderer *Renderer
}

// NewStaticExporter builds the web static-export renderer.
func NewStaticExporter(renderer *Renderer) StaticExporter {
	return StaticExporter{Renderer: renderer}
}

// RenderNamespace renders one namespace's pages to static HTML and returns the
// client-side search index entries. The request's Pages are already filtered to
// the namespace by export.Namespace.
func (x StaticExporter) RenderNamespace(request staticexport.NamespaceRequest) ([]staticexport.SearchEntry, error) {
	nsPages := request.Pages
	ns, cfg, outDir, title := request.Namespace, request.Config, request.OutDir, request.Title

	hrefs := make(map[string]string, len(nsPages))
	entries := make([]BacklinkEntry, 0, len(nsPages))
	for _, p := range nsPages {
		_, rest := wiki.NamespaceFor(p.Slug)
		hrefs[p.Slug] = rest
		entries = append(entries, BacklinkEntry{Slug: p.Slug, Title: pageDisplayTitle(p)})
	}

	tree := buildPageTree(entries, ns, cfg.Index, cfg.Tree)
	orderedPages := orderedTreePages(tree)
	if title = strings.TrimSpace(title); title == "" {
		title = wiki.NamespaceDisplayTitle(ns, cfg)
	}
	indexPage := cfg.Index
	skin := resolveSkin(cfg.Skin)
	palette := cfg.Palette
	if palette == "" {
		palette = skin.Palette
	}
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}

	searchEntries := make([]staticexport.SearchEntry, 0, len(nsPages))
	for _, p := range nsPages {
		_, rest := wiki.NamespaceFor(p.Slug)
		hrefFor := func(slug string) (string, bool) {
			toRest, ok := hrefs[slug]
			if !ok {
				return "", false
			}
			return staticPageHref(rest, toRest), true
		}
		content, err := x.Renderer.RenderStatic(p.Body, ns, hrefFor)
		if err != nil {
			return nil, fmt.Errorf("rendering %s: %w", p.Slug, err)
		}
		content = template.HTML(strings.ReplaceAll(string(content), "/_/attachments/"+ns+"/", staticAssetPrefix(rest)+"attachments/"))
		searchEntries = append(searchEntries, staticexport.SearchEntry{
			Title: pageDisplayTitle(p), Href: staticPagePath(rest), Text: exportPlainText(content),
		})
		data := TemplateData{
			SiteName:       title,
			AssetPath:      staticAssetPrefix(rest),
			NamespaceHome:  staticAssetHref(rest, "index.html"),
			Static:         true,
			MermaidNeeded:  strings.Contains(string(content), `class="mermaid"`),
			Title:          pageDisplayTitle(p),
			Slug:           p.Slug,
			Content:        content,
			Namespace:      ns,
			NamespaceTitle: title,
			Skin:           skinName(cfg.Skin),
			ThemeStyle:     buildThemeStyle(userRecord{Palette: palette}),
			SidebarTree:    renderStaticTree(tree, rest, func(to string) string { return staticPageHref(rest, to) }),
			RailWidgets:    widgetsForSlot(slotRail, cfg.Widgets),
		}

		data.PreviousPage, data.NextPage = pageNeighbours(orderedPages, rest, func(to string) string { return staticPageHref(rest, to) })
		outPath := filepath.Join(outDir, filepath.FromSlash(staticPagePath(rest)))
		if err := writeExportPage(outPath, tmpl["page"], data); err != nil {
			return nil, err
		}
		if rest == indexPage {
			indexData := data
			indexData.AssetPath, indexData.NamespaceHome, indexData.SidebarTree = "", "index.html", renderStaticTree(tree, indexPage, func(to string) string { return staticPageHref("", to) })
			indexData.PreviousPage, indexData.NextPage = pageNeighbours(orderedPages, rest, func(to string) string { return staticPageHref("", to) })
			indexContent, err := x.Renderer.RenderStatic(p.Body, ns, func(slug string) (string, bool) {
				toRest, ok := hrefs[slug]
				if !ok {
					return "", false
				}
				return staticPageHref("", toRest), true
			})
			if err != nil {
				return nil, fmt.Errorf("rendering root index %s: %w", p.Slug, err)
			}
			indexData.Content = template.HTML(strings.ReplaceAll(string(indexContent), "/_/attachments/"+ns+"/", "attachments/"))
			if err := writeExportPage(filepath.Join(outDir, "index.html"), tmpl["page"], indexData); err != nil {
				return nil, err
			}
		}
	}
	if indexPage == "" {
		stub := TemplateData{SiteName: title, NamespaceHome: "index.html", Static: true, Title: ns, Namespace: ns, NamespaceTitle: title, Skin: skinName(cfg.Skin), ThemeStyle: buildThemeStyle(userRecord{Palette: palette}), SidebarTree: renderStaticTree(tree, "", func(to string) string { return staticPageHref("", to) }), Content: "<p>Select a page from the sidebar.</p>", RailWidgets: widgetsForSlot(slotRail, cfg.Widgets)}
		if err := writeExportPage(filepath.Join(outDir, "index.html"), tmpl["page"], stub); err != nil {
			return nil, err
		}
	}
	return searchEntries, nil
}

// ExportNamespace renders a namespace to static HTML under outDir using the web
// presentation stack. It is a thin adapter over export.Namespace for the
// browser handler and tests; the composition root wires export.Namespace with
// NewStaticExporter directly.
func ExportNamespace(pages []Page, renderer *Renderer, reg wiki.NamespaceRegistry, store *Store, ns, outDir, title string) error {
	return staticexport.Namespace(staticexport.NamespaceRequest{
		Pages:      pages,
		Namespace:  ns,
		Config:     reg[ns],
		OutDir:     outDir,
		Title:      title,
		Assets:     webFS,
		AssetsRoot: "web/static",
		Store:      store,
	}, NewStaticExporter(renderer))
}

type exportSearchEntry = staticexport.SearchEntry

func exportPlainText(content template.HTML) string {
	tokenizer := html.NewTokenizer(strings.NewReader(string(content)))
	var text strings.Builder
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return strings.Join(strings.Fields(text.String()), " ")
		case html.TextToken:
			text.Write(tokenizer.Text())
			text.WriteByte(' ')
		}
	}
}

func writeExportPage(outPath string, tmpl *template.Template, data TemplateData) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(outPath), err)
	}
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", outPath, err)
	}
	if err := tmpl.ExecuteTemplate(f, "layout", data); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", outPath, err)
	}
	return nil
}

func (app *App) handleExportNamespace(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !app.requireTokenNamespace(w, r, name) {
		return
	}
	select {
	case exportSemaphore <- struct{}{}:
		defer func() { <-exportSemaphore }()
	default:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "export is busy", http.StatusTooManyRequests)
		return
	}

	paths, err := app.Store.List()
	if err != nil {
		slog.Error("listing export pages", "namespace", name, "err", err)
		http.Error(w, "export failed", http.StatusInternalServerError)
		return
	}
	var pages []Page
	for _, path := range paths {
		slug := strings.TrimSuffix(path, ".md")
		if ns, _ := wiki.NamespaceFor(slug); ns != name {
			continue
		}
		content, _, err := app.Store.Read(path)
		if err != nil {
			slog.Error("reading export page", "namespace", name, "err", err)
			http.Error(w, "export failed", http.StatusInternalServerError)
			return
		}
		pages = append(pages, ParsePage(slug, content))
	}

	tmpDir, err := os.MkdirTemp("", "hmd-export-*")
	if err != nil {
		slog.Error("creating export directory", "namespace", name, "err", err)
		http.Error(w, "export failed", http.StatusInternalServerError)
		return
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			slog.Warn("removing export directory", "dir", tmpDir, "err", err)
		}
	}()

	if err := ExportNamespace(pages, app.Render, app.Namespaces(), app.Store, name, tmpDir, ""); err != nil {
		slog.Error("exporting namespace", "namespace", name, "err", err)
		http.Error(w, "export failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-export.zip"`, name))
	if err := zipDir(w, tmpDir); err != nil {
		slog.Error("streaming namespace export", "namespace", name, "err", err)
	}
}

func zipDir(w io.Writer, dir string) (err error) {
	return staticexport.Zip(w, dir)
}
