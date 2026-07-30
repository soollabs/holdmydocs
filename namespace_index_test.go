package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

// TestNamespaceIndex covers browsing a namespace: /blog/ lists its pages, a
// bare /blog remains an independent root-page URL, and an anonymous visitor
// sees a namespace index only when the namespace is published.
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
	mustStatus(client, "/blog", http.StatusNotFound)
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

	// The sidebar has to link to the trailing-slash index or it is unreachable
	// by navigation.
	if _, home := body(client, "/readme"); !strings.Contains(home, `href="/blog/"`) {
		t.Errorf("sidebar has no link to /blog/, body: %s", home)
	}

	// A real root page remains independent from its namespace.
	savePage(t, app, "blog", "Root blog")
	if _, got := body(client, "/blog"); !strings.Contains(got, "Root blog") {
		t.Errorf("a real page must win over the namespace index, body: %s", got)
	}

	// Anonymous: unknown and private namespace indexes have byte-identical 404s.
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

	// Anonymous visitors can browse a published namespace index.
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
	for _, want := range []string{"0 pages", "No pages in this namespace yet", `href="/empty/new?do=edit"`} {
		if !strings.Contains(body, want) {
			t.Errorf("empty namespace missing %q, body: %s", want, body)
		}
	}

	if err := app.Auth.AddUser("reader", "secret"); err != nil {
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
	resp, err := reader.PostForm(server.URL+"/_/login", url.Values{"username": {"reader"}, "password": {"secret"}})
	if err != nil {
		t.Fatalf("reader login: %v", err)
	}
	closeTestBody(t, resp.Body)
	if got := get(reader); strings.Contains(got, `href="/empty/new?do=edit"`) {
		t.Fatal("read-scoped user can see namespace Create page link")
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
