package web

import (
	"hmd/internal/testhttp"

	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"hmd/internal/wiki"
)

func TestExportUIAsksBeforeMainURLFallback(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	adminLogin(t, server, client)
	cfg := app.config()
	cfg.BaseURL = "https://live.example.org/"
	app.SetConfig(cfg)
	endpoint := server.URL + "/_/settings/namespaces/" + testNS + "/export"
	resp, err := testhttp.Get(t, client, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Use main base URL and export") || resp.Header.Get("Content-Type") == "application/zip" {
		t.Fatalf("did not ask for fallback: %s", body)
	}
	resp, err = testhttp.Get(t, client, endpoint+"?use-main-base-url=1")
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("export response: %s", body)
	}
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range archive.File {
		if file.Name == "sitemap.xml" {
			f, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(f)
			func() {
				if err := f.Close(); err != nil {
					t.Errorf("closing response body: %v", err)
				}
			}()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "https://live.example.org/") {
				t.Fatalf("sitemap = %s", data)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("sitemap missing")
	}
	if app.Namespaces()[testNS].Export.BaseURL != "" {
		t.Fatal("one-off fallback changed namespace settings")
	}
}

func TestNamespaceExportSettingsSaveAndRender(t *testing.T) {
	app, server, client := newTestAppFull(t)
	defer server.Close()
	adminLogin(t, server, client)
	sitemap := false
	settings := wiki.ExportConfig{BaseURL: "https://docs.example.org/manual/", Sitemap: &sitemap, RobotsAllow: []string{"Googlebot", "CustomBot"}, Links: []wiki.ExportLink{{Label: "GitHub", URL: "https://github.com/soollabs/holdmydocs", Icon: "fa-brands fa-github", Location: "topbar", IconOnly: true}}}
	resp, err := postJSON(t, client, server.URL+"/_/api/namespaces", map[string]any{"name": "docs", "export": settings})
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status %d", resp.StatusCode)
	}
	if c := app.Namespaces()["docs"].Export; c.BaseURL != settings.BaseURL || c.SitemapEnabled() || len(c.Links) != 1 {
		t.Fatalf("saved config = %+v", c)
	}
	if c := app.Namespaces()["docs"].Export.Links[0]; c.Location != "topbar" || !c.IconOnly {
		t.Fatal("link presentation settings not saved")
	}
	resp, err = testhttp.Get(t, client, server.URL+"/_/namespaces/docs/edit")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"https://docs.example.org/manual/", "Googlebot, CustomBot", "fa-brands fa-github", `id="export-add-link"`, `value="topbar" selected`, `class="export-link-icon-only" checked`, "Labels are shown as link text", "namespace-preview.css"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("editor missing %q", want)
		}
	}
	if strings.Contains(string(body), `name="export_sitemap" type="checkbox" checked`) {
		t.Fatal("disabled sitemap checked")
	}
	if !strings.Contains(string(body), `<style nonce=`) || strings.Contains(string(body), `<style>html,body`) {
		t.Fatal("namespace preview stylesheet must carry the page CSP nonce")
	}
}
