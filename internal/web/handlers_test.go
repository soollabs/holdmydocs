package web

import (
	"bytes"
	"context"
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
	"hmd/internal/api"
	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/httpapi"
	"hmd/internal/httpmiddleware"
	"hmd/internal/search"
	"hmd/internal/store"
	"hmd/internal/wiki"
)

const testNS = "notes"
const testHome = testNS + "/" + store.DefaultIndexPage

type csrfTestTransport struct {
	base http.RoundTripper
	jar  http.CookieJar
	auth *auth.Auth
}

func (t csrfTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet || req.Method == http.MethodHead || req.Method == http.MethodOptions || req.Header.Get("Authorization") != "" {
		return t.base.RoundTrip(req)
	}
	cookies := t.jar.Cookies(req.URL)
	if cookie, err := req.Cookie("hmd_session"); err == nil {
		cookies = append(cookies, cookie)
	}
	for _, cookie := range cookies {
		if cookie.Name != "hmd_session" {
			continue
		}
		clone := req.Clone(req.Context())
		clone.Header = req.Header.Clone()
		for _, jarCookie := range t.jar.Cookies(req.URL) {
			clone.AddCookie(jarCookie)
		}
		clone.Header.Set("Origin", req.URL.Scheme+"://"+req.URL.Host)
		clone.Header.Set("X-CSRF-Token", t.auth.CSRFToken(cookie.Value))
		return t.base.RoundTrip(clone)
	}
	return t.base.RoundTrip(req)
}

func newTestApp(t *testing.T) (*httptest.Server, *http.Client) {
	_, server, client := newTestAppFull(t)
	return server, client
}

func adminLogin(t *testing.T, server *httptest.Server, client *http.Client) {
	t.Helper()
	resp, err := client.PostForm(server.URL+"/_/login", url.Values{"username": {"admin"}, "password": {"password12345"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	closeTestBody(t, resp.Body)
}

func closeTestBody(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
}

// postJSON sends a JSON body to the composed test server, mirroring how the
// browser JavaScript calls the httpapi mutation surface.
func postJSON(t *testing.T, client *http.Client, url string, body any) (*http.Response, error) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshalling JSON body for %s: %v", url, err)
	}
	return client.Post(url, "application/json", bytes.NewReader(raw))
}

// postPageSave saves a page through the JSON mutation endpoint, converting the
// form values the retired ?do=save route accepted.
func postPageSave(t *testing.T, client *http.Client, server *httptest.Server, slug string, form url.Values) (*http.Response, error) {
	t.Helper()
	return postJSON(t, client, server.URL+"/_/api/pages/"+slug, map[string]any{
		"title":     form.Get("title"),
		"body":      form.Get("body"),
		"tags":      wiki.ParseTags(form.Get("tags")),
		"new_slug":  form.Get("new_slug"),
		"base_hash": form.Get("basehash"),
		"hidden":    form.Get("hidden") == "on",
	})
}

// postPageRevert restores a revision through the JSON mutation endpoint.
func postPageRevert(t *testing.T, client *http.Client, server *httptest.Server, slug, hash string) (*http.Response, error) {
	t.Helper()
	return postJSON(t, client, server.URL+"/_/api/pages/revert/"+slug, map[string]any{"hash": hash})
}

// postPageTags replaces a page's tags through the JSON mutation endpoint.
func postPageTags(t *testing.T, client *http.Client, server *httptest.Server, slug, tags string) (*http.Response, error) {
	t.Helper()
	return postJSON(t, client, server.URL+"/_/api/pages/tags/"+slug, map[string]any{"tags": wiki.ParseTags(tags)})
}

// postPageRename retitles a page through the JSON mutation endpoint.
func postPageRename(t *testing.T, client *http.Client, server *httptest.Server, slug, title string) (*http.Response, error) {
	t.Helper()
	return postJSON(t, client, server.URL+"/_/api/pages/rename/"+slug, map[string]any{"title": title})
}

// testHandler assembles the browser mux exactly as the composition root does,
// mounting the httpapi JSON surface alongside the browser routes, then wraps
// the shared middleware. It lets package tests exercise the adapter boundary
// end to end.
func testHandler(app *App) http.Handler {
	mux := http.NewServeMux()
	httpapi.New(app.API, httpapi.Options{
		Renderer:         app.Render,
		DocumentsEnabled: app.API.DocumentsEnabled(),
	}).Register(mux)
	mux.Handle("/", app.Routes())
	return httpmiddleware.SecurityHeaders(app.Auth.Middleware(mux))
}

// testApp is the package-test fixture: the browser App plus the raw
// persistence and index handles so tests can seed a repository directly,
// then exercise the App through the application api.
type testApp struct {
	*App
	Store *store.Store
	Index *search.Index
}

func newTestAppFull(t *testing.T) (*testApp, *httptest.Server, *http.Client) {
	repoDir := t.TempDir()
	appDir := t.TempDir()

	cfg := config.Config{
		ConfigFile: filepath.Join(appDir, "config.yaml"),
		RepoDir:    repoDir,
		AppDir:     appDir,
		Git:        config.GitConfig{User: "test"},
		AdminUser:  "admin",
		AdminPass:  "password12345",
	}

	if path := os.Getenv("HMD_CONFIG_FILE"); path != "" {
		cfg.ConfigFile = path
		if loaded, err := config.LoadConfig(); err == nil {
			loaded.RepoDir = repoDir
			loaded.AppDir = appDir
			loaded.AdminUser = "admin"
			loaded.AdminPass = "password12345"
			cfg = loaded
		}
	}

	repo, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	nsCfg, err := wiki.NamespaceConfig{Widgets: wiki.BuiltinNamespaceWidgets, Index: store.DefaultIndexPage}.Encode()
	if err != nil {
		t.Fatalf("encoding namespace config: %v", err)
	}
	if _, err := repo.Save(wiki.NamespaceConfigPath(testNS), nsCfg, "Configure namespace "+testNS, cfg.Git.User, cfg.Git.User+"@hmd.local"); err != nil {
		t.Fatalf("seeding namespace config: %v", err)
	}
	if _, err := repo.Save(wiki.PageFile(testHome), wiki.Page{Slug: testHome, Title: testNS, Body: store.DefaultHomeMD}.Encode(), "Add "+testHome, cfg.Git.User, cfg.Git.User+"@hmd.local"); err != nil {
		t.Fatalf("seeding index page: %v", err)
	}
	if _, err := repo.Save(".help.md", wiki.Page{Slug: "help", Title: "Help", Tags: []string{"meta"}, Body: store.DefaultHelpMD}.Encode(), "Add .help.md", cfg.Git.User, cfg.Git.User+"@hmd.local"); err != nil {
		t.Fatalf("seeding .help.md: %v", err)
	}
	wikiData, err := (wiki.WikiConfig{Landing: testNS + "/"}).Encode()
	if err != nil {
		t.Fatalf("encoding wiki config: %v", err)
	}
	if _, err := repo.Save(wiki.ConfigFile, wikiData, "Configure wiki settings", cfg.Git.User, cfg.Git.User+"@hmd.local"); err != nil {
		t.Fatalf("seeding wiki config: %v", err)
	}
	repo.NeedsSetup.Store(false)

	pages, _ := repo.List()
	var pageObjs []wiki.Page
	for _, p := range pages {
		content, _, _ := repo.Read(p)
		pageObjs = append(pageObjs, wiki.ParsePage(p[:len(p)-3], content))
	}

	index, _ := search.BuildIndex(pageObjs)
	authn, _ := OpenAuth(cfg)
	renderer := wiki.NewRenderer(index.ResolveLink)

	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates failed: %v", err)
	}

	namespaces, err := api.BuildNamespaceRegistry(repoDir)
	if err != nil {
		t.Fatalf("api.BuildNamespaceRegistry failed: %v", err)
	}

	env := &testApp{
		App:   &App{API: api.New(repo, index, authn), Auth: authn, Render: renderer, Tmpl: tmpl},
		Store: repo,
		Index: index,
	}
	env.SetConfig(cfg)
	env.SetWikiConfig(wiki.WikiConfig{Landing: testNS + "/"})
	env.SetNamespaces(namespaces)

	server := httptest.NewServer(testHandler(env.App))

	jar, _ := cookiejar.New(&cookiejar.Options{})
	client := &http.Client{
		Jar: jar,
		Transport: csrfTestTransport{
			base: http.DefaultTransport,
			jar:  jar,
			auth: authn,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	loginForm := url.Values{
		"username": {"admin"},
		"password": {"password12345"},
	}
	req, _ := http.NewRequest("POST", server.URL+"/_/login", bytes.NewBufferString(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := client.Do(req); err != nil {
		t.Fatalf("login request failed: %v", err)
	}

	return env, server, client
}

func TestViewHome(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/" + testHome)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Welcome")) {
		t.Errorf("Body should contain 'Welcome'")
	}
}

func TestAttachmentSymlinkIsNotServed(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	path := filepath.Join(app.config().RepoDir, "attachments", testHome, "linked.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(server.URL + "/_/attachments/" + testHome + "/linked.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("symlink attachment status = %d, want 404", resp.StatusCode)
	}
}

func TestCreateAffordance(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/" + testNS + "/does-not-exist")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
	}()

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

	slug := testNS + "/test-page"

	resp, err := client.Get(server.URL + "/" + slug + "?do=edit")
	if err != nil {
		t.Fatalf("GET edit failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing edit response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Edit page status = %d, want 200", resp.StatusCode)
	}

	saveForm := url.Values{
		"title":    {"Test Page"},
		"body":     {"This is a test page."},
		"basehash": {""},
	}
	resp, err = postPageSave(t, client, server, slug, saveForm)
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing save response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Save status = %d, want 200", resp.StatusCode)
	}

	resp, err = client.Get(server.URL + "/" + slug)
	if err != nil {
		t.Fatalf("GET page failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing page response body: %v", err)
		}
	}()

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("This is a test page.")) {
		t.Errorf("Saved page should contain body text")
	}

}

