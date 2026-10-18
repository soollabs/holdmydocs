package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var (
	templateAction  = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
	templateScript  = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)>`)
	themeScript     = regexp.MustCompile(`(?s)\{\{define "theme-script"\}\}.*?\{\{end\}\}`)
	templateComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	textBetweenTags = regexp.MustCompile(`>([^<>]+)<`)
	textAttribute   = regexp.MustCompile(`\b(?:placeholder|title|aria-label|alt|label)="([^"]*)"`)
	htmlEntity      = regexp.MustCompile(`&#?\w+;`)
	hasWord         = regexp.MustCompile(`[A-Za-z]{2,}`)
)

// literalAllowed lists template text that is deliberately not translated: key
// names, file names and other technical tokens a translator should not touch.
var literalAllowed = map[string]bool{
	"ctrl j": true, "ctrl k": true, // key names
	".md": true, ".wiki.yaml": true, ".help.md": true, "notes/": true, // file and path examples
	"Googlebot": true, "Bing": true, "DuckDuckGo": true, "Apple": true, // crawler names
	"(client_secret_post)": true, "(client_secret_basic)": true, "(none)": true, // OAuth protocol values
	"https://docs.example.org/": true, "https://client.example/callback": true, // example URLs
	"fa-brands fa-github":     true, // icon class names
	".Now.Format  2006-01-02": true, // slug pattern syntax example, entity-escaped in the template
}

// TestTemplatesHaveNoRawEnglish keeps new user-facing text in the catalogue.
// Text inside {{ }} actions and <script>/<style> blocks is ignored, because
// actions are already translated or are data and scripts read js.* messages.
func TestTemplatesHaveNoRawEnglish(t *testing.T) {
	err := fs.WalkDir(webFS, "web/templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		raw, err := fs.ReadFile(webFS, path)
		if err != nil {
			return err
		}
		src := templateComment.ReplaceAllString(string(raw), "")
		src = themeScript.ReplaceAllString(src, "")
		src = templateScript.ReplaceAllString(src, "<x></x>")
		src = templateAction.ReplaceAllString(src, "")
		check := func(kind, text string) {
			text = strings.TrimSpace(htmlEntity.ReplaceAllString(text, " "))
			if hasWord.MatchString(text) && !literalAllowed[text] {
				t.Errorf("%s: raw English %s %q; add a message and use {{t}}", path, kind, text)
			}
		}
		for _, m := range textBetweenTags.FindAllStringSubmatch(src, -1) {
			check("text", m[1])
		}
		for _, m := range textAttribute.FindAllStringSubmatch(src, -1) {
			check("attribute", m[1])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
