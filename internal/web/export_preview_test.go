package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func previewResponse(t *testing.T, client *http.Client, endpoint string) (*http.Response, string) {
	t.Helper()
	response, err := client.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	closeTestBody(t, response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, string(body)
}

func TestExportPreviewSnapshotSecurityAndLifecycle(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	t.Cleanup(app.CloseExportPreviews)
	adminLogin(t, server, client)
	client.CheckRedirect = nil
	reg := app.Namespaces()
	cfg := reg[testNS]
	cfg.Export.BaseURL = "https://docs.example.org/"
	reg[testNS] = cfg
	app.SetNamespaces(reg)
	endpoint := server.URL + "/_/settings/namespaces/" + testNS + "/preview"
	response, body := previewResponse(t, client, endpoint)
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Request.URL.Path, "/_/export-preview/") {
		t.Fatalf("preview status/path: %d %s %s", response.StatusCode, response.Request.URL.Path, body)
	}
	previewURL := strings.TrimSuffix(response.Request.URL.String(), "/") + "/"
	id := strings.Split(strings.Trim(response.Request.URL.Path, "/"), "/")[2]
	app.previewMu.Lock()
	snapshot := app.previews[id]
	app.previewMu.Unlock()
	if snapshot == nil {
		t.Fatal("snapshot missing")
	}
	if !strings.Contains(body, `nonce="`+snapshot.nonce+`" src=`) || !strings.Contains(response.Header.Get("Content-Security-Policy"), "script-src 'nonce-"+snapshot.nonce+"'") {
		t.Fatal("export scripts and preview CSP must share a nonce")
	}
	for _, want := range []string{"form-action 'none'", "connect-src 'none'", "sandbox allow-scripts allow-same-origin", "allow-popups allow-popups-to-escape-sandbox"} {
		if !strings.Contains(response.Header.Get("Content-Security-Policy"), want) {
			t.Errorf("CSP missing %s", want)
		}
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Robots-Tag") != "noindex, nofollow, noarchive" {
		t.Fatal("preview must not be cached or indexed")
	}
	if response.Header.Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Fatal("preview resources must not be embedded cross-origin")
	}
	response, body = previewResponse(t, client, previewURL+"style.css")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "body.shell") {
		t.Fatal("CSS unavailable")
	}
	response, _ = previewResponse(t, client, previewURL+"missing/")
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("directory listing/missing page must not be served")
	}
	response, _ = previewResponse(t, client, previewURL+"%2e%2e%5csecret")
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("backslash traversal accepted")
	}
	outside := t.TempDir() + "/outside.txt"
	if err := os.WriteFile(outside, []byte("outside snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, snapshot.dir+"/outside.txt"); err != nil {
		t.Fatal(err)
	}
	response, _ = previewResponse(t, client, previewURL+"outside.txt")
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("symlink escaped preview root")
	}
	// Attachment scripts must not gain permission from the generated HTML.
	if err := os.MkdirAll(snapshot.dir+"/attachments", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshot.dir+"/attachments/untrusted.html", []byte("<script>alert(1)</script>"), 0o600); err != nil {
		t.Fatal(err)
	}
	response, _ = previewResponse(t, client, previewURL+"attachments/untrusted.html")
	if response.Header.Get("Content-Disposition") != "attachment" || strings.Contains(response.Header.Get("Content-Security-Policy"), "allow-scripts") {
		t.Fatal("active attachment served with script permission")
	}
	// Other users cannot use a snapshot URL, even if they have settings access.
	if err := app.Auth.AddUserWithScopes("other", "password12345", []string{"settings"}); err != nil {
		t.Fatal(err)
	}
	login, err := client.PostForm(server.URL+"/_/login", url.Values{"username": {"other"}, "password": {"password12345"}})
	if err != nil {
		t.Fatal(err)
	}
	closeTestBody(t, login.Body)
	response, _ = previewResponse(t, client, previewURL+"style.css")
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("snapshot leaked to another user")
	}
	response, _ = previewResponse(t, client, endpoint)
	if response.StatusCode != http.StatusOK {
		t.Fatal("settings-only user cannot generate and view preview")
	}
	otherURL := strings.TrimSuffix(response.Request.URL.String(), "/") + "/"
	if err := app.Auth.SetScopes("other", []string{"read"}); err != nil {
		t.Fatal(err)
	}
	response, _ = previewResponse(t, client, otherURL+"style.css")
	if response.StatusCode != http.StatusForbidden && response.Request.URL.Path != "/_/login" {
		t.Fatal("revoked settings access still serves assets")
	}
	adminLogin(t, server, client)
	client.CheckRedirect = nil
	response, _ = previewResponse(t, client, endpoint)
	if response.StatusCode != http.StatusOK {
		t.Fatal("regeneration failed")
	}
	app.previewMu.Lock()
	_, oldExists := app.previews[id]
	app.previewMu.Unlock()
	if oldExists {
		t.Fatal("old same-user namespace snapshot retained")
	}
	if _, err := os.Stat(snapshot.dir); !os.IsNotExist(err) {
		t.Fatal("superseded snapshot directory not removed")
	}
	newID := strings.Split(strings.Trim(response.Request.URL.Path, "/"), "/")[2]
	app.previewMu.Lock()
	current := app.previews[newID]
	current.mu.Lock()
	current.expires = time.Now().Add(-time.Minute)
	current.mu.Unlock()
	app.previewMu.Unlock()
	response, _ = previewResponse(t, client, response.Request.URL.String())
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("expired snapshot served")
	}
	app.expireExportPreview(newID)
	if _, err := os.Stat(current.dir); !os.IsNotExist(err) {
		t.Fatal("expiry did not remove directory")
	}
	app.CloseExportPreviews()
	app.previewMu.Lock()
	count := len(app.previews)
	app.previewMu.Unlock()
	if count != 0 {
		t.Fatal("shutdown did not clear previews")
	}
}

