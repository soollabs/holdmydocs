package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportDocumentationContent(t *testing.T) {
	pages := []Page{
		{Slug: "docs/home", Title: "Welcome", Body: "# Welcome\n\nStart with [[Guide]]."},
		{Slug: "docs/nested/guide", Title: "Guide", Body: "# Guide\n\n## Learn\n\nA searchable café & tea. [[Private]] [[Missing]]\n\n```mermaid\ngraph LR\n A --> B\n```\n\n```html\n</script><script>alert(1)</script>\n```"},
		{Slug: "private/secret", Title: "Private", Body: "never index this secret"},
	}
	index, err := BuildIndex(pages)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	store, err := OpenStore(Config{RepoDir: t.TempDir(), Git: GitConfig{User: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	reg := NamespaceRegistry{"docs": {Index: "home", Widgets: []string{"outline"}}}
	if err := ExportNamespace(pages, NewRenderer(index.ResolveLink), reg, store, "docs", out, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.html", "home/index.html", "nested/guide/index.html"} {
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		// Preserve the author's heading even when it matches the template title.
		if got := strings.Count(string(data), "<h1"); got != 2 {
			t.Errorf("%s has %d headings, want template title and author heading", name, got)
		}
	}
	guide, err := os.ReadFile(filepath.Join(out, "nested/guide/index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`src="../../mermaid.min.js"`, `src="../../export.js"`, `href="../../home/index.html" rel="prev"`} {
		if !strings.Contains(string(guide), want) {
			t.Errorf("guide missing %q", want)
		}
	}
	home, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(home), `mermaid.min.js`) {
		t.Error("unneeded Mermaid script on homepage")
	}
	script, err := os.ReadFile(filepath.Join(out, "search-index.js"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(script), "</script>") {
		t.Error("search script contains unescaped HTML")
	}
	var entries []exportSearchEntry
	data := strings.TrimSuffix(strings.TrimPrefix(string(script), "window.HMDSearchIndex = "), ";")
	if err := json.Unmarshal([]byte(data), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("search has %d pages, want 2", len(entries))
	}
	if entries[1].Href != "nested/guide/index.html" || !strings.Contains(entries[1].Text, "searchable café & tea") {
		t.Errorf("search entry = %+v", entries[1])
	}
	if strings.Contains(data, "never index this secret") {
		t.Error("search leaked another namespace")
	}
	if strings.Contains(entries[1].Text, "<h2>") {
		t.Error("search includes rendered HTML tags")
	}
}

func TestExportRejectsMissingIndex(t *testing.T) {
	pages := []Page{{Slug: "docs/guide", Title: "Guide", Body: "Text"}}
	reg := NamespaceRegistry{"docs": {Index: "missing"}}
	// Invalid index is rejected before rendering or copying anything.
	out := filepath.Join(t.TempDir(), "output")
	err := ExportNamespace(pages, nil, reg, nil, "docs", out, "")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("invalid export created output directory")
	}
}
