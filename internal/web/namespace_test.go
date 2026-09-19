package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"hmd/internal/api"
	"hmd/internal/config"
	"hmd/internal/presentation"
	"hmd/internal/search"
	"hmd/internal/wiki"
)

func TestParseNamespaceConfig(t *testing.T) {
	yaml := []byte(`
widgets: [calendar, writing-stats, prev-entries]
public: true
description: Daily notes and decisions
new:
  template: entry
  slug: '{{.Now.Format "2006-01-02"}}'
`)
	cfg, err := wiki.ParseNamespaceConfig(yaml)
	if err != nil {
		t.Fatalf("wiki.ParseNamespaceConfig: %v", err)
	}
	if len(cfg.Widgets) != 3 || cfg.Widgets[0] != "calendar" {
		t.Errorf("Widgets = %v", cfg.Widgets)
	}
	if !cfg.Public {
		t.Error("Public = false, want true")
	}
	if cfg.Description != "Daily notes and decisions" {
		t.Errorf("Description = %q", cfg.Description)
	}
	if cfg.New == nil || cfg.New.Template != "entry" {
		t.Errorf("New = %+v", cfg.New)
	}
}

func TestNormaliseNamespaceConfigDescriptionLimit(t *testing.T) {
	_, err := api.NormaliseNamespaceConfig("notes", wiki.NamespaceConfig{Description: strings.Repeat("x", wiki.MaxNamespaceDescriptionRunes+1)}, api.NewPageTemplateData{})
	if err == nil {
		t.Errorf("description longer than %d characters was accepted", wiki.MaxNamespaceDescriptionRunes)
	}
}

func TestParseNamespaceConfigUnknownKeyRejected(t *testing.T) {
	yaml := []byte("widgets: [calendar]\nbogus: true\n")
	if _, err := wiki.ParseNamespaceConfig(yaml); err == nil {
		t.Error("expected error for unknown key, got nil")
	}
}

func TestNormaliseNamespaceConfigRejectsFixedWidgets(t *testing.T) {
	for _, id := range []string{"search", "tree", "outline"} {
		if _, err := api.NormaliseNamespaceConfig("notes", wiki.NamespaceConfig{Widgets: []string{id}}, api.NewPageTemplateData{}); err == nil {
			t.Errorf("fixed widget %q was accepted", id)
		}
	}
}

func TestNormaliseNamespaceConfigAllowsRoot(t *testing.T) {
	if _, err := api.NormaliseNamespaceConfig("", wiki.NamespaceConfig{Widgets: []string{"pages"}}, api.NewPageTemplateData{}); err != nil {
		t.Fatalf("normalise root namespace config: %v", err)
	}
}

func TestNamespaceTreeItems(t *testing.T) {
	items := namespaceTreeItems(map[string]string{
		"docs/home":         "Home",
		"docs/guides/setup": "Set up",
		"docs/reference":    "Reference",
		"other/page":        "Other",
	}, "docs", "home", []string{"reference", "guides"})
	got := make([]string, len(items))
	for i, item := range items {
		got[i] = item.Path
	}
	want := []string{"home", "reference", "guides", "guides/setup"}
	if !slices.Equal(got, want) {
		t.Errorf("paths = %v, want %v", got, want)
	}
	if !items[0].IsIndex || !items[2].Folder {
		t.Errorf("items = %+v, want a fixed index followed by a folder", items)
	}
}

func TestNamespaceTreeEditorIsCollapsedAndHierarchical(t *testing.T) {
	editor := string(namespaceTreeEditor(map[string]string{
		"docs/home":         "Home",
		"docs/guides/setup": "Set up",
	}, "docs", "home", []string{"guides"}))
	if strings.Contains(editor, "<details open") || !strings.Contains(editor, `data-parent="guides"`) || !strings.Contains(editor, `class="tree-order-toggle"`) {
		t.Errorf("editor = %s, want a collapsed guides subtree", editor)
	}
	if !strings.Contains(editor, `data-path="home"`) || !strings.Contains(editor, `tree-order-item fixed`) {
		t.Errorf("editor = %s, want fixed index row", editor)
	}
}

func TestNormaliseNamespaceConfigTreePaths(t *testing.T) {
	cfg, err := api.NormaliseNamespaceConfig("docs", wiki.NamespaceConfig{Tree: []string{" guides ", "guides/setup"}}, api.NewPageTemplateData{})
	if err != nil {
		t.Fatalf("normalise tree paths: %v", err)
	}
	if want := []string{"guides", "guides/setup"}; !slices.Equal(cfg.Tree, want) {
		t.Errorf("Tree = %v, want %v", cfg.Tree, want)
	}
	for _, tree := range [][]string{{"../private"}, {"guides", "guides"}} {
		if _, err := api.NormaliseNamespaceConfig("docs", wiki.NamespaceConfig{Tree: tree}, api.NewPageTemplateData{}); err == nil {
			t.Errorf("invalid tree %v was accepted", tree)
		}
	}
}

func TestLoadNamespaceConfigMissingFileIsDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg := wiki.LoadNamespaceConfig(dir, "blog")
	if cfg.Public {
		t.Error("missing config should default to private")
	}
	if len(cfg.Widgets) != len(wiki.BuiltinNamespaceWidgets) {
		t.Errorf("Widgets = %v, want built-in defaults", cfg.Widgets)
	}
}

