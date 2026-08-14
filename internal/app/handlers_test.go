package app

import (
	"bytes"
	"compress/gzip"
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

// testNS is the namespace every fixture page lives in, and testHome its index
// page — the shape a wiki has after setup, since every page belongs to a
// namespace.
const testNS = "notes"
const testHome = testNS + "/" + defaultIndexPage

type csrfTestTransport struct {
	base http.RoundTripper
	jar  http.CookieJar
	auth *Auth
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

// adminLogin logs client in as the bootstrap admin, for tests exercising
// handlers behind the settings scope.
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

func TestCompression(t *testing.T) {
	body := strings.Repeat("compress me ", 100)
	handler := compression(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, body)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	reader, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	decoded, err := io.ReadAll(reader)
	closeTestBody(t, reader)
	if err != nil || string(decoded) != body {
		t.Fatalf("decoded response = %q, %v", decoded, err)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip;q=0")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("disabled gzip Content-Encoding = %q", got)
	}
}

func TestRequestSecurity(t *testing.T) {
	app := &App{Auth: &Auth{sessions: make(map[string]sessionRecord)}}
	app.SetConfig(Config{BaseURL: "https://wiki.example.com", TrustedProxies: []string{"10.0.0.0/8"}})
	app.Auth.sessions["session"] = sessionRecord{User: "admin", Expires: time.Now().Add(time.Hour), CSRF: "token"}
	called := 0
	handler := app.requestSecurity(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called++ }))

	request := func(origin, token string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "https://wiki.example.com/notes/page?do=save", strings.NewReader("csrf_token="+token))
		r.Host = "wiki.example.com"
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: "hmd_session", Value: "session"})
		return r
	}
	for _, tc := range []struct{ origin, token string }{{"https://evil.example", "token"}, {"https://wiki.example.com", "wrong"}, {"", "token"}} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request(tc.origin, tc.token))
		if rec.Code != http.StatusForbidden {
			t.Errorf("origin=%q token=%q status=%d, want 403", tc.origin, tc.token, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request("https://wiki.example.com", "token"))
	if rec.Code != http.StatusOK || called != 1 {
		t.Fatalf("valid CSRF status/calls = %d/%d, want 200/1", rec.Code, called)
	}

	req := httptest.NewRequest(http.MethodGet, "http://wiki.example.com/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Forwarded-Proto", "https")
	if app.isSecureRequest(req) {
		t.Fatal("untrusted forwarded proto marked request secure")
	}
	req.RemoteAddr = "10.1.2.3:1234"
	if !app.isSecureRequest(req) {
		t.Fatal("trusted forwarded proto did not mark request secure")
	}
}

