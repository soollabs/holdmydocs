package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

// TestNamespaceIndex ensures namespace indexes list pages and respect publication settings.
func TestNamespaceIndex(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	for _, slug := range []string{"blog/first", "blog/second"} {
		if _, err := app.Store.Save(pageFile(slug), Page{Slug: slug, Title: strings.ToUpper(slug), Body: "hi"}.Encode(), "add", "t", "t@e"); err != nil {
			t.Fatalf("saving %s: %v", slug, err)
		}
		if err := app.Index.Update(Page{Slug: slug, Title: strings.ToUpper(slug), Body: "hi"}); err != nil {
			t.Fatalf("indexing %s: %v", slug, err)
		}
	}

	body := func(c *http.Client, path string) (int, string) {
		resp, err := c.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer closeTestBody(t, resp.Body)
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	mustStatus := func(c *http.Client, path string, want int) {
		t.Helper()
		code, _ := body(c, path)
		if code != want {
			t.Fatalf("GET %s = %d, want %d", path, code, want)
		}
	}

	mustStatus(client, "/blog/", http.StatusOK)
	mustStatus(client, "/blog", http.StatusOK)
	_, page := body(client, "/blog/")
	for _, want := range []string{"/blog/first", "/blog/second", "2 pages", `href="/_/namespaces/blog/edit"`} {
		if !strings.Contains(page, want) {
			t.Errorf("namespace index missing %q, body: %s", want, page)
		}
	}
	if strings.Contains(page, "blog/.md") || strings.Contains(page, `name: 'rename'`) {
		t.Fatal("namespace index exposed page-only UI")
	}
	if strings.Contains(page, `href="/_/admin#namespaces"`) {
		t.Errorf("namespace index still links to the removed admin panel")
	}

	if _, home := body(client, "/"+testHome); !strings.Contains(home, `href="/blog/"`) {
		t.Errorf("sidebar has no link to /blog/, body: %s", home)
	}

	anon := &http.Client{}
	if err := writeNamespaceConfig(t, app, "private", "public: false\n"); err != nil {
		t.Fatalf("configuring private namespace: %v", err)
	}
	unknownCode, unknown := body(anon, "/unknown/")
	privateCode, private := body(anon, "/private/")
	if unknownCode != http.StatusNotFound || privateCode != http.StatusNotFound {
		t.Fatalf("anonymous unknown/private statuses = %d/%d, want 404/404", unknownCode, privateCode)
	}
	if unknown != private {
		t.Errorf("anonymous unknown/private 404 bodies differ:\nunknown: %q\nprivate: %q", unknown, private)
	}

	if err := writeNamespaceConfig(t, app, "blog", "public: true\n"); err != nil {
		t.Fatalf("publishing blog: %v", err)
	}
	code, pub := body(anon, "/blog/")
	if code != http.StatusOK {
		t.Fatalf("anonymous GET /blog/ = %d once published, want 200", code)
	}
	if !strings.Contains(pub, "/blog/first") {
		t.Errorf("published index missing its pages, body: %s", pub)
	}
}

func TestNamespaceIndexEmptyStateAndCreateScope(t *testing.T) {
	app, server, admin := newTestAppFull(t)
	defer server.Close()
	if err := writeNamespaceConfig(t, app, "empty", "public: true\n"); err != nil {
		t.Fatalf("configuring empty namespace: %v", err)
	}

	get := func(client *http.Client) string {
		resp, err := client.Get(server.URL + "/empty/")
		if err != nil {
			t.Fatalf("GET empty namespace: %v", err)
		}
		defer closeTestBody(t, resp.Body)
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	body := get(admin)
	for _, want := range []string{"0 pages", "No pages in this namespace yet", `id="new-page"`, `href="/empty/new?do=edit"`} {
		if !strings.Contains(body, want) {
			t.Errorf("empty namespace missing %q, body: %s", want, body)
		}
	}

	if err := app.Auth.AddUser("reader", "password12345"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := app.Auth.SetScopes("reader", []string{"read"}); err != nil {
		t.Fatalf("SetScopes: %v", err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	reader := &http.Client{Jar: jar}
	resp, err := reader.PostForm(server.URL+"/_/login", url.Values{"username": {"reader"}, "password": {"password12345"}})
	if err != nil {
		t.Fatalf("reader login: %v", err)
	}
	closeTestBody(t, resp.Body)
	if got := get(reader); strings.Contains(got, `id="new-page"`) {
		t.Fatal("read-scoped user can see the +new button")
	}
}

// TestNamespaceIndexCustomPage ensures a configured index page replaces the listing when it exists.
func TestNamespaceIndexCustomPage(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	savePage(t, app, "docs/home", "Welcome to the docs")
	savePage(t, app, "docs/other", "Another page")

	get := func(path string) (int, string) {
		resp, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer closeTestBody(t, resp.Body)
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, body := get("/docs/"); code != http.StatusOK || strings.Contains(body, "Welcome to the docs") {
		t.Fatalf("expected page list before index is configured, got %d: %s", code, body)
	}

	if err := writeNamespaceConfig(t, app, "docs", "index: home\n"); err != nil {
		t.Fatalf("configuring index: %v", err)
	}
	if code, body := get("/docs/"); code != http.StatusOK || !strings.Contains(body, "Welcome to the docs") {
		t.Fatalf("expected index page content, got %d: %s", code, body)
	}

	if err := writeNamespaceConfig(t, app, "docs", "index: missing\n"); err != nil {
		t.Fatalf("configuring missing index: %v", err)
	}
	if code, body := get("/docs/"); code != http.StatusOK || !strings.Contains(body, "/docs/other") {
		t.Fatalf("expected fallback to page list, got %d: %s", code, body)
	}
}

// TestNamespaceWikiLinkResolvesWithinNamespace ensures title links resolve to namespaced pages.
func TestNamespaceWikiLinkResolvesWithinNamespace(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	for _, p := range []Page{
		{Slug: "health/overview", Title: "Overview", Body: "hi"},
		{Slug: "health/plan", Title: "Plan", Body: "See [[Overview]] for context."},
	} {
		if _, err := app.Store.Save(pageFile(p.Slug), p.Encode(), "add", "t", "t@e"); err != nil {
			t.Fatalf("saving %s: %v", p.Slug, err)
		}
		if err := app.Index.Update(p); err != nil {
			t.Fatalf("indexing %s: %v", p.Slug, err)
		}
	}

	resp, err := client.Get(server.URL + "/health/plan")
	if err != nil {
		t.Fatalf("GET /health/plan: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	b, _ := io.ReadAll(resp.Body)
	body := string(b)

	if !strings.Contains(body, `href="/health/overview"`) {
		t.Fatalf("wiki-link should resolve to /health/overview, got: %s", body)
	}
	if strings.Contains(body, `class="missing`) {
		t.Errorf("wiki-link to an existing sibling page must not render as missing: %s", body)
	}
}

// TestRenameWithinNamespace ensures renaming a page preserves its namespace and rewrites links.
func TestRenameWithinNamespace(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	for _, p := range []Page{
		{Slug: "health/overview", Title: "Overview", Body: "hi"},
		{Slug: "health/plan", Title: "Plan", Body: "See [[Overview]] for context."},
	} {
		if _, err := app.Store.Save(pageFile(p.Slug), p.Encode(), "add", "t", "t@e"); err != nil {
			t.Fatalf("saving %s: %v", p.Slug, err)
		}
		if err := app.Index.Update(p); err != nil {
			t.Fatalf("indexing %s: %v", p.Slug, err)
		}
	}

	resp, err := client.PostForm(server.URL+"/health/overview?do=rename", url.Values{"title": {"Summary"}})
	if err != nil {
		t.Fatalf("POST ?do=rename: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("?do=rename: status = %d, body = %s", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), `"health/summary"`) {
		t.Errorf("rename should stay in the namespace, got: %s", b)
	}

	content, _, err := app.Store.Read(pageFile("health/plan"))
	if err != nil {
		t.Fatalf("reading health/plan: %v", err)
	}
	if body := ParsePage("health/plan", content).Body; !strings.Contains(body, "[[Summary]]") {
		t.Errorf("link in health/plan should have been rewritten, got: %s", body)
	}
}

func savePage(t *testing.T, app *App, slug, body string) {
	t.Helper()
	page := Page{Slug: slug, Title: slug, Body: body}
	if _, err := app.Store.Save(pageFile(slug), page.Encode(), "add", "t", "t@e"); err != nil {
		t.Fatalf("saving %s: %v", slug, err)
	}
	if err := app.Index.Update(page); err != nil {
		t.Fatalf("indexing %s: %v", slug, err)
	}
}
