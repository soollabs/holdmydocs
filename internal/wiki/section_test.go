package wiki

import "testing"

func TestSections(t *testing.T) {
	body := "Intro\n\n# Guide\n\nText\n\n## Install ##\n\nSteps\n\n```\n# not a heading\n```\n\n> # quoted\n\nSetext\n------\n\nMore\n\n# Later\n\nEnd\n"
	sections := Sections(body)
	want := []struct {
		heading string
		level   int
		text    string
	}{
		{"Guide", 1, "# Guide\n\nText\n\n## Install ##\n\nSteps\n\n```\n# not a heading\n```\n\n> # quoted\n\nSetext\n------\n\nMore\n\n"},
		{"Install", 2, "## Install ##\n\nSteps\n\n```\n# not a heading\n```\n\n> # quoted\n\n"},
		{"Setext", 2, "Setext\n------\n\nMore\n\n"},
		{"Later", 1, "# Later\n\nEnd\n"},
	}
	if len(sections) != len(want) {
		t.Fatalf("Sections = %+v, want %d sections", sections, len(want))
	}
	for i, w := range want {
		got := sections[i]
		if got.Heading != w.heading || got.Level != w.level || body[got.Start:got.End] != w.text {
			t.Errorf("section %d = %q level %d text %q; want %q level %d text %q",
				i, got.Heading, got.Level, body[got.Start:got.End], w.heading, w.level, w.text)
		}
	}
	if section, ok := FindSection(body, "  install "); !ok || section.Heading != "Install" {
		t.Errorf("FindSection ignoring case = %+v, %v", section, ok)
	}
	if _, ok := FindSection(body, "not a heading"); ok {
		t.Error("FindSection matched a heading inside a code block")
	}
}
