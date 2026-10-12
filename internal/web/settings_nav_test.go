package web

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"hmd/internal/oauthserver"
	"hmd/internal/testhttp"
)

func TestSettingsNavigationLinksSettingsPages(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	store, err := oauthserver.OpenStore(app.config().AppDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	app.OAuth, err = oauthserver.NewService(oauthserver.ServerOptions{Issuer: "https://wiki.example.test"}, store, app.Auth)
	if err != nil {
		t.Fatal(err)
	}
	adminLogin(t, server, client)
	for _, tc := range []struct {
		path, section, label, subpage string
	}{
		{"", "appearance", "Appearance", ""},
		{"/appearance", "appearance", "Appearance", ""},
		{"/git-author", "git-author", "Git author", ""},
		{"/access-tokens", "access-tokens", "Access tokens", ""},
		{"/server", "server", "Server", ""},
		{"/git-remote", "git-remote", "Git remote", ""},
		{"/behaviour", "behaviour", "Behaviour", ""},
		{"/users", "users", "Users", ""},
		{"/advanced-config", "advanced-config", "config.yaml", ""},
		{"/advanced", "advanced", "Environment overrides", ""},
		{"/namespaces", "namespaces", "Namespaces", ""},
		{"/namespaces/new", "namespaces", "Namespaces", "New namespace"},
		{"/namespaces/" + testNS + "/edit", "namespaces", "Namespaces", "Edit " + testNS},
		{"/connections", "connections", "Connected applications", ""},
		{"/oauth-clients", "oauth-clients", "MCP OAuth clients", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := testhttp.Get(t, client, server.URL+"/_/settings"+tc.path)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("settings response = %d: %s", resp.StatusCode, body)
			}
			html := string(body)
			current := `<a href="/_/settings/` + tc.section + `" aria-current="page">`
			for _, want := range []string{
				`class="settings-layout"`, `<div class="settings-selector"><details class="ui-disclosure">`,
				`aria-label="Settings section: ` + tc.label + `"`, `aria-label="Breadcrumb"`,
				`<a href="/_/settings">Settings</a>`, current,
			} {
				if !strings.Contains(html, want) {
					t.Errorf("missing %q", want)
				}
			}
			if strings.Count(html, current) != 2 {
				t.Error("desktop and mobile should highlight the same section")
			}
			breadcrumb := tc.label
			if tc.subpage != "" {
				breadcrumb = tc.subpage
				if !strings.Contains(html, `<a href="/_/settings/namespaces">Namespaces</a>`) {
					t.Error("missing namespace parent breadcrumb")
				}
			}
			if !strings.Contains(html, `<span class="current" aria-current="page">`+breadcrumb+`</span>`) {
				t.Error("incorrect current breadcrumb")
			}
			for _, nav := range regexp.MustCompile(`(?s)<nav class="settings-nav[^"]*"[^>]*>(.*?)</nav>`).FindAllStringSubmatch(html, -1) {
				for _, href := range regexp.MustCompile(`href="([^"]+)"`).FindAllStringSubmatch(nav[1], -1) {
					if !strings.HasPrefix(href[1], "/_/settings/") {
						t.Errorf("non-unified navigation link: %s", href[1])
					}
				}
			}
			if strings.Contains(html, `class="settings-tabs"`) {
				t.Error("old tab strip still present")
			}
			if tc.section == "advanced-config" && !strings.Contains(html, `<div class="settings-advanced"><details class="ui-disclosure" open>`) {
				t.Error("advanced navigation should open on advanced sections")
			}
			if tc.section == "users" {
				for _, panel := range []string{"server", "git-remote", "behaviour", "advanced-config"} {
					if !strings.Contains(html, `data-panel="`+panel+`" hidden`) {
						t.Errorf("inactive %s panel should be hidden server-side", panel)
					}
				}
			}
		})
	}
}

func TestSettingsRetiredPageRoutesDoNotRedirect(t *testing.T) {
	_, server, client := newTestAppFull(t)
	defer server.Close()
	adminLogin(t, server, client)
	for _, path := range []string{"/_/admin", "/_/admin/oauth", "/_/namespaces", "/_/namespaces/new", "/_/namespaces/" + testNS + "/edit", "/_/connections"} {
		resp, err := testhttp.Get(t, client, server.URL+path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Location") != "" {
			t.Errorf("%s = %d, Location %q; want 404 without redirect", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
}
