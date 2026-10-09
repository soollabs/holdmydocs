package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
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

// testEnv is the MCP adapter's own fixture. It wires the application operations
// the adapter consumes plus the browser-side capability-upload transport the
// upload_attachment flow calls back into, without reaching through the web
// package.
type testEnv struct {
	api    *api.API
	store  *store.Store
	index  *search.Index
	auth   *auth.Auth
	server *httptest.Server
}

type bearerTransport struct{ token string }

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}

type testOAuthVerifier struct{}

func (testOAuthVerifier) VerifyBearer(_ context.Context, token, path string) (auth.TokenPrincipal, bool) {
	if token != "oauth-test-token" || path != "/_/mcp" {
		return auth.TokenPrincipal{}, false
	}
	return auth.TokenPrincipal{
		User: "admin", Scopes: []string{"read"}, Namespaces: []string{"notes"},
		GrantID: "grant-test", FamilyID: "family-test",
	}, true
}

func (testOAuthVerifier) Challenge() string {
	return `Bearer resource_metadata="https://wiki.example.test/.well-known/oauth-protected-resource"`
}

func newMCPTestApp(t *testing.T, mcpEnabled bool) (*httptest.Server, string) {
	t.Helper()
	app, server, token := newMCPTestAppWithApp(t, mcpEnabled)
	_ = app
	return server, token
}

func newMCPTestAppWithApp(t *testing.T, mcpEnabled bool) (*testEnv, *httptest.Server, string) {
	t.Helper()

	cfg := config.Config{
		RepoDir:   t.TempDir(),
		AppDir:    t.TempDir(),
		Git:       config.GitConfig{User: "test"},
		AdminUser: "admin",
		AdminPass: "password12345",
		MCP:       config.MCPConfig{Enabled: mcpEnabled},
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

	client := api.New(content, index, authn)
	client.SetConfig(cfg)
	client.SetNamespaces(namespaces)

	env := &testEnv{api: client, store: content, index: index, auth: authn}
	env.server = httptest.NewServer(env.handler(mcpEnabled))
	t.Cleanup(env.server.Close)

	token, err := authn.AddToken("admin", "test", time.Time{}, []string{"read", "write", "settings"}, nil)
	if err != nil {
		t.Fatalf("AddToken failed: %v", err)
	}
	return env, env.server, token
}

// handler assembles the MCP endpoint and the browser capability-upload endpoint
// the upload flow posts to, behind the shared authentication middleware, the
// way the composition root does.
func (env *testEnv) handler(mcpEnabled bool) http.Handler {
	mux := http.NewServeMux()
	if mcpEnabled {
		handler := NewServer(env.api, Options{Version: "test", DocumentsEnabled: env.index.DocumentsEnabled()}).Handler()
		mux.Handle("GET /_/mcp", handler)
		mux.Handle("POST /_/mcp", handler)
		mux.Handle("DELETE /_/mcp", handler)
	}
	mux.HandleFunc("POST /_/api/attachment-uploads/{token}", env.capabilityUpload)
	return httpmiddleware.SecurityHeaders(env.auth.Middleware(mux))
}

// capabilityUpload is the minimal browser transport for a one-use upload
// capability: it redeems the token and streams the file to the shared
// api.UploadAttachment operation.
func (env *testEnv) capabilityUpload(w http.ResponseWriter, r *http.Request) {
	const maxBytes = 10 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "no file uploaded", http.StatusBadRequest)
		return
	}
	var content []byte
	var filename string
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			http.Error(w, "invalid upload", http.StatusBadRequest)
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		content, err = io.ReadAll(io.LimitReader(part, maxBytes+1))
		filename = part.FileName()
		_ = part.Close()
		if err != nil {
			http.Error(w, "error reading file", http.StatusInternalServerError)
			return
		}
		break
	}
	if content == nil {
		http.Error(w, "no file uploaded", http.StatusBadRequest)
		return
	}
	upload, err := env.api.RedeemUploadCapability(r.Context(), r.PathValue("token"), filename, content)
	if err != nil {
		if api.CategoryOf(err) == api.CategoryNotFound {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "error saving file", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"url": "/_/attachments/" + upload.Slug + "/" + upload.Filename})
}

