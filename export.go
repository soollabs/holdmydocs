package main

import (
	"archive/zip"
	"fmt"
	"html"
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

func pageDisplayTitle(p Page) string {
	if p.Title != "" {
		return p.Title
	}
	_, rest := namespaceFor(p.Slug)
	return rest
}

// relHref computes the href from the page at fromRest to the page at
// toRest, as a path relative to fromRest's own directory — so the exported
// site works unmodified whether it's opened straight off disk (file://),
// served from a domain root, or served from a subpath. Both arguments are
// slug remainders within the namespace (forward-slash, no ".html").
func relHref(fromRest, toRest string) string {
	fromDirs := splitDir(fromRest)
	toDirs := splitDir(toRest)

	i := 0
	for i < len(fromDirs) && i < len(toDirs) && fromDirs[i] == toDirs[i] {
		i++
	}
	ups := len(fromDirs) - i

	parts := append([]string{}, toDirs[i:]...)
	parts = append(parts, path.Base(toRest)+".html")
	href := strings.Join(parts, "/")
	if ups > 0 {
		href = strings.Repeat("../", ups) + href
	}
	return href
}

// renderBreadcrumb renders the ancestor titles above rest's page title, e.g.
// "Guides / Advanced Configuration" for "guides/advanced" — an ancestor
// links to its own page when one exists at that path (a folder segment that
// is also a page, like "guides"), otherwise it's shown as plain text. The
// final (current-page) segment is never a link.
func renderBreadcrumb(rest string, titleByRest map[string]string) template.HTML {
	segments := strings.Split(rest, "/")
	parts := make([]string, 0, len(segments))
	prefix := ""
	for i, seg := range segments {
		if i == 0 {
			prefix = seg
		} else {
			prefix += "/" + seg
		}
		title, hasPage := titleByRest[prefix]
		if !hasPage {
			title = seg
		}
		text := html.EscapeString(title)
		if i < len(segments)-1 && hasPage {
			parts = append(parts, fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(relHref(rest, prefix)), text))
		} else {
			parts = append(parts, text)
		}
	}
	return template.HTML(strings.Join(parts, ` <span class="crumb-sep">/</span> `))
}

// relPrefix is the "../" run needed to reach the export root from restPath's
// own directory — used for the bundled font files.
func relPrefix(restPath string) string {
	return strings.Repeat("../", len(splitDir(restPath)))
}

// splitDir returns rest's directory as path segments, or nil if rest has
// none (a top-level page).
func splitDir(rest string) []string {
	dir := path.Dir(rest)
	if dir == "." {
		return nil
	}
	return strings.Split(dir, "/")
}

// renderNav renders root's Children as a nested file tree (see docsCSS's
// ".tree" rules for the connector lines) of links relative to the page at
// currentRest. A folder's <details> starts open — a docs site is meant to
// be skimmed, and there's no JS on a static export to remember a collapsed
// state across pages anyway — but it stays a <details>/<summary> so a
// reader can still fold sections that don't interest them.
func renderNav(root *navNode, currentRest string) template.HTML {
	var b strings.Builder
	writeNavChildren(&b, root.Children, currentRest, true)
	return template.HTML(b.String())
}

func writeNavChildren(b *strings.Builder, nodes []*navNode, currentRest string, top bool) {
	if top {
		b.WriteString(`<ul class="tree">`)
	} else {
		b.WriteString(`<ul class="tree branch">`)
	}
	for _, n := range nodes {
		b.WriteString("<li>")
		if len(n.Children) > 0 {
			b.WriteString("<details open><summary>")
			if n.IsPage {
				writeNavLink(b, n, currentRest)
			} else {
				b.WriteString(`<span class="dir">` + html.EscapeString(n.Name) + `</span>`)
			}
			b.WriteString("</summary>")
			writeNavChildren(b, n.Children, currentRest, false)
			b.WriteString("</details>")
		} else {
			writeNavLink(b, n, currentRest)
		}
		b.WriteString("</li>")
	}
	b.WriteString("</ul>")
}

