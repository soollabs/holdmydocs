package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestOIDCUsernameFallback(t *testing.T) {
	cases := []struct {
		claims oidcClaims
		want   string
	}{
		{oidcClaims{PreferredUsername: "alice", Email: "alice@example.com"}, "alice"},
		{oidcClaims{Email: "bob@example.com"}, "bob"},
		{oidcClaims{Email: "@example.com"}, ""},
		{oidcClaims{}, ""},
	}
	for _, c := range cases {
		if got := oidcUsername(c.claims); got != c.want {
			t.Errorf("oidcUsername(%+v) = %q, want %q", c.claims, got, c.want)
		}
	}
}

func TestEmptyHashCannotPasswordLogin(t *testing.T) {
	auth, err := OpenAuth(Config{AppDir: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}
	if err := auth.EnsureOIDCUser("sso-user", ""); err != nil {
		t.Fatalf("EnsureOIDCUser failed: %v", err)
	}

	if _, ok := auth.Login("sso-user", ""); ok {
		t.Errorf("empty password login on empty-hash record should fail")
	}
	if _, ok := auth.Login("sso-user", "anything"); ok {
		t.Errorf("password login on empty-hash record should fail")
	}
}

func TestEnsureOIDCUser(t *testing.T) {
	cfg := Config{AppDir: t.TempDir()}
	auth, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}

	// First login provisions with the claims author.
	if err := auth.EnsureOIDCUser("alice", "Alice <alice@example.com>"); err != nil {
		t.Fatalf("first EnsureOIDCUser failed: %v", err)
	}
	if got := auth.AuthorFor("alice"); got != "Alice <alice@example.com>" {
		t.Errorf("AuthorFor = %q, want claims author", got)
	}

	// A user-set author is not clobbered by later logins.
	if err := auth.SetAuthor("alice", "Custom <me@example.com>"); err != nil {
		t.Fatalf("SetAuthor failed: %v", err)
	}
	if err := auth.EnsureOIDCUser("alice", "Alice <alice@example.com>"); err != nil {
		t.Fatalf("second EnsureOIDCUser failed: %v", err)
	}
	if got := auth.AuthorFor("alice"); got != "Custom <me@example.com>" {
		t.Errorf("AuthorFor = %q, second login clobbered the user's override", got)
	}

	// Record persists across restart.
	auth2, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("second OpenAuth failed: %v", err)
	}
	if got := auth2.AuthorFor("alice"); got != "Custom <me@example.com>" {
		t.Errorf("provisioned record did not persist, AuthorFor = %q", got)
	}
}

func TestOIDCCallbackRejectsBadState(t *testing.T) {
	// State is checked before the OAuth config is touched, so an empty
	// OIDCAuth is enough here.
	app := &App{OIDC: &OIDCAuth{}}

	// Missing state cookie.
	r := httptest.NewRequest("GET", "/auth/oidc/callback?state=abc&code=x", nil)
	w := httptest.NewRecorder()
	app.handleOIDCCallback(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing state cookie: got %d, want 400", w.Code)
	}

	// Mismatched state.
	r = httptest.NewRequest("GET", "/auth/oidc/callback?state=abc&code=x", nil)
	r.AddCookie(&http.Cookie{Name: "hmd_oidc_state", Value: "different"})
	w = httptest.NewRecorder()
	app.handleOIDCCallback(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("mismatched state: got %d, want 400", w.Code)
	}
}

func TestOIDCCallbackDisabled(t *testing.T) {
	app := &App{}
	r := httptest.NewRequest("GET", "/auth/oidc/callback", nil)
	w := httptest.NewRecorder()
	app.handleOIDCCallback(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("disabled OIDC callback: got %d, want 404", w.Code)
	}
}

