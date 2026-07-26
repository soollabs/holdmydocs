package main

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"testing"
)

// TestDailyPageRoundTrip exercises the daily/YYYY-MM-DD routes end to end:
// create via edit+save, then view, since "daily/YYYY-MM-DD" is the only
// slug in the app containing a "/" and needs its own routes (see the
// handleViewDaily/handleEditDaily/handleSaveDaily/handleHistoryDaily comment
// in handlers.go).
func TestDailyPageRoundTrip(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	resp, err := client.Get(server.URL + "/page/daily/2026-07-24/edit")
	if err != nil {
		t.Fatalf("GET edit: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing edit response body: %v", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /page/daily/2026-07-24/edit status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)

	form := url.Values{
		"title":    {"2026-07-24"},
		"body":     {"Today's entry."},
		"basehash": {regexp.MustCompile(`name="basehash" value="([0-9a-f]*)"`).FindStringSubmatch(string(body))[1]},
	}
	saveResp, err := client.PostForm(server.URL+"/page/daily/2026-07-24/save", form)
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	if err := saveResp.Body.Close(); err != nil {
		t.Fatalf("closing save response body: %v", err)
	}
	if saveResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save status = %d, want 303", saveResp.StatusCode)
	}

	viewResp, err := client.Get(server.URL + "/page/daily/2026-07-24")
	if err != nil {
		t.Fatalf("GET view: %v", err)
	}
	defer func() {
		if err := viewResp.Body.Close(); err != nil {
			t.Errorf("closing view response body: %v", err)
		}
	}()
	viewBody, _ := io.ReadAll(viewResp.Body)
	if viewResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /page/daily/2026-07-24 status = %d, want 200, body: %s", viewResp.StatusCode, viewBody)
	}
	if !bytes.Contains(viewBody, []byte("Today&#39;s entry.")) && !bytes.Contains(viewBody, []byte("Today's entry.")) {
		t.Errorf("expected saved body content on view page, got: %s", viewBody)
	}

	histResp, err := client.Get(server.URL + "/page/daily/2026-07-24/history")
	if err != nil {
		t.Fatalf("GET history: %v", err)
	}
	defer func() {
		if err := histResp.Body.Close(); err != nil {
			t.Errorf("closing history response body: %v", err)
		}
	}()
	if histResp.StatusCode != http.StatusOK {
		t.Errorf("GET /page/daily/2026-07-24/history status = %d, want 200", histResp.StatusCode)
	}
}
