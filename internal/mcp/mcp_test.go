package mcp

import (
	"hmd/internal/testhttp"

	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
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

	token, err := authn.AddToken("admin", "test", time.Time{}, []string{"read", "write", "admin"}, nil)
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
	settings, err := app.auth.AddToken("admin", "settings-only", time.Time{}, []string{"admin"}, nil)
	if err != nil {
		t.Fatalf("settings token: %v", err)
	}
	return app, server, restricted, settings
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
		"move_page": true, "rename_page": true, "search": true, "backlinks": true, "recent_changes": true, "health": true,
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
		"list_pages": {"namespace", "offset", "limit"}, "list_namespaces": {},
		"read_page": {"slug", "section", "offset", "limit"}, "save_page": {"slug", "title", "tags", "body", "pin", "basehash"},
		"edit_page": {"slug", "basehash", "edits", "dryRun"}, "delete_page": {"slug", "basehash"},
		"move_page": {"slug", "new_slug", "basehash"}, "rename_page": {"slug", "title", "basehash"},
		"search": {"query", "namespace", "offset", "limit"}, "backlinks": {"slug"}, "recent_changes": {"limit"}, "health": {"namespace"},
		"read_namespace":    {"name"},
		"save_namespace":    {"name", "widgets", "public", "title", "description", "image", "skin", "palette", "index", "tree", "new", "export", "basehash"},
		"upload_attachment": {"slug", "filename"}, "read_attachment": {"slug", "filename", "offset", "limit"},
	}
	readOnly := map[string]bool{
		"list_pages": true, "list_namespaces": true, "read_page": true, "read_namespace": true, "read_attachment": true,
		"search": true, "search_attachments": true, "backlinks": true, "health": true, "recent_changes": true,
	}
	for _, tool := range result.Tools {
		if tool.Title == "" {
			t.Errorf("%s has no title", tool.Name)
		}
		annotations := tool.Annotations
		if annotations == nil || annotations.OpenWorldHint == nil || *annotations.OpenWorldHint {
			t.Errorf("%s annotations = %+v, want openWorldHint false", tool.Name, annotations)
		} else if annotations.ReadOnlyHint != readOnly[tool.Name] {
			t.Errorf("%s readOnlyHint = %v, want %v", tool.Name, annotations.ReadOnlyHint, readOnly[tool.Name])
		} else if !readOnly[tool.Name] && annotations.DestructiveHint == nil {
			t.Errorf("%s does not say whether it is destructive", tool.Name)
		} else if want := !readOnly[tool.Name] && tool.Name != "upload_attachment"; annotations.IdempotentHint != want {
			t.Errorf("%s idempotentHint = %v, want %v", tool.Name, annotations.IdempotentHint, want)
		}
		for kind, raw := range map[string]any{"input": tool.InputSchema, "output": tool.OutputSchema} {
			encoded, _ := json.Marshal(raw)
			if raw == nil || strings.Contains(string(encoded), `"null"`) {
				t.Errorf("%s %s schema is missing or allows null: %s", tool.Name, kind, encoded)
			}
		}
		args, ok := wantArgs[tool.Name]
		if !ok {
			continue
		}
		schema, _ := tool.InputSchema.(map[string]any)
		if schema["additionalProperties"] != false {
			t.Errorf("%s accepts unknown arguments: %#v", tool.Name, schema)
		}
		properties, _ := schema["properties"].(map[string]any)
		if len(properties) != len(args) {
			t.Errorf("%s has arguments %v, want %v", tool.Name, slices.Sorted(maps.Keys(properties)), args)
		}
		for _, arg := range args {
			if _, ok := properties[arg]; !ok {
				t.Errorf("%s schema missing %q: %#v", tool.Name, arg, schema)
			}
		}
	}
}