func writeNavLink(b *strings.Builder, n *navNode, currentRest string) {
	class := "nav-link"
	if n.Path == currentRest {
		class += " current"
	}
	fmt.Fprintf(b, `<a class="%s" href="%s">%s</a>`, class, html.EscapeString(relHref(currentRest, n.Path)), html.EscapeString(n.Title))
}

// exportPageData is what exportPageTemplate renders.
type exportPageData struct {
	Title      string
	SiteTitle  string        // optional site-wide title (ExportNamespace's title param); "" falls back to Namespace
	Breadcrumb template.HTML // ancestor titles above the page title, e.g. "Guides / Advanced Configuration"
	Namespace  string
	AssetPath  string // relative prefix to reach style.css/fonts from this page
	Nav        template.HTML
	Content    template.HTML
}

// SidebarTitle is what the sidebar header shows: SiteTitle if set, otherwise
// the bare namespace name.
func (d exportPageData) SidebarTitle() string {
	if d.SiteTitle != "" {
		return d.SiteTitle
	}
	return d.Namespace + "/"
}

// HTMLTitle is what the <title> tag shows.
func (d exportPageData) HTMLTitle() string {
	if d.SiteTitle == "" {
		return d.Title
	}
	return d.Title + " · " + d.SiteTitle
}

// docsCSS is the static export's own stylesheet — deliberately not the live
// app's style.css/skins.css, which are built for an interactive editor with
// widgets and settings, not a standalone docs site. It's written once as a
// shared file (copyExportAssets), not inlined per page, so it isn't
// duplicated in full across every generated HTML file.
const docsCSS = `
@font-face { font-family: "JetBrains Mono"; src: url("fonts/JetBrainsMono-Regular.woff2") format("woff2"); font-weight: 400; }
@font-face { font-family: "JetBrains Mono"; src: url("fonts/JetBrainsMono-Bold.woff2") format("woff2"); font-weight: 700; }

:root {
	--ink: #0b0d0e;
	--panel: #14171a;
	--line: #262b2e;
	--text: #dce1e3;
	--mute: #7a8286;
	--signal: #3ecf8e;
	--serif: Charter, "Iowan Old Style", Georgia, "Times New Roman", serif;
	--mono: "JetBrains Mono", ui-monospace, Menlo, monospace;
}
* { box-sizing: border-box; }
html { -webkit-text-size-adjust: 100%; }
body {
	margin: 0;
	background: var(--ink);
	color: var(--text);
	font-family: var(--serif);
	font-size: 17px;
	line-height: 1.65;
	display: grid;
	grid-template-columns: 260px minmax(0, 1fr);
	min-height: 100vh;
}
a { color: var(--signal); text-decoration: none; }
a:hover { text-decoration: underline; }
:focus-visible { outline: 2px solid var(--signal); outline-offset: 2px; }

.sidebar {
	background: var(--panel);
	border-right: 1px solid var(--line);
	padding: 28px 20px;
	font-family: var(--mono);
	font-size: 13px;
	overflow-y: auto;
}
.sidebar-ns {
	display: block;
	color: var(--mute);
	letter-spacing: 0.08em;
	text-transform: uppercase;
	font-size: 11px;
	margin-bottom: 16px;
	padding-bottom: 12px;
	border-bottom: 1px solid var(--line);
}

/* Tree: a folder's children get a guide line down the left edge and a
   short connector into each row, the same shape "tree docs/" would print —
   these pages are literally files in a namespace directory, so the sidebar
   says so instead of just listing them. */
ul.tree { list-style: none; margin: 0; padding: 0; }
ul.tree.branch { margin-left: 0.7em; padding-left: 0.9em; border-left: 1px solid var(--line); }
ul.tree li { position: relative; padding: 3px 0; }
ul.tree.branch > li::before {
	content: "";
	position: absolute;
	left: -0.9em;
	top: 1em;
	width: 0.6em;
	height: 1px;
	background: var(--line);
}
.sidebar details > summary { cursor: pointer; list-style: revert; color: var(--mute); }
.sidebar details > summary::marker { color: var(--mute); }
.sidebar .dir { color: var(--mute); }
.sidebar a.nav-link { color: var(--text); }
.sidebar a.nav-link:hover { color: var(--signal); text-decoration: none; }
.sidebar a.nav-link.current { color: var(--signal); font-weight: 700; }
.sidebar a.nav-link.current::before { content: "\25cf  "; }

main { padding: 56px 48px; display: flex; justify-content: center; }
.content { width: 100%; max-width: 680px; }
.eyebrow {
	display: block;
	font-family: var(--mono);
	font-size: 12px;
	letter-spacing: 0.06em;
	color: var(--mute);
	margin-bottom: 10px;
}
.eyebrow a { color: var(--mute); text-decoration: underline; }
.eyebrow a:hover { color: var(--signal); }
.eyebrow .crumb-sep { color: var(--line); }
h1.page-title {
	font-family: var(--serif);
	font-weight: 700;
	font-size: 2.3rem;
	margin: 0 0 20px;
	padding-bottom: 20px;
	border-bottom: 1px solid var(--line);
}
article h2 { font-size: 1.5rem; margin: 2em 0 0.6em; }
article h3 { font-size: 1.2rem; margin: 1.8em 0 0.5em; }
article p, article ul, article ol { margin: 0.9em 0; }
article li { margin: 0.3em 0; }
article code {
	font-family: var(--mono);
	font-size: 0.85em;
	background: var(--panel);
	padding: 0.15em 0.4em;
	border-radius: 3px;
}
article pre {
	background: var(--panel);
	border-left: 3px solid var(--signal);
	border-radius: 3px;
	padding: 16px 18px;
	overflow-x: auto;
}
article pre code { background: none; padding: 0; }
article blockquote {
	margin: 1.2em 0;
	padding-left: 1em;
	border-left: 3px solid var(--line);
	color: var(--mute);
}
article a.wiki { border-bottom: 1px solid currentColor; }

@media (max-width: 720px) {
	body { grid-template-columns: 1fr; }
	.sidebar { border-right: none; border-bottom: 1px solid var(--line); }
	main { padding: 32px 20px; }
	h1.page-title { font-size: 1.8rem; }
}
`