func newMCPRestrictedTestApp(t *testing.T) (*testEnv, *httptest.Server, string, string) {
	t.Helper()
	app, server, _ := newMCPTestAppWithApp(t, true)

	seedPage := func(page wiki.Page) {
		t.Helper()
		if _, err := app.store.Save(wiki.PageFile(page.Slug), page.Encode(), "Seed "+page.Slug, "admin", "admin@hmd.local"); err != nil {
			t.Fatalf("seeding %s: %v", page.Slug, err)
		}
		if err := app.index.Update(page); err != nil {
			t.Fatalf("indexing %s: %v", page.Slug, err)
		}
	}
	seedPage(wiki.Page{Slug: "notes/allowed", Title: "Allowed", Body: "shared notes content"})
	seedPage(wiki.Page{Slug: "private/denied", Title: "Denied", Body: "shared private content"})

	for _, name := range []string{"notes", "empty"} {
		if _, err := app.store.Save(wiki.NamespaceConfigPath(name), []byte("public: true\n"), "Configure namespace "+name, "admin", "admin@hmd.local"); err != nil {
			t.Fatalf("seeding %s namespace: %v", name, err)
		}
	}
	if err := app.api.RefreshNamespaces(); err != nil {
		t.Fatalf("refreshing namespaces: %v", err)
	}

	restricted, err := app.auth.AddToken("admin", "notes-only", time.Time{}, []string{"read", "write"}, []string{"notes"})
	if err != nil {
		t.Fatalf("restricted token: %v", err)
	}
	settings, err := app.auth.AddToken("admin", "settings-only", time.Time{}, []string{"settings"}, nil)
	if err != nil {
		t.Fatalf("settings token: %v", err)
	}
	return app, server, restricted, settings
}

func closeTestBody(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
}
func connectMCP(t *testing.T, server *httptest.Server, token string) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:   server.URL + "/_/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{token}},
	}, nil)
	if err != nil {
		t.Fatalf("MCP connect failed: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("closing MCP session: %v", err)
		}
	})
	return session
}

func callTool(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func toolText(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			return tc.Text
		}
	}
	t.Fatalf("no text content in result: %+v", res)
	return ""
}

func toolJSON(t *testing.T, res *sdk.CallToolResult, out any) {
	t.Helper()
	if err := json.Unmarshal([]byte(toolText(t, res)), out); err != nil {
		t.Fatalf("unmarshalling tool result: %v", err)
	}
}

func TestMCPExposesExpectedTools(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	session := connectMCP(t, server, token)
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	allowed := map[string]bool{
		"list_pages": true, "read_page": true, "save_page": true, "edit_page": true, "delete_page": true,
		"search": true, "backlinks": true, "recent_changes": true, "health": true,
		"list_namespaces": true, "read_namespace": true, "save_namespace": true,
		"upload_attachment": true, "read_attachment": true,
	}
	for _, tool := range result.Tools {
		if !allowed[tool.Name] {
			t.Errorf("MCP exposes unexpected tool %q", tool.Name)
		}
		delete(allowed, tool.Name)
	}
	for name := range allowed {
		t.Errorf("MCP missing page tool %q", name)
	}
}

func TestMCPOAuthPrincipalReceivesScopeStepUpGuidance(t *testing.T) {
	env, server, _ := newMCPTestAppWithApp(t, true)
	env.auth.SetOAuthBearerVerifier(testOAuthVerifier{})
	session := connectMCP(t, server, "oauth-test-token")

	read := callTool(t, session, "read_page", map[string]any{"slug": testHome})
	if read.IsError {
		t.Fatalf("OAuth read within its granted namespace failed: %s", toolText(t, read))
	}
	write := callTool(t, session, "save_page", map[string]any{
		"slug": "notes/oauth-denied", "title": "Denied", "body": "should not be saved",
	})
	if !write.IsError || !strings.Contains(toolText(t, write), "insufficient_scope") ||
		!strings.Contains(toolText(t, write), `"write"`) {
		t.Fatalf("OAuth write denial lacks scope step-up guidance: isError=%v, text=%q", write.IsError, toolText(t, write))
	}
	crossNamespace := callTool(t, session, "read_page", map[string]any{"slug": "private/denied"})
	if !crossNamespace.IsError || !strings.Contains(toolText(t, crossNamespace), "reconnect") {
		t.Fatalf("OAuth namespace denial lacks reconnect guidance: isError=%v, text=%q", crossNamespace.IsError, toolText(t, crossNamespace))
	}
}

func TestMCPToolContracts(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	session := connectMCP(t, server, token)
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	wantArgs := map[string][]string{
		"list_pages": {}, "list_namespaces": {},
		"read_page": {"slug", "path"}, "save_page": {"slug", "path", "title", "tags", "body", "pin", "basehash"},
		"edit_page": {"slug", "path", "basehash", "edits", "dryRun"}, "delete_page": {"slug", "path"},
		"search": {"query"}, "backlinks": {"slug", "path"}, "recent_changes": {"limit"}, "health": {"namespace"},
		"read_namespace":    {"name"},
		"save_namespace":    {"name", "widgets", "public", "title", "description", "skin", "palette", "index", "tree", "new", "basehash"},
		"upload_attachment": {"slug", "path", "filename"}, "read_attachment": {"slug", "path", "filename"},
	}
	for _, tool := range result.Tools {
		args, ok := wantArgs[tool.Name]
		if !ok {
			continue
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			t.Errorf("%s input schema has type %T, want map", tool.Name, tool.InputSchema)
			continue
		}
		properties, _ := schema["properties"].(map[string]any)
		for _, arg := range args {
			if _, ok := properties[arg]; !ok {
				t.Errorf("%s schema missing %q: %#v", tool.Name, arg, schema)
			}
			if !strings.Contains(tool.Description, arg) {
				t.Errorf("%s description does not name argument %q", tool.Name, arg)
			}
		}
		if len(args) == 0 && !strings.Contains(tool.Description, "Arguments: none") {
			t.Errorf("%s description does not explicitly say it takes no arguments", tool.Name)
		}
		for _, arg := range map[string][]string{
			"save_page": {"tags"}, "save_namespace": {"widgets", "tree"},
		}[tool.Name] {
			property, _ := properties[arg].(map[string]any)
			items, _ := property["items"].(map[string]any)
			if items["type"] != "string" {
				t.Errorf("%s.%s items type = %v, want string", tool.Name, arg, items["type"])
			}
		}
	}
}

