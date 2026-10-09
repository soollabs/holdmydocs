package app

import (
	"testing"

	"hmd/internal/wiki"
)

func TestExportCLIPrecedence(t *testing.T) {
	disabled := false
	namespace := wiki.ExportConfig{BaseURL: "https://docs.example.org/manual/", Sitemap: &disabled, RobotsAllow: []string{"Googlebot"}, Links: []wiki.ExportLink{{Label: "Source", URL: "https://github.com/soollabs/holdmydocs"}}}
	c, err := resolveExportSettings(namespace, "https://live.example.org/", exportCLIOptions{Sitemap: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != namespace.BaseURL || c.SitemapEnabled() || len(c.RobotsAllow) != 1 || len(c.Links) != 1 {
		t.Fatalf("lost namespace settings: %+v", c)
	}
	options := exportCLIOptions{BaseURL: "https://preview.example.org/", Sitemap: true, Bots: "", Links: "[]", Provided: map[string]bool{"export-base-url": true, "export-sitemap": true, "export-robots-allow": true, "export-links": true}}
	c, err = resolveExportSettings(namespace, "https://live.example.org/", options)
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != options.BaseURL || !c.SitemapEnabled() || len(c.RobotsAllow) != 0 || len(c.Links) != 0 {
		t.Fatalf("overrides failed: %+v", c)
	}
	if len(namespace.RobotsAllow) != 1 || len(namespace.Links) != 1 || namespace.SitemapEnabled() {
		t.Fatal("mutated namespace settings")
	}
	options.Links = `[{"label":"GitHub","url":"https://github.com/soollabs/holdmydocs","icon":"fa-brands fa-github","location":"topbar","icon_only":true}]`
	c, err = resolveExportSettings(namespace, "", options)
	if err != nil || len(c.Links) != 1 || c.Links[0].Location != "topbar" || !c.Links[0].IconOnly {
		t.Fatalf("link presentation override: %+v, %v", c.Links, err)
	}
	if namespace.Links[0].Label != "Source" {
		t.Fatal("link override mutated namespace")
	}
	c, err = resolveExportSettings(namespace, "https://live.example.org/", exportCLIOptions{UseMain: true})
	if err != nil || c.BaseURL != "https://live.example.org/" {
		t.Fatalf("explicit main URL: %+v, %v", c, err)
	}
	if _, err := resolveExportSettings(wiki.ExportConfig{}, "https://live.example.org/", exportCLIOptions{}); err == nil {
		t.Fatal("silently fell back to main URL")
	}
	options.UseMain = true
	if _, err := resolveExportSettings(namespace, "https://live.example.org/", options); err == nil {
		t.Fatal("accepted conflicting URL flags")
	}
	options.UseMain = false
	options.Links = "not JSON"
	if _, err := resolveExportSettings(namespace, "", options); err == nil {
		t.Fatal("accepted invalid JSON")
	}
}
