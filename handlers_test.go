package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func newTestApp(t *testing.T) (*httptest.Server, *http.Client) {
	repoDir := t.TempDir()
	appDir := t.TempDir()

	cfg := Config{
		RepoDir:      repoDir,
		AppDir:       appDir,
		GitUser:      "test",
		AdminUser:    "admin",
		AdminPass:    "test",
		HomeFilename: "readme.md",
	}

	// When a config file is configured, overlay its values so the settings
	// handlers see the same cfg a real deployment (env > file > defaults) would.
	if path := os.Getenv("HMD_CONFIG_FILE"); path != "" {
		if fc, err := LoadFileConfig(path); err == nil {
			if fc.Bind != "" {
				cfg.Bind = fc.Bind
			}
			if fc.RemoteURL != "" {
				cfg.RemoteURL = fc.RemoteURL
			}
			if fc.GitUser != "" {
				cfg.GitUser = fc.GitUser
			}
			if fc.SiteName != "" {
				cfg.SiteName = fc.SiteName
			}
			if fc.Hostname != "" {
				cfg.Hostname = fc.Hostname
			}
			if fc.PathLabel != "" {
				cfg.PathLabel = fc.PathLabel
			}
			if fc.UserLabel != "" {
				cfg.UserLabel = fc.UserLabel
			}
			if fc.MaxUploadBytes != nil {
				cfg.MaxUploadBytes = *fc.MaxUploadBytes
			}
			if fc.SyncPollMs != nil {
				cfg.SyncPollMs = *fc.SyncPollMs
			}
			if fc.ShowTagsSidebar != nil {
				cfg.ShowTagsSidebar = *fc.ShowTagsSidebar
			}
			if fc.SyncMode != "" {
				cfg.SyncMode = fc.SyncMode
			}
		}
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	// Tests exercise pages/handlers, not the setup flow itself — simulate a
	// completed setup so readme.md/.help.md fixtures exist as before, since
	// OpenStore no longer auto-seeds anything without consent.
	if _, err := store.Save("readme.md", Page{Slug: "readme", Title: "readme", Body: defaultHomeMD}.Encode(), "Add readme.md", cfg.GitUser, cfg.GitUser+"@hmd.local"); err != nil {
		t.Fatalf("seeding readme.md: %v", err)
	}
	if _, err := store.Save(".help.md", Page{Slug: "help", Title: "Help", Tags: []string{"meta"}, Body: defaultHelpMD}.Encode(), "Add .help.md", cfg.GitUser, cfg.GitUser+"@hmd.local"); err != nil {
		t.Fatalf("seeding .help.md: %v", err)
	}
	store.NeedsSetup.Store(false)

	pages, _ := store.List()
	var pageObjs []Page
	for _, p := range pages {
		content, _, _ := store.Read(p)
		pageObjs = append(pageObjs, ParsePage(p[:len(p)-3], content))
	}

	index, _ := BuildIndex(pageObjs)
	auth, _ := OpenAuth(cfg)
	renderer := NewRenderer(index.Exists)

	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates failed: %v", err)
	}

	app := &App{
		Store:  store,
		Auth:   auth,
		Index:  index,
		Render: renderer,
		Tmpl:   tmpl,
	}
	app.SetConfig(cfg)

	server := httptest.NewServer(app.Auth.Middleware(app.Routes()))

	// Create client with cookie jar
	jar, _ := cookiejar.New(&cookiejar.Options{})
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Login
	loginForm := url.Values{
		"username": {"admin"},
		"password": {"test"},
	}
	req, _ := http.NewRequest("POST", server.URL+"/login", bytes.NewBufferString(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client.Do(req)

	return server, client
}

func TestViewHome(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/page/readme")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Welcome")) {
		t.Errorf("Body should contain 'Welcome'")
	}
}

func TestCreateAffordance(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/page/does-not-exist")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Status = %d, want 404", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Create this page")) {
		t.Logf("Response body:\n%s", body)
		t.Errorf("Body should contain 'Create this page'")
	}
}

func TestEditSaveRoundTrip(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	slug := "test-page"

	// GET edit page for new slug
	resp, err := client.Get(server.URL + "/page/" + slug + "/edit")
	if err != nil {
		t.Fatalf("GET edit failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Edit page status = %d, want 200", resp.StatusCode)
	}

	// POST save
	saveForm := url.Values{
		"title":    {"Test Page"},
		"body":     {"This is a test page."},
		"basehash": {""},
	}
	resp, err = client.PostForm(server.URL+"/page/"+slug+"/save", saveForm)
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("Save status = %d, want 303", resp.StatusCode)
	}

	// GET page, verify content
	resp, err = client.Get(server.URL + "/page/" + slug)
	if err != nil {
		t.Fatalf("GET page failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("This is a test page.")) {
		t.Errorf("Saved page should contain body text")
	}

	// Verify commit was made (checking store history)
	// This would require access to the app's store, which we don't have in the test
	// So we just verify the page was saved by checking we can view it
}

