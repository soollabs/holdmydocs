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

// TestCreateUserViaSettings covers the UI replacement for `hmd adduser`: an
// admin creates a user from the settings page, with scopes picked in the
// form, and the user can immediately log in with them in effect.
func TestCreateUserViaSettings(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	loginResp, err := client.PostForm(server.URL+"/login", url.Values{"username": {"admin"}, "password": {"test"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	closeTestBody(t, loginResp.Body)

	createResp, err := client.PostForm(server.URL+"/settings/users", url.Values{
		"name": {"newbie"}, "password": {"secret"}, "scopes": {"read"},
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
	if _, ok := app.Auth.Login("newbie", "secret"); !ok {
		t.Error("newbie should be able to log in with the password set at creation")
	}

	// Duplicate name is rejected rather than silently resetting the password.
	dupResp, err := client.PostForm(server.URL+"/settings/users", url.Values{
		"name": {"newbie"}, "password": {"other"},
	})
	if err != nil {
		t.Fatalf("POST /settings/users (dup): %v", err)
	}
	closeTestBody(t, dupResp.Body)
	if _, ok := app.Auth.Login("newbie", "secret"); !ok {
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

	loginResp, err := client.PostForm(server.URL+"/login", url.Values{"username": {"admin"}, "password": {"test"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	closeTestBody(t, loginResp.Body)

	resp, err := client.PostForm(server.URL+"/settings/users/scopes", url.Values{
		"name": {"admin"}, "scopes": {"read"},
	})
	if err != nil {
		t.Fatalf("POST /settings/users/scopes: %v", err)
	}
	closeTestBody(t, resp.Body)

	if app.Auth.prefs("admin").Scopes != nil {
		t.Errorf("bootstrap admin scopes = %v, want unchanged (nil = full access)", app.Auth.prefs("admin").Scopes)
	}

	settingsResp, err := client.Get(server.URL + "/settings")
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

	if err := app.Auth.AddUser("mod", "secret"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := app.Auth.SetScopes("mod", []string{"read", "write", "settings"}); err != nil {
		t.Fatalf("SetScopes: %v", err)
	}

	loginResp, err := client.PostForm(server.URL+"/login", url.Values{"username": {"mod"}, "password": {"secret"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	closeTestBody(t, loginResp.Body)

	resp, err := client.PostForm(server.URL+"/settings/users/scopes", url.Values{
		"name": {"mod"}, "scopes": {"read"},
	})
	if err != nil {
		t.Fatalf("POST /settings/users/scopes: %v", err)
	}
	closeTestBody(t, resp.Body)

	if !app.Auth.prefs("mod").hasScope(scopeSettings) {
		t.Error("mod should still have settings scope after the rejected self-lockout")
	}

	settingsResp, err := client.Get(server.URL + "/settings")
	if err != nil {
		t.Fatalf("GET /settings: %v", err)
	}
	closeTestBody(t, settingsResp.Body)
	if settingsResp.StatusCode != http.StatusOK {
		t.Errorf("mod should still have settings access after the rejected self-lockout, got %d", settingsResp.StatusCode)
	}
}