func TestMCPPageIdentifier(t *testing.T) {
	for _, tt := range []struct {
		name, slug, path, want string
		wantErr                bool
	}{
		{name: "slug", slug: "notes/page", want: "notes/page"},
		{name: "path alias", path: "notes/page", want: "notes/page"},
		{name: "neither", wantErr: true},
		{name: "both", slug: "notes/page", path: "notes/page", wantErr: true},
		{name: "invalid", path: "../page", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mcpPageIdentifier(tt.slug, tt.path)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("mcpPageIdentifier(%q, %q) = %q, %v; want %q, error=%v", tt.slug, tt.path, got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestMCPUploadAttachment(t *testing.T) {
	app, server, token := newMCPTestAppWithApp(t, true)
	session := connectMCP(t, server, token)
	result := callTool(t, session, "upload_attachment", map[string]any{
		"path": testHome, "filename": "source.txt",
	})
	if result.IsError {
		t.Fatalf("upload_attachment: %s", toolText(t, result))
	}
	var out mcpAttachmentUploadOut
	toolJSON(t, result, &out)
	if !strings.HasPrefix(out.UploadURL, server.URL+"/_/api/attachment-uploads/") || out.AttachmentURL != "/_/attachments/notes/readme/source.txt" || out.ExpiresAt == "" {
		t.Fatalf("upload result = %#v", out)
	}
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "source.txt")
	if err != nil {
		t.Fatalf("creating upload field: %v", err)
	}
	if _, err := part.Write([]byte("PDF content")); err != nil {
		t.Fatalf("writing upload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing upload: %v", err)
	}
	upload, err := http.Post(out.UploadURL, writer.FormDataContentType(), body)
	if err != nil {
		t.Fatalf("posting upload: %v", err)
	}
	defer closeTestBody(t, upload.Body)
	if upload.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want %d", upload.StatusCode, http.StatusOK)
	}
	got, _, err := app.store.Read("attachments/notes/readme/source.txt")
	if err != nil || string(got) != "PDF content" {
		t.Fatalf("uploaded attachment = %q, %v", got, err)
	}
}

func TestMCPReadAttachment(t *testing.T) {
	app, server, token := newMCPTestAppWithApp(t, true)
	path := "attachments/" + testHome + "/source.txt"
	source := []byte("source text")
	if _, err := app.store.SaveAll(map[string][]byte{
		path:                                 source,
		search.ExtractedAttachmentPath(path): search.EncodeExtractedAttachment(search.AttachmentBlobHash(source), "extracted text"),
	}, "Add attachment source.txt", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving attachment: %v", err)
	}
	session := connectMCP(t, server, token)
	result := callTool(t, session, "read_attachment", map[string]any{"path": testHome, "filename": "source.txt"})
	if result.IsError {
		t.Fatalf("read_attachment: %s", toolText(t, result))
	}
	var out mcpAttachmentReadOut
	toolJSON(t, result, &out)
	if out.Text != "extracted text" {
		t.Fatalf("read attachment = %q", out.Text)
	}
}

func TestMCPNamespaceTools(t *testing.T) {
	_, server, token := newMCPTestAppWithApp(t, true)
	session := connectMCP(t, server, token)

	res := callTool(t, session, "list_namespaces", nil)
	if res.IsError {
		t.Fatalf("list_namespaces: %s", toolText(t, res))
	}
	var list mcpNamespacesOut
	toolJSON(t, res, &list)
	if len(list.Namespaces) != 1 || list.Namespaces[0].Name != testNS {
		t.Fatalf("list_namespaces = %+v, want %s", list.Namespaces, testNS)
	}

	res = callTool(t, session, "read_namespace", map[string]any{"name": testNS})
	if res.IsError {
		t.Fatalf("read_namespace: %s", toolText(t, res))
	}
	var current mcpNamespaceOut
	toolJSON(t, res, &current)
	if current.Hash == "" || current.Index != defaultIndexPage {
		t.Fatalf("read_namespace = %+v, want a hash and index %q", current, defaultIndexPage)
	}

	res = callTool(t, session, "save_namespace", map[string]any{
		"name": testNS, "widgets": []string{"pages"}, "public": true,
		"title": "Notes", "description": "Personal notes", "skin": "newsprint", "index": defaultIndexPage, "tree": []string{"guides", "reference"},
		"basehash": current.Hash,
	})
	if res.IsError {
		t.Fatalf("save_namespace: %s", toolText(t, res))
	}
	var saved mcpNamespaceOut
	toolJSON(t, res, &saved)
	if saved.Hash == "" || !saved.Public || saved.Title != "Notes" || saved.Description != "Personal notes" || saved.Palette != "" || !slices.Equal(saved.Tree, []string{"guides", "reference"}) {
		t.Errorf("save_namespace = %+v", saved)
	}
	res = callTool(t, session, "list_namespaces", nil)
	toolJSON(t, res, &list)
	if list.Namespaces[0].Description != "Personal notes" {
		t.Errorf("list_namespaces description = %q", list.Namespaces[0].Description)
	}

	res = callTool(t, session, "save_namespace", map[string]any{
		"name": testNS, "widgets": []string{"pages"}, "basehash": current.Hash,
	})
	if !res.IsError {
		t.Error("save_namespace accepted a stale basehash")
	}

	res = callTool(t, session, "save_namespace", map[string]any{"name": "blog", "public": true})
	if res.IsError {
		t.Fatalf("create namespace: %s", toolText(t, res))
	}
}

func TestMCPDisabledRouteNotRegistered(t *testing.T) {
	server, token := newMCPTestApp(t, false)

	req, _ := http.NewRequest("POST", server.URL+"/_/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("disabled /mcp: got status %d, want 404", resp.StatusCode)
	}
}

func TestMCPWithoutBearer401JSON(t *testing.T) {
	server, _ := newMCPTestApp(t, true)

	resp, err := http.Post(server.URL+"/_/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated /mcp: got status %d, want 401", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("unauthenticated /mcp: got Content-Type %q, want application/json", ct)
	}
}

func TestMCPInvalidBearer401(t *testing.T) {
	server, _ := newMCPTestApp(t, true)

	req, _ := http.NewRequest("POST", server.URL+"/_/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer hmd_bogus")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("invalid Bearer: got status %d, want 401", resp.StatusCode)
	}
}

func TestMCPRequiresModernRequestMetadata(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/_/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing request metadata: got status %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	var body struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != -32020 {
		t.Errorf("missing request metadata: got error code %d, want -32020", body.Error.Code)
	}
}

func TestMCPRejectsMismatchedProtocolMetadata(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2025-11-25","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Method", "server/discover")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched protocol metadata: got status %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	var response struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != -32020 {
		t.Errorf("mismatched protocol metadata: got error code %d, want -32020", response.Error.Code)
	}
}

