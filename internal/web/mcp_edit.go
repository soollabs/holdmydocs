package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

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
	Slug    string `json:"slug"`
	Hash    string `json:"hash" jsonschema:"basehash for the next edit or save"`
	Changed bool   `json:"changed"`
	Diff    string `json:"diff" jsonschema:"unified diff of the body change; empty when unchanged"`
}

func (app *App) registerMCPEditTool(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "edit_page",
		Description: "Edit an existing page's Markdown body without resending it; title, tags and pin are preserved. Arguments: exactly one of slug or path (required page identifier in namespace/page form; slug is canonical), basehash (required hash from read_page or the last successful edit/save), edits (required non-empty array of {oldText, newText} exact-text replacements, applied in order), and optional dryRun. Requires write access. " +
			"Each oldText must occur exactly once at the time its edit is applied; a missing or ambiguous match fails the whole request without a write. One atomic commit applies all edits; stale hashes fail, unchanged bodies produce no commit. dryRun validates and returns a unified diff without committing. Returns slug, hash, changed and diff.",
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
		if in.BaseHash == "" {
			return nil, mcpEditOut{}, fmt.Errorf("basehash is required; read the target page first")
		}

		cfg := app.config()
		if cfg.SyncMode == "bidirectional" && cfg.Git.RemoteURL != "" {
			if _, err := app.Store.FetchAndFF(); err != nil {
				slog.Warn("mcp edit-time fetch", "slug", slug, "err", err)
			}
		}
		file := pageFile(slug)
		content, hash, err := app.Store.Read(file)
		if err != nil {
			return nil, mcpEditOut{}, err
		}
		if hash != in.BaseHash {
			return mcpEditConflict(hash), mcpEditOut{}, nil
		}
		page := ParsePage(slug, content)
		body, err := applyPageEdits(page.Body, in.Edits)
		if err != nil {
			return nil, mcpEditOut{}, err
		}
		if err := validatePageInput(page.Title, page.Tags, body); err != nil {
			return nil, mcpEditOut{}, err
		}
		changed := body != page.Body
		diff := pageEditDiff(page.Body, body)
		if in.DryRun {
			return nil, mcpEditOut{Slug: slug, Hash: hash, Changed: changed, Diff: diff}, nil
		}
		if changed {
			page.Body = body
			encoded := page.Encode()
			changed = !bytes.Equal(content, encoded)
			content = encoded
		}
		authorName, authorEmail := app.gitAuthor(app.mcpUser(ctx))
		hash, err = app.Store.SaveChecked(file, file, in.BaseHash, content, "Edit "+page.Title, authorName, authorEmail)
		if errors.Is(err, ErrConflict) {
			_, currentHash, readErr := app.Store.Read(file)
			if readErr != nil {
				return nil, mcpEditOut{}, fmt.Errorf("conflict: page changed or was deleted since basehash")
			}
			return mcpEditConflict(currentHash), mcpEditOut{}, nil
		}
		if err != nil {
			return nil, mcpEditOut{}, err
		}
		if changed {
			if err := app.Index.UpdatePage(page, hash); err != nil {
				return nil, mcpEditOut{}, err
			}
			slog.Info("mcp edited", "slug", slug, "author", authorName)
		}
		return nil, mcpEditOut{Slug: slug, Hash: hash, Changed: changed, Diff: diff}, nil
	})
}

func mcpEditConflict(hash string) *mcp.CallToolResult {
	payload, _ := json.Marshal(map[string]string{"error": "conflict: page changed since basehash; re-read before retrying", "hash": hash})
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}
}

func applyPageEdits(body string, edits []mcpPageEdit) (string, error) {
	if len(edits) == 0 {
		return "", fmt.Errorf("edits must contain at least one replacement")
	}
	if len(edits) > 128 {
		return "", fmt.Errorf("edits exceeds 128 replacements")
	}
	if len(body) > maxPageBodyBytes {
		return "", fmt.Errorf("page body exceeds edit limit")
	}
	for i, edit := range edits {
		if edit.OldText == "" {
			return "", fmt.Errorf("edit %d oldText must not be empty", i+1)
		}
		if !utf8.ValidString(edit.OldText) || !utf8.ValidString(edit.NewText) {
			return "", fmt.Errorf("edit %d text must be valid UTF-8", i+1)
		}
		count := textOccurrenceCount(body, edit.OldText)
		if count == 0 {
			return "", fmt.Errorf("edit %d oldText was not found", i+1)
		}
		if count != 1 {
			return "", fmt.Errorf("edit %d oldText matched %d times; provide more context", i+1, count)
		}
		body = strings.Replace(body, edit.OldText, edit.NewText, 1)
		if len(body) > maxPageBodyBytes {
			return "", fmt.Errorf("edited body exceeds %d bytes", maxPageBodyBytes)
		}
	}
	return body, nil
}

func pageEditDiff(old, new string) string {
	if old == new {
		return ""
	}
	var out strings.Builder
	out.WriteString("--- body (before)\n+++ body (after)\n")
	fmt.Fprintf(&out, "@@ -1,%d +1,%d @@\n", textLineCount(old), textLineCount(new))
	for _, line := range strings.SplitAfter(old, "\n") {
		out.WriteByte('-')
		out.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			out.WriteByte('\n')
		}
	}
	for _, line := range strings.SplitAfter(new, "\n") {
		out.WriteByte('+')
		out.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func textLineCount(text string) int {
	if text == "" {
		return 0
	}
	count := strings.Count(text, "\n")
	if strings.HasSuffix(text, "\n") {
		return count
	}
	return count + 1
}

func textOccurrenceCount(text, needle string) int {
	count := 0
	for start := 0; ; {
		index := strings.Index(text[start:], needle)
		if index < 0 {
			return count
		}
		count++
		start += index + 1
	}
}
