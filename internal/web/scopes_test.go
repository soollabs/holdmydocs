package web

import (
	"hmd/internal/testhttp"

	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/wiki"
)

func TestHasScopeRejectsEmptyScopes(t *testing.T) {
	u := auth.UserRecord{}
	for _, s := range []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeSettings} {
		if u.HasScope(s) {
			t.Errorf("user with no scopes has %s access", s)
		}
	}
}

func TestHasScopeRestricts(t *testing.T) {
	u := auth.UserRecord{Scopes: []string{"read"}}
	if !u.HasScope(auth.ScopeRead) {
		t.Error("expected read access")
	}
	if u.HasScope(auth.ScopeWrite) {
		t.Error("expected no write access")
	}
	if u.HasScope(auth.ScopeSettings) {
		t.Error("expected no settings access")
	}
}

func TestSetScopesRejectsUnknown(t *testing.T) {
	authn, err := OpenAuth(config.Config{AppDir: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenAuth: %v", err)
	}
	if err := authn.AddUser("bob", "password12345"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := authn.SetScopes("bob", []string{"bogus"}); err == nil {
		t.Error("expected error for unknown scope")
	}
	if err := authn.SetScopes("bob", []string{"read"}); err != nil {
		t.Fatalf("SetScopes: %v", err)
	}
	if authn.Prefs("bob").HasScope(auth.ScopeWrite) {
		t.Error("bob retains write access")
	}

	if err := authn.SetScopes("bob", nil); err == nil {
		t.Error("SetScopes(nil) accepted empty scopes")
	}
}

// TestScopeEnforcementIntegration tests HTTP scope enforcement.
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
	loginResp, err := testhttp.PostForm(t, client, server.URL+"/_/login", url.Values{"username": {"reader"}, "password": {"password12345"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := loginResp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}

	viewResp, err := testhttp.Get(t, client, server.URL+"/"+testHome)
	if err != nil {
		t.Fatalf("GET page: %v", err)
	}
	if err := viewResp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if viewResp.StatusCode != http.StatusOK {
		t.Errorf("read-scoped user GET /page/readme = %d, want 200", viewResp.StatusCode)
	}

	saveResp, err := postPageSave(t, client, server, testHome, url.Values{
		"title": {"readme"}, "body": {"nope"}, "basehash": {""},
	})
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	if err := saveResp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if saveResp.StatusCode != http.StatusForbidden {
		t.Errorf("read-scoped user POST save = %d, want 403", saveResp.StatusCode)
	}

	settingsResp, err := testhttp.Get(t, client, server.URL+"/_/settings")
	if err != nil {
		t.Fatalf("GET settings: %v", err)
	}
	if err := settingsResp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if settingsResp.StatusCode != http.StatusForbidden {
		t.Errorf("read-scoped user GET /settings = %d, want 403", settingsResp.StatusCode)
	}

	if err := app.Auth.AddUser("settings-only", "password12345"); err != nil {
		t.Fatalf("AddUser(settings-only): %v", err)
	}
	if err := app.Auth.SetScopes("settings-only", []string{"settings"}); err != nil {
		t.Fatalf("SetScopes(settings-only): %v", err)
	}
	settingsToken, err := app.Auth.AddToken("settings-only", "namespace-management", time.Time{}, []string{"settings"}, nil)
	if err != nil {
		t.Fatalf("AddToken(settings-only): %v", err)
	}
	for _, path := range []string{"/_/namespaces", "/_/namespaces/new"} {
		settingsReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatalf("new settings PAT request: %v", err)
		}
		settingsReq.Header.Set("Authorization", "Bearer "+settingsToken)
		settingsPATResp, err := http.DefaultClient.Do(settingsReq)
		if err != nil {
			t.Fatalf("settings PAT GET %s: %v", path, err)
		}
		if err := settingsPATResp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
		if settingsPATResp.StatusCode != http.StatusOK {
			t.Errorf("settings-only PAT GET %s = %d, want 200", path, settingsPATResp.StatusCode)
		}
	}

	readerToken, err := app.Auth.AddToken("reader", "read-only", time.Time{}, []string{"read"}, nil)
	if err != nil {
		t.Fatalf("AddToken(reader): %v", err)
	}
	readerReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/_/namespaces", nil)
	if err != nil {
		t.Fatalf("new reader PAT request: %v", err)
	}
	readerReq.Header.Set("Authorization", "Bearer "+readerToken)
	readerPATResp, err := http.DefaultClient.Do(readerReq)
	if err != nil {
		t.Fatalf("reader PAT GET /_/namespaces: %v", err)
	}
	if err := readerPATResp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if readerPATResp.StatusCode != http.StatusForbidden {
		t.Errorf("read-only PAT GET /_/namespaces = %d, want 403", readerPATResp.StatusCode)
	}

	adminLogin(t, server, settingsClient)
	for _, path := range []string{"/_/namespaces", "/_/namespaces/new"} {
		resp, err := testhttp.Get(t, settingsClient, server.URL+path)
		if err != nil {
			t.Fatalf("settings GET %s: %v", path, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("settings GET %s = %d, want 200", path, resp.StatusCode)
		}
	}

	for _, path := range []string{"/_/namespaces", "/_/namespaces/new"} {
		resp, err := testhttp.Get(t, client, server.URL+path)
		if err != nil {
			t.Fatalf("reader GET %s: %v", path, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("reader GET %s = %d, want 403", path, resp.StatusCode)
		}
	}
}

func TestRestrictedTokenHTTP(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	seed := func(page wiki.Page) {
		t.Helper()
		if _, err := app.Store.Save(wiki.PageFile(page.Slug), page.Encode(), "Seed "+page.Slug, "test", "test@hmd.local"); err != nil {
			t.Fatalf("seed %s: %v", page.Slug, err)
		}
		if err := app.Index.Update(page); err != nil {
			t.Fatalf("index %s: %v", page.Slug, err)
		}
	}
	seed(wiki.Page{Slug: "notes/allowed", Title: "Notes allowed", Tags: []string{"shared"}, Body: "notes content [[missing-notes]]"})
	seed(wiki.Page{Slug: "private/denied", Title: "Private denied", Tags: []string{"shared"}, Body: "private content [[missing-private]]"})
	for _, page := range []wiki.Page{
		{Slug: "notes/draft", Title: "Notes draft"},
		{Slug: "private/denied", Title: "Private hidden"},
	} {
		if _, err := app.Store.Save(wiki.HiddenFile(page.Slug), page.Encode(), "Seed hidden "+page.Slug, "test", "test@hmd.local"); err != nil {
			t.Fatalf("seed hidden %s: %v", page.Slug, err)
		}
	}
	if _, err := app.Store.Save("attachments/private/denied/file.txt", []byte("private attachment"), "Seed attachment", "test", "test@hmd.local"); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	notesConfig, err := (wiki.NamespaceConfig{New: &wiki.NewPageConfig{Template: "template", Slug: "created"}}).Encode()
	if err != nil {
		t.Fatalf("encode notes config: %v", err)
	}
	if _, err := app.Store.Save(wiki.NamespaceConfigPath("notes"), notesConfig, "Configure notes", "test", "test@hmd.local"); err != nil {
		t.Fatalf("seed notes config: %v", err)
	}
	if err := app.apiClient().RefreshNamespaces(); err != nil {
		t.Fatalf("refreshing namespaces: %v", err)
	}

	token, err := app.Auth.AddToken("admin", "notes-http", time.Time{}, []string{"read", "write"}, []string{"notes"})
	if err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	do := func(method, path string, body io.Reader) (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, body)
		if err != nil {
			t.Fatalf("new request %s %s: %v", method, path, err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %s %s: %v", method, path, err)
		}
		data, readErr := io.ReadAll(resp.Body)
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
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

	if got, _ := do(http.MethodGet, "/_/new?ns=private", nil); got != http.StatusForbidden {
		t.Errorf("GET /_/new?ns=private = %d, want 403", got)
	}
	if got, _ := do(http.MethodGet, "/_/new?ns=notes", nil); got != http.StatusOK {
		t.Errorf("GET /_/new?ns=notes = %d, want 200 (draft rendered inline)", got)
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
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/_/api/attachments/"+slug, body)
		if err != nil {
			t.Fatalf("new upload request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("upload %s: %v", slug, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
		return resp.StatusCode
	}
	if got := upload("private/denied"); got != http.StatusForbidden {
		t.Errorf("upload private/denied = %d, want 403", got)
	}
	if got := upload("notes/allowed"); got != http.StatusOK {
		t.Errorf("upload notes/allowed = %d, want 200", got)
	}
}

// TestCreateUserViaSettings tests user creation and scope selection.
func TestCreateUserViaSettings(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	loginResp, err := testhttp.PostForm(t, client, server.URL+"/_/login", url.Values{"username": {"admin"}, "password": {"password12345"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := loginResp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}

	createResp, err := postJSON(t, client, server.URL+"/_/api/settings/users", userCreateJSON(url.Values{
		"name": {"newbie"}, "password": {"password12345"}, "scopes": {"read"},
	}))
	if err != nil {
		t.Fatalf("POST /settings/users: %v", err)
	}
	if err := createResp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}

	if !app.Auth.UserExists("newbie") {
		t.Fatal("newbie should exist after creation")
	}
	if app.Auth.Prefs("newbie").HasScope(auth.ScopeWrite) {
		t.Error("newbie should be read-only per the submitted scopes")
	}
	if _, ok := app.Auth.Login("newbie", "password12345"); !ok {
		t.Error("newbie should be able to log in with the password set at creation")
	}

	emptyResp, err := postJSON(t, client, server.URL+"/_/api/settings/users", userCreateJSON(url.Values{
		"name": {"no-policy"}, "password": {"password12345"},
	}))
	if err != nil {
		t.Fatalf("POST /settings/users (empty scopes): %v", err)
	}
	if err := emptyResp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if app.Auth.UserExists("no-policy") {
		t.Error("user with no submitted scopes should not have been persisted")
	}

	dupResp, err := postJSON(t, client, server.URL+"/_/api/settings/users", userCreateJSON(url.Values{
		"name": {"newbie"}, "password": {"otherpassword"},
	}))
	if err != nil {
		t.Fatalf("POST /settings/users (dup): %v", err)
	}
	if err := dupResp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if _, ok := app.Auth.Login("newbie", "password12345"); !ok {
		t.Error("original password should still work after a rejected duplicate create")
	}
}

// TestSetUserScopesBootstrapAdminImmutable preserves bootstrap admin access.
func TestSetUserScopesBootstrapAdminImmutable(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	loginResp, err := testhttp.PostForm(t, client, server.URL+"/_/login", url.Values{"username": {"admin"}, "password": {"password12345"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := loginResp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}

	resp, err := postJSON(t, client, server.URL+"/_/api/settings/users/scopes", userScopesJSON(url.Values{
		"name": {"admin"}, "scopes": {"read"},
	}))
	if err != nil {
		t.Fatalf("POST /settings/users/scopes: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}

	if got := app.Auth.Prefs("admin").Scopes; len(got) != 3 {
		t.Errorf("bootstrap admin scopes = %v, want all scopes", got)
	}

	settingsResp, err := testhttp.Get(t, client, server.URL+"/_/settings")
	if err != nil {
		t.Fatalf("GET /settings: %v", err)
	}
	if err := settingsResp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if settingsResp.StatusCode != http.StatusOK {
		t.Errorf("admin should still have settings access, got %d", settingsResp.StatusCode)
	}
}

// TestSetUserScopesBlocksSelfLockout prevents settings self-lockout.
func TestSetUserScopesBlocksSelfLockout(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	if err := app.Auth.AddUser("mod", "password12345"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := app.Auth.SetScopes("mod", []string{"read", "write", "settings"}); err != nil {
		t.Fatalf("SetScopes: %v", err)
	}

	loginResp, err := testhttp.PostForm(t, client, server.URL+"/_/login", url.Values{"username": {"mod"}, "password": {"password12345"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := loginResp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}

	resp, err := postJSON(t, client, server.URL+"/_/api/settings/users/scopes", userScopesJSON(url.Values{
		"name": {"mod"}, "scopes": {"read"},
	}))
	if err != nil {
		t.Fatalf("POST /settings/users/scopes: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}

	if !app.Auth.Prefs("mod").HasScope(auth.ScopeSettings) {
		t.Error("mod should still have settings scope after the rejected self-lockout")
	}

	settingsResp, err := testhttp.Get(t, client, server.URL+"/_/settings")
	if err != nil {
		t.Fatalf("GET /settings: %v", err)
	}
	if err := settingsResp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if settingsResp.StatusCode != http.StatusOK {
		t.Errorf("mod should still have settings access after the rejected self-lockout, got %d", settingsResp.StatusCode)
	}
}
