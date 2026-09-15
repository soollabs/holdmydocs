package web

import (
	"strings"
	"testing"
)

func TestMCPEditPage(t *testing.T) {
	server, token := newMCPTestApp(t, true)
	session := connectMCP(t, server, token)
	slug := testNS + "/edit-note"
	res := callTool(t, session, "save_page", map[string]any{"slug": slug, "title": "Edit Note", "tags": []string{"ai"}, "pin": true, "body": "old old\nfoo old"})
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
	head := func() string {
		t.Helper()
		res := callTool(t, session, "recent_changes", map[string]any{"limit": 1})
		if res.IsError {
			t.Fatal(toolText(t, res))
		}
		var recent mcpRecentOut
		toolJSON(t, res, &recent)
		return recent.Commits[0].Hash
	}
	originalHead := head()
	for _, args := range []map[string]any{
		{"slug": slug, "script": "s/old/new/g"},
		{"slug": slug, "basehash": "", "script": "s/old/new/g"},
		{"slug": slug, "basehash": "stale", "script": "s/old/new/g"},
		{"slug": slug, "basehash": created.Hash, "script": "s/old/new/g; r /etc/passwd"},
		{"slug": slug, "basehash": created.Hash, "script": "s/old/new/g; s/[invalid/x/"},
		{"slug": "../bad", "basehash": created.Hash, "script": "s/old/new/g"},
		{"slug": testNS + "/missing", "basehash": created.Hash, "script": "s/old/new/g"},
	} {
		res := callTool(t, session, "edit_page", args)
		if !res.IsError {
			t.Fatalf("invalid edit succeeded: %v", args)
		}
		if page := read(); page.Hash != created.Hash || page.Body != "old old\nfoo old" {
			t.Fatalf("failed edit changed page: %+v", page)
		}
	}
	if head() != originalHead {
		t.Fatal("failed edits created commits")
	}

	res = callTool(t, session, "edit_page", map[string]any{"slug": slug, "basehash": created.Hash, "script": "s/old/new/g; s/foo/new/g"})
	if res.IsError {
		t.Fatal(toolText(t, res))
	}
	var edited mcpEditOut
	toolJSON(t, res, &edited)
	if !edited.Changed || edited.Hash == created.Hash || edited.Hash == "" || strings.Contains(toolText(t, res), "body") {
		t.Fatalf("unexpected edit response: %s", toolText(t, res))
	}
	page := read()
	if page.Body != "new new\nnew new" || page.Title != "Edit Note" || !page.Pin || strings.Join(page.Tags, ",") != "ai" || page.Hash != edited.Hash {
		t.Fatalf("incorrect edit: %+v", page)
	}
	res = callTool(t, session, "search", map[string]any{"query": "new"})
	if !strings.Contains(toolText(t, res), "edit-note") {
		t.Fatal("edit not indexed")
	}
	res = callTool(t, session, "recent_changes", map[string]any{"limit": 2})
	var recent mcpRecentOut
	toolJSON(t, res, &recent)
	if len(recent.Commits) != 2 || recent.Commits[0].Message != "Edit Edit Note" || recent.Commits[1].Hash != originalHead {
		t.Fatalf("edit did not create exactly one commit: %+v", recent)
	}
	editedHead := head()

	res = callTool(t, session, "edit_page", map[string]any{"slug": slug, "basehash": created.Hash, "script": "s/new/lost/g"})
	if !res.IsError || strings.Contains(toolText(t, res), "body") {
		t.Fatalf("expected compact conflict: %s", toolText(t, res))
	}
	for _, script := range []string{"s/absent/present/g", "s/new/new/g", "s/new/old/g; s/old/new/g"} {
		res = callTool(t, session, "edit_page", map[string]any{"slug": slug, "basehash": edited.Hash, "script": script})
		if res.IsError {
			t.Fatal(toolText(t, res))
		}
		var noop mcpEditOut
		toolJSON(t, res, &noop)
		if noop.Changed || noop.Hash != edited.Hash {
			t.Fatalf("no-op response: %+v", noop)
		}
	}
	if head() != editedHead {
		t.Fatal("no-op or stale edit created a commit")
	}
}

