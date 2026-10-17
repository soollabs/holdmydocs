package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestLegacyAttachmentMigration checks the old-layout banner, the settings
// action that moves the files, and that the attachment URL keeps working.
func TestLegacyAttachmentMigration(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	legacy := "attachments/" + testHome + "/chart.png"
	if _, err := app.Store.Save(legacy, []byte("png"), "seed", "test", "test@hmd.local"); err != nil {
		t.Fatal(err)
	}
	if page := getPage(t, client, server.URL+"/"+testHome); !strings.Contains(page, "Attachments need moving.") {
		t.Fatal("banner missing while legacy attachments exist")
	}
	page := getPage(t, client, server.URL+"/_/settings/wiki")
	if !strings.Contains(page, `action="/_/api/settings/attachments/migrate"`) {
		t.Fatal("settings lacks the move action")
	}
	if !strings.Contains(page, "<td><code>"+testNS+"/</code></td>") || !strings.Contains(page, "moves into the namespace") {
		t.Error("settings does not list the namespace whose attachments move")
	}

	resp, err := postJSON(t, client, server.URL+"/_/api/settings/attachments/migrate", emptyJSON(url.Values{}))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("migrate status = %d", resp.StatusCode)
	}

	if attachment, ok := app.Store.FindAttachment(testHome, "chart.png"); !ok || attachment.Path == legacy {
		t.Fatalf("attachment not moved: %+v, %v", attachment, ok)
	}
	if page := getPage(t, client, server.URL+"/"+testHome); strings.Contains(page, "Attachments need moving.") {
		t.Error("banner still shown after migration")
	}
	if body := getPage(t, client, server.URL+"/_/attachments/"+testHome+"/chart.png"); body != "png" {
		t.Errorf("attachment URL after migration = %q", body)
	}
}

// TestOrphanedAttachmentsListedAndDeleted checks that old attachments of a
// namespace that no longer exists are listed in settings and can be deleted.
func TestOrphanedAttachmentsListedAndDeleted(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	if _, err := app.Store.Save("attachments/gone/page/old.png", []byte("png"), "seed", "test", "test@hmd.local"); err != nil {
		t.Fatal(err)
	}
	page := getPage(t, client, server.URL+"/_/settings/wiki")
	if !strings.Contains(page, `name="namespace" value="gone"`) || strings.Contains(page, `action="/_/api/settings/attachments/migrate"`) {
		t.Fatal("settings should list the gone namespace without a move action")
	}
	if strings.Contains(page, "Attachments need moving.") {
		t.Error("banner shown when nothing can be moved")
	}

	resp, err := postJSON(t, client, server.URL+"/_/api/settings/attachments/delete", map[string]string{"namespace": "gone"})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	if _, ok := app.Store.FindAttachment("gone/page", "old.png"); ok {
		t.Error("orphaned attachment still present")
	}
	if page := getPage(t, client, server.URL+"/_/settings/wiki"); strings.Contains(page, `id="attachments"`) {
		t.Error("attachments section still shown with nothing left")
	}
}
