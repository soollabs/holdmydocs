package main

import (
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// seedNewPageTemplate configures the notes namespace with a `new:` block
// and seeds its hidden entry template page.
func seedNewPageTemplate(t *testing.T, app *App, slugTemplate, titleTemplate, bodyTemplate string) {
	t.Helper()
	yaml := "new:\n  template: entry\n  slug: '" + slugTemplate + "'\n"
	if err := writeNamespaceConfig(t, app, testNS, yaml); err != nil {
		t.Fatalf("writing namespace config: %v", err)
	}
	authorName, authorEmail := app.gitAuthor("admin")
	tpl := Page{Slug: testNS + "/entry", Title: titleTemplate, Body: bodyTemplate}
	if _, err := app.Store.Save(hiddenFile(testNS+"/entry"), tpl.Encode(), "seed template", authorName, authorEmail); err != nil {
		t.Fatalf("seeding template page: %v", err)
	}
}

var (
	draftSlugRe  = regexp.MustCompile(`id="cm-host" data-slug="([^"]*)"`)
	draftTitleRe = regexp.MustCompile(`id="title"[^>]*value="([^"]*)"`)
	draftTagsRe  = regexp.MustCompile(`id="tags"[^>]*value="([^"]*)"`)
	draftBodyRe  = regexp.MustCompile(`(?s)id="editor-src"[^>]*>(.*?)</textarea>`)
)

func draftField(re *regexp.Regexp, body string) string {
	m := re.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1])
}

