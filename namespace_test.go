package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	cfg, err := parseNamespaceConfig(yaml)
	if err != nil {
		t.Fatalf("parseNamespaceConfig: %v", err)
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
	_, err := normaliseNamespaceConfig("notes", NamespaceConfig{Description: strings.Repeat("x", maxNamespaceDescriptionRunes+1)}, newPageTemplateData{})
	if err == nil {
		t.Errorf("description longer than %d characters was accepted", maxNamespaceDescriptionRunes)
	}
}

func TestParseNamespaceConfigUnknownKeyRejected(t *testing.T) {
	yaml := []byte("widgets: [calendar]\nbogus: true\n")
	if _, err := parseNamespaceConfig(yaml); err == nil {
		t.Error("expected error for unknown key, got nil")
	}
}

func TestNormaliseNamespaceConfigAllowsRoot(t *testing.T) {
	if _, err := normaliseNamespaceConfig("", NamespaceConfig{Widgets: []string{"pages"}}, newPageTemplateData{}); err != nil {
		t.Fatalf("normalise root namespace config: %v", err)
	}
}

func TestLoadNamespaceConfigMissingFileIsDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg := loadNamespaceConfig(dir, "blog")
	if cfg.Public {
		t.Error("missing config should default to private")
	}
	if len(cfg.Widgets) != len(builtinWidgets) {
		t.Errorf("Widgets = %v, want built-in defaults", cfg.Widgets)
	}
}

func TestLoadNamespaceConfigMalformedYAMLFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, namespaceConfigFile), []byte("widgets: [oops\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := loadNamespaceConfig(dir, "blog")
	if cfg.Public {
		t.Error("malformed config should fall back to private default")
	}
	if len(cfg.Widgets) != len(builtinWidgets) {
		t.Errorf("Widgets = %v, want built-in defaults", cfg.Widgets)
	}
}

func TestLoadNamespaceConfigUnknownWidgetIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, namespaceConfigFile), []byte("widgets: [not-a-real-widget]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := loadNamespaceConfig(dir, "blog")
	if len(cfg.Widgets) != len(builtinWidgets) {
		t.Errorf("Widgets = %v, want built-in defaults after unknown id", cfg.Widgets)
	}
}

func TestLoadNamespaceConfigInvalidSettingsFallBackToDefaults(t *testing.T) {
	for name, yaml := range map[string]string{
		"skin":    "skin: unknown\n",
		"palette": "palette: unknown\n",
		"index":   "index: ../private\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, namespaceConfigFile), []byte(yaml), 0644); err != nil {
				t.Fatal(err)
			}
			cfg := loadNamespaceConfig(dir, "blog")
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
		{"notes", "notes", ""}, // a single segment names a namespace, not a page
		{"notes/2026-07-27", "notes", "2026-07-27"},
	}
	for _, tt := range tests {
		ns, rest := namespaceFor(tt.slug)
		if ns != tt.wantNS || rest != tt.wantR {
			t.Errorf("namespaceFor(%q) = (%q, %q), want (%q, %q)", tt.slug, ns, rest, tt.wantNS, tt.wantR)
		}
	}
}

func TestValidNamespaceNameRejectsReserved(t *testing.T) {
	if validNamespaceName("_") {
		t.Error("_ must be rejected as a namespace name")
	}
	if validNamespaceName(".hidden") {
		t.Error("dot-prefixed directories must be rejected as namespaces")
	}
	if validNamespaceName(attachmentsDir) {
		t.Error("the attachments directory must be rejected as a namespace name")
	}
	if !validNamespaceName("blog") {
		t.Error("blog should be a valid namespace name")
	}
}