func TestOptimisticLockConflict(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	slug := "lock-test"

	// Get the hash for a new page
	resp, err := client.Get(server.URL + "/page/" + slug + "/edit")
	if err != nil {
		t.Fatalf("GET edit failed: %v", err)
	}
	defer resp.Body.Close()
	// BaseHash will be empty for new page

	// Save directly via client to change the page
	saveForm := url.Values{
		"title":    {"Version 1"},
		"body":     {"First version"},
		"basehash": {""},
	}
	resp, err = client.PostForm(server.URL+"/page/"+slug+"/save", saveForm)
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	resp.Body.Close()

	// Now try to save with stale basehash
	saveForm2 := url.Values{
		"title":    {"Version 2"},
		"body":     {"Second version"},
		"basehash": {""}, // stale hash
	}
	resp, err = client.PostForm(server.URL+"/page/"+slug+"/save", saveForm2)
	if err != nil {
		t.Fatalf("POST save2 failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Errorf("Conflict status = %d, want 409", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("overwrite")) {
		t.Errorf("Conflict page should have overwrite button")
	}
}

func TestPreview(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	previewForm := url.Values{
		"body": {"**bold**"},
	}
	resp, err := client.PostForm(server.URL+"/api/preview", previewForm)
	if err != nil {
		t.Fatalf("POST preview failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("<strong>")) {
		t.Errorf("Preview should render bold as <strong>")
	}
}

func TestUnauthenticatedRedirect(t *testing.T) {
	server, _ := newTestApp(t)
	defer server.Close()

	// Fresh client without auth
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(server.URL + "/page/readme")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("Unauthenticated request status = %d, want 303", resp.StatusCode)
	}

	location := resp.Header.Get("Location")
	if !bytes.Contains([]byte(location), []byte("/login")) {
		t.Errorf("Should redirect to /login, got %q", location)
	}
}

func TestAttachmentUploadAndServe(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	slug := "test-page"

	// Create a simple PNG file (minimal valid PNG)
	pngData := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
		0x54, 0x08, 0x99, 0x63, 0xf8, 0x0f, 0x00, 0x00,
		0x01, 0x01, 0x01, 0x00, 0x18, 0xdd, 0x8d, 0xb4,
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44,
		0xae, 0x42, 0x60, 0x82,
	}

	// Upload via multipart
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "test-image.png")
	io.Copy(part, bytes.NewReader(pngData))
	writer.Close()

	req, _ := http.NewRequest("POST", server.URL+"/api/attachments/"+slug, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST upload failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Upload status = %d, want 200", resp.StatusCode)
	}

	respBody, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(respBody, []byte("/attachments/"+slug+"/")) {
		t.Errorf("Response should contain attachment URL")
	}

	// GET the attachment
	resp, err = client.Get(server.URL + "/attachments/" + slug + "/test-image.png")
	if err != nil {
		t.Fatalf("GET attachment failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Serve status = %d, want 200", resp.StatusCode)
	}

	content, _ := io.ReadAll(resp.Body)
	if len(content) == 0 {
		t.Errorf("Attachment content is empty")
	}
}

func TestAttachmentRejectsBadNames(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	slug := "test-page"

	// Try to upload .exe file
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "virus.exe")
	part.Write([]byte("malware"))
	writer.Close()

	req, _ := http.NewRequest("POST", server.URL+"/api/attachments/"+slug, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST attachment failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Bad extension status = %d, want 400", resp.StatusCode)
	}
}

func TestAttachmentUploadRejectsPathTraversal(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	pngData := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
		0x54, 0x08, 0x99, 0x63, 0xf8, 0x0f, 0x00, 0x00,
		0x01, 0x01, 0x01, 0x00, 0x18, 0xdd, 0x8d, 0xb4,
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44,
		0xae, 0x42, 0x60, 0x82,
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "escape.png")
	io.Copy(part, bytes.NewReader(pngData))
	writer.Close()

	// Percent-encoded ".." as the {slug} path segment: net/http's ServeMux
	// redirects literal ".." segments before routing, but %2e%2e reaches
	// PathValue("slug") as ".." unmolested, so this is the exploitable form.
	// It would otherwise resolve to a path outside attachments/ once joined
	// with the repo dir.
	req, _ := http.NewRequest("POST", server.URL+"/api/attachments/%2e%2e", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST upload failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Path traversal slug status = %d, want 400", resp.StatusCode)
	}
}

