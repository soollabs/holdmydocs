package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hmd/internal/api"
	"hmd/internal/auth"
	"hmd/internal/testhttp"
)

func TestReadOnlyBrowser(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	cfg := app.Config()
	cfg.ReadOnly = true
	app.SetConfig(cfg)
	before, hash, err := app.Store.Read(testHome + ".md")
	if err != nil {
		t.Fatal(err)
	}
	response, err := testhttp.Get(t, client, server.URL+"/"+testHome)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("reading page: %v, %v", readErr, closeErr)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "read-only") {
		t.Fatalf("administrator browse = %d", response.StatusCode)
	}
	if strings.Contains(string(body), `aria-label="Edit page"`) || strings.Contains(string(body), `id="new-page"`) {
		t.Fatal("read-only page exposes mutation controls")
	}
	response, err = testhttp.Get(t, client, server.URL+"/"+testHome+"?do=edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("edit form = %v, %v", response, err)
	}
	response, err = postPageSave(t, client, server, testHome, url.Values{
		"title": {"changed"}, "body": {"changed"}, "basehash": {hash},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("save = %v, %v", response, err)
	}
	after, afterHash, err := app.Store.Read(testHome + ".md")
	if err != nil || string(after) != string(before) || afterHash != hash {
		t.Fatal("read-only HTTP request changed the repository")
	}

	// Public documents must remain available without a session or any account.
	emptyAuth, err := auth.Open(auth.Options{AppDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if emptyAuth.HasUsers() {
		t.Fatal("unexpected account in browsing-only fixture")
	}
	app.Auth = emptyAuth
	namespaces := app.Namespaces()
	public := namespaces[testNS]
	public.Public = true
	namespaces[testNS] = public
	app.API = api.New(app.Store, app.Index, emptyAuth)
	app.SetConfig(cfg)
	app.SetNamespaces(namespaces)
	browser := httptest.NewServer(testHandler(app.App))
	defer browser.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, browser.URL+"/"+testHome, nil)
	if err != nil {
		t.Fatal(err)
	}
	anonymous, err := (&http.Client{}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := anonymous.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if anonymous.StatusCode != http.StatusOK {
		t.Fatalf("anonymous public browse = %v, %v", anonymous, err)
	}
	public.Public = false
	namespaces[testNS] = public
	app.SetNamespaces(namespaces)
	anonymous, err = (&http.Client{}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := anonymous.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if anonymous.StatusCode != http.StatusNotFound {
		t.Fatalf("anonymous private browse = %v, %v", anonymous, err)
	}
}

// TestReadOnlySettingsViewOnly pins what an administrator sees in settings on
// a read-only instance: settings stay in the menu and sidebar, a banner
// explains why nothing can change, and every control is disabled with no save
// row. Read-only mode refuses account saves too, so account pages are no
// different from system ones.
func TestReadOnlySettingsViewOnly(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	get := func(path string) string {
		t.Helper()
		response, err := testhttp.Get(t, client, server.URL+path)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("reading %s: %v, %v", path, readErr, closeErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, response.StatusCode)
		}
		return string(body)
	}

	if body := get("/_/settings/git"); strings.Contains(body, `data-dismiss="read-only"`) || strings.Contains(body, "settings-view-only") {
		t.Error("writable instance shows the read-only banner or disables settings")
	}

	cfg := app.Config()
	cfg.ReadOnly = true
	app.SetConfig(cfg)
	for _, path := range []string{"/_/settings/appearance", "/_/settings/git", "/_/settings/users", "/_/settings/namespaces"} {
		body := get(path)
		if !strings.Contains(body, `data-dismiss="read-only"`) || !strings.Contains(body, "Read-only mode.") || !strings.Contains(body, "ui-notice__dismiss") {
			t.Errorf("%s lacks the dismissible read-only banner", path)
		}
		if !strings.Contains(body, `<fieldset class="settings-view-only" disabled>`) {
			t.Errorf("%s controls are not disabled", path)
		}
		if !strings.Contains(body, `<span class="settings-nav-group">system</span>`) {
			t.Errorf("%s sidebar hides the system settings", path)
		}
		if !strings.Contains(body, `<a href="/_/settings">Settings</a>`) || !strings.Contains(body, `<a href="/_/settings/server">Admin</a>`) {
			t.Errorf("%s menu hides Settings or Admin", path)
		}
	}
}
