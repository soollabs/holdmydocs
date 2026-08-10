package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// bearerTransport adds a PAT to every request the MCP client makes.
type bearerTransport struct{ token string }

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}

// newMCPTestApp starts an app (repo seeded with readme.md) and mints a PAT
// for the admin user.
func newMCPTestApp(t *testing.T, mcpEnabled bool) (*httptest.Server, string) {
	t.Helper()
	_, server, token := newMCPTestAppWithApp(t, mcpEnabled)
	return server, token
}

func newMCPTestAppWithApp(t *testing.T, mcpEnabled bool) (*App, *httptest.Server, string) {
	t.Helper()

	cfg := Config{
		RepoDir:   t.TempDir(),
		AppDir:    t.TempDir(),
		Git:       GitConfig{User: "test"},
		AdminUser: "admin",
		AdminPass: "test",
		MCP:       MCPConfig{Enabled: mcpEnabled},
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	nsCfg, err := NamespaceConfig{Widgets: builtinWidgets, Index: defaultIndexPage}.Encode()
	if err != nil {
		t.Fatalf("encoding namespace config: %v", err)
	}
	if _, err := store.Save(namespaceConfigPath(testNS), nsCfg, "Configure namespace "+testNS, cfg.Git.User, cfg.Git.User+"@hmd.local"); err != nil {
		t.Fatalf("seeding namespace config: %v", err)
	}
	home := Page{Slug: testHome, Title: testNS, Body: defaultHomeMD}
	if _, err := store.Save(pageFile(testHome), home.Encode(), "Add "+testHome, cfg.Git.User, cfg.Git.User+"@hmd.local"); err != nil {
		t.Fatalf("seeding %s: %v", testHome, err)
	}
	store.NeedsSetup.Store(false)

	index, err := BuildIndex([]Page{home})
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

	namespaces, err := BuildNamespaceRegistry(cfg.RepoDir)
	if err != nil {
		t.Fatalf("BuildNamespaceRegistry failed: %v", err)
	}

	app := &App{Store: store, Auth: auth, Index: index, Render: NewRenderer(index.ResolveLink), Tmpl: tmpl}
	app.SetConfig(cfg)
	app.SetNamespaces(namespaces)

	server := httptest.NewServer(auth.Middleware(app.Routes()))
	t.Cleanup(server.Close)

	token, err := auth.AddToken("admin", "test", time.Time{}, nil, nil)
	if err != nil {
		t.Fatalf("AddToken failed: %v", err)
	}
	return app, server, token
}

func newMCPRestrictedTestApp(t *testing.T) (*App, *httptest.Server, string, string) {
	t.Helper()
	app, server, _ := newMCPTestAppWithApp(t, true)

	seedPage := func(page Page) {
		t.Helper()
		if _, err := app.Store.Save(pageFile(page.Slug), page.Encode(), "Seed "+page.Slug, "admin", "admin@hmd.local"); err != nil {
			t.Fatalf("seeding %s: %v", page.Slug, err)
		}
		if err := app.Index.Update(page); err != nil {
			t.Fatalf("indexing %s: %v", page.Slug, err)
		}
	}
	seedPage(Page{Slug: "notes/allowed", Title: "Allowed", Body: "shared notes content"})
	seedPage(Page{Slug: "private/denied", Title: "Denied", Body: "shared private content"})

	for _, name := range []string{"notes", "empty"} {
		if _, err := app.Store.Save(namespaceConfigPath(name), []byte("public: true\n"), "Configure namespace "+name, "admin", "admin@hmd.local"); err != nil {
			t.Fatalf("seeding %s namespace: %v", name, err)
		}
	}
	app.refreshNamespaces()

	restricted, err := app.Auth.AddToken("admin", "notes-only", time.Time{}, []string{"read", "write"}, []string{"notes"})
	if err != nil {
		t.Fatalf("restricted token: %v", err)
	}
	settings, err := app.Auth.AddToken("admin", "settings-only", time.Time{}, []string{"settings"}, nil)
	if err != nil {
		t.Fatalf("settings token: %v", err)
	}
	return app, server, restricted, settings
}

// connectMCP opens an MCP session against server using token.
func connectMCP(t *testing.T, server *httptest.Server, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
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

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

// toolText returns the first text content of a tool result.
func toolText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	t.Fatalf("no text content in result: %+v", res)
	return ""
}

// toolJSON unmarshals the first text content of a tool result into out.
func toolJSON(t *testing.T, res *mcp.CallToolResult, out any) {
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
		"list_pages": true, "read_page": true, "save_page": true, "delete_page": true,
		"search": true, "backlinks": true, "recent_changes": true, "health": true,
		"list_namespaces": true, "read_namespace": true, "save_namespace": true,
		"upload_attachment": true,
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

func TestMCPUploadAttachment(t *testing.T) {
	app, server, token := newMCPTestAppWithApp(t, true)
	session := connectMCP(t, server, token)
	content := []byte("PDF content")
	result := callTool(t, session, "upload_attachment", map[string]any{
		"slug": testHome, "filename": "Quarterly Report.PDF", "content_base64": base64.StdEncoding.EncodeToString(content),
	})
	if result.IsError {
		t.Fatalf("upload_attachment: %s", toolText(t, result))
	}
	var out mcpAttachmentUploadOut
	toolJSON(t, result, &out)
	if out.URL != "/_/attachments/notes/readme/quarterly-report.pdf" || out.Indexed {
		t.Fatalf("upload result = %#v", out)
	}
	got, _, err := app.Store.Read("attachments/notes/readme/quarterly-report.pdf")
	if err != nil || string(got) != string(content) {
		t.Fatalf("uploaded attachment = %q, %v", got, err)
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
		"title": "Notes", "description": "Personal notes", "skin": "newsprint", "index": defaultIndexPage,
		"basehash": current.Hash,
	})
	if res.IsError {
		t.Fatalf("save_namespace: %s", toolText(t, res))
	}
	var saved mcpNamespaceOut
	toolJSON(t, res, &saved)
	if saved.Hash == "" || !saved.Public || saved.Title != "Notes" || saved.Description != "Personal notes" || saved.Palette != "" {
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

func TestMCPRecentChangesOnlyAllowsPageFiles(t *testing.T) {
	for path, want := range map[string]bool{
		"notes/page.md": true, ".wiki.yaml": false, "notes/.namespace.yaml": false,
		".notes/template.md": false, "attachments/notes/page/file.png": false,
	} {
		got := mcpCommitAllowed(context.Background(), CommitDetail{Files: []string{path}})
		if got != want {
			t.Errorf("mcpCommitAllowed(%q) = %v, want %v", path, got, want)
		}
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

func TestMCPRejectsLegacyInitialise(t *testing.T) {
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
		t.Fatalf("legacy initialise: got status %d, want %d: %s", resp.StatusCode, http.StatusNotFound, responseBody)
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
		t.Errorf("legacy initialise: got error code %d, want -32601", response.Error.Code)
	}
}

func TestMCPAcceptsLegacyInitialise(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	body := `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"roots":{}},"clientInfo":{"name":"opencode","version":"1.18.9"}}}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/_/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy initialise: got status %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if resp.Header.Get("Mcp-Session-Id") == "" {
		t.Fatal("legacy initialise response missing Mcp-Session-Id")
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

func TestMCPRejectsLegacySessionHeader(t *testing.T) {
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
	req.Header.Set("Mcp-Session-Id", "legacy")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("legacy session header: got status %d, want %d", resp.StatusCode, http.StatusBadRequest)
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
		t.Errorf("legacy session header: got error code %d, want -32020", response.Error.Code)
	}
}

func TestMCPRejectsLegacySessionQuery(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/_/mcp?sessionId=legacy", strings.NewReader(body))
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
		t.Fatalf("legacy session query: got status %d, want %d", resp.StatusCode, http.StatusBadRequest)
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
		t.Errorf("legacy session query: got error code %d, want -32020", response.Error.Code)
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
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "block", Description: "Wait for cancellation."}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, nil, ctx.Err()
	})
	httpServer := httptest.NewServer(newMCPHTTPHandler(func(*http.Request) *mcp.Server { return server }))
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

	// Create (empty basehash).
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

	// Create on an existing page must fail.
	res = callTool(t, session, "save_page", map[string]any{
		"slug": testNS + "/agent-note", "title": "Agent note", "body": "clobber",
	})
	if !res.IsError {
		t.Fatal("create over existing page succeeded, want conflict")
	}

	// Read round-trips body, tags and hash.
	res = callTool(t, session, "read_page", map[string]any{"slug": testNS + "/agent-note"})
	if res.IsError {
		t.Fatalf("read failed: %s", toolText(t, res))
	}
	var page mcpPageOut
	toolJSON(t, res, &page)
	if page.Body != "Hello from an agent. See [[readme]]." || page.Title != "Agent note" || page.Hash != created.Hash {
		t.Errorf("read_page mismatch: %+v (want hash %s)", page, created.Hash)
	}

	// Stale basehash conflicts, carrying the current hash and body.
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

	// Fresh basehash saves.
	res = callTool(t, session, "save_page", map[string]any{
		"slug": testNS + "/agent-note", "title": "Agent note", "body": "Updated. See [[" + testNS + "]].", "basehash": created.Hash,
	})
	if res.IsError {
		t.Fatalf("update failed: %s", toolText(t, res))
	}

	// list_pages and backlinks see the new page.
	res = callTool(t, session, "list_pages", nil)
	if !strings.Contains(toolText(t, res), "agent-note") {
		t.Error("list_pages missing agent-note")
	}
	res = callTool(t, session, "backlinks", map[string]any{"slug": testHome})
	if !strings.Contains(toolText(t, res), "agent-note") {
		t.Errorf("backlinks(%s) missing agent-note", testHome)
	}

	// search finds it.
	res = callTool(t, session, "search", map[string]any{"query": "Updated"})
	if !strings.Contains(toolText(t, res), "agent-note") {
		t.Error("search missing agent-note")
	}

	// recent_changes: newest commit is the update, attributed to the PAT's
	// owner, touching the page file.
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

	// Delete, then read 404s.
	res = callTool(t, session, "delete_page", map[string]any{"slug": testNS + "/agent-note"})
	if res.IsError {
		t.Fatalf("delete failed: %s", toolText(t, res))
	}
	res = callTool(t, session, "read_page", map[string]any{"slug": testNS + "/agent-note"})
	if !res.IsError {
		t.Error("read after delete succeeded, want error")
	}

	// Traversal-shaped slugs are rejected.
	res = callTool(t, session, "read_page", map[string]any{"slug": "../users"})
	if !res.IsError {
		t.Error("traversal slug accepted, want error")
	}
}

func TestMCPHealth(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	session := connectMCP(t, server, token)

	// A dangling link in one namespace, the same in another, and an unlinked
	// (orphan) page alongside.
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

	// Unscoped: sees problems from both namespaces.
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

	// Scoped to "notes": only the namespaced dangling link and orphan.
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
		{"delete_page", map[string]any{"slug": "private/denied"}},
		{"upload_attachment", map[string]any{"slug": "private/denied", "filename": "denied.pdf", "content_base64": "eA=="}},
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
		if err := app.Auth.AddUser(user.name, "secret"); err != nil {
			t.Fatalf("AddUser(%s): %v", user.name, err)
		}
		if err := app.Auth.SetScopes(user.name, []string{user.scope}); err != nil {
			t.Fatalf("SetScopes(%s): %v", user.name, err)
		}
	}
	readerToken, err := app.Auth.AddToken("reader", "mcp", time.Time{}, nil, nil)
	if err != nil {
		t.Fatalf("reader token: %v", err)
	}
	writerToken, err := app.Auth.AddToken("writer", "mcp", time.Time{}, nil, nil)
	if err != nil {
		t.Fatalf("writer token: %v", err)
	}
	managerToken, err := app.Auth.AddToken("manager", "mcp", time.Time{}, []string{"settings"}, nil)
	if err != nil {
		t.Fatalf("manager token: %v", err)
	}

	reader := connectMCP(t, server, readerToken)
	if res := callTool(t, reader, "list_pages", nil); res.IsError {
		t.Fatalf("read scope list_pages: %s", toolText(t, res))
	}
	if res := callTool(t, reader, "save_page", map[string]any{"slug": "nope", "body": "nope"}); !res.IsError {
		t.Error("read scope saved a page")
	}
	if res := callTool(t, reader, "upload_attachment", map[string]any{"slug": testHome, "filename": "reader.pdf", "content_base64": "eA=="}); !res.IsError {
		t.Error("read scope uploaded an attachment")
	}

	writer := connectMCP(t, server, writerToken)
	if res := callTool(t, writer, "save_page", map[string]any{"slug": testNS + "/writer-note", "body": "hello"}); res.IsError {
		t.Fatalf("write scope save_page: %s", toolText(t, res))
	}
	if res := callTool(t, writer, "upload_attachment", map[string]any{"slug": testHome, "filename": "writer.pdf", "content_base64": "eA=="}); res.IsError {
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

var tokenRe = regexp.MustCompile(`hmd_[0-9a-f]{64}`)

// createTokenViaUI posts the settings token form and extracts the minted
// token from the rendered page.
func createTokenViaUI(t *testing.T, server *httptest.Server, client *http.Client, label, expiry string, scopes, namespaces []string) string {
	t.Helper()
	values := url.Values{"label": {label}, "expiry": {expiry}, "scopes": scopes, "namespaces": namespaces}
	resp, err := client.PostForm(server.URL+"/_/settings/tokens", values)
	if err != nil {
		t.Fatalf("POST /_/settings/tokens: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	body, _ := io.ReadAll(resp.Body)
	token := tokenRe.FindString(string(body))
	if token == "" {
		t.Fatalf("no token in settings response (status %d)", resp.StatusCode)
	}
	return token
}

func TestTokenSettingsUI(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	for _, page := range []Page{
		{Slug: "notes/allowed", Title: "Allowed", Body: "only notes"},
		{Slug: "private/denied", Title: "Denied", Body: "private"},
	} {
		if _, err := app.Store.Save(pageFile(page.Slug), page.Encode(), "Add "+page.Slug, "test", "test@hmd.local"); err != nil {
			t.Fatal(err)
		}
		if err := app.Index.Update(page); err != nil {
			t.Fatal(err)
		}
	}
	app.refreshNamespaces()

	token := createTokenViaUI(t, server, client, "laptop", "30d", []string{"read"}, []string{"notes"})
	privateToken := createTokenViaUI(t, server, client, "private-pages", "30d", []string{"read"}, []string{"private"})

	// The token authenticates API requests.
	req, _ := http.NewRequest("GET", server.URL+"/_/api/search?q=readme", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Bearer request failed: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Bearer /api/search: got %d, want 200", resp.StatusCode)
	}

	pageReq, _ := http.NewRequest(http.MethodGet, server.URL+"/notes/allowed", nil)
	pageReq.Header.Set("Authorization", "Bearer "+token)
	pageResp, err := http.DefaultClient.Do(pageReq)
	if err != nil {
		t.Fatalf("selected namespace request failed: %v", err)
	}
	closeTestBody(t, pageResp.Body)
	if pageResp.StatusCode != http.StatusOK {
		t.Fatalf("selected namespace request = %d, want 200", pageResp.StatusCode)
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/private/denied", http.StatusOK},
		{"/notes/allowed", http.StatusForbidden},
	} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+privateToken)
		privateResp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("private-only request %s failed: %v", tc.path, err)
		}
		closeTestBody(t, privateResp.Body)
		if privateResp.StatusCode != tc.want {
			t.Errorf("private-only request %s = %d, want %d", tc.path, privateResp.StatusCode, tc.want)
		}
	}

	allNamespacesToken := createTokenViaUI(t, server, client, "all-pages", "30d", []string{"read"}, nil)
	adminToken := createTokenViaUI(t, server, client, "administrator", "30d", []string{"settings"}, []string{"notes"})
	settingsRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/_/settings", nil)
	for _, cookie := range client.Jar.Cookies(settingsRequest.URL) {
		settingsRequest.AddCookie(cookie)
	}
	settingsData := app.settingsData(settingsRequest)

	findToken := func(name string) TokenView {
		t.Helper()
		for _, view := range settingsData.Tokens {
			if view.Name == name {
				return view
			}
		}
		t.Fatalf("settings data missing token %q", name)
		return TokenView{}
	}
	selected := findToken("laptop")
	if len(selected.Scopes) != 1 || selected.Scopes[0] != "read" {
		t.Errorf("selected token scopes = %v, want [read]", selected.Scopes)
	}
	if len(selected.Namespaces) != 1 || selected.Namespaces[0] != "notes" {
		t.Errorf("selected token namespaces = %v, want [notes]", selected.Namespaces)
	}
	if selected.NamespaceLabel != "notes only" {
		t.Errorf("selected token namespace label = %q, want notes only", selected.NamespaceLabel)
	}
	if selected.ScopeLabel != "read" {
		t.Errorf("selected token scope label = %q, want read", selected.ScopeLabel)
	}
	if priv := findToken("private-pages"); len(priv.Namespaces) != 1 || priv.Namespaces[0] != "private" || priv.NamespaceLabel != "private only" {
		t.Errorf("private token = %#v, want private-only namespace access", priv)
	}
	if all := findToken("all-pages"); all.NamespaceLabel != "All namespaces" {
		t.Errorf("unrestricted token namespace label = %q, want All namespaces", all.NamespaceLabel)
	}
	if admin := findToken("administrator"); admin.NamespaceLabel != "Administrator" {
		t.Errorf("settings token namespace label = %q, want Administrator", admin.NamespaceLabel)
	}
	if len(settingsData.TokenNamespaces) != 2 || settingsData.TokenNamespaces[0] != "notes" || settingsData.TokenNamespaces[1] != "private" {
		t.Errorf("token namespace catalogue = %v, want [notes private]", settingsData.TokenNamespaces)
	}

	for _, path := range []string{"/" + testHome, "/private/denied"} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+allNamespacesToken)
		allResp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("all-namespaces request %s failed: %v", path, err)
		}
		closeTestBody(t, allResp.Body)
		if allResp.StatusCode != http.StatusOK {
			t.Errorf("all-namespaces request %s = %d, want 200", path, allResp.StatusCode)
		}
	}

	postInvalid := func(values url.Values) string {
		t.Helper()
		invalidResp, err := client.PostForm(server.URL+"/_/settings/tokens", values)
		if err != nil {
			t.Fatalf("POST invalid token: %v", err)
		}
		defer closeTestBody(t, invalidResp.Body)
		body, err := io.ReadAll(invalidResp.Body)
		if err != nil {
			t.Fatalf("read invalid token response: %v", err)
		}
		if invalidResp.StatusCode != http.StatusOK {
			t.Fatalf("invalid token response status = %d, want 200", invalidResp.StatusCode)
		}
		return string(body)
	}
	for _, tc := range []struct {
		name   string
		values url.Values
		want   string
	}{
		{"unknown namespace", url.Values{"label": {"bad-namespace"}, "expiry": {"30d"}, "scopes": {"read"}, "namespaces": {"missing"}}, "unknown namespace"},
		{"unknown scope", url.Values{"label": {"bad-scope"}, "expiry": {"30d"}, "scopes": {"unknown"}}, "unknown scope"},
		{"missing scope", url.Values{"label": {"missing-scope"}, "expiry": {"30d"}}, "at least one scope"},
	} {
		body := postInvalid(tc.values)
		if !strings.Contains(body, tc.want) {
			t.Errorf("%s response missing %q: %s", tc.name, tc.want, body)
		}
		if tokenRe.FindString(body) != "" {
			t.Errorf("%s response exposed a token value", tc.name)
		}
	}
	if len(app.Auth.TokensFor("admin")) != 4 {
		t.Errorf("invalid forms created tokens: got %d, want 4", len(app.Auth.TokensFor("admin")))
	}

	for _, path := range []string{"/" + testHome, "/private/denied"} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		adminResp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("administrator request %s failed: %v", path, err)
		}
		closeTestBody(t, adminResp.Body)
		if adminResp.StatusCode != http.StatusOK {
			t.Errorf("administrator request %s = %d, want 200", path, adminResp.StatusCode)
		}
	}
	// Duplicate label is rejected.
	dupResp, err := client.PostForm(server.URL+"/_/settings/tokens", url.Values{"label": {"laptop"}, "expiry": {"30d"}, "scopes": {"read"}, "namespaces": {"notes"}})
	if err != nil {
		t.Fatalf("POST duplicate: %v", err)
	}
	dupBody, _ := io.ReadAll(dupResp.Body)
	if err := dupResp.Body.Close(); err != nil {
		t.Fatalf("closing duplicate-token response body: %v", err)
	}
	if !strings.Contains(string(dupBody), "already exists") {
		t.Error("duplicate label accepted, want error")
	}

	// The settings page lists it with its expiry date.
	settingsResp, _ := client.Get(server.URL + "/_/settings")
	pageBody, _ := io.ReadAll(settingsResp.Body)
	if err := settingsResp.Body.Close(); err != nil {
		t.Fatalf("closing settings response body: %v", err)
	}
	if !strings.Contains(string(pageBody), "laptop") {
		t.Error("settings page missing token row")
	}
	if !strings.Contains(string(pageBody), `name="namespaces" value="`+testNS+`"`) {
		t.Errorf("settings page missing the %s namespace toggle", testNS)
	}
	if !strings.Contains(string(pageBody), time.Now().Add(30*24*time.Hour).Format("2006-01-02")) {
		t.Error("settings page missing 30-day expiry date")
	}

	// Revoke, then the token no longer authenticates.
	revokeResp, err := client.PostForm(server.URL+"/_/settings/tokens/revoke", url.Values{"label": {"laptop"}})
	if err != nil {
		t.Fatalf("POST revoke: %v", err)
	}
	if err := revokeResp.Body.Close(); err != nil {
		t.Fatalf("closing revoke response body: %v", err)
	}

	req, _ = http.NewRequest("GET", server.URL+"/_/api/search?q=readme", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Bearer request failed: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked Bearer: got %d, want 401", resp.StatusCode)
	}
}

func TestTokenExpiry(t *testing.T) {
	cfg := Config{AppDir: t.TempDir(), RepoDir: t.TempDir(), AdminUser: "admin", AdminPass: "test"}
	auth, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}

	expired, err := auth.AddToken("admin", "old", time.Now().Add(-time.Minute), nil, nil)
	if err != nil {
		t.Fatalf("AddToken failed: %v", err)
	}
	if _, ok := auth.UserForBearer(expired); ok {
		t.Error("expired token verified, want rejection")
	}

	live, err := auth.AddToken("admin", "live", time.Now().Add(time.Hour), nil, nil)
	if err != nil {
		t.Fatalf("AddToken failed: %v", err)
	}
	if principal, ok := auth.UserForBearer(live); !ok || principal.User != "admin" {
		t.Errorf("live token: got (%#v, %v), want admin, true", principal, ok)
	}

	// Expiry is enforced on cache hits too, not just first verification.
	auth.mu.Lock()
	cached := auth.tokenCache[live]
	cached.expires = time.Now().Add(-time.Minute)
	auth.tokenCache[live] = cached
	auth.mu.Unlock()
	if _, ok := auth.UserForBearer(live); ok {
		t.Error("cache-expired token verified, want rejection")
	}
}
