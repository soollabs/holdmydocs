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
		{Slug: "docs/guides/setup", Title: "Guide", Body: "## Setup\n\n![Logo](/_/attachments/docs/logo.svg)"},
		{Slug: "docs/index", Title: "Page named index", Body: "This must not replace the namespace index."},
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
	reg := NamespaceRegistry{"docs": {Index: "home", Title: "Documentation", Skin: "newsprint", Palette: "dracula", Widgets: []string{"outline"}}}
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
	pageNamedIndex, err := os.ReadFile(filepath.Join(outDir, "index", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pageNamedIndex), "This must not") {
		t.Error("page named index was not exported separately")
	}
}