func TestBuildNamespaceRegistry(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "blog"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blog", namespaceConfigFile), []byte("public: true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Reserved and dot-prefixed directories must not become namespaces.
	if err := os.Mkdir(filepath.Join(dir, "_"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	reg, err := BuildNamespaceRegistry(dir)
	if err != nil {
		t.Fatalf("BuildNamespaceRegistry: %v", err)
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
	reg := NamespaceRegistry{"": defaultNamespaceConfig()}
	cfg := reg.Resolve("nope/page")
	if cfg.Public {
		t.Error("unknown namespace should default to private")
	}
	if len(cfg.Widgets) != len(builtinWidgets) {
		t.Errorf("Widgets = %v, want built-in defaults", cfg.Widgets)
	}
}

func TestNamespaceRegistryNamesRootFirst(t *testing.T) {
	reg := NamespaceRegistry{"": {}, "zeta": {}, "alpha": {}}
	names := reg.Names()
	if len(names) != 3 || names[0] != "" || names[1] != "alpha" || names[2] != "zeta" {
		t.Errorf("Names() = %v", names)
	}
}

func TestNamespaceCatalogue(t *testing.T) {
	reg := NamespaceRegistry{
		"":     defaultNamespaceConfig(),
		"blog": {Configured: true},
	}
	titles := map[string]string{
		"readme":      "Root page",
		"notes/entry": "Notes entry",
	}

	summaries := namespaceSummaries(reg, titles)
	if len(summaries) != 2 {
		t.Fatalf("namespaceSummaries() returned %d entries, want 2: %+v", len(summaries), summaries)
	}
	if summaries[0].Name != "blog" || summaries[1].Name != "notes" {
		t.Fatalf("namespaceSummaries() names = [%s, %s], want [blog, notes]", summaries[0].Name, summaries[1].Name)
	}
	if summaries[0].Count != 0 || len(summaries[0].Pages) != 0 {
		t.Errorf("empty blog summary = %+v, want zero pages", summaries[0])
	}
	if summaries[1].Count != 1 || len(summaries[1].Pages) != 1 || summaries[1].Pages[0].Slug != "notes/entry" {
		t.Errorf("notes summary = %+v, want one notes/entry page", summaries[1])
	}
}

// TestCreateNamespaceFromAdmin covers the namespaces panel's write path:
// posting a name that doesn't exist yet creates the namespace, its new-page
// template page is seeded so ctrl-j works immediately, and the registry
// reflects all of it without waiting for pollFS.
func TestCreateNamespaceFromAdmin(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	adminLogin(t, server, client)

	resp, err := client.PostForm(server.URL+"/_/settings/namespaces", url.Values{
		"name": {"blog"}, "widgets": {"backlinks"}, "public": {"on"},
		"description": {"Writing and research"},
		"new_enabled": {"on"}, "slug_preset": {"daily"},
	})
	if err != nil {
		t.Fatalf("POST /_/settings/namespaces: %v", err)
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
	if cfg.New == nil || cfg.New.Template != defaultNewPageTemplate {
		t.Errorf("new-page config = %+v, want template %q", cfg.New, defaultNewPageTemplate)
	}
	if cfg.New != nil && cfg.New.Slug != slugPatternFor("daily") {
		t.Errorf("slug pattern = %q, want the daily preset %q", cfg.New.Slug, slugPatternFor("daily"))
	}
	if !cfg.Configured {
		t.Error("a namespace with a saved .namespace.yaml should report itself configured")
	}

	// The config and the seeded template page are both in the repo.
	if _, _, err := app.Store.Read(namespaceConfigPath("blog")); err != nil {
		t.Errorf("reading blog/.namespace.yaml: %v", err)
	}
	if _, _, err := app.Store.Read(hiddenFile("blog/" + defaultNewPageTemplate)); err != nil {
		t.Errorf("reading seeded template %s: %v", hiddenFile("blog/"+defaultNewPageTemplate), err)
	}

	// ctrl-j in the namespace now works end to end: POST /_/new renders
	// today's page from that template directly, as a draft that isn't
	// persisted until Save.
	newResp, err := client.Post(server.URL+"/_/new?ns=blog", "application/x-www-form-urlencoded", nil)
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

// TestNamespaceManagement covers the settings-scoped namespace directory and
// the focused editor routes.
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

	resp, err := client.PostForm(server.URL+"/_/settings/namespaces", url.Values{"name": {"blog"}})
	if err != nil {
		t.Fatalf("create blog: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/_/namespaces") {
		t.Errorf("create redirect = %d %q, want 303 /_/namespaces", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, err = client.Get(server.URL + "/_/namespaces")
	if err != nil {
		t.Fatalf("GET namespace directory: %v", err)
	}
	content, err := io.ReadAll(resp.Body)
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
	closeTestBody(t, resp.Body)
	body = string(content)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `name="name"`) || !strings.Contains(body, `/delete-all`) || !strings.Contains(body, `Published view preview`) || !strings.Contains(body, `sidebar-tree`) || !strings.Contains(body, `ON THIS PAGE`) {
		t.Errorf("namespace editor = %d, body missing focused form: %s", resp.StatusCode, body)
	}

	if _, _, err := app.Store.Read(hiddenFile("blog/" + defaultNewPageTemplate)); err != nil {
		t.Fatalf("seeded template missing: %v", err)
	}
}

func saveConfiguredEmptyNamespace(t *testing.T, app *App, name string) []byte {
	t.Helper()
	content := []byte("public: true\n")
	if _, err := app.Store.Save(namespaceConfigPath(name), content, "Configure namespace "+name, "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving %s config: %v", name, err)
	}
	app.refreshNamespaces()
	return content
}

func postNamespaceDelete(t *testing.T, server *httptest.Server, client *http.Client, name string) *http.Response {
	t.Helper()
	resp, err := client.PostForm(server.URL+"/_/settings/namespaces/delete", url.Values{"name": {name}})
	if err != nil {
		t.Fatalf("deleting %q: %v", name, err)
	}
	return resp
}

func postNamespaceDeleteAll(t *testing.T, server *httptest.Server, client *http.Client, name string) *http.Response {
	t.Helper()
	resp, err := client.PostForm(server.URL+"/_/settings/namespaces/delete-all", url.Values{"name": {name}})
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
	page := Page{Slug: "blog/post", Title: "Post", Body: "content"}
	if _, err := app.Store.Save(pageFile(page.Slug), page.Encode(), "Add blog/post", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving indexed page: %v", err)
	}
	if err := app.Index.Update(page); err != nil {
		t.Fatalf("indexing page: %v", err)
	}

	resp := postNamespaceDelete(t, server, client, "blog")
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deleting namespace with indexed page = %d, want 200", resp.StatusCode)
	}

	got, _, err := app.Store.Read(namespaceConfigPath("blog"))
	if err != nil || string(got) != string(config) {
		t.Fatalf("config after rejected indexed-page deletion = %q, %v; want %q", got, err, config)
	}
	if _, _, err := app.Store.Read(pageFile(page.Slug)); err != nil {
		t.Fatalf("indexed page after rejected deletion: %v", err)
	}
}

func TestNamespaceManagementRejectsHiddenFileWithoutMutation(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	adminLogin(t, server, client)

	config := saveConfiguredEmptyNamespace(t, app, "blog")
	hidden := Page{Slug: "blog/template", Title: "Template", Body: "hidden"}
	if _, err := app.Store.Save(hiddenFile(hidden.Slug), hidden.Encode(), "Add hidden template", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving hidden template: %v", err)
	}

	resp := postNamespaceDelete(t, server, client, "blog")
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deleting namespace with hidden file = %d, want 200", resp.StatusCode)
	}

	got, _, err := app.Store.Read(namespaceConfigPath("blog"))
	if err != nil || string(got) != string(config) {
		t.Fatalf("config after rejected hidden-file deletion = %q, %v; want %q", got, err, config)
	}
	if _, _, err := app.Store.Read(hiddenFile(hidden.Slug)); err != nil {
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
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deleting namespace with ordinary content = %d, want 200", resp.StatusCode)
	}

	got, _, err := app.Store.Read(namespaceConfigPath("blog"))
	if err != nil || string(got) != string(config) {
		t.Fatalf("config after rejected ordinary-content deletion = %q, %v; want %q", got, err, config)
	}
	if content, err := os.ReadFile(extra); err != nil || string(content) != "keep me" {
		t.Fatalf("ordinary content after rejected deletion = %q, %v", content, err)
	}
}

func TestStoreDeleteNamespaceRejectsContentWithoutMutation(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(Config{
		RepoDir: tmpDir,
		AppDir:  t.TempDir(),
		Git:     GitConfig{User: "test"},
	})
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	config := []byte("public: true\n")
	page := []byte("keep me\n")
	if _, err := store.Save(namespaceConfigPath("blog"), config, "configure blog", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving namespace config: %v", err)
	}
	if _, err := store.Save(pageFile("blog/post"), page, "add blog/post", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving namespace page: %v", err)
	}

	if err := store.DeleteNamespace("blog", "delete blog", "test", "test@hmd.local"); err == nil {
		t.Fatal("deleting namespace with a page should be rejected")
	}
	if got, _, err := store.Read(namespaceConfigPath("blog")); err != nil || string(got) != string(config) {
		t.Fatalf("config after rejected deletion = %q, %v; want %q", got, err, config)
	}
	if got, _, err := store.Read(pageFile("blog/post")); err != nil || string(got) != string(page) {
		t.Fatalf("page after rejected deletion = %q, %v; want %q", got, err, page)
	}
}

func TestNamespaceManagementDeletesGenuinelyEmptyConfiguredNamespace(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	adminLogin(t, server, client)
	saveConfiguredEmptyNamespace(t, app, "empty")

	resp := postNamespaceDelete(t, server, client, "empty")
	location := resp.Header.Get("Location")
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location, "/_/namespaces") {
		t.Fatalf("deleting empty namespace = %d %q, want 303 /_/namespaces", resp.StatusCode, location)
	}
	if _, _, err := app.Store.Read(namespaceConfigPath("empty")); err == nil {
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
	page := Page{Slug: "blog/post", Title: "Post", Body: "content"}
	if _, err := app.Store.Save(pageFile(page.Slug), page.Encode(), "Add blog/post", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving namespace page: %v", err)
	}
	if err := app.Index.Update(page); err != nil {
		t.Fatalf("indexing namespace page: %v", err)
	}
	if _, err := app.Store.Save("blog/drafts/note.txt", []byte("note"), "Add namespace file", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving nested namespace file: %v", err)
	}
	if _, err := app.Store.Save(hiddenFile("blog/template"), Page{Slug: "blog/template", Title: "Template"}.Encode(), "Add namespace template", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving namespace template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(app.config().RepoDir, "blog", "upload.bin"), []byte("upload"), 0644); err != nil {
		t.Fatalf("saving untracked namespace file: %v", err)
	}

	resp := postNamespaceDeleteAll(t, server, client, "blog")
	location := resp.Header.Get("Location")
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location, "/_/namespaces") {
		t.Fatalf("delete all = %d %q, want 303 /_/namespaces", resp.StatusCode, location)
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
			config, _, err := app.Store.Read(namespaceConfigPath(testNS))
			if err != nil {
				t.Fatal(err)
			}

			resp := postNamespaceDelete(t, server, client, name)
			closeTestBody(t, resp.Body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("deleting %q = %d, want 400", name, resp.StatusCode)
			}
			got, _, err := app.Store.Read(namespaceConfigPath(testNS))
			if err != nil || string(got) != string(config) {
				t.Fatalf("notes config after rejected %q deletion = %q, %v; want %q", name, got, err, config)
			}
		})
	}
}