var exportPageTemplate = template.Must(template.New("export-page").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{.HTMLTitle}}</title>
<link rel="stylesheet" href="{{.AssetPath}}style.css">
</head>
<body>
<nav class="sidebar">
<span class="sidebar-ns">{{.SidebarTitle}}</span>
{{.Nav}}
</nav>
<main>
<div class="content">
<nav class="eyebrow">{{.Breadcrumb}}</nav>
<h1 class="page-title">{{.Title}}</h1>
<article id="page-content">{{.Content}}</article>
</div>
</main>
</body>
</html>
`))

// ExportNamespace renders every page in namespace ns to static HTML under
// outDir: one file per page (mirroring its slug path under the namespace),
// a file-tree sidebar built from that same page set, and the namespace's
// index page (per NamespaceConfig.Index, if set) duplicated to index.html.
// title, if non-empty, is shown in the sidebar header and appended to every
// <title> tag; empty falls back to the bare namespace name. Every link —
// nav, wiki-links, and the stylesheet/fonts — is relative to the page
// emitting it, so the result works served from any path, or opened directly
// off disk with no server at all.
//
// It's reachable both from main.go's -export-namespace CLI flag and from
// the namespaces settings page (handleExportNamespace), run by whoever owns
// the repo, so it doesn't re-check the namespace's public flag — that's a
// website-serving concern, not an export one.
func ExportNamespace(pages []Page, renderer *Renderer, reg NamespaceRegistry, repoDir, ns, outDir, title string) error {
	var nsPages []Page
	for _, p := range pages {
		if pns, _ := namespaceFor(p.Slug); pns == ns {
			nsPages = append(nsPages, p)
		}
	}
	if len(nsPages) == 0 {
		return fmt.Errorf("no pages found in namespace %q", ns)
	}

	hrefs := make(map[string]string, len(nsPages))       // slug -> rest, for RenderStatic's cross-page link check
	titleByRest := make(map[string]string, len(nsPages)) // rest -> title, for renderBreadcrumb
	entries := make([]BacklinkEntry, 0, len(nsPages))
	for _, p := range nsPages {
		_, rest := namespaceFor(p.Slug)
		title := pageDisplayTitle(p)
		hrefs[p.Slug] = rest
		titleByRest[rest] = title
		entries = append(entries, BacklinkEntry{Slug: p.Slug, Title: title})
	}

	tree := buildPageTree(entries, ns)

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	if err := copyExportAssets(outDir); err != nil {
		return err
	}

	indexPage := reg[ns].Index // page name (single segment) that stands in for /{ns}/, if configured

	for _, p := range nsPages {
		_, rest := namespaceFor(p.Slug)
		hrefFor := func(slug string) (string, bool) {
			toRest, ok := hrefs[slug]
			if !ok {
				return "", false
			}
			return relHref(rest, toRest), true
		}
		content, err := renderer.RenderStatic(p.Body, ns, hrefFor)
		if err != nil {
			return fmt.Errorf("rendering %s: %w", p.Slug, err)
		}
		data := exportPageData{
			Title:      pageDisplayTitle(p),
			SiteTitle:  title,
			Breadcrumb: renderBreadcrumb(rest, titleByRest),
			Namespace:  ns,
			AssetPath:  relPrefix(rest),
			Nav:        renderNav(tree, rest),
			Content:    content,
		}

		outPath := filepath.Join(outDir, filepath.FromSlash(rest)+".html")
		if err := writeExportPage(outPath, data); err != nil {
			return err
		}
		if rest == indexPage {
			indexData := data
			indexData.AssetPath, indexData.Nav = "", renderNav(tree, "")
			if err := writeExportPage(filepath.Join(outDir, "index.html"), indexData); err != nil {
				return err
			}
		}
	}
	if indexPage == "" {
		stub := exportPageData{Title: ns, SiteTitle: title, Namespace: ns, Nav: renderNav(tree, ""), Content: "<p>Select a page from the sidebar.</p>"}
		if err := writeExportPage(filepath.Join(outDir, "index.html"), stub); err != nil {
			return err
		}
	}

	if err := copyExportAttachments(repoDir, outDir, ns); err != nil {
		return err
	}

	slog.Info("exported namespace", "namespace", ns, "pages", len(nsPages), "dir", outDir)
	return nil
}

func writeExportPage(outPath string, data exportPageData) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(outPath), err)
	}
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", outPath, err)
	}
	defer f.Close()
	if err := exportPageTemplate.Execute(f, data); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	return nil
}

// copyExportAssets writes the shared style.css and copies the app's bundled
// JetBrains Mono files into outDir — the only assets the export borrows
// from the live app, since docsCSS is otherwise self-contained. Written
// once per export, not duplicated into every page.
func copyExportAssets(outDir string) error {
	if err := os.WriteFile(filepath.Join(outDir, "style.css"), []byte(docsCSS), 0o644); err != nil {
		return fmt.Errorf("writing style.css: %w", err)
	}
	return fs.WalkDir(webFS, "web/static/fonts", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := webFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		dest := filepath.Join(outDir, "fonts", filepath.Base(path))
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
func copyExportAttachments(repoDir, outDir, ns string) error {
	src := filepath.Join(repoDir, "attachments", ns)
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", src, err)
	}
	if !info.IsDir() {
		return nil
	}
	dest := filepath.Join(outDir, "attachments")
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
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

	paths, err := app.Store.List()
	if err != nil {
		http.Error(w, "listing pages: "+err.Error(), http.StatusInternalServerError)
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
			http.Error(w, "reading "+path+": "+err.Error(), http.StatusInternalServerError)
			return
		}
		pages = append(pages, ParsePage(slug, content))
	}

	tmpDir, err := os.MkdirTemp("", "hmd-export-*")
	if err != nil {
		http.Error(w, "creating temp dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmpDir)

	title := strings.TrimSpace(r.URL.Query().Get("title"))
	if err := ExportNamespace(pages, app.Render, app.Namespaces(), app.config().RepoDir, name, tmpDir, title); err != nil {
		http.Error(w, "export failed: "+err.Error(), http.StatusInternalServerError)
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

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
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
