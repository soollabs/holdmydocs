package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// newGardenApp builds a garden-enabled app with a fixed fixture: two public
// pages (plant, bloom), one private page (secret), and one attachment each
// for plant and secret.
func newGardenApp(t *testing.T) *httptest.Server {
	t.Helper()

	cfg := Config{
		RepoDir:       t.TempDir(),
		AppDir:        t.TempDir(),
		GitUser:       "test",
		AdminUser:     "admin",
		AdminPass:     "test",
		HomeFilename:  "readme.md",
		SiteName:      "hmd",
		GardenEnabled: true,
		GardenTitle:   "My Garden",
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	store.NeedsSetup.Store(false)

	fixtures := []Page{
		{Slug: "plant", Title: "Plant", Tags: []string{"green"}, Public: true,
			Body: "Growing well.\n\nSee [[Bloom]] and [[Secret]].\n\n![leaf](/attachments/plant/leaf.png)"},
		{Slug: "bloom", Title: "Bloom", Public: true, Body: "Flowering."},
		{Slug: "secret", Title: "Secret", Body: "Private notes."},
	}
	for _, p := range fixtures {
		if _, err := store.Save(pageFile(p.Slug), p.Encode(), "Add "+p.Slug, "test", "test@hmd.local"); err != nil {
			t.Fatalf("seeding %s: %v", p.Slug, err)
		}
	}
	for _, path := range []string{"attachments/plant/leaf.png", "attachments/secret/hidden.png"} {
		if _, err := store.Save(path, []byte("png-bytes"), "Add attachment", "test", "test@hmd.local"); err != nil {
			t.Fatalf("seeding %s: %v", path, err)
		}
	}

	index, err := BuildIndex(fixtures)
	if err != nil {
		t.Fatalf("BuildIndex failed: %v", err)
	}
	auth, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates failed: %v", err)
	}

	app := &App{Store: store, Auth: auth, Index: index, Render: NewRenderer(index.Exists), Tmpl: tmpl}
	app.SetConfig(cfg)

	server := httptest.NewServer(auth.Middleware(app.Routes()))
	t.Cleanup(server.Close)
	return server
}

