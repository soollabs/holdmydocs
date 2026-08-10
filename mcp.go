package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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

type mcpNamespaceIn struct {
	Name string `json:"name" jsonschema:"namespace name, e.g. notes"`
}

type mcpNamespaceMeta struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Pages       int    `json:"pages"`
	Public      bool   `json:"public"`
}

type mcpNamespacesOut struct {
	Namespaces []mcpNamespaceMeta `json:"namespaces"`
}

type mcpNamespaceOut struct {
	Name        string         `json:"name"`
	Widgets     []string       `json:"widgets"`
	Public      bool           `json:"public"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	Skin        string         `json:"skin,omitempty"`
	Palette     string         `json:"palette,omitempty"`
	Index       string         `json:"index,omitempty"`
	New         *NewPageConfig `json:"new,omitempty"`
	Hash        string         `json:"hash"`
}

type mcpSaveNamespaceIn struct {
	Name        string         `json:"name" jsonschema:"namespace name, e.g. notes"`
	Widgets     []string       `json:"widgets,omitempty"`
	Public      bool           `json:"public"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty" jsonschema:"brief namespace description, maximum 255 characters"`
	Skin        string         `json:"skin,omitempty"`
	Palette     string         `json:"palette,omitempty"`
	Index       string         `json:"index,omitempty"`
	New         *NewPageConfig `json:"new,omitempty"`
	BaseHash    string         `json:"basehash,omitempty" jsonschema:"hash from read_namespace; omit to create a namespace configuration"`
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

type mcpAttachmentSearchIn struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum 50 results, default 20"`
}

type mcpAttachmentSearchHit struct {
	OwnerSlug string  `json:"owner_slug"`
	Filename  string  `json:"filename"`
	URL       string  `json:"url"`
	Excerpt   string  `json:"excerpt" jsonschema:"escaped text containing optional <mark> tags"`
	Score     float64 `json:"score"`
}

type mcpAttachmentSearchOut struct {
	Hits []mcpAttachmentSearchHit `json:"hits"`
}

type mcpAttachmentUploadIn struct {
	Slug     string `json:"slug" jsonschema:"page slug that owns the attachment, always namespace/page"`
	Filename string `json:"filename" jsonschema:"original filename; supported extensions are PDF, Office, OpenDocument, PNG, JPEG, GIF, and WebP"`
}

type mcpAttachmentUploadOut struct {
	UploadURL     string `json:"upload_url" jsonschema:"one-use multipart POST URL; upload the file as the file form field"`
	AttachmentURL string `json:"attachment_url" jsonschema:"URL of the uploaded attachment after a successful upload"`
	ExpiresAt     string `json:"expires_at"`
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
	if strings.HasPrefix(path, ".") || !strings.HasSuffix(path, ".md") {
		return "", false
	}
	slug := strings.TrimSuffix(path, ".md")
	if !validMCPPageSlug(slug) {
		return "", false
	}
	namespace, _ := namespaceFor(slug)
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
		Name: name, Widgets: cfg.Widgets, Public: cfg.Public, Title: cfg.Title, Description: cfg.Description,
		Skin: cfg.Skin, Palette: cfg.Palette, Index: cfg.Index, New: cfg.New, Hash: hash,
	}
}

