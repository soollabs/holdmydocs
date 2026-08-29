package web

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeNamespaceConfig(t *testing.T, app *App, ns, yaml string) error {
	t.Helper()
	dir := app.config().RepoDir
	if ns != "" {
		dir = filepath.Join(dir, ns)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, namespaceConfigFile), []byte(yaml), 0644); err != nil {
		return err
	}
	reg, err := BuildNamespaceRegistry(app.config().RepoDir)
	if err != nil {
		return err
	}
	app.SetNamespaces(reg)
	return nil
}

func setNamespacePublic(t *testing.T, app *App, ns string, public bool) {
	t.Helper()
	if !public {
		return
	}
	if err := writeNamespaceConfig(t, app, ns, "public: true\n"); err != nil {
		t.Fatalf("writing .namespace.yaml: %v", err)
	}
}

func seedPage(t *testing.T, app *App, page Page) {
	t.Helper()
	authorName, authorEmail := app.gitAuthor("admin")
	if _, err := app.Store.Save(pageFile(page.Slug), page.Encode(), "seed "+page.Slug, authorName, authorEmail); err != nil {
		t.Fatalf("seeding %s: %v", page.Slug, err)
	}
	if err := app.Index.Update(page); err != nil {
		t.Fatalf("indexing %s: %v", page.Slug, err)
	}
}

func noAuthClient() *http.Client {
	return &http.Client{}
}

func TestAnonymousPublicNamespacePageServes200WithNoChrome(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	if err := writeNamespaceConfig(t, app, "blog", "public: true\ntitle: My Blog\n"); err != nil {
		t.Fatalf("writing .namespace.yaml: %v", err)
	}
	seedPage(t, app, Page{Slug: "blog/hello", Title: "Hello", Body: "Public **content**."})

	resp, err := noAuthClient().Get(server.URL + "/blog/hello")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	if !strings.Contains(s, "Public") || !strings.Contains(s, "<strong>content</strong>") {
		t.Errorf("body missing rendered page content: %s", s)
	}
	for _, needle := range []string{`<title>Hello — My Blog</title>`, `class="topbar-site">My Blog</a>`} {
		if !strings.Contains(s, needle) {
			t.Errorf("anonymous public page missing namespace title %q: %s", needle, s)
		}
	}
	// The sidebar tree is server-rendered with no auth-only API calls behind
	// it, so it's allowed for anonymous public viewers — everything else
	// (topbar, statusline, palette, app.js) stays authed-only.
	if !strings.Contains(s, `class="sidebar"`) {
		t.Errorf("anonymous public-namespace body should contain the sidebar tree: %s", s)
	}
	for _, needle := range []string{`id="statusline"`, `id="palette-backdrop"`, `id="sidebar-toggle"`, `app.js`} {
		if strings.Contains(s, needle) {
			t.Errorf("anonymous body should not contain %q", needle)
		}
	}
}

func TestAnonymousHeadMatchesPublicGetAccess(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	setNamespacePublic(t, app, "blog", true)
	seedPage(t, app, Page{Slug: "blog/hello", Title: "Hello", Body: "Public content."})

	req, err := http.NewRequest(http.MethodHead, server.URL+"/blog/hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := noAuthClient().Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD public page = %d, want 200", resp.StatusCode)
	}
}

func TestAnonymousPublicNamespaceSkinUsesDefaultPalette(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	if err := writeNamespaceConfig(t, app, "blog", "public: true\nskin: newsprint\n"); err != nil {
		t.Fatalf("writing .namespace.yaml: %v", err)
	}
	seedPage(t, app, Page{Slug: "blog/hello", Title: "Hello", Body: "Public content."})

	resp, err := noAuthClient().Get(server.URL + "/blog/hello")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{`data-skin="newsprint"`, `--bg:#002b36;`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("public newsprint page missing %q: %s", want, body)
		}
	}
}

func TestAnonymousPublicPageGetsOutlineRail(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	setNamespacePublic(t, app, "blog", true)
	seedPage(t, app, Page{Slug: "blog/hello", Title: "Hello", Body: "## One\ntext\n\n## Two\nmore text"})

	resp, err := noAuthClient().Get(server.URL + "/blog/hello")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	// toc.js builds the "on this page" list client-side from this page's own
	// (already public) headings — no auth-only endpoint involved — so it
	// should load for an anonymous viewer even though app.js does not.
	if !strings.Contains(s, `id="toc-rail"`) {
		t.Errorf("anonymous public page with headings should render the outline rail: %s", s)
	}
	if !strings.Contains(s, `toc.js`) {
		t.Errorf("anonymous public page with headings should load toc.js: %s", s)
	}
	if strings.Contains(s, `app.js`) {
		t.Errorf("anonymous body should not load app.js: %s", s)
	}
}

