package app

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHasScopeDefaultsToFullAccess(t *testing.T) {
	u := userRecord{}
	for _, s := range []scope{scopeRead, scopeWrite, scopeSettings} {
		if !u.hasScope(s) {
			t.Errorf("user with no Scopes set should have %s access by default", s)
		}
	}
}

func TestHasScopeRestricts(t *testing.T) {
	u := userRecord{Scopes: []string{"read"}}
	if !u.hasScope(scopeRead) {
		t.Error("expected read access")
	}
	if u.hasScope(scopeWrite) {
		t.Error("expected no write access")
	}
	if u.hasScope(scopeSettings) {
		t.Error("expected no settings access")
	}
}

func TestSetScopesRejectsUnknown(t *testing.T) {
	auth, err := OpenAuth(Config{AppDir: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenAuth: %v", err)
	}
	if err := auth.AddUser("bob", "password12345"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := auth.SetScopes("bob", []string{"bogus"}); err == nil {
		t.Error("expected error for unknown scope")
	}
	if err := auth.SetScopes("bob", []string{"read"}); err != nil {
		t.Fatalf("SetScopes: %v", err)
	}
	if auth.prefs("bob").hasScope(scopeWrite) {
		t.Error("bob should no longer have write access")
	}

	// Restoring full access is an empty list, not "all scope names".
	if err := auth.SetScopes("bob", nil); err != nil {
		t.Fatalf("SetScopes(nil): %v", err)
	}
	if !auth.prefs("bob").hasScope(scopeWrite) {
		t.Error("bob should have full access again after clearing scopes")
	}
}

// TestScopeEnforcementIntegration is the end-to-end guarantee behind the
// scope model: a read-only user can view pages but is denied at the
// middleware for anything write- or settings-shaped.
func TestScopeEnforcementIntegration(t *testing.T) {
	app, server, settingsClient := newTestAppFull(t)
	defer server.Close()

	if err := app.Auth.AddUser("reader", "password12345"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := app.Auth.SetScopes("reader", []string{"read"}); err != nil {
		t.Fatalf("SetScopes: %v", err)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	loginResp, err := client.PostForm(server.URL+"/_/login", url.Values{"username": {"reader"}, "password": {"password12345"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	closeTestBody(t, loginResp.Body)

	viewResp, err := client.Get(server.URL + "/" + testHome)
	if err != nil {
		t.Fatalf("GET page: %v", err)
	}
	closeTestBody(t, viewResp.Body)
	if viewResp.StatusCode != http.StatusOK {
		t.Errorf("read-scoped user GET /page/readme = %d, want 200", viewResp.StatusCode)
	}

	saveResp, err := client.PostForm(server.URL+"/"+testHome+"?do=save", url.Values{
		"title": {"readme"}, "body": {"nope"}, "basehash": {""},
	})
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	closeTestBody(t, saveResp.Body)
	if saveResp.StatusCode != http.StatusForbidden {
		t.Errorf("read-scoped user POST save = %d, want 403", saveResp.StatusCode)
	}

	settingsResp, err := client.Get(server.URL + "/_/settings")
	if err != nil {
		t.Fatalf("GET settings: %v", err)
	}
	closeTestBody(t, settingsResp.Body)
	if settingsResp.StatusCode != http.StatusForbidden {
		t.Errorf("read-scoped user GET /settings = %d, want 403", settingsResp.StatusCode)
	}

	if err := app.Auth.AddUser("settings-only", "password12345"); err != nil {
		t.Fatalf("AddUser(settings-only): %v", err)
	}
	if err := app.Auth.SetScopes("settings-only", []string{"settings"}); err != nil {
		t.Fatalf("SetScopes(settings-only): %v", err)
	}
	settingsToken, err := app.Auth.AddToken("settings-only", "namespace-management", time.Time{}, nil, nil)
	if err != nil {
		t.Fatalf("AddToken(settings-only): %v", err)
	}
	for _, path := range []string{"/_/namespaces", "/_/namespaces/new"} {
		settingsReq, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatalf("new settings PAT request: %v", err)
		}
		settingsReq.Header.Set("Authorization", "Bearer "+settingsToken)
		settingsPATResp, err := http.DefaultClient.Do(settingsReq)
		if err != nil {
			t.Fatalf("settings PAT GET %s: %v", path, err)
		}
		closeTestBody(t, settingsPATResp.Body)
		if settingsPATResp.StatusCode != http.StatusOK {
			t.Errorf("settings-only PAT GET %s = %d, want 200", path, settingsPATResp.StatusCode)
		}
	}

	readerToken, err := app.Auth.AddToken("reader", "read-only", time.Time{}, nil, nil)
	if err != nil {
		t.Fatalf("AddToken(reader): %v", err)
	}
	readerReq, err := http.NewRequest(http.MethodGet, server.URL+"/_/namespaces", nil)
	if err != nil {
		t.Fatalf("new reader PAT request: %v", err)
	}
	readerReq.Header.Set("Authorization", "Bearer "+readerToken)
	readerPATResp, err := http.DefaultClient.Do(readerReq)
	if err != nil {
		t.Fatalf("reader PAT GET /_/namespaces: %v", err)
	}
	closeTestBody(t, readerPATResp.Body)
	if readerPATResp.StatusCode != http.StatusForbidden {
		t.Errorf("read-only PAT GET /_/namespaces = %d, want 403", readerPATResp.StatusCode)
	}

	adminLogin(t, server, settingsClient)
	for _, path := range []string{"/_/namespaces", "/_/namespaces/new"} {
		resp, err := settingsClient.Get(server.URL + path)
		if err != nil {
			t.Fatalf("settings GET %s: %v", path, err)
		}
		closeTestBody(t, resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("settings GET %s = %d, want 200", path, resp.StatusCode)
		}
	}

	for _, path := range []string{"/_/namespaces", "/_/namespaces/new"} {
		resp, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatalf("reader GET %s: %v", path, err)
		}
		closeTestBody(t, resp.Body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("reader GET %s = %d, want 403", path, resp.StatusCode)
		}
	}
}

func TestRestrictedTokenHTTP(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	seed := func(page Page) {
		t.Helper()
		if _, err := app.Store.Save(pageFile(page.Slug), page.Encode(), "Seed "+page.Slug, "test", "test@hmd.local"); err != nil {
			t.Fatalf("seed %s: %v", page.Slug, err)
		}
		if err := app.Index.Update(page); err != nil {
			t.Fatalf("index %s: %v", page.Slug, err)
		}
	}
	seed(Page{Slug: "notes/allowed", Title: "Notes allowed", Tags: []string{"shared"}, Body: "notes content [[missing-notes]]"})
	seed(Page{Slug: "private/denied", Title: "Private denied", Tags: []string{"shared"}, Body: "private content [[missing-private]]"})
	for _, page := range []Page{
		{Slug: "notes/draft", Title: "Notes draft"},
		{Slug: "private/denied", Title: "Private hidden"},
	} {
		if _, err := app.Store.Save(hiddenFile(page.Slug), page.Encode(), "Seed hidden "+page.Slug, "test", "test@hmd.local"); err != nil {
			t.Fatalf("seed hidden %s: %v", page.Slug, err)
		}
	}
	if _, err := app.Store.Save("attachments/private/denied/file.txt", []byte("private attachment"), "Seed attachment", "test", "test@hmd.local"); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	notesConfig, err := (NamespaceConfig{New: &NewPageConfig{Template: "template", Slug: "created"}}).Encode()
	if err != nil {
		t.Fatalf("encode notes config: %v", err)
	}
	if _, err := app.Store.Save(namespaceConfigPath("notes"), notesConfig, "Configure notes", "test", "test@hmd.local"); err != nil {
		t.Fatalf("seed notes config: %v", err)
	}
	app.refreshNamespaces()

	token, err := app.Auth.AddToken("admin", "notes-http", time.Time{}, []string{"read", "write"}, []string{"notes"})
	if err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	do := func(method, path string, body io.Reader) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, body)
		if err != nil {
			t.Fatalf("new request %s %s: %v", method, path, err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %s %s: %v", method, path, err)
		}
		data, readErr := io.ReadAll(resp.Body)
		closeTestBody(t, resp.Body)
		if readErr != nil {
			t.Fatalf("read response %s %s: %v", method, path, readErr)
		}
		return resp.StatusCode, data
	}

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/notes/allowed", http.StatusOK},
		{http.MethodGet, "/private/denied", http.StatusForbidden},
		{http.MethodGet, "/other/page", http.StatusForbidden},
		{http.MethodGet, "/_/settings", http.StatusForbidden},
		{http.MethodGet, "/_/api/preview/private/denied", http.StatusForbidden},
		{http.MethodGet, "/_/attachments/private/denied/file.txt", http.StatusForbidden},
	} {
		if got, _ := do(tc.method, tc.path, nil); got != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, got, tc.want)
		}
	}
	if status, body := do(http.MethodGet, "/_/api/preview/private/denied", nil); status != http.StatusForbidden || string(body) != `{"error":"namespace access denied"}` {
		t.Errorf("API namespace denial = %d %q, want 403 JSON error", status, body)
	}

	for _, path := range []string{
		"/_/api/search?q=private",
		"/_/search?q=denied",
		"/_/tags",
		"/_/tags/shared",
		"/_/health-report",
		"/_/hidden",
		"/_/namespaces",
	} {
		status, body := do(http.MethodGet, path, nil)
		if path != "/_/namespaces" && status != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, status)
		}
		if strings.Contains(string(body), "private/denied") || strings.Contains(string(body), "private") {
			t.Errorf("GET %s leaked a restricted namespace: %s", path, body)
		}
	}

	if got, _ := do(http.MethodPost, "/_/new?ns=private", nil); got != http.StatusForbidden {
		t.Errorf("POST /_/new?ns=private = %d, want 403", got)
	}
	if got, _ := do(http.MethodPost, "/_/new?ns=notes", nil); got != http.StatusOK {
		t.Errorf("POST /_/new?ns=notes = %d, want 200 (draft rendered inline)", got)
	}

	upload := func(slug string) int {
		t.Helper()
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		part, err := writer.CreateFormFile("file", "file.png")
		if err != nil {
			t.Fatalf("create upload part: %v", err)
		}
		if _, err := part.Write([]byte("png")); err != nil {
			t.Fatalf("write upload: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close upload: %v", err)
		}
		req, err := http.NewRequest(http.MethodPost, server.URL+"/_/api/attachments/"+slug, body)
		if err != nil {
			t.Fatalf("new upload request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("upload %s: %v", slug, err)
		}
		closeTestBody(t, resp.Body)
		return resp.StatusCode
	}
	if got := upload("private/denied"); got != http.StatusForbidden {
		t.Errorf("upload private/denied = %d, want 403", got)
	}
	if got := upload("notes/allowed"); got != http.StatusOK {
		t.Errorf("upload notes/allowed = %d, want 200", got)
	}
}