func TestLoadNamespaceConfigMalformedYAMLFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, wiki.NamespaceConfigFile), []byte("widgets: [oops\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := wiki.LoadNamespaceConfig(dir, "blog")
	if cfg.Public {
		t.Error("malformed config should fall back to private default")
	}
	if len(cfg.Widgets) != len(wiki.BuiltinNamespaceWidgets) {
		t.Errorf("Widgets = %v, want built-in defaults", cfg.Widgets)
	}
}

func TestLoadNamespaceConfigUnknownWidgetIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, wiki.NamespaceConfigFile), []byte("widgets: [not-a-real-widget]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := wiki.LoadNamespaceConfig(dir, "blog")
	if len(cfg.Widgets) != len(wiki.BuiltinNamespaceWidgets) {
		t.Errorf("Widgets = %v, want built-in defaults after unknown id", cfg.Widgets)
	}
}

func TestLoadNamespaceConfigInvalidSettingsFallBackToDefaults(t *testing.T) {
	for name, yaml := range map[string]string{
		"skin":    "skin: unknown\n",
		"palette": "palette: unknown\n",
		"index":   "index: ../private\n",
		"widgets": "widgets: [tree]\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, wiki.NamespaceConfigFile), []byte(yaml), 0644); err != nil {
				t.Fatal(err)
			}
			cfg := wiki.LoadNamespaceConfig(dir, "blog")
			if !cfg.Configured || cfg.LoadError == "" {
				t.Errorf("invalid config = %+v, want configured fallback with an error", cfg)
			}
			if cfg.Skin != "" || cfg.Palette != "" || cfg.Index != "" || cfg.New != nil {
				t.Errorf("invalid config = %+v, want defaults", cfg)
			}
		})
	}
}

func TestNamespaceFor(t *testing.T) {
	tests := []struct {
		slug   string
		wantNS string
		wantR  string
	}{
		{"blog/drafts/post", "blog", "drafts/post"},
		{"notes", "notes", ""},
		{"notes/2026-07-27", "notes", "2026-07-27"},
	}
	for _, tt := range tests {
		ns, rest := wiki.NamespaceFor(tt.slug)
		if ns != tt.wantNS || rest != tt.wantR {
			t.Errorf("wiki.NamespaceFor(%q) = (%q, %q), want (%q, %q)", tt.slug, ns, rest, tt.wantNS, tt.wantR)
		}
	}
}

func TestValidNamespaceNameRejectsReserved(t *testing.T) {
	if wiki.ValidNamespaceName("_") {
		t.Error("_ must be rejected as a namespace name")
	}
	if wiki.ValidNamespaceName(".hidden") {
		t.Error("dot-prefixed directories must be rejected as namespaces")
	}
	if wiki.ValidNamespaceName("attachments") {
		t.Error("the attachments directory must be rejected as a namespace name")
	}
	if !wiki.ValidNamespaceName("blog") {
		t.Error("blog should be a valid namespace name")
	}
}