func TestMCPRejectsUnsupportedInitialise(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Method", "initialize")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, resp.Body)
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unsupported initialise: got status %d, want %d: %s", resp.StatusCode, http.StatusNotFound, responseBody)
	}
	var response struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != -32601 {
		t.Errorf("unsupported initialise: got error code %d, want -32601", response.Error.Code)
	}
}

func TestMCPStreamableHTTPLegacyLifecycle(t *testing.T) {
	for _, version := range []string{"2025-03-26", "2025-06-18", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			server, token := newMCPTestApp(t, true)
			post := func(body, version string) (*http.Response, []byte) {
				t.Helper()
				req, err := http.NewRequest(http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("Content-Type", "application/json")
				if version != "" {
					req.Header.Set("MCP-Protocol-Version", version)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer closeTestBody(t, resp.Body)
				responseBody, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
					var data [][]byte
					for line := range bytes.SplitSeq(responseBody, []byte("\n")) {
						if after, ok := bytes.CutPrefix(line, []byte("data:")); ok {
							data = append(data, bytes.TrimSpace(after))
						}
					}
					responseBody = bytes.Join(data, []byte("\n"))
				}
				return resp, responseBody
			}

			// Legacy Streamable HTTP initialise has no version header and no modern
			// per-request metadata. Negotiation is carried in initialize.params.
			init, body := post(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, version), "")
			if init.StatusCode != http.StatusOK {
				t.Fatalf("initialize: got %d: %s", init.StatusCode, body)
			}
			if !bytes.Contains(body, []byte(`"protocolVersion":"`+version+`"`)) {
				t.Fatalf("initialize did not negotiate legacy protocol: %s", body)
			}

			notification, body := post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, version)
			if notification.StatusCode != http.StatusAccepted && notification.StatusCode != http.StatusOK {
				t.Fatalf("notifications/initialized: got %d: %s", notification.StatusCode, body)
			}

			list, body := post(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, version)
			if list.StatusCode != http.StatusOK {
				t.Fatalf("tools/list: got %d: %s", list.StatusCode, body)
			}
			if !bytes.Contains(body, []byte(`"tools"`)) {
				t.Fatalf("tools/list response missing tools: %s", body)
			}
			for _, header := range []string{version, ""} {
				call, body := post(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_page","arguments":{"slug":%q}}}`, testHome), header)
				if call.StatusCode != http.StatusOK {
					t.Fatalf("tools/call (header %q): %d: %s", header, call.StatusCode, body)
				}
				var result struct {
					Result struct {
						IsError bool              `json:"isError"`
						Content []json.RawMessage `json:"content"`
					} `json:"result"`
					Error json.RawMessage `json:"error"`
				}
				if err := json.Unmarshal(body, &result); err != nil || result.Result.IsError || len(result.Result.Content) == 0 || len(result.Error) != 0 {
					t.Fatalf("tools/call (header %q) did not read the page: %s (%v)", header, body, err)
				}
			}
			unsupported, body := post(`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`, "2024-11-05")
			if unsupported.StatusCode != http.StatusBadRequest {
				t.Fatalf("unsupported version: %d: %s", unsupported.StatusCode, body)
			}
			for _, method := range []string{http.MethodGet, http.MethodDelete} {
				req, err := http.NewRequest(method, server.URL+"/_/mcp", nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("MCP-Protocol-Version", version)
				req.Header.Set("Accept", "text/event-stream")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				closeTestBody(t, resp.Body)
				if resp.StatusCode != http.StatusMethodNotAllowed {
					t.Fatalf("stateless %s: %d, want 405", method, resp.StatusCode)
				}
			}
		})
	}
}

