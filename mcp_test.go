package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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

	cfg := Config{
		RepoDir:      t.TempDir(),
		AppDir:       t.TempDir(),
		Git:          GitConfig{User: "test"},
		AdminUser:    "admin",
		AdminPass:    "test",
		HomeFilename: "readme.md",
		MCP:          MCPConfig{Enabled: mcpEnabled},
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	if _, err := store.Save("readme.md", Page{Slug: "readme", Title: "readme", Body: defaultHomeMD}.Encode(), "Add readme.md", cfg.Git.User, cfg.Git.User+"@hmd.local"); err != nil {
		t.Fatalf("seeding readme.md: %v", err)
	}
	store.NeedsSetup.Store(false)

	index, err := BuildIndex([]Page{ParsePage("readme", []byte(defaultHomeMD))})
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

	app := &App{Store: store, Auth: auth, Index: index, Render: NewRenderer(index.Exists), Tmpl: tmpl}
	app.SetConfig(cfg)
	app.SetNamespaces(namespaces)

	server := httptest.NewServer(auth.Middleware(app.Routes()))
	t.Cleanup(server.Close)

	token, err := auth.AddToken("admin", "test", time.Time{})
	if err != nil {
		t.Fatalf("AddToken failed: %v", err)
	}
	return server, token
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

func TestMCPToolFlow(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	session := connectMCP(t, server, token)

	// Create (empty basehash).
	res := callTool(t, session, "save_page", map[string]any{
		"slug":  "agent-note",
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
		"slug": "agent-note", "title": "Agent note", "body": "clobber",
	})
	if !res.IsError {
		t.Fatal("create over existing page succeeded, want conflict")
	}

	// Read round-trips body, tags and hash.
	res = callTool(t, session, "read_page", map[string]any{"slug": "agent-note"})
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
		"slug": "agent-note", "title": "Agent note", "body": "stale write", "basehash": "0000000000000000000000000000000000000000",
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
		"slug": "agent-note", "title": "Agent note", "body": "Updated. See [[readme]].", "basehash": created.Hash,
	})
	if res.IsError {
		t.Fatalf("update failed: %s", toolText(t, res))
	}

	// list_pages and backlinks see the new page.
	res = callTool(t, session, "list_pages", nil)
	if !strings.Contains(toolText(t, res), "agent-note") {
		t.Error("list_pages missing agent-note")
	}
	res = callTool(t, session, "backlinks", map[string]any{"slug": "readme"})
	if !strings.Contains(toolText(t, res), "agent-note") {
		t.Error("backlinks(readme) missing agent-note")
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
	if len(head.Files) != 1 || head.Files[0] != "agent-note.md" {
		t.Errorf("head commit files mismatch: %v", head.Files)
	}

	// Delete, then read 404s.
	res = callTool(t, session, "delete_page", map[string]any{"slug": "agent-note"})
	if res.IsError {
		t.Fatalf("delete failed: %s", toolText(t, res))
	}
	res = callTool(t, session, "read_page", map[string]any{"slug": "agent-note"})
	if !res.IsError {
		t.Error("read after delete succeeded, want error")
	}

	// Traversal-shaped slugs are rejected.
	res = callTool(t, session, "read_page", map[string]any{"slug": "../users"})
	if !res.IsError {
		t.Error("traversal slug accepted, want error")
	}
}

var tokenRe = regexp.MustCompile(`hmd_[0-9a-f]{64}`)

// createTokenViaUI posts the settings token form and extracts the minted
// token from the rendered page.
func createTokenViaUI(t *testing.T, server *httptest.Server, client *http.Client, label, expiry string) string {
	t.Helper()
	resp, err := client.PostForm(server.URL+"/_/settings/tokens", url.Values{"label": {label}, "expiry": {expiry}})
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
	server, client := newTestApp(t)
	defer server.Close()

	token := createTokenViaUI(t, server, client, "laptop", "30d")

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

	// Duplicate label is rejected.
	dupResp, err := client.PostForm(server.URL+"/_/settings/tokens", url.Values{"label": {"laptop"}, "expiry": {"30d"}})
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
	pageResp, _ := client.Get(server.URL + "/_/settings")
	pageBody, _ := io.ReadAll(pageResp.Body)
	if err := pageResp.Body.Close(); err != nil {
		t.Fatalf("closing settings response body: %v", err)
	}
	if !strings.Contains(string(pageBody), "laptop") {
		t.Error("settings page missing token row")
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
	cfg := Config{AppDir: t.TempDir(), RepoDir: t.TempDir(), AdminUser: "admin", AdminPass: "test", HomeFilename: "readme.md"}
	auth, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}

	expired, err := auth.AddToken("admin", "old", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("AddToken failed: %v", err)
	}
	if _, ok := auth.UserForBearer(expired); ok {
		t.Error("expired token verified, want rejection")
	}

	live, err := auth.AddToken("admin", "live", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("AddToken failed: %v", err)
	}
	if user, ok := auth.UserForBearer(live); !ok || user != "admin" {
		t.Errorf("live token: got (%q, %v), want (admin, true)", user, ok)
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
