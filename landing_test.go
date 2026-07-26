package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestLandingRouteBySkin checks "/" redirects to each skin's Landing
// target: everything but journal goes to the home page; journal goes to
// today's daily entry in edit mode.
func TestLandingRouteBySkin(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	setSkin := func(name string) {
		form := url.Values{"skin": {name}}
		resp, err := client.PostForm(server.URL+"/settings/appearance", form)
		if err != nil {
			t.Fatalf("setting skin %s: %v", name, err)
		}
		resp.Body.Close()
	}

	noRedirectClient := &http.Client{
		Jar: client.Jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	today := time.Now().Format("2006-01-02")
	tests := []struct {
		skin     string
		wantPath string
	}{
		{"phosphor", "/page/readme"},
		{"newsprint", "/page/readme"},
		{"soft", "/page/readme"},
		{"bare", "/page/readme"},
		{"journal", "/page/daily/" + today + "/edit"},
	}
	for _, tt := range tests {
		t.Run(tt.skin, func(t *testing.T) {
			setSkin(tt.skin)
			resp, err := noRedirectClient.Get(server.URL + "/")
			if err != nil {
				t.Fatalf("GET /: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303", resp.StatusCode)
			}
			loc := resp.Header.Get("Location")
			if loc != tt.wantPath {
				t.Errorf("skin=%s: Location = %q, want %q", tt.skin, loc, tt.wantPath)
			}
		})
	}
}

// TestDailyEnabledJSGlobal checks the window.hmdDailyEnabled flag rendered
// into the page matches each skin's DailyKey (bare has none, so ctrl-j and
// the >daily palette verb must be disabled client-side there).
func TestDailyEnabledJSGlobal(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	setSkin := func(name string) {
		resp, err := client.PostForm(server.URL+"/settings/appearance", url.Values{"skin": {name}})
		if err != nil {
			t.Fatalf("setting skin %s: %v", name, err)
		}
		resp.Body.Close()
	}
	get := func(path string) string {
		resp, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	tests := []struct {
		skin string
		want string
	}{
		{"phosphor", "hmdDailyEnabled =  true"},
		{"newsprint", "hmdDailyEnabled =  true"},
		{"soft", "hmdDailyEnabled =  true"},
		{"journal", "hmdDailyEnabled =  true"},
		{"bare", "hmdDailyEnabled =  false"},
	}
	for _, tt := range tests {
		t.Run(tt.skin, func(t *testing.T) {
			setSkin(tt.skin)
			body := get("/page/readme")
			if !strings.Contains(body, tt.want) {
				t.Errorf("skin=%s: expected %q in page, not found", tt.skin, tt.want)
			}
		})
	}
}

func TestInboxIndexRendersUnreadPages(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	authorName, authorEmail := app.gitAuthor("admin")
	page := Page{Slug: "clip-a", Title: "Clip A", Unread: true, Source: "https://example.com/a", Body: "x"}
	if _, err := app.Store.Save("clip-a.md", page.Encode(), "seed", authorName, authorEmail); err != nil {
		t.Fatalf("seeding page: %v", err)
	}
	app.Index.Update(page)

	resp, err := client.Get(server.URL + "/inbox")
	if err != nil {
		t.Fatalf("GET /inbox: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
