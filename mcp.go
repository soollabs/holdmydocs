package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Slug string `json:"slug" jsonschema:"page slug, e.g. my-page"`
}

type mcpPageOut struct {
	Slug   string   `json:"slug"`
	Title  string   `json:"title"`
	Tags   []string `json:"tags,omitempty"`
	Body   string   `json:"body"`
	Public bool     `json:"public,omitempty"`
	Pin    bool     `json:"pin,omitempty"`
	Hash   string   `json:"hash"`
}

type mcpSaveIn struct {
	Slug     string   `json:"slug" jsonschema:"page slug, e.g. my-page"`
	Title    string   `json:"title,omitempty" jsonschema:"page title; defaults to the slug"`
	Tags     []string `json:"tags,omitempty"`
	Body     string   `json:"body" jsonschema:"raw markdown body (frontmatter is managed by hmd)"`
	Public   bool     `json:"public,omitempty" jsonschema:"serve unauthenticated in the public garden; preserve the value from read_page when updating"`
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

// validMCPSlug rejects slugs that could escape the repo directory or reach
// the hidden-page namespace, mirroring validateHomeFilename's constraints.
func validMCPSlug(slug string) bool {
	return slug != "" && !strings.ContainsAny(slug, "/\\") && !strings.HasPrefix(slug, ".")
}

// mcpUser resolves the committing user for a tool call from the Bearer token
// on the underlying HTTP request (already verified by the middleware), so
// agent commits are attributed to the token's owner exactly like web saves.
func (app *App) mcpUser(req *mcp.CallToolRequest) string {
	if req.Extra == nil {
		return ""
	}
	if h := req.Extra.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if user, ok := app.Auth.UserForBearer(strings.TrimPrefix(h, "Bearer ")); ok {
			return user
		}
	}
	// Cookie-authenticated calls (same-origin tooling) still resolve.
	if cookie, err := (&http.Request{Header: req.Extra.Header}).Cookie("hmd_session"); err == nil {
		if user, ok := app.Auth.UserFor(cookie.Value); ok {
			return user
		}
	}
	return ""
}

// mcpHandler builds the MCP server and returns its streamable HTTP handler
// for mounting at /mcp. Auth happens in Auth.Middleware before this handler.
func (app *App) mcpHandler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "hmd", Version: version}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_pages",
		Description: "List all wiki pages with slug, title and tags.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, mcpListOut, error) {
		titles := app.Index.Titles()
		out := mcpListOut{Pages: []mcpPageMeta{}}
		for slug, title := range titles {
			out.Pages = append(out.Pages, mcpPageMeta{Slug: slug, Title: title, Tags: app.Index.TagsFor(slug)})
		}
		sort.Slice(out.Pages, func(i, j int) bool { return out.Pages[i].Slug < out.Pages[j].Slug })
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_page",
		Description: "Read a wiki page: raw markdown body plus the page's current hash. save_page requires that hash as basehash.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSlugIn) (*mcp.CallToolResult, mcpPageOut, error) {
		if !validMCPSlug(in.Slug) {
			return nil, mcpPageOut{}, fmt.Errorf("invalid slug %q", in.Slug)
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
			Slug: p.Slug, Title: p.Title, Tags: p.Tags, Body: p.Body, Public: p.Public,
			Pin: p.Pin, Hash: hash,
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "save_page",
		Description: "Create or update a wiki page (each save is a git commit). " +
			"Update: pass the basehash returned by read_page; on conflict the error carries the page's current hash and body — re-read or merge, then retry with that hash. " +
			"Create: omit basehash (fails if the page already exists). Never overwrite without a fresh basehash.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSaveIn) (*mcp.CallToolResult, mcpSaveOut, error) {
		if !validMCPSlug(in.Slug) {
			return nil, mcpSaveOut{}, fmt.Errorf("invalid slug %q", in.Slug)
		}
		title := strings.TrimSpace(in.Title)
		if title == "" {
			title = in.Slug
		}
		page := Page{
			Slug: in.Slug, Title: title, Tags: in.Tags, Body: in.Body, Public: in.Public,
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
		authorName, authorEmail := app.gitAuthor(app.mcpUser(req))
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
		if !validMCPSlug(in.Slug) {
			return nil, nil, fmt.Errorf("invalid slug %q", in.Slug)
		}
		authorName, authorEmail := app.gitAuthor(app.mcpUser(req))
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
		hits, err := app.Index.Search(in.Query)
		if err != nil {
			return nil, mcpSearchOut{}, err
		}
		out := mcpSearchOut{Hits: []mcpSearchHit{}}
		for _, h := range hits {
			out.Hits = append(out.Hits, mcpSearchHit(h))
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "backlinks",
		Description: "List the pages whose [[wiki-links]] point at the given page.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpSlugIn) (*mcp.CallToolResult, mcpBacklinksOut, error) {
		titles := app.Index.Titles()
		out := mcpBacklinksOut{Backlinks: []mcpPageMeta{}}
		for _, slug := range app.Index.Backlinks(in.Slug) {
			out.Backlinks = append(out.Backlinks, mcpPageMeta{Slug: slug, Title: titles[slug], Tags: app.Index.TagsFor(slug)})
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "recent_changes",
		Description: "The newest commits on the wiki, newest first, with the files each touched.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpRecentIn) (*mcp.CallToolResult, mcpRecentOut, error) {
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

	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		// stateless — no session state to time out or resume; revisit if a client needs server-initiated messages
		Stateless: true,
		// Rebinding protection rejects localhost connections with a public Host
		// header, which is exactly what a same-box reverse proxy sends. Auth is
		// a Bearer PAT (not ambient), so rebinding gains an attacker nothing.
		DisableLocalhostProtection: true,
	})
}
