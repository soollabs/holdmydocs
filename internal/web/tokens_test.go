package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"hmd/internal/config"
	"hmd/internal/wiki"
)

var tokenRe = regexp.MustCompile(`hmd_[0-9a-f]{64}`)

func createTokenViaUI(t *testing.T, server *httptest.Server, client *http.Client, label, expiry string, scopes, namespaces []string) string {
	t.Helper()
	values := url.Values{"label": {label}, "expiry": {expiry}, "scopes": scopes, "namespaces": namespaces}
	resp, err := postJSON(t, client, server.URL+"/_/api/settings/tokens", tokenCreateJSON(values))
	if err != nil {
		t.Fatalf("POST /_/api/settings/tokens: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token creation status = %d, want 200", resp.StatusCode)
	}
	var created struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decoding token response: %v", err)
	}
	token := tokenRe.FindString(created.Token)
	if token == "" {
		t.Fatalf("no token in settings response (status %d)", resp.StatusCode)
	}
	return token
}

func TestTokenSettingsUI(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	for _, page := range []wiki.Page{
		{Slug: "notes/allowed", Title: "Allowed", Body: "only notes"},
		{Slug: "private/denied", Title: "Denied", Body: "private"},
	} {
		if _, err := app.Store.Save(wiki.PageFile(page.Slug), page.Encode(), "Add "+page.Slug, "test", "test@hmd.local"); err != nil {
			t.Fatal(err)
		}
		if err := app.Index.Update(page); err != nil {
			t.Fatal(err)
		}
	}
	app.apiClient().RefreshNamespaces()

	token := createTokenViaUI(t, server, client, "laptop", "30d", []string{"read"}, []string{"notes"})
	privateToken := createTokenViaUI(t, server, client, "private-pages", "30d", []string{"read"}, []string{"private"})

	req, _ := http.NewRequest("GET", server.URL+"/_/api/search?q=readme", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Bearer request failed: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Bearer /api/search: got %d, want 200", resp.StatusCode)
	}

	pageReq, _ := http.NewRequest(http.MethodGet, server.URL+"/notes/allowed", nil)
	pageReq.Header.Set("Authorization", "Bearer "+token)
	pageResp, err := http.DefaultClient.Do(pageReq)
	if err != nil {
		t.Fatalf("selected namespace request failed: %v", err)
	}
	closeTestBody(t, pageResp.Body)
	if pageResp.StatusCode != http.StatusOK {
		t.Fatalf("selected namespace request = %d, want 200", pageResp.StatusCode)
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/private/denied", http.StatusOK},
		{"/notes/allowed", http.StatusForbidden},
	} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+privateToken)
		privateResp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("private-only request %s failed: %v", tc.path, err)
		}
		closeTestBody(t, privateResp.Body)
		if privateResp.StatusCode != tc.want {
			t.Errorf("private-only request %s = %d, want %d", tc.path, privateResp.StatusCode, tc.want)
		}
	}

	allNamespacesToken := createTokenViaUI(t, server, client, "all-pages", "30d", []string{"read"}, nil)
	adminToken := createTokenViaUI(t, server, client, "administrator", "30d", []string{"settings"}, []string{"notes"})
	settingsRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/_/settings", nil)
	for _, cookie := range client.Jar.Cookies(settingsRequest.URL) {
		settingsRequest.AddCookie(cookie)
	}
	settingsData := app.settingsData(settingsRequest)

	findToken := func(name string) TokenView {
		t.Helper()
		for _, view := range settingsData.Tokens {
			if view.Name == name {
				return view
			}
		}
		t.Fatalf("settings data missing token %q", name)
		return TokenView{}
	}
	selected := findToken("laptop")
	if len(selected.Scopes) != 1 || selected.Scopes[0] != "read" {
		t.Errorf("selected token scopes = %v, want [read]", selected.Scopes)
	}
	if len(selected.Namespaces) != 1 || selected.Namespaces[0] != "notes" {
		t.Errorf("selected token namespaces = %v, want [notes]", selected.Namespaces)
	}
	if selected.NamespaceLabel != "notes only" {
		t.Errorf("selected token namespace label = %q, want notes only", selected.NamespaceLabel)
	}
	if selected.ScopeLabel != "read" {
		t.Errorf("selected token scope label = %q, want read", selected.ScopeLabel)
	}
	if priv := findToken("private-pages"); len(priv.Namespaces) != 1 || priv.Namespaces[0] != "private" || priv.NamespaceLabel != "private only" {
		t.Errorf("private token = %#v, want private-only namespace access", priv)
	}
	if all := findToken("all-pages"); all.NamespaceLabel != "All namespaces" {
		t.Errorf("unrestricted token namespace label = %q, want All namespaces", all.NamespaceLabel)
	}
	if admin := findToken("administrator"); admin.NamespaceLabel != "Administrator" {
		t.Errorf("settings token namespace label = %q, want Administrator", admin.NamespaceLabel)
	}
	if len(settingsData.TokenNamespaces) != 2 || settingsData.TokenNamespaces[0] != "notes" || settingsData.TokenNamespaces[1] != "private" {
		t.Errorf("token namespace catalogue = %v, want [notes private]", settingsData.TokenNamespaces)
	}

	for _, path := range []string{"/" + testHome, "/private/denied"} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+allNamespacesToken)
		allResp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("all-namespaces request %s failed: %v", path, err)
		}
		closeTestBody(t, allResp.Body)
		if allResp.StatusCode != http.StatusOK {
			t.Errorf("all-namespaces request %s = %d, want 200", path, allResp.StatusCode)
		}
	}

	postInvalid := func(values url.Values) string {
		t.Helper()
		invalidResp, err := postJSON(t, client, server.URL+"/_/api/settings/tokens", tokenCreateJSON(values))
		if err != nil {
			t.Fatalf("POST invalid token: %v", err)
		}
		defer closeTestBody(t, invalidResp.Body)
		body, err := io.ReadAll(invalidResp.Body)
		if err != nil {
			t.Fatalf("read invalid token response: %v", err)
		}
		if invalidResp.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid token response status = %d, want 400", invalidResp.StatusCode)
		}
		return string(body)
	}
	for _, tc := range []struct {
		name   string
		values url.Values
		want   string
	}{
		{"unknown namespace", url.Values{"label": {"bad-namespace"}, "expiry": {"30d"}, "scopes": {"read"}, "namespaces": {"missing"}}, "unknown namespace"},
		{"unknown scope", url.Values{"label": {"bad-scope"}, "expiry": {"30d"}, "scopes": {"unknown"}}, "unknown scope"},
		{"missing scope", url.Values{"label": {"missing-scope"}, "expiry": {"30d"}}, "at least one scope"},
	} {
		body := postInvalid(tc.values)
		if !strings.Contains(body, tc.want) {
			t.Errorf("%s response missing %q: %s", tc.name, tc.want, body)
		}
		if tokenRe.FindString(body) != "" {
			t.Errorf("%s response exposed a token value", tc.name)
		}
	}
	if len(app.Auth.TokensFor("admin")) != 4 {
		t.Errorf("invalid forms created tokens: got %d, want 4", len(app.Auth.TokensFor("admin")))
	}

	for _, path := range []string{"/" + testHome, "/private/denied"} {
		req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		adminResp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("administrator request %s failed: %v", path, err)
		}
		closeTestBody(t, adminResp.Body)
		if adminResp.StatusCode != http.StatusOK {
			t.Errorf("administrator request %s = %d, want 200", path, adminResp.StatusCode)
		}
	}

	dupResp, err := postJSON(t, client, server.URL+"/_/api/settings/tokens", tokenCreateJSON(url.Values{"label": {"laptop"}, "expiry": {"30d"}, "scopes": {"read"}, "namespaces": {"notes"}}))
	if err != nil {
		t.Fatalf("POST duplicate: %v", err)
	}
	dupBody, _ := io.ReadAll(dupResp.Body)
	if err := dupResp.Body.Close(); err != nil {
		t.Fatalf("closing duplicate-token response body: %v", err)
	}
	if !strings.Contains(string(dupBody), "already exists") {
		t.Error("duplicate label accepted, want error")
	}

	settingsResp, _ := client.Get(server.URL + "/_/settings")
	pageBody, _ := io.ReadAll(settingsResp.Body)
	if err := settingsResp.Body.Close(); err != nil {
		t.Fatalf("closing settings response body: %v", err)
	}
	if !strings.Contains(string(pageBody), "laptop") {
		t.Error("settings page missing token row")
	}
	if !strings.Contains(string(pageBody), `name="namespaces" value="`+testNS+`"`) {
		t.Errorf("settings page missing the %s namespace toggle", testNS)
	}
	if !strings.Contains(string(pageBody), time.Now().Add(30*24*time.Hour).Format("2006-01-02")) {
		t.Error("settings page missing 30-day expiry date")
	}

	revokeResp, err := postJSON(t, client, server.URL+"/_/api/settings/tokens/revoke", revokeTokenJSON(url.Values{"label": {"laptop"}}))
	if err != nil {
		t.Fatalf("POST revoke: %v", err)
	}
	if err := revokeResp.Body.Close(); err != nil {
		t.Fatalf("closing revoke response body: %v", err)
	}

	req, _ = http.NewRequest("GET", server.URL+"/_/api/search?q=readme", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Bearer request failed: %v", err)
	}
	closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked Bearer: got %d, want 401", resp.StatusCode)
	}
}

func TestTokenExpiry(t *testing.T) {
	cfg := config.Config{AppDir: t.TempDir(), RepoDir: t.TempDir(), AdminUser: "admin", AdminPass: "password12345"}
	auth, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}

	expired, err := auth.AddToken("admin", "old", time.Now().Add(-time.Minute), []string{"read", "write", "settings"}, nil)
	if err != nil {
		t.Fatalf("AddToken failed: %v", err)
	}
	if _, ok := auth.UserForBearer(expired); ok {
		t.Error("expired token verified, want rejection")
	}

	live, err := auth.AddToken("admin", "live", time.Now().Add(time.Hour), []string{"read", "write", "settings"}, nil)
	if err != nil {
		t.Fatalf("AddToken failed: %v", err)
	}
	if principal, ok := auth.UserForBearer(live); !ok || principal.User != "admin" {
		t.Errorf("live token: got (%#v, %v), want admin, true", principal, ok)
	}

}
