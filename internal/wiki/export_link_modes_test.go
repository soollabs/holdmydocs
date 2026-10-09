package wiki

import "testing"

func TestExportLinkModes(t *testing.T) {
	link := ExportLink{Label: "Example", URL: "https://example.org", Icon: "fa-solid fa-heart"}
	top := link
	top.Location = "topbar"
	cfg, err := NormaliseExportConfig(ExportConfig{Links: []ExportLink{top}}, false)
	if err != nil || !cfg.Links[0].IconOnly {
		t.Fatalf("topbar must normalise to icon-only: %+v, %v", cfg, err)
	}
	top.Icon = ""
	if _, err := NormaliseExportConfig(ExportConfig{Links: []ExportLink{top}}, false); err == nil {
		t.Fatal("topbar without an icon accepted")
	}
	icon := link
	icon.IconOnly = true
	if _, err := NormaliseExportConfig(ExportConfig{Links: []ExportLink{link, icon}}, false); err == nil {
		t.Fatal("mixed sidebar modes accepted")
	}
	for _, icons := range []bool{false, true} {
		link.IconOnly = icons
		if _, err := NormaliseExportConfig(ExportConfig{Links: []ExportLink{link, link}}, false); err != nil {
			t.Fatal(err)
		}
	}
}