func TestExportPreviewMissingURLAndAnonymousAccess(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	t.Cleanup(app.CloseExportPreviews)
	adminLogin(t, server, client)
	client.CheckRedirect = nil
	config := app.config()
	config.BaseURL = "https://live.example.org"
	app.SetConfig(config)
	// Direct test requests use the test origin; GET is sufficient here.
	response, body := previewResponse(t, client, server.URL+"/_/settings/namespaces/"+testNS+"/preview")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "Use main base URL and preview") {
		t.Fatalf("missing confirmation: %s", body)
	}
	response, _ = previewResponse(t, client, server.URL+"/_/settings/namespaces/"+testNS+"/preview?use-main-base-url=1")
	if !strings.Contains(response.Request.URL.Path, "/_/export-preview/") {
		t.Fatal("fallback did not generate preview")
	}
	if app.Namespaces()[testNS].Export.BaseURL != "" {
		t.Fatal("fallback changed saved settings")
	}
	previewURL := response.Request.URL.String()
	reg := app.Namespaces()
	delete(reg, testNS)
	app.SetNamespaces(reg)
	response, _ = previewResponse(t, client, previewURL+"style.css")
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("deleted namespace still serves preview")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	anonymous := &http.Client{Jar: jar}
	response, body = previewResponse(t, anonymous, previewURL+"style.css")
	if response.Request.URL.Path != "/_/login" || strings.Contains(body, "body.shell {") {
		t.Fatal("unauthenticated asset request leaked content")
	}
}

func TestExportPreviewCapacity(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	t.Cleanup(app.CloseExportPreviews)
	adminLogin(t, server, client)
	reg := app.Namespaces()
	cfg := reg[testNS]
	cfg.Export.BaseURL = "https://docs.example.org/"
	reg[testNS] = cfg
	app.SetNamespaces(reg)
	app.previewMu.Lock()
	app.previews = make(map[string]*exportPreview)
	for i := 0; i < maxExportPreviews; i++ {
		id := strings.Repeat("x", i+1)
		app.previews[id] = &exportPreview{dir: t.TempDir(), owner: "someone-else", namespace: testNS, expires: time.Now().Add(time.Minute)}
	}
	app.previewMu.Unlock()
	response, _ := previewResponse(t, client, server.URL+"/_/settings/namespaces/"+testNS+"/preview")
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatal("preview capacity not enforced")
	}
}