func (app *App) readMCPNamespace(name string) (mcpNamespaceOut, error) {
	cfg, ok := app.Namespaces()[name]
	if !ok {
		return mcpNamespaceOut{}, fmt.Errorf("namespace %q not found", name)
	}
	_, hash, err := app.Store.Read(namespaceConfigPath(name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return mcpNamespaceOut{}, err
	}
	return mcpNamespaceOutput(name, cfg, hash), nil
}

// mcpHandler builds the MCP server and returns its streamable HTTP handler
// for mounting at /mcp. Auth happens in Auth.Middleware before this handler.
func (app *App) mcpHandler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "hmd", Version: version}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_pages",
		Description: "List every page accessible to this caller, returning its namespace/page slug, title, and tags. Use a page slug with read_page before updating it.",
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

	if app.Index.documents != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "search_attachments",
			Description: "Search extracted attachment text using keyword and semantic ranking. Available only when document indexing is enabled. Results are limited to accessible page-owned attachments; excerpts are escaped text with optional <mark> tags.",
		}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpAttachmentSearchIn) (*mcp.CallToolResult, mcpAttachmentSearchOut, error) {
			if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
				return nil, mcpAttachmentSearchOut{}, err
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 20
			}
			if limit > 50 {
				limit = 50
			}
			hits, err := app.Index.SearchAttachments(ctx, in.Query, 50)
			if err != nil {
				return nil, mcpAttachmentSearchOut{}, err
			}
			out := mcpAttachmentSearchOut{Hits: []mcpAttachmentSearchHit{}}
			for _, hit := range hits {
				if err := app.mcpRequireSlug(ctx, hit.OwnerSlug); err != nil || !tokenAllowsSlug(ctx, hit.OwnerSlug) {
					continue
				}
				out.Hits = append(out.Hits, mcpAttachmentSearchHit{
					OwnerSlug: hit.OwnerSlug, Filename: hit.Filename, URL: hit.URL,
					Excerpt: hit.Excerpt, Score: hit.Score,
				})
				if len(out.Hits) == limit {
					break
				}
			}
			return nil, out, nil
		})
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "upload_attachment",
		Description: "Create a one-use native multipart upload URL for a supported document or image owned by a page. Requires write access to that page. POST the file as the file form field. After a successful upload, use save_page to add [filename](attachment_url) to the owning page, or ![alt text](attachment_url) for an image.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpAttachmentUploadIn) (*mcp.CallToolResult, mcpAttachmentUploadOut, error) {
		if err := app.mcpRequireScope(ctx, scopeWrite); err != nil {
			return nil, mcpAttachmentUploadOut{}, err
		}
		if !validMCPPageSlug(in.Slug) {
			return nil, mcpAttachmentUploadOut{}, fmt.Errorf("invalid slug %q", in.Slug)
		}
		if err := app.mcpRequireSlug(ctx, in.Slug); err != nil {
			return nil, mcpAttachmentUploadOut{}, err
		}
		filename := filepath.Base(in.Filename)
		ext := strings.ToLower(filepath.Ext(filename))
		name := Slugify(filename[:len(filename)-len(ext)])
		if name == "" || !supportedAttachmentExtensions[ext] {
			return nil, mcpAttachmentUploadOut{}, errors.New("file type or filename is not allowed")
		}
		filename = name + ext
		var token [32]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, mcpAttachmentUploadOut{}, fmt.Errorf("creating upload URL: %w", err)
		}
		expires := time.Now().Add(10 * time.Minute)
		tokenString := fmt.Sprintf("%x", token)
		app.uploads.Store(tokenString, uploadCapability{Slug: in.Slug, Filename: filename, User: app.mcpUser(ctx), Expires: expires})
		return nil, mcpAttachmentUploadOut{
			UploadURL:     "/_/api/attachment-uploads/" + tokenString,
			AttachmentURL: "/_/attachments/" + in.Slug + "/" + filename,
			ExpiresAt:     expires.UTC().Format(time.RFC3339),
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_namespaces",
		Description: "List namespaces accessible to this caller, with their description, page count, and public/private status. Use read_namespace for full settings.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, mcpNamespacesOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpNamespacesOut{}, err
		}
		out := mcpNamespacesOut{Namespaces: []mcpNamespaceMeta{}}
		for _, summary := range namespaceSummaries(app.Namespaces(), app.Index.Titles()) {
			if !tokenAllowsNamespace(ctx, summary.Name) {
				continue
			}
			out.Namespaces = append(out.Namespaces, mcpNamespaceMeta{Name: summary.Name, Description: summary.Config.Description, Pages: summary.Count, Public: summary.Config.Public})
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_namespace",
		Description: "Read one namespace's full settings and current hash. Requires settings access. Pass that hash as basehash to save_namespace when updating.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpNamespaceIn) (*mcp.CallToolResult, mcpNamespaceOut, error) {
		if err := app.mcpRequireScope(ctx, scopeSettings); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		if !validNamespaceName(in.Name) {
			return nil, mcpNamespaceOut{}, fmt.Errorf("invalid namespace %q", in.Name)
		}
		if err := app.mcpRequireNamespace(ctx, in.Name); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		out, err := app.readMCPNamespace(in.Name)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "save_namespace",
		Description: "Create or update namespace settings (each save is a git commit). " +
			"Requires settings access. Update: pass the hash returned by read_namespace; on conflict re-read and retry. " +
			"Create: omit basehash; this writes only namespace configuration and does not delete or change pages.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSaveNamespaceIn) (*mcp.CallToolResult, mcpNamespaceOut, error) {
		if err := app.mcpRequireScope(ctx, scopeSettings); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		if !validNamespaceName(in.Name) {
			return nil, mcpNamespaceOut{}, fmt.Errorf("invalid namespace %q", in.Name)
		}
		if err := app.mcpRequireNamespace(ctx, in.Name); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		cfg, err := normaliseNamespaceConfig(in.Name, NamespaceConfig{
			Widgets: in.Widgets, Public: in.Public, Title: in.Title, Description: in.Description, Skin: in.Skin,
			Palette: in.Palette, Index: in.Index, New: in.New,
		}, newPageTemplateData{Now: time.Now(), User: app.mcpUser(ctx), Namespace: in.Name})
		if err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		data, err := cfg.Encode()
		if err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		authorName, authorEmail := app.gitAuthor(app.mcpUser(ctx))
		path := namespaceConfigPath(in.Name)
		hash, err := app.Store.SaveChecked(path, path, in.BaseHash, data, "Configure namespace "+path, authorName, authorEmail)
		if errors.Is(err, ErrConflict) {
			current, readErr := app.readMCPNamespace(in.Name)
			if readErr != nil {
				return nil, mcpNamespaceOut{}, readErr
			}
			payload, _ := json.Marshal(map[string]any{"error": "conflict: the namespace changed since basehash (or already exists)", "namespace": current})
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, mcpNamespaceOut{}, nil
		}
		if err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		app.refreshNamespaces()
		slog.Info("mcp namespace configured", "namespace", in.Name, "author", authorName)
		return nil, mcpNamespaceOutput(in.Name, cfg, hash), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_page",
		Description: "Read one accessible namespace/page, including its raw Markdown body, metadata, and current hash. Pass the hash as basehash to save_page when updating.",
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
			"Requires write access. Update: first read_page, then pass its hash as basehash; on conflict, re-read or merge and retry with the fresh hash. " +
			"Create: omit basehash, which fails if the page already exists. Never overwrite without a fresh basehash.",
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
		if err := app.Index.UpdatePage(page, hash); err != nil {
			return nil, mcpSaveOut{}, err
		}
		slog.Info("mcp saved", "slug", in.Slug, "author", authorName, "message", message)
		return nil, mcpSaveOut{Slug: in.Slug, Hash: hash}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_page",
		Description: "Delete one accessible namespace/page. Requires write access. The page remains recoverable from Git history.",
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
		Description: "Full-text search accessible page titles, bodies, and tags. Results include escaped snippets with optional <mark> tags; use read_page for the complete page.",
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
		Description: "List accessible pages whose [[wiki-links]] target a given accessible namespace/page.",
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
		Description: "List the newest accessible wiki commits first, with commit metadata and touched files. A commit is omitted if it includes files outside this caller's namespace access.",
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
		Description: "Read-only wiki hygiene report for accessible pages: dangling [[wiki-links]] (target page missing) and orphan pages (no incoming links). " +
			"Optionally scope it to one accessible namespace.",
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

	return newMCPHTTPHandler(func(*http.Request) *mcp.Server { return server })
}