func TestAnonymousPrivateAndNonexistentPagesByteIdentical404(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	seedPage(t, app, Page{Slug: "blog/private", Title: "Private"})

	client := noAuthClient()

	respPrivate, err := client.Get(server.URL + "/blog/private")
	if err != nil {
		t.Fatalf("GET private: %v", err)
	}
	defer closeTestBody(t, respPrivate.Body)
	bodyPrivate, _ := io.ReadAll(respPrivate.Body)

	respMissing, err := client.Get(server.URL + "/blog/does-not-exist")
	if err != nil {
		t.Fatalf("GET missing: %v", err)
	}
	defer closeTestBody(t, respMissing.Body)
	bodyMissing, _ := io.ReadAll(respMissing.Body)

	if respPrivate.StatusCode != http.StatusNotFound || respMissing.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d / %d, want 404 / 404", respPrivate.StatusCode, respMissing.StatusCode)
	}
	if string(bodyPrivate) != string(bodyMissing) {
		t.Errorf("private and nonexistent bodies differ:\nprivate: %q\nmissing: %q", bodyPrivate, bodyMissing)
	}
}

func TestAnonymousWikiLinkToPrivatePageUnwraps(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	setNamespacePublic(t, app, "blog", true)
	seedPage(t, app, Page{Slug: "secret", Title: "Secret"})
	seedPage(t, app, Page{Slug: "blog/post", Title: "Post", Body: "See [[Secret]] and [[Nowhere]]."})

	resp, err := noAuthClient().Get(server.URL + "/blog/post")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	if strings.Contains(s, `href="/secret"`) {
		t.Errorf("wiki-link to a private page should not render as a link: %s", s)
	}
	if !strings.Contains(s, "Secret") {
		t.Errorf("private link title should still appear as plain text: %s", s)
	}
	if strings.Contains(s, `href="/nowhere"`) {
		t.Errorf("wiki-link to a nonexistent page should not render as a link: %s", s)
	}
}

func TestAnonymousTocNotExpanded(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	setNamespacePublic(t, app, "blog", true)
	seedPage(t, app, Page{Slug: "secret", Title: "Secret Title"})
	seedPage(t, app, Page{Slug: "blog/index", Title: "Index", Body: "<!-- hmd:toc -->"})

	resp, err := noAuthClient().Get(server.URL + "/blog/index")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	if strings.Contains(s, "Secret Title") {
		t.Errorf("hmd:toc must not expand for an anonymous viewer, leaked a private title: %s", s)
	}
}

func TestAnonymousAttachment(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	setNamespacePublic(t, app, "blog", true)
	seedPage(t, app, Page{Slug: "blog/post", Title: "Post"})
	seedPage(t, app, Page{Slug: "secret", Title: "Secret"})

	authorName, authorEmail := app.gitAuthor("admin")
	if _, err := app.Store.Save("attachments/blog/post/pic.png", []byte("fake-png"), "add attachment", authorName, authorEmail); err != nil {
		t.Fatalf("seeding attachment: %v", err)
	}
	if _, err := app.Store.Save("attachments/secret/pic.png", []byte("fake-png"), "add attachment", authorName, authorEmail); err != nil {
		t.Fatalf("seeding attachment: %v", err)
	}

	client := noAuthClient()

	resp, err := client.Get(server.URL + "/_/attachments/blog/post/pic.png")
	if err != nil {
		t.Fatalf("GET public attachment: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("public-namespace attachment status = %d, want 200", resp.StatusCode)
	}

	resp2, err := client.Get(server.URL + "/_/attachments/secret/pic.png")
	if err != nil {
		t.Fatalf("GET private attachment: %v", err)
	}
	defer closeTestBody(t, resp2.Body)
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("private-namespace attachment status = %d, want 404", resp2.StatusCode)
	}
}

func TestAnonymousDoActionsStayBehindAuth(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	setNamespacePublic(t, app, "blog", true)
	seedPage(t, app, Page{Slug: "blog/post", Title: "Post"})

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(server.URL + "/blog/post?do=edit")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("?do=edit on a public page, anonymous, status = %d, want 303 (login redirect)", resp.StatusCode)
	}
}