func TestBuildNamespaceRegistry(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "blog"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blog", wiki.NamespaceConfigFile), []byte("public: true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(filepath.Join(dir, "_"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	reg, err := api.BuildNamespaceRegistry(dir)
	if err != nil {
		t.Fatalf("api.BuildNamespaceRegistry: %v", err)
	}
	if _, ok := reg["_"]; ok {
		t.Error("reserved namespace _ must not appear in the registry")
	}
	if _, ok := reg[".git"]; ok {
		t.Error("dot-prefixed directory must not appear in the registry")
	}
	if !reg.IsPublic("blog/post") {
		t.Error("blog/post should resolve to the public blog namespace")
	}
	if reg.IsPublic("readme") {
		t.Error("root namespace should default to private")
	}
	if reg.IsPublic("unknown/page") {
		t.Error("a namespace not in the registry should default to private")
	}
}

func TestNamespaceRegistryResolveUnknownNamespaceDefaults(t *testing.T) {
	reg := wiki.NamespaceRegistry{"": wiki.DefaultNamespaceConfig()}
	cfg := reg.Resolve("nope/page")
	if cfg.Public {
		t.Error("unknown namespace should default to private")
	}
	if len(cfg.Widgets) != len(wiki.BuiltinNamespaceWidgets) {
		t.Errorf("Widgets = %v, want built-in defaults", cfg.Widgets)
	}
}

func TestNamespaceRegistryNamesRootFirst(t *testing.T) {
	reg := wiki.NamespaceRegistry{"": {}, "zeta": {}, "alpha": {}}
	names := reg.Names()
	if len(names) != 3 || names[0] != "" || names[1] != "alpha" || names[2] != "zeta" {
		t.Errorf("Names() = %v", names)
	}
}

// TestCreateNamespaceFromAdmin tests namespace creation through the admin form.
func TestCreateNamespaceFromAdmin(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	adminLogin(t, server, client)

	resp, err := postJSON(t, client, server.URL+"/_/api/namespaces", namespaceSaveJSON(url.Values{
		"name": {"blog"}, "widgets": {"backlinks"}, "public": {"on"},
		"description": {"Writing and research"},
		"new_enabled": {"on"}, "slug_preset": {"daily"},
	}))
	if err != nil {
		t.Fatalf("POST /_/api/namespaces: %v", err)
	}
	closeTestBody(t, resp.Body)

	cfg, ok := app.Namespaces()["blog"]
	if !ok {
		t.Fatal("blog namespace should exist in the registry after the save")
	}
	if got := strings.Join(cfg.Widgets, ","); got != "backlinks" {
		t.Errorf("widgets = %q, want backlinks", got)
	}
	if !cfg.Public {
		t.Error("blog should be public")
	}
	if cfg.Description != "Writing and research" {
		t.Errorf("description = %q", cfg.Description)
	}
	if cfg.New == nil || cfg.New.Template != wiki.DefaultNewPageTemplate {
		t.Errorf("new-page config = %+v, want template %q", cfg.New, wiki.DefaultNewPageTemplate)
	}
	if cfg.New != nil && cfg.New.Slug != presentation.SlugPatternFor("daily") {
		t.Errorf("slug pattern = %q, want the daily preset %q", cfg.New.Slug, presentation.SlugPatternFor("daily"))
	}
	if !cfg.Configured {
		t.Error("a namespace with a saved .namespace.yaml should report itself configured")
	}

	if _, _, err := app.Store.Read(wiki.NamespaceConfigPath("blog")); err != nil {
		t.Errorf("reading blog/.namespace.yaml: %v", err)
	}
	if _, _, err := app.Store.Read(wiki.HiddenFile("blog/" + wiki.DefaultNewPageTemplate)); err != nil {
		t.Errorf("reading seeded template %s: %v", wiki.HiddenFile("blog/"+wiki.DefaultNewPageTemplate), err)
	}

	newResp, err := client.Get(server.URL + "/_/new?ns=blog")
	if err != nil {
		t.Fatalf("POST /_/new?ns=blog: %v", err)
	}
	body, _ := io.ReadAll(newResp.Body)
	closeTestBody(t, newResp.Body)
	todaySlug := "blog/" + time.Now().Format("2006-01-02")
	if !strings.Contains(string(body), `data-slug="`+todaySlug+`"`) {
		t.Errorf("draft response should render the editor for %s: %s", todaySlug, body)
	}
}

// TestNamespaceManagement tests namespace directory and editor routes.
func TestNamespaceManagement(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	adminLogin(t, server, client)
	for _, path := range []string{"/_/namespaces", "/_/namespaces/new"} {
		resp, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		closeTestBody(t, resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
	}

	resp, err := postJSON(t, client, server.URL+"/_/api/namespaces", namespaceSaveJSON(url.Values{"name": {"blog"}}))
	if err != nil {
		t.Fatalf("create blog: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("create status = %d, want 200", resp.StatusCode)
	}

	resp, err = client.Get(server.URL + "/_/namespaces")
	if err != nil {
		t.Fatalf("GET namespace directory: %v", err)
	}
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading namespace directory: %v", err)
	}
	closeTestBody(t, resp.Body)
	body := string(content)
	if !strings.Contains(body, "blog") || !strings.Contains(body, "0 pages") {
		t.Errorf("namespace directory missing blog catalogue row: %s", body)
	}
	if !strings.Contains(body, `class="table-wrap namespace-table"`) {
		t.Errorf("namespace directory table is missing its responsive semantic class: %s", body)
	}

	resp, err = client.Get(server.URL + "/_/namespaces/blog/edit")
	if err != nil {
		t.Fatalf("GET namespace editor: %v", err)
	}
	content, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading namespace editor: %v", err)
	}
	closeTestBody(t, resp.Body)
	body = string(content)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `name="name"`) || !strings.Contains(body, `/delete-all`) || !strings.Contains(body, `Published view preview`) || !strings.Contains(body, `sidebar-tree`) || !strings.Contains(body, `ON THIS PAGE`) {
		t.Errorf("namespace editor = %d, body missing focused form: %s", resp.StatusCode, body)
	}

	if _, _, err := app.Store.Read(wiki.HiddenFile("blog/" + wiki.DefaultNewPageTemplate)); err != nil {
		t.Fatalf("seeded template missing: %v", err)
	}
}

func saveConfiguredEmptyNamespace(t *testing.T, app *testApp, name string) []byte {
	t.Helper()
	content := []byte("public: true\n")
	if _, err := app.Store.Save(wiki.NamespaceConfigPath(name), content, "Configure namespace "+name, "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving %s config: %v", name, err)
	}
	if err := app.apiClient().RefreshNamespaces(); err != nil {
		t.Fatalf("refreshing namespaces: %v", err)
	}
	return content
}

func postNamespaceDelete(t *testing.T, server *httptest.Server, client *http.Client, name string) *http.Response {
	t.Helper()
	resp, err := postJSON(t, client, server.URL+"/_/api/namespaces/delete", namespaceActionJSON(url.Values{"name": {name}}))
	if err != nil {
		t.Fatalf("deleting %q: %v", name, err)
	}
	return resp
}