func TestMCPPageSlug(t *testing.T) {
	for _, tt := range []struct {
		slug, wantErr string
	}{
		{slug: "notes/page"},
		{slug: "notes/my_page"},
		{slug: "", wantErr: "needs a namespace and a page name"},
		{slug: "page", wantErr: "needs a namespace and a page name"},
		{slug: "../page", wantErr: `segment ".." starts with "."`},
		{slug: "notes/_draft", wantErr: `segment "_draft" starts with "_"`},
		{slug: "attachments/page", wantErr: `namespace "attachments" is reserved`},
		{slug: "notes//page", wantErr: "a segment is empty"},
	} {
		t.Run(tt.slug, func(t *testing.T) {
			got, err := mcpPageSlug(tt.slug)
			if tt.wantErr == "" {
				if err != nil || got != tt.slug {
					t.Fatalf("mcpPageSlug(%q) = %q, %v; want the slug", tt.slug, got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "for example notes/my-page") {
				t.Fatalf("mcpPageSlug(%q) error = %v; want it to contain %q and an example", tt.slug, err, tt.wantErr)
			}
		})
	}
}

func TestMCPUploadAttachment(t *testing.T) {
	app, server, token := newMCPTestAppWithApp(t, true)
	session := connectMCP(t, server, token)
	result := callTool(t, session, "upload_attachment", map[string]any{
		"slug": testHome, "filename": "source.txt",
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
	upload, err := testhttp.Post(t, http.DefaultClient, out.UploadURL, writer.FormDataContentType(), body)
	if err != nil {
		t.Fatalf("posting upload: %v", err)
	}
	{
		response := upload
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}
		}()
	}
	if upload.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want %d", upload.StatusCode, http.StatusOK)
	}
	attachment, ok := app.store.FindAttachment("notes/readme", "source.txt")
	got, _, err := app.store.Read(attachment.Path)
	if !ok || err != nil || string(got) != "PDF content" {
		t.Fatalf("uploaded attachment = %q, %v", got, err)
	}
}

func TestMCPReadAttachment(t *testing.T) {
	app, server, token := newMCPTestAppWithApp(t, true)
	source := []byte("source text")
	extracted := search.EncodeExtractedAttachment(search.AttachmentBlobHash(source), "extracted text")
	if _, err := app.store.SaveAttachment(testHome, "source.txt", source, extracted, "Add attachment source.txt", "test", "test@hmd.local"); err != nil {
		t.Fatalf("saving attachment: %v", err)
	}
	session := connectMCP(t, server, token)
	result := callTool(t, session, "read_attachment", map[string]any{"slug": testHome, "filename": "source.txt"})
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

	// An update keeps every field it omits, and an empty new object removes
	// the new-page template.
	res = callTool(t, session, "save_namespace", map[string]any{
		"name": testNS, "palette": "nord", "new": map[string]any{"template": "", "slug": ""}, "basehash": saved.Hash,
	})
	if res.IsError {
		t.Fatalf("partial save_namespace: %s", toolText(t, res))
	}
	var partial mcpNamespaceOut
	toolJSON(t, res, &partial)
	if partial.Palette != "nord" || !partial.Public || partial.Title != "Notes" || partial.Skin != "newsprint" ||
		!slices.Equal(partial.Widgets, []string{"pages"}) || !slices.Equal(partial.Tree, []string{"guides", "reference"}) || partial.New != nil {
		t.Errorf("partial save_namespace = %+v", partial)
	}
	if bad := callTool(t, session, "save_namespace", map[string]any{"name": testNS, "skin": "neon", "basehash": partial.Hash}); !bad.IsError {
		t.Error("save_namespace accepted an unknown skin")
	}

	res = callTool(t, session, "save_namespace", map[string]any{"name": "blog", "public": true})
	if res.IsError {
		t.Fatalf("create namespace: %s", toolText(t, res))
	}
}

func TestMCPDisabledRouteNotRegistered(t *testing.T) {
	server, token := newMCPTestApp(t, false)

	req, _ := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/_/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	{
		response := resp
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}
		}()
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("disabled /mcp: got status %d, want 404", resp.StatusCode)
	}
}

