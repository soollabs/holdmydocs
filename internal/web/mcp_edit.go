package web

import (
	"context"
	"encoding/json"
	"fmt"

	"hmd/internal/api"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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

func (app *App) registerMCPEditTool(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "edit_page",
		Description: "Edit an existing page's Markdown body without resending it; title, tags and pin are preserved. Arguments: exactly one of slug or path (required page identifier in namespace/page form; slug is canonical), basehash (required hash from read_page or the last successful edit/save), edits (required non-empty array of {oldText, newText} exact-text replacements, applied in order), and optional dryRun. Requires write access. " +
			"Each oldText must occur exactly once at the time its edit is applied; a missing or ambiguous match fails the whole request without a write. One atomic commit applies all edits; stale hashes fail, unchanged bodies produce no commit. dryRun validates and returns a unified diff without committing. Returns slug, hash, changed and diff. A non-empty index_warning means the commit succeeded but index reconciliation is pending.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpEditIn) (*mcp.CallToolResult, mcpEditOut, error) {
		if err := app.mcpRequireScope(ctx, scopeWrite); err != nil {
			return nil, mcpEditOut{}, err
		}
		slug, err := mcpPageIdentifier(in.Slug, in.Path)
		if err != nil {
			return nil, mcpEditOut{}, err
		}
		if err := app.mcpRequireSlug(ctx, slug); err != nil {
			return nil, mcpEditOut{}, err
		}
		edits := make([]api.PageEdit, 0, len(in.Edits))
		for _, edit := range in.Edits {
			edits = append(edits, api.PageEdit{OldText: edit.OldText, NewText: edit.NewText})
		}
		result, err := app.apiClient().EditPage(ctx, api.EditPageInput{Slug: slug, BaseHash: in.BaseHash, Edits: edits, DryRun: in.DryRun})
		if err != nil {
			if api.CategoryOf(err) == api.CategoryConflict {
				_, currentHash, readErr := app.Store.Read(pageFile(slug))
				if readErr != nil {
					return nil, mcpEditOut{}, fmt.Errorf("conflict: page changed or was deleted since basehash")
				}
				return mcpEditConflict(currentHash), mcpEditOut{}, nil
			}
			return nil, mcpEditOut{}, err
		}
		return nil, mcpEditOut{
			Slug: result.Slug, Hash: result.BlobHash, Changed: result.Changed,
			Diff: result.Diff, IndexWarning: result.IndexWarning,
		}, nil
	})
}

func mcpEditConflict(hash string) *mcp.CallToolResult {
	payload, _ := json.Marshal(map[string]string{"error": "conflict: page changed since basehash; re-read before retrying", "hash": hash})
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}
}