// TestCommittedSaveWithIndexWarning demonstrates the browser save contract when
// the derived index cannot refresh: the write is committed (200) and the JSON
// result reports a pending index refresh, so the editor does not present a
// spurious failure or invite a retry of a durable write.
func TestCommittedSaveWithIndexWarning(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	if err := app.Index.Close(); err != nil {
		t.Fatalf("closing index: %v", err)
	}

	slug := testNS + "/index-warning"
	resp, err := postPageSave(t, client, server, slug, url.Values{
		"title":    {"Index Warning"},
		"body":     {"committed despite index failure"},
		"basehash": {""},
	})
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d, want 200 (committed)", resp.StatusCode)
	}
	var result struct {
		IndexWarning string `json:"index_warning"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding save result: %v", err)
	}
	if result.IndexWarning == "" {
		t.Error("committed save should report the pending index refresh")
	}
	if _, _, err := app.Store.Read(wiki.PageFile(slug)); err != nil {
		t.Fatalf("committed page missing: %v", err)
	}
}

func TestOptimisticLockConflict(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	slug := testNS + "/lock-test"

	resp, err := client.Get(server.URL + "/" + slug + "?do=edit")
	if err != nil {
		t.Fatalf("GET edit failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing edit response body: %v", err)
		}
	}()

	saveForm := url.Values{
		"title":    {"Version 1"},
		"body":     {"First version"},
		"basehash": {""},
	}
	resp, err = postPageSave(t, client, server, slug, saveForm)
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing save response body: %v", err)
	}

	saveForm2 := url.Values{
		"title":    {"Version 2"},
		"body":     {"Second version"},
		"basehash": {""},
	}
	resp, err = postPageSave(t, client, server, slug, saveForm2)
	if err != nil {
		t.Fatalf("POST save2 failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing conflict response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusConflict {
		t.Errorf("Conflict status = %d, want 409", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var conflict struct {
		Error    string `json:"error"`
		Conflict struct {
			CurrentHash string `json:"current_hash"`
		} `json:"conflict"`
	}
	if err := json.Unmarshal(body, &conflict); err != nil {
		t.Fatalf("conflict response is not JSON: %v (%s)", err, body)
	}
	if conflict.Conflict.CurrentHash == "" {
		t.Errorf("conflict response should carry the current hash: %s", body)
	}
}

func TestUnauthenticatedAccess(t *testing.T) {
	server, _ := newTestApp(t)
	defer server.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	t.Run("root redirects to login", func(t *testing.T) {
		resp, err := client.Get(server.URL + "/")
		if err != nil {
			t.Fatalf("GET failed: %v", err)
		}
		defer closeTestBody(t, resp.Body)

		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", resp.StatusCode)
		}
		if location := resp.Header.Get("Location"); !bytes.Contains([]byte(location), []byte("/_/login")) {
			t.Errorf("Should redirect to /login, got %q", location)
		}
	})

	t.Run("private page 404s", func(t *testing.T) {
		resp, err := client.Get(server.URL + "/" + testHome)
		if err != nil {
			t.Fatalf("GET failed: %v", err)
		}
		defer closeTestBody(t, resp.Body)

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
	})
}

func TestAttachmentUploadAndServe(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	slug := testNS + "/test-page"

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
	part, _ := writer.CreateFormFile("file", "test-image.png")
	if _, err := io.Copy(part, bytes.NewReader(pngData)); err != nil {
		t.Fatalf("copying PNG data: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}

	req, _ := http.NewRequest("POST", server.URL+"/_/api/attachments/"+slug, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST upload failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing upload response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Upload status = %d, want 200", resp.StatusCode)
	}

	respBody, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(respBody, []byte("/_/attachments/"+slug+"/")) {
		t.Errorf("Response should contain attachment URL")
	}

	resp, err = client.Get(server.URL + "/_/attachments/" + slug + "/test-image.png")
	if err != nil {
		t.Fatalf("GET attachment failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing attachment response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Serve status = %d, want 200", resp.StatusCode)
	}

	content, _ := io.ReadAll(resp.Body)
	if len(content) == 0 {
		t.Errorf("Attachment content is empty")
	}
}

func TestAttachmentAcceptsTikaFormats(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	slug := testNS + "/test-page"

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "virus.exe")
	if _, err := part.Write([]byte("malware")); err != nil {
		t.Fatalf("writing malware payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}

	req, _ := http.NewRequest("POST", server.URL+"/_/api/attachments/"+slug, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST attachment failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing rejected attachment response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("upload status = %d, want 200", resp.StatusCode)
	}
	resp, err = client.Get(server.URL + "/_/attachments/" + slug + "/virus.exe")
	if err != nil {
		t.Fatalf("GET attachment failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if got := resp.Header.Get("Content-Disposition"); got != "attachment" {
		t.Errorf("Content-Disposition = %q, want attachment", got)
	}
}

func TestTextAttachmentStoresExtractedSidecar(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "source.txt")
	if err != nil {
		t.Fatalf("creating upload field: %v", err)
	}
	if _, err := part.Write([]byte("source text")); err != nil {
		t.Fatalf("writing source: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}
	req, err := http.NewRequest("POST", server.URL+"/_/api/attachments/"+testHome, body)
	if err != nil {
		t.Fatalf("creating upload request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("posting upload: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", resp.StatusCode)
	}

	source := []byte("source text")
	sidecar, _, err := app.Store.Read(search.ExtractedAttachmentPath("attachments/" + testHome + "/source.txt"))
	if err != nil {
		t.Fatalf("reading extracted sidecar: %v", err)
	}
	if text, ok := search.DecodeExtractedAttachment(search.AttachmentBlobHash(source), sidecar); !ok || text != "source text" {
		t.Fatalf("extracted sidecar = %q, valid = %v", text, ok)
	}
	attachments, err := app.Store.ListAttachments()
	if err != nil || len(attachments) != 1 || attachments[0] != "attachments/"+testHome+"/source.txt" {
		t.Fatalf("ListAttachments = %v, %v", attachments, err)
	}
	commits, err := app.Store.RecentCommits(1)
	if err != nil {
		t.Fatalf("RecentCommits: %v", err)
	}
	if len(commits) != 1 || len(commits[0].Files) != 2 {
		t.Fatalf("latest commit = %+v", commits)
	}

	resp, err = client.Get(server.URL + "/_/attachments/" + testHome + "/.hmd/extracted/source.txt.txt")
	if err != nil {
		t.Fatalf("getting hidden sidecar: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("hidden sidecar status = %d, want 404", resp.StatusCode)
	}
}

func TestAttachmentRejectsOversizedUpload(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	cfg := app.config()
	cfg.MaxUploadBytes = 256
	app.SetConfig(cfg)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "large.png")
	if err != nil {
		t.Fatalf("creating upload field: %v", err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("x"), 512)); err != nil {
		t.Fatalf("writing upload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}

	req, err := http.NewRequest("POST", server.URL+"/_/api/attachments/"+testHome, body)
	if err != nil {
		t.Fatalf("creating upload request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("posting upload: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized upload status = %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
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
	if _, err := io.Copy(part, bytes.NewReader(pngData)); err != nil {
		t.Fatalf("copying PNG data: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}

	// Use an encoded dot-segment to bypass ServeMux normalization and test slug validation.
	req, _ := http.NewRequest("POST", server.URL+"/_/api/attachments/%2e%2e", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST upload failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing traversal response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Path traversal slug status = %d, want 400", resp.StatusCode)
	}
}

func TestSearchPage(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	slug := testNS + "/search-test"
	saveForm := url.Values{
		"title":    {"Search Test"},
		"body":     {"This contains uniquewordxyz for searching."},
		"basehash": {""},
	}
	resp, err := postPageSave(t, client, server, slug, saveForm)
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	resp, err = client.Get(server.URL + "/_/search?q=uniquewordxyz")
	if err != nil {
		t.Fatalf("GET search failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

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

	saveForm := url.Values{
		"title":    {"Page One"},
		"body":     {"This links to [[Page Two]]"},
		"basehash": {""},
	}
	resp, err := postPageSave(t, client, server, testNS+"/page-one", saveForm)
	if err != nil {
		t.Fatalf("POST one failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	saveForm = url.Values{
		"title":    {"Page Two"},
		"body":     {"This is page two."},
		"basehash": {""},
	}
	resp, err = postPageSave(t, client, server, testNS+"/page-two", saveForm)
	if err != nil {
		t.Fatalf("POST two failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	resp, err = client.Get(server.URL + "/" + testNS + "/page-two")
	if err != nil {
		t.Fatalf("GET page two failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("linked from")) {
		t.Logf("Response: %s", body)
		t.Errorf("Page should show 'linked from' section")
	}
	if !bytes.Contains(body, []byte("/page-one")) {
		t.Errorf("Backlinks should contain link to page-one")
	}
}

func TestHistoryListAndRevert(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	slug := testNS + "/history-test"

	saveForm := url.Values{
		"title":    {"History Test"},
		"body":     {"Version one"},
		"basehash": {""},
	}
	resp, err := postPageSave(t, client, server, slug, saveForm)
	if err != nil {
		t.Fatalf("POST v1 failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	saveForm = url.Values{
		"title":    {"History Test"},
		"body":     {"Version two"},
		"basehash": {""},
	}
	resp, err = postPageSave(t, client, server, slug, saveForm)
	if err != nil {
		t.Fatalf("POST v2 failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	resp, err = client.Get(server.URL + "/" + slug + "?do=history")
	if err != nil {
		t.Fatalf("GET history failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("History status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("History")) {
		t.Errorf("History page should contain 'History'")
	}

}

func TestRevertToOldVersion(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	slug := testNS + "/revert-test"

	saveForm := url.Values{
		"title":    {"Revert Test"},
		"body":     {"Original content"},
		"basehash": {""},
	}
	resp, err := postPageSave(t, client, server, slug, saveForm)
	if err != nil {
		t.Fatalf("POST v1 failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	resp, err = client.Get(server.URL + "/" + slug)
	if err != nil {
		t.Fatalf("GET page failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Original content")) {
		t.Errorf("Page should show v1 content")
	}
}

// TestPageChrome tests the shared page layout.
func TestPageChrome(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/" + testHome)
	if err != nil {
		t.Fatalf("GET page failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)

	for _, want := range []string{
		`src="` + staticURL("app.js") + `"`,
		`class="app-topbar"`,
		`class="sidebar"`,
		`action="/_/logout"`,
		`seg-sync`,
	} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("Page missing %s", want)
		}
	}

	if bytes.Contains(body, []byte(`src="`+staticURL("mermaid.min.js")+`"`)) {
		t.Error("Mermaid script loaded on page without mermaid content")
	}

	if bytes.Contains(body, []byte("hmd:toc")) {
		t.Error("raw hmd:toc token should not appear in rendered HTML")
	}

	resp, err = client.Get(server.URL + "/" + testHome + "?do=edit")
	if err != nil {
		t.Fatalf("GET edit failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ = io.ReadAll(resp.Body)

	for _, want := range []string{
		`id="cm-host"`,
		`data-slug="` + testHome + `"`,
		`src="` + staticURL("editor.js") + `"`,
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

	resp, err := postPageSave(t, client, server, testNS+"/mermaid-test", url.Values{
		"title":    {"Mermaid Test"},
		"body":     {"```mermaid\ngraph TD;\n  A-->B\n```\n"},
		"tags":     {""},
		"basehash": {""},
	})
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	resp, err = client.Get(server.URL + "/" + testNS + "/mermaid-test")
	if err != nil {
		t.Fatalf("GET page failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)

	if !bytes.Contains(body, []byte(`src="`+staticURL("mermaid.min.js")+`"`)) {
		t.Error("Mermaid script missing on page with mermaid content")
	}

	resp, err = client.Get(server.URL + "/" + testNS + "/mermaid-test?do=edit")
	if err != nil {
		t.Fatalf("GET edit failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ = io.ReadAll(resp.Body)

	if !bytes.Contains(body, []byte(`src="`+staticURL("mermaid.min.js")+`"`)) {
		t.Error("Mermaid script missing on edit page when body contains mermaid")
	}
}

func TestLoginErrorShown(t *testing.T) {
	server, _ := newTestApp(t)
	defer server.Close()

	resp, err := http.PostForm(server.URL+"/_/login", url.Values{
		"username": {"admin"},
		"password": {"wrong"},
	})
	if err != nil {
		t.Fatalf("POST login failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

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
	resp, err := postPageSave(t, client, server, testNS+"/tagged", form)
	if err != nil {
		t.Fatalf("save request failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	resp, err = client.Get(server.URL + "/_/tags")
	if err != nil {
		t.Fatalf("tags index request failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `href="/_/tags/go"`) {
		t.Errorf("GET /_/tags should list a link to /_/tags/go, got status %d body:\n%s", resp.StatusCode, body)
	}

	resp, err = client.Get(server.URL + "/_/tags/go")
	if err != nil {
		t.Fatalf("tag page request failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `href="/`+testNS+`/tagged"`) {
		t.Errorf("GET /_/tags/go should list a link to /page/tagged, got status %d body:\n%s", resp.StatusCode, body)
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
	resp, err := postPageSave(t, client, server, testNS+"/tagged", form)
	if err != nil {
		t.Fatalf("save request failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	resp, err = client.Get(server.URL + "/" + testNS + "/tagged?do=edit")
	if err != nil {
		t.Fatalf("edit request failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `value="go, wiki"`) {
		t.Errorf("edit page should show tags input, got:\n%s", body)
	}

	resp, err = client.Get(server.URL + "/" + testNS + "/tagged")
	if err != nil {
		t.Fatalf("view request failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `href="/_/tags/go"`) || !strings.Contains(string(body), `class="meta-tag"`) {
		t.Errorf("page view should show a tag chip linking to /_/tags/go, got:\n%s", body)
	}
}

// TestTitleEscaped verifies that page titles are escaped.
func TestTitleEscaped(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	saveForm := url.Values{
		"title":    {`<script>alert(1)</script>`},
		"body":     {"content"},
		"basehash": {""},
	}
	resp, err := postPageSave(t, client, server, testNS+"/xss-test", saveForm)
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	resp, err = client.Get(server.URL + "/xss-test")
	if err != nil {
		t.Fatalf("GET page failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)

	if bytes.Contains(body, []byte("<script>alert(1)</script>")) {
		t.Error("Page title rendered unescaped — stored XSS")
	}
}

func TestAppConfigPointer(t *testing.T) {
	repoDir := t.TempDir()
	appDir := t.TempDir()
	cfg := config.Config{RepoDir: repoDir, AppDir: appDir, Git: config.GitConfig{User: "test"}}
	repo, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	authn, _ := OpenAuth(cfg)
	index, _ := search.BuildIndex(nil)
	renderer := wiki.NewRenderer(index.ResolveLink)
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates failed: %v", err)
	}
	namespaces, err := api.BuildNamespaceRegistry(repoDir)
	if err != nil {
		t.Fatalf("api.BuildNamespaceRegistry failed: %v", err)
	}

	app := &App{API: api.New(repo, index, authn), Auth: authn, Render: renderer, Tmpl: tmpl}
	app.SetConfig(cfg)
	app.SetWikiConfig(wiki.WikiConfig{SiteName: "TestWiki"})
	app.SetNamespaces(namespaces)

	got := app.wikiConfig()
	if got.SiteName != "TestWiki" {
		t.Errorf("wikiConfig().SiteName = %q, want %q", got.SiteName, "TestWiki")
	}

	app.SetWikiConfig(wiki.WikiConfig{SiteName: "Changed"})
	got2 := app.wikiConfig()
	if got2.SiteName != "Changed" {
		t.Errorf("after swap, wikiConfig().SiteName = %q, want %q", got2.SiteName, "Changed")
	}
}

func TestBuildSettingsDataEditable(t *testing.T) {
	cfg := config.Config{
		ConfigFile: "/path/config.yaml",
		Bind:       ":8080",
		RepoDir:    "/data/repo",
		AppDir:     "/data/app",
		Git:        config.GitConfig{RemoteURL: "https://example.com/repo.git", User: "hmd", Token: "secret"},
	}

	sd := buildSettingsData(cfg, auth.UserRecord{})

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
	cfg := config.Config{
		ConfigFile: "/path/config.yaml",
		Bind:       ":9999",
		RepoDir:    "/data/repo",
		AppDir:     "/data/app",
		Git:        config.GitConfig{User: "hmd"},
		EnvOverrides: map[string]string{
			"Bind": "HMD_BIND",
		},
	}

	sd := buildSettingsData(cfg, auth.UserRecord{})

	if sd.Fields["Bind"].Editable {
		t.Error("Bind should be read-only (env set)")
	}
	if sd.Fields["Bind"].EnvVar != "HMD_BIND" {
		t.Errorf("Bind EnvVar = %q, want %q", sd.Fields["Bind"].EnvVar, "HMD_BIND")
	}
}

func TestBuildSettingsDataTokenFileLocked(t *testing.T) {
	cfg := config.Config{
		ConfigFile: "/path/config.yaml",
		Git:        config.GitConfig{Token: "file-token", User: "hmd"},
		RepoDir:    "/data/repo",
		AppDir:     "/data/app",
		EnvOverrides: map[string]string{
			"Git.TokenFile": "HMD_GIT_TOKEN_FILE",
		},
	}

	sd := buildSettingsData(cfg, auth.UserRecord{})

	if sd.Fields["GitToken"].Editable {
		t.Error("GitToken should be read-only when token file is set")
	}
	if sd.Fields["GitToken"].EnvVar == "" {
		t.Error("GitToken EnvVar should be set when token file is configured")
	}
}

func TestSettingsGetWithConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := dir + "/config.yaml"
	yamlContent := "bind: \":7000\"\n"
	if err := os.WriteFile(cfgFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	t.Setenv("HMD_CONFIG_FILE", cfgFile)

	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/_/admin")
	if err != nil {
		t.Fatalf("GET /settings failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte(":7000")) {
		t.Errorf("Body should contain the bind value from the config file")
	}
}

func TestSettingsPostSavesAndUpdates(t *testing.T) {
	dir := t.TempDir()
	cfgFile := dir + "/config.yaml"
	yamlContent := "bind: \":7000\"\n"
	if err := os.WriteFile(cfgFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	t.Setenv("HMD_CONFIG_FILE", cfgFile)

	server, client := newTestApp(t)
	defer server.Close()

	form := url.Values{
		"bind":             {":7001"},
		"repo_dir":         {"/data/repo"},
		"remote_url":       {""},
		"git_user":         {"test"},
		"git_token":        {""},
		"max_upload_bytes": {"10485760"},
		"sync_poll_ms":     {"10000"},
		"sync_mode":        {"push"},
	}
	resp, err := postJSON(t, client, server.URL+"/_/api/settings/server", serverSettingsJSON(form))
	if err != nil {
		t.Fatalf("POST settings failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.StatusCode)
	}

	loaded, err := config.LoadFileConfig(cfgFile)
	if err != nil {
		t.Fatalf("LoadFileConfig failed: %v", err)
	}
	if loaded.Bind != ":7001" {
		t.Errorf("Bind in file = %q, want %q", loaded.Bind, ":7001")
	}
}

func TestWikiSettingsPostSavesPortableConfig(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	resp, err := postJSON(t, client, server.URL+"/_/api/settings/wiki", wikiConfigJSON(url.Values{
		"site_name": {"Shared Wiki"},
		"landing":   {"notes/"},
	}))
	if err != nil {
		t.Fatalf("POST /_/api/settings/wiki: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := app.wikiConfig(); got != (wiki.WikiConfig{Landing: "notes/", SiteName: "Shared Wiki"}) {
		t.Errorf("runtime wiki config = %#v", got)
	}
	stored, exists, err := wiki.LoadWikiConfig(app.config().RepoDir)
	if err != nil {
		t.Fatalf("LoadWikiConfig: %v", err)
	}
	if !exists || stored != app.wikiConfig() {
		t.Errorf("stored wiki config = %#v, exists=%v", stored, exists)
	}
	admin, err := client.Get(server.URL + "/_/admin")
	if err != nil {
		t.Fatalf("GET /_/admin: %v", err)
	}
	defer closeTestBody(t, admin.Body)
	body, _ := io.ReadAll(admin.Body)
	if !bytes.Contains(body, []byte(`action="/_/api/settings/wiki"`)) || !bytes.Contains(body, []byte("Travels with the content repository")) {
		t.Errorf("admin page should distinguish repository settings, body: %s", body)
	}
}

func TestSettingsPostInvalidBind(t *testing.T) {
	dir := t.TempDir()
	cfgFile := dir + "/config.yaml"
	yamlContent := ""
	if err := os.WriteFile(cfgFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	t.Setenv("HMD_CONFIG_FILE", cfgFile)

	server, client := newTestApp(t)
	defer server.Close()

	form := url.Values{
		"bind":             {""},
		"repo_dir":         {"/data/repo"},
		"remote_url":       {""},
		"git_user":         {"test"},
		"git_token":        {""},
		"max_upload_bytes": {"10485760"},
		"sync_poll_ms":     {"10000"},
		"sync_mode":        {"push"},
	}
	resp, err := postJSON(t, client, server.URL+"/_/api/settings/server", serverSettingsJSON(form))
	if err != nil {
		t.Fatalf("POST settings failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Status = %d, want 400", resp.StatusCode)
	}
}

func TestSettingsAppearancePostSavesPrefsIndependentlyOfSystemConfig(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	form := url.Values{
		"font_ui":           {"helvetica"},
		"font_mono":         {"courier"},
		"show_tags_sidebar": {"on"},
	}
	resp, err := postJSON(t, client, server.URL+"/_/api/settings/appearance", appearanceJSON(form))
	if err != nil {
		t.Fatalf("POST appearance failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.StatusCode)
	}

	resp2, err := client.Get(server.URL + "/_/settings")
	if err != nil {
		t.Fatalf("GET /settings failed: %v", err)
	}
	defer func() {
		if err := resp2.Body.Close(); err != nil {
			t.Errorf("closing settings response body: %v", err)
		}
	}()
	body, _ := io.ReadAll(resp2.Body)
	if !bytes.Contains(body, []byte(`value="helvetica" selected`)) {
		t.Errorf("saved UI font should be selected on reload, body: %s", body)
	}
	if !bytes.Contains(body, []byte(`value="courier" selected`)) {
		t.Errorf("saved mono font should be selected on reload, body: %s", body)
	}
}

func TestSettingsAppearancePostRejectsUnknownFont(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	form := url.Values{"font_ui": {"comic-sans"}}
	resp, err := postJSON(t, client, server.URL+"/_/api/settings/appearance", appearanceJSON(form))
	if err != nil {
		t.Fatalf("POST appearance failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Status = %d, want 400", resp.StatusCode)
	}
}

func TestRerunSetupShowsModalEvenWhenFilesExist(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/_/settings")
	if err != nil {
		t.Fatalf("GET /settings failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	if bytes.Contains(body, []byte("Set up wiki")) {
		t.Errorf("setup modal should not appear before re-run is clicked, body: %s", body)
	}

	resp2, err := postJSON(t, client, server.URL+"/_/api/settings/setup", emptyJSON(url.Values{}))
	if err != nil {
		t.Fatalf("POST /_/api/settings/setup failed: %v", err)
	}
	defer func() {
		if err := resp2.Body.Close(); err != nil {
			t.Errorf("closing setup response body: %v", err)
		}
	}()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("re-run setup status = %d, want 200", resp2.StatusCode)
	}

	resp3, err := client.Get(server.URL + "/_/settings")
	if err != nil {
		t.Fatalf("GET /settings after re-run failed: %v", err)
	}
	defer func() {
		if err := resp3.Body.Close(); err != nil {
			t.Errorf("closing settings response body: %v", err)
		}
	}()
	body3, _ := io.ReadAll(resp3.Body)
	if !bytes.Contains(body3, []byte("Set up wiki")) {
		t.Errorf("setup modal should appear after re-run setup even though files exist, body: %s", body3)
	}
	if !bytes.Contains(body3, []byte(`name="add_namespace"`)) || !bytes.Contains(body3, []byte(`name="add_help"`)) {
		t.Errorf("re-run modal should offer both the namespace and help items, body: %s", body3)
	}

	resp4, err := postJSON(t, client, server.URL+"/_/api/setup", setupJSON(url.Values{"action": {"skip"}}))
	if err != nil {
		t.Fatalf("POST /setup skip failed: %v", err)
	}
	defer func() {
		if err := resp4.Body.Close(); err != nil {
			t.Errorf("closing setup response body: %v", err)
		}
	}()

	resp5, err := client.Get(server.URL + "/_/settings")
	if err != nil {
		t.Fatalf("GET /settings after skip failed: %v", err)
	}
	defer func() {
		if err := resp5.Body.Close(); err != nil {
			t.Errorf("closing settings response body: %v", err)
		}
	}()
	body5, _ := io.ReadAll(resp5.Body)
	if bytes.Contains(body5, []byte("Set up wiki")) {
		t.Errorf("setup modal should not reappear after skip, body: %s", body5)
	}
}

func TestSettingsFullFlow(t *testing.T) {
	dir := t.TempDir()
	cfgFile := dir + "/config.yaml"
	yamlContent := "bind: \":7000\"\ngit:\n  user: test\n"
	if err := os.WriteFile(cfgFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	t.Setenv("HMD_CONFIG_FILE", cfgFile)

	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/_/admin")
	if err != nil {
		t.Fatalf("GET /admin failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	if !bytes.Contains(body, []byte(":7000")) {
		t.Errorf("GET should show original bind value")
	}

	form := url.Values{
		"bind":             {":7001"},
		"repo_dir":         {"/data/repo"},
		"remote_url":       {""},
		"git_user":         {"test"},
		"git_token":        {""},
		"max_upload_bytes": {"10485760"},
		"sync_poll_ms":     {"10000"},
		"sync_mode":        {"push"},
	}
	resp2, err := postJSON(t, client, server.URL+"/_/api/settings/server", serverSettingsJSON(form))
	if err != nil {
		t.Fatalf("POST settings failed: %v", err)
	}
	if err := resp2.Body.Close(); err != nil {
		t.Fatalf("closing settings response body: %v", err)
	}
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("POST Status = %d, want 200", resp2.StatusCode)
	}

	resp3, err := client.Get(server.URL + "/_/admin?saved=1")
	if err != nil {
		t.Fatalf("GET /admin?saved=1 failed: %v", err)
	}
	body3, _ := io.ReadAll(resp3.Body)
	if err := resp3.Body.Close(); err != nil {
		t.Fatalf("closing saved settings response body: %v", err)
	}
	if !bytes.Contains(body3, []byte(":7001")) {
		t.Errorf("After save, should show updated bind value")
	}

	loaded, err := config.LoadFileConfig(cfgFile)
	if err != nil {
		t.Fatalf("LoadFileConfig failed: %v", err)
	}
	if loaded.Bind != ":7001" {
		t.Errorf("File Bind = %q, want %q", loaded.Bind, ":7001")
	}
}

func TestSettingsExportBakesInEnvValues(t *testing.T) {
	dir := t.TempDir()
	appDir := dir + "/app"
	cfgFile := appDir + "/config.yaml"
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatalf("mkdir appDir: %v", err)
	}
	yamlContent := ""
	if err := os.WriteFile(cfgFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	t.Setenv("HMD_CONFIG_FILE", cfgFile)
	t.Setenv("HMD_SYNC_MODE", "bidirectional")

	server, client := newTestApp(t)
	defer server.Close()

	resp, err := postJSON(t, client, server.URL+"/_/api/settings/export", emptyJSON(url.Values{}))
	if err != nil {
		t.Fatalf("POST /_/api/settings/export failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.StatusCode)
	}

	loaded, err := config.LoadFileConfig(cfgFile)
	if err != nil {
		t.Fatalf("LoadFileConfig failed: %v", err)
	}
	if loaded.SyncMode != "bidirectional" {
		t.Errorf("SyncMode in file = %q, want %q (exported from HMD_SYNC_MODE)", loaded.SyncMode, "bidirectional")
	}
}

func TestConfigExportOmitsResolvedTokenWhenTokenFileSet(t *testing.T) {
	dir := t.TempDir()
	tokenFile := dir + "/token.txt"
	if err := os.WriteFile(tokenFile, []byte("secret-from-file\n"), 0644); err != nil {
		t.Fatalf("failed to write token file: %v", err)
	}

	cfg := config.Config{
		Git: config.GitConfig{TokenFile: tokenFile, Token: "secret-from-file"},
	}

	fc := cfg.ToFileConfig()
	if fc.Git.Token != "" {
		t.Errorf("Git.Token = %q, want empty (resolved-from-file secret should not be exported in plaintext)", fc.Git.Token)
	}
	if fc.Git.TokenFile != tokenFile {
		t.Errorf("Git.TokenFile = %q, want %q", fc.Git.TokenFile, tokenFile)
	}
}

// injectTOC builds a browser App over the given index so table-of-contents
// tests exercise the application api path used in production.
func injectTOC(body string, ix *search.Index, indexSlug, ns string) string {
	ctx := auth.WithTokenPrincipal(context.Background(), auth.TokenPrincipal{User: "tester", Scopes: []string{"read"}})
	return (&App{API: api.New(nil, ix, nil)}).injectTOC(ctx, body, indexSlug, ns)
}

func TestInjectTOC(t *testing.T) {
	pages := []wiki.Page{
		{Slug: "notes/readme", Title: "Home"},
		{Slug: "notes/alpha", Title: "Alpha"},
		{Slug: "notes/beta", Title: "Beta"},
		{Slug: "notes/gamma", Title: "Gamma", Tags: []string{"meta"}},
		{Slug: "notes/delta", Title: "Delta", Tags: []string{"meta"}},
		{Slug: "notes/epsilon", Title: "Epsilon", Tags: []string{"guide"}},
	}
	ix, _ := search.BuildIndex(pages)
	const index = "notes/readme"

	t.Run("all pages token excludes the namespace index", func(t *testing.T) {
		out := injectTOC("head\n\n<!-- hmd:toc -->\ntail", ix, index, "notes")
		want := "head\n\n- [[Alpha]]\n- [[Beta]]\n- [[Delta]]\n- [[Epsilon]]\n- [[Gamma]]\n\ntail"
		if out != want {
			t.Errorf("injectTOC all = %q, want %q", out, want)
		}
	})

	t.Run("tag filter OR semantics", func(t *testing.T) {
		out := injectTOC("<!-- hmd:toc:meta,guide -->", ix, index, "notes")

		want := "- [[Delta]]\n- [[Epsilon]]\n- [[Gamma]]\n"
		if out != want {
			t.Errorf("injectTOC tag = %q, want %q", out, want)
		}
	})

	t.Run("no token unchanged", func(t *testing.T) {
		body := "just some markdown, no token here"
		if got := injectTOC(body, ix, index, "notes"); got != body {
			t.Errorf("injectTOC should be a no-op when no token present, got %q", got)
		}
	})

	t.Run("multiple tokens", func(t *testing.T) {
		out := injectTOC("A: <!-- hmd:toc:meta -->\nB: <!-- hmd:toc:guide -->", ix, index, "notes")
		want := "A: - [[Delta]]\n- [[Gamma]]\n\nB: - [[Epsilon]]\n"
		if out != want {
			t.Errorf("injectTOC multiple = %q, want %q", out, want)
		}
	})

	t.Run("empty index", func(t *testing.T) {
		empty, _ := search.BuildIndex(nil)
		out := injectTOC("<!-- hmd:toc -->", empty, index, "notes")
		if out != "" {
			t.Errorf("injectTOC on empty index = %q, want empty", out)
		}
	})

	t.Run("scoped to namespace, excludes other namespaces", func(t *testing.T) {
		nsPages := []wiki.Page{
			{Slug: "blog/readme", Title: "Blog Home"},
			{Slug: "blog/alpha", Title: "Blog Alpha"},
			{Slug: "blog/beta", Title: "Blog Beta", Tags: []string{"meta"}},
			{Slug: "notes/2026-07-31", Title: "Notes Entry", Tags: []string{"meta"}},
		}
		nsIx, _ := search.BuildIndex(nsPages)

		out := injectTOC("<!-- hmd:toc -->", nsIx, "blog/readme", "blog")
		want := "- [[Blog Alpha]]\n- [[Blog Beta]]\n"
		if out != want {
			t.Errorf("injectTOC ns=blog all = %q, want %q", out, want)
		}

		out = injectTOC("<!-- hmd:toc:meta -->", nsIx, "blog/readme", "blog")
		want = "- [[Blog Beta]]\n"
		if out != want {
			t.Errorf("injectTOC ns=blog tag=meta = %q, want %q", out, want)
		}

		out = injectTOC("<!-- hmd:toc -->", nsIx, "notes/readme", "notes")
		want = "- [[Notes Entry]]\n"
		if out != want {
			t.Errorf("injectTOC ns=notes = %q, want %q", out, want)
		}
	})
}

func TestTOCRenderedOnHome(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := postPageSave(t, client, server, testNS+"/alpha", url.Values{
		"title":    {"Alpha"},
		"body":     {"Alpha body"},
		"tags":     {"meta"},
		"basehash": {""},
	})
	if err != nil {
		t.Fatalf("save alpha failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	resp, err = client.Get(server.URL + "/" + testHome)
	if err != nil {
		t.Fatalf("GET /page/readme failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)

	if !bytes.Contains(body, []byte(`href="/`+testNS+`/alpha"`)) {
		t.Errorf("index page should contain a TOC link to /page/alpha, body: %s", body)
	}
	if bytes.Contains(body, []byte("hmd:toc")) {
		t.Error("raw hmd:toc token should not appear in rendered HTML")
	}

	resp, err = postPageSave(t, client, server, testNS+"/toc-test", url.Values{
		"title":    {"TOC Test"},
		"body":     {"Pages:\n\n<!-- hmd:toc:meta -->\n"},
		"tags":     {""},
		"basehash": {""},
	})
	if err != nil {
		t.Fatalf("save toc-test failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	resp, err = client.Get(server.URL + "/" + testNS + "/toc-test")
	if err != nil {
		t.Fatalf("GET /page/toc-test failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ = io.ReadAll(resp.Body)

	if !bytes.Contains(body, []byte(`href="/`+testNS+`/alpha"`)) {
		t.Errorf("tag-filtered TOC should link alpha (tagged meta), body: %s", body)
	}
}

func TestHelpNotInSearchButInPalette(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/_/search?q=frontmatter")
	if err != nil {
		t.Fatalf("GET /search failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	if bytes.Contains(body, []byte(`href="/help"`)) {
		t.Errorf("help should not appear in search results, body: %s", body)
	}

	resp2, err := client.Get(server.URL + "/_/static/app.js")
	if err != nil {
		t.Fatalf("GET /_/static/app.js failed: %v", err)
	}
	defer func() {
		if err := resp2.Body.Close(); err != nil {
			t.Errorf("closing app.js response body: %v", err)
		}
	}()
	body2, _ := io.ReadAll(resp2.Body)
	if !bytes.Contains(body2, []byte(`href="/_/hidden"`)) || !bytes.Contains(body2, []byte(`:hidden:`)) {
		t.Errorf("app.js should contain a :hidden: palette row linking to /hidden")
	}
}

func TestHelpAtHiddenRoute(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/_/hidden/help")
	if err != nil {
		t.Fatalf("GET /_/hidden/help failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("Frontmatter")) {
		t.Errorf("hidden help page should contain formatting conventions, body: %s", body)
	}

	resp2, err := client.Get(server.URL + "/help")
	if err != nil {
		t.Fatalf("GET /page/help failed: %v", err)
	}
	defer func() {
		if err := resp2.Body.Close(); err != nil {
			t.Errorf("closing page response body: %v", err)
		}
	}()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("GET /page/help should be 404, got %d", resp2.StatusCode)
	}

	resp3, err := client.Get(server.URL + "/_/hidden")
	if err != nil {
		t.Fatalf("GET /hidden failed: %v", err)
	}
	defer func() {
		if err := resp3.Body.Close(); err != nil {
			t.Errorf("closing hidden response body: %v", err)
		}
	}()
	body3, _ := io.ReadAll(resp3.Body)
	if !bytes.Contains(body3, []byte(`href="/_/hidden/help"`)) {
		t.Errorf("/_/hidden should list help, body: %s", body3)
	}
}

func TestSetupInterstitialOnExistingRepo(t *testing.T) {
	repoDir := t.TempDir()
	appDir := t.TempDir()

	gitRepo, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	wt, err := gitRepo.Worktree()
	if err != nil {
		t.Fatalf("worktree failed: %v", err)
	}
	existingPage := filepath.Join(repoDir, "existing-page.md")
	if err := os.WriteFile(existingPage, []byte("# Existing\n"), 0644); err != nil {
		t.Fatalf("writing existing page: %v", err)
	}
	if _, err := wt.Add("existing-page.md"); err != nil {
		t.Fatalf("adding existing page: %v", err)
	}
	if _, err := wt.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@hmd.local", When: time.Now()},
	}); err != nil {
		t.Fatalf("initial commit failed: %v", err)
	}

	cfg := config.Config{
		RepoDir:   repoDir,
		AppDir:    appDir,
		Git:       config.GitConfig{User: "test"},
		AdminUser: "admin",
		AdminPass: "password12345",
	}

	repo, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	pages, _ := repo.List()
	var pageObjs []wiki.Page
	for _, p := range pages {
		content, _, _ := repo.Read(p)
		pageObjs = append(pageObjs, wiki.ParsePage(p[:len(p)-3], content))
	}
	index, _ := search.BuildIndex(pageObjs)
	authn, _ := OpenAuth(cfg)
	renderer := wiki.NewRenderer(index.ResolveLink)
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates failed: %v", err)
	}
	namespaces, err := api.BuildNamespaceRegistry(repoDir)
	if err != nil {
		t.Fatalf("api.BuildNamespaceRegistry failed: %v", err)
	}

	env := &testApp{
		App:   &App{API: api.New(repo, index, authn), Auth: authn, Render: renderer, Tmpl: tmpl},
		Store: repo,
		Index: index,
	}
	env.SetConfig(cfg)
	env.SetNamespaces(namespaces)

	server := httptest.NewServer(testHandler(env.App))
	defer server.Close()

	jar, _ := cookiejar.New(&cookiejar.Options{})
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	loginForm := url.Values{"username": {"admin"}, "password": {"password12345"}}
	req, _ := http.NewRequest("POST", server.URL+"/_/login", bytes.NewBufferString(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := client.Do(req); err != nil {
		t.Fatalf("login request failed: %v", err)
	}

	resp, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("root should redirect, got status %d", resp.StatusCode)
	}

	resp2, err := client.Get(server.URL + "/" + testHome)
	if err != nil {
		t.Fatalf("GET /page/readme failed: %v", err)
	}
	defer func() {
		if err := resp2.Body.Close(); err != nil {
			t.Errorf("closing home response body: %v", err)
		}
	}()
	body2, _ := io.ReadAll(resp2.Body)
	if !bytes.Contains(body2, []byte("Set up wiki")) {
		t.Errorf("setup modal should appear when NeedsSetup, body: %s", body2)
	}

	resp3, err := postJSON(t, client, server.URL+"/_/api/setup", setupJSON(url.Values{
		"action":            {"add"},
		"setup_wiki":        {"on"},
		"site_name":         {wiki.DefaultSiteName},
		"default_namespace": {api.NewSetupNamespaceOption},
		"new_namespace":     {testNS},
	}))
	if err != nil {
		t.Fatalf("POST /setup failed: %v", err)
	}
	defer func() {
		if err := resp3.Body.Close(); err != nil {
			t.Errorf("closing setup response body: %v", err)
		}
	}()
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("setup status = %d, want 200", resp3.StatusCode)
	}

	resp4, err := client.Get(server.URL + "/" + testHome)
	if err != nil {
		t.Fatalf("GET /page/readme failed: %v", err)
	}
	defer func() {
		if err := resp4.Body.Close(); err != nil {
			t.Errorf("closing home response body: %v", err)
		}
	}()
	if resp4.StatusCode != http.StatusOK {
		t.Errorf("home page status = %d, want 200", resp4.StatusCode)
	}
	body4, _ := io.ReadAll(resp4.Body)
	if !bytes.Contains(body4, []byte("Welcome")) {
		t.Errorf("home page should contain 'Welcome', body: %s", body4)
	}

	resp5, err := client.Get(server.URL + "/" + testHome)
	if err != nil {
		t.Fatalf("GET /page/readme after setup failed: %v", err)
	}
	defer func() {
		if err := resp5.Body.Close(); err != nil {
			t.Errorf("closing home response body: %v", err)
		}
	}()
	body5, _ := io.ReadAll(resp5.Body)
	if bytes.Contains(body5, []byte("setup-modal-backdrop")) {
		t.Errorf("setup modal should not appear after setup, body: %s", body5)
	}

	if landing := env.wikiConfig().Landing; landing != testNS+"/" {
		t.Errorf("landing = %q, want %s/", landing, testNS)
	}
	if _, _, err := env.Store.Read(wiki.ConfigFile); err != nil {
		t.Errorf("%s should be seeded: %v", wiki.ConfigFile, err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "readme.md")); err != nil {
		t.Errorf("root readme.md should be seeded for the git host front page: %v", err)
	}
}

func TestSetupChoosesDetectedNamespaceForWikiConfig(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	if err := app.Store.Remove(wiki.ConfigFile, "Remove wiki config", "test", "test@hmd.local"); err != nil {
		t.Fatalf("removing %s: %v", wiki.ConfigFile, err)
	}
	app.Store.NeedsSetup.Store(true)

	resp, err := client.Get(server.URL + "/" + testHome)
	if err != nil {
		t.Fatalf("GET setup page: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)
	for _, want := range []string{`name="default_namespace"`, `<option value="notes">notes/</option>`, `name="new_namespace"`, `name="site_name"`} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("setup should offer %q, body: %s", want, body)
		}
	}

	resp, err = postJSON(t, client, server.URL+"/_/api/setup", setupJSON(url.Values{
		"action":            {"add"},
		"setup_wiki":        {"on"},
		"site_name":         {"Detected Wiki"},
		"default_namespace": {testNS},
	}))
	if err != nil {
		t.Fatalf("POST setup: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("setup status = %d, want 200", resp.StatusCode)
	}
	if got := app.wikiConfig(); got != (wiki.WikiConfig{Landing: testNS + "/", SiteName: "Detected Wiki"}) {
		t.Errorf("wiki config = %#v", got)
	}
}

// TestNamespaceIndexExcludedFromItsOwnTOC verifies index exclusion from its TOC.
func TestNamespaceIndexExcludedFromItsOwnTOC(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	savePage(t, app, testNS+"/alpha", "Alpha body")

	resp, err := client.Get(server.URL + "/" + testNS + "/")
	if err != nil {
		t.Fatalf("GET /%s/: %v", testNS, err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)

	if !bytes.Contains(body, []byte(`<a class="wiki" href="/`+testNS+`/alpha"`)) {
		t.Errorf("index TOC should link the sibling page, body: %s", body)
	}
	if bytes.Contains(body, []byte(`<a class="wiki" href="/`+testHome+`"`)) {
		t.Errorf("index TOC must not link the index page itself, body: %s", body)
	}
}
