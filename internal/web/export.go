package web

import (
	"fmt"
	staticexport "hmd/internal/export"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var exportSemaphore = make(chan struct{}, 1)

func pageDisplayTitle(p Page) string {
	if p.Title != "" {
		return p.Title
	}
	_, rest := namespaceFor(p.Slug)
	return rest
}

// staticPagePath gives every page its own directory. This leaves the export
// root's index.html available as the configured namespace index, even when a
// namespace also contains a page named "index".
func staticPagePath(rest string) string {
	return staticexport.PagePath(rest)
}

// staticRelativePath links from one exported file to another. Export paths
// always use slashes, even when HMD is built on Windows.
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

// ExportNamespace renders every page in namespace ns to static HTML under
// outDir: one file per page (mirroring its slug path under the namespace),
// a file-tree sidebar built from that same page set, and the namespace's
// index page (per NamespaceConfig.Index, if set) duplicated to index.html.
// It uses the same template, skin, palette and public-only widgets as the
// live anonymous view. Every link and asset is relative to its HTML file.
//
// It's reachable both from main.go's -export-namespace CLI flag and from
// the namespaces settings page (handleExportNamespace), run by whoever owns
// the repo, so it doesn't re-check the namespace's public flag — that's a
// website-serving concern, not an export one.
func ExportNamespace(pages []Page, renderer *Renderer, reg NamespaceRegistry, store *Store, ns, outDir, title string) error {
	var nsPages []Page
	for _, p := range pages {
		if pns, _ := namespaceFor(p.Slug); pns == ns {
			nsPages = append(nsPages, p)
		}
	}
	if len(nsPages) == 0 {
		return fmt.Errorf("no pages found in namespace %q", ns)
	}
	if len(nsPages) > maxExportFiles {
		return fmt.Errorf("export exceeds %d files", maxExportFiles)
	}

	hrefs := make(map[string]string, len(nsPages)) // slug -> rest, for RenderStatic's cross-page link check
	entries := make([]BacklinkEntry, 0, len(nsPages))
	for _, p := range nsPages {
		_, rest := namespaceFor(p.Slug)
		title := pageDisplayTitle(p)
		hrefs[p.Slug] = rest
		entries = append(entries, BacklinkEntry{Slug: p.Slug, Title: title})
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	if err := copyExportAssets(outDir); err != nil {
		return err
	}

	cfg := reg[ns]
	tree := buildPageTree(entries, ns, cfg.Index, cfg.Tree)
	if title = strings.TrimSpace(title); title == "" {
		title = namespaceDisplayTitle(ns, cfg)
	}
	indexPage := cfg.Index // page name (single segment) that stands in for /{ns}/, if configured
	skin := resolveSkin(cfg.Skin)
	palette := cfg.Palette
	if palette == "" {
		palette = skin.Palette
	}
	tmpl, err := parseTemplates()
	if err != nil {
		return err
	}

	for _, p := range nsPages {
		_, rest := namespaceFor(p.Slug)
		hrefFor := func(slug string) (string, bool) {
			toRest, ok := hrefs[slug]
			if !ok {
				return "", false
			}
			return staticPageHref(rest, toRest), true
		}
		content, err := renderer.RenderStatic(p.Body, ns, hrefFor)
		if err != nil {
			return fmt.Errorf("rendering %s: %w", p.Slug, err)
		}
		content = template.HTML(strings.ReplaceAll(string(content), "/_/attachments/"+ns+"/", staticAssetPrefix(rest)+"attachments/"))
		data := TemplateData{
			SiteName:       title,
			AssetPath:      staticAssetPrefix(rest),
			NamespaceHome:  staticAssetHref(rest, "index.html"),
			Static:         true,
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

		outPath := filepath.Join(outDir, filepath.FromSlash(staticPagePath(rest)))
		if err := writeExportPage(outPath, tmpl["page"], data); err != nil {
			return err
		}
		if rest == indexPage {
			indexData := data
			indexData.AssetPath, indexData.NamespaceHome, indexData.SidebarTree = "", "index.html", renderStaticTree(tree, "", func(to string) string { return staticPageHref("", to) })
			indexContent, err := renderer.RenderStatic(p.Body, ns, func(slug string) (string, bool) {
				toRest, ok := hrefs[slug]
				if !ok {
					return "", false
				}
				return staticPageHref("", toRest), true
			})
			if err != nil {
				return fmt.Errorf("rendering root index %s: %w", p.Slug, err)
			}
			indexData.Content = template.HTML(strings.ReplaceAll(string(indexContent), "/_/attachments/"+ns+"/", "attachments/"))
			if err := writeExportPage(filepath.Join(outDir, "index.html"), tmpl["page"], indexData); err != nil {
				return err
			}
		}
	}
	if indexPage == "" {
		stub := TemplateData{SiteName: title, NamespaceHome: "index.html", Static: true, Title: ns, Namespace: ns, NamespaceTitle: title, Skin: skinName(cfg.Skin), ThemeStyle: buildThemeStyle(userRecord{Palette: palette}), SidebarTree: renderStaticTree(tree, "", func(to string) string { return staticPageHref("", to) }), Content: "<p>Select a page from the sidebar.</p>", RailWidgets: widgetsForSlot(slotRail, cfg.Widgets)}
		if err := writeExportPage(filepath.Join(outDir, "index.html"), tmpl["page"], stub); err != nil {
			return err
		}
	}

	files, bytes := len(nsPages), int64(0)
	for _, p := range nsPages {
		bytes += int64(len(p.Body))
	}
	if err := copyExportAttachments(store, outDir, ns, &files, &bytes); err != nil {
		return err
	}

	slog.Info("exported namespace", "namespace", ns, "pages", len(nsPages), "dir", outDir)
	return nil
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

// copyExportAssets writes the public view's styles, scripts and fonts once.
func copyExportAssets(outDir string) error {
	return staticexport.CopyAssets(webFS, "web/static", outDir)
}

// copyExportAttachments copies attachments/<ns>/... from the content repo
// into outDir/attachments/, unconditionally — a page-by-page reference scan
// would need to track renames and inline-HTML uploads, so this just mirrors
// the whole namespace's attachment tree, same as how the app stores it.
func copyExportAttachments(store *Store, outDir, ns string, files *int, bytes *int64) error {
	return staticexport.CopyAttachments(store, outDir, ns, files, bytes)
}

// handleExportNamespace is the web-UI equivalent of the -export-namespace
// CLI flag: it builds the same static export into a scratch directory and
// streams it back as a zip download, for anyone who'd rather click a button
// on the namespaces settings page than run the binary by hand.
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
		if ns, _ := namespaceFor(slug); ns != name {
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
