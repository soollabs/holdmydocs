package web

import (
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
			closeTestBody(t, resp.Body)

			for _, path := range []string{"/" + testHome, "/" + testHome + "?do=edit", "/_/settings", "/_/inbox", "/_/tags"} {
				resp, err := client.Get(server.URL + path)
				if err != nil {
					t.Fatalf("GET %s: %v", path, err)
				}
				closeTestBody(t, resp.Body)
				if resp.StatusCode >= 500 {
					t.Errorf("skin=%s GET %s status = %d, want < 500", p, path, resp.StatusCode)
				}
			}
		})
	}
}
