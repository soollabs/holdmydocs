package main

import (
	"io"
	"net/url"
	"strings"
	"testing"
)

// TestStatuslineSegmentsPerProfile checks the statusline shows/hides
// segments per specs/2026-07-25-profiles-widgets.md profile implementation's table: docs
// gets the full set (+new, context, user); journal drops those for a
// words-today segment and a dot-only sync; clipper swaps in +clip url,
// source host and unread count; minimal strips everything down to mode,
// title and a dot-only sync.
func TestStatuslineSegmentsPerProfile(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	setProfile := func(name string) {
		resp, err := client.PostForm(server.URL+"/settings/appearance", url.Values{"profile": {name}})
		if err != nil {
			t.Fatalf("setting profile %s: %v", name, err)
		}
		resp.Body.Close()
	}
	get := func() string {
		resp, err := client.Get(server.URL + "/page/readme")
		if err != nil {
			t.Fatalf("GET /page/readme: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	setProfile("docs")
	docs := get()
	if !strings.Contains(docs, `id="new-btn">+ new<`) {
		t.Error("docs profile should show the +new button")
	}
	if !strings.Contains(docs, `class="seg seg-right seg-user"`) {
		t.Error("docs profile should show the user segment")
	}

	setProfile("journal")
	journal := get()
	if strings.Contains(journal, `id="new-btn">+ new<`) {
		t.Error("journal profile should not show the docs +new button")
	}
	if !strings.Contains(journal, "words today") {
		t.Error("journal profile should show a words-today segment")
	}
	if strings.Contains(journal, `id="sync-text"`) {
		t.Error("journal profile should show a dot-only sync (no sync-text)")
	}

	setProfile("clipper")
	clipper := get()
	if !strings.Contains(clipper, "+ clip url") {
		t.Error("clipper profile should show a +clip url button")
	}
	if !strings.Contains(clipper, "unread") {
		t.Error("clipper profile should show an unread count segment")
	}

	setProfile("minimal")
	minimal := get()
	if strings.Contains(minimal, `class="seg seg-right seg-user"`) {
		t.Error("minimal profile should not show the user segment")
	}
	if strings.Contains(minimal, `id="sync-text"`) {
		t.Error("minimal profile should show a dot-only sync (no sync-text)")
	}
	if strings.Contains(minimal, `id="new-btn"`) {
		t.Error("minimal profile should not show any +new/+clip button")
	}
}

func TestStatuslineModeLabelPerProfile(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	resp, err := client.PostForm(server.URL+"/settings/appearance", url.Values{"profile": {"journal"}})
	if err != nil {
		t.Fatalf("setting profile: %v", err)
	}
	resp.Body.Close()

	authorName, authorEmail := app.gitAuthor("admin")
	if _, err := app.Store.Save("daily/2026-07-25.md", (Page{Slug: "daily/2026-07-25", Title: "2026-07-25", Body: "x"}).Encode(), "seed", authorName, authorEmail); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	editResp, err := client.Get(server.URL + "/page/daily/2026-07-25/edit")
	if err != nil {
		t.Fatalf("GET edit: %v", err)
	}
	defer editResp.Body.Close()
	body, _ := io.ReadAll(editResp.Body)
	if !strings.Contains(string(body), `id="status-mode">write<`) {
		t.Errorf("journal profile in edit mode should show mode label 'write', body snippet not found")
	}
}
