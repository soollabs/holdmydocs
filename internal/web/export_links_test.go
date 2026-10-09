package web

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	staticexport "hmd/internal/export"
	"hmd/internal/wiki"
)

func TestExportLinkPresentation(t *testing.T) {
	for _, icons := range []bool{false, true} {
		for _, index := range []string{"home", ""} {
			t.Run(fmt.Sprintf("index=%s/icons=%t", index, icons), func(t *testing.T) {
				out := t.TempDir()
				req := staticexport.NamespaceRequest{
					Namespace: "docs", OutDir: out, Pages: []wiki.Page{{Slug: "docs/home", Title: "Home", Body: "Hello"}},
					Config: wiki.NamespaceConfig{Index: index, Export: wiki.ExportConfig{Links: []wiki.ExportLink{
						{Label: "Sidebar & text", URL: "https://example.org/sidebar", Icon: "fa-solid fa-heart", IconOnly: icons},
						{Label: "Top icon", URL: "https://example.org/top", Location: "topbar", Icon: "fa-solid fa-heart"},
						{Label: "GitHub", URL: "https://github.com/soollabs/holdmydocs", Icon: "fa-brands fa-github", Location: "topbar", IconOnly: true},
						{Label: "Sidebar second", URL: "https://example.org/icon", Icon: "fa-solid fa-heart", Location: "sidebar", IconOnly: icons},
					}}},
				}
				if _, err := NewStaticExporter(wiki.NewRenderer(nil)).RenderNamespace(req); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"index.html", "home/index.html"} {
					body, err := os.ReadFile(filepath.Join(out, name))
					if err != nil {
						t.Fatal(err)
					}
					page := string(body)
					for _, want := range []string{`class="export-topbar-links"`, `aria-label="Top icon" title="Top icon"`, `aria-label="GitHub" title="GitHub"`, `class="fa-brands fa-github" aria-hidden="true"`} {
						if !strings.Contains(page, want) {
							t.Errorf("%s missing %s", name, want)
						}
					}
					if !strings.Contains(page, "export-icons.css") || strings.Contains(page, "cdnjs.cloudflare.com") {
						t.Error("icons must use the local stylesheet")
					}
					if strings.Count(page, `target="_blank" rel="noopener noreferrer"`) < 4 {
						t.Error("external navigation links must open a new browsing context safely")
					}
					if strings.Contains(page, `<span>GitHub</span>`) || strings.Contains(page, `<span>Top icon</span>`) {
						t.Error("icon-only label rendered visibly")
					}
					if strings.Index(page, `class="export-topbar-links"`) > strings.Index(page, `id="export-search-toggle"`) {
						t.Error("topbar links must precede Search docs")
					}
					sidebar := page[strings.Index(page, `<nav class="export-sidebar-links`):]
					sidebar = sidebar[:strings.Index(sidebar, "</nav>")]
					if strings.Contains(sidebar, "export-sidebar-icons") != icons || strings.Contains(sidebar, "<i ") != icons || strings.Contains(sidebar, "<span>Sidebar &amp; text</span>") == icons {
						t.Error("sidebar must render only the selected shared mode")
					}
					headerEnd := strings.Index(page, "</header>")
					if strings.Index(page, `href="https://example.org/sidebar"`) < headerEnd || strings.Index(page, `href="https://example.org/top"`) > headerEnd {
						t.Error("link placed in wrong navigation region")
					}
				}
			})
		}
	}
}