func postNamespaceDeleteAll(t *testing.T, server *httptest.Server, client *http.Client, name string) *http.Response {
	t.Helper()
	resp, err := postJSON(t, client, server.URL+"/_/api/namespaces/delete-all", namespaceActionJSON(url.Values{"name": {name}}))
	if err != nil {
		t.Fatalf("deleting all of %q: %v", name, err)
	}
	return resp
}

func TestNamespaceManagementRejectsIndexedPageWithoutMutation(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	adminLogin(t, server, client)

	config := saveConfiguredEmptyNamespace(t, app, "blog")
	page := wiki.Page{Slug: "blog/post", Title: "Post", Body: "content"}
	if _, err := app.Store.Save(wiki.PageFile(page.Slug), page.Encode(), "Add blog/post", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving indexed page: %v", err)
	}
	if err := app.Index.Update(page); err != nil {
		t.Fatalf("indexing page: %v", err)
	}

	resp := postNamespaceDelete(t, server, client, "blog")
	body, _ := io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("deleting namespace with indexed page = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(string(body), "\"error\"") {
		t.Errorf("rejection should report a JSON error: %s", body)
	}

	got, _, err := app.Store.Read(wiki.NamespaceConfigPath("blog"))
	if err != nil || string(got) != string(config) {
		t.Fatalf("config after rejected indexed-page deletion = %q, %v; want %q", got, err, config)
	}
	if _, _, err := app.Store.Read(wiki.PageFile(page.Slug)); err != nil {
		t.Fatalf("indexed page after rejected deletion: %v", err)
	}
}

func TestNamespaceManagementRejectsHiddenFileWithoutMutation(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	adminLogin(t, server, client)

	config := saveConfiguredEmptyNamespace(t, app, "blog")
	hidden := wiki.Page{Slug: "blog/template", Title: "Template", Body: "hidden"}
	if _, err := app.Store.Save(wiki.HiddenFile(hidden.Slug), hidden.Encode(), "Add hidden template", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving hidden template: %v", err)
	}

	resp := postNamespaceDelete(t, server, client, "blog")
	body, _ := io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("deleting namespace with hidden file = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(string(body), "\"error\"") {
		t.Errorf("rejection should report a JSON error: %s", body)
	}

	got, _, err := app.Store.Read(wiki.NamespaceConfigPath("blog"))
	if err != nil || string(got) != string(config) {
		t.Fatalf("config after rejected hidden-file deletion = %q, %v; want %q", got, err, config)
	}
	if _, _, err := app.Store.Read(wiki.HiddenFile(hidden.Slug)); err != nil {
		t.Fatalf("hidden file after rejected deletion: %v", err)
	}
}

func TestNamespaceManagementRejectsOtherDirectoryContentWithoutMutation(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	adminLogin(t, server, client)

	config := saveConfiguredEmptyNamespace(t, app, "blog")
	extra := filepath.Join(app.config().RepoDir, "blog", "metadata.json")
	if err := os.WriteFile(extra, []byte("keep me"), 0644); err != nil {
		t.Fatalf("saving ordinary namespace content: %v", err)
	}

	resp := postNamespaceDelete(t, server, client, "blog")
	body, _ := io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("deleting namespace with ordinary content = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(string(body), "\"error\"") {
		t.Errorf("rejection should report a JSON error: %s", body)
	}

	got, _, err := app.Store.Read(wiki.NamespaceConfigPath("blog"))
	if err != nil || string(got) != string(config) {
		t.Fatalf("config after rejected ordinary-content deletion = %q, %v; want %q", got, err, config)
	}
	if content, err := os.ReadFile(extra); err != nil || string(content) != "keep me" {
		t.Fatalf("ordinary content after rejected deletion = %q, %v", content, err)
	}
}

func TestStoreDeleteNamespaceRejectsContentWithoutMutation(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(config.Config{
		RepoDir: tmpDir,
		AppDir:  t.TempDir(),
		Git:     config.GitConfig{User: "test"},
	})
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	config := []byte("public: true\n")
	page := []byte("keep me\n")
	if _, err := store.Save(wiki.NamespaceConfigPath("blog"), config, "configure blog", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving namespace config: %v", err)
	}
	if _, err := store.Save(wiki.PageFile("blog/post"), page, "add blog/post", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving namespace page: %v", err)
	}

	if err := store.DeleteNamespace("blog", "delete blog", "test", "test@hmd.local"); err == nil {
		t.Fatal("deleting namespace with a page should be rejected")
	}
	if got, _, err := store.Read(wiki.NamespaceConfigPath("blog")); err != nil || string(got) != string(config) {
		t.Fatalf("config after rejected deletion = %q, %v; want %q", got, err, config)
	}
	if got, _, err := store.Read(wiki.PageFile("blog/post")); err != nil || string(got) != string(page) {
		t.Fatalf("page after rejected deletion = %q, %v; want %q", got, err, page)
	}
}

