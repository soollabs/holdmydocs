package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"hmd/internal/config"
	"hmd/internal/testhttp"
)

// ownSettingsPages are the settings pages every signed-in user may open.
var ownSettingsPages = []string{"/_/settings", "/_/settings/appearance", "/_/settings/git-author", "/_/settings/access-tokens"}

// signIn adds a user with the given scopes and returns a client holding their
// session.
func signIn(t *testing.T, app *testApp, serverURL, name string, scopes ...string) *http.Client {
	t.Helper()
	if name != "admin" {
		if err := app.Auth.AddUserWithScopes(name, "password12345", scopes); err != nil {
			t.Fatalf("adding %s: %v", name, err)
		}
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := testhttp.PostForm(t, client, serverURL+"/_/login", url.Values{"username": {name}, "password": {"password12345"}})
	if err != nil {
		t.Fatalf("signing in %s: %v", name, err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	return client
}

// fetch returns a response's status and body.
func fetch(t *testing.T, client *http.Client, method, target string, cookies ...*http.Cookie) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	body, readErr := io.ReadAll(resp.Body)
	if err := resp.Body.Close(); err != nil || readErr != nil {
		t.Fatalf("reading %s: %v, %v", target, readErr, err)
	}
	return resp.StatusCode, string(body)
}

var hrefPattern = regexp.MustCompile(`href="([^"#]+)`)

// linksIn returns the links between the first start marker and the next end
// marker after it.
func linksIn(t *testing.T, body, start, end string) []string {
	t.Helper()
	_, rest, ok := strings.Cut(body, start)
	if !ok {
		t.Fatalf("page has no %s", start)
	}
	rest, _, _ = strings.Cut(rest, end)
	var links []string
	for _, m := range hrefPattern.FindAllStringSubmatch(rest, -1) {
		links = append(links, m[1])
	}
	return links
}

// An ordinary user sees Settings in the top-bar menu and every link there
// and in the settings sidebar opens for them. The server panels are neither
// linked nor reachable, and their own saves work.
func TestOwnSettingsOpenToEveryUser(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()
	users := map[string][]string{"writer": {"read", "write"}, "reader": {"read"}}
	for name, scopes := range users {
		t.Run(name, func(t *testing.T) {
			client := signIn(t, app, server.URL, name, scopes...)
			_, page := fetch(t, client, http.MethodGet, server.URL+"/"+testHome)
			menu := linksIn(t, page, `<div class="topbar-menu-items">`, `</div>`)
			if !slices.Contains(menu, "/_/settings") {
				t.Errorf("top-bar menu has no Settings link: %v", menu)
			}
			_, settings := fetch(t, client, http.MethodGet, server.URL+"/_/settings")
			sidebar := linksIn(t, settings, `<nav class="settings-nav"`, `</nav>`)
			for _, link := range append(menu, sidebar...) {
				if rest, ok := strings.CutPrefix(link, "/_/settings/"); ok && !slices.Contains(ownSettingsPages, link) && rest != "connections" {
					t.Errorf("links to an administrator panel %s", link)
				}
				if status, _ := fetch(t, client, http.MethodGet, server.URL+link); status != http.StatusOK {
					t.Errorf("GET %s = %d, want 200", link, status)
				}
			}
			for _, path := range ownSettingsPages {
				if status, _ := fetch(t, client, http.MethodGet, server.URL+path); status != http.StatusOK {
					t.Errorf("GET %s = %d, want 200", path, status)
				}
			}
			for _, path := range []string{"/_/settings/server", "/_/settings/users"} {
				if status, _ := fetch(t, client, http.MethodGet, server.URL+path); status != http.StatusForbidden {
					t.Errorf("GET %s = %d, want 403", path, status)
				}
			}
			saves := []struct {
				path string
				body any
				want int
			}{
				{"/_/api/settings/users", map[string]any{"name": "intruder", "password": "password12345", "scopes": []string{"admin"}}, http.StatusForbidden},
				{"/_/api/settings/appearance", map[string]any{"skin": "journal"}, http.StatusOK},
				{"/_/api/settings/author", map[string]any{"git_author": "Someone <someone@example.com>"}, http.StatusOK},
				{"/_/api/settings/tokens", map[string]any{"label": "laptop", "scopes": []string{"read"}}, http.StatusOK},
				{"/_/api/settings/tokens", map[string]any{"label": "admin", "scopes": []string{"admin"}}, http.StatusBadRequest},
				{"/_/api/settings/tokens/revoke", map[string]any{"label": "laptop"}, http.StatusOK},
			}
			for _, save := range saves {
				resp, err := postJSON(t, client, server.URL+save.path, save.body)
				if err != nil {
					t.Fatal(err)
				}
				if err := resp.Body.Close(); err != nil {
					t.Errorf("closing response body: %v", err)
				}
				if resp.StatusCode != save.want {
					t.Errorf("POST %s %v = %d, want %d", save.path, save.body, resp.StatusCode, save.want)
				}
			}
			if tokens := app.Auth.TokensFor(name); len(tokens) != 0 {
				t.Errorf("%s still holds tokens %+v", name, tokens)
			}
			if app.Auth.UserExists("intruder") {
				t.Error("an ordinary user created a user")
			}
			// The token form offers the scopes the user holds, and preferences
			// to everyone.
			_, tokensPage := fetch(t, client, http.MethodGet, server.URL+"/_/settings/access-tokens")
			for _, scope := range []string{"read", "write", "preferences", "admin"} {
				offered := strings.Contains(tokensPage, `name="scopes" value="`+scope+`"`)
				if offered != (scope == "preferences" || slices.Contains(scopes, scope)) {
					t.Errorf("token form offers %s = %v, user scopes %v", scope, offered, scopes)
				}
			}

			// A token changes the account only with the preferences scope.
			for tokenScopes, want := range map[string]int{"read": http.StatusForbidden, "read preferences": http.StatusOK} {
				token, err := app.Auth.AddToken(name, "script-"+strings.ReplaceAll(tokenScopes, " ", "-"), time.Time{}, strings.Fields(tokenScopes), nil)
				if err != nil {
					t.Fatal(err)
				}
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/_/api/settings/author", strings.NewReader(`{"git_author":"Script <script@example.com>"}`))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+token)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				if err := resp.Body.Close(); err != nil {
					t.Errorf("closing response body: %v", err)
				}
				if resp.StatusCode != want {
					t.Errorf("%s token POST author = %d, want %d", tokenScopes, resp.StatusCode, want)
				}
			}
		})
	}
}

// On a read-only instance ordinary users still open their own settings, but
// the git author and token panels are view-only, without the environment
// variable only administrators can act on.
func TestOwnSettingsViewOnlyWhenReadOnly(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()
	client := signIn(t, app, server.URL, "writer", "read", "write")
	cfg := app.config()
	cfg.ReadOnly = true
	app.SetConfig(cfg)
	_, page := fetch(t, client, http.MethodGet, server.URL+"/"+testHome)
	if !slices.Contains(linksIn(t, page, `<div class="topbar-menu-items">`, `</div>`), "/_/settings") {
		t.Error("top-bar menu has no Settings link on a read-only instance")
	}
	for _, path := range ownSettingsPages {
		status, body := fetch(t, client, http.MethodGet, server.URL+path)
		if status != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, status)
		}
		viewOnly := path == "/_/settings/git-author" || path == "/_/settings/access-tokens"
		if got := strings.Contains(body, `<fieldset class="settings-view-only" disabled>`); got != viewOnly {
			t.Errorf("GET %s view-only = %v, want %v", path, got, viewOnly)
		}
		if strings.Contains(body, "HMD_SERVER_READ_ONLY") {
			t.Errorf("GET %s names the read-only environment variable to an ordinary user", path)
		}
	}
	resp, err := postJSON(t, client, server.URL+"/_/api/settings/author", map[string]any{"git_author": "Someone <someone@example.com>"})
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("read-only POST author = %d, want 403", resp.StatusCode)
	}
}

