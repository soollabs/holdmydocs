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
		resp, err := client.PostForm(server.URL+"/_/settings/appearance", form)
		if err != nil {
			t.Fatalf("setting skin %s: %v", name, err)
		}
		closeTestBody(t, resp.Body)
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
		{"phosphor", "/readme"},
		{"newsprint", "/readme"},
		{"soft", "/readme"},
		{"bare", "/readme"},
		{"journal", "/daily/" + today + "?do=edit"},
	}
	for _, tt := range tests {
		t.Run(tt.skin, func(t *testing.T) {
			setSkin(tt.skin)
			resp, err := noRedirectClient.Get(server.URL + "/")
			if err != nil {
				t.Fatalf("GET /: %v", err)
			}
			closeTestBody(t, resp.Body)
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
		resp, err := client.PostForm(server.URL+"/_/settings/appearance", url.Values{"skin": {name}})
		if err != nil {
			t.Fatalf("setting skin %s: %v", name, err)
		}
		closeTestBody(t, resp.Body)
	}
	get := func(path string) string {
		resp, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer closeTestBody(t, resp.Body)
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
			body := get("/readme")
			if !strings.Contains(body, tt.want) {
				t.Errorf("skin=%s: expected %q in page, not found", tt.skin, tt.want)
			}
		})
	}
}