func TestNamespaceManagementDeletesGenuinelyEmptyConfiguredNamespace(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	adminLogin(t, server, client)
	saveConfiguredEmptyNamespace(t, app, "empty")

	resp := postNamespaceDelete(t, server, client, "empty")
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deleting empty namespace = %d, want 200", resp.StatusCode)
	}
	if _, _, err := app.Store.Read(wiki.NamespaceConfigPath("empty")); err == nil {
		t.Fatal("empty namespace config still exists after successful deletion")
	}
	if _, err := os.Stat(filepath.Join(app.config().RepoDir, "empty")); !os.IsNotExist(err) {
		t.Fatalf("empty namespace directory stat = %v, want not exist", err)
	}
	if _, ok := app.Namespaces()["empty"]; ok {
		t.Fatal("successfully deleted namespace remains in registry")
	}
}

func TestNamespaceManagementDeletesAllFiles(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	adminLogin(t, server, client)

	saveConfiguredEmptyNamespace(t, app, "blog")
	page := wiki.Page{Slug: "blog/post", Title: "Post", Body: "content"}
	if _, err := app.Store.Save(wiki.PageFile(page.Slug), page.Encode(), "Add blog/post", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving namespace page: %v", err)
	}
	if err := app.Index.Update(page); err != nil {
		t.Fatalf("indexing namespace page: %v", err)
	}
	if _, err := app.Store.Save("blog/drafts/note.txt", []byte("note"), "Add namespace file", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving nested namespace file: %v", err)
	}
	if _, err := app.Store.Save(wiki.HiddenFile("blog/template"), wiki.Page{Slug: "blog/template", Title: "Template"}.Encode(), "Add namespace template", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving namespace template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(app.config().RepoDir, "blog", "upload.bin"), []byte("upload"), 0644); err != nil {
		t.Fatalf("saving untracked namespace file: %v", err)
	}

	resp := postNamespaceDeleteAll(t, server, client, "blog")
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete all = %d, want 200", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(app.config().RepoDir, "blog")); !os.IsNotExist(err) {
		t.Fatalf("namespace directory stat = %v, want not exist", err)
	}
	if app.Index.Exists(page.Slug) {
		t.Fatal("deleted namespace page remains in the index")
	}
	if _, ok := app.Namespaces()["blog"]; ok {
		t.Fatal("deleted namespace remains in the registry")
	}
}

func TestNamespaceManagementRejectsRootAndInvalidDeletionNames(t *testing.T) {
	for _, name := range []string{"", "/", "_", ".hidden", "a/b", `a\\b`} {
		t.Run(name, func(t *testing.T) {
			app, server, client := newTestAppFull(t)
			defer server.Close()
			adminLogin(t, server, client)
			config, _, err := app.Store.Read(wiki.NamespaceConfigPath(testNS))
			if err != nil {
				t.Fatal(err)
			}

			resp := postNamespaceDelete(t, server, client, name)
			closeTestBody(t, resp.Body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("deleting %q = %d, want 400", name, resp.StatusCode)
			}
			got, _, err := app.Store.Read(wiki.NamespaceConfigPath(testNS))
			if err != nil || string(got) != string(config) {
				t.Fatalf("notes config after rejected %q deletion = %q, %v; want %q", name, got, err, config)
			}
		})
	}
}

// TestSaveNamespaceRejectsBadInput tests validation of namespace settings.
func TestSaveNamespaceRejectsBadInput(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	adminLogin(t, server, client)

	cases := []struct {
		name string
		form url.Values
	}{
		{"unknown widget", url.Values{"name": {"nope"}, "widgets": {"not-a-widget"}}},
		{"reserved name", url.Values{"name": {wiki.ReservedNamespace}}},
		{"dot-prefixed name", url.Values{"name": {".hidden"}}},
		{"nested name", url.Values{"name": {"a/b"}}},
		{"custom pattern left empty", url.Values{"name": {"nope"}, "new_enabled": {"on"}, "slug_preset": {"custom"}}},
		{"unparseable pattern", url.Values{"name": {"nope"}, "new_enabled": {"on"}, "slug_preset": {"custom"}, "slug_custom": {"{{.Now"}}},

		{"pattern rendering a slash", url.Values{"name": {"nope"}, "new_enabled": {"on"}, "slug_preset": {"custom"}, "slug_custom": {`{{.Now.Format "2006/01-02"}}`}}},
		{"invalid template page name", url.Values{"name": {"nope"}, "new_enabled": {"on"}, "slug_preset": {"daily"}, "template": {".hidden"}}},
	}
	for _, tc := range cases {
		resp, err := postJSON(t, client, server.URL+"/_/api/namespaces", namespaceSaveJSON(tc.form))
		if err != nil {
			t.Fatalf("%s: POST /_/api/namespaces: %v", tc.name, err)
		}
		closeTestBody(t, resp.Body)
		if _, ok := app.Namespaces()["nope"]; ok {
			t.Errorf("%s: rejected save must not create the namespace", tc.name)
		}
		if _, _, err := app.Store.Read(wiki.NamespaceConfigPath("nope")); err == nil {
			t.Errorf("%s: rejected save must not write a config file", tc.name)
		}
	}
}

