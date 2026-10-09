package wiki

import "testing"

func FuzzParsePage(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("---\ntitle: Test\ntags: one, two\n---\nBody"),
		[]byte("---\r\ntitle: <script>\r\n---\r\n[[../../secret]]"),
		{0xff, 0xfe, 0xfd},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		page := ParsePage("notes/fuzz", raw)
		_ = ParsePage(page.Slug, page.Encode())
	})
}

func FuzzRenderWikiLinks(f *testing.F) {
	for _, seed := range []string{"[[Page]]", "`[[code]]`", "[[<script>alert(1)</script>]]", "```\n[[fence]]\n```"} {
		f.Add(seed)
	}
	renderer := NewRenderer(func(string, string) (string, bool) { return "notes/page", true })
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		if _, err := renderer.Render(body, "notes"); err != nil {
			t.Fatal(err)
		}
	})
}

func FuzzValidPageSlug(f *testing.F) {
	for _, seed := range []string{"notes/page", "../secret", "notes/../../secret", "notes/é", "_/admin"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, slug string) { _ = ValidPageSlug(slug) })
}
