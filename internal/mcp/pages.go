package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"hmd/internal/api"
	"hmd/internal/wiki"
)

type mcpPageMeta struct {
	Slug  string   `json:"slug"`
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
}

func mcpPageMetaOf(slug, title string, tags []string) mcpPageMeta {
	return mcpPageMeta{Slug: slug, Title: title, Tags: nonNil(tags)}
}

// nonNil returns s, or an empty slice when s is nil, so a list is never sent
// as null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// defaultListPages is the list_pages window when no limit is given.
var defaultListPages = toolDefault("list_pages", "limit")

type mcpListIn struct {
	Namespace string `json:"namespace,omitempty"`
	Offset    int    `json:"offset,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type mcpListOut struct {
	Pages      []mcpPageMeta `json:"pages"`
	More       bool          `json:"more"`
	NextOffset int           `json:"next_offset"`
}

type mcpSlugIn struct {
	Slug string `json:"slug"`
}

type mcpReadPageIn struct {
	Slug    string `json:"slug"`
	Section string `json:"section,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type mcpPageOut struct {
	Slug  string   `json:"slug"`
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
	Body  string   `json:"body"`
	Pin   bool     `json:"pin"`
	Hash  string   `json:"hash"`
	textWindow
}

type mcpSaveIn struct {
	Slug     string    `json:"slug"`
	BaseHash string    `json:"basehash,omitempty"`
	Body     *string   `json:"body,omitempty"`
	Title    *string   `json:"title,omitempty"`
	Tags     *[]string `json:"tags,omitempty"`
	Pin      *bool     `json:"pin,omitempty"`
}

type mcpSaveOut struct {
	Slug         string `json:"slug"`
	Hash         string `json:"hash"`
	IndexWarning string `json:"index_warning,omitempty"`
}

type mcpPageEdit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type mcpEditIn struct {
	Slug     string        `json:"slug"`
	BaseHash string        `json:"basehash"`
	Edits    []mcpPageEdit `json:"edits"`
	DryRun   bool          `json:"dryRun,omitempty"`
}

type mcpEditOut struct {
	Slug         string `json:"slug"`
	Hash         string `json:"hash"`
	Changed      bool   `json:"changed"`
	Diff         string `json:"diff"`
	IndexWarning string `json:"index_warning,omitempty"`
}

type mcpDeleteIn struct {
	Slug     string `json:"slug"`
	BaseHash string `json:"basehash,omitempty"`
}

type mcpDeleteOut struct {
	Slug string `json:"slug"`
}

type mcpMoveIn struct {
	Slug     string `json:"slug"`
	NewSlug  string `json:"new_slug"`
	BaseHash string `json:"basehash"`
}

type mcpRenameIn struct {
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	BaseHash string `json:"basehash"`
}

type mcpRenameOut struct {
	Slug         string   `json:"slug"`
	Hash         string   `json:"hash"`
	IndexWarning string   `json:"index_warning,omitempty"`
	FailedLinks  []string `json:"failed_links"`
}

type mcpBacklinksOut struct {
	Backlinks []mcpPageMeta `json:"backlinks"`
}