// TestDeleteNamespaceKeepsPages tests that resetting configuration preserves pages.
func TestDeleteNamespaceKeepsPages(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	adminLogin(t, server, client)

	for _, ns := range []string{"blog", "empty"} {
		resp, err := postJSON(t, client, server.URL+"/_/api/namespaces", namespaceSaveJSON(url.Values{
			"name": {ns}, "widgets": {"pages"}, "public": {"on"},
		}))
		if err != nil {
			t.Fatalf("creating %s: %v", ns, err)
		}
		closeTestBody(t, resp.Body)
	}
	if _, err := app.Store.Save(wiki.PageFile("blog/hello"), wiki.Page{Slug: "blog/hello", Title: "Hello"}.Encode(), "Add blog/hello", "test", "test@hmd.local"); err != nil {
		t.Fatalf("seeding blog/hello: %v", err)
	}

	resp, err := postJSON(t, client, server.URL+"/_/api/namespaces/reset", namespaceActionJSON(url.Values{"name": {"blog"}}))
	if err != nil {
		t.Fatalf("resetting blog: %v", err)
	}
	closeTestBody(t, resp.Body)
	if _, _, err := app.Store.Read(wiki.PageFile("blog/hello")); err != nil {
		t.Fatalf("reset must preserve indexed pages: %v", err)
	}
	if _, _, err := app.Store.Read(wiki.HiddenFile("blog/" + wiki.DefaultNewPageTemplate)); err != nil {
		t.Fatalf("reset must preserve hidden files: %v", err)
	}
	if _, _, err := app.Store.Read(wiki.NamespaceConfigPath("blog")); err == nil {
		t.Fatal("reset must remove the namespace configuration")
	}
	if cfg, ok := app.Namespaces()["blog"]; !ok {
		t.Fatal("reset must leave the namespace directory in the refreshed registry")
	} else if cfg.Configured {
		t.Fatalf("reset must refresh blog as unconfigured, got %+v", cfg)
	}

	resp, err = postJSON(t, client, server.URL+"/_/api/namespaces", namespaceSaveJSON(url.Values{"name": {"blog"}, "widgets": {"pages"}}))
	if err != nil {
		t.Fatalf("reconfigure blog: %v", err)
	}
	closeTestBody(t, resp.Body)

	for _, ns := range []string{"blog", "empty"} {
		resp, err := postJSON(t, client, server.URL+"/_/api/namespaces/delete", namespaceActionJSON(url.Values{"name": {ns}}))
		if err != nil {
			t.Fatalf("deleting %s: %v", ns, err)
		}
		body, _ := io.ReadAll(resp.Body)
		closeTestBody(t, resp.Body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("deleting non-empty %s = %d, want 400", ns, resp.StatusCode)
		}
		if !strings.Contains(string(body), "\"error\"") {
			t.Errorf("deleting non-empty %s should report a JSON error: %s", ns, body)
		}
	}

	cfg, ok := app.Namespaces()["blog"]
	if !ok {
		t.Fatal("blog should still be a namespace: it still contains a page")
	}
	if !cfg.Configured {
		t.Errorf("rejected deletion must leave blog config intact, got %+v", cfg)
	}
	if _, _, err := app.Store.Read(wiki.PageFile("blog/hello")); err != nil {
		t.Errorf("removing a namespace config must not touch its pages: %v", err)
	}

	if _, ok := app.Namespaces()["empty"]; !ok {
		t.Error("hidden namespace files must block deletion")
	}
}

// TestNamespaceNewPageFormRoundTrip tests namespace form value preservation.
func TestNamespaceNewPageFormRoundTrip(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	adminLogin(t, server, client)

	custom := `{{.User}}-x`
	if err := writeNamespaceConfig(t, app, "blog", "widgets: [pages]\nnew:\n  template: entry\n  slug: '"+custom+"'\n"); err != nil {
		t.Fatalf("writing namespace config: %v", err)
	}

	entries := namespaceListEntries(app.Namespaces(), "admin")
	var blog NamespaceListEntry
	for _, e := range entries {
		if e.Name == "blog" {
			blog = e
		}
	}
	if !blog.NewEnabled {
		t.Error("a namespace with a new: block should show the toggle on")
	}
	if blog.Template != "entry" {
		t.Errorf("template = %q, want the hand-written entry", blog.Template)
	}
	if blog.SlugPreset != presentation.SlugPresetCustom {
		t.Errorf("slug preset = %q, want %q for a pattern no preset produces", blog.SlugPreset, presentation.SlugPresetCustom)
	}
	if blog.TemplateHref != "/_/hidden/blog/entry?do=edit" {
		t.Errorf("template link = %q, want the hidden-page editor for blog/entry", blog.TemplateHref)
	}

	_, hash, err := app.Store.Read(wiki.NamespaceConfigPath("blog"))
	if err != nil {
		t.Fatalf("reading blog config hash: %v", err)
	}
	resp, err := postJSON(t, client, server.URL+"/_/api/namespaces", namespaceSaveJSON(url.Values{
		"name": {"blog"}, "widgets": {"pages"}, "template": {blog.Template},
		"new_enabled": {"on"}, "slug_preset": {"monthly"}, "basehash": {hash},
	}))
	if err != nil {
		t.Fatalf("saving blog: %v", err)
	}
	closeTestBody(t, resp.Body)

	cfg := app.Namespaces()["blog"]
	if cfg.New == nil || cfg.New.Template != "entry" {
		t.Errorf("new-page config = %+v, want the hand-written template preserved", cfg.New)
	}
	if cfg.New != nil && cfg.New.Slug != presentation.SlugPatternFor("monthly") {
		t.Errorf("slug = %q, want the monthly preset", cfg.New.Slug)
	}

	_, hash, err = app.Store.Read(wiki.NamespaceConfigPath("blog"))
	if err != nil {
		t.Fatalf("reading blog config hash: %v", err)
	}
	off, err := postJSON(t, client, server.URL+"/_/api/namespaces", namespaceSaveJSON(url.Values{
		"name": {"blog"}, "widgets": {"pages"}, "public": {"on"}, "template": {blog.Template},
		"slug_preset": {"monthly"}, "basehash": {hash},
	}))
	if err != nil {
		t.Fatalf("disabling new pages: %v", err)
	}
	closeTestBody(t, off.Body)

	cfg = app.Namespaces()["blog"]
	if cfg.New != nil {
		t.Errorf("new-page config = %+v, want nil once the toggle is off", cfg.New)
	}
	if !cfg.Public || strings.Join(cfg.Widgets, ",") != "pages" {
		t.Errorf("the rest of the config should be untouched, got %+v", cfg)
	}
}