// TestSaveNamespaceRejectsBadInput checks the form's validation: a rejected
// save must not create the namespace or write anything.
func TestSaveNamespaceRejectsBadInput(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	adminLogin(t, server, client)

	cases := []struct {
		name string
		form url.Values
	}{
		{"unknown widget", url.Values{"name": {"nope"}, "widgets": {"not-a-widget"}}},
		{"reserved name", url.Values{"name": {reservedNamespace}}},
		{"dot-prefixed name", url.Values{"name": {".hidden"}}},
		{"nested name", url.Values{"name": {"a/b"}}},
		{"custom pattern left empty", url.Values{"name": {"nope"}, "new_enabled": {"on"}, "slug_preset": {"custom"}}},
		{"unparseable pattern", url.Values{"name": {"nope"}, "new_enabled": {"on"}, "slug_preset": {"custom"}, "slug_custom": {"{{.Now"}}},
		// Renders to "2026/07-28": a namespace is one level deep, so a
		// pattern that produces a subdirectory could never be created.
		{"pattern rendering a slash", url.Values{"name": {"nope"}, "new_enabled": {"on"}, "slug_preset": {"custom"}, "slug_custom": {`{{.Now.Format "2006/01-02"}}`}}},
		{"invalid template page name", url.Values{"name": {"nope"}, "new_enabled": {"on"}, "slug_preset": {"daily"}, "template": {".hidden"}}},
	}
	for _, tc := range cases {
		resp, err := client.PostForm(server.URL+"/_/settings/namespaces", tc.form)
		if err != nil {
			t.Fatalf("%s: POST /_/settings/namespaces: %v", tc.name, err)
		}
		closeTestBody(t, resp.Body)
		if _, ok := app.Namespaces()["nope"]; ok {
			t.Errorf("%s: rejected save must not create the namespace", tc.name)
		}
		if _, _, err := app.Store.Read(namespaceConfigPath("nope")); err == nil {
			t.Errorf("%s: rejected save must not write a config file", tc.name)
		}
	}
}

