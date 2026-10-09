package wiki

import "testing"

func TestExportConfigValidation(t *testing.T) {
	for _, raw := range []string{"relative", "//example.org/", "ftp://example.org/", "https://user:pass@example.org/", "https://example.org/?a=1", "https://example.org/#anchor", "https://example.org/\nDisallow: /"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := NormaliseExportConfig(ExportConfig{BaseURL: raw}, false); err == nil {
				t.Fatal("accepted invalid URL")
			}
		})
	}
	for _, bot := range []string{"*", "Googlebot\nAllow: /", "bot:evil", "bot name"} {
		if _, err := NormaliseExportConfig(ExportConfig{RobotsAllow: []string{bot}}, false); err == nil {
			t.Errorf("accepted bot %q", bot)
		}
	}
	if _, err := NormaliseExportConfig(ExportConfig{RobotsAllow: []string{"Googlebot", "googlebot"}}, false); err == nil {
		t.Fatal("accepted duplicate bot")
	}
	for _, link := range []ExportLink{
		{Label: "Bad", URL: "javascript:alert(1)"},
		{Label: "Bad", URL: "https://user:pass@example.org/"},
		{URL: "https://example.org/"},
		{Label: "Bad", URL: "https://example.org/", Icon: `fa-brands fa-github" onclick="evil`},
		{Label: "Bad", URL: "https://example.org/", IconOnly: true},
		{Label: "Bad", URL: "https://example.org/", Location: "footer"},
	} {
		if _, err := NormaliseExportConfig(ExportConfig{Links: []ExportLink{link}}, false); err == nil {
			t.Errorf("accepted link %#v", link)
		}
	}
	if _, err := NormaliseExportConfig(ExportConfig{}, true); err == nil {
		t.Fatal("missing URL accepted for export")
	}
}

func TestExportNamespaceYAMLRoundTrip(t *testing.T) {
	raw := []byte("export:\n  base_url: https://docs.example.org/manual\n  sitemap: false\n  robots_allow: [Googlebot, CustomBot]\n  links:\n    - label: GitHub\n      url: https://github.com/soollabs/holdmydocs\n      icon: fa-brands fa-github\n      location: topbar\n      icon_only: true\n")
	c, err := ParseNamespaceConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	c, err = NormaliseNamespaceConfigBase("docs", c)
	if err != nil {
		t.Fatal(err)
	}
	if c.Export.BaseURL != "https://docs.example.org/manual/" || c.Export.SitemapEnabled() {
		t.Fatalf("config = %+v", c.Export)
	}
	encoded, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseNamespaceConfig(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if again.Export.SitemapEnabled() || len(again.Export.Links) != 1 || again.Export.Links[0].Icon != "fa-brands fa-github" {
		t.Fatalf("round trip = %+v", again.Export)
	}
	if again.Export.Links[0].Location != "topbar" || !again.Export.Links[0].IconOnly {
		t.Fatal("link presentation fields lost")
	}
	if !DefaultNamespaceConfig().Export.SitemapEnabled() {
		t.Fatal("sitemap should default to enabled")
	}
}
