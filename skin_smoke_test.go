package main

import (
	"net/url"
	"testing"
)

// TestAllSkinsRenderWithoutError smoke-tests every skin against the
// core routes (view, edit, settings, inbox) — a substitute for the visual
// screenshot review the spec calls for, which needs a browser this
// environment doesn't have. It only proves nothing 500s; it does not
// verify layout, spacing, or overflow at any width.
func TestAllSkinsRenderWithoutError(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	for _, p := range skinNames {
		t.Run(p, func(t *testing.T) {
			resp, err := client.PostForm(server.URL+"/settings/appearance", url.Values{"skin": {p}})
			if err != nil {
				t.Fatalf("setting skin: %v", err)
			}
			closeTestBody(t, resp.Body)

			for _, path := range []string{"/page/readme", "/page/readme/edit", "/settings", "/inbox", "/tags"} {
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