func TestMCPOnlyAllowsPost(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			req, err := http.NewRequest(method, server.URL+"/_/mcp", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer closeTestBody(t, resp.Body)
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("%s /_/mcp: got status %d, want %d", method, resp.StatusCode, http.StatusMethodNotAllowed)
			}
			if method != http.MethodPut && resp.Header.Get("Allow") != http.MethodPost {
				allow := resp.Header.Get("Allow")
				t.Errorf("%s /_/mcp: got Allow %q, want %q", method, allow, http.MethodPost)
			}
		})
	}
}

func TestMCPRejectsSessionHeader(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Method", "server/discover")
	req.Header.Set("Mcp-Session-Id", "session")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("session header: got status %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	var response struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != -32020 {
		t.Errorf("session header: got error code %d, want -32020", response.Error.Code)
	}
}

func TestMCPRejectsSessionQuery(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/_/mcp?sessionId=session", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Method", "server/discover")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("session query: got status %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	var response struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != -32020 {
		t.Errorf("session query: got error code %d, want -32020", response.Error.Code)
	}
}

func TestMCPRejectsCrossOriginRequest(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Method", "server/discover")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	req.Header.Set("Origin", "https://untrusted.example")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin MCP request: got status %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestMCPHandlerPropagatesRequestCancellation(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "block", Description: "Wait for cancellation."}, func(ctx context.Context, _ *sdk.CallToolRequest, _ any) (*sdk.CallToolResult, any, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, nil, ctx.Err()
	})
	httpServer := httptest.NewServer(newMCPHTTPHandler("https://wiki.example.com", func(*http.Request) *sdk.Server { return server }))
	t.Cleanup(httpServer.Close)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"block","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "block")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)

	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocking tool did not start")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("tool context was not cancelled")
	}
	if err := <-done; err == nil {
		t.Error("cancelled request returned a successful response")
	}
}

func TestMCPRequiresClientCapabilities(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"}}}}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Method", "server/discover")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing client capabilities: got status %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	var response struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != -32021 {
		t.Errorf("missing client capabilities: got error code %d, want -32021", response.Error.Code)
	}
}

func TestMCPRequiresClientInfo(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Method", "server/discover")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing client info: got status %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	var response struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != -32020 {
		t.Errorf("missing client info: got error code %d, want -32020", response.Error.Code)
	}
}

func TestMCPRequiresMetadataProtocolVersion(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Method", "server/discover")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing metadata protocol version: got status %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	var response struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != -32020 {
		t.Errorf("missing metadata protocol version: got error code %d, want -32020", response.Error.Code)
	}
}

