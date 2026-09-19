package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hmd/internal/config"
	staticexport "hmd/internal/export"
	"hmd/internal/search"
	"hmd/internal/wiki"
)

func TestExportNamespaceUsesPublicView(t *testing.T) {
	pages := []wiki.Page{
		{Slug: "docs/home", Title: "Home", Body: "# Welcome\n\n[[Guide]]"},
		{Slug: "docs/about", Title: "About", Body: "About"},
		{Slug: "docs/guides/setup", Title: "Guide", Body: "## Setup\n\n![Logo](/_/attachments/docs/logo.svg)"},
		{Slug: "docs/index", Title: "Page named index", Body: "This must not replace the namespace index."},
		{Slug: "docs/reference/api", Title: "API", Body: "API"},
	}
	index, err := search.BuildIndex(pages)
	if err != nil {
		t.Fatal(err)
	}
	repoDir := t.TempDir()
	store, err := OpenStore(config.Config{RepoDir: repoDir, AppDir: t.TempDir(), Git: config.GitConfig{User: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	reg := wiki.NamespaceRegistry{"docs": {Index: "home", Tree: []string{"reference", "guides"}, Title: "Documentation", Skin: "newsprint", Palette: "dracula", Widgets: []string{"outline"}}}
	if err := staticexport.Namespace(staticexport.NamespaceRequest{
		Pages:      pages,
		Namespace:  "docs",
		Config:     reg["docs"],
		OutDir:     outDir,
		Assets:     webFS,
		AssetsRoot: "web/static",
		Store:      store,
	}, NewStaticExporter(wiki.NewRenderer(index.ResolveLink))); err != nil {
		t.Fatal(err)
	}

	html, err := os.ReadFile(filepath.Join(outDir, "guides", "setup", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)
	for _, want := range []string{
		`data-skin="newsprint"`,
		`--bg:#282a36;`,
		`href="../../style.css"`,
		`href="../../skins.css"`,
		`href="../../home/index.html"`,
		`src="../../toc.js"`,
		`src="../../attachments/logo.svg"`,
		`<title>Guide — Documentation</title>`,
		`class="topbar-site">Documentation</a>`,
		`href="../../export.css"`,
		`src="../../search-index.js"`,
		`src="../../export.js"`,
		`src="../../page.js"`,
		`id="public-sidebar-toggle"`,
		`aria-controls="sidebar"`,
		`aria-label="Page navigation"`,
		`Skip to content`,
		`aria-label="Previous and next pages"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("export missing %q", want)
		}
	}
	if strings.Contains(page, "/_/static/") {
		t.Error("export refers to live static assets")
	}

	for _, name := range []string{"style.css", "skins.css", "toc.js", "sidebar.js", "export.css", "export.js", "page.js", "search-index.js"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("export missing %s: %v", name, err)
		}
	}
	root, err := os.ReadFile(filepath.Join(outDir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(root), "Welcome") || strings.Contains(string(root), "This must not") {
		t.Error("configured namespace index was not exported to index.html")
	}
	if !strings.Contains(string(root), `href="reference/api/index.html" rel="next"`) {
		t.Error("root index pager must follow configured tree order with root-relative paths")
	}
	if !strings.Contains(string(root), `class="nav-link current" href="home/index.html"`) {
		t.Error("root index must mark its configured page current in navigation")
	}
	if !strings.Contains(string(root), `href="guides/setup/index.html"`) {
		t.Error("root index wiki-links must be relative to index.html")
	}
	for _, earlier := range []string{`href="home/index.html"`, `href="reference/api/index.html"`, `href="guides/setup/index.html"`, `href="about/index.html"`} {
		if at := strings.Index(string(root), earlier); at == -1 {
			t.Errorf("root tree missing %q", earlier)
		} else if previous := strings.Index(string(root), `class="sidebar-tree"`); previous > at {
			t.Errorf("root tree link %q is outside the sidebar", earlier)
		}
	}
	if got := []int{strings.Index(string(root), `href="home/index.html"`), strings.Index(string(root), `href="reference/api/index.html"`), strings.Index(string(root), `href="guides/setup/index.html"`), strings.Index(string(root), `href="about/index.html"`)}; got[0] >= got[1] || got[1] >= got[2] || got[2] >= got[3] {
		t.Errorf("exported tree order = %v, want home, reference, guides, about", got)
	}
	pageNamedIndex, err := os.ReadFile(filepath.Join(outDir, "index", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pageNamedIndex), "This must not") {
		t.Error("page named index was not exported separately")
	}
}
