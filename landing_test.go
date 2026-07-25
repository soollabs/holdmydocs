package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestLandingRouteByProfile checks "/" redirects to each profile's Landing
// target: docs/research/minimal -> home page, journal -> today's daily
// entry in edit mode, clipper -> /inbox.
func TestLandingRouteByProfile(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	setProfile := func(name string) {
		form := url.Values{"profile": {name}}
		resp, err := client.PostForm(server.URL+"/settings/appearance", form)
		if err != nil {
			t.Fatalf("setting profile %s: %v", name, err)
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
		profile  string
		wantPath string
	}{
		{"docs", "/page/readme"},
		{"research", "/page/readme"},
		{"minimal", "/page/readme"},
		{"journal", "/page/daily/" + today + "/edit"},
		{"clipper", "/inbox"},
	}
	for _, tt := range tests {
		t.Run(tt.profile, func(t *testing.T) {
			setProfile(tt.profile)
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
				t.Errorf("profile=%s: Location = %q, want %q", tt.profile, loc, tt.wantPath)
			}
		})
	}
}

// TestDailyEnabledJSGlobal checks the window.hmdDailyEnabled flag rendered
// into the page matches each profile's DailyKey (clipper/minimal have none,
// so ctrl-j and the >daily palette verb must be disabled client-side).
func TestDailyEnabledJSGlobal(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	setProfile := func(name string) {
		resp, err := client.PostForm(server.URL+"/settings/appearance", url.Values{"profile": {name}})
		if err != nil {
			t.Fatalf("setting profile %s: %v", name, err)
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
		profile string
		want    string
	}{
		{"docs", "hmdDailyEnabled =  true"},
		{"research", "hmdDailyEnabled =  true"},
		{"journal", "hmdDailyEnabled =  true"},
		{"clipper", "hmdDailyEnabled =  false"},
		{"minimal", "hmdDailyEnabled =  false"},
	}
	for _, tt := range tests {
		t.Run(tt.profile, func(t *testing.T) {
			setProfile(tt.profile)
			body := get("/page/readme")
			if !strings.Contains(body, tt.want) {
				t.Errorf("profile=%s: expected %q in page, not found", tt.profile, tt.want)
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
