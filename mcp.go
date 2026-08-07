package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The MCP server exposes the wiki to agents (Claude Code, Claude Desktop,
// anything MCP) over streamable HTTP at /mcp. Tools are thin wrappers over
// the existing store and index operations; the optimistic-lock discipline
// (basehash from read_page, conflict on stale save) is carried in the tool
// descriptions, since agents follow those.

type mcpPageMeta struct {
	Slug  string   `json:"slug"`
	Title string   `json:"title"`
	Tags  []string `json:"tags,omitempty"`
}

type mcpListOut struct {
	Pages []mcpPageMeta `json:"pages"`
}

type mcpSlugIn struct {
	Slug string `json:"slug" jsonschema:"page slug, always namespace/page, e.g. notes/my-page"`
}

type mcpPageOut struct {
	Slug  string   `json:"slug"`
	Title string   `json:"title"`
	Tags  []string `json:"tags,omitempty"`
	Body  string   `json:"body"`
	Pin   bool     `json:"pin,omitempty"`
	Hash  string   `json:"hash"`
}

type mcpSaveIn struct {
	Slug     string   `json:"slug" jsonschema:"page slug, always namespace/page, e.g. notes/my-page"`
	Title    string   `json:"title,omitempty" jsonschema:"page title; defaults to the slug"`
	Tags     []string `json:"tags,omitempty"`
	Body     string   `json:"body" jsonschema:"raw markdown body (frontmatter is managed by hmd)"`
	Pin      bool     `json:"pin,omitempty" jsonschema:"surfaced by the pinned sidebar widget; preserve the value from read_page when updating"`
	BaseHash string   `json:"basehash,omitempty" jsonschema:"hash from read_page; omit to create a new page"`
}

type mcpSaveOut struct {
	Slug string `json:"slug"`
	Hash string `json:"hash" jsonschema:"the new basehash for a follow-up save"`
}

type mcpSearchIn struct {
	Query string `json:"query"`
}

type mcpSearchHit struct {
	Slug    string   `json:"slug"`
	Title   string   `json:"title"`
	Snippet string   `json:"snippet,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

type mcpSearchOut struct {
	Hits []mcpSearchHit `json:"hits"`
}

type mcpBacklinksOut struct {
	Backlinks []mcpPageMeta `json:"backlinks"`
}

type mcpRecentIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"max commits to return, default 20"`
}

type mcpCommit struct {
	Hash    string   `json:"hash"`
	Message string   `json:"message"`
	Author  string   `json:"author"`
	When    string   `json:"when"`
	Files   []string `json:"files,omitempty"`
}

type mcpRecentOut struct {
	Commits []mcpCommit `json:"commits"`
}

type mcpHealthIn struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"limit the report to this namespace; omit for the whole wiki"`
}

type mcpHealthOut struct {
	Missing []HealthMissingEntry `json:"missing"`
	Orphans []string             `json:"orphans"`
}

type mcpNamespaceIn struct {
	Name     string         `json:"name" jsonschema:"namespace name, e.g. notes"`
	Widgets  []string       `json:"widgets,omitempty"`
	Public   bool           `json:"public,omitempty"`
	New      *NewPageConfig `json:"new,omitempty"`
	Index    string         `json:"index,omitempty" jsonschema:"page name in this namespace shown at /{namespace}/ instead of the page list"`
	BaseHash string         `json:"basehash,omitempty" jsonschema:"hash from read_namespace; omit to create a namespace configuration"`
}

type mcpNamespaceNameIn struct {
	Name string `json:"name" jsonschema:"namespace name, e.g. notes"`
}

type mcpNamespaceOut struct {
	Name       string         `json:"name"`
	Widgets    []string       `json:"widgets"`
	Public     bool           `json:"public"`
	New        *NewPageConfig `json:"new,omitempty"`
	Index      string         `json:"index,omitempty"`
	Configured bool           `json:"configured"`
	Hash       string         `json:"hash"`
}

type mcpNamespaceListEntry struct {
	Name   string `json:"name"`
	Count  int    `json:"count"`
	Public bool   `json:"public"`
}

type mcpNamespaceListOut struct {
	Namespaces []mcpNamespaceListEntry `json:"namespaces"`
}

const mcpProtocolVersion = "2026-07-28"

const mcpGateBodyLimit = 1 << 20

type mcpGateRequest struct {
	ID     json.RawMessage `json:"id"`
	Params struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	} `json:"params"`
}

func writeMCPGateError(w http.ResponseWriter, code int, id json.RawMessage, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    any    `json:"data,omitempty"`
		} `json:"error"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Error: struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    any    `json:"data,omitempty"`
		}{Code: code, Message: message, Data: data},
	})
}

func mcpProtocolGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("MCP-Protocol-Version") != mcpProtocolVersion {
			writeMCPGateError(w, mcp.CodeUnsupportedProtocolVersion, nil, "unsupported protocol version", mcp.UnsupportedProtocolVersionData{
				Supported: []string{mcpProtocolVersion},
				Requested: r.Header.Get("MCP-Protocol-Version"),
			})
			return
		}
		if r.Header.Get("Mcp-Session-Id") != "" {
			writeMCPGateError(w, mcp.CodeHeaderMismatch, nil, "legacy sessions are unsupported", nil)
			return
		}
		if r.URL.Query().Get("sessionId") != "" {
			writeMCPGateError(w, mcp.CodeHeaderMismatch, nil, "legacy sessions are unsupported", nil)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcpGateBodyLimit))
		if err != nil {
			writeMCPGateError(w, -32600, nil, "invalid request", nil)
			return
		}
		var request mcpGateRequest
		if err := json.Unmarshal(body, &request); err != nil {
			writeMCPGateError(w, -32600, nil, "invalid request", nil)
			return
		}
		if len(request.Params.Meta) == 0 {
			writeMCPGateError(w, mcp.CodeHeaderMismatch, request.ID, "missing request metadata", nil)
			return
		}
		if len(request.Params.Meta["io.modelcontextprotocol/protocolVersion"]) == 0 {
			writeMCPGateError(w, mcp.CodeHeaderMismatch, request.ID, "missing metadata protocol version", nil)
			return
		}
		if len(request.Params.Meta["io.modelcontextprotocol/clientInfo"]) == 0 {
			writeMCPGateError(w, mcp.CodeHeaderMismatch, request.ID, "missing client info", nil)
			return
		}
		if len(request.Params.Meta["io.modelcontextprotocol/clientCapabilities"]) == 0 {
			writeMCPGateError(w, mcp.CodeMissingRequiredClientCapabilities, request.ID, "missing client capabilities", nil)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

func newMCPHTTPHandler(serverForRequest func(*http.Request) *mcp.Server) http.Handler {
	legacy := mcp.NewStreamableHTTPHandler(serverForRequest, nil)
	modern := mcp.NewStreamableHTTPHandler(serverForRequest, &mcp.StreamableHTTPOptions{
		// stateless — no session state to time out or resume; revisit if a client needs server-initiated messages
		Stateless:                    true,
		PropagateRequestCancellation: true,
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("MCP-Protocol-Version") == mcpProtocolVersion {
			mcpProtocolGate(modern).ServeHTTP(w, r)
			return
		}
		legacy.ServeHTTP(w, r)
	})
	return http.NewCrossOriginProtection().Handler(handler)
}

// mcpUser returns the authenticated request user, which Auth.Middleware has
// already verified from the Bearer token or session cookie.
func (app *App) mcpUser(ctx context.Context) string {
	user, _ := ctx.Value(ctxUserKey{}).(string)
	return user
}

// mcpRequireScope keeps MCP permissions at the tool boundary: one HTTP
// endpoint hosts read, write and settings actions with different permissions.
func (app *App) mcpRequireScope(ctx context.Context, need scope) error {
	if principal, ok := tokenPrincipalFromContext(ctx); ok {
		if principal.User == "" || !principal.HasScope(need) {
			return fmt.Errorf("forbidden: %s scope required", need)
		}
		return nil
	}
	user := app.mcpUser(ctx)
	if user == "" || !app.Auth.prefs(user).hasScope(need) {
		return fmt.Errorf("forbidden: %s scope required", need)
	}
	return nil
}

func (app *App) mcpRequireNamespace(ctx context.Context, namespace string) error {
	if tokenAllowsNamespace(ctx, namespace) {
		return nil
	}
	return fmt.Errorf("forbidden: namespace %q is not allowed", namespace)
}

func (app *App) mcpRequireSlug(ctx context.Context, slug string) error {
	if tokenAllowsSlug(ctx, slug) {
		return nil
	}
	return fmt.Errorf("forbidden: namespace access denied")
}

func mcpPathNamespace(path string) (string, bool) {
	if path == namespaceConfigFile {
		return "", true
	}
	if namespace, ok := strings.CutSuffix(path, "/"+namespaceConfigFile); ok {
		if validNamespaceName(namespace) {
			return namespace, true
		}
		return "", false
	}
	if strings.HasPrefix(path, ".") {
		path = strings.TrimPrefix(path, ".")
	}
	if !strings.HasSuffix(path, ".md") {
		return "", false
	}
	slug := strings.TrimSuffix(path, ".md")
	namespace, rest := namespaceFor(slug)
	if namespace != "" {
		if !validNamespaceName(namespace) || rest == "" {
			return "", false
		}
	} else if !validMCPPageSegment(rest) {
		return "", false
	}
	for _, segment := range strings.Split(rest, "/") {
		if !validMCPPageSegment(segment) {
			return "", false
		}
	}
	return namespace, true
}

func mcpCommitAllowed(ctx context.Context, commit CommitDetail) bool {
	if len(commit.Files) == 0 {
		return false
	}
	for _, path := range commit.Files {
		namespace, ok := mcpPathNamespace(path)
		if !ok || !tokenAllowsNamespace(ctx, namespace) {
			return false
		}
	}
	return true
}

func mcpNamespaceOutput(name string, cfg NamespaceConfig, hash string) mcpNamespaceOut {
	return mcpNamespaceOut{
		Name: name, Widgets: cfg.Widgets, Public: cfg.Public, New: cfg.New, Index: cfg.Index,
		Configured: cfg.Configured, Hash: hash,
	}
}

func (app *App) readMCPNamespace(name string) (mcpNamespaceOut, error) {
	for _, summary := range namespaceSummaries(app.Namespaces(), app.Index.Titles()) {
		if summary.Name != name {
			continue
		}
		hash := ""
		if summary.Config.Configured {
			if _, currentHash, err := app.Store.Read(namespaceConfigPath(name)); err != nil {
				return mcpNamespaceOut{}, err
			} else {
				hash = currentHash
			}
		}
		return mcpNamespaceOutput(name, summary.Config, hash), nil
	}
	return mcpNamespaceOut{}, fmt.Errorf("namespace %q not found", name)
}

// mcpHandler builds the MCP server and returns its streamable HTTP handler
// for mounting at /mcp. Auth happens in Auth.Middleware before this handler.
func (app *App) mcpHandler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "hmd", Version: version}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_pages",
		Description: "List all wiki pages with slug, title and tags.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, mcpListOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpListOut{}, err
		}
		titles := app.Index.Titles()
		out := mcpListOut{Pages: []mcpPageMeta{}}
		for slug, title := range titles {
			if !tokenAllowsSlug(ctx, slug) {
				continue
			}
			out.Pages = append(out.Pages, mcpPageMeta{Slug: slug, Title: title, Tags: app.Index.TagsFor(slug)})
		}
		sort.Slice(out.Pages, func(i, j int) bool { return out.Pages[i].Slug < out.Pages[j].Slug })
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_page",
		Description: "Read a wiki page: raw markdown body plus the page's current hash. save_page requires that hash as basehash.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSlugIn) (*mcp.CallToolResult, mcpPageOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpPageOut{}, err
		}
		if !validMCPPageSlug(in.Slug) {
			return nil, mcpPageOut{}, fmt.Errorf("invalid slug %q", in.Slug)
		}
		if err := app.mcpRequireSlug(ctx, in.Slug); err != nil {
			return nil, mcpPageOut{}, err
		}
		content, hash, err := app.Store.Read(pageFile(in.Slug))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, mcpPageOut{}, fmt.Errorf("page %q not found", in.Slug)
			}
			return nil, mcpPageOut{}, err
		}
		p := ParsePage(in.Slug, content)
		return nil, mcpPageOut{
			Slug: p.Slug, Title: p.Title, Tags: p.Tags, Body: p.Body,
			Pin: p.Pin, Hash: hash,
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "save_page",
		Description: "Create or update a wiki page (each save is a git commit). " +
			"Update: pass the basehash returned by read_page; on conflict the error carries the page's current hash and body — re-read or merge, then retry with that hash. " +
			"Create: omit basehash (fails if the page already exists). Never overwrite without a fresh basehash.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSaveIn) (*mcp.CallToolResult, mcpSaveOut, error) {
		if err := app.mcpRequireScope(ctx, scopeWrite); err != nil {
			return nil, mcpSaveOut{}, err
		}
		if !validMCPPageSlug(in.Slug) {
			return nil, mcpSaveOut{}, fmt.Errorf("invalid slug %q", in.Slug)
		}
		if err := app.mcpRequireSlug(ctx, in.Slug); err != nil {
			return nil, mcpSaveOut{}, err
		}
		title := strings.TrimSpace(in.Title)
		if title == "" {
			title = in.Slug
		}
		page := Page{
			Slug: in.Slug, Title: title, Tags: in.Tags, Body: in.Body,
			Pin: in.Pin,
		}

		cfg := app.config()
		if cfg.SyncMode == "bidirectional" && cfg.Git.RemoteURL != "" {
			if _, err := app.Store.FetchAndFF(); err != nil {
				slog.Warn("mcp save-time fetch", "slug", in.Slug, "err", err)
			}
		}

		message := "Update " + title
		if in.BaseHash == "" {
			message = "Create " + title
		}
		authorName, authorEmail := app.gitAuthor(app.mcpUser(ctx))
		file := pageFile(in.Slug)
		hash, err := app.Store.SaveChecked(file, file, in.BaseHash, page.Encode(), message, authorName, authorEmail)
		if errors.Is(err, ErrConflict) {
			content, currentHash, readErr := app.Store.Read(file)
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return nil, mcpSaveOut{}, readErr
			}
			current := ParsePage(in.Slug, content)
			payload, _ := json.Marshal(map[string]string{
				"error": "conflict: the page changed since basehash (or already exists)",
				"hash":  currentHash,
				"body":  current.Body,
			})
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}},
			}, mcpSaveOut{}, nil
		}
		if err != nil {
			return nil, mcpSaveOut{}, err
		}
		if err := app.Index.Update(page); err != nil {
			return nil, mcpSaveOut{}, err
		}
		slog.Info("mcp saved", "slug", in.Slug, "author", authorName, "message", message)
		return nil, mcpSaveOut{Slug: in.Slug, Hash: hash}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_page",
		Description: "Delete a wiki page. The git history keeps its content recoverable.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSlugIn) (*mcp.CallToolResult, any, error) {
		if err := app.mcpRequireScope(ctx, scopeWrite); err != nil {
			return nil, nil, err
		}
		if !validMCPPageSlug(in.Slug) {
			return nil, nil, fmt.Errorf("invalid slug %q", in.Slug)
		}
		if err := app.mcpRequireSlug(ctx, in.Slug); err != nil {
			return nil, nil, err
		}
		authorName, authorEmail := app.gitAuthor(app.mcpUser(ctx))
		if err := app.Store.Remove(pageFile(in.Slug), "Delete "+in.Slug, authorName, authorEmail); err != nil {
			return nil, nil, err
		}
		app.Index.Remove(in.Slug)
		slog.Info("mcp deleted", "slug", in.Slug, "author", authorName)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "deleted " + in.Slug}},
		}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search",
		Description: "Full-text search over page titles, bodies and tags. Snippets highlight matches with <mark> tags.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSearchIn) (*mcp.CallToolResult, mcpSearchOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpSearchOut{}, err
		}
		hits, err := app.Index.Search(in.Query)
		if err != nil {
			return nil, mcpSearchOut{}, err
		}
		out := mcpSearchOut{Hits: []mcpSearchHit{}}
		for _, h := range hits {
			if !tokenAllowsSlug(ctx, h.Slug) {
				continue
			}
			out.Hits = append(out.Hits, mcpSearchHit(h))
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "backlinks",
		Description: "List the pages whose [[wiki-links]] point at the given page.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSlugIn) (*mcp.CallToolResult, mcpBacklinksOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpBacklinksOut{}, err
		}
		if !validMCPPageSlug(in.Slug) {
			return nil, mcpBacklinksOut{}, fmt.Errorf("invalid slug %q", in.Slug)
		}
		if err := app.mcpRequireSlug(ctx, in.Slug); err != nil {
			return nil, mcpBacklinksOut{}, err
		}
		titles := app.Index.Titles()
		out := mcpBacklinksOut{Backlinks: []mcpPageMeta{}}
		for _, slug := range app.Index.Backlinks(in.Slug) {
			if !tokenAllowsSlug(ctx, slug) {
				continue
			}
			out.Backlinks = append(out.Backlinks, mcpPageMeta{Slug: slug, Title: titles[slug], Tags: app.Index.TagsFor(slug)})
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "recent_changes",
		Description: "The newest commits on the wiki, newest first, with the files each touched.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpRecentIn) (*mcp.CallToolResult, mcpRecentOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpRecentOut{}, err
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 20
		}
		commits, err := app.Store.RecentCommits(limit)
		if err != nil {
			return nil, mcpRecentOut{}, err
		}
		out := mcpRecentOut{Commits: []mcpCommit{}}
		for _, c := range commits {
			if !mcpCommitAllowed(ctx, c) {
				continue
			}
			out.Commits = append(out.Commits, mcpCommit{
				Hash:    c.Hash,
				Message: c.Message,
				Author:  c.Author,
				When:    c.When.Format(time.RFC3339),
				Files:   c.Files,
			})
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "health",
		Description: "Read-only wiki hygiene report: dangling [[wiki-links]] (linked but no page exists) and orphan pages " +
			"(no incoming links). Pass namespace to scope the report to one namespace.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpHealthIn) (*mcp.CallToolResult, mcpHealthOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpHealthOut{}, err
		}
		if in.Namespace != "" {
			if !validNamespaceName(in.Namespace) {
				return nil, mcpHealthOut{}, fmt.Errorf("invalid namespace %q", in.Namespace)
			}
			if err := app.mcpRequireNamespace(ctx, in.Namespace); err != nil {
				return nil, mcpHealthOut{}, err
			}
		}
		missing, orphans := app.Index.Health(app.Namespaces().IndexSlugs())
		missing, orphans = filterHealth(ctx, missing, orphans)
		if in.Namespace != "" {
			missing, orphans = filterHealthNamespace(missing, orphans, in.Namespace)
		}
		out := mcpHealthOut{Missing: []HealthMissingEntry{}, Orphans: orphans}
		if out.Orphans == nil {
			out.Orphans = []string{}
		}
		for slug, sources := range missing {
			out.Missing = append(out.Missing, HealthMissingEntry{Slug: slug, Sources: sources})
		}
		sort.Slice(out.Missing, func(i, j int) bool { return out.Missing[i].Slug < out.Missing[j].Slug })
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_namespaces",
		Description: "List namespaces with their page counts and visibility.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, mcpNamespaceListOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpNamespaceListOut{}, err
		}
		out := mcpNamespaceListOut{Namespaces: []mcpNamespaceListEntry{}}
		for _, summary := range namespaceSummaries(app.Namespaces(), app.Index.Titles()) {
			if !tokenAllowsNamespace(ctx, summary.Name) {
				continue
			}
			out.Namespaces = append(out.Namespaces, mcpNamespaceListEntry{Name: summary.Name, Count: summary.Count, Public: summary.Config.Public})
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_namespace",
		Description: "Read a namespace's settings and current config hash. save_namespace requires that hash when updating.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpNamespaceNameIn) (*mcp.CallToolResult, mcpNamespaceOut, error) {
		if err := app.mcpRequireScope(ctx, scopeSettings); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		if !validNamespaceName(in.Name) {
			return nil, mcpNamespaceOut{}, fmt.Errorf("invalid namespace name %q", in.Name)
		}
		if err := app.mcpRequireNamespace(ctx, in.Name); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		out, err := app.readMCPNamespace(in.Name)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "save_namespace",
		Description: "Create or update namespace settings. Update: pass the hash returned by read_namespace; on conflict the error carries current settings for merge and retry. Create: omit basehash.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpNamespaceIn) (*mcp.CallToolResult, mcpNamespaceOut, error) {
		if err := app.mcpRequireScope(ctx, scopeSettings); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		if !validNamespaceName(in.Name) {
			return nil, mcpNamespaceOut{}, fmt.Errorf("invalid namespace name %q", in.Name)
		}
		if err := app.mcpRequireNamespace(ctx, in.Name); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		user := app.mcpUser(ctx)
		cfg, err := normaliseNamespaceConfig(in.Name, NamespaceConfig{Widgets: in.Widgets, Public: in.Public, New: in.New, Index: in.Index}, newPageTemplateData{Now: time.Now(), User: user, Namespace: in.Name})
		if err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		data, err := cfg.Encode()
		if err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		creating := !app.Namespaces()[in.Name].Configured
		authorName, authorEmail := app.gitAuthor(user)
		path := namespaceConfigPath(in.Name)
		hash, err := app.Store.SaveChecked(path, path, in.BaseHash, data, "Configure namespace "+path, authorName, authorEmail)
		if errors.Is(err, ErrConflict) {
			current, currentErr := app.readMCPNamespace(in.Name)
			if currentErr != nil {
				return nil, mcpNamespaceOut{}, currentErr
			}
			payload, _ := json.Marshal(map[string]any{
				"error":     "conflict: namespace settings changed since basehash (or already exist)",
				"hash":      current.Hash,
				"namespace": current,
			})
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, mcpNamespaceOut{}, nil
		}
		if err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		template := defaultNewPageTemplate
		if cfg.New != nil {
			template = cfg.New.Template
		}
		if creating || cfg.New != nil {
			if err := app.ensureNewPageTemplate(in.Name, template, authorName, authorEmail); err != nil {
				return nil, mcpNamespaceOut{}, err
			}
		}
		app.refreshNamespaces()
		cfg.Configured = true
		return nil, mcpNamespaceOutput(in.Name, cfg, hash), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_namespace",
		Description: "Delete an empty namespace only. Pages and hidden template files must be moved or removed first.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpNamespaceNameIn) (*mcp.CallToolResult, any, error) {
		if err := app.mcpRequireScope(ctx, scopeSettings); err != nil {
			return nil, nil, err
		}
		if !validNamespaceName(in.Name) {
			return nil, nil, fmt.Errorf("invalid namespace name %q", in.Name)
		}
		if err := app.mcpRequireNamespace(ctx, in.Name); err != nil {
			return nil, nil, err
		}
		authorName, authorEmail := app.gitAuthor(app.mcpUser(ctx))
		if err := app.Store.DeleteNamespace(in.Name, "Delete namespace "+in.Name, authorName, authorEmail); err != nil {
			return nil, nil, err
		}
		app.refreshNamespaces()
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "deleted namespace " + in.Name}}}, nil, nil
	})

	return newMCPHTTPHandler(func(*http.Request) *mcp.Server { return server })
}