func TestMCPToolFlow(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	session := connectMCP(t, server, token)

	res := callTool(t, session, "save_page", map[string]any{
		"path":  testNS + "/agent-note",
		"title": "Agent note",
		"tags":  []string{"ai"},
		"body":  "Hello from an agent. See [[readme]].",
	})
	if res.IsError {
		t.Fatalf("create failed: %s", toolText(t, res))
	}
	var created mcpSaveOut
	toolJSON(t, res, &created)
	if created.Hash == "" {
		t.Fatal("create returned no hash")
	}

	res = callTool(t, session, "save_page", map[string]any{
		"slug": testNS + "/agent-note", "title": "Agent note", "body": "clobber",
	})
	if !res.IsError {
		t.Fatal("create over existing page succeeded, want conflict")
	}

	res = callTool(t, session, "read_page", map[string]any{"path": testNS + "/agent-note"})
	if res.IsError {
		t.Fatalf("read by path alias failed: %s", toolText(t, res))
	}
	both := callTool(t, session, "read_page", map[string]any{"slug": testNS + "/agent-note", "path": testNS + "/agent-note"})
	if !both.IsError {
		t.Fatal("read with both slug and path succeeded")
	}
	var page mcpPageOut
	toolJSON(t, res, &page)
	if page.Body != "Hello from an agent. See [[readme]]." || page.Title != "Agent note" || page.Hash != created.Hash {
		t.Errorf("read_page mismatch: %+v (want hash %s)", page, created.Hash)
	}

	res = callTool(t, session, "save_page", map[string]any{
		"slug": testNS + "/agent-note", "title": "Agent note", "body": "stale write", "basehash": "0000000000000000000000000000000000000000",
	})
	if !res.IsError {
		t.Fatal("stale save succeeded, want conflict")
	}
	var conflict struct{ Hash, Body string }
	toolJSON(t, res, &conflict)
	if conflict.Hash != created.Hash || conflict.Body != page.Body {
		t.Errorf("conflict payload mismatch: %+v", conflict)
	}

	res = callTool(t, session, "save_page", map[string]any{
		"slug": testNS + "/agent-note", "title": "Agent note", "body": "Updated. See [[" + testNS + "]].", "basehash": created.Hash,
	})
	if res.IsError {
		t.Fatalf("update failed: %s", toolText(t, res))
	}

	res = callTool(t, session, "list_pages", nil)
	if !strings.Contains(toolText(t, res), "agent-note") {
		t.Error("list_pages missing agent-note")
	}
	res = callTool(t, session, "backlinks", map[string]any{"slug": testHome})
	if !strings.Contains(toolText(t, res), "agent-note") {
		t.Errorf("backlinks(%s) missing agent-note", testHome)
	}

	res = callTool(t, session, "search", map[string]any{"query": "Updated"})
	if !strings.Contains(toolText(t, res), "agent-note") {
		t.Error("search missing agent-note")
	}

	res = callTool(t, session, "recent_changes", map[string]any{"limit": 5})
	var recent mcpRecentOut
	toolJSON(t, res, &recent)
	if len(recent.Commits) == 0 {
		t.Fatal("recent_changes returned no commits")
	}
	head := recent.Commits[0]
	if head.Message != "Update Agent note" || head.Author != "admin" {
		t.Errorf("head commit mismatch: %+v", head)
	}
	if len(head.Files) != 1 || head.Files[0] != testNS+"/agent-note.md" {
		t.Errorf("head commit files mismatch: %v", head.Files)
	}

	res = callTool(t, session, "delete_page", map[string]any{"slug": testNS + "/agent-note"})
	if res.IsError {
		t.Fatalf("delete failed: %s", toolText(t, res))
	}
	res = callTool(t, session, "read_page", map[string]any{"slug": testNS + "/agent-note"})
	if !res.IsError {
		t.Error("read after delete succeeded, want error")
	}

	res = callTool(t, session, "read_page", map[string]any{"slug": "../users"})
	if !res.IsError {
		t.Error("traversal slug accepted, want error")
	}
}

func TestMCPHealth(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	session := connectMCP(t, server, token)

	res := callTool(t, session, "save_page", map[string]any{
		"slug": "blog/agent-note", "title": "Agent note", "body": "see [[nowhere]]",
	})
	if res.IsError {
		t.Fatalf("save_page blog/agent-note: %s", toolText(t, res))
	}
	res = callTool(t, session, "save_page", map[string]any{
		"slug": "notes/agent-note", "title": "Agent note", "body": "see [[nowhere]]",
	})
	if res.IsError {
		t.Fatalf("save_page notes/agent-note: %s", toolText(t, res))
	}
	res = callTool(t, session, "save_page", map[string]any{
		"slug": "notes/orphan", "title": "Orphan", "body": "unlinked",
	})
	if res.IsError {
		t.Fatalf("save_page notes/orphan: %s", toolText(t, res))
	}

	res = callTool(t, session, "health", nil)
	if res.IsError {
		t.Fatalf("health: %s", toolText(t, res))
	}
	var out mcpHealthOut
	toolJSON(t, res, &out)
	if len(out.Missing) != 2 {
		t.Errorf("unscoped missing = %+v, want 2 entries", out.Missing)
	}
	if !slices.Contains(out.Orphans, "notes/orphan") {
		t.Errorf("unscoped orphans = %v, want notes/orphan", out.Orphans)
	}
	if len(out.Stale) != 0 {
		t.Errorf("fresh pages reported stale = %v, want none", out.Stale)
	}

	res = callTool(t, session, "health", map[string]any{"namespace": "notes"})
	if res.IsError {
		t.Fatalf("health(notes): %s", toolText(t, res))
	}
	toolJSON(t, res, &out)
	if len(out.Missing) != 1 || out.Missing[0].Slug != "notes/nowhere" {
		t.Errorf("scoped missing = %+v, want [notes/nowhere]", out.Missing)
	}
	wantOrphans := []string{"notes/agent-note", "notes/orphan"}
	if !slices.Equal(out.Orphans, wantOrphans) {
		t.Errorf("scoped orphans = %v, want %v", out.Orphans, wantOrphans)
	}
}