type mcpHealthIn struct {
	Namespace string `json:"namespace,omitempty"`
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

// maxListedSections caps the headings a missing-section error names.
const maxListedSections = 50

// sectionNotFound reports a missing section with the headings the body has,
// so the caller can pick one.
func sectionNotFound(heading, body string) error {
	sections := wiki.Sections(body)
	if len(sections) == 0 {
		return fmt.Errorf("section %q not found: the page has no headings", heading)
	}
	listed := sections[:min(len(sections), maxListedSections)]
	names := make([]string, 0, len(listed))
	for _, section := range listed {
		names = append(names, strconv.Quote(section.Heading))
	}
	more := ""
	if len(sections) > maxListedSections {
		more = fmt.Sprintf(" and %d more", len(sections)-maxListedSections)
	}
	return fmt.Errorf("section %q not found; the page's headings are %s%s", heading, strings.Join(names, ", "), more)
}

func (s *Server) registerPages(server *sdk.Server) {
	addTool(s, server, "list_pages", func(ctx context.Context, req *sdk.CallToolRequest, in mcpListIn) (*sdk.CallToolResult, mcpListOut, error) {
		limit := in.Limit
		if limit == 0 {
			limit = defaultListPages
		}
		if in.Namespace != "" {
			if err := wiki.CheckNamespaceName(in.Namespace); err != nil {
				return nil, mcpListOut{}, fmt.Errorf("invalid namespace %q: %w", in.Namespace, err)
			}
			if err := s.requireNamespace(ctx, in.Namespace); err != nil {
				return nil, mcpListOut{}, err
			}
		}
		pages, err := s.api.ListPages(ctx)
		if err != nil {
			return nil, mcpListOut{}, err
		}
		if in.Namespace != "" {
			pages = slices.DeleteFunc(pages, func(page api.PageSummary) bool {
				namespace, _ := wiki.NamespaceFor(page.Slug)
				return namespace != in.Namespace
			})
		}
		start := min(in.Offset, len(pages))
		end := min(start+limit, len(pages))
		out := mcpListOut{Pages: make([]mcpPageMeta, 0, end-start), More: end < len(pages), NextOffset: end}
		for _, page := range pages[start:end] {
			out.Pages = append(out.Pages, mcpPageMetaOf(page.Slug, page.Title, page.Tags))
		}
		return nil, out, nil
	})

	addTool(s, server, "read_page", func(ctx context.Context, req *sdk.CallToolRequest, in mcpReadPageIn) (*sdk.CallToolResult, mcpPageOut, error) {
		slug, err := mcpPageSlug(in.Slug)
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
		body := view.Body
		if in.Section != "" {
			section, ok := wiki.FindSection(body, in.Section)
			if !ok {
				return nil, mcpPageOut{}, sectionNotFound(in.Section, body)
			}
			body = body[section.Start:section.End]
		}
		out := mcpPageOut{Slug: view.Slug, Title: view.Title, Tags: nonNil(view.Tags), Pin: view.Pin, Hash: view.Hash}
		out.Body, out.textWindow, err = windowText(body, in.Offset, in.Limit)
		if err != nil {
			return nil, mcpPageOut{}, err
		}
		return nil, out, nil
	})

	addTool(s, server, "save_page", func(ctx context.Context, req *sdk.CallToolRequest, in mcpSaveIn) (*sdk.CallToolResult, mcpSaveOut, error) {
		slug, err := mcpPageSlug(in.Slug)
		if err != nil {
			return nil, mcpSaveOut{}, err
		}
		if err := s.requireSlug(ctx, slug); err != nil {
			return nil, mcpSaveOut{}, err
		}
		save := api.SavePageInput{Slug: slug, BaseHash: in.BaseHash}
		if in.BaseHash != "" {
			// Start from the revision the caller read. The checked save
			// below fails unless that revision is still current, so the
			// fields kept here cannot come from a newer edit.
			current, err := s.api.ViewPage(ctx, slug)
			if api.CategoryOf(err) == api.CategoryNotFound {
				return nil, mcpSaveOut{}, mcpConflict("the page no longer exists", "")
			}
			if err != nil {
				return nil, mcpSaveOut{}, err
			}
			if current.Hash != in.BaseHash {
				return nil, mcpSaveOut{}, mcpPageConflict("the page changed since basehash", current)
			}
			save.Title, save.Tags, save.Body, save.Pin = current.Title, current.Tags, current.Body, current.Pin
		} else if in.Body == nil {
			return nil, mcpSaveOut{}, fmt.Errorf("body is required to create a page")
		}
		if in.Title != nil {
			save.Title = *in.Title
		}
		if in.Tags != nil {
			save.Tags = *in.Tags
		}
		if in.Body != nil {
			save.Body = *in.Body
		}
		if in.Pin != nil {
			save.Pin = *in.Pin
		}
		mutation, err := s.api.SavePage(ctx, save)
		if api.CategoryOf(err) == api.CategoryConflict {
			current, _ := s.api.ViewPage(ctx, slug)
			if in.BaseHash == "" {
				return nil, mcpSaveOut{}, mcpPageConflict("the page already exists; read it and pass its hash to update it", current)
			}
			return nil, mcpSaveOut{}, mcpPageConflict("the page changed since basehash", current)
		}
		if err != nil {
			return nil, mcpSaveOut{}, err
		}
		slog.Info("mcp saved", "slug", mutation.Slug, "warning", mutation.IndexWarning)
		return nil, mcpSaveOut{Slug: mutation.Slug, Hash: mutation.BlobHash, IndexWarning: mutation.IndexWarning}, nil
	})

	s.registerEditTool(server)

	addTool(s, server, "delete_page", func(ctx context.Context, req *sdk.CallToolRequest, in mcpDeleteIn) (*sdk.CallToolResult, mcpDeleteOut, error) {
		slug, err := mcpPageSlug(in.Slug)
		if err != nil {
			return nil, mcpDeleteOut{}, err
		}
		if err := s.requireSlug(ctx, slug); err != nil {
			return nil, mcpDeleteOut{}, err
		}
		_, err = s.api.DeletePage(ctx, slug, false, in.BaseHash)
		if api.CategoryOf(err) == api.CategoryConflict {
			hash := ""
			if view, readErr := s.api.ViewPage(ctx, slug); readErr == nil {
				hash = view.Hash
			}
			return nil, mcpDeleteOut{}, mcpConflict("the page changed since basehash or no longer exists", hash)
		}
		if err != nil {
			return nil, mcpDeleteOut{}, err
		}
		slog.Info("mcp deleted", "slug", slug)
		return nil, mcpDeleteOut{Slug: slug}, nil
	})

	addTool(s, server, "move_page", func(ctx context.Context, req *sdk.CallToolRequest, in mcpMoveIn) (*sdk.CallToolResult, mcpSaveOut, error) {
		slug, err := mcpPageSlug(in.Slug)
		if err != nil {
			return nil, mcpSaveOut{}, err
		}
		newSlug, err := mcpPageSlug(in.NewSlug)
		if err != nil {
			return nil, mcpSaveOut{}, err
		}
		if err := s.requireSlug(ctx, slug); err != nil {
			return nil, mcpSaveOut{}, err
		}
		if err := s.requireSlug(ctx, newSlug); err != nil {
			return nil, mcpSaveOut{}, err
		}
		if newSlug == slug {
			return nil, mcpSaveOut{}, api.InvalidInput("new_slug is the page's current slug", nil)
		}
		if in.BaseHash == "" {
			return nil, mcpSaveOut{}, api.InvalidInput("basehash is required; read the page first", nil)
		}
		// MovePage accepts a missing source for the editor's unsaved pages.
		if _, err := s.api.ViewPage(ctx, slug); err != nil {
			return nil, mcpSaveOut{}, err
		}
		mutation, err := s.api.MovePage(ctx, api.MovePageInput{FromSlug: slug, ToSlug: newSlug, BaseHash: in.BaseHash})
		if api.CategoryOf(err) == api.CategoryConflict {
			hash := ""
			if view, readErr := s.api.ViewPage(ctx, slug); readErr == nil {
				hash = view.Hash
			}
			return nil, mcpSaveOut{}, mcpConflict("new_slug already holds a page, or the page changed since basehash", hash)
		}
		if err != nil {
			return nil, mcpSaveOut{}, err
		}
		slog.Info("mcp moved", "from", slug, "to", mutation.Slug)
		return nil, mcpSaveOut{Slug: mutation.Slug, Hash: mutation.BlobHash, IndexWarning: mutation.IndexWarning}, nil
	})

	addTool(s, server, "rename_page", func(ctx context.Context, req *sdk.CallToolRequest, in mcpRenameIn) (*sdk.CallToolResult, mcpRenameOut, error) {
		slug, err := mcpPageSlug(in.Slug)
		if err != nil {
			return nil, mcpRenameOut{}, err
		}
		if err := s.requireSlug(ctx, slug); err != nil {
			return nil, mcpRenameOut{}, err
		}
		if in.BaseHash == "" {
			return nil, mcpRenameOut{}, api.InvalidInput("basehash is required; read the page first", nil)
		}
		result, err := s.api.RenamePage(ctx, api.RenamePageInput{Slug: slug, NewTitle: in.Title, BaseHash: in.BaseHash})
		if api.CategoryOf(err) == api.CategoryConflict {
			hash := ""
			if view, readErr := s.api.ViewPage(ctx, slug); readErr == nil {
				hash = view.Hash
			}
			return nil, mcpRenameOut{}, mcpConflict("a page with that title already exists, or the page changed since basehash", hash)
		}
		if err != nil {
			return nil, mcpRenameOut{}, err
		}
		slog.Info("mcp renamed", "from", slug, "to", result.Slug, "failures", len(result.FailedLinks))
		return nil, mcpRenameOut{Slug: result.Slug, Hash: result.BlobHash, IndexWarning: result.IndexWarning, FailedLinks: nonNil(result.FailedLinks)}, nil
	})

	addTool(s, server, "backlinks", func(ctx context.Context, req *sdk.CallToolRequest, in mcpSlugIn) (*sdk.CallToolResult, mcpBacklinksOut, error) {
		slug, err := mcpPageSlug(in.Slug)
		if err != nil {
			return nil, mcpBacklinksOut{}, err
		}
		backlinks, err := s.api.Backlinks(ctx, slug)
		if err != nil {
			return nil, mcpBacklinksOut{}, err
		}
		out := mcpBacklinksOut{Backlinks: make([]mcpPageMeta, 0, len(backlinks))}
		for _, backlink := range backlinks {
			out.Backlinks = append(out.Backlinks, mcpPageMetaOf(backlink.Slug, backlink.Title, backlink.Tags))
		}
		return nil, out, nil
	})

	addTool(s, server, "health", func(ctx context.Context, req *sdk.CallToolRequest, in mcpHealthIn) (*sdk.CallToolResult, mcpHealthOut, error) {
		report, err := s.api.Health(ctx, in.Namespace)
		if err != nil {
			return nil, mcpHealthOut{}, err
		}
		out := mcpHealthOut{
			Missing: make([]mcpHealthEntry, 0, len(report.Missing)),
			Orphans: nonNil(report.Orphans),
			Stale:   make([]mcpHealthStaleEntry, 0, len(report.Stale)),
		}
		for _, entry := range report.Missing {
			out.Missing = append(out.Missing, mcpHealthEntry{Slug: entry.Slug, Sources: nonNil(entry.Sources)})
		}
		for _, entry := range report.Stale {
			out.Stale = append(out.Stale, mcpHealthStaleEntry{Slug: entry.Slug, LastUpdated: entry.LastUpdated})
		}
		return nil, out, nil
	})
}

func (s *Server) registerEditTool(server *sdk.Server) {
	addTool(s, server, "edit_page", func(ctx context.Context, req *sdk.CallToolRequest, in mcpEditIn) (*sdk.CallToolResult, mcpEditOut, error) {
		slug, err := mcpPageSlug(in.Slug)
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
		if api.CategoryOf(err) == api.CategoryConflict {
			hash := ""
			if view, readErr := s.api.ViewPage(ctx, slug); readErr == nil {
				hash = view.Hash
			}
			return nil, mcpEditOut{}, mcpConflict("the page changed since basehash", hash)
		}
		if err != nil {
			return nil, mcpEditOut{}, err
		}
		return nil, mcpEditOut{
			Slug: result.Slug, Hash: result.BlobHash, Changed: result.Changed,
			Diff: result.Diff, IndexWarning: result.IndexWarning,
		}, nil
	})
}