func TestNewPageSlugRendersFromNow(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	seedNewPageTemplate(t, app, `{{.Now.Format "2006-01-02"}}`, `{{.Now.Format "2006-01-02"}}`, "Template content.")

	resp, err := client.Post(server.URL+"/_/new?ns="+testNS, "", nil)
	if err != nil {
		t.Fatalf("POST /_/new: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (draft rendered inline, no redirect)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)

	today := time.Now().Format("2006-01-02")
	if slug := draftField(draftSlugRe, string(body)); slug != testNS+"/"+today {
		t.Errorf("slug = %q, want %q", slug, testNS+"/"+today)
	}
	if !strings.Contains(string(body), "Template content.") {
		t.Errorf("draft edit form missing template content: %s", body)
	}

	// Nothing is persisted until Save: the draft is rendered directly in
	// this response, never written to the store.
	if _, _, err := app.Store.Read(pageFile(testNS + "/" + today)); err == nil {
		t.Error("page should not be created until Save, but it was persisted by /_/new")
	}
}

func TestNewPageNeverOverwritesExisting(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	seedNewPageTemplate(t, app, `{{.Now.Format "2006-01-02"}}`, "Title", "Fresh from template.")

	today := time.Now().Format("2006-01-02")
	authorName, authorEmail := app.gitAuthor("admin")
	existing := Page{Slug: testNS + "/" + today, Title: "Already here", Body: "Don't touch me."}
	if _, err := app.Store.Save(pageFile(existing.Slug), existing.Encode(), "seed existing", authorName, authorEmail); err != nil {
		t.Fatalf("seeding existing page: %v", err)
	}
	if err := app.Index.Update(existing); err != nil {
		t.Fatalf("indexing: %v", err)
	}

	noRedirectClient := &http.Client{
		Jar:           client.Jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := noRedirectClient.Post(server.URL+"/_/new?ns="+testNS, "", nil)
	if err != nil {
		t.Fatalf("POST /_/new: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if want := "/" + testNS + "/" + today + "?do=edit"; resp.Header.Get("Location") != want {
		t.Errorf("Location = %q, want %q", resp.Header.Get("Location"), want)
	}

	viewResp, err := client.Get(server.URL + "/" + testNS + "/" + today)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, viewResp.Body)
	body, _ := io.ReadAll(viewResp.Body)
	if !strings.Contains(string(body), "Don&#39;t touch me.") && !strings.Contains(string(body), "Don't touch me.") {
		t.Errorf("existing page content was overwritten: %s", body)
	}
	if strings.Contains(string(body), "Fresh from template.") {
		t.Error("existing page should not be overwritten by the template")
	}
}

func TestNewPageRejectsUnsafeRenderedSlugs(t *testing.T) {
	tests := []struct {
		name, slugTemplate string
	}{
		{"contains slash", "sub/dir"},
		{"dot-dot", "../escape"},
		{"leading dot", ".hidden"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, server, client := newTestAppFull(t)
			defer server.Close()
			seedNewPageTemplate(t, app, tt.slugTemplate, "Title", "Body.")

			resp, err := client.Post(server.URL+"/_/new?ns="+testNS, "", nil)
			if err != nil {
				t.Fatalf("POST /_/new: %v", err)
			}
			defer closeTestBody(t, resp.Body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for slug template %q", resp.StatusCode, tt.slugTemplate)
			}
		})
	}
}

func TestNewPageRejectsUnsafeTemplatePage(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	seedNewPageTemplate(t, app, "page", "Title", "Body.")
	app.Namespaces()[testNS].New.Template = "../private"

	resp, err := client.Post(server.URL+"/_/new?ns="+testNS, "", nil)
	if err != nil {
		t.Fatalf("POST /_/new: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestNewPageTemplateStaysHiddenFromListingsAndSearch(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	seedNewPageTemplate(t, app, `{{.Now.Format "2006-01-02"}}`, "Title", "Body.")

	if app.Index.Exists(testNS + "/entry") {
		t.Error("template page should not be in the search index")
	}

	resp, err := client.Post(server.URL+"/_/new?ns="+testNS, "", nil)
	if err != nil {
		t.Fatalf("POST /_/new: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)

	today := time.Now().Format("2006-01-02")
	if app.Index.Exists(testNS + "/" + today) {
		t.Error("draft entry should not be indexed until it is saved")
	}

	saveResp, err := client.PostForm(server.URL+"/"+testNS+"/"+today+"?do=save", url.Values{
		"title": {draftField(draftTitleRe, string(body))}, "body": {"Body."}, "basehash": {""},
	})
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	closeTestBody(t, saveResp.Body)

	if !app.Index.Exists(testNS + "/" + today) {
		t.Error("entry should be indexed once actually saved")
	}
	if app.Index.Exists(testNS + "/entry") {
		t.Error("template page should still not be in the search index after use")
	}
}

func TestNewPageUnknownNamespace404s(t *testing.T) {
	_, server, client := newTestAppFull(t)
	defer server.Close()

	resp, err := client.Post(server.URL+"/_/new?ns=nope", "", nil)
	if err != nil {
		t.Fatalf("POST /_/new: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a namespace with no new: template", resp.StatusCode)
	}
}

// TestNewPageMissingTemplateFallsBack covers a namespace whose declared
// new.template page doesn't exist — hand-written config, or a deleted
// template. Quick-create must still create the page rather than failing.
func TestNewPageMissingTemplateFallsBack(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	// Config only: no template page seeded.
	if err := writeNamespaceConfig(t, app, testNS, "new:\n  template: entry\n  slug: '{{.Now.Format \"2006-01-02\"}}'\n"); err != nil {
		t.Fatalf("writing namespace config: %v", err)
	}

	resp, err := client.Post(server.URL+"/_/new?ns="+testNS, "", nil)
	if err != nil {
		t.Fatalf("POST /_/new: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (draft rendered inline, no redirect)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)

	slug := testNS + "/" + time.Now().Format("2006-01-02")
	if got := draftField(draftSlugRe, string(body)); got != slug {
		t.Errorf("slug = %q, want %q", got, slug)
	}
	if got := draftField(draftTitleRe, string(body)); got != time.Now().Format("2006-01-02") {
		t.Errorf("title = %q, want the slug it was named after", got)
	}
}

// TestNewPageSubstitutesTitleTagsAndBody covers all three templated fields of
// a template page — a substitution that worked in the title and body but not
// the tags would just be a trap.
func TestNewPageSubstitutesTitleTagsAndBody(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	seedNewPageTemplate(t, app, `{{.Now.Format "2006-01-02"}}`,
		`{{.Now.Format "Monday, 2 January 2006"}}`,
		`Written by {{.User}} in {{.Namespace}} at {{.Now.Format "15:04"}}.`)

	// seedNewPageTemplate doesn't set tags, so add a templated one.
	tpl := Page{
		Slug:  testNS + "/entry",
		Title: `{{.Now.Format "Monday, 2 January 2006"}}`,
		Tags:  []string{`{{.Now.Format "2006-01"}}`, "notes"},
		Body:  `Written by {{.User}} in {{.Namespace}} at {{.Now.Format "15:04"}}.`,
	}
	authorName, authorEmail := app.gitAuthor("admin")
	if _, err := app.Store.Save(hiddenFile(testNS+"/entry"), tpl.Encode(), "seed template", authorName, authorEmail); err != nil {
		t.Fatalf("seeding template page: %v", err)
	}

	resp, err := client.Post(server.URL+"/_/new?ns="+testNS, "", nil)
	if err != nil {
		t.Fatalf("POST /_/new: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	closeTestBody(t, resp.Body)

	now := time.Now()
	tags := ParseTags(draftField(draftTagsRe, string(body)))

	if want := now.Format("Monday, 2 January 2006"); draftField(draftTitleRe, string(body)) != want {
		t.Errorf("title = %q, want %q", draftField(draftTitleRe, string(body)), want)
	}
	if want := now.Format("2006-01"); len(tags) != 2 || tags[0] != want || tags[1] != "notes" {
		t.Errorf("tags = %v, want [%q notes]", tags, want)
	}
	if want := "Written by admin in notes at " + now.Format("15:04") + "."; !strings.Contains(draftField(draftBodyRe, string(body)), want) {
		t.Errorf("body = %q, want it to contain %q", draftField(draftBodyRe, string(body)), want)
	}
}