// TestDeleteNamespaceKeepsPages checks that removing a namespace's config
// reverts it to the defaults without touching the pages filed under it, and
// that a namespace holding nothing else disappears entirely.
func TestDeleteNamespaceKeepsPages(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	adminLogin(t, server, client)

	for _, ns := range []string{"blog", "empty"} {
		resp, err := client.PostForm(server.URL+"/_/settings/namespaces", url.Values{
			"name": {ns}, "widgets": {"pages"}, "public": {"on"},
		})
		if err != nil {
			t.Fatalf("creating %s: %v", ns, err)
		}
		closeTestBody(t, resp.Body)
	}
	if _, err := app.Store.Save(pageFile("blog/hello"), Page{Slug: "blog/hello", Title: "Hello"}.Encode(), "Add blog/hello", "test", "test@hmd.local"); err != nil {
		t.Fatalf("seeding blog/hello: %v", err)
	}

	// Reset is explicitly non-destructive and leaves the indexed page and
	// hidden template in place.
	resp, err := client.PostForm(server.URL+"/_/settings/namespaces/reset", url.Values{"name": {"blog"}})
	if err != nil {
		t.Fatalf("resetting blog: %v", err)
	}
	closeTestBody(t, resp.Body)
	if _, _, err := app.Store.Read(pageFile("blog/hello")); err != nil {
		t.Fatalf("reset must preserve indexed pages: %v", err)
	}
	if _, _, err := app.Store.Read(hiddenFile("blog/" + defaultNewPageTemplate)); err != nil {
		t.Fatalf("reset must preserve hidden files: %v", err)
	}
	if _, _, err := app.Store.Read(namespaceConfigPath("blog")); err == nil {
		t.Fatal("reset must remove the namespace configuration")
	}
	if cfg, ok := app.Namespaces()["blog"]; !ok {
		t.Fatal("reset must leave the namespace directory in the refreshed registry")
	} else if cfg.Configured {
		t.Fatalf("reset must refresh blog as unconfigured, got %+v", cfg)
	}
	// Reconfigure it so the separate deletion guard can prove rejection leaves
	// the configuration intact as well.
	resp, err = client.PostForm(server.URL+"/_/settings/namespaces", url.Values{"name": {"blog"}, "widgets": {"pages"}})
	if err != nil {
		t.Fatalf("reconfigure blog: %v", err)
	}
	closeTestBody(t, resp.Body)

	// True deletion rejects a namespace with either indexed content or a
	// hidden namespace file, and must leave its config intact.
	for _, ns := range []string{"blog", "empty"} {
		resp, err := client.PostForm(server.URL+"/_/settings/namespaces/delete", url.Values{"name": {ns}})
		if err != nil {
			t.Fatalf("deleting %s: %v", ns, err)
		}
		closeTestBody(t, resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("deleting non-empty %s = %d, want 200 with error form", ns, resp.StatusCode)
		}
	}

	// blog still holds a page, so it stays configured and is no longer public.
	cfg, ok := app.Namespaces()["blog"]
	if !ok {
		t.Fatal("blog should still be a namespace: it still contains a page")
	}
	if !cfg.Configured {
		t.Errorf("rejected deletion must leave blog config intact, got %+v", cfg)
	}
	if _, _, err := app.Store.Read(pageFile("blog/hello")); err != nil {
		t.Errorf("removing a namespace config must not touch its pages: %v", err)
	}

	// empty has the seeded hidden template, so it is not deletable either.
	if _, ok := app.Namespaces()["empty"]; !ok {
		t.Error("hidden namespace files must block deletion")
	}
}