// TestSeededTemplateExplainsItself tests the seeded template's documented fields.
func TestSeededTemplateExplainsItself(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	adminLogin(t, server, client)

	resp, err := postJSON(t, client, server.URL+"/_/api/namespaces", namespaceSaveJSON(url.Values{
		"name": {"blog"}, "widgets": {"pages"},
	}))
	if err != nil {
		t.Fatalf("creating blog: %v", err)
	}
	closeTestBody(t, resp.Body)

	content, _, err := app.Store.Read(wiki.HiddenFile("blog/" + wiki.DefaultNewPageTemplate))
	if err != nil {
		t.Fatalf("reading seeded template: %v", err)
	}
	tpl := wiki.ParsePage("blog/"+wiki.DefaultNewPageTemplate, content)

	for _, field := range api.NewPageTemplateFields {
		if !strings.Contains(tpl.Body, "`"+field+"`") {
			t.Errorf("seeded template should document %q as text, body was:\n%s", field, tpl.Body)
		}
		if !strings.Contains(tpl.Body, "{{"+field+"}}") {
			t.Errorf("seeded template should demonstrate %q live", field)
		}
	}

	data := api.NewPageTemplateData{Now: time.Now(), User: "admin", Namespace: "blog"}
	rendered, err := api.RenderNewPageText(tpl.Body, data)
	if err != nil {
		t.Fatalf("seeded template body does not render: %v", err)
	}
	if strings.Contains(rendered, "{{") {
		t.Errorf("rendered body still contains a template action:\n%s", rendered)
	}
	for _, want := range []string{"admin", "blog", time.Now().Format("2006-01-02")} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered body missing %q:\n%s", want, rendered)
		}
	}
	if title, err := api.RenderNewPageText(tpl.Title, data); err != nil {
		t.Errorf("seeded template title does not render: %v", err)
	} else if title != time.Now().Format("Monday, 2 January 2006") {
		t.Errorf("rendered title = %q, want today's long date", title)
	}
}

// TestRenderLiveTreeOnlyOpensCurrentPageAncestors tests ancestor branch expansion.
func TestRenderLiveTreeOnlyOpensCurrentPageAncestors(t *testing.T) {
	entries := []search.BacklinkEntry{
		{Slug: "docs/a/one", Title: "One"},
		{Slug: "docs/a/two", Title: "Two"},
		{Slug: "docs/b/three", Title: "Three"},
	}
	root := buildPageTree(entries, "docs", "", nil)
	html := string(renderLiveTree(root, "docs", "a/one"))

	if strings.Count(html, "<details open>") != 1 {
		t.Errorf("want exactly 1 open branch (the ancestor of the current page), got:\n%s", html)
	}
	if !strings.Contains(html, `<details><summary><span class="dir">b</span>`) {
		t.Errorf("branch \"b\" (not on the current page's path) should render closed:\n%s", html)
	}
	if !strings.Contains(html, `<details open><summary><span class="dir">a</span>`) {
		t.Errorf("branch \"a\" (ancestor of the current page) should render open:\n%s", html)
	}
}

func TestBuildPageTreeOrdersIndexAndSections(t *testing.T) {
	entries := []search.BacklinkEntry{
		{Slug: "docs/about", Title: "About"},
		{Slug: "docs/guides/setup", Title: "Setup"},
		{Slug: "docs/home", Title: "Home"},
		{Slug: "docs/reference/api", Title: "API"},
	}
	root := buildPageTree(entries, "docs", "home", []string{"reference", "guides"})

	got := make([]string, len(root.Children))
	for i, node := range root.Children {
		got[i] = node.Path
	}
	if want := []string{"home", "reference", "guides", "about"}; !slices.Equal(got, want) {
		t.Errorf("root tree = %v, want %v", got, want)
	}
}

