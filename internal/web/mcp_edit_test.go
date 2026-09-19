package web

import (
	"strings"
	"testing"
)

func TestMCPEditPage(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	session := connectMCP(t, server, token)
	slug := testNS + "/edit-note"
	res := callTool(t, session, "save_page", map[string]any{"slug": slug, "title": "Edit Note", "tags": []string{"ai"}, "pin": true, "body": "old one\nold two"})
	if res.IsError {
		t.Fatal(toolText(t, res))
	}
	var created mcpSaveOut
	toolJSON(t, res, &created)

	read := func() mcpPageOut {
		t.Helper()
		res := callTool(t, session, "read_page", map[string]any{"slug": slug})
		if res.IsError {
			t.Fatal(toolText(t, res))
		}
		var page mcpPageOut
		toolJSON(t, res, &page)
		return page
	}
	for _, args := range []map[string]any{
		{"slug": slug, "edits": []any{map[string]any{"oldText": "old", "newText": "new"}}},
		{"slug": slug, "basehash": "", "edits": []any{map[string]any{"oldText": "old one", "newText": "new one"}}},
		{"slug": slug, "basehash": created.Hash, "edits": []any{}},
		{"slug": slug, "basehash": created.Hash, "edits": []any{map[string]any{"oldText": "old", "newText": "new"}}},
		{"slug": slug, "basehash": created.Hash, "edits": []any{map[string]any{"oldText": "missing", "newText": "new"}}},
		{"slug": "../bad", "basehash": created.Hash, "edits": []any{map[string]any{"oldText": "old one", "newText": "new one"}}},
	} {
		res := callTool(t, session, "edit_page", args)
		if !res.IsError {
			t.Fatalf("invalid edit succeeded: %v", args)
		}
		if page := read(); page.Hash != created.Hash || page.Body != "old one\nold two" {
			t.Fatalf("failed edit changed page: %+v", page)
		}
	}

	res = callTool(t, session, "edit_page", map[string]any{
		"path": slug, "basehash": created.Hash,
		"edits": []any{
			map[string]any{"oldText": "old one", "newText": "new one"},
			map[string]any{"oldText": "old two", "newText": "new two"},
		},
	})
	if res.IsError {
		t.Fatal(toolText(t, res))
	}
	var edited mcpEditOut
	toolJSON(t, res, &edited)
	if !edited.Changed || edited.Hash == created.Hash || !strings.Contains(edited.Diff, "-old one") || !strings.Contains(edited.Diff, "+new one") {
		t.Fatalf("unexpected edit response: %+v", edited)
	}
	page := read()
	if page.Body != "new one\nnew two" || page.Title != "Edit Note" || !page.Pin || strings.Join(page.Tags, ",") != "ai" || page.Hash != edited.Hash {
		t.Fatalf("incorrect edit: %+v", page)
	}

	res = callTool(t, session, "edit_page", map[string]any{
		"slug": slug, "basehash": edited.Hash, "dryRun": true,
		"edits": []any{map[string]any{"oldText": "new one", "newText": "preview one"}},
	})
	if res.IsError {
		t.Fatal(toolText(t, res))
	}
	var preview mcpEditOut
	toolJSON(t, res, &preview)
	if !preview.Changed || preview.Hash != edited.Hash || !strings.Contains(preview.Diff, "+preview one") {
		t.Fatalf("unexpected dry run response: %+v", preview)
	}
	if page := read(); page.Body != "new one\nnew two" || page.Hash != edited.Hash {
		t.Fatalf("dry run changed page: %+v", page)
	}
}
