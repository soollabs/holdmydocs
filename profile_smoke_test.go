package main

import (
	"net/url"
	"testing"
)

// TestAllProfilesRenderWithoutError smoke-tests every profile against the
// core routes (view, edit, settings, inbox) — a substitute for the visual
// screenshot review the spec calls for, which needs a browser this
// environment doesn't have. It only proves nothing 500s; it does not
// verify layout, spacing, or overflow at any width.
func TestAllProfilesRenderWithoutError(t *testing.T) {
	server, client := newTestApp(t)
	defer server.Close()

	for _, p := range profileNames {
		t.Run(p, func(t *testing.T) {
			resp, err := client.PostForm(server.URL+"/settings/appearance", url.Values{"profile": {p}})
			if err != nil {
				t.Fatalf("setting profile: %v", err)
			}
			resp.Body.Close()

			for _, path := range []string{"/page/readme", "/page/readme/edit", "/settings", "/inbox", "/tags"} {
				resp, err := client.Get(server.URL + path)
				if err != nil {
					t.Fatalf("GET %s: %v", path, err)
				}
				resp.Body.Close()
				if resp.StatusCode >= 500 {
					t.Errorf("profile=%s GET %s status = %d, want < 500", p, path, resp.StatusCode)
				}
			}
		})
	}
}
