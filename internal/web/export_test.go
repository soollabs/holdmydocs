package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportNamespaceUsesPublicView(t *testing.T) {
	pages := []Page{
		{Slug: "docs/home", Title: "Home", Body: "# Welcome\n\n[[Guide]]"},
		{Slug: "docs/about", Title: "About", Body: "About"},
		{Slug: "docs/guides/setup", Title: "Guide", Body: "## Setup\n\n![Logo](/_/attachments/docs/logo.svg)"},
		{Slug: "docs/index", Title: "Page named index", Body: "This must not replace the namespace index."},
		{Slug: "docs/reference/api", Title: "API", Body: "API"},
	}
	index, err := BuildIndex(pages)
	if err != nil {
		t.Fatal(err)
	}
	repoDir := t.TempDir()
	store, err := OpenStore(Config{RepoDir: repoDir, AppDir: t.TempDir(), Git: GitConfig{User: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	reg := NamespaceRegistry{"docs": {Index: "home", Tree: []string{"reference", "guides"}, Title: "Documentation", Skin: "newsprint", Palette: "dracula", Widgets: []string{"outline"}}}
	if err := ExportNamespace(pages, NewRenderer(index.ResolveLink), reg, store, "docs", outDir, ""); err != nil {
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
	} {
		if !strings.Contains(page, want) {
			t.Errorf("export missing %q", want)
		}
	}
	if strings.Contains(page, "/_/static/") {
		t.Error("export refers to live static assets")
	}

	for _, name := range []string{"style.css", "skins.css", "toc.js", "sidebar.js"} {
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
