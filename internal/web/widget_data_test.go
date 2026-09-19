package web

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"hmd/internal/wiki"
)

// TestWidgetFrontmatterPreservedAcrossSave verifies that pin survives an editor save.
func TestWidgetFrontmatterPreservedAcrossSave(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	authorName, authorEmail := app.gitAuthor("admin")
	page := wiki.Page{Slug: testNS + "/clip", Title: "Clip", Body: "Original body.", Pin: true}
	if _, err := app.Store.Save(wiki.PageFile(page.Slug), page.Encode(), "seed", authorName, authorEmail); err != nil {
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
	resp, err := postPageSave(t, client, server, testNS+"/clip", form)
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d, want 200", resp.StatusCode)
	}

	content, _, err := app.Store.Read(wiki.PageFile(page.Slug))
	if err != nil {
		t.Fatalf("reading saved page: %v", err)
	}
	saved := wiki.ParsePage(page.Slug, content)
	if !saved.Pin {
		t.Errorf("pin not preserved across editor save: %+v", saved)
	}
	if !strings.Contains(saved.Body, "Edited body.") {
		t.Errorf("body edit didn't take effect: %q", saved.Body)
	}
}