// forcedConditions turns every administrator banner condition on.
type forcedConditions struct{}

func (forcedConditions) PendingRestart() []config.Setting {
	for _, s := range config.Settings() {
		if s.Restart {
			return []config.Setting{s}
		}
	}
	panic("no restart-only setting")
}
func (forcedConditions) LegacyAttachments() bool { return true }
func (forcedConditions) HelpDrifted() bool       { return true }
func (forcedConditions) IndexFailures() int      { return 2 }
func (forcedConditions) SyncState() (string, string, int64) {
	return "failed", "push to origin failed", 0
}

// With every instance problem forced on, only administrators see the
// banners about them, while everyone who can save their appearance still
// sees their own unsaved-appearance banner.
func TestBannerAudiences(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()
	app.conditions = forcedConditions{}
	adminBanners := []string{`data-dismiss="sync-failed"`, `data-dismiss="index-failed"`, `data-dismiss="restart:`, `data-dismiss="legacy-attachments"`, `data-dismiss="help-drifted"`}
	users := []struct {
		name   string
		scopes []string
		admin  bool
	}{
		{"admin", nil, true},
		{"writer", []string{"read", "write"}, false},
		{"reader", []string{"read"}, false},
	}
	for _, user := range users {
		t.Run(user.name, func(t *testing.T) {
			client := signIn(t, app, server.URL, user.name, user.scopes...)
			draft := url.Values{"user": {user.name}, "base": {appearanceBase(app.Auth.Prefs(user.name))}, "skin": {"newsprint"}, "palette": {"nord"}}.Encode()
			status, body := fetch(t, client, http.MethodGet, server.URL+"/"+testHome, &http.Cookie{Name: appearanceCookie, Value: draft})
			if status != http.StatusOK {
				t.Fatalf("GET page = %d", status)
			}
			for _, banner := range adminBanners {
				if got := strings.Contains(body, banner); got != user.admin {
					t.Errorf("banner %s shown = %v, want %v", banner, got, user.admin)
				}
			}
			if tag := draftNotice(t, body); strings.Contains(tag, "hidden") {
				t.Errorf("unsaved appearance banner is hidden: %s", tag)
			}
		})
	}
}
