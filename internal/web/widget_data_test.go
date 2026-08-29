package web

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// TestWidgetFrontmatterPreservedAcrossSave verifies that pin survives an editor save.
func TestWidgetFrontmatterPreservedAcrossSave(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	authorName, authorEmail := app.gitAuthor("admin")
	page := Page{Slug: testNS + "/clip", Title: "Clip", Body: "Original body.", Pin: true}
	if _, err := app.Store.Save(pageFile(page.Slug), page.Encode(), "seed", authorName, authorEmail); err != nil {
		t.Fatalf("seeding page: %v", err)
	}
	if err := app.Index.Update(page); err != nil {
		t.Fatalf("updating index: %v", err)
	}

	getEdit := func() string {
		resp, err := client.Get(server.URL + "/" + testNS + "/clip?do=edit")
		if err != nil {
			t.Fatalf("GET edit: %v", err)
		}
		defer closeTestBody(t, resp.Body)
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	basehashRe := regexp.MustCompile(`name="basehash" value="([0-9a-f]*)"`)
	form := url.Values{
		"title":    {"Clip"},
		"body":     {"Edited body."},
		"basehash": {basehashRe.FindStringSubmatch(getEdit())[1]},
	}
	resp, err := client.PostForm(server.URL+"/"+testNS+"/clip?do=save", form)
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save status = %d, want 303", resp.StatusCode)
	}

	content, _, err := app.Store.Read(pageFile(page.Slug))
	if err != nil {
		t.Fatalf("reading saved page: %v", err)
	}
	saved := ParsePage(page.Slug, content)
	if !saved.Pin {
		t.Errorf("pin not preserved across editor save: %+v", saved)
	}
	if !strings.Contains(saved.Body, "Edited body.") {
		t.Errorf("body edit didn't take effect: %q", saved.Body)
	}
}
