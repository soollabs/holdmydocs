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
	textAttribute   = regexp.MustCompile(`\b(?:placeholder|title|aria-label|aria-description|alt|label|data-confirm)="([^"]*)"`)
	// actionText finds literal text passed to component helpers inside {{ }}:
	// a button's label and label-like component properties.
	actionText = regexp.MustCompile(`\bbutton\s+"([^"]*)"|"(?:label|title|aria-label|placeholder|alt)"\s+"([^"]*)"`)
	htmlEntity = regexp.MustCompile(`&#?\w+;`)
	hasWord    = regexp.MustCompile(`[A-Za-z]{2,}`)
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
// Inside {{ }} actions only literal component labels are checked, since other
// action text is translated or is data; <script> blocks are covered by
// TestScriptsHaveNoRawEnglish.
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
		check := func(kind, text string) {
			text = strings.TrimSpace(htmlEntity.ReplaceAllString(text, " "))
			if hasWord.MatchString(text) && !literalAllowed[text] {
				t.Errorf("%s: raw English %s %q; add a message and use {{t}}", path, kind, text)
			}
		}
		for _, m := range actionText.FindAllStringSubmatch(src, -1) {
			check("component label", m[1]+m[2])
		}
		src = templateAction.ReplaceAllString(src, "")
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

var (
	inlineScript = regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script>`)
	// scriptText finds a quoted string with words that a script shows to a
	// person: assigned as element text or a label, or passed to alert/confirm.
	scriptText = regexp.MustCompile(`(?:\.(?:textContent|innerText|title|placeholder|ariaLabel)\s*=|\b(?:alert|confirm)\(|setAttribute\(\s*'(?:aria-label|aria-description|title|placeholder)'\s*,)\s*(?:'([^']*)'|"([^"]*)"|` + "`" + `([^` + "`" + `]*)` + "`" + `)`)
)

// vendoredScripts are third-party bundles whose text is not ours to translate
// (CodeMirror's own text goes through js.editor.* phrases instead).
var vendoredScripts = map[string]bool{"editor.js": true, "mermaid.min.js": true}

// TestScriptsHaveNoRawEnglish keeps text that browser scripts show in the
// catalogue: static scripts and inline template scripts must read it through
// window.hmdT (or {{t}} in a template) rather than a quoted English string.
func TestScriptsHaveNoRawEnglish(t *testing.T) {
	sources := map[string]string{}
	statics, err := fs.Glob(webFS, "web/static/*.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range statics {
		if vendoredScripts[strings.TrimPrefix(path, "web/static/")] {
			continue
		}
		raw, err := fs.ReadFile(webFS, path)
		if err != nil {
			t.Fatal(err)
		}
		sources[path] = string(raw)
	}
	err = fs.WalkDir(webFS, "web/templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		raw, err := fs.ReadFile(webFS, path)
		if err != nil {
			return err
		}
		for _, m := range inlineScript.FindAllStringSubmatch(string(raw), -1) {
			sources[path] += m[1] + "\n"
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) < 5 {
		t.Fatalf("only %d script sources found", len(sources))
	}
	for path, src := range sources {
		for _, m := range scriptText.FindAllStringSubmatch(src, -1) {
			text := strings.TrimSpace(m[1] + m[2] + m[3])
			if hasWord.MatchString(text) && !literalAllowed[text] {
				t.Errorf("%s: raw English %q shown by a script; add a js.* message and use window.hmdT", path, text)
			}
		}
	}
}