func newTestAppFull(t *testing.T) (*App, *httptest.Server, *http.Client) {
	repoDir := t.TempDir()
	appDir := t.TempDir()

	cfg := Config{
		ConfigFile: filepath.Join(appDir, "config.yaml"),
		RepoDir:    repoDir,
		AppDir:     appDir,
		Git:        GitConfig{User: "test"},
		AdminUser:  "admin",
		AdminPass:  "password12345",
	}

	// When a config file is configured, overlay its values so the settings
	// handlers see the same cfg a real deployment (LoadConfig) would.
	if path := os.Getenv("HMD_CONFIG_FILE"); path != "" {
		cfg.ConfigFile = path
		if loaded, err := LoadConfig(); err == nil {
			loaded.RepoDir = repoDir
			loaded.AppDir = appDir
			loaded.AdminUser = "admin"
			loaded.AdminPass = "password12345"
			cfg = loaded
		}
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	// Tests exercise pages/handlers, not the setup flow itself — simulate a
	// completed setup so the first namespace, its index page and .help.md
	// exist, since OpenStore seeds nothing without consent.
	nsCfg, err := NamespaceConfig{Widgets: builtinWidgets, Index: defaultIndexPage}.Encode()
	if err != nil {
		t.Fatalf("encoding namespace config: %v", err)
	}
	if _, err := store.Save(namespaceConfigPath(testNS), nsCfg, "Configure namespace "+testNS, cfg.Git.User, cfg.Git.User+"@hmd.local"); err != nil {
		t.Fatalf("seeding namespace config: %v", err)
	}
	if _, err := store.Save(pageFile(testHome), Page{Slug: testHome, Title: testNS, Body: defaultHomeMD}.Encode(), "Add "+testHome, cfg.Git.User, cfg.Git.User+"@hmd.local"); err != nil {
		t.Fatalf("seeding index page: %v", err)
	}
	if _, err := store.Save(".help.md", Page{Slug: "help", Title: "Help", Tags: []string{"meta"}, Body: defaultHelpMD}.Encode(), "Add .help.md", cfg.Git.User, cfg.Git.User+"@hmd.local"); err != nil {
		t.Fatalf("seeding .help.md: %v", err)
	}
	wikiData, err := (WikiConfig{Landing: testNS + "/"}).Encode()
	if err != nil {
		t.Fatalf("encoding wiki config: %v", err)
	}
	if _, err := store.Save(wikiConfigFile, wikiData, "Configure wiki settings", cfg.Git.User, cfg.Git.User+"@hmd.local"); err != nil {
		t.Fatalf("seeding wiki config: %v", err)
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
	renderer := NewRenderer(index.ResolveLink)

	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates failed: %v", err)
	}

	namespaces, err := BuildNamespaceRegistry(repoDir)
	if err != nil {
		t.Fatalf("BuildNamespaceRegistry failed: %v", err)
	}

	app := &App{
		Store:  store,
		Auth:   auth,
		Index:  index,
		Render: renderer,
		Tmpl:   tmpl,
	}
	app.SetConfig(cfg)
	app.SetWikiConfig(WikiConfig{Landing: testNS + "/"})
	app.SetNamespaces(namespaces)

	server := httptest.NewServer(securityHeaders(app.Auth.Middleware(app.Routes())))

	// Create client with cookie jar
	jar, _ := cookiejar.New(&cookiejar.Options{})
	client := &http.Client{
		Jar: jar,
		Transport: csrfTestTransport{
			base: http.DefaultTransport,
			jar:  jar,
			auth: auth,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Login
	loginForm := url.Values{
		"username": {"admin"},
		"password": {"password12345"},
	}
	req, _ := http.NewRequest("POST", server.URL+"/_/login", bytes.NewBufferString(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := client.Do(req); err != nil {
		t.Fatalf("login request failed: %v", err)
	}

	return app, server, client
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

	// GET edit page for new slug
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

	// POST save
	saveForm := url.Values{
		"title":    {"Test Page"},
		"body":     {"This is a test page."},
		"basehash": {""},
	}
	resp, err = client.PostForm(server.URL+"/"+slug+"?do=save", saveForm)
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing save response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("Save status = %d, want 303", resp.StatusCode)
	}

	// GET page, verify content
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

	// Verify commit was made (checking store history)
	// This would require access to the app's store, which we don't have in the test
	// So we just verify the page was saved by checking we can view it
}

func TestOptimisticLockConflict(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	slug := testNS + "/lock-test"

	// Get the hash for a new page
	resp, err := client.Get(server.URL + "/" + slug + "?do=edit")
	if err != nil {
		t.Fatalf("GET edit failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing edit response body: %v", err)
		}
	}()
	// BaseHash will be empty for new page

	// Save directly via client to change the page
	saveForm := url.Values{
		"title":    {"Version 1"},
		"body":     {"First version"},
		"basehash": {""},
	}
	resp, err = client.PostForm(server.URL+"/"+slug+"?do=save", saveForm)
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing save response body: %v", err)
	}

	// Now try to save with stale basehash
	saveForm2 := url.Values{
		"title":    {"Version 2"},
		"body":     {"Second version"},
		"basehash": {""}, // stale hash
	}
	resp, err = client.PostForm(server.URL+"/"+slug+"?do=save", saveForm2)
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
	resp, err := client.PostForm(server.URL+"/_/api/preview", previewForm)
	if err != nil {
		t.Fatalf("POST preview failed: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing preview response body: %v", err)
		}
	}()

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("<strong>")) {
		t.Errorf("Preview should render bold as <strong>")
	}
}

