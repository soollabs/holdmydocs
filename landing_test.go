package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestLandingRoute checks "/" redirects to Config.LandingSlug(): the home
// page by default, or the configured Landing slug when set — independent of
// skin, since Landing moved from a per-skin enum to a plain config slug.
func TestLandingRoute(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	noRedirectClient := &http.Client{
		Jar: client.Jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	get := func() string {
		resp, err := noRedirectClient.Get(server.URL + "/")
		if err != nil {
			t.Fatalf("GET /: %v", err)
		}
		closeTestBody(t, resp.Body)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303", resp.StatusCode)
		}
		return resp.Header.Get("Location")
	}

	if loc := get(); loc != "/readme" {
		t.Errorf("default Location = %q, want /readme", loc)
	}

	cfg := app.config()
	cfg.Landing = "journal/2026-07-24"
	app.SetConfig(cfg)

	if loc := get(); loc != "/journal/2026-07-24" {
		t.Errorf("configured Landing Location = %q, want /journal/2026-07-24", loc)
	}
}

// TestJournalEnabledJSGlobal checks window.hmdJournalEnabled reflects
// whether the journal namespace has a `new:` template, independent of skin.
func TestJournalEnabledJSGlobal(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()

	get := func() string {
		resp, err := client.Get(server.URL + "/readme")
		if err != nil {
			t.Fatalf("GET /readme: %v", err)
		}
		defer closeTestBody(t, resp.Body)
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	if body := get(); !strings.Contains(body, "hmdJournalEnabled =  false") {
		t.Errorf("expected hmdJournalEnabled false with no journal namespace configured, body: %s", body)
	}

	if err := writeNamespaceConfig(t, app, "journal", "new:\n  template: entry\n  slug: '{{.Now.Format \"2006-01-02\"}}'\n"); err != nil {
		t.Fatalf("writing namespace config: %v", err)
	}

	setSkin := func(name string) {
		resp, err := client.PostForm(server.URL+"/_/settings/appearance", url.Values{"skin": {name}})
		if err != nil {
			t.Fatalf("setting skin %s: %v", name, err)
		}
		closeTestBody(t, resp.Body)
	}
	for _, s := range skinNames {
		setSkin(s)
		if body := get(); !strings.Contains(body, "hmdJournalEnabled =  true") {
			t.Errorf("skin=%s: expected hmdJournalEnabled true once journal has a new: template, body: %s", s, body)
		}
	}
}