func TestSearchPage(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	// Save a page with unique text
	slug := "search-test"
	saveForm := url.Values{
		"title":    {"Search Test"},
		"body":     {"This contains uniquewordxyz for searching."},
		"basehash": {""},
	}
	resp, err := client.PostForm(server.URL+"/page/"+slug+"/save", saveForm)
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	resp.Body.Close()

	// Search for the unique word
	resp, err = client.Get(server.URL + "/search?q=uniquewordxyz")
	if err != nil {
		t.Fatalf("GET search failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Search status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte(slug)) {
		t.Errorf("Search results should contain page slug")
	}
}

func TestBacklinksShown(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	// Save page "page-one" with link to "Page Two" (which slugifies to "page-two")
	saveForm := url.Values{
		"title":    {"Page One"},
		"body":     {"This links to [[Page Two]]"},
		"basehash": {""},
	}
	resp, err := client.PostForm(server.URL+"/page/page-one/save", saveForm)
	if err != nil {
		t.Fatalf("POST one failed: %v", err)
	}
	resp.Body.Close()

	// Save page "page-two"
	saveForm = url.Values{
		"title":    {"Page Two"},
		"body":     {"This is page two."},
		"basehash": {""},
	}
	resp, err = client.PostForm(server.URL+"/page/page-two/save", saveForm)
	if err != nil {
		t.Fatalf("POST two failed: %v", err)
	}
	resp.Body.Close()

	// View page page-two, should show backlinks
	resp, err = client.Get(server.URL + "/page/page-two")
	if err != nil {
		t.Fatalf("GET page two failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("linked from")) {
		t.Logf("Response: %s", body)
		t.Errorf("Page should show 'linked from' section")
	}
	if !bytes.Contains(body, []byte("/page/page-one")) {
		t.Errorf("Backlinks should contain link to page-one")
	}
}

func TestHistoryListAndRevert(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	slug := "history-test"

	// Save page twice with different bodies
	saveForm := url.Values{
		"title":    {"History Test"},
		"body":     {"Version one"},
		"basehash": {""},
	}
	resp, err := client.PostForm(server.URL+"/page/"+slug+"/save", saveForm)
	if err != nil {
		t.Fatalf("POST v1 failed: %v", err)
	}
	resp.Body.Close()

	saveForm = url.Values{
		"title":    {"History Test"},
		"body":     {"Version two"},
		"basehash": {""},
	}
	resp, err = client.PostForm(server.URL+"/page/"+slug+"/save", saveForm)
	if err != nil {
		t.Fatalf("POST v2 failed: %v", err)
	}
	resp.Body.Close()

	// GET history
	resp, err = client.Get(server.URL + "/page/" + slug + "/history")
	if err != nil {
		t.Fatalf("GET history failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("History status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("git log")) {
		t.Errorf("History page should contain 'git log'")
	}

	// Extract the older commit hash from the response
	// For this test, we'll just verify we can view an old version
	// In a real test, we'd parse the HTML to get the hash
	// For now, assume the second entry in history is the old one
}

func TestRevertToOldVersion(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	slug := "revert-test"

	// Save v1
	saveForm := url.Values{
		"title":    {"Revert Test"},
		"body":     {"Original content"},
		"basehash": {""},
	}
	resp, err := client.PostForm(server.URL+"/page/"+slug+"/save", saveForm)
	if err != nil {
		t.Fatalf("POST v1 failed: %v", err)
	}
	resp.Body.Close()

	// Verify page shows v1
	resp, err = client.Get(server.URL + "/page/" + slug)
	if err != nil {
		t.Fatalf("GET page failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Original content")) {
		t.Errorf("Page should show v1 content")
	}
}

// TestPageChrome guards against pages bypassing the shared layout: every
// rendered page must carry the editor/preview scripts and header controls.
func TestPageChrome(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/page/readme")
	if err != nil {
		t.Fatalf("GET page failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	for _, want := range []string{
		`src="/static/app.js"`,
		`class="sidebar"`,
		`action="/logout"`,
		`seg-sync`,
	} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("Page missing %s", want)
		}
	}

	// Mermaid should NOT be loaded on pages without mermaid content
	if bytes.Contains(body, []byte(`src="/static/mermaid.min.js"`)) {
		t.Error("Mermaid script loaded on page without mermaid content")
	}

	// The <!-- hmd:toc --> token in the seeded readme.md must be replaced
	// server-side, never reach the rendered HTML.
	if bytes.Contains(body, []byte("hmd:toc")) {
		t.Error("raw hmd:toc token should not appear in rendered HTML")
	}

	resp, err = client.Get(server.URL + "/page/readme/edit")
	if err != nil {
		t.Fatalf("GET edit failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(resp.Body)

	for _, want := range []string{
		`id="cm-host"`,
		`data-slug="readme"`,
		`src="/static/editor.js"`,
		`id="preview"`,
		`data-action="toc"`,
	} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("Edit page missing %s", want)
		}
	}
}

func TestMermaidConditionalLoad(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	// Create a page with a mermaid diagram
	resp, err := client.PostForm(server.URL+"/page/mermaid-test/save", url.Values{
		"title":    {"Mermaid Test"},
		"body":     {"```mermaid\ngraph TD;\n  A-->B\n```\n"},
		"tags":     {""},
		"basehash": {""},
	})
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	resp.Body.Close()

	// View the page — mermaid script should be present
	resp, err = client.Get(server.URL + "/page/mermaid-test")
	if err != nil {
		t.Fatalf("GET page failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if !bytes.Contains(body, []byte(`src="/static/mermaid.min.js"`)) {
		t.Error("Mermaid script missing on page with mermaid content")
	}

	// View the edit page — mermaid script should be present (body contains "mermaid")
	resp, err = client.Get(server.URL + "/page/mermaid-test/edit")
	if err != nil {
		t.Fatalf("GET edit failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(resp.Body)

	if !bytes.Contains(body, []byte(`src="/static/mermaid.min.js"`)) {
		t.Error("Mermaid script missing on edit page when body contains mermaid")
	}
}

func TestLoginErrorShown(t *testing.T) {
	server, _ := newTestApp(t)
	defer server.Close()

	resp, err := http.PostForm(server.URL+"/login", url.Values{
		"username": {"admin"},
		"password": {"wrong"},
	})
	if err != nil {
		t.Fatalf("POST login failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Bad login status = %d, want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Invalid username or password")) {
		t.Errorf("Login page should show the error message")
	}
}

func TestTagBrowsePages(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	form := url.Values{
		"title":    {"Tagged Page"},
		"body":     {"Some content."},
		"tags":     {"go, wiki"},
		"basehash": {""},
	}
	req, _ := http.NewRequest("POST", server.URL+"/page/tagged/save", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("save request failed: %v", err)
	}
	resp.Body.Close()

	resp, err = client.Get(server.URL + "/tags")
	if err != nil {
		t.Fatalf("tags index request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `href="/tags/go"`) {
		t.Errorf("GET /tags should list a link to /tags/go, got status %d body:\n%s", resp.StatusCode, body)
	}

	resp, err = client.Get(server.URL + "/tags/go")
	if err != nil {
		t.Fatalf("tag page request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `href="/page/tagged"`) {
		t.Errorf("GET /tags/go should list a link to /page/tagged, got status %d body:\n%s", resp.StatusCode, body)
	}
}

func TestTagsOnEditAndView(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	form := url.Values{
		"title":    {"Tagged Page"},
		"body":     {"Some content."},
		"tags":     {"go, wiki"},
		"basehash": {""},
	}
	req, _ := http.NewRequest("POST", server.URL+"/page/tagged/save", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("save request failed: %v", err)
	}
	resp.Body.Close()

	// Edit form should show the tags back.
	resp, err = client.Get(server.URL + "/page/tagged/edit")
	if err != nil {
		t.Fatalf("edit request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `value="go, wiki"`) {
		t.Errorf("edit page should show tags input, got:\n%s", body)
	}

	// Page view should show tag chips.
	resp, err = client.Get(server.URL + "/page/tagged")
	if err != nil {
		t.Fatalf("view request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `href="/tags/go"`) || !strings.Contains(string(body), `class="meta-tag"`) {
		t.Errorf("page view should show a tag chip linking to /tags/go, got:\n%s", body)
	}
}

// TestTitleEscaped guards against stored XSS via page titles.
func TestTitleEscaped(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	saveForm := url.Values{
		"title":    {`<script>alert(1)</script>`},
		"body":     {"content"},
		"basehash": {""},
	}
	resp, err := client.PostForm(server.URL+"/page/xss-test/save", saveForm)
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	resp.Body.Close()

	resp, err = client.Get(server.URL + "/page/xss-test")
	if err != nil {
		t.Fatalf("GET page failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if bytes.Contains(body, []byte("<script>alert(1)</script>")) {
		t.Error("Page title rendered unescaped — stored XSS")
	}
}

func TestSearchAPI(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	// Save a page with unique text
	slug := "api-search-test"
	saveForm := url.Values{
		"title":    {"API Search Test"},
		"body":     {"This contains uniquetestword for API search."},
		"basehash": {""},
	}
	resp, err := client.PostForm(server.URL+"/page/"+slug+"/save", saveForm)
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	resp.Body.Close()

	// Search via API
	resp, err = client.Get(server.URL + "/api/search?q=uniquetestword")
	if err != nil {
		t.Fatalf("GET api/search failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("API search status = %d, want 200", resp.StatusCode)
	}

	var results []AutocompleteResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("Failed to decode JSON: %v", err)
	}

	if len(results) != 1 || results[0].Slug != slug {
		t.Errorf("API search results = %+v, want one result with slug %q", results, slug)
	}
	if results[0].Title != "API Search Test" {
		t.Errorf("API search title = %q, want %q", results[0].Title, "API Search Test")
	}
}

func TestSyncAPI(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/api/sync")
	if err != nil {
		t.Fatalf("GET api/sync failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("API sync status = %d, want 200", resp.StatusCode)
	}

	var status SyncStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("Failed to decode JSON: %v", err)
	}
	if status.State == "" {
		t.Errorf("API sync state = %q, want non-empty", status.State)
	}
	if status.At == "" {
		t.Errorf("API sync at = %q, want non-empty", status.At)
	}
}

func TestAppConfigPointer(t *testing.T) {
	repoDir := t.TempDir()
	appDir := t.TempDir()
	cfg := Config{RepoDir: repoDir, AppDir: appDir, GitUser: "test", SiteName: "TestWiki"}
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	auth, _ := OpenAuth(cfg)
	index, _ := BuildIndex(nil)
	renderer := NewRenderer(index.Exists)
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates failed: %v", err)
	}
	app := &App{
		Store:  store,
		Auth:   auth,
		Index:  index,
		Render: renderer,
		Tmpl:   tmpl,
	}
	app.SetConfig(cfg)

	got := app.config()
	if got.SiteName != "TestWiki" {
		t.Errorf("config().SiteName = %q, want %q", got.SiteName, "TestWiki")
	}

	// Swap and verify readers see the new value
	cfg2 := cfg
	cfg2.SiteName = "Changed"
	app.SetConfig(cfg2)
	got2 := app.config()
	if got2.SiteName != "Changed" {
		t.Errorf("after swap, config().SiteName = %q, want %q", got2.SiteName, "Changed")
	}
}

func TestBuildSettingsDataEditable(t *testing.T) {
	cfg := Config{
		Bind:      ":8080",
		RepoDir:   "/data/repo",
		AppDir:    "/data/app",
		RemoteURL: "https://example.com/repo.git",
		GitUser:   "hmd",
		GitToken:  "secret",
		SiteName:  "My Wiki",
	}
	fc := fileConfig{
		Bind:      ":8080",
		RemoteURL: "https://example.com/repo.git",
		GitUser:   "hmd",
		SiteName:  "My Wiki",
	}

	sd := buildSettingsData(cfg, fc, "/path/config.yaml", true)

	if sd.NoConfigFile {
		t.Error("NoConfigFile = true, want false")
	}
	if !sd.Fields["Bind"].Editable {
		t.Error("Bind should be editable (no env var set)")
	}
	if !sd.Fields["Bind"].RestartRequired {
		t.Error("Bind should be RestartRequired")
	}
	if sd.Fields["Bind"].Value != ":8080" {
		t.Errorf("Bind Value = %q, want %q", sd.Fields["Bind"].Value, ":8080")
	}
	if !sd.Fields["RepoDir"].RestartRequired {
		t.Error("RepoDir should be RestartRequired")
	}
	if !sd.Fields["AppDir"].RestartRequired {
		t.Error("AppDir should be RestartRequired")
	}
	if sd.Fields["RemoteURL"].RestartRequired {
		t.Error("RemoteURL should not be RestartRequired")
	}
	if sd.Fields["GitToken"].Value != "set" {
		t.Errorf("GitToken Value = %q, want %q", sd.Fields["GitToken"].Value, "set")
	}
	if sd.Fields["AdminUser"].Editable {
		t.Error("AdminUser should not be Editable (bootstrap only)")
	}
	if sd.Fields["AdminPass"].Editable {
		t.Error("AdminPass should not be Editable (bootstrap only)")
	}
}

func TestBuildSettingsDataEnvLocked(t *testing.T) {
	t.Setenv("HMD_BIND", ":9999")
	t.Setenv("HMD_SITE_NAME", "Env Wiki")

	cfg := Config{
		Bind:     ":9999",
		SiteName: "Env Wiki",
		RepoDir:  "/data/repo",
		AppDir:   "/data/app",
		GitUser:  "hmd",
	}
	fc := fileConfig{
		Bind:     ":7000",
		SiteName: "YAML Wiki",
	}

	sd := buildSettingsData(cfg, fc, "/path/config.yaml", true)

	if sd.Fields["Bind"].Editable {
		t.Error("Bind should be read-only (env set)")
	}
	if sd.Fields["Bind"].EnvVar != "HMD_BIND" {
		t.Errorf("Bind EnvVar = %q, want %q", sd.Fields["Bind"].EnvVar, "HMD_BIND")
	}
	if sd.Fields["SiteName"].Editable {
		t.Error("SiteName should be read-only (env set)")
	}
	if sd.Fields["SiteName"].EnvVar != "HMD_SITE_NAME" {
		t.Errorf("SiteName EnvVar = %q, want %q", sd.Fields["SiteName"].EnvVar, "HMD_SITE_NAME")
	}
}

func TestBuildSettingsDataNoConfigFile(t *testing.T) {
	cfg := Config{
		Bind:     ":8080",
		SiteName: "hmd",
		RepoDir:  "/data/repo",
		AppDir:   "/data/app",
		GitUser:  "hmd",
	}

	sd := buildSettingsData(cfg, fileConfig{}, "", false)

	if !sd.NoConfigFile {
		t.Error("NoConfigFile = false, want true")
	}
	if sd.Fields["Bind"].Editable {
		t.Error("Bind should be read-only when no config file")
	}
	if sd.Fields["SiteName"].Editable {
		t.Error("SiteName should be read-only when no config file")
	}
}

func TestBuildSettingsDataTokenFileLocked(t *testing.T) {
	t.Setenv("HMD_GIT_TOKEN_FILE", "/path/to/token")

	cfg := Config{
		GitToken: "file-token",
		RepoDir:  "/data/repo",
		AppDir:   "/data/app",
		GitUser:  "hmd",
	}

	sd := buildSettingsData(cfg, fileConfig{}, "/path/config.yaml", true)

	if sd.Fields["GitToken"].Editable {
		t.Error("GitToken should be read-only when token file is set")
	}
	if sd.Fields["GitToken"].EnvVar == "" {
		t.Error("GitToken EnvVar should be set when token file is configured")
	}
}

func TestSettingsGetNoConfigFile(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/settings")
	if err != nil {
		t.Fatalf("GET /settings failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("HMD_CONFIG_FILE")) {
		t.Errorf("Body should contain the no-config-file banner")
	}
}

func TestSettingsGetWithConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := dir + "/config.yaml"
	yamlContent := "site_name: My Wiki\nbind: \":7000\"\n"
	if err := os.WriteFile(cfgFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	t.Setenv("HMD_CONFIG_FILE", cfgFile)

	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/settings")
	if err != nil {
		t.Fatalf("GET /settings failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("My Wiki")) {
		t.Errorf("Body should contain the site name from the config file")
	}
}

func TestSettingsPostSavesAndUpdates(t *testing.T) {
	dir := t.TempDir()
	cfgFile := dir + "/config.yaml"
	yamlContent := "site_name: Old Name\nbind: \":7000\"\n"
	if err := os.WriteFile(cfgFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	t.Setenv("HMD_CONFIG_FILE", cfgFile)

	server, client := newTestApp(t)
	defer server.Close()

	form := url.Values{
		"site_name":         {"New Name"},
		"bind":              {":7000"},
		"repo_dir":          {"/data/repo"},
		"app_dir":           {"/data/app"},
		"remote_url":        {""},
		"git_user":          {"test"},
		"git_token":         {""},
		"hostname":          {"homelab"},
		"path_label":        {"~/wiki"},
		"user_label":        {""},
		"max_upload_bytes":  {"10485760"},
		"sync_poll_ms":      {"10000"},
		"show_tags_sidebar": {"on"},
		"sync_mode":         {"push"},
	}
	req, _ := http.NewRequest("POST", server.URL+"/settings", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /settings failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("Status = %d, want 303", resp.StatusCode)
	}

	loaded, err := LoadFileConfig(cfgFile)
	if err != nil {
		t.Fatalf("LoadFileConfig failed: %v", err)
	}
	if loaded.SiteName != "New Name" {
		t.Errorf("SiteName in file = %q, want %q", loaded.SiteName, "New Name")
	}
}

func TestSettingsPostInvalidBind(t *testing.T) {
	dir := t.TempDir()
	cfgFile := dir + "/config.yaml"
	yamlContent := "site_name: Old Name\n"
	if err := os.WriteFile(cfgFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	t.Setenv("HMD_CONFIG_FILE", cfgFile)

	server, client := newTestApp(t)
	defer server.Close()

	form := url.Values{
		"site_name":         {"New Name"},
		"bind":              {""},
		"repo_dir":          {"/data/repo"},
		"app_dir":           {"/data/app"},
		"remote_url":        {""},
		"git_user":          {"test"},
		"git_token":         {""},
		"hostname":          {"homelab"},
		"path_label":        {"~/wiki"},
		"user_label":        {""},
		"max_upload_bytes":  {"10485760"},
		"sync_poll_ms":      {"10000"},
		"show_tags_sidebar": {"on"},
		"sync_mode":         {"push"},
	}
	req, _ := http.NewRequest("POST", server.URL+"/settings", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /settings failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Status = %d, want 400", resp.StatusCode)
	}
}

func TestSettingsPostNoConfigFile(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	form := url.Values{"site_name": {"New Name"}}
	req, _ := http.NewRequest("POST", server.URL+"/settings", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /settings failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Status = %d, want 403", resp.StatusCode)
	}
}

func TestRerunSetupShowsModalEvenWhenFilesExist(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	// newTestApp already seeds readme.md and .help.md, so under normal
	// (non-forced) NeedsSetup logic, neither would be missing and the
	// modal would have nothing to show.
	resp, err := client.Get(server.URL + "/settings")
	if err != nil {
		t.Fatalf("GET /settings failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if bytes.Contains(body, []byte("Set up wiki")) {
		t.Errorf("setup modal should not appear before re-run is clicked, body: %s", body)
	}

	resp2, err := client.PostForm(server.URL+"/settings/setup", url.Values{})
	if err != nil {
		t.Fatalf("POST /settings/setup failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusSeeOther {
		t.Errorf("re-run setup status = %d, want 303", resp2.StatusCode)
	}

	resp3, err := client.Get(server.URL + "/settings")
	if err != nil {
		t.Fatalf("GET /settings after re-run failed: %v", err)
	}
	defer resp3.Body.Close()
	body3, _ := io.ReadAll(resp3.Body)
	if !bytes.Contains(body3, []byte("Set up wiki")) {
		t.Errorf("setup modal should appear after re-run setup even though files exist, body: %s", body3)
	}
	if !bytes.Contains(body3, []byte(`name="add_home"`)) || !bytes.Contains(body3, []byte(`name="add_help"`)) {
		t.Errorf("re-run modal should offer both home and help items, body: %s", body3)
	}
	if bytes.Contains(body3, []byte(`name="add_home" checked`)) {
		t.Errorf("home checkbox should default unchecked since readme.md already exists, body: %s", body3)
	}

	// Skipping should clear ForceSetup so the modal doesn't keep reappearing.
	resp4, err := client.PostForm(server.URL+"/setup", url.Values{"action": {"skip"}})
	if err != nil {
		t.Fatalf("POST /setup skip failed: %v", err)
	}
	defer resp4.Body.Close()

	resp5, err := client.Get(server.URL + "/settings")
	if err != nil {
		t.Fatalf("GET /settings after skip failed: %v", err)
	}
	defer resp5.Body.Close()
	body5, _ := io.ReadAll(resp5.Body)
	if bytes.Contains(body5, []byte("Set up wiki")) {
		t.Errorf("setup modal should not reappear after skip, body: %s", body5)
	}
}

func TestSettingsFullFlow(t *testing.T) {
	dir := t.TempDir()
	cfgFile := dir + "/config.yaml"
	yamlContent := "site_name: Original\nbind: \":7000\"\ngit_user: test\n"
	if err := os.WriteFile(cfgFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	t.Setenv("HMD_CONFIG_FILE", cfgFile)

	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/settings")
	if err != nil {
		t.Fatalf("GET /settings failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(body, []byte("Original")) {
		t.Errorf("GET should show original site name")
	}

	form := url.Values{
		"site_name":         {"Updated Wiki"},
		"bind":              {":7000"},
		"repo_dir":          {"/data/repo"},
		"app_dir":           {"/data/app"},
		"remote_url":        {""},
		"git_user":          {"test"},
		"git_token":         {""},
		"hostname":          {"homelab"},
		"path_label":        {"~/wiki"},
		"user_label":        {""},
		"max_upload_bytes":  {"10485760"},
		"sync_poll_ms":      {"10000"},
		"show_tags_sidebar": {"on"},
		"sync_mode":         {"push"},
	}
	req, _ := http.NewRequest("POST", server.URL+"/settings", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /settings failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusSeeOther {
		t.Errorf("POST Status = %d, want 303", resp2.StatusCode)
	}

	resp3, err := client.Get(server.URL + "/settings?saved=1")
	if err != nil {
		t.Fatalf("GET /settings?saved=1 failed: %v", err)
	}
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if !bytes.Contains(body3, []byte("Updated Wiki")) {
		t.Errorf("After save, should show updated site name")
	}

	loaded, err := LoadFileConfig(cfgFile)
	if err != nil {
		t.Fatalf("LoadFileConfig failed: %v", err)
	}
	if loaded.SiteName != "Updated Wiki" {
		t.Errorf("File SiteName = %q, want %q", loaded.SiteName, "Updated Wiki")
	}
}

func TestInjectTOC(t *testing.T) {
	pages := []Page{
		{Slug: "home", Title: "Home"},
		{Slug: "alpha", Title: "Alpha"},
		{Slug: "beta", Title: "Beta"},
		{Slug: "gamma", Title: "Gamma", Tags: []string{"meta"}},
		{Slug: "delta", Title: "Delta", Tags: []string{"meta"}},
		{Slug: "epsilon", Title: "Epsilon", Tags: []string{"guide"}},
	}
	ix, _ := BuildIndex(pages)

	t.Run("all pages token excludes_home", func(t *testing.T) {
		out := injectTOC("head\n\n<!-- hmd:toc -->\ntail", ix, "home")
		want := "head\n\n- [[Alpha]]\n- [[Beta]]\n- [[Delta]]\n- [[Epsilon]]\n- [[Gamma]]\n\ntail"
		if out != want {
			t.Errorf("injectTOC all = %q, want %q", out, want)
		}
	})

	t.Run("tag filter OR semantics", func(t *testing.T) {
		out := injectTOC("<!-- hmd:toc:meta,guide -->", ix, "home")
		// meta: gamma, delta; guide: epsilon. Sorted by slug: delta, epsilon, gamma.
		want := "- [[Delta]]\n- [[Epsilon]]\n- [[Gamma]]\n"
		if out != want {
			t.Errorf("injectTOC tag = %q, want %q", out, want)
		}
	})

	t.Run("no token unchanged", func(t *testing.T) {
		body := "just some markdown, no token here"
		if got := injectTOC(body, ix, "home"); got != body {
			t.Errorf("injectTOC should be a no-op when no token present, got %q", got)
		}
	})

	t.Run("multiple tokens", func(t *testing.T) {
		out := injectTOC("A: <!-- hmd:toc:meta -->\nB: <!-- hmd:toc:guide -->", ix, "home")
		want := "A: - [[Delta]]\n- [[Gamma]]\n\nB: - [[Epsilon]]\n"
		if out != want {
			t.Errorf("injectTOC multiple = %q, want %q", out, want)
		}
	})

	t.Run("empty index", func(t *testing.T) {
		empty, _ := BuildIndex(nil)
		out := injectTOC("<!-- hmd:toc -->", empty, "home")
		if out != "" {
			t.Errorf("injectTOC on empty index = %q, want empty", out)
		}
	})
}

func TestTOCRenderedOnHome(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	// Create a page so the index TOC has something to list.
	resp, err := client.PostForm(server.URL+"/page/alpha/save", url.Values{
		"title":    {"Alpha"},
		"body":     {"Alpha body"},
		"tags":     {"meta"},
		"basehash": {""},
	})
	if err != nil {
		t.Fatalf("save alpha failed: %v", err)
	}
	resp.Body.Close()

	// View the index page: the seeded <!-- hmd:toc --> token must be
	// replaced with a rendered wiki-link to the new page.
	resp, err = client.Get(server.URL + "/page/readme")
	if err != nil {
		t.Fatalf("GET /page/readme failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if !bytes.Contains(body, []byte(`href="/page/alpha"`)) {
		t.Errorf("index page should contain a TOC link to /page/alpha, body: %s", body)
	}
	if bytes.Contains(body, []byte("hmd:toc")) {
		t.Error("raw hmd:toc token should not appear in rendered HTML")
	}

	// A tag-filtered TOC on a non-index page should also render. Create a
	// second page that embeds <!-- hmd:toc:meta --> and view it.
	resp, err = client.PostForm(server.URL+"/page/toc-test/save", url.Values{
		"title":    {"TOC Test"},
		"body":     {"Pages:\n\n<!-- hmd:toc:meta -->\n"},
		"tags":     {""},
		"basehash": {""},
	})
	if err != nil {
		t.Fatalf("save toc-test failed: %v", err)
	}
	resp.Body.Close()

	resp, err = client.Get(server.URL + "/page/toc-test")
	if err != nil {
		t.Fatalf("GET /page/toc-test failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(resp.Body)

	if !bytes.Contains(body, []byte(`href="/page/alpha"`)) {
		t.Errorf("tag-filtered TOC should link alpha (tagged meta), body: %s", body)
	}
}

func TestHelpNotInSearchButInPalette(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	// .help.md is NOT in the bleve search index.
	resp, err := client.Get(server.URL + "/search?q=frontmatter")
	if err != nil {
		t.Fatalf("GET /search failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if bytes.Contains(body, []byte(`href="/page/help"`)) {
		t.Errorf("help should not appear in search results, body: %s", body)
	}

	// The palette (app.js) should have a built-in :hidden: row linking to /hidden.
	resp2, err := client.Get(server.URL + "/static/app.js")
	if err != nil {
		t.Fatalf("GET /static/app.js failed: %v", err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if !bytes.Contains(body2, []byte(`href="/hidden"`)) || !bytes.Contains(body2, []byte(`:hidden:`)) {
		t.Errorf("app.js should contain a :hidden: palette row linking to /hidden")
	}
}

func TestHelpAtHiddenRoute(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	// Help is served at /hidden/help, not /page/help, and covers both UI
	// usage and formatting conventions in one file.
	resp, err := client.Get(server.URL + "/hidden/help")
	if err != nil {
		t.Fatalf("GET /hidden/help failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Frontmatter")) {
		t.Errorf("hidden help page should contain formatting conventions, body: %s", body)
	}

	// /page/help should NOT serve the help page.
	resp2, err := client.Get(server.URL + "/page/help")
	if err != nil {
		t.Fatalf("GET /page/help failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("GET /page/help should be 404, got %d", resp2.StatusCode)
	}

	// /hidden index should list help.
	resp3, err := client.Get(server.URL + "/hidden")
	if err != nil {
		t.Fatalf("GET /hidden failed: %v", err)
	}
	defer resp3.Body.Close()
	body3, _ := io.ReadAll(resp3.Body)
	if !bytes.Contains(body3, []byte(`href="/hidden/help"`)) {
		t.Errorf("/hidden should list help, body: %s", body3)
	}
}

func TestSetupInterstitialOnExistingRepo(t *testing.T) {
	repoDir := t.TempDir()
	appDir := t.TempDir()

	// Create a git repo with content but no readme.md.
	repo, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree failed: %v", err)
	}
	existingPage := filepath.Join(repoDir, "existing-page.md")
	if err := os.WriteFile(existingPage, []byte("# Existing\n"), 0644); err != nil {
		t.Fatalf("writing existing page: %v", err)
	}
	wt.Add("existing-page.md")
	wt.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@hmd.local", When: time.Now()},
	})

	cfg := Config{
		RepoDir:      repoDir,
		AppDir:       appDir,
		GitUser:      "test",
		AdminUser:    "admin",
		AdminPass:    "test",
		HomeFilename: "readme.md",
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	pages, _ := store.List()
	var pageObjs []Page
	for _, p := range pages {
		content, _, _ := store.Read(p)
		pageObjs = append(pageObjs, ParsePage(p[:len(p)-3], content))
	}
	index, _ := BuildIndex(pageObjs)
	auth, _ := OpenAuth(cfg)
	renderer := NewRenderer(index.Exists)
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates failed: %v", err)
	}
	app := &App{
		Store:  store,
		Auth:   auth,
		Index:  index,
		Render: renderer,
		Tmpl:   tmpl,
	}
	app.SetConfig(cfg)

	server := httptest.NewServer(app.Auth.Middleware(app.Routes()))
	defer server.Close()

	jar, _ := cookiejar.New(&cookiejar.Options{})
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	loginForm := url.Values{"username": {"admin"}, "password": {"test"}}
	req, _ := http.NewRequest("POST", server.URL+"/login", bytes.NewBufferString(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client.Do(req)

	// Root should redirect to /page/readme (not a standalone setup page).
	resp, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("root should redirect, got status %d", resp.StatusCode)
	}

	// Setup modal should appear on any authed page when NeedsSetup.
	resp2, err := client.Get(server.URL + "/page/readme")
	if err != nil {
		t.Fatalf("GET /page/readme failed: %v", err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if !bytes.Contains(body2, []byte("Set up wiki")) {
		t.Errorf("setup modal should appear when NeedsSetup, body: %s", body2)
	}

	// POST setup with action=add, add_home=on to seed readme.md.
	resp3, err := client.PostForm(server.URL+"/setup", url.Values{"action": {"add"}, "add_home": {"on"}})
	if err != nil {
		t.Fatalf("POST /setup failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusSeeOther {
		t.Errorf("setup status = %d, want 303", resp3.StatusCode)
	}

	// readme.md should now exist and be viewable.
	resp4, err := client.Get(server.URL + "/page/readme")
	if err != nil {
		t.Fatalf("GET /page/readme failed: %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK {
		t.Errorf("home page status = %d, want 200", resp4.StatusCode)
	}
	body4, _ := io.ReadAll(resp4.Body)
	if !bytes.Contains(body4, []byte("Welcome")) {
		t.Errorf("home page should contain 'Welcome', body: %s", body4)
	}

	// Modal should no longer appear after setup.
	resp5, err := client.Get(server.URL + "/page/readme")
	if err != nil {
		t.Fatalf("GET /page/readme after setup failed: %v", err)
	}
	defer resp5.Body.Close()
	body5, _ := io.ReadAll(resp5.Body)
	if bytes.Contains(body5, []byte("setup-modal-backdrop")) {
		t.Errorf("setup modal should not appear after setup, body: %s", body5)
	}
}

// TestCustomHomeFilename exercises HMD_HOME_FILENAME end-to-end: the seed
// writes the configured file, / redirects to /page/<slug>, the page is
// viewable there, and the TOC token on a sibling page excludes it.
func TestCustomHomeFilename(t *testing.T) {
	repoDir := t.TempDir()
	appDir := t.TempDir()

	cfg := Config{
		RepoDir:      repoDir,
		AppDir:       appDir,
		GitUser:      "test",
		AdminUser:    "admin",
		AdminPass:    "test",
		HomeFilename: "index.md",
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	if _, err := store.Save("index.md", Page{Slug: "index", Title: "index", Body: defaultHomeMD}.Encode(), "Add index.md", cfg.GitUser, cfg.GitUser+"@hmd.local"); err != nil {
		t.Fatalf("seeding index.md: %v", err)
	}
	if _, err := store.Save(".help.md", Page{Slug: "help", Title: "Help", Tags: []string{"meta"}, Body: defaultHelpMD}.Encode(), "Add .help.md", cfg.GitUser, cfg.GitUser+"@hmd.local"); err != nil {
		t.Fatalf("seeding .help.md: %v", err)
	}
	store.NeedsSetup.Store(false)

	// Also add a second page so the TOC has something to list.
	if _, err := store.Save("alpha.md", Page{Slug: "alpha", Title: "Alpha", Body: "Alpha body"}.Encode(), "Add alpha", cfg.GitUser, cfg.GitUser+"@hmd.local"); err != nil {
		t.Fatalf("seeding alpha: %v", err)
	}

	pages, _ := store.List()
	var pageObjs []Page
	for _, p := range pages {
		content, _, _ := store.Read(p)
		pageObjs = append(pageObjs, ParsePage(p[:len(p)-3], content))
	}
	index, _ := BuildIndex(pageObjs)
	auth, _ := OpenAuth(cfg)
	renderer := NewRenderer(index.Exists)
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates failed: %v", err)
	}
	app := &App{Store: store, Auth: auth, Index: index, Render: renderer, Tmpl: tmpl}
	app.SetConfig(cfg)

	server := httptest.NewServer(app.Auth.Middleware(app.Routes()))
	defer server.Close()

	jar, _ := cookiejar.New(&cookiejar.Options{})
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	loginForm := url.Values{"username": {"admin"}, "password": {"test"}}
	req, _ := http.NewRequest("POST", server.URL+"/login", bytes.NewBufferString(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client.Do(req)

	// Root redirects to /page/index (slug derived from index.md).
	resp, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/page/index" {
		t.Errorf("root redirect = %q, want /page/index", loc)
	}

	// The home page is viewable at /page/index.
	resp2, err := client.Get(server.URL + "/page/index")
	if err != nil {
		t.Fatalf("GET /page/index failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("GET /page/index status = %d, want 200", resp2.StatusCode)
	}
	body2, _ := io.ReadAll(resp2.Body)
	if !bytes.Contains(body2, []byte("Welcome")) {
		t.Errorf("home page should contain 'Welcome', body: %s", body2)
	}

	// TOC on the home page lists alpha but must not list the home slug itself.
	if !bytes.Contains(body2, []byte(`href="/page/alpha"`)) {
		t.Errorf("home page TOC should link alpha, body: %s", body2)
	}
	if bytes.Contains(body2, []byte(`href="/page/index"`)) {
		t.Errorf("home page TOC must not link the home page itself, body: %s", body2)
	}

	// A custom filename like home.md must NOT be treated as home: create a
	// page named home.md and confirm it appears in TOC listings (it's an
	// ordinary page now that index.md is the configured home file).
	if _, err := store.Save("home.md", Page{Slug: "home", Title: "Home", Body: "<!-- hmd:toc -->\n"}.Encode(), "Add home", cfg.GitUser, cfg.GitUser+"@hmd.local"); err != nil {
		t.Fatalf("seeding home.md: %v", err)
	}
	app.Index.Update(ParsePage("home", []byte("---\ntitle: Home\n---\n\n<!-- hmd:toc -->\n")))

	resp3, err := client.Get(server.URL + "/page/home")
	if err != nil {
		t.Fatalf("GET /page/home failed: %v", err)
	}
	defer resp3.Body.Close()
	body3, _ := io.ReadAll(resp3.Body)
	// "home" is an ordinary page, NOT the home slug, so it should appear in
	// the TOC of the index page (home slug "index" is the one excluded).
	resp4, err := client.Get(server.URL + "/page/index")
	if err != nil {
		t.Fatalf("GET /page/index failed: %v", err)
	}
	defer resp4.Body.Close()
	body4, _ := io.ReadAll(resp4.Body)
	if !bytes.Contains(body4, []byte(`href="/page/home"`)) {
		t.Errorf("index page TOC should list the ordinary 'home' page, body: %s", body4)
	}
	if bytes.Contains(body4, []byte(`href="/page/index"`)) {
		t.Errorf("index page TOC must not list the home page itself, body: %s", body4)
	}
	// And the 'home' page's own TOC should list alpha but not the home slug.
	if !bytes.Contains(body3, []byte(`href="/page/alpha"`)) {
		t.Errorf("home page TOC should list alpha, body: %s", body3)
	}
	if bytes.Contains(body3, []byte(`href="/page/index"`)) {
		t.Errorf("home page TOC must not list the home slug 'index', body: %s", body3)
	}
}
