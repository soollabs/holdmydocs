package web

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hmd/internal/config"
	staticexport "hmd/internal/export"
	"hmd/internal/wiki"
)

func TestPageSummary(t *testing.T) {
	long := strings.Repeat("word ", 40)
	for _, tc := range []struct {
		name, content, description, image string
	}{
		{"first paragraph", `<h2>Intro</h2><p>First <strong>bold</strong> line<br>next.</p><p>Second.</p>`, "First bold line next.", ""},
		{"skips image-only paragraph", `<p><img src="/_/attachments/docs/home/hero.png" alt=""></p><p>Text.</p>`, "Text.", "home/hero.png"},
		{"first PNG or JPEG of the namespace", `<p>Text.</p><img src="/_/attachments/other/a.png"><img src="/_/attachments/docs/logo.svg"><img src="/_/attachments/docs/guides/my%20shot.JPG">`, "Text.", "guides/my shot.JPG"},
		{"cut at a word boundary", `<p>` + long + `</p>`, strings.TrimSpace(strings.Repeat("word ", 31)) + "…", ""},
		{"no paragraph", `<h1>Only a heading</h1>`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			description, image := pageSummary(template.HTML(tc.content), "docs")
			if description != tc.description || image != tc.image {
				t.Errorf("pageSummary = %q, %q; want %q, %q", description, image, tc.description, tc.image)
			}
		})
	}
}

func TestExportLinkPreview(t *testing.T) {
	pages := []wiki.Page{
		{Slug: "docs/home", Title: "Home", Body: "**A wiki & more.**\n\n![Hero](/_/attachments/docs/home/hero.png)"},
		{Slug: "docs/guides/setup", Title: "Setup", Body: "## Setup"},
	}
	store, err := OpenStore(config.Config{RepoDir: t.TempDir(), AppDir: t.TempDir(), Git: config.GitConfig{User: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"docs/home", "docs/brand"} {
		if _, err := store.SaveAttachment(slug, "hero.png", []byte("png"), nil, "add image", "test", "test@example.org"); err != nil {
			t.Fatal(err)
		}
	}
	export := func(image string) (string, error) {
		outDir := t.TempDir()
		cfg := wiki.NamespaceConfig{Index: "home", Title: "Docs", Description: "Namespace summary",
			Export: wiki.ExportConfig{BaseURL: "https://docs.example.org/manual/", Image: image}}
		return outDir, staticexport.Namespace(staticexport.NamespaceRequest{
			Pages: pages, Namespace: "docs", Config: cfg, OutDir: outDir, Store: store,
		}, NewStaticExporter(wiki.NewRenderer(func(string, string) (string, bool) { return "", false })))
	}
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	outDir, err := export("brand/hero.png")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]string{
		"index.html": {
			`<meta name="description" content="A wiki &amp; more.">`,
			`<link rel="canonical" href="https://docs.example.org/manual/">`,
			`<meta property="og:type" content="website">`,
			`<meta property="og:site_name" content="Docs">`,
			`<meta property="og:title" content="Home">`,
			`<meta property="og:image" content="https://docs.example.org/manual/attachments/home/hero.png">`,
			`<meta name="twitter:card" content="summary_large_image">`,
		},
		"home/index.html": {`<link rel="canonical" href="https://docs.example.org/manual/">`},
		"guides/setup/index.html": {
			`<meta name="description" content="Namespace summary">`,
			`<link rel="canonical" href="https://docs.example.org/manual/guides/setup/">`,
			`<meta property="og:type" content="article">`,
			`<meta property="og:image" content="https://docs.example.org/manual/attachments/brand/hero.png">`,
		},
	} {
		page := read(filepath.Join(outDir, filepath.FromSlash(path)))
		for _, needle := range want {
			if !strings.Contains(page, needle) {
				t.Errorf("%s missing %q", path, needle)
			}
		}
	}

	outDir, err = export("")
	if err != nil {
		t.Fatal(err)
	}
	if page := read(filepath.Join(outDir, "guides", "setup", "index.html")); strings.Contains(page, "og:image") || !strings.Contains(page, `<meta name="twitter:card" content="summary">`) {
		t.Error("a page without an image and no export image must get a text-only card")
	}

	if _, err := export("brand/gone.png"); err == nil || !strings.Contains(err.Error(), `export image "brand/gone.png" not found`) {
		t.Errorf("export with a missing image: err = %v", err)
	}
}

func TestLiveLinkPreview(t *testing.T) {
	cfg := wiki.NamespaceConfig{Index: "home", Export: wiki.ExportConfig{Image: "brand/hero.png"}}
	if got := liveLinkPreview("", "docs", "guide", cfg, ""); got != nil {
		t.Errorf("preview without a base URL = %#v, want none", got)
	}
	for _, tc := range []struct {
		page, image string
		want        linkPreview
	}{
		{"home", "", linkPreview{URL: "https://wiki.example.org/docs/", Type: "website", Image: "https://wiki.example.org/_/attachments/docs/brand/hero.png"}},
		{"guides/my setup", "guides/shot.png", linkPreview{URL: "https://wiki.example.org/docs/guides/my%20setup", Type: "article", Image: "https://wiki.example.org/_/attachments/docs/guides/shot.png"}},
	} {
		if got := liveLinkPreview("https://wiki.example.org", "docs", tc.page, cfg, tc.image); got == nil || *got != tc.want {
			t.Errorf("liveLinkPreview(%q) = %#v, want %#v", tc.page, got, tc.want)
		}
	}
}