// anonGet fetches url with no session cookie, without following redirects.
func anonGet(t *testing.T, url string) (int, string, http.Header) {
	t.Helper()
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

func TestGardenPrivate404Identical(t *testing.T) {
	server := newGardenApp(t)

	codePrivate, bodyPrivate, _ := anonGet(t, server.URL+"/garden/secret")
	codeMissing, bodyMissing, _ := anonGet(t, server.URL+"/garden/never-existed")

	if codePrivate != http.StatusNotFound || codeMissing != http.StatusNotFound {
		t.Fatalf("status = %d/%d, want 404/404", codePrivate, codeMissing)
	}
	if bodyPrivate != bodyMissing {
		t.Errorf("private and missing 404 bodies differ: %q vs %q", bodyPrivate, bodyMissing)
	}
}

func TestGardenPublicRendersUnauthenticated(t *testing.T) {
	server := newGardenApp(t)

	code, body, _ := anonGet(t, server.URL+"/garden/plant")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !strings.Contains(body, "Growing well") {
		t.Errorf("body should contain page content")
	}
	if !strings.Contains(body, "Plant") || !strings.Contains(body, "green") {
		t.Errorf("body should contain title and tags")
	}

	// Same slug behind the private route still redirects to login.
	code, _, headers := anonGet(t, server.URL+"/page/plant")
	if code != http.StatusSeeOther || headers.Get("Location") != "/login" {
		t.Errorf("GET /page/plant = %d -> %q, want 303 -> /login", code, headers.Get("Location"))
	}
}

func TestGardenWikiLinkRewriting(t *testing.T) {
	server := newGardenApp(t)

	_, body, _ := anonGet(t, server.URL+"/garden/plant")

	if !strings.Contains(body, `href="/garden/bloom"`) {
		t.Errorf("link to public page should be rewritten to /garden/: %s", body)
	}
	if strings.Contains(body, `href="/page/`) {
		t.Errorf("no /page/ links may leak into the garden: %s", body)
	}
	if strings.Contains(body, "/garden/secret") {
		t.Errorf("private page must not be linked: %s", body)
	}
	if !strings.Contains(body, "Secret") {
		t.Errorf("private wiki-link should unwrap to plain text: %s", body)
	}
	if !strings.Contains(body, `"/garden/attachments/plant/leaf.png"`) {
		t.Errorf("attachment URL should be rewritten to the garden route: %s", body)
	}
}

func TestGardenAttachments(t *testing.T) {
	server := newGardenApp(t)

	code, body, _ := anonGet(t, server.URL+"/garden/attachments/plant/leaf.png")
	if code != http.StatusOK || body != "png-bytes" {
		t.Errorf("public page attachment = %d %q, want 200 png-bytes", code, body)
	}

	code, _, _ = anonGet(t, server.URL+"/garden/attachments/secret/hidden.png")
	if code != http.StatusNotFound {
		t.Errorf("private page attachment = %d, want 404", code)
	}
}

func TestGardenFeed(t *testing.T) {
	server := newGardenApp(t)

	code, body, headers := anonGet(t, server.URL+"/garden/feed.xml")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if ct := headers.Get("Content-Type"); !strings.Contains(ct, "rss") {
		t.Errorf("Content-Type = %q, want rss", ct)
	}
	if !strings.Contains(body, "<title>Plant</title>") || !strings.Contains(body, "<title>Bloom</title>") {
		t.Errorf("feed should list public pages: %s", body)
	}
	// The word "Secret" may appear in a public page's own body text (the
	// author published it) — but the private page itself must not be an item.
	if strings.Contains(body, "<title>Secret</title>") || strings.Contains(body, "Private notes") {
		t.Errorf("feed must not list private pages: %s", body)
	}
	if strings.Contains(body, "[[") {
		t.Errorf("feed snippets should not contain wiki-link markup: %s", body)
	}
}

func TestGardenDisabledRedirectsToLogin(t *testing.T) {
	// Default config: garden disabled — the whole namespace stays behind auth.
	server, _ := newTestApp(t)
	defer server.Close()

	for _, path := range []string{"/garden", "/garden/readme", "/garden/feed.xml", "/garden/attachments/x/y.png"} {
		code, _, headers := anonGet(t, server.URL+path)
		if code != http.StatusSeeOther || headers.Get("Location") != "/login" {
			t.Errorf("GET %s = %d -> %q, want 303 -> /login", path, code, headers.Get("Location"))
		}
	}
}

// The editor's public checkbox round-trips: save with public=on, the edit
// form comes back checked; save without, it comes back unchecked.
func TestGardenPublicCheckboxRoundTrip(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	getEdit := func() string {
		resp, err := client.Get(server.URL + "/page/plant/edit")
		if err != nil {
			t.Fatalf("GET edit: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	basehashRe := regexp.MustCompile(`name="basehash" value="([0-9a-f]*)"`)
	save := func(form url.Values) {
		form.Set("basehash", basehashRe.FindStringSubmatch(getEdit())[1])
		resp, err := client.PostForm(server.URL+"/page/plant/save", form)
		if err != nil {
			t.Fatalf("POST save: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("save status = %d, want 303", resp.StatusCode)
		}
	}

	save(url.Values{"title": {"Plant"}, "body": {"Growing."}, "public": {"on"}})
	if !strings.Contains(getEdit(), `name="public" checked`) {
		t.Errorf("public checkbox should be checked after saving with public=on")
	}

	// Save again without the field — standard checkbox semantics, unpublishes.
	save(url.Values{"title": {"Plant"}, "body": {"Growing."}})
	if strings.Contains(getEdit(), `name="public" checked`) {
		t.Errorf("public checkbox should be unchecked after saving without public")
	}
}
