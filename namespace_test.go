package main

import (
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
	if cfg.New == nil || cfg.New.Template != "entry" {
		t.Errorf("New = %+v", cfg.New)
	}
}

func TestParseNamespaceConfigUnknownKeyRejected(t *testing.T) {
	yaml := []byte("widgets: [calendar]\nbogus: true\n")
	if _, err := parseNamespaceConfig(yaml); err == nil {
		t.Error("expected error for unknown key, got nil")
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

func TestNamespaceFor(t *testing.T) {
	tests := []struct {
		slug   string
		wantNS string
		wantR  string
	}{
		{"blog/drafts/post", "blog", "drafts/post"},
		{"readme", "", "readme"},
		{"journal/2026-07-27", "journal", "2026-07-27"},
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

// TestCreateNamespaceFromAdmin covers the namespaces panel's write path:
// posting a name that doesn't exist yet creates the namespace, its new-page
// template page is seeded so ctrl-j works immediately, and the registry
// reflects all of it without waiting for pollFS.
func TestCreateNamespaceFromAdmin(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	adminLogin(t, server, client)

	resp, err := client.PostForm(server.URL+"/_/settings/namespaces", url.Values{
		"name": {"blog"}, "widgets": {"search, backlinks"}, "public": {"on"},
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
	if got := strings.Join(cfg.Widgets, ","); got != "search,backlinks" {
		t.Errorf("widgets = %q, want search,backlinks", got)
	}
	if !cfg.Public {
		t.Error("blog should be public")
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

	// ctrl-j in the namespace now works end to end: POST /_/new creates
	// today's page from that template and redirects to its editor.
	newResp, err := client.Post(server.URL+"/_/new?ns=blog", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST /_/new?ns=blog: %v", err)
	}
	closeTestBody(t, newResp.Body)
	todaySlug := "blog/" + time.Now().Format("2006-01-02")
	if _, _, err := app.Store.Read(pageFile(todaySlug)); err != nil {
		t.Errorf("reading %s: %v", pageFile(todaySlug), err)
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
			"name": {ns}, "widgets": {"search"}, "public": {"on"},
		})
		if err != nil {
			t.Fatalf("creating %s: %v", ns, err)
		}
		closeTestBody(t, resp.Body)
	}
	if _, err := app.Store.Save(pageFile("blog/hello"), Page{Slug: "blog/hello", Title: "Hello"}.Encode(), "Add blog/hello", "test", "test@hmd.local"); err != nil {
		t.Fatalf("seeding blog/hello: %v", err)
	}

	for _, ns := range []string{"blog", "empty"} {
		resp, err := client.PostForm(server.URL+"/_/settings/namespaces/delete", url.Values{"name": {ns}})
		if err != nil {
			t.Fatalf("deleting %s: %v", ns, err)
		}
		closeTestBody(t, resp.Body)
	}

	// blog still holds a page, so it stays a namespace — on the defaults,
	// and no longer public.
	cfg, ok := app.Namespaces()["blog"]
	if !ok {
		t.Fatal("blog should still be a namespace: it still contains a page")
	}
	if cfg.Public || cfg.Configured {
		t.Errorf("blog should be back on the defaults, got %+v", cfg)
	}
	if _, _, err := app.Store.Read(pageFile("blog/hello")); err != nil {
		t.Errorf("removing a namespace config must not touch its pages: %v", err)
	}

	// empty held nothing else, so the directory went with the config.
	if _, ok := app.Namespaces()["empty"]; ok {
		t.Error("an empty namespace should disappear once its config is removed")
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
	custom := `{{.User}}/x`
	if err := writeNamespaceConfig(t, app, "blog", "widgets: [search]\nnew:\n  template: entry\n  slug: '"+custom+"'\n"); err != nil {
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
		"name": {"blog"}, "widgets": {"search"}, "template": {blog.Template},
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
		"name": {"blog"}, "widgets": {"search"}, "public": {"on"}, "template": {blog.Template},
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
	if !cfg.Public || strings.Join(cfg.Widgets, ",") != "search" {
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
		"name": {"blog"}, "widgets": {"search"},
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