// TestNamespaceNewPageFormRoundTrip covers what the form does and doesn't
// decide for you: the template page name is a convention carried through as a
// hidden field (a hand-written one survives a save), the slug comes from a
// preset unless it matches none, and unticking the toggle drops the `new:`
// block without touching anything else.
func TestNamespaceNewPageFormRoundTrip(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	adminLogin(t, server, client)

	// A hand-written config: non-default template name, pattern no preset produces.
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
	if blog.SlugPreset != slugPresetCustom {
		t.Errorf("slug preset = %q, want %q for a pattern no preset produces", blog.SlugPreset, slugPresetCustom)
	}
	if blog.TemplateHref != "/_/hidden/blog/entry?do=edit" {
		t.Errorf("template link = %q, want the hidden-page editor for blog/entry", blog.TemplateHref)
	}

	// Saving the form as rendered keeps the hand-written template name.
	resp, err := client.PostForm(server.URL+"/_/settings/namespaces", url.Values{
		"name": {"blog"}, "widgets": {"pages"}, "template": {blog.Template},
		"new_enabled": {"on"}, "slug_preset": {"monthly"},
	})
	if err != nil {
		t.Fatalf("saving blog: %v", err)
	}
	closeTestBody(t, resp.Body)

	cfg := app.Namespaces()["blog"]
	if cfg.New == nil || cfg.New.Template != "entry" {
		t.Errorf("new-page config = %+v, want the hand-written template preserved", cfg.New)
	}
	if cfg.New != nil && cfg.New.Slug != slugPatternFor("monthly") {
		t.Errorf("slug = %q, want the monthly preset", cfg.New.Slug)
	}

	// Unticking the toggle drops new: and leaves the rest alone.
	off, err := client.PostForm(server.URL+"/_/settings/namespaces", url.Values{
		"name": {"blog"}, "widgets": {"pages"}, "public": {"on"}, "template": {blog.Template},
		"slug_preset": {"monthly"},
	})
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

// TestSeededTemplateExplainsItself covers the default template page: every
// namespace created from the form gets one even with ctrl-j off, it documents
// exactly the fields newPageTemplateData carries, and it renders cleanly —
// the field names in its table are inert text, so it reads the same in its own
// editor as in a page created from it.
func TestSeededTemplateExplainsItself(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	adminLogin(t, server, client)

	// No new_enabled: a namespace with ctrl-j off still gets a template.
	resp, err := client.PostForm(server.URL+"/_/settings/namespaces", url.Values{
		"name": {"blog"}, "widgets": {"pages"},
	})
	if err != nil {
		t.Fatalf("creating blog: %v", err)
	}
	closeTestBody(t, resp.Body)

	content, _, err := app.Store.Read(hiddenFile("blog/" + defaultNewPageTemplate))
	if err != nil {
		t.Fatalf("reading seeded template: %v", err)
	}
	tpl := ParsePage("blog/"+defaultNewPageTemplate, content)

	for _, field := range newPageTemplateFields {
		if !strings.Contains(tpl.Body, "`"+field+"`") {
			t.Errorf("seeded template should document %q as text, body was:\n%s", field, tpl.Body)
		}
		if !strings.Contains(tpl.Body, "{{"+field+"}}") {
			t.Errorf("seeded template should demonstrate %q live", field)
		}
	}

	// It renders as a real template: no leftover actions, values substituted.
	data := newPageTemplateData{Now: time.Now(), User: "admin", Namespace: "blog"}
	rendered, err := renderNewPageText(tpl.Body, data)
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
	if title, err := renderNewPageText(tpl.Title, data); err != nil {
		t.Errorf("seeded template title does not render: %v", err)
	} else if title != time.Now().Format("Monday, 2 January 2006") {
		t.Errorf("rendered title = %q, want today's long date", title)
	}
}

// TestRenderLiveTreeOnlyOpensCurrentPageAncestors covers the fix for a bug
// where every <details> in the sidebar tree rendered open regardless of
// which page you're on — expanding the entire namespace on every visit.
// Only the branches leading to the current page should start open.
func TestRenderLiveTreeOnlyOpensCurrentPageAncestors(t *testing.T) {
	entries := []BacklinkEntry{
		{Slug: "docs/a/one", Title: "One"},
		{Slug: "docs/a/two", Title: "Two"},
		{Slug: "docs/b/three", Title: "Three"},
	}
	root := buildPageTree(entries, "docs")
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
