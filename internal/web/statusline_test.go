package web

import (
	"io"
	"net/url"
	"strings"
	"testing"
)

// TestStatuslineSegmentsPerSkin checks actions stay in the shared top bar
// while each skin keeps its informational statusline variant.
func TestStatuslineSegmentsPerSkin(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	setSkin := func(name string) {
		resp, err := client.PostForm(server.URL+"/_/settings/appearance", url.Values{"skin": {name}})
		if err != nil {
			t.Fatalf("setting skin %s: %v", name, err)
		}
		closeTestBody(t, resp.Body)
	}
	get := func() string {
		resp, err := client.Get(server.URL + "/" + testHome)
		if err != nil {
			t.Fatalf("GET /%s: %v", testHome, err)
		}
		defer closeTestBody(t, resp.Body)
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	setSkin("phosphor")
	docs := get()
	if !strings.Contains(docs, `id="new-page"`) || !strings.Contains(docs, `title="Edit page"`) {
		t.Error("phosphor skin should show New and Edit in the top bar")
	}
	if !strings.Contains(docs, `class="seg seg-right seg-user"`) {
		t.Error("phosphor skin should show the user segment")
	}
	statusStart, statusEnd := strings.Index(docs, `id="statusline"`), strings.Index(docs, `id="palette-backdrop"`)
	if statusStart == -1 || statusEnd == -1 || strings.Contains(docs[statusStart:statusEnd], "<button") || strings.Contains(docs[statusStart:statusEnd], "<form") {
		t.Error("statusline should be information only")
	}
	sidebarStart, sidebarEnd := strings.Index(docs, `class="sidebar"`), strings.Index(docs, "</nav>")
	if sidebarStart == -1 || sidebarEnd == -1 || strings.Contains(docs[sidebarStart:sidebarEnd], "<button") || strings.Contains(docs[sidebarStart:sidebarEnd], "tree-new") {
		t.Error("sidebar should contain navigation, not app actions")
	}

	setSkin("journal")
	journal := get()
	if !strings.Contains(journal, `id="new-page"`) {
		t.Error("journal skin should show New in the top bar")
	}
	if !strings.Contains(journal, "words today") {
		t.Error("journal skin should show a words-today segment")
	}
	if strings.Contains(journal, `id="sync-text"`) {
		t.Error("journal skin should show a dot-only sync (no sync-text)")
	}

	setSkin("bare")
	minimal := get()
	if strings.Contains(minimal, `class="seg seg-right seg-user"`) {
		t.Error("bare skin should not show the user segment")
	}
	if strings.Contains(minimal, `id="sync-text"`) {
		t.Error("bare skin should show a dot-only sync (no sync-text)")
	}
	if !strings.Contains(minimal, `id="new-page"`) {
		t.Error("bare skin should show New in the top bar")
	}
	for _, body := range []string{docs, journal, minimal} {
		if strings.Contains(body, `id="new-btn"`) || strings.Contains(body, `id="mobile-new"`) {
			t.Error("duplicate New control is rendered")
		}
	}

	editResp, err := client.Get(server.URL + "/" + testHome + "?do=edit")
	if err != nil {
		t.Fatalf("GET edit: %v", err)
	}
	defer closeTestBody(t, editResp.Body)
	editBody, _ := io.ReadAll(editResp.Body)
	if !strings.Contains(string(editBody), `form="edit-form" class="topbar-action topbar-primary">Save</button>`) {
		t.Error("edit mode should replace Edit with Save in the top bar")
	}
	if strings.Contains(string(editBody), `id="save-btn"`) {
		t.Error("editor still renders a duplicate Save button")
	}
}

func TestStatuslineModeLabelPerSkin(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	resp, err := client.PostForm(server.URL+"/_/settings/appearance", url.Values{"skin": {"journal"}})
	if err != nil {
		t.Fatalf("setting skin: %v", err)
	}
	closeTestBody(t, resp.Body)

	authorName, authorEmail := app.gitAuthor("admin")
	if _, err := app.Store.Save("daily/2026-07-25.md", (Page{Slug: "daily/2026-07-25", Title: "2026-07-25", Body: "x"}).Encode(), "seed", authorName, authorEmail); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	editResp, err := client.Get(server.URL + "/daily/2026-07-25?do=edit")
	if err != nil {
		t.Fatalf("GET edit: %v", err)
	}
	defer func() {
		if err := editResp.Body.Close(); err != nil {
			t.Errorf("closing edit response body: %v", err)
		}
	}()
	body, _ := io.ReadAll(editResp.Body)
	if !strings.Contains(string(body), `id="status-mode">write<`) {
		t.Errorf("journal skin in edit mode should show mode label 'write', body snippet not found")
	}
}
