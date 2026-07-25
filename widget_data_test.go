package main

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// TestWidgetFrontmatterPreservedAcrossSave asserts that pin/unread/source/
// author/read_time — which have no dedicated editor UI — survive a normal
// web-editor save untouched, per the spec's "must round-trip through the
// editor untouched" requirement.
func TestWidgetFrontmatterPreservedAcrossSave(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	authorName, authorEmail := app.gitAuthor("admin")
	page := Page{Slug: "clip", Title: "Clip", Body: "Original body.", Unread: true, Source: "https://example.com/x", Author: "Jane", ReadTime: "3 min"}
	if _, err := app.Store.Save("clip.md", page.Encode(), "seed", authorName, authorEmail); err != nil {
		t.Fatalf("seeding page: %v", err)
	}
	app.Index.Update(page)

	getEdit := func() string {
		resp, err := client.Get(server.URL + "/page/clip/edit")
		if err != nil {
			t.Fatalf("GET edit: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	basehashRe := regexp.MustCompile(`name="basehash" value="([0-9a-f]*)"`)
	form := url.Values{
		"title":    {"Clip"},
		"body":     {"Edited body."},
		"basehash": {basehashRe.FindStringSubmatch(getEdit())[1]},
	}
	resp, err := client.PostForm(server.URL+"/page/clip/save", form)
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save status = %d, want 303", resp.StatusCode)
	}

	content, _, err := app.Store.Read("clip.md")
	if err != nil {
		t.Fatalf("reading saved page: %v", err)
	}
	saved := ParsePage("clip", content)
	if !saved.Unread || saved.Source != "https://example.com/x" || saved.Author != "Jane" || saved.ReadTime != "3 min" {
		t.Errorf("widget frontmatter not preserved across editor save: %+v", saved)
	}
	if !strings.Contains(saved.Body, "Edited body.") {
		t.Errorf("body edit didn't take effect: %q", saved.Body)
	}
}
