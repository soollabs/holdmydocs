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
	if page := getPage(t, client, server.URL+"/_/settings/wiki"); !strings.Contains(page, `action="/_/api/settings/attachments/migrate"`) {
		t.Fatal("settings lacks the move action")
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
