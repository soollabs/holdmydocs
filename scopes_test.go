package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
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
	if err := auth.AddUser("bob", "secret"); err != nil {
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
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	if err := app.Auth.AddUser("reader", "secret"); err != nil {
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
	loginResp, err := client.PostForm(server.URL+"/login", url.Values{"username": {"reader"}, "password": {"secret"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	closeTestBody(t, loginResp.Body)

	viewResp, err := client.Get(server.URL + "/page/readme")
	if err != nil {
		t.Fatalf("GET page: %v", err)
	}
	closeTestBody(t, viewResp.Body)
	if viewResp.StatusCode != http.StatusOK {
		t.Errorf("read-scoped user GET /page/readme = %d, want 200", viewResp.StatusCode)
	}

	saveResp, err := client.PostForm(server.URL+"/page/readme/save", url.Values{
		"title": {"readme"}, "body": {"nope"}, "basehash": {""},
	})
	if err != nil {
		t.Fatalf("POST save: %v", err)
	}
	closeTestBody(t, saveResp.Body)
	if saveResp.StatusCode != http.StatusForbidden {
		t.Errorf("read-scoped user POST save = %d, want 403", saveResp.StatusCode)
	}

	settingsResp, err := client.Get(server.URL + "/settings")
	if err != nil {
		t.Fatalf("GET settings: %v", err)
	}
	closeTestBody(t, settingsResp.Body)
	if settingsResp.StatusCode != http.StatusForbidden {
		t.Errorf("read-scoped user GET /settings = %d, want 403", settingsResp.StatusCode)
	}
}