func TestMCPWithoutBearer401JSON(t *testing.T) {
	server, _ := newMCPTestApp(t, true)

	resp, err := testhttp.Post(t, http.DefaultClient, server.URL+"/_/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	{
		response := resp
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}
		}()
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated /mcp: got status %d, want 401", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("unauthenticated /mcp: got Content-Type %q, want application/json", ct)
	}
}

func TestMCPInvalidBearer401(t *testing.T) {
	server, _ := newMCPTestApp(t, true)

	req, _ := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/_/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer hmd_bogus")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	{
		response := resp
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}
		}()
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("invalid Bearer: got status %d, want 401", resp.StatusCode)
	}
}

func TestMCPRequiresModernRequestMetadata(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/_/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`))
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
	{
		response := resp
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}
		}()
	}
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
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
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
	{
		response := resp
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}
		}()
	}
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
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
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
	{
		response := resp
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}
		}()
	}
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
			type replyMetadata struct{ StatusCode int }
			post := func(body, version string) (replyMetadata, []byte) {
				t.Helper()
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
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
				{
					response := resp
					defer func() {
						if err := response.Body.Close(); err != nil {
							t.Errorf("closing response body: %v", err)
						}
					}()
				}
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
				return replyMetadata{StatusCode: resp.StatusCode}, responseBody
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
				req, err := http.NewRequestWithContext(t.Context(), method, server.URL+"/_/mcp", nil)
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
				if err := resp.Body.Close(); err != nil {
					t.Errorf("closing response body: %v", err)
				}
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
			req, err := http.NewRequestWithContext(t.Context(), method, server.URL+"/_/mcp", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			{
				response := resp
				defer func() {
					if err := response.Body.Close(); err != nil {
						t.Errorf("closing response body: %v", err)
					}
				}()
			}
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
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
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
	{
		response := resp
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}
		}()
	}
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
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/_/mcp?sessionId=session", strings.NewReader(body))
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
	{
		response := resp
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}
		}()
	}
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
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
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
	{
		response := resp
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}
		}()
	}
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
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
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
	{
		response := resp
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}
		}()
	}
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
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
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
	{
		response := resp
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}
		}()
	}
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
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
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
	{
		response := resp
		defer func() {
			if err := response.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}
		}()
	}
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
		"slug":  testNS + "/agent-note",
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

	if alias := callTool(t, session, "read_page", map[string]any{"path": testNS + "/agent-note"}); !alias.IsError {
		t.Fatal("read with the removed path argument succeeded")
	}
	res = callTool(t, session, "read_page", map[string]any{"slug": testNS + "/agent-note"})
	if res.IsError {
		t.Fatalf("read failed: %s", toolText(t, res))
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
	var conflict struct {
		Error, Hash string
		Body        *string
	}
	toolJSON(t, res, &conflict)
	if conflict.Hash != created.Hash || !strings.HasPrefix(conflict.Error, "conflict:") ||
		conflict.Body == nil || *conflict.Body != "Hello from an agent. See [[readme]]." {
		t.Errorf("conflict payload mismatch: %+v", conflict)
	}

	// An update keeps every field it omits: here title, tags and pin.
	res = callTool(t, session, "save_page", map[string]any{
		"slug": testNS + "/agent-note", "body": "Updated. See [[" + testNS + "]].", "basehash": created.Hash,
	})
	if res.IsError {
		t.Fatalf("update failed: %s", toolText(t, res))
	}
	var updated mcpSaveOut
	toolJSON(t, res, &updated)
	res = callTool(t, session, "save_page", map[string]any{
		"slug": testNS + "/agent-note", "pin": true, "basehash": updated.Hash,
	})
	if res.IsError {
		t.Fatalf("metadata-only update failed: %s", toolText(t, res))
	}
	toolJSON(t, res, &updated)
	res = callTool(t, session, "read_page", map[string]any{"slug": testNS + "/agent-note"})
	toolJSON(t, res, &page)
	if page.Title != "Agent note" || !slices.Equal(page.Tags, []string{"ai"}) || !page.Pin || page.Body != "Updated. See [["+testNS+"]]." {
		t.Errorf("partial updates did not keep omitted fields: %+v", page)
	}
	if missing := callTool(t, session, "save_page", map[string]any{"slug": testNS + "/no-body"}); !missing.IsError {
		t.Error("create without a body succeeded")
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

	res = callTool(t, session, "delete_page", map[string]any{"slug": testNS + "/agent-note", "basehash": created.Hash})
	toolJSON(t, res, &conflict)
	if !res.IsError || conflict.Hash != page.Hash {
		t.Fatalf("delete with a stale basehash = %s, want a conflict with the current hash", toolText(t, res))
	}
	res = callTool(t, session, "delete_page", map[string]any{"slug": testNS + "/agent-note", "basehash": page.Hash})
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
		{"manager", "admin"},
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
	managerToken, err := app.auth.AddToken("manager", "mcp", time.Time{}, []string{"admin"}, nil)
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

func TestMCPSearchAndListFilters(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	session := connectMCP(t, server, token)
	for _, slug := range []string{"notes/alpha", "notes/beta", "notes/gamma", "blog/delta"} {
		res := callTool(t, session, "save_page", map[string]any{"slug": slug, "body": "shared marker text"})
		if res.IsError {
			t.Fatalf("save_page %s: %s", slug, toolText(t, res))
		}
	}

	var search mcpSearchOut
	toolJSON(t, callTool(t, session, "search", map[string]any{"query": "marker", "limit": 2}), &search)
	if len(search.Hits) != 2 || !search.More {
		t.Errorf("search with limit 2 = %+v, want 2 hits and more", search)
	}
	toolJSON(t, callTool(t, session, "search", map[string]any{"query": "marker", "namespace": "blog"}), &search)
	if len(search.Hits) != 1 || search.Hits[0].Slug != "blog/delta" || search.More || search.Hits[0].Tags == nil {
		t.Errorf("search in blog = %+v, want only blog/delta with an empty tag list", search)
	}
	if res := callTool(t, session, "search", map[string]any{"query": "marker", "limit": 500}); !res.IsError {
		t.Error("search accepted a limit above the maximum")
	}

	var list mcpListOut
	toolJSON(t, callTool(t, session, "list_pages", map[string]any{"namespace": "blog"}), &list)
	if len(list.Pages) != 1 || list.Pages[0].Slug != "blog/delta" {
		t.Errorf("list_pages in blog = %+v", list.Pages)
	}
	if res := callTool(t, session, "list_pages", map[string]any{"unexpected": true}); !res.IsError {
		t.Error("list_pages accepted an unknown argument")
	}
	if res := callTool(t, session, "recent_changes", map[string]any{"limit": 100000}); !res.IsError {
		t.Error("recent_changes accepted a limit above the maximum")
	}
	if res := callTool(t, session, "edit_page", map[string]any{"slug": "notes/alpha", "basehash": "x", "edits": []any{}}); !res.IsError {
		t.Error("edit_page accepted an empty edits list")
	}
}

func TestMCPTextWindow(t *testing.T) {
	text, window, err := windowText("héllo world", 0, 5)
	if err != nil || text != "héllo" || window.NextOffset != 5 || window.TotalChars != 11 || !window.Truncated {
		t.Errorf("first window = %q %+v, %v", text, window, err)
	}
	text, window, err = windowText("héllo world", 6, 100)
	if err != nil || text != "world" || window.NextOffset != 11 || window.Truncated {
		t.Errorf("last window = %q %+v, %v", text, window, err)
	}
	text, window, err = windowText("", 0, 0)
	if err != nil || text != "" || window.Truncated {
		t.Errorf("empty text = %q %+v, %v", text, window, err)
	}
	if _, _, err := windowText("short", 6, 0); err == nil || !strings.Contains(err.Error(), "5 characters") {
		t.Errorf("window past the end error = %v, want one naming the length", err)
	}
}

func TestMCPReadPageSectionAndWindow(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	session := connectMCP(t, server, token)
	body := "Intro\n\n# Setup\n\nInstall it.\n\n## Detail\n\nMore.\n\n# Usage\n\nRun it.\n"
	if res := callTool(t, session, "save_page", map[string]any{"slug": "notes/guide", "body": body}); res.IsError {
		t.Fatalf("save_page: %s", toolText(t, res))
	}

	var page mcpPageOut
	toolJSON(t, callTool(t, session, "read_page", map[string]any{"slug": "notes/guide", "section": "setup"}), &page)
	if page.Body != "# Setup\n\nInstall it.\n\n## Detail\n\nMore.\n\n" || page.Truncated || page.Hash == "" {
		t.Errorf("read_page section = %+v", page)
	}
	toolJSON(t, callTool(t, session, "read_page", map[string]any{"slug": "notes/guide", "offset": 2, "limit": 3}), &page)
	if page.Body != "tro" || !page.Truncated || page.NextOffset != 5 || page.TotalChars < len("Intro") {
		t.Errorf("read_page window = %+v", page)
	}
	if res := callTool(t, session, "read_page", map[string]any{"slug": "notes/guide", "offset": 1000}); !res.IsError {
		t.Errorf("read_page past the end = %s, want an error", toolText(t, res))
	}
	res := callTool(t, session, "read_page", map[string]any{"slug": "notes/guide", "section": "Missing"})
	if !res.IsError || !strings.Contains(toolText(t, res), `"Setup", "Detail", "Usage"`) {
		t.Errorf("missing section error = %s, want the page's headings", toolText(t, res))
	}
}

func TestMCPPagination(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	session := connectMCP(t, server, token)
	for _, slug := range []string{"notes/alpha", "notes/beta", "notes/gamma"} {
		if res := callTool(t, session, "save_page", map[string]any{"slug": slug, "body": "paged marker text"}); res.IsError {
			t.Fatalf("save_page %s: %s", slug, toolText(t, res))
		}
	}

	var list mcpListOut
	toolJSON(t, callTool(t, session, "list_pages", map[string]any{"limit": 2}), &list)
	if len(list.Pages) != 2 || !list.More || list.NextOffset != 2 {
		t.Fatalf("first list window = %+v", list)
	}
	seen := []string{list.Pages[0].Slug, list.Pages[1].Slug}
	var rest mcpListOut
	toolJSON(t, callTool(t, session, "list_pages", map[string]any{"offset": list.NextOffset, "limit": 2}), &rest)
	if len(rest.Pages) != 2 || rest.More {
		t.Fatalf("last list window = %+v", rest)
	}
	seen = append(seen, rest.Pages[0].Slug, rest.Pages[1].Slug)
	if !slices.IsSorted(seen) || len(slices.Compact(slices.Clone(seen))) != 4 {
		t.Errorf("list windows = %v, want four distinct slugs in order", seen)
	}

	var search mcpSearchOut
	toolJSON(t, callTool(t, session, "search", map[string]any{"query": "paged", "limit": 2}), &search)
	if len(search.Hits) != 2 || !search.More || search.NextOffset != 2 {
		t.Fatalf("first search window = %+v", search)
	}
	var next mcpSearchOut
	toolJSON(t, callTool(t, session, "search", map[string]any{"query": "paged", "offset": search.NextOffset, "limit": 2}), &next)
	if len(next.Hits) != 1 || next.More || slices.ContainsFunc(search.Hits, func(hit mcpSearchHit) bool { return hit.Slug == next.Hits[0].Slug }) {
		t.Errorf("second search window = %+v after %+v", next, search)
	}
}

func TestMCPMoveAndRename(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	session := connectMCP(t, server, token)

	var saved mcpSaveOut
	toolJSON(t, callTool(t, session, "save_page", map[string]any{
		"slug": testNS + "/target", "title": "Target", "tags": []string{"keep"}, "body": "target body", "pin": true,
	}), &saved)
	callTool(t, session, "save_page", map[string]any{"slug": testNS + "/linker", "title": "Linker", "body": "See [[Target]]."})

	res := callTool(t, session, "move_page", map[string]any{"slug": testNS + "/target", "new_slug": testNS + "/moved", "basehash": "0000000000000000000000000000000000000000"})
	var conflict struct{ Error, Hash string }
	toolJSON(t, res, &conflict)
	if !res.IsError || conflict.Hash != saved.Hash {
		t.Fatalf("move with a stale basehash = %s, want a conflict with the current hash", toolText(t, res))
	}
	res = callTool(t, session, "move_page", map[string]any{"slug": testNS + "/target", "new_slug": testNS + "/linker", "basehash": saved.Hash})
	if !res.IsError {
		t.Fatal("move onto an existing page succeeded, want conflict")
	}

	res = callTool(t, session, "move_page", map[string]any{"slug": testNS + "/target", "new_slug": testNS + "/moved", "basehash": saved.Hash})
	if res.IsError {
		t.Fatalf("move failed: %s", toolText(t, res))
	}
	var moved mcpSaveOut
	toolJSON(t, res, &moved)
	var page mcpPageOut
	toolJSON(t, callTool(t, session, "read_page", map[string]any{"slug": testNS + "/moved"}), &page)
	if page.Title != "Target" || page.Body != "target body" || !page.Pin || len(page.Tags) != 1 || page.Tags[0] != "keep" || page.Hash != moved.Hash {
		t.Errorf("moved page = %+v, want metadata kept", page)
	}
	if res := callTool(t, session, "read_page", map[string]any{"slug": testNS + "/target"}); !res.IsError {
		t.Error("old slug still readable after move")
	}

	res = callTool(t, session, "rename_page", map[string]any{"slug": testNS + "/moved", "title": "Renamed", "basehash": "0000000000000000000000000000000000000000"})
	if !res.IsError {
		t.Fatal("rename with a stale basehash succeeded, want conflict")
	}
	res = callTool(t, session, "rename_page", map[string]any{"slug": testNS + "/moved", "title": "Renamed", "basehash": moved.Hash})
	if res.IsError {
		t.Fatalf("rename failed: %s", toolText(t, res))
	}
	var renamed mcpRenameOut
	toolJSON(t, res, &renamed)
	if renamed.Slug != testNS+"/renamed" || len(renamed.FailedLinks) != 0 {
		t.Errorf("rename result = %+v", renamed)
	}
	toolJSON(t, callTool(t, session, "read_page", map[string]any{"slug": testNS + "/linker"}), &page)
	if page.Body != "See [[Renamed]]." {
		t.Errorf("linker body = %q, want the link rewritten", page.Body)
	}
}

func TestMCPMoveAndRenameRejectUncheckedWrites(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	session := connectMCP(t, server, token)
	callTool(t, session, "save_page", map[string]any{"slug": testNS + "/page", "title": "Page", "body": "body"})

	for name, args := range map[string]map[string]any{
		"move missing":      {"slug": testNS + "/missing", "new_slug": testNS + "/x", "basehash": "abc"},
		"move empty hash":   {"slug": testNS + "/page", "new_slug": testNS + "/x", "basehash": ""},
		"move to same slug": {"slug": testNS + "/page", "new_slug": testNS + "/page", "basehash": "abc"},
		"rename empty hash": {"slug": testNS + "/page", "title": "Other", "basehash": ""},
	} {
		tool := "move_page"
		if strings.HasPrefix(name, "rename") {
			tool = "rename_page"
		}
		if res := callTool(t, session, tool, args); !res.IsError {
			t.Errorf("%s succeeded: %s", name, toolText(t, res))
		}
	}
	if res := callTool(t, session, "read_page", map[string]any{"slug": testNS + "/x"}); !res.IsError {
		t.Error("a rejected move created the destination page")
	}
	if res := callTool(t, session, "read_page", map[string]any{"slug": testNS + "/page"}); res.IsError {
		t.Error("a rejected write moved the page")
	}
}
