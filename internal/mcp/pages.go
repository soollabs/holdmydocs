package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"hmd/internal/api"
)

type mcpPageMeta struct {
	Slug  string   `json:"slug"`
	Title string   `json:"title"`
	Tags  []string `json:"tags,omitempty"`
}

type mcpListOut struct {
	Pages []mcpPageMeta `json:"pages"`
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

type mcpPageEdit struct {
	OldText string `json:"oldText" jsonschema:"exact existing Markdown text to replace; it must occur exactly once"`
	NewText string `json:"newText" jsonschema:"replacement Markdown text; may be empty to delete oldText"`
}

type mcpEditIn struct {
	Slug     string        `json:"slug,omitempty" jsonschema:"canonical page identifier in namespace/page form; provide slug or path, not both"`
	Path     string        `json:"path,omitempty" jsonschema:"alias for slug, in namespace/page form; provide slug or path, not both"`
	BaseHash string        `json:"basehash" jsonschema:"required hash from read_page or the last successful edit/save; stale hashes are rejected"`
	Edits    []mcpPageEdit `json:"edits" jsonschema:"one or more exact-text replacements, applied in order"`
	DryRun   bool          `json:"dryRun,omitempty" jsonschema:"when true, return the diff without committing"`
}

type mcpEditOut struct {
	Slug         string `json:"slug"`
	Hash         string `json:"hash" jsonschema:"basehash for the next edit or save"`
	Changed      bool   `json:"changed"`
	Diff         string `json:"diff" jsonschema:"unified diff of the body change; empty when unchanged"`
	IndexWarning string `json:"index_warning,omitempty" jsonschema:"set when the edit committed but the search index refresh failed; the commit is durable and indexing is reconciled in the background"`
}

type mcpBacklinksOut struct {
	Backlinks []mcpPageMeta `json:"backlinks"`
}

type mcpHealthIn struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"limit the report to this namespace; omit for the whole wiki"`
}

type mcpHealthEntry struct {
	Slug    string   `json:"slug"`
	Sources []string `json:"sources"`
}

type mcpHealthStaleEntry struct {
	Slug        string    `json:"slug"`
	LastUpdated time.Time `json:"last_updated"`
}

type mcpHealthOut struct {
	Missing []mcpHealthEntry      `json:"missing"`
	Orphans []string              `json:"orphans"`
	Stale   []mcpHealthStaleEntry `json:"stale"`
}

func (s *Server) registerPages(server *sdk.Server) {
	sdk.AddTool(server, &sdk.Tool{
		Name:        "list_pages",
		Description: "List every page accessible to this caller. Arguments: none. Returns each canonical namespace/page slug, title and tags. Pass a returned slug to read_page before updating it.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, _ any) (*sdk.CallToolResult, mcpListOut, error) {
		if err := s.requireScope(ctx, api.ScopeRead); err != nil {
			return nil, mcpListOut{}, err
		}
		pages, err := s.api.ListPages(ctx)
		if err != nil {
			return nil, mcpListOut{}, err
		}
		out := mcpListOut{Pages: make([]mcpPageMeta, 0, len(pages))}
		for _, page := range pages {
			out.Pages = append(out.Pages, mcpPageMeta{Slug: page.Slug, Title: page.Title, Tags: page.Tags})
		}
		return nil, out, nil
	})

	sdk.AddTool(server, &sdk.Tool{
		Name:        "read_page",
		Description: "Read one accessible page. Argument: exactly one of slug or path (required page identifier in namespace/page form, for example ai/readme); slug is canonical and path is an alias. Returns the raw Markdown body, title, tags, pin state and current hash. Pass those metadata values and the hash as basehash to save_page when updating.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in mcpSlugIn) (*sdk.CallToolResult, mcpPageOut, error) {
		if err := s.requireScope(ctx, api.ScopeRead); err != nil {
			return nil, mcpPageOut{}, err
		}
		slug, err := mcpPageIdentifier(in.Slug, in.Path)
		if err != nil {
			return nil, mcpPageOut{}, err
		}
		if err := s.requireSlug(ctx, slug); err != nil {
			return nil, mcpPageOut{}, err
		}
		view, err := s.api.ViewPage(ctx, slug)
		if err != nil {
			return nil, mcpPageOut{}, err
		}
		return nil, mcpPageOut{
			Slug: view.Slug, Title: view.Title, Tags: view.Tags, Body: view.Body,
			Pin: view.Pin, Hash: view.Hash,
		}, nil
	})

	sdk.AddTool(server, &sdk.Tool{
		Name: "save_page",
		Description: "Create or replace a wiki page (each save is a git commit). Arguments: exactly one of slug or path (required page identifier in namespace/page form; slug is canonical), body (required complete raw Markdown string), title (optional string), tags (optional complete string array), pin (optional boolean), and basehash (required for updates; omit only on create). " +
			"Requires write access. For updates, first call read_page and preserve title, tags and pin unless intentionally changing them; omitted metadata is reset. Pass its hash as basehash; on conflict, re-read or merge and retry with the fresh hash. Creating without basehash fails if the page already exists. Never overwrite without a fresh basehash.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in mcpSaveIn) (*sdk.CallToolResult, mcpSaveOut, error) {
		if err := s.requireScope(ctx, api.ScopeWrite); err != nil {
			return nil, mcpSaveOut{}, err
		}
		slug, err := mcpPageIdentifier(in.Slug, in.Path)
		if err != nil {
			return nil, mcpSaveOut{}, err
		}
		if err := s.requireSlug(ctx, slug); err != nil {
			return nil, mcpSaveOut{}, err
		}
		mutation, err := s.api.SavePage(ctx, api.SavePageInput{
			Slug: slug, Title: in.Title, Tags: in.Tags, Body: in.Body, Pin: in.Pin, BaseHash: in.BaseHash,
		})
		if err != nil {
			if api.CategoryOf(err) == api.CategoryConflict {
				currentHash, currentBody := "", ""
				if view, readErr := s.api.ViewPage(ctx, slug); readErr == nil {
					currentHash, currentBody = view.Hash, view.Body
				} else if api.CategoryOf(readErr) != api.CategoryNotFound {
					return nil, mcpSaveOut{}, readErr
				}
				return mcpToolError(map[string]string{
					"error": "conflict: the page changed since basehash (or already exists)",
					"hash":  currentHash,
					"body":  currentBody,
				}), mcpSaveOut{}, nil
			}
			return nil, mcpSaveOut{}, err
		}
		slog.Info("mcp saved", "slug", mutation.Slug, "warning", mutation.IndexWarning)
		return nil, mcpSaveOut{Slug: mutation.Slug, Hash: mutation.BlobHash, IndexWarning: mutation.IndexWarning}, nil
	})

	s.registerEditTool(server)

	sdk.AddTool(server, &sdk.Tool{
		Name:        "delete_page",
		Description: "Delete one accessible page. Argument: exactly one of slug or path (required page identifier in namespace/page form); slug is canonical and path is an alias. Requires write access. Deletion is immediate and does not use a basehash, but the page remains recoverable from Git history.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in mcpSlugIn) (*sdk.CallToolResult, any, error) {
		if err := s.requireScope(ctx, api.ScopeWrite); err != nil {
			return nil, nil, err
		}
		slug, err := mcpPageIdentifier(in.Slug, in.Path)
		if err != nil {
			return nil, nil, err
		}
		if err := s.requireSlug(ctx, slug); err != nil {
			return nil, nil, err
		}
		if _, err := s.api.DeletePage(ctx, slug, false); err != nil {
			return nil, nil, err
		}
		slog.Info("mcp deleted", "slug", slug)
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: "deleted " + slug}},
		}, nil, nil
	})

	sdk.AddTool(server, &sdk.Tool{
		Name:        "backlinks",
		Description: "List accessible pages whose [[wiki-links]] target a given accessible page. Argument: exactly one of slug or path (required target-page identifier in namespace/page form); slug is canonical and path is an alias.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in mcpSlugIn) (*sdk.CallToolResult, mcpBacklinksOut, error) {
		if err := s.requireScope(ctx, api.ScopeRead); err != nil {
			return nil, mcpBacklinksOut{}, err
		}
		slug, err := mcpPageIdentifier(in.Slug, in.Path)
		if err != nil {
			return nil, mcpBacklinksOut{}, err
		}
		backlinks, err := s.api.Backlinks(ctx, slug)
		if err != nil {
			return nil, mcpBacklinksOut{}, err
		}
		out := mcpBacklinksOut{Backlinks: make([]mcpPageMeta, 0, len(backlinks))}
		for _, backlink := range backlinks {
			out.Backlinks = append(out.Backlinks, mcpPageMeta{Slug: backlink.Slug, Title: backlink.Title, Tags: backlink.Tags})
		}
		return nil, out, nil
	})

	sdk.AddTool(server, &sdk.Tool{
		Name: "health",
		Description: fmt.Sprintf("Read-only wiki hygiene report for accessible pages: dangling [[wiki-links]] (target page missing), orphan pages (no incoming links), and stale pages (no Git revision in at least %d days, with last-updated time). ", api.StalePageDays) +
			"Argument: namespace (optional namespace-name string; omit for the whole accessible wiki).",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in mcpHealthIn) (*sdk.CallToolResult, mcpHealthOut, error) {
		if err := s.requireScope(ctx, api.ScopeRead); err != nil {
			return nil, mcpHealthOut{}, err
		}
		report, err := s.api.Health(ctx, in.Namespace)
		if err != nil {
			return nil, mcpHealthOut{}, err
		}
		out := mcpHealthOut{
			Missing: make([]mcpHealthEntry, 0, len(report.Missing)),
			Orphans: report.Orphans,
			Stale:   make([]mcpHealthStaleEntry, 0, len(report.Stale)),
		}
		for _, entry := range report.Missing {
			out.Missing = append(out.Missing, mcpHealthEntry{Slug: entry.Slug, Sources: entry.Sources})
		}
		for _, entry := range report.Stale {
			out.Stale = append(out.Stale, mcpHealthStaleEntry{Slug: entry.Slug, LastUpdated: entry.LastUpdated})
		}
		return nil, out, nil
	})
}

func (s *Server) registerEditTool(server *sdk.Server) {
	sdk.AddTool(server, &sdk.Tool{
		Name: "edit_page",
		Description: "Edit an existing page's Markdown body without resending it; title, tags and pin are preserved. Arguments: exactly one of slug or path (required page identifier in namespace/page form; slug is canonical), basehash (required hash from read_page or the last successful edit/save), edits (required non-empty array of {oldText, newText} exact-text replacements, applied in order), and optional dryRun. Requires write access. " +
			"Each oldText must occur exactly once at the time its edit is applied; a missing or ambiguous match fails the whole request without a write. One atomic commit applies all edits; stale hashes fail, unchanged bodies produce no commit. dryRun validates and returns a unified diff without committing. Returns slug, hash, changed and diff. A non-empty index_warning means the commit succeeded but index reconciliation is pending.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in mcpEditIn) (*sdk.CallToolResult, mcpEditOut, error) {
		if err := s.requireScope(ctx, api.ScopeWrite); err != nil {
			return nil, mcpEditOut{}, err
		}
		slug, err := mcpPageIdentifier(in.Slug, in.Path)
		if err != nil {
			return nil, mcpEditOut{}, err
		}
		if err := s.requireSlug(ctx, slug); err != nil {
			return nil, mcpEditOut{}, err
		}
		edits := make([]api.PageEdit, 0, len(in.Edits))
		for _, edit := range in.Edits {
			edits = append(edits, api.PageEdit{OldText: edit.OldText, NewText: edit.NewText})
		}
		result, err := s.api.EditPage(ctx, api.EditPageInput{Slug: slug, BaseHash: in.BaseHash, Edits: edits, DryRun: in.DryRun})
		if err != nil {
			if api.CategoryOf(err) == api.CategoryConflict {
				view, readErr := s.api.ViewPage(ctx, slug)
				if readErr != nil {
					return nil, mcpEditOut{}, fmt.Errorf("conflict: page changed or was deleted since basehash")
				}
				return mcpEditConflict(view.Hash), mcpEditOut{}, nil
			}
			return nil, mcpEditOut{}, err
		}
		return nil, mcpEditOut{
			Slug: result.Slug, Hash: result.BlobHash, Changed: result.Changed,
			Diff: result.Diff, IndexWarning: result.IndexWarning,
		}, nil
	})
}
