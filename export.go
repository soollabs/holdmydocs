package main

import (
	"archive/zip"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
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
	return path.Join(rest, "index.html")
}

// staticRelativePath links from one exported file to another. Export paths
// always use slashes, even when HMD is built on Windows.
func staticRelativePath(from, to string) string {
	href, err := filepath.Rel(filepath.FromSlash(path.Dir(from)), filepath.FromSlash(to))
	if err != nil {
		return to // both paths are generated locally, so this cannot normally fail
	}
	return filepath.ToSlash(href)
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

	hrefs := make(map[string]string, len(nsPages)) // slug -> rest, for RenderStatic's cross-page link check
	entries := make([]BacklinkEntry, 0, len(nsPages))
	for _, p := range nsPages {
		_, rest := namespaceFor(p.Slug)
		title := pageDisplayTitle(p)
		hrefs[p.Slug] = rest
		entries = append(entries, BacklinkEntry{Slug: p.Slug, Title: title})
	}

	tree := buildPageTree(entries, ns)

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	if err := copyExportAssets(outDir); err != nil {
		return err
	}

	cfg := reg[ns]
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

	if err := copyExportAttachments(store, outDir, ns); err != nil {
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
	defer f.Close()
	if err := tmpl.ExecuteTemplate(f, "layout", data); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	return nil
}

// copyExportAssets writes the public view's styles, scripts and fonts once.
func copyExportAssets(outDir string) error {
	return fs.WalkDir(webFS, "web/static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := webFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		rel, err := filepath.Rel("web/static", path)
		if err != nil {
			return err
		}
		dest := filepath.Join(outDir, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	})
}

// copyExportAttachments copies attachments/<ns>/... from the content repo
// into outDir/attachments/, unconditionally — a page-by-page reference scan
// would need to track renames and inline-HTML uploads, so this just mirrors
// the whole namespace's attachment tree, same as how the app stores it.
func copyExportAttachments(store *Store, outDir, ns string) error {
	paths, err := store.ListAttachments()
	if err != nil {
		return err
	}
	prefix := attachmentsDir + "/" + ns + "/"
	for _, attachmentPath := range paths {
		if !strings.HasPrefix(attachmentPath, prefix) {
			continue
		}
		file, _, err := store.OpenAttachment(attachmentPath)
		if err != nil {
			return fmt.Errorf("opening attachment %q: %w", attachmentPath, err)
		}
		rel := strings.TrimPrefix(attachmentPath, prefix)
		target := filepath.Join(outDir, "attachments", filepath.FromSlash(rel))
		if err := copyOpenFile(file, target); err != nil {
			file.Close()
			return fmt.Errorf("copying attachment %q: %w", attachmentPath, err)
		}
		file.Close()
	}
	return nil
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
	defer os.RemoveAll(tmpDir)

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

func zipDir(w io.Writer, dir string) error {
	zw := zip.NewWriter(w)
	defer zw.Close()
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		dest, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(dest, src)
		return err
	})
}

func copyOpenFile(in *os.File, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
