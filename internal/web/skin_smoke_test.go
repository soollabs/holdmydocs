package web

import (
	"hmd/internal/testhttp"

	"net/url"
	"testing"
)

// TestAllSkinsRenderWithoutError ensures core routes render for every skin.
func TestAllSkinsRenderWithoutError(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	for _, p := range skinNames {
		t.Run(p, func(t *testing.T) {
			resp, err := postJSON(t, client, server.URL+"/_/api/settings/appearance", appearanceJSON(url.Values{"skin": {p}}))
			if err != nil {
				t.Fatalf("setting skin: %v", err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Errorf("closing response body: %v", err)
			}

			for _, path := range []string{"/" + testHome, "/" + testHome + "?do=edit", "/_/settings", "/_/inbox", "/_/tags"} {
				resp, err := testhttp.Get(t, client, server.URL+path)
				if err != nil {
					t.Fatalf("GET %s: %v", path, err)
				}
				if err := resp.Body.Close(); err != nil {
					t.Errorf("closing response body: %v", err)
				}
				if resp.StatusCode >= 500 {
					t.Errorf("skin=%s GET %s status = %d, want < 500", p, path, resp.StatusCode)
				}
			}
		})
	}
}