func TestMCPNamespacedPage(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	session := connectMCP(t, server, token)

	res := callTool(t, session, "save_page", map[string]any{
		"slug": "notes/agent-note", "title": "Agent note", "body": "hello",
	})
	if res.IsError {
		t.Fatalf("namespaced save: %s", toolText(t, res))
	}
	var saved struct {
		Hash string `json:"hash"`
	}
	toolJSON(t, res, &saved)
	if saved.Hash == "" {
		t.Fatal("namespaced save returned no hash")
	}

	res = callTool(t, session, "read_page", map[string]any{"slug": "notes/agent-note"})
	if res.IsError {
		t.Fatalf("namespaced read: %s", toolText(t, res))
	}
	var page struct {
		Slug string `json:"slug"`
		Body string `json:"body"`
	}
	toolJSON(t, res, &page)
	if page.Slug != "notes/agent-note" || page.Body != "hello" {
		t.Errorf("namespaced read = %+v, want notes/agent-note with body hello", page)
	}

	res = callTool(t, session, "list_pages", nil)
	if res.IsError || !strings.Contains(toolText(t, res), "notes/agent-note") {
		t.Fatal("list_pages missing namespaced page")
	}
	res = callTool(t, session, "delete_page", map[string]any{"slug": "notes/agent-note"})
	if res.IsError {
		t.Fatalf("namespaced delete: %s", toolText(t, res))
	}

	for _, slug := range []string{"notes/child/page", "notes/", "/page", ".notes/page", "_/page", "notes/.hidden"} {
		res = callTool(t, session, "read_page", map[string]any{"slug": slug})
		if !res.IsError {
			t.Errorf("invalid namespaced slug %q accepted", slug)
		}
	}
}

func TestMCPRestrictedToken(t *testing.T) {
	_, server, token, settingsToken := newMCPRestrictedTestApp(t)
	restricted := connectMCP(t, server, token)

	if res := callTool(t, restricted, "read_page", map[string]any{"slug": "notes/allowed"}); res.IsError {
		t.Fatalf("allowed read: %s", toolText(t, res))
	}

	var pages mcpListOut
	res := callTool(t, restricted, "list_pages", nil)
	if res.IsError {
		t.Fatalf("list_pages: %s", toolText(t, res))
	}
	toolJSON(t, res, &pages)
	for _, page := range pages.Pages {
		if !strings.HasPrefix(page.Slug, "notes/") {
			t.Errorf("list_pages leaked %q", page.Slug)
		}
	}
	var namespaces mcpNamespacesOut
	res = callTool(t, restricted, "list_namespaces", nil)
	if res.IsError {
		t.Fatalf("list_namespaces: %s", toolText(t, res))
	}
	toolJSON(t, res, &namespaces)
	if len(namespaces.Namespaces) != 1 || namespaces.Namespaces[0].Name != "notes" {
		t.Errorf("restricted namespaces = %+v, want notes only", namespaces.Namespaces)
	}

	var hits mcpSearchOut
	res = callTool(t, restricted, "search", map[string]any{"query": "shared"})
	if res.IsError {
		t.Fatalf("search: %s", toolText(t, res))
	}
	toolJSON(t, res, &hits)
	for _, hit := range hits.Hits {
		if !strings.HasPrefix(hit.Slug, "notes/") {
			t.Errorf("search leaked %q", hit.Slug)
		}
	}

	var backlinks mcpBacklinksOut
	res = callTool(t, restricted, "backlinks", map[string]any{"slug": "notes/allowed"})
	if res.IsError {
		t.Fatalf("backlinks: %s", toolText(t, res))
	}
	toolJSON(t, res, &backlinks)
	for _, backlink := range backlinks.Backlinks {
		if !strings.HasPrefix(backlink.Slug, "notes/") {
			t.Errorf("backlinks leaked %q", backlink.Slug)
		}
	}

	admin := connectMCP(t, server, settingsToken)
	res = callTool(t, admin, "list_namespaces", nil)
	if res.IsError {
		t.Fatalf("settings list_namespaces: %s", toolText(t, res))
	}
	toolJSON(t, res, &namespaces)
	if len(namespaces.Namespaces) != 3 {
		t.Errorf("settings namespaces = %+v, want all namespaces", namespaces.Namespaces)
	}
	for _, slug := range []string{testHome, "notes/allowed", "private/denied"} {
		if res := callTool(t, admin, "read_page", map[string]any{"slug": slug}); res.IsError {
			t.Errorf("settings token read %s: %s", slug, toolText(t, res))
		}
	}
	var recent mcpRecentOut
	res = callTool(t, restricted, "recent_changes", map[string]any{"limit": 100})
	if res.IsError {
		t.Fatalf("recent_changes: %s", toolText(t, res))
	}
	toolJSON(t, res, &recent)
	for _, commit := range recent.Commits {
		for _, path := range commit.Files {
			if path == "readme.md" || strings.HasPrefix(path, "private/") || strings.HasPrefix(path, ".private/") {
				t.Errorf("recent_changes leaked %q in commit %s", path, commit.Hash)
			}
		}
	}

	denied := []struct {
		name string
		args map[string]any
	}{
		{"read_page", map[string]any{"slug": "readme"}},
		{"read_page", map[string]any{"slug": "private/denied"}},
		{"save_page", map[string]any{"slug": "private/new", "body": "denied"}},
		{"edit_page", map[string]any{"slug": "private/denied", "basehash": "stale", "edits": []any{map[string]any{"oldText": "a", "newText": "b"}}}},
		{"delete_page", map[string]any{"slug": "private/denied"}},
		{"upload_attachment", map[string]any{"slug": "private/denied", "filename": "denied.pdf"}},
		{"read_attachment", map[string]any{"slug": "private/denied", "filename": "file.txt"}},
		{"backlinks", map[string]any{"slug": "private/denied"}},
	}
	for _, tc := range denied {
		if res := callTool(t, restricted, tc.name, tc.args); !res.IsError {
			t.Errorf("%s allowed a disallowed namespace", tc.name)
		}
	}
}

