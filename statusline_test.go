package main

import (
	"io"
	"net/url"
	"strings"
	"testing"
)

// TestStatuslineSegmentsPerSkin checks the statusline shows/hides segments
// per the skin's Status variant (skins.go): phosphor gets the full set
// (+new, context, user); journal drops those for a words-today segment and
// a dot-only sync; bare strips down to mode, title and a dot-only sync.
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
	if !strings.Contains(docs, `id="new-btn">+ new<`) {
		t.Error("phosphor skin should show the +new button")
	}
	if !strings.Contains(docs, `class="seg seg-right seg-user"`) {
		t.Error("phosphor skin should show the user segment")
	}

	setSkin("journal")
	journal := get()
	if strings.Contains(journal, `id="new-btn">+ new<`) {
		t.Error("journal skin should not show the docs +new button")
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
	if strings.Contains(minimal, `id="new-btn"`) {
		t.Error("bare skin should not show any +new button")
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