// TestCreateUserViaSettings covers the UI replacement for `hmd adduser`: an
// admin creates a user from the settings page, with scopes picked in the
// form, and the user can immediately log in with them in effect.
func TestCreateUserViaSettings(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	loginResp, err := client.PostForm(server.URL+"/_/login", url.Values{"username": {"admin"}, "password": {"password12345"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	closeTestBody(t, loginResp.Body)

	createResp, err := client.PostForm(server.URL+"/_/settings/users", url.Values{
		"name": {"newbie"}, "password": {"password12345"}, "scopes": {"read"},
	})
	if err != nil {
		t.Fatalf("POST /settings/users: %v", err)
	}
	closeTestBody(t, createResp.Body)

	if !app.Auth.UserExists("newbie") {
		t.Fatal("newbie should exist after creation")
	}
	if app.Auth.prefs("newbie").hasScope(scopeWrite) {
		t.Error("newbie should be read-only per the submitted scopes")
	}
	if _, ok := app.Auth.Login("newbie", "password12345"); !ok {
		t.Error("newbie should be able to log in with the password set at creation")
	}

	// Duplicate name is rejected rather than silently resetting the password.
	dupResp, err := client.PostForm(server.URL+"/_/settings/users", url.Values{
		"name": {"newbie"}, "password": {"otherpassword"},
	})
	if err != nil {
		t.Fatalf("POST /settings/users (dup): %v", err)
	}
	closeTestBody(t, dupResp.Body)
	if _, ok := app.Auth.Login("newbie", "password12345"); !ok {
		t.Error("original password should still work after a rejected duplicate create")
	}
}

// TestSetUserScopesBootstrapAdminImmutable ensures the bootstrap admin
// (HMD_ADMIN_USER) always keeps full access. It isn't listed with editable
// checkboxes on the settings page; this covers a hand-crafted request
// against it too, since that's the one account that can't be recreated from
// the UI if it were ever locked out.
func TestSetUserScopesBootstrapAdminImmutable(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	loginResp, err := client.PostForm(server.URL+"/_/login", url.Values{"username": {"admin"}, "password": {"password12345"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	closeTestBody(t, loginResp.Body)

	resp, err := client.PostForm(server.URL+"/_/settings/users/scopes", url.Values{
		"name": {"admin"}, "scopes": {"read"},
	})
	if err != nil {
		t.Fatalf("POST /settings/users/scopes: %v", err)
	}
	closeTestBody(t, resp.Body)

	if app.Auth.prefs("admin").Scopes != nil {
		t.Errorf("bootstrap admin scopes = %v, want unchanged (nil = full access)", app.Auth.prefs("admin").Scopes)
	}

	settingsResp, err := client.Get(server.URL + "/_/settings")
	if err != nil {
		t.Fatalf("GET /settings: %v", err)
	}
	closeTestBody(t, settingsResp.Body)
	if settingsResp.StatusCode != http.StatusOK {
		t.Errorf("admin should still have settings access, got %d", settingsResp.StatusCode)
	}
}

// TestSetUserScopesBlocksSelfLockout ensures a non-bootstrap user with
// settings scope can't strip that scope from their own account — with hmd
// scopes removed, that would lock them out with no way back short of
// hand-editing users.json.
func TestSetUserScopesBlocksSelfLockout(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	if err := app.Auth.AddUser("mod", "password12345"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := app.Auth.SetScopes("mod", []string{"read", "write", "settings"}); err != nil {
		t.Fatalf("SetScopes: %v", err)
	}

	loginResp, err := client.PostForm(server.URL+"/_/login", url.Values{"username": {"mod"}, "password": {"password12345"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	closeTestBody(t, loginResp.Body)

	resp, err := client.PostForm(server.URL+"/_/settings/users/scopes", url.Values{
		"name": {"mod"}, "scopes": {"read"},
	})
	if err != nil {
		t.Fatalf("POST /settings/users/scopes: %v", err)
	}
	closeTestBody(t, resp.Body)

	if !app.Auth.prefs("mod").hasScope(scopeSettings) {
		t.Error("mod should still have settings scope after the rejected self-lockout")
	}

	settingsResp, err := client.Get(server.URL + "/_/settings")
	if err != nil {
		t.Fatalf("GET /settings: %v", err)
	}
	closeTestBody(t, settingsResp.Body)
	if settingsResp.StatusCode != http.StatusOK {
		t.Errorf("mod should still have settings access after the rejected self-lockout, got %d", settingsResp.StatusCode)
	}
}
