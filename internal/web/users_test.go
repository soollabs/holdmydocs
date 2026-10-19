package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"hmd/internal/auth"
	"hmd/internal/config"
)

func TestUserManagementRoutesAndAccountSessions(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()
	reader := signIn(t, app, server.URL, "reader", "read")
	status, body := fetch(t, reader, http.MethodGet, server.URL+"/_/settings/password")
	if status != http.StatusOK || !strings.Contains(body, `name="current"`) {
		t.Fatalf("reader password panel: %d", status)
	}
	response, err := postJSON(t, reader, server.URL+"/_/api/settings/password", map[string]any{
		"current": "password12345", "password": "another-unique-test-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("own password change: %d", response.StatusCode)
	}
	if status, _ := fetch(t, reader, http.MethodGet, server.URL+"/_/settings/password"); status != http.StatusOK {
		t.Fatalf("current session lost: %d", status)
	}
	for _, path := range []string{"/_/api/settings/users/delete", "/_/api/settings/users/password"} {
		response, err := postJSON(t, reader, server.URL+path, map[string]any{"name": "admin", "password": "another-unique-test-password"})
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Errorf("%s = %d", path, response.StatusCode)
		}
	}
	pat, err := app.Auth.AddToken("reader", "preferences", time.Time{}, []string{"read", "preferences"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/_/api/settings/password", strings.NewReader(`{"current":"another-unique-test-password","password":"yet-another-unique-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+pat)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("PAT password change = %d", response.StatusCode)
	}
}

func TestUserListTypesAndBootstrapBanner(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()
	admin := signIn(t, app, server.URL, "admin", "read", "write", "admin")
	if err := app.Auth.AddUserWithScopes("local-user", "a-unique-long-test-password", []string{"read"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Auth.EnsureOIDCUser(auth.OIDCIdentity{Issuer: "https://id.example.test", Subject: "one"}, "sso-user", "", []string{"read"}); err != nil {
		t.Fatal(err)
	}
	session, ok := app.Auth.NewSession("sso-user")
	if !ok {
		t.Fatal("SSO session failed")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(origin, []*http.Cookie{{Name: "hmd_session", Value: session, Path: "/"}})
	sso := &http.Client{Jar: jar}
	_, ssoBody := fetch(t, sso, http.MethodGet, server.URL+"/_/settings/password")
	if strings.Contains(ssoBody, `name="current"`) || strings.Contains(ssoBody, `href="/_/settings/password"`) || !strings.Contains(ssoBody, "identity provider") {
		t.Error("SSO password panel or navigation offers a password change")
	}
	_, body := fetch(t, admin, http.MethodGet, server.URL+"/_/settings/users")
	for _, fragment := range []string{`value="admin"`, `value="local-user"`, `value="sso-user"`, `name="reset-user" value="local-user"`, `id="user-reset-dialog"`, "single sign-on", "OIDC admission"} {
		if !strings.Contains(body, fragment) {
			t.Errorf("user list lacks %q", fragment)
		}
	}
	if strings.Contains(body, `name="reset-user" value="sso-user"`) {
		t.Error("SSO reset offered")
	}
	if !strings.Contains(body, `formaction="/_/api/settings/users/delete"`) {
		t.Error("deletion not offered")
	}
	_, body = fetch(t, admin, http.MethodGet, server.URL+"/_/settings/server")
	if strings.Contains(body, `name="server.admin.user"`) || strings.Contains(body, `name="server.admin.password"`) {
		t.Error("bootstrap fields on Server")
	}
	cfg := app.config()
	cfg.AdminUser, cfg.AdminPass = "admin", "a-unique-long-test-password"
	app.SetConfig(cfg)
	_, body = fetch(t, admin, http.MethodGet, server.URL+"/_/settings/users")
	if !strings.Contains(body, `data-dismiss="bootstrap-env"`) || !strings.Contains(body, config.EnvName("server.admin.password")) {
		t.Error("admin bootstrap banner missing")
	}
	reader := signIn(t, app, server.URL, "reader", "read")
	_, body = fetch(t, reader, http.MethodGet, server.URL+"/_/settings/password")
	if strings.Contains(body, `data-dismiss="bootstrap-env"`) {
		t.Error("bootstrap banner shown to reader")
	}
	cfg.AdminUser, cfg.AdminPass = "", ""
	app.SetConfig(cfg)
	_, body = fetch(t, admin, http.MethodGet, server.URL+"/_/settings/users")
	if strings.Contains(body, `data-dismiss="bootstrap-env"`) {
		t.Error("bootstrap banner shown without variables")
	}
	response, err := postJSON(t, admin, server.URL+"/_/api/settings/users/delete", map[string]any{"name": "sso-user"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("delete SSO: %d %s", response.StatusCode, raw)
	}
}