func TestApplyPageEdits(t *testing.T) {
	tests := []struct{ name, body, script, want string }{
		{"first per line", "old old\nold old", "s/old/new/", "new old\nnew old"},
		{"global", "old old\nold old\n", "s/old/new/g", "new new\nnew new\n"},
		{"sequential", "old foo", "s/old/foo/g; s/foo/bar/g", "bar bar"},
		{"newline commands", "old foo", "s/old/new/\ns/foo/bar/", "new bar"},
		{"numeric address", "old\nold\nold", "2s/old/new/", "old\nnew\nold"},
		{"last address", "old\nold\nold", "$s/old/new/", "old\nold\nnew"},
		{"numeric range", "old\nold\nold", "1,2s/old/new/", "new\nnew\nold"},
		{"range to last", "old\nold\nold", "2,$s/old/new/", "old\nnew\nnew"},
		{"regexp address", "yes old\nno old", "/^yes/s/old/new/", "yes new\nno old"},
		{"addresses see earlier changes", "old\nno", "s/old/foo/; /foo/s/foo/new/", "new\nno"},
		{"regexp ranges", "start old\nold\nend old\nold\nstart old\nend old", "/^start/,/^end/s/old/new/g", "start new\nnew\nend new\nold\nstart new\nend new"},
		{"regexp end skips first", "x\na\nx\na", "/x/,/x/s/a/b/", "x\nb\nx\na"},
		{"end before start", "a\na\na\na", "3,1s/a/b/", "a\na\nb\na"},
		{"alternate delimiter", "https://old/x", "s|https://old/|https://new/|", "https://new/x"},
		{"escaped delimiter", "a/b", `s/a\/b/c\/d/`, "c/d"},
		{"escaped regexp punctuation delimiter", "a.b", `s.a\.b.c.`, "c"},
		{"replacement", "abc 123", `s/([a-z]+) ([0-9]+)/\2 \1 & \& $1/`, "123 abc abc 123 & $1"},
		{"replacement escapes", "a", `s/a/\\\n\t/`, "\\\n\t"},
		{"escaped ampersand delimiter", "a", `s&a&\&&`, "&"},
		{"unmatched group", "b", `s/(a)?b/\1x/`, "x"},
		{"unicode", "你好 你好", "s/你好/世界/g", "世界 世界"},
		{"literal semicolon", "a;b", "s/a;b/c;d/", "c;d"},
		{"no match", "hello\n", "s/no/yes/", "hello\n"},
		{"empty input", "", "s/a/b/", ""},
		{"empty matching", "a\nb", "s/^/x/", "xa\nxb"},
		{"delete", "a\nb\nc\n", "2d", "a\nc\n"},
		{"delete skips commands", "a\nb", "1d; s/b/c/", "c"},
		{"insert", "a\nb", "2i before", "a\nbefore\nb"},
		{"append deferred", "a\nb", "1a after\ns/a/c/", "c\nafter\nb"},
		{"append final", "a", "$a end", "a\nend\n"},
		{"change", "a\nb\nc", "2c changed", "a\nchanged\nc"},
		{"range change", "a\nb\nc", "1,2c changed", "changed\nc"},
		{"unterminated range change", "a\nb\nc", "/a/,/missing/c changed", "changed\n"},
		{"multiline insertion", "a", "i\\\none\\\ntwo", "one\ntwo\na"},
		{"text semicolon", "a", "i literal; text", "literal; text\na"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyPageEdits(tt.body, tt.script)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPageEditRejectsUnsupportedSyntax(t *testing.T) {
	for _, script := range []string{
		"", " ; \n", "s/a/b", "s/a/b/i", "s/a/b/2", "s/a/b/gw file", "s//b/", "s/[a/b/", "s/a/\\1/", "s/a/\\q/",
		"0d", "999999999999999999999999d", "1,", "1", "1,sd", "e id", "r /etc/passwd", "w file", ":loop; b loop", "/a/!d", "{s/a/b/}", "s a b ", "sXaXbX", "s/a/b/; unsupported", string([]byte{0xff}),
	} {
		t.Run(script, func(t *testing.T) {
			if _, err := applyPageEdits("a", script); err == nil {
				t.Fatal("invalid script accepted")
			}
		})
	}
}

func TestPageEditLimits(t *testing.T) {
	scripts := []string{
		strings.Repeat("s/a/b/;", maxEditCommands+1),
		"s/a/" + strings.Repeat("b", maxPageBodyBytes) + "/",
		"s/a/" + strings.Repeat("b", 1024) + "/g",
		"s/a/" + strings.Repeat("b", 1024) + "/g; s/b/cccc/g",
		"a " + strings.Repeat("b", 1024),
	}
	for _, script := range scripts {
		if _, err := applyPageEdits(strings.Repeat("a\n", 1024), script); err == nil {
			t.Fatal("oversized edit accepted")
		}
	}
}

func FuzzApplyPageEdits(f *testing.F) {
	for _, s := range []string{"s/old/new/g", "1,2d", "/a/s/(a)/\\1/", "i text", "s&a&\\&&", "s/a/b/; s/b/c/"} {
		f.Add("old a\n", s)
	}
	f.Fuzz(func(t *testing.T, body, script string) {
		if len(body) > 4096 || len(script) > 4096 {
			t.Skip()
		}
		out, err := applyPageEdits(body, script)
		if err == nil && len(out) > maxPageBodyBytes {
			t.Fatal("output exceeds limit")
		}
	})
}