// TestSaveNamespaceRejectsStaleBrowserUpdate verifies the browser's checked
// namespace write: a form carrying an out-of-date hash is rejected with a 409
// JSON conflict carrying the stale base hash, the current committed hash and the
// current committed config, while the concurrent configuration is preserved.
func TestSaveNamespaceRejectsStaleBrowserUpdate(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	adminLogin(t, server, client)

	saveConfiguredEmptyNamespace(t, app, "blog")
	_, staleHash, err := app.Store.Read(wiki.NamespaceConfigPath("blog"))
	if err != nil {
		t.Fatalf("reading blog hash: %v", err)
	}

	// A concurrent writer replaces the config after the form was rendered.
	concurrent := []byte("public: true\ntitle: Concurrent\n")
	if _, err := app.Store.Save(wiki.NamespaceConfigPath("blog"), concurrent, "Concurrent edit", "test", "test@hmd.local"); err != nil {
		t.Fatalf("concurrent save: %v", err)
	}
	if err := app.apiClient().RefreshNamespaces(); err != nil {
		t.Fatalf("refreshing namespaces: %v", err)
	}
	_, currentHash, err := app.Store.Read(wiki.NamespaceConfigPath("blog"))
	if err != nil {
		t.Fatalf("reading concurrent hash: %v", err)
	}

	resp, err := postJSON(t, client, server.URL+"/_/api/namespaces", namespaceSaveJSON(url.Values{
		"name": {"blog"}, "widgets": {"pages"}, "title": {"Submitted title"}, "basehash": {staleHash},
	}))
	if err != nil {
		t.Fatalf("stale save: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale save = %d, want 409", resp.StatusCode)
	}
	if got, _, err := app.Store.Read(wiki.NamespaceConfigPath("blog")); err != nil || string(got) != string(concurrent) {
		t.Fatalf("config after stale save = %q, %v; want the concurrent config preserved", got, err)
	}
	if !strings.Contains(string(body), `"conflict"`) {
		t.Errorf("conflict response should carry a conflict object: %s", body)
	}
	if !strings.Contains(string(body), `"base_hash":"`+staleHash+`"`) {
		t.Errorf("conflict response should echo the stale base hash %q: %s", staleHash, body)
	}
	if !strings.Contains(string(body), `"current_hash":"`+currentHash+`"`) {
		t.Errorf("conflict response should carry the current committed hash %q: %s", currentHash, body)
	}
	if !strings.Contains(string(body), `"title":"Concurrent"`) {
		t.Errorf("conflict response should carry the current committed config: %s", body)
	}
}

// TestSaveNamespaceRejectsSimultaneousCreation verifies that a browser create
// does not overwrite a configuration created concurrently: an empty basehash
// write fails when the configuration already exists.
func TestSaveNamespaceRejectsSimultaneousCreation(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	adminLogin(t, server, client)

	config := saveConfiguredEmptyNamespace(t, app, "blog")

	resp, err := postJSON(t, client, server.URL+"/_/api/namespaces", namespaceSaveJSON(url.Values{
		"name": {"blog"}, "widgets": {"pages"},
	}))
	if err != nil {
		t.Fatalf("simultaneous create: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("simultaneous create = %d, want 409", resp.StatusCode)
	}
	if got, _, err := app.Store.Read(wiki.NamespaceConfigPath("blog")); err != nil || string(got) != string(config) {
		t.Fatalf("config after simultaneous create = %q, %v; want the existing config preserved", got, err)
	}
}

// TestSaveNamespaceCreatesConfigForImplicitNamespace verifies that a namespace
// with no configuration file yet can still be configured with an empty basehash,
// while a concurrent create is rejected.
func TestSaveNamespaceCreatesConfigForImplicitNamespace(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	adminLogin(t, server, client)

	// An implicit namespace: a page directory with no .namespace.yaml.
	if _, err := app.Store.Save(wiki.PageFile("implicit/note"), wiki.Page{Slug: "implicit/note", Title: "Note"}.Encode(), "Add implicit/note", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving implicit page: %v", err)
	}
	if err := app.apiClient().RefreshNamespaces(); err != nil {
		t.Fatalf("refreshing namespaces: %v", err)
	}

	resp, err := postJSON(t, client, server.URL+"/_/api/namespaces", namespaceSaveJSON(url.Values{"name": {"implicit"}, "widgets": {"pages"}}))
	if err != nil {
		t.Fatalf("configuring implicit namespace: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("configuring implicit namespace = %d, want 200", resp.StatusCode)
	}
	if _, _, err := app.Store.Read(wiki.NamespaceConfigPath("implicit")); err != nil {
		t.Fatalf("implicit namespace config missing after save: %v", err)
	}

	// A concurrent create must not overwrite the freshly written configuration.
	resp, err = postJSON(t, client, server.URL+"/_/api/namespaces", namespaceSaveJSON(url.Values{"name": {"implicit"}, "widgets": {"tags"}}))
	if err != nil {
		t.Fatalf("simultaneous create: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("simultaneous create = %d, want 409", resp.StatusCode)
	}
	if cfg := app.Namespaces()["implicit"]; strings.Join(cfg.Widgets, ",") != "pages" {
		t.Fatalf("implicit namespace widgets = %v, want the first create preserved", cfg.Widgets)
	}
}