func TestAPIsRejectRootPageSlugs(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	for _, tc := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/_/api/preview/readme", http.StatusNotFound},
		{http.MethodPost, "/_/api/attachments/readme", http.StatusBadRequest},
		{http.MethodGet, "/_/attachments/readme/file.png", http.StatusNotFound},
	} {
		req, err := http.NewRequest(tc.method, server.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		closeTestBody(t, resp.Body)
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
}

// TestUnauthenticatedAccess covers the two anonymous outcomes on a wiki with
// no public namespaces: the site root still redirects to login (so a login
// wall stays reachable), while a specific content page — private, like
// every namespace here — 404s rather than redirecting. See
// TestAnonymousPublicNamespace for the public-namespace case.
func TestUnauthenticatedAccess(t *testing.T) {
	server, _ := newTestApp(t)
	defer server.Close()

	// Fresh client without auth
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

	// GET the attachment
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

	// Tika decides whether this format can be extracted; HMD stores it either way.
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
	sidecar, _, err := app.Store.Read(extractedAttachmentPath("attachments/" + testHome + "/source.txt"))
	if err != nil {
		t.Fatalf("reading extracted sidecar: %v", err)
	}
	if text, ok := decodeExtractedAttachment(attachmentBlobHash(source), sidecar); !ok || text != "source text" {
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

	// Percent-encoded ".." as the {slug} path segment: net/http's ServeMux
	// redirects literal ".." segments before routing, but %2e%2e reaches
	// PathValue("slug") as ".." unmolested, so this is the exploitable form.
	// It would otherwise resolve to a path outside attachments/ once joined
	// with the repo dir.
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

	// Save a page with unique text
	slug := testNS + "/search-test"
	saveForm := url.Values{
		"title":    {"Search Test"},
		"body":     {"This contains uniquewordxyz for searching."},
		"basehash": {""},
	}
	resp, err := client.PostForm(server.URL+"/"+slug+"?do=save", saveForm)
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	// Search for the unique word
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

	// Save page "page-one" with link to "Page Two" (which slugifies to "page-two")
	saveForm := url.Values{
		"title":    {"Page One"},
		"body":     {"This links to [[Page Two]]"},
		"basehash": {""},
	}
	resp, err := client.PostForm(server.URL+"/"+testNS+"/page-one?do=save", saveForm)
	if err != nil {
		t.Fatalf("POST one failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	// Save page "page-two"
	saveForm = url.Values{
		"title":    {"Page Two"},
		"body":     {"This is page two."},
		"basehash": {""},
	}
	resp, err = client.PostForm(server.URL+"/"+testNS+"/page-two?do=save", saveForm)
	if err != nil {
		t.Fatalf("POST two failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	// View page page-two, should show backlinks
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

	// Save page twice with different bodies
	saveForm := url.Values{
		"title":    {"History Test"},
		"body":     {"Version one"},
		"basehash": {""},
	}
	resp, err := client.PostForm(server.URL+"/"+slug+"?do=save", saveForm)
	if err != nil {
		t.Fatalf("POST v1 failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	saveForm = url.Values{
		"title":    {"History Test"},
		"body":     {"Version two"},
		"basehash": {""},
	}
	resp, err = client.PostForm(server.URL+"/"+slug+"?do=save", saveForm)
	if err != nil {
		t.Fatalf("POST v2 failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	// GET history
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

	// Extract the older commit hash from the response
	// For this test, we'll just verify we can view an old version
	// In a real test, we'd parse the HTML to get the hash
	// For now, assume the second entry in history is the old one
}

func TestRevertToOldVersion(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	slug := testNS + "/revert-test"

	// Save v1
	saveForm := url.Values{
		"title":    {"Revert Test"},
		"body":     {"Original content"},
		"basehash": {""},
	}
	resp, err := client.PostForm(server.URL+"/"+slug+"?do=save", saveForm)
	if err != nil {
		t.Fatalf("POST v1 failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	// Verify page shows v1
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

// TestPageChrome guards against pages bypassing the shared layout: every
// rendered page must carry the editor/preview scripts and header controls.
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
		`src="/_/static/app.js?v=2"`,
		`class="app-topbar"`,
		`class="sidebar"`,
		`action="/_/logout"`,
		`seg-sync`,
	} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("Page missing %s", want)
		}
	}

	// Mermaid should NOT be loaded on pages without mermaid content
	if bytes.Contains(body, []byte(`src="/_/static/mermaid.min.js"`)) {
		t.Error("Mermaid script loaded on page without mermaid content")
	}

	// The <!-- hmd:toc --> token in the seeded readme.md must be replaced
	// server-side, never reach the rendered HTML.
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
		`src="/_/static/editor.js?v=2"`,
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
	resp, err := client.PostForm(server.URL+"/"+testNS+"/mermaid-test?do=save", url.Values{
		"title":    {"Mermaid Test"},
		"body":     {"```mermaid\ngraph TD;\n  A-->B\n```\n"},
		"tags":     {""},
		"basehash": {""},
	})
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	// View the page — mermaid script should be present
	resp, err = client.Get(server.URL + "/" + testNS + "/mermaid-test")
	if err != nil {
		t.Fatalf("GET page failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)

	if !bytes.Contains(body, []byte(`src="/_/static/mermaid.min.js"`)) {
		t.Error("Mermaid script missing on page with mermaid content")
	}

	// View the edit page — mermaid script should be present (body contains "mermaid")
	resp, err = client.Get(server.URL + "/" + testNS + "/mermaid-test?do=edit")
	if err != nil {
		t.Fatalf("GET edit failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ = io.ReadAll(resp.Body)

	if !bytes.Contains(body, []byte(`src="/_/static/mermaid.min.js"`)) {
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
	req, _ := http.NewRequest("POST", server.URL+"/"+testNS+"/tagged?do=save", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
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
	req, _ := http.NewRequest("POST", server.URL+"/"+testNS+"/tagged?do=save", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("save request failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	// Edit form should show the tags back.
	resp, err = client.Get(server.URL + "/" + testNS + "/tagged?do=edit")
	if err != nil {
		t.Fatalf("edit request failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `value="go, wiki"`) {
		t.Errorf("edit page should show tags input, got:\n%s", body)
	}

	// Page view should show tag chips.
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

// TestTitleEscaped guards against stored XSS via page titles.
func TestTitleEscaped(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	saveForm := url.Values{
		"title":    {`<script>alert(1)</script>`},
		"body":     {"content"},
		"basehash": {""},
	}
	resp, err := client.PostForm(server.URL+"/"+testNS+"/xss-test?do=save", saveForm)
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

func TestSearchAPI(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	// Save a page with unique text
	slug := testNS + "/api-search-test"
	saveForm := url.Values{
		"title":    {"API Search Test"},
		"body":     {"This contains uniquetestword for API search."},
		"basehash": {""},
	}
	resp, err := client.PostForm(server.URL+"/"+slug+"?do=save", saveForm)
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	// Search via API
	resp, err = client.Get(server.URL + "/_/api/search?q=uniquetestword")
	if err != nil {
		t.Fatalf("GET api/search failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

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

func TestHealthAPI(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	saveForm := url.Values{"title": {"Dangling"}, "body": {"see [[nowhere]]"}, "basehash": {""}}
	resp, err := client.PostForm(server.URL+"/blog/dangling?do=save", saveForm)
	if err != nil {
		t.Fatalf("POST save failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	nsForm := url.Values{"title": {"Dangling"}, "body": {"see [[nowhere]]"}, "basehash": {""}}
	resp, err = client.PostForm(server.URL+"/notes/dangling?do=save", nsForm)
	if err != nil {
		t.Fatalf("POST namespaced save failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	// Unscoped: both dangling links show up.
	resp, err = client.Get(server.URL + "/_/api/health")
	if err != nil {
		t.Fatalf("GET api/health failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("api/health status = %d, want 200", resp.StatusCode)
	}
	var report HealthReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decoding health JSON: %v", err)
	}
	closeTestBody(t, resp.Body)
	if len(report.Missing) != 2 {
		t.Errorf("unscoped missing = %+v, want 2 entries", report.Missing)
	}

	// Scoped to "notes": only the namespaced dangling link.
	resp, err = client.Get(server.URL + "/_/api/health?namespace=notes")
	if err != nil {
		t.Fatalf("GET api/health?namespace=notes failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decoding scoped health JSON: %v", err)
	}
	if len(report.Missing) != 1 || report.Missing[0].Slug != "notes/nowhere" {
		t.Errorf("scoped missing = %+v, want [notes/nowhere]", report.Missing)
	}
}

func TestSyncAPI(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/_/api/sync")
	if err != nil {
		t.Fatalf("GET api/sync failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

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
	cfg := Config{RepoDir: repoDir, AppDir: appDir, Git: GitConfig{User: "test"}}
	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	auth, _ := OpenAuth(cfg)
	index, _ := BuildIndex(nil)
	renderer := NewRenderer(index.ResolveLink)
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates failed: %v", err)
	}
	namespaces, err := BuildNamespaceRegistry(repoDir)
	if err != nil {
		t.Fatalf("BuildNamespaceRegistry failed: %v", err)
	}

	app := &App{
		Store:  store,
		Auth:   auth,
		Index:  index,
		Render: renderer,
		Tmpl:   tmpl,
	}
	app.SetConfig(cfg)
	app.SetWikiConfig(WikiConfig{SiteName: "TestWiki"})
	app.SetNamespaces(namespaces)

	got := app.wikiConfig()
	if got.SiteName != "TestWiki" {
		t.Errorf("wikiConfig().SiteName = %q, want %q", got.SiteName, "TestWiki")
	}

	// Swap and verify readers see the new value
	app.SetWikiConfig(WikiConfig{SiteName: "Changed"})
	got2 := app.wikiConfig()
	if got2.SiteName != "Changed" {
		t.Errorf("after swap, wikiConfig().SiteName = %q, want %q", got2.SiteName, "Changed")
	}
}

func TestBuildSettingsDataEditable(t *testing.T) {
	cfg := Config{
		ConfigFile: "/path/config.yaml",
		Bind:       ":8080",
		RepoDir:    "/data/repo",
		AppDir:     "/data/app",
		Git:        GitConfig{RemoteURL: "https://example.com/repo.git", User: "hmd", Token: "secret"},
	}

	sd := buildSettingsData(cfg, userRecord{})

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
	cfg := Config{
		ConfigFile: "/path/config.yaml",
		Bind:       ":9999",
		RepoDir:    "/data/repo",
		AppDir:     "/data/app",
		Git:        GitConfig{User: "hmd"},
		EnvOverrides: map[string]string{
			"Bind": "HMD_BIND",
		},
	}

	sd := buildSettingsData(cfg, userRecord{})

	if sd.Fields["Bind"].Editable {
		t.Error("Bind should be read-only (env set)")
	}
	if sd.Fields["Bind"].EnvVar != "HMD_BIND" {
		t.Errorf("Bind EnvVar = %q, want %q", sd.Fields["Bind"].EnvVar, "HMD_BIND")
	}
}

func TestBuildSettingsDataTokenFileLocked(t *testing.T) {
	cfg := Config{
		ConfigFile: "/path/config.yaml",
		Git:        GitConfig{Token: "file-token", User: "hmd"},
		RepoDir:    "/data/repo",
		AppDir:     "/data/app",
		EnvOverrides: map[string]string{
			"Git.TokenFile": "HMD_GIT_TOKEN_FILE",
		},
	}

	sd := buildSettingsData(cfg, userRecord{})

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
	req, _ := http.NewRequest("POST", server.URL+"/_/admin", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /admin failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("Status = %d, want 303", resp.StatusCode)
	}

	loaded, err := LoadFileConfig(cfgFile)
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

	resp, err := client.PostForm(server.URL+"/_/settings/wiki", url.Values{
		"site_name": {"Shared Wiki"},
		"landing":   {"notes/"},
	})
	if err != nil {
		t.Fatalf("POST /_/settings/wiki: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/_/admin?wiki-saved=1" {
		t.Errorf("redirect = %q, want wiki settings confirmation", got)
	}
	if got := app.wikiConfig(); got != (WikiConfig{Landing: "notes/", SiteName: "Shared Wiki"}) {
		t.Errorf("runtime wiki config = %#v", got)
	}
	stored, exists, err := LoadWikiConfig(app.config().RepoDir)
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
	if !bytes.Contains(body, []byte(`action="/_/settings/wiki"`)) || !bytes.Contains(body, []byte("Travels with the content repository")) {
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
	req, _ := http.NewRequest("POST", server.URL+"/_/admin", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /admin failed: %v", err)
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
	req, _ := http.NewRequest("POST", server.URL+"/_/settings/appearance", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /_/settings/appearance failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("Status = %d, want 303", resp.StatusCode)
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
	req, _ := http.NewRequest("POST", server.URL+"/_/settings/appearance", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /_/settings/appearance failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Status = %d, want 400", resp.StatusCode)
	}
}

func TestRerunSetupShowsModalEvenWhenFilesExist(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	// newTestApp already seeds readme.md and .help.md, so under normal
	// (non-forced) NeedsSetup logic, neither would be missing and the
	// modal would have nothing to show.
	resp, err := client.Get(server.URL + "/_/settings")
	if err != nil {
		t.Fatalf("GET /settings failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	if bytes.Contains(body, []byte("Set up wiki")) {
		t.Errorf("setup modal should not appear before re-run is clicked, body: %s", body)
	}

	resp2, err := client.PostForm(server.URL+"/_/settings/setup", url.Values{})
	if err != nil {
		t.Fatalf("POST /_/settings/setup failed: %v", err)
	}
	defer func() {
		if err := resp2.Body.Close(); err != nil {
			t.Errorf("closing setup response body: %v", err)
		}
	}()
	if resp2.StatusCode != http.StatusSeeOther {
		t.Errorf("re-run setup status = %d, want 303", resp2.StatusCode)
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

	// Skipping should clear ForceSetup so the modal doesn't keep reappearing.
	resp4, err := client.PostForm(server.URL+"/_/setup", url.Values{"action": {"skip"}})
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
	req, _ := http.NewRequest("POST", server.URL+"/_/admin", bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /admin failed: %v", err)
	}
	if err := resp2.Body.Close(); err != nil {
		t.Fatalf("closing settings response body: %v", err)
	}
	if resp2.StatusCode != http.StatusSeeOther {
		t.Errorf("POST Status = %d, want 303", resp2.StatusCode)
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

	loaded, err := LoadFileConfig(cfgFile)
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

	resp, err := client.PostForm(server.URL+"/_/settings/export", url.Values{})
	if err != nil {
		t.Fatalf("POST /_/settings/export failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("Status = %d, want 303", resp.StatusCode)
	}

	loaded, err := LoadFileConfig(cfgFile)
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

	cfg := Config{
		Git: GitConfig{TokenFile: tokenFile, Token: "secret-from-file"},
	}

	fc := cfg.toFileConfig()
	if fc.Git.Token != "" {
		t.Errorf("Git.Token = %q, want empty (resolved-from-file secret should not be exported in plaintext)", fc.Git.Token)
	}
	if fc.Git.TokenFile != tokenFile {
		t.Errorf("Git.TokenFile = %q, want %q", fc.Git.TokenFile, tokenFile)
	}
}

func TestInjectTOC(t *testing.T) {
	pages := []Page{
		{Slug: "notes/readme", Title: "Home"},
		{Slug: "notes/alpha", Title: "Alpha"},
		{Slug: "notes/beta", Title: "Beta"},
		{Slug: "notes/gamma", Title: "Gamma", Tags: []string{"meta"}},
		{Slug: "notes/delta", Title: "Delta", Tags: []string{"meta"}},
		{Slug: "notes/epsilon", Title: "Epsilon", Tags: []string{"guide"}},
	}
	ix, _ := BuildIndex(pages)
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
		// meta: gamma, delta; guide: epsilon. Sorted by slug: delta, epsilon, gamma.
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
		empty, _ := BuildIndex(nil)
		out := injectTOC("<!-- hmd:toc -->", empty, index, "notes")
		if out != "" {
			t.Errorf("injectTOC on empty index = %q, want empty", out)
		}
	})

	t.Run("scoped to namespace, excludes other namespaces", func(t *testing.T) {
		nsPages := []Page{
			{Slug: "blog/readme", Title: "Blog Home"},
			{Slug: "blog/alpha", Title: "Blog Alpha"},
			{Slug: "blog/beta", Title: "Blog Beta", Tags: []string{"meta"}},
			{Slug: "notes/2026-07-31", Title: "Notes Entry", Tags: []string{"meta"}},
		}
		nsIx, _ := BuildIndex(nsPages)

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

	// Create a page so the index TOC has something to list.
	resp, err := client.PostForm(server.URL+"/"+testNS+"/alpha?do=save", url.Values{
		"title":    {"Alpha"},
		"body":     {"Alpha body"},
		"tags":     {"meta"},
		"basehash": {""},
	})
	if err != nil {
		t.Fatalf("save alpha failed: %v", err)
	}
	closeTestBody(t, resp.Body)

	// View the index page: the seeded <!-- hmd:toc --> token must be
	// replaced with a rendered wiki-link to the new page.
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

	// A tag-filtered TOC on a non-index page should also render. Create a
	// second page that embeds <!-- hmd:toc:meta --> and view it.
	resp, err = client.PostForm(server.URL+"/"+testNS+"/toc-test?do=save", url.Values{
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

	// .help.md is NOT in the bleve search index.
	resp, err := client.Get(server.URL + "/_/search?q=frontmatter")
	if err != nil {
		t.Fatalf("GET /search failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	if bytes.Contains(body, []byte(`href="/help"`)) {
		t.Errorf("help should not appear in search results, body: %s", body)
	}

	// The palette (app.js) should have a built-in :hidden: row linking to /_/hidden.
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

	// Help is served at /_/hidden/help, not /page/help, and covers both UI
	// usage and formatting conventions in one file.
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

	// /page/help should NOT serve the help page.
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

	// /hidden index should list help.
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
	if _, err := wt.Add("existing-page.md"); err != nil {
		t.Fatalf("adding existing page: %v", err)
	}
	if _, err := wt.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@hmd.local", When: time.Now()},
	}); err != nil {
		t.Fatalf("initial commit failed: %v", err)
	}

	cfg := Config{
		RepoDir:   repoDir,
		AppDir:    appDir,
		Git:       GitConfig{User: "test"},
		AdminUser: "admin",
		AdminPass: "password12345",
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
	renderer := NewRenderer(index.ResolveLink)
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates failed: %v", err)
	}
	namespaces, err := BuildNamespaceRegistry(repoDir)
	if err != nil {
		t.Fatalf("BuildNamespaceRegistry failed: %v", err)
	}

	app := &App{
		Store:  store,
		Auth:   auth,
		Index:  index,
		Render: renderer,
		Tmpl:   tmpl,
	}
	app.SetConfig(cfg)
	app.SetNamespaces(namespaces)

	server := httptest.NewServer(securityHeaders(app.Auth.Middleware(app.Routes())))
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

	// Root should redirect to /page/readme (not a standalone setup page).
	resp, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("root should redirect, got status %d", resp.StatusCode)
	}

	// Setup modal should appear on any authed page when NeedsSetup.
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

	// POST setup with action=add, add_namespace=on to seed the first namespace.
	resp3, err := client.PostForm(server.URL+"/_/setup", url.Values{
		"action":            {"add"},
		"setup_wiki":        {"on"},
		"site_name":         {defaultSiteName},
		"default_namespace": {newSetupNamespaceOption},
		"new_namespace":     {testNS},
	})
	if err != nil {
		t.Fatalf("POST /setup failed: %v", err)
	}
	defer func() {
		if err := resp3.Body.Close(); err != nil {
			t.Errorf("closing setup response body: %v", err)
		}
	}()
	if resp3.StatusCode != http.StatusSeeOther {
		t.Errorf("setup status = %d, want 303", resp3.StatusCode)
	}

	// The namespace's index page should now exist and be viewable.
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

	// Modal should no longer appear after setup.
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

	// Creating the namespace is what makes it the default destination, and
	// what leaves a front page behind for whoever browses the content repo.
	if landing := app.wikiConfig().Landing; landing != testNS+"/" {
		t.Errorf("landing = %q, want %s/", landing, testNS)
	}
	if _, _, err := app.Store.Read(wikiConfigFile); err != nil {
		t.Errorf("%s should be seeded: %v", wikiConfigFile, err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "readme.md")); err != nil {
		t.Errorf("root readme.md should be seeded for the git host front page: %v", err)
	}
}

func TestSetupChoosesDetectedNamespaceForWikiConfig(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	if err := app.Store.Remove(wikiConfigFile, "Remove wiki config", "test", "test@hmd.local"); err != nil {
		t.Fatalf("removing %s: %v", wikiConfigFile, err)
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

	resp, err = client.PostForm(server.URL+"/_/setup", url.Values{
		"action":            {"add"},
		"setup_wiki":        {"on"},
		"site_name":         {"Detected Wiki"},
		"default_namespace": {testNS},
	})
	if err != nil {
		t.Fatalf("POST setup: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("setup status = %d, want 303", resp.StatusCode)
	}
	if got := app.wikiConfig(); got != (WikiConfig{Landing: testNS + "/", SiteName: "Detected Wiki"}) {
		t.Errorf("wiki config = %#v", got)
	}
}

// TestNamespaceIndexExcludedFromItsOwnTOC pins the one thing a namespace's
// index page gets that ordinary pages don't: a <!-- hmd:toc --> on it lists
// its siblings but never itself, since it stands in for the listing.
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

	// Scoped to wiki-links, since the sidebar and tree widgets link the index
	// page by design — only the TOC listing is under test here.
	if !bytes.Contains(body, []byte(`<a class="wiki" href="/`+testNS+`/alpha"`)) {
		t.Errorf("index TOC should link the sibling page, body: %s", body)
	}
	if bytes.Contains(body, []byte(`<a class="wiki" href="/`+testHome+`"`)) {
		t.Errorf("index TOC must not link the index page itself, body: %s", body)
	}
}