func TestOIDCLoginSetsStateAndRedirects(t *testing.T) {
	app := &App{OIDC: &OIDCAuth{
		oauth: oauth2.Config{
			ClientID:    "hmd",
			Endpoint:    oauth2.Endpoint{AuthURL: "https://idp.example.com/auth"},
			RedirectURL: "https://wiki.example.com/auth/oidc/callback",
		},
	}}

	r := httptest.NewRequest("GET", "/auth/oidc/login", nil)
	w := httptest.NewRecorder()
	app.handleOIDCLogin(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want 303", w.Code)
	}
	var state string
	for _, c := range w.Result().Cookies() {
		if c.Name == "hmd_oidc_state" {
			state = c.Value
			if !c.HttpOnly {
				t.Errorf("state cookie must be HttpOnly")
			}
		}
	}
	if state == "" {
		t.Fatalf("no hmd_oidc_state cookie set")
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://idp.example.com/auth") || !strings.Contains(loc, "state="+state) {
		t.Errorf("redirect %q should target the IdP with the state from the cookie", loc)
	}
	if !strings.Contains(loc, "code_challenge=") {
		t.Errorf("redirect %q should carry a PKCE challenge", loc)
	}
}

func TestValidateIconSVG(t *testing.T) {
	cases := []struct {
		name string
		svg  string
		ok   bool
	}{
		{"square viewBox", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0h24v24H0z"/></svg>`, true},
		{"square width/height", `<svg width="32" height="32"></svg>`, true},
		{"non-square viewBox", `<svg viewBox="0 0 24 16"></svg>`, false},
		{"non-square width/height", `<svg width="32" height="16"></svg>`, false},
		{"no dimensions", `<svg></svg>`, false},
		{"not svg", `<html></html>`, false},
		{"not xml", `PNG garbage`, false},
	}
	for _, c := range cases {
		err := validateIconSVG([]byte(c.svg))
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected rejection", c.name)
		}
	}

	if err := validateIconSVG(bytes.Repeat([]byte("x"), maxIconBytes+1)); err == nil {
		t.Errorf("oversized icon should be rejected")
	}
}

func TestOIDCIconLoadAndServe(t *testing.T) {
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"></svg>`
	path := filepath.Join(t.TempDir(), "icon.svg")
	if err := os.WriteFile(path, []byte(svg), 0644); err != nil {
		t.Fatal(err)
	}

	icon, err := loadOIDCIcon(context.Background(), path)
	if err != nil {
		t.Fatalf("loadOIDCIcon failed: %v", err)
	}

	app := &App{OIDC: &OIDCAuth{icon: icon}}
	w := httptest.NewRecorder()
	app.handleOIDCIcon(w, httptest.NewRequest("GET", "/auth/oidc/icon", nil))
	if w.Code != http.StatusOK || w.Body.String() != svg {
		t.Errorf("icon not served back: code=%d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q", ct)
	}

	// No icon configured: 404.
	app = &App{OIDC: &OIDCAuth{}}
	w = httptest.NewRecorder()
	app.handleOIDCIcon(w, httptest.NewRequest("GET", "/auth/oidc/icon", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("unset icon: got %d, want 404", w.Code)
	}

	// Non-square local file fails to load.
	badPath := filepath.Join(t.TempDir(), "bad.svg")
	if err := os.WriteFile(badPath, []byte(`<svg viewBox="0 0 24 16"></svg>`), 0644); err != nil {
		t.Fatalf("writing non-square icon: %v", err)
	}
	if _, err := loadOIDCIcon(context.Background(), badPath); err == nil {
		t.Errorf("non-square icon file should be rejected")
	}
}

func TestLoginTemplateOIDC(t *testing.T) {
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatalf("parseTemplates failed: %v", err)
	}

	render := func(data TemplateData) string {
		var buf bytes.Buffer
		if err := tmpl["login"].ExecuteTemplate(&buf, "layout", data); err != nil {
			t.Fatalf("executing login template: %v", err)
		}
		return buf.String()
	}

	// SSO enabled, local login hidden.
	out := render(TemplateData{OIDCEnabled: true, OIDCButtonText: "Login with Authelia"})
	if !strings.Contains(out, "Login with Authelia") || !strings.Contains(out, "/auth/oidc/login") {
		t.Errorf("SSO button with configured text should be rendered")
	}
	if strings.Contains(out, `name="password"`) {
		t.Errorf("password form should be hidden when local login is disabled")
	}

	// SSO disabled, plain password form.
	out = render(TemplateData{OIDCLocalLogin: true})
	if strings.Contains(out, "/auth/oidc/login") {
		t.Errorf("SSO button should not render when OIDC is disabled")
	}
	if !strings.Contains(out, `name="password"`) {
		t.Errorf("password form should render when OIDC is disabled")
	}
}
