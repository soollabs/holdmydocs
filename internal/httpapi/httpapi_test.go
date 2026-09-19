package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hmd/internal/api"
	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/httpmiddleware"
	"hmd/internal/search"
	"hmd/internal/store"
	"hmd/internal/wiki"
)

const (
	testNS           = "notes"
	defaultIndexPage = store.DefaultIndexPage
	testHome         = testNS + "/" + defaultIndexPage
)

// testEnv is the HTTP data adapter's own fixture. It wires the application
// operations the adapter consumes and serves only the httpapi routes behind the
// shared authentication and security-header middleware, without importing the
// web package.
type testEnv struct {
	api    *api.API
	store  *store.Store
	index  *search.Index
	auth   *auth.Auth
	server *httptest.Server
	token  string
}

type bearerTransport struct{ token string }

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}

func newTestEnv(t *testing.T, documents bool) (*testEnv, *http.Client) {
	t.Helper()

	cfg := config.Config{
		RepoDir:   t.TempDir(),
		AppDir:    t.TempDir(),
		Git:       config.GitConfig{User: "test"},
		AdminUser: "admin",
		AdminPass: "password12345",
	}
	content, err := store.Open(store.Options{RepoDir: cfg.RepoDir, Git: store.GitOptions{User: cfg.Git.User}})
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	nsCfg, err := wiki.NamespaceConfig{Widgets: wiki.BuiltinNamespaceWidgets, Index: defaultIndexPage}.Encode()
	if err != nil {
		t.Fatalf("encoding namespace config: %v", err)
	}
	if _, err := content.Save(wiki.NamespaceConfigPath(testNS), nsCfg, "Configure namespace "+testNS, cfg.Git.User, cfg.Git.User+"@hmd.local"); err != nil {
		t.Fatalf("seeding namespace config: %v", err)
	}
	home := wiki.Page{Slug: testHome, Title: testNS, Body: store.DefaultHomeMD}
	if _, err := content.Save(wiki.PageFile(testHome), home.Encode(), "Add "+testHome, cfg.Git.User, cfg.Git.User+"@hmd.local"); err != nil {
		t.Fatalf("seeding %s: %v", testHome, err)
	}
	content.NeedsSetup.Store(false)

	index, err := search.BuildIndex([]wiki.Page{home})
	if err != nil {
		t.Fatalf("BuildIndex failed: %v", err)
	}
	authn, err := auth.Open(auth.Options{AppDir: cfg.AppDir, AdminUser: cfg.AdminUser, AdminPass: cfg.AdminPass})
	if err != nil {
		t.Fatalf("auth.Open failed: %v", err)
	}
	namespaces, err := api.BuildNamespaceRegistry(cfg.RepoDir)
	if err != nil {
		t.Fatalf("api.BuildNamespaceRegistry failed: %v", err)
	}
	app := api.New(content, index, authn)
	app.SetConfig(cfg)
	app.SetWikiConfig(wiki.WikiConfig{Landing: testNS + "/"})
	app.SetNamespaces(namespaces)

	env := &testEnv{api: app, store: content, index: index, auth: authn}

	// DocumentsEnabled gates the attachment-search route; a fixture that needs
	// it registers a stub document search via the same option the composition
	// root passes.
	renderer := wiki.NewRenderer(index.ResolveLink)
	mux := http.NewServeMux()
	New(app, Options{Renderer: renderer, DocumentsEnabled: documents}).Register(mux)
	env.server = httptest.NewServer(httpmiddleware.SecurityHeaders(authn.Middleware(mux)))
	t.Cleanup(env.server.Close)

	env.token, err = authn.AddToken("admin", "test", time.Time{}, []string{"read", "write", "settings"}, nil)
	if err != nil {
		t.Fatalf("AddToken failed: %v", err)
	}

	client := &http.Client{
		Transport: bearerTransport{token: env.token},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return env, client
}

// seedPage writes and indexes one page after startup, as a mutation would.
func (env *testEnv) seedPage(t *testing.T, page wiki.Page) {
	t.Helper()
	if _, err := env.store.Save(wiki.PageFile(page.Slug), page.Encode(), "Seed "+page.Slug, "admin", "admin@hmd.local"); err != nil {
		t.Fatalf("seeding %s: %v", page.Slug, err)
	}
	if err := env.index.Update(page); err != nil {
		t.Fatalf("indexing %s: %v", page.Slug, err)
	}
}

type testInput struct {
	Name string `json:"name"`
}

type testOutput struct {
	Echo string `json:"echo"`
}

// TestRegisterJSONCentralisesDecodingAndEncoding exercises the generic helper
// through a concrete input/result pair: query decoding, bounded JSON body
// decoding, api.Error category mapping and successful encoding.
func TestRegisterJSONCentralisesDecodingAndEncoding(t *testing.T) {
	mux := http.NewServeMux()
	h := &Handlers{}
	h.registerJSON(mux, "POST /_/api/test", func(_ context.Context, in testInput) (testOutput, error) {
		switch in.Name {
		case "boom":
			return testOutput{}, api.Conflict("conflict")
		case "bad":
			return testOutput{}, api.InvalidInput("bad input", nil)
		default:
			return testOutput{Echo: in.Name}, nil
		}
	})
	h.registerJSON(mux, "GET /_/api/test", func(_ context.Context, in testInput) (testOutput, error) {
		return testOutput{Echo: in.Name}, nil
	})
	h.registerJSON(mux, "POST /_/api/test-empty", func(_ context.Context, _ noInput) (testOutput, error) {
		return testOutput{Echo: "empty"}, nil
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	post := func(body string) *http.Response {
		t.Helper()
		resp, err := http.Post(server.URL+"/_/api/test", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		return resp
	}

	resp := post(`{"name":"hi"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid body status = %d, want 200", resp.StatusCode)
	}
	var out testOutput
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if out.Echo != "hi" {
		t.Errorf("echo = %q, want hi", out.Echo)
	}

	if resp := post(`{"name":`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed body status = %d, want 400", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	if resp := post(`{"name":"boom"}`); resp.StatusCode != http.StatusConflict {
		t.Errorf("conflict status = %d, want 409", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	if resp := post(`{"name":"bad"}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid input status = %d, want 400", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	// Oversized bodies are rejected inside the shared decoding helper.
	if resp := post(`{"name":"` + strings.Repeat("x", maxJSONBodyBytes) + `"}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("oversized body status = %d, want 400", resp.StatusCode)
	} else {
		resp.Body.Close()
	}

	// A bodyless GET decodes its input from the query string.
	resp, err := http.Get(server.URL + "/_/api/test?name=query")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding GET response: %v", err)
	}
	if out.Echo != "query" {
		t.Errorf("query echo = %q, want query", out.Echo)
	}

	// A POST with no body decodes to the zero input rather than failing.
	resp, err = http.Post(server.URL+"/_/api/test-empty", "", nil)
	if err != nil {
		t.Fatalf("POST empty: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty POST status = %d, want 200", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding empty POST response: %v", err)
	}
	if out.Echo != "empty" {
		t.Errorf("empty POST echo = %q, want empty", out.Echo)
	}
}

func TestSearchEndpoint(t *testing.T) {
	env, client := newTestEnv(t, false)
	env.seedPage(t, wiki.Page{Slug: testNS + "/api-search", Title: "API Search", Body: "This contains uniquetestword for API search."})

	resp, err := client.Get(env.server.URL + "/_/api/search?q=uniquetestword")
	if err != nil {
		t.Fatalf("GET api/search: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search status = %d, want 200", resp.StatusCode)
	}
	var results []AutocompleteResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		t.Fatalf("decoding search JSON: %v", err)
	}
	if len(results) != 1 || results[0].Slug != testNS+"/api-search" {
		t.Errorf("results = %+v, want one result for %s", results, testNS+"/api-search")
	}
	if results[0].Title != "API Search" {
		t.Errorf("title = %q, want API Search", results[0].Title)
	}
}

func TestHealthEndpoint(t *testing.T) {
	env, client := newTestEnv(t, false)
	env.seedPage(t, wiki.Page{Slug: testNS + "/dangling", Title: "Dangling", Body: "see [[nowhere]]"})

	resp, err := client.Get(env.server.URL + "/_/api/health")
	if err != nil {
		t.Fatalf("GET api/health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
	var report HealthReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decoding health JSON: %v", err)
	}
	if len(report.Missing) != 1 || report.Missing[0].Slug != testNS+"/nowhere" {
		t.Errorf("missing = %+v, want [%s/nowhere]", report.Missing, testNS)
	}

	resp, err = client.Get(env.server.URL + "/_/api/health?namespace=" + testNS)
	if err != nil {
		t.Fatalf("GET scoped api/health: %v", err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decoding scoped health JSON: %v", err)
	}
	if len(report.Missing) != 1 {
		t.Errorf("scoped missing = %+v, want one entry", report.Missing)
	}

	resp, err = client.Get(env.server.URL + "/_/api/health?namespace=bad%2Fname")
	if err != nil {
		t.Fatalf("GET invalid namespace health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid namespace status = %d, want 400", resp.StatusCode)
	}
}

func TestSyncEndpoint(t *testing.T) {
	env, client := newTestEnv(t, false)

	resp, err := client.Get(env.server.URL + "/_/api/sync")
	if err != nil {
		t.Fatalf("GET api/sync: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sync status = %d, want 200", resp.StatusCode)
	}
	var status SyncStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decoding sync JSON: %v", err)
	}
	if status.State == "" || status.At == "" {
		t.Errorf("sync status = %+v, want non-empty state and at", status)
	}
}

func TestPreviewEndpoints(t *testing.T) {
	env, client := newTestEnv(t, false)
	env.seedPage(t, wiki.Page{Slug: testNS + "/preview-source", Title: "Preview Source", Body: "alpha beta gamma"})

	resp, err := client.Get(env.server.URL + "/_/api/preview/" + testNS + "/preview-source")
	if err != nil {
		t.Fatalf("GET preview: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d, want 200", resp.StatusCode)
	}
	var card struct {
		Title   string   `json:"title"`
		Snippet string   `json:"snippet"`
		Tags    []string `json:"tags"`
		Age     string   `json:"age"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decoding preview JSON: %v", err)
	}
	if card.Title != "Preview Source" || card.Snippet == "" || card.Age == "" {
		t.Errorf("preview card = %+v, want title, snippet and age", card)
	}

	form := strings.NewReader("body=**bold**")
	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/_/api/preview?slug="+testNS+"/preview-source", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("POST preview: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("<strong>")) {
		t.Errorf("preview body = %q, want rendered <strong>", body)
	}
}

func TestAttachmentUploadAndServe(t *testing.T) {
	env, client := newTestEnv(t, false)
	env.seedPage(t, wiki.Page{Slug: testNS + "/attachment-page", Title: "Attachment Page", Body: "body"})

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "test-image.png")
	if _, err := io.Copy(part, bytes.NewReader([]byte("\x89PNG\r\n\x1a\nrest"))); err != nil {
		t.Fatalf("copying upload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, env.server.URL+"/_/api/attachments/"+testNS+"/attachment-page", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST upload: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", resp.StatusCode)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(respBody, []byte("/_/attachments/"+testNS+"/attachment-page/")) {
		t.Errorf("upload response = %q, want attachment URL", respBody)
	}

	resp, err = client.Get(env.server.URL + "/_/attachments/" + testNS + "/attachment-page/test-image.png")
	if err != nil {
		t.Fatalf("GET attachment: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("serve status = %d, want 200", resp.StatusCode)
	}
	content, _ := io.ReadAll(resp.Body)
	if len(content) == 0 {
		t.Error("served attachment is empty")
	}
}

func TestAPIsRejectRootPageSlugs(t *testing.T) {
	env, client := newTestEnv(t, false)

	for _, tc := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/_/api/preview/readme", http.StatusNotFound},
		{http.MethodPost, "/_/api/attachments/readme", http.StatusBadRequest},
		{http.MethodGet, "/_/attachments/readme/file.png", http.StatusNotFound},
	} {
		req, err := http.NewRequest(tc.method, env.server.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
}

// TestRestrictedTokenNamespaceDeniedJSON preserves the API-surface security
// contract after the adapter split: a namespace-restricted bearer token denied
// on an /_/api/ endpoint receives 403 with the shared JSON error body.
func TestRestrictedTokenNamespaceDeniedJSON(t *testing.T) {
	env, _ := newTestEnv(t, false)

	restricted, err := env.auth.AddToken("admin", "restricted-http", time.Time{}, []string{"read", "write"}, []string{testNS})
	if err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	client := &http.Client{
		Transport: bearerTransport{token: restricted},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(env.server.URL + "/_/api/preview/private/denied")
	if err != nil {
		t.Fatalf("GET denied preview: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if got := strings.TrimSpace(string(body)); got != `{"error":"namespace access denied"}` {
		t.Errorf("body = %q, want namespace denial JSON", got)
	}
}
