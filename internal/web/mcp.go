package web

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
	"time"

	"hmd/internal/api"
	"hmd/internal/wiki"

	"github.com/modelcontextprotocol/go-sdk/mcp"
) // The MCP server exposes the wiki over streamable HTTP at /mcp.

type mcpPageMeta struct {
	Slug  string   `json:"slug"`
	Title string   `json:"title"`
	Tags  []string `json:"tags,omitempty"`
}

type mcpListOut struct {
	Pages []mcpPageMeta `json:"pages"`
}

type mcpNamespaceIn struct {
	Name string `json:"name" jsonschema:"namespace name, e.g. notes; this is a name, not a page slug or path"`
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
	Name        string              `json:"name"`
	Widgets     []string            `json:"widgets"`
	Public      bool                `json:"public"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Skin        string              `json:"skin,omitempty"`
	Palette     string              `json:"palette,omitempty"`
	Index       string              `json:"index,omitempty"`
	Tree        []string            `json:"tree,omitempty"`
	New         *wiki.NewPageConfig `json:"new,omitempty"`
	Hash        string              `json:"hash"`
}

type mcpSaveNamespaceIn struct {
	Name        string              `json:"name" jsonschema:"namespace name, e.g. notes; this is a name, not a page slug or path"`
	Widgets     []string            `json:"widgets,omitempty" jsonschema:"complete ordered string array of configured widget IDs; preserve from read_namespace when updating"`
	Public      bool                `json:"public" jsonschema:"whether anonymous users can read the namespace; preserve from read_namespace when updating"`
	Title       string              `json:"title,omitempty" jsonschema:"human-readable namespace title; preserve from read_namespace when updating"`
	Description string              `json:"description,omitempty" jsonschema:"brief namespace description, maximum 255 characters; preserve from read_namespace when updating"`
	Skin        string              `json:"skin,omitempty" jsonschema:"presentation skin name; preserve from read_namespace when updating"`
	Palette     string              `json:"palette,omitempty" jsonschema:"colour palette name; preserve from read_namespace when updating"`
	Index       string              `json:"index,omitempty" jsonschema:"one-segment page name used as the namespace index; preserve from read_namespace when updating"`
	Tree        []string            `json:"tree,omitempty" jsonschema:"complete ordered string array of page or folder paths for the tree; preserve from read_namespace when updating"`
	New         *wiki.NewPageConfig `json:"new,omitempty" jsonschema:"new-page template configuration; preserve from read_namespace when updating"`
	BaseHash    string              `json:"basehash,omitempty" jsonschema:"hash from read_namespace; omit only when creating a namespace configuration"`
}

type mcpSlugIn struct {
	Slug string `json:"slug,omitempty" jsonschema:"canonical page identifier in namespace/page form, e.g. notes/my-page; provide slug or path, not both"`
	Path string `json:"path,omitempty" jsonschema:"alias for slug, in namespace/page form; provide slug or path, not both"`
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
	Slug     string   `json:"slug,omitempty" jsonschema:"canonical page identifier in namespace/page form, e.g. notes/my-page; provide slug or path, not both"`
	Path     string   `json:"path,omitempty" jsonschema:"alias for slug, in namespace/page form; provide slug or path, not both"`
	Title    string   `json:"title,omitempty" jsonschema:"page title; defaults to the slug on create; preserve the value from read_page when updating"`
	Tags     []string `json:"tags,omitempty" jsonschema:"page tags; preserve the complete string array from read_page when updating"`
	Body     string   `json:"body" jsonschema:"complete raw Markdown body; frontmatter is managed by HMD"`
	Pin      bool     `json:"pin,omitempty" jsonschema:"surfaced by the pinned sidebar widget; preserve the value from read_page when updating"`
	BaseHash string   `json:"basehash,omitempty" jsonschema:"hash from read_page; omit only when creating a page"`
}

type mcpSaveOut struct {
	Slug         string `json:"slug"`
	Hash         string `json:"hash" jsonschema:"the new basehash for a follow-up save"`
	IndexWarning string `json:"index_warning,omitempty" jsonschema:"set when the save committed but the search index refresh failed; the commit is durable and indexing is reconciled in the background"`
}

type mcpSearchIn struct {
	Query string `json:"query" jsonschema:"full-text query for page titles, bodies, and tags"`
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
	Query string `json:"query" jsonschema:"keyword or semantic query for extracted attachment text"`
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
	Slug     string `json:"slug,omitempty" jsonschema:"canonical identifier of the page that owns the attachment, in namespace/page form; provide slug or path, not both"`
	Path     string `json:"path,omitempty" jsonschema:"alias for slug, identifying the owning page in namespace/page form; provide slug or path, not both"`
	Filename string `json:"filename" jsonschema:"original filename; HMD returns its canonical attachment URL; document formats supported by Apache Tika are indexed when document search is enabled"`
}

type mcpAttachmentUploadOut struct {
	UploadURL     string `json:"upload_url" jsonschema:"one-use multipart POST URL; upload the file as the file form field"`
	AttachmentURL string `json:"attachment_url" jsonschema:"URL of the uploaded attachment after a successful upload"`
	ExpiresAt     string `json:"expires_at"`
}

type mcpAttachmentReadIn struct {
	Slug     string `json:"slug,omitempty" jsonschema:"canonical identifier of the page that owns the attachment, in namespace/page form; provide slug or path, not both"`
	Path     string `json:"path,omitempty" jsonschema:"alias for slug, identifying the owning page in namespace/page form; provide slug or path, not both"`
	Filename string `json:"filename" jsonschema:"canonical attachment filename, without a path"`
}

type mcpAttachmentReadOut struct {
	Text string `json:"text"`
}

type mcpBaseURLKey struct{}

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
			writeMCPGateError(w, mcp.CodeHeaderMismatch, nil, "session headers are unsupported", nil)
			return
		}
		if r.URL.Query().Get("sessionId") != "" {
			writeMCPGateError(w, mcp.CodeHeaderMismatch, nil, "session query parameters are unsupported", nil)
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

func newMCPHTTPHandler(baseURL string, serverForRequest func(*http.Request) *mcp.Server) http.Handler {
	modern := mcp.NewStreamableHTTPHandler(serverForRequest, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		PropagateRequestCancellation: true,
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBaseURL := baseURL
		if requestBaseURL == "" {
			requestBaseURL = "http://" + r.Host
			if r.TLS != nil {
				requestBaseURL = "https://" + r.Host
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), mcpBaseURLKey{}, requestBaseURL))
		mcpProtocolGate(modern).ServeHTTP(w, r)
	})
	return http.NewCrossOriginProtection().Handler(handler)
}

func (app *App) mcpUser(ctx context.Context) string {
	return userFromContext(ctx)
}

func (app *App) mcpRequireScope(ctx context.Context, need scope) error {
	if principal, ok := tokenPrincipalFromContext(ctx); ok {
		if principal.User == "" || !principal.HasScope(need) {
			return fmt.Errorf("forbidden: %s scope required", need)
		}
		return nil
	}
	user := app.mcpUser(ctx)
	if user == "" || !app.Auth.Prefs(user).HasScope(need) {
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

func mcpPageIdentifier(slug, path string) (string, error) {
	if slug != "" && path != "" {
		return "", errors.New("provide slug or path, not both")
	}
	identifier := slug
	if identifier == "" {
		identifier = path
	}
	if identifier == "" {
		return "", errors.New("slug or path is required")
	}
	if !wiki.ValidPageSlug(identifier) {
		return "", fmt.Errorf("invalid page identifier %q: expected namespace/page", identifier)
	}
	return identifier, nil
}

func mcpNamespaceOutput(name string, cfg wiki.NamespaceConfig, hash string) mcpNamespaceOut {
	return mcpNamespaceOut{
		Name: name, Widgets: cfg.Widgets, Public: cfg.Public, Title: cfg.Title, Description: cfg.Description,
		Skin: cfg.Skin, Palette: cfg.Palette, Index: cfg.Index, Tree: cfg.Tree, New: cfg.New, Hash: hash,
	}
}

func mcpNamespaceFromDetail(detail *api.NamespaceDetail) mcpNamespaceOut {
	return mcpNamespaceOutput(detail.Name, detail.Config, detail.Hash)
}

func (app *App) mcpHandler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "hmd", Version: version}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_pages",
		Description: "List every page accessible to this caller. Arguments: none. Returns each canonical namespace/page slug, title and tags. Pass a returned slug to read_page before updating it.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, mcpListOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpListOut{}, err
		}
		pages, err := app.apiClient().ListPages(ctx)
		if err != nil {
			return nil, mcpListOut{}, err
		}
		out := mcpListOut{Pages: make([]mcpPageMeta, 0, len(pages))}
		for _, page := range pages {
			out.Pages = append(out.Pages, mcpPageMeta{Slug: page.Slug, Title: page.Title, Tags: page.Tags})
		}
		return nil, out, nil
	})

	if app.Index.DocumentsEnabled() {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "search_attachments",
			Description: "Search extracted attachment text using keyword and semantic ranking. Arguments: query (required string) and limit (optional integer, default 20, maximum 50). Available only when document indexing is enabled. Results are limited to accessible page-owned attachments; excerpts are escaped text with optional <mark> tags.",
		}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpAttachmentSearchIn) (*mcp.CallToolResult, mcpAttachmentSearchOut, error) {
			if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
				return nil, mcpAttachmentSearchOut{}, err
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 20
			}
			if limit > api.MaxAttachmentSearchResults {
				limit = api.MaxAttachmentSearchResults
			}
			hits, err := app.apiClient().SearchAttachments(ctx, in.Query, limit)
			if err != nil {
				return nil, mcpAttachmentSearchOut{}, err
			}
			out := mcpAttachmentSearchOut{Hits: make([]mcpAttachmentSearchHit, 0, len(hits))}
			for _, hit := range hits {
				out.Hits = append(out.Hits, mcpAttachmentSearchHit{
					OwnerSlug: hit.OwnerSlug, Filename: hit.Filename, URL: hit.URL,
					Excerpt: hit.Excerpt, Score: hit.Score,
				})
			}
			return nil, out, nil
		})
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "upload_attachment",
		Description: "Create a one-use native multipart upload URL for a document or image owned by a page. Arguments: exactly one of slug or path (required page identifier in namespace/page form), plus filename (required original filename without a directory). Requires write access to that page. HMD canonicalises filename; use the returned attachment_url rather than constructing one. POST the file as the file form field. Apache Tika extracts and indexes supported formats when document search is enabled. After a successful upload, use save_page to add [filename](attachment_url) to the owning page, or ![alt text](attachment_url) for an image.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpAttachmentUploadIn) (*mcp.CallToolResult, mcpAttachmentUploadOut, error) {
		if err := app.mcpRequireScope(ctx, scopeWrite); err != nil {
			return nil, mcpAttachmentUploadOut{}, err
		}
		slug, err := mcpPageIdentifier(in.Slug, in.Path)
		if err != nil {
			return nil, mcpAttachmentUploadOut{}, err
		}
		if err := app.mcpRequireSlug(ctx, slug); err != nil {
			return nil, mcpAttachmentUploadOut{}, err
		}
		grant, err := app.apiClient().IssueUploadCapability(ctx, slug, in.Filename)
		if err != nil {
			return nil, mcpAttachmentUploadOut{}, err
		}
		baseURL, _ := ctx.Value(mcpBaseURLKey{}).(string)
		return nil, mcpAttachmentUploadOut{
			UploadURL:     baseURL + "/_/api/attachment-uploads/" + grant.Token,
			AttachmentURL: "/_/attachments/" + grant.Slug + "/" + grant.Filename,
			ExpiresAt:     grant.Expires.UTC().Format(time.RFC3339),
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_attachment",
		Description: "Read an attachment's cached extracted text. Arguments: exactly one of slug or path (required owning-page identifier in namespace/page form), plus filename (required canonical attachment filename without a directory). Requires read access to the owning page. Uploads made while document indexing is enabled have a Git-tracked extraction sidecar; source text files also get one. Use search_attachments first for large documents.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpAttachmentReadIn) (*mcp.CallToolResult, mcpAttachmentReadOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpAttachmentReadOut{}, err
		}
		slug, err := mcpPageIdentifier(in.Slug, in.Path)
		if err != nil {
			return nil, mcpAttachmentReadOut{}, err
		}
		if err := app.mcpRequireSlug(ctx, slug); err != nil {
			return nil, mcpAttachmentReadOut{}, err
		}
		text, err := app.apiClient().ReadAttachment(ctx, slug, in.Filename)
		if err != nil {
			return nil, mcpAttachmentReadOut{}, err
		}
		return nil, mcpAttachmentReadOut{Text: text}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_namespaces",
		Description: "List namespaces accessible to this caller. Arguments: none. Returns each namespace's name, description, page count and public/private status. Pass a returned name to read_namespace for full settings.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, mcpNamespacesOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpNamespacesOut{}, err
		}
		out := mcpNamespacesOut{Namespaces: []mcpNamespaceMeta{}}
		for _, summary := range app.apiClient().ListNamespaces(ctx) {
			out.Namespaces = append(out.Namespaces, mcpNamespaceMeta{Name: summary.Name, Description: summary.Config.Description, Pages: summary.Count, Public: summary.Config.Public})
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_namespace",
		Description: "Read one namespace's full settings and current hash. Argument: name (required namespace name, for example notes; not a page slug or path). Requires settings access. Pass the returned hash as basehash to save_namespace when updating.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpNamespaceIn) (*mcp.CallToolResult, mcpNamespaceOut, error) {
		if err := app.mcpRequireScope(ctx, scopeSettings); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		detail, err := app.apiClient().ReadNamespace(ctx, in.Name)
		if err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		return nil, mcpNamespaceFromDetail(detail), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "save_namespace",
		Description: "Create or replace namespace settings (each save is a git commit). Arguments: name (required string), public (required boolean), widgets (optional complete string array), title, description, skin, palette and index (optional strings), tree (optional complete string array), new (optional object with template and slug naming-pattern strings), and basehash (required for updates; omit only on create). " +
			"Requires settings access. For updates, first call read_namespace and preserve every setting you do not intend to change; omitted optional fields are reset. Allowed widgets: pages, namespaces, pinned, tags, log, health, calendar, writing-stats, page-meta, backlinks, prev-entries. Allowed skins: phosphor, newsprint, journal, soft, bare. Allowed palettes: phosphor, catppuccin, dracula, everforest, gruvbox, monokai, nord, one dark, rosé pine, solarized, tokyo night. Index must be a one-segment page name. On conflict re-read and retry. Creating or updating settings does not delete or change pages.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSaveNamespaceIn) (*mcp.CallToolResult, mcpNamespaceOut, error) {
		if err := app.mcpRequireScope(ctx, scopeSettings); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		if !wiki.ValidNamespaceName(in.Name) {
			return nil, mcpNamespaceOut{}, fmt.Errorf("invalid namespace %q", in.Name)
		}
		if err := app.mcpRequireNamespace(ctx, in.Name); err != nil {
			return nil, mcpNamespaceOut{}, err
		}
		cfg := wiki.NamespaceConfig{
			Widgets: in.Widgets, Public: in.Public, Title: in.Title, Description: in.Description, Skin: in.Skin,
			Palette: in.Palette, Index: in.Index, Tree: in.Tree, New: in.New,
		}
		detail, err := app.apiClient().SaveNamespace(ctx, api.SaveNamespaceInput{
			Name:         in.Name,
			Config:       cfg,
			TemplateData: api.NewPageTemplateData{Now: time.Now(), User: app.mcpUser(ctx), Namespace: in.Name},
			BaseHash:     in.BaseHash,
		})
		if err != nil {
			if api.CategoryOf(err) == api.CategoryConflict {
				current, readErr := app.apiClient().ReadNamespace(ctx, in.Name)
				if readErr != nil {
					return nil, mcpNamespaceOut{}, readErr
				}
				payload, _ := json.Marshal(map[string]any{"error": "conflict: the namespace changed since basehash (or already exists)", "namespace": mcpNamespaceFromDetail(current)})
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}, mcpNamespaceOut{}, nil
			}
			return nil, mcpNamespaceOut{}, err
		}
		slog.Info("mcp namespace configured", "namespace", in.Name, "author", app.mcpUser(ctx))
		return nil, mcpNamespaceFromDetail(detail), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_page",
		Description: "Read one accessible page. Argument: exactly one of slug or path (required page identifier in namespace/page form, for example ai/readme); slug is canonical and path is an alias. Returns the raw Markdown body, title, tags, pin state and current hash. Pass those metadata values and the hash as basehash to save_page when updating.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSlugIn) (*mcp.CallToolResult, mcpPageOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpPageOut{}, err
		}
		slug, err := mcpPageIdentifier(in.Slug, in.Path)
		if err != nil {
			return nil, mcpPageOut{}, err
		}
		in.Slug = slug
		view, err := app.apiClient().ViewPage(ctx, slug)
		if err != nil {
			return nil, mcpPageOut{}, err
		}
		return nil, mcpPageOut{
			Slug: view.Slug, Title: view.Title, Tags: view.Tags, Body: view.Body,
			Pin: view.Pin, Hash: view.Hash,
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "save_page",
		Description: "Create or replace a wiki page (each save is a git commit). Arguments: exactly one of slug or path (required page identifier in namespace/page form; slug is canonical), body (required complete raw Markdown string), title (optional string), tags (optional complete string array), pin (optional boolean), and basehash (required for updates; omit only on create). " +
			"Requires write access. For updates, first call read_page and preserve title, tags and pin unless intentionally changing them; omitted metadata is reset. Pass its hash as basehash; on conflict, re-read or merge and retry with the fresh hash. Creating without basehash fails if the page already exists. Never overwrite without a fresh basehash.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSaveIn) (*mcp.CallToolResult, mcpSaveOut, error) {
		if err := app.mcpRequireScope(ctx, scopeWrite); err != nil {
			return nil, mcpSaveOut{}, err
		}
		slug, err := mcpPageIdentifier(in.Slug, in.Path)
		if err != nil {
			return nil, mcpSaveOut{}, err
		}
		in.Slug = slug
		if err := app.mcpRequireSlug(ctx, slug); err != nil {
			return nil, mcpSaveOut{}, err
		}
		mutation, err := app.apiClient().SavePage(ctx, api.SavePageInput{
			Slug: slug, Title: in.Title, Tags: in.Tags, Body: in.Body, Pin: in.Pin, BaseHash: in.BaseHash,
		})
		if err != nil {
			if api.CategoryOf(err) == api.CategoryConflict {
				content, currentHash, readErr := app.Store.Read(pageFile(slug))
				if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
					return nil, mcpSaveOut{}, readErr
				}
				current := ParsePage(slug, content)
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
			return nil, mcpSaveOut{}, err
		}
		slog.Info("mcp saved", "slug", mutation.Slug, "warning", mutation.IndexWarning)
		return nil, mcpSaveOut{Slug: mutation.Slug, Hash: mutation.BlobHash, IndexWarning: mutation.IndexWarning}, nil
	})

	app.registerMCPEditTool(server)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_page",
		Description: "Delete one accessible page. Argument: exactly one of slug or path (required page identifier in namespace/page form); slug is canonical and path is an alias. Requires write access. Deletion is immediate and does not use a basehash, but the page remains recoverable from Git history.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSlugIn) (*mcp.CallToolResult, any, error) {
		if err := app.mcpRequireScope(ctx, scopeWrite); err != nil {
			return nil, nil, err
		}
		slug, err := mcpPageIdentifier(in.Slug, in.Path)
		if err != nil {
			return nil, nil, err
		}
		in.Slug = slug
		if err := app.mcpRequireSlug(ctx, slug); err != nil {
			return nil, nil, err
		}
		if _, err := app.apiClient().DeletePage(ctx, slug); err != nil {
			return nil, nil, err
		}
		slog.Info("mcp deleted", "slug", slug)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "deleted " + slug}},
		}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search",
		Description: "Full-text search accessible page titles, bodies, and tags. Argument: query (required full-text query string). Results include escaped snippets with optional <mark> tags; use read_page for the complete page.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSearchIn) (*mcp.CallToolResult, mcpSearchOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpSearchOut{}, err
		}
		hits, err := app.apiClient().SearchPages(ctx, in.Query)
		if err != nil {
			return nil, mcpSearchOut{}, err
		}
		out := mcpSearchOut{Hits: make([]mcpSearchHit, 0, len(hits))}
		for _, h := range hits {
			out.Hits = append(out.Hits, mcpSearchHit{Slug: h.Slug, Title: h.Title, Snippet: h.Snippet, Tags: h.Tags})
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "backlinks",
		Description: "List accessible pages whose [[wiki-links]] target a given accessible page. Argument: exactly one of slug or path (required target-page identifier in namespace/page form); slug is canonical and path is an alias.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSlugIn) (*mcp.CallToolResult, mcpBacklinksOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpBacklinksOut{}, err
		}
		slug, err := mcpPageIdentifier(in.Slug, in.Path)
		if err != nil {
			return nil, mcpBacklinksOut{}, err
		}
		in.Slug = slug
		backlinks, err := app.apiClient().Backlinks(ctx, slug)
		if err != nil {
			return nil, mcpBacklinksOut{}, err
		}
		out := mcpBacklinksOut{Backlinks: make([]mcpPageMeta, 0, len(backlinks))}
		for _, backlink := range backlinks {
			out.Backlinks = append(out.Backlinks, mcpPageMeta{Slug: backlink.Slug, Title: backlink.Title, Tags: backlink.Tags})
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "recent_changes",
		Description: "List the newest accessible wiki commits first, with commit metadata and touched files. Argument: limit (optional positive integer, default 20). A commit is omitted if it includes files outside this caller's namespace access.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpRecentIn) (*mcp.CallToolResult, mcpRecentOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpRecentOut{}, err
		}
		commits, err := app.apiClient().RecentChanges(ctx, in.Limit)
		if err != nil {
			return nil, mcpRecentOut{}, err
		}
		out := mcpRecentOut{Commits: make([]mcpCommit, 0, len(commits))}
		for _, c := range commits {
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
			"Argument: namespace (optional namespace-name string; omit for the whole accessible wiki).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpHealthIn) (*mcp.CallToolResult, mcpHealthOut, error) {
		if err := app.mcpRequireScope(ctx, scopeRead); err != nil {
			return nil, mcpHealthOut{}, err
		}
		report, err := app.apiClient().Health(ctx, in.Namespace)
		if err != nil {
			return nil, mcpHealthOut{}, err
		}
		out := mcpHealthOut{Missing: make([]HealthMissingEntry, 0, len(report.Missing)), Orphans: report.Orphans}
		for _, entry := range report.Missing {
			out.Missing = append(out.Missing, HealthMissingEntry{Slug: entry.Slug, Sources: entry.Sources})
		}
		return nil, out, nil
	})

	return newMCPHTTPHandler(app.externalBaseURL(), func(*http.Request) *mcp.Server { return server })
}