func TestMCPScopes(t *testing.T) {
	app, server, _ := newMCPTestAppWithApp(t, true)
	for _, user := range []struct {
		name  string
		scope string
	}{
		{"reader", "read"},
		{"writer", "write"},
		{"manager", "settings"},
	} {
		if err := app.auth.AddUser(user.name, "password12345"); err != nil {
			t.Fatalf("AddUser(%s): %v", user.name, err)
		}
		if err := app.auth.SetScopes(user.name, []string{user.scope}); err != nil {
			t.Fatalf("SetScopes(%s): %v", user.name, err)
		}
	}
	readerToken, err := app.auth.AddToken("reader", "mcp", time.Time{}, []string{"read"}, nil)
	if err != nil {
		t.Fatalf("reader token: %v", err)
	}
	writerToken, err := app.auth.AddToken("writer", "mcp", time.Time{}, []string{"write"}, nil)
	if err != nil {
		t.Fatalf("writer token: %v", err)
	}
	managerToken, err := app.auth.AddToken("manager", "mcp", time.Time{}, []string{"settings"}, nil)
	if err != nil {
		t.Fatalf("manager token: %v", err)
	}

	_, homeHash, err := app.store.Read(wiki.PageFile(testHome))
	if err != nil {
		t.Fatal(err)
	}
	reader := connectMCP(t, server, readerToken)
	if res := callTool(t, reader, "edit_page", map[string]any{"slug": testHome, "basehash": homeHash, "edits": []any{map[string]any{"oldText": "Welcome", "newText": "denied"}}}); !res.IsError {
		t.Error("read scope edited a page")
	}
	if res := callTool(t, reader, "list_pages", nil); res.IsError {
		t.Fatalf("read scope list_pages: %s", toolText(t, res))
	}
	if res := callTool(t, reader, "save_page", map[string]any{"slug": "nope", "body": "nope"}); !res.IsError {
		t.Error("read scope saved a page")
	}
	if res := callTool(t, reader, "upload_attachment", map[string]any{"slug": testHome, "filename": "reader.pdf"}); !res.IsError {
		t.Error("read scope uploaded an attachment")
	}

	writer := connectMCP(t, server, writerToken)
	if res := callTool(t, writer, "edit_page", map[string]any{"slug": testHome, "basehash": homeHash, "edits": []any{map[string]any{"oldText": "Welcome", "newText": "Edited"}}}); res.IsError {
		t.Fatalf("write scope edit_page: %s", toolText(t, res))
	}
	if res := callTool(t, writer, "save_page", map[string]any{"slug": testNS + "/writer-note", "body": "hello"}); res.IsError {
		t.Fatalf("write scope save_page: %s", toolText(t, res))
	}
	if res := callTool(t, writer, "upload_attachment", map[string]any{"slug": testHome, "filename": "writer.pdf"}); res.IsError {
		t.Fatalf("write scope upload_attachment: %s", toolText(t, res))
	}
	if res := callTool(t, writer, "list_pages", nil); !res.IsError {
		t.Error("write scope listed pages")
	}

	manager := connectMCP(t, server, managerToken)
	if res := callTool(t, manager, "list_pages", nil); res.IsError {
		t.Fatalf("settings scope list_pages: %s", toolText(t, res))
	}
	if res := callTool(t, manager, "save_page", map[string]any{"slug": testNS + "/manager-note", "body": "nope"}); res.IsError {
		t.Fatalf("settings scope save_page: %s", toolText(t, res))
	}
}
