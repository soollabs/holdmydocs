package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// seedJournalTemplate configures the journal namespace with a `new:` block
// and seeds its hidden entry template page (journal/entry, marked hidden).
func seedJournalTemplate(t *testing.T, app *App, slugTemplate, titleTemplate, bodyTemplate string) {
	t.Helper()
	yaml := "new:\n  template: entry\n  slug: '" + slugTemplate + "'\n"
	if err := writeNamespaceConfig(t, app, "journal", yaml); err != nil {
		t.Fatalf("writing namespace config: %v", err)
	}
	authorName, authorEmail := app.gitAuthor("admin")
	tpl := Page{Slug: "journal/entry", Title: titleTemplate, Body: bodyTemplate}
	if _, err := app.Store.Save(hiddenFile("journal/entry"), tpl.Encode(), "seed template", authorName, authorEmail); err != nil {
		t.Fatalf("seeding template page: %v", err)
	}
}

func TestNewPageSlugRendersFromNow(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	seedJournalTemplate(t, app, `{{.Now.Format "2006-01-02"}}`, `{{.Now.Format "2006-01-02"}}`, "Dear diary.")

	noRedirectClient := &http.Client{
		Jar:           client.Jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := noRedirectClient.Post(server.URL+"/_/new?ns=journal", "", nil)
	if err != nil {
		t.Fatalf("POST /_/new: %v", err)
	}
	defer closeTestBody(t, resp.Body)

	today := time.Now().Format("2006-01-02")
	wantLoc := "/journal/" + today + "?do=edit"
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != wantLoc {
		t.Errorf("Location = %q, want %q", loc, wantLoc)
	}

	viewResp, err := client.Get(server.URL + "/journal/" + today)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer closeTestBody(t, viewResp.Body)
	body, _ := io.ReadAll(viewResp.Body)
	if !strings.Contains(string(body), "Dear diary.") {
		t.Errorf("new entry body missing template content: %s", body)
	}
}

func TestNewPageNeverOverwritesExisting(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	seedJournalTemplate(t, app, `{{.Now.Format "2006-01-02"}}`, "Title", "Fresh from template.")

	today := time.Now().Format("2006-01-02")
	authorName, authorEmail := app.gitAuthor("admin")
	existing := Page{Slug: "journal/" + today, Title: "Already here", Body: "Don't touch me."}
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
	resp, err := noRedirectClient.Post(server.URL+"/_/new?ns=journal", "", nil)
	if err != nil {
		t.Fatalf("POST /_/new: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if want := "/journal/" + today + "?do=edit"; resp.Header.Get("Location") != want {
		t.Errorf("Location = %q, want %q", resp.Header.Get("Location"), want)
	}

	viewResp, err := client.Get(server.URL + "/journal/" + today)
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
			seedJournalTemplate(t, app, tt.slugTemplate, "Title", "Body.")

			resp, err := client.Post(server.URL+"/_/new?ns=journal", "", nil)
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

func TestNewPageTemplateStaysHiddenFromListingsAndSearch(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	seedJournalTemplate(t, app, `{{.Now.Format "2006-01-02"}}`, "Title", "Body.")

	if app.Index.Exists("journal/entry") {
		t.Error("template page should not be in the search index")
	}

	resp, err := client.Post(server.URL+"/_/new?ns=journal", "", nil)
	if err != nil {
		t.Fatalf("POST /_/new: %v", err)
	}
	closeTestBody(t, resp.Body)

	today := time.Now().Format("2006-01-02")
	if !app.Index.Exists("journal/" + today) {
		t.Error("newly created entry should be indexed")
	}
	if app.Index.Exists("journal/entry") {
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
// template. ctrl-j must still create today's page rather than failing.
func TestNewPageMissingTemplateFallsBack(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	// Config only: no template page seeded.
	if err := writeNamespaceConfig(t, app, "journal", "new:\n  template: entry\n  slug: '{{.Now.Format \"2006-01-02\"}}'\n"); err != nil {
		t.Fatalf("writing namespace config: %v", err)
	}

	resp, err := client.Post(server.URL+"/_/new?ns=journal", "", nil)
	if err != nil {
		t.Fatalf("POST /_/new: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	slug := "journal/" + time.Now().Format("2006-01-02")
	if want := "/" + slug + "?do=edit"; resp.Header.Get("Location") != want {
		t.Errorf("redirect = %q (status %d), want %q", resp.Header.Get("Location"), resp.StatusCode, want)
	}

	content, _, err := app.Store.Read(pageFile(slug))
	if err != nil {
		t.Fatalf("reading %s: %v", pageFile(slug), err)
	}
	if got := ParsePage(slug, content).Title; got != time.Now().Format("2006-01-02") {
		t.Errorf("title = %q, want the slug it was named after", got)
	}
}

// TestNewPageSubstitutesTitleTagsAndBody covers all three templated fields of
// a template page — a substitution that worked in the title and body but not
// the tags would just be a trap.
func TestNewPageSubstitutesTitleTagsAndBody(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	seedJournalTemplate(t, app, `{{.Now.Format "2006-01-02"}}`,
		`{{.Now.Format "Monday, 2 January 2006"}}`,
		`Written by {{.User}} in {{.Namespace}} at {{.Now.Format "15:04"}}.`)

	// seedJournalTemplate doesn't set tags, so add a templated one.
	tpl := Page{
		Slug:  "journal/entry",
		Title: `{{.Now.Format "Monday, 2 January 2006"}}`,
		Tags:  []string{`{{.Now.Format "2006-01"}}`, "journal"},
		Body:  `Written by {{.User}} in {{.Namespace}} at {{.Now.Format "15:04"}}.`,
	}
	authorName, authorEmail := app.gitAuthor("admin")
	if _, err := app.Store.Save(hiddenFile("journal/entry"), tpl.Encode(), "seed template", authorName, authorEmail); err != nil {
		t.Fatalf("seeding template page: %v", err)
	}

	resp, err := client.Post(server.URL+"/_/new?ns=journal", "", nil)
	if err != nil {
		t.Fatalf("POST /_/new: %v", err)
	}
	closeTestBody(t, resp.Body)

	now := time.Now()
	slug := "journal/" + now.Format("2006-01-02")
	content, _, err := app.Store.Read(pageFile(slug))
	if err != nil {
		t.Fatalf("reading %s: %v", pageFile(slug), err)
	}
	page := ParsePage(slug, content)

	if want := now.Format("Monday, 2 January 2006"); page.Title != want {
		t.Errorf("title = %q, want %q", page.Title, want)
	}
	if want := now.Format("2006-01"); len(page.Tags) != 2 || page.Tags[0] != want || page.Tags[1] != "journal" {
		t.Errorf("tags = %v, want [%q journal]", page.Tags, want)
	}
	if want := "Written by admin in journal at " + now.Format("15:04") + "."; !strings.Contains(page.Body, want) {
		t.Errorf("body = %q, want it to contain %q", page.Body, want)
	}
}
