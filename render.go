package main

import (
	"bytes"
	"fmt"
	"html"
	htmltemplate "html/template"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
)

// sanitizePolicy strips script/event-handler HTML that goldmark's
// WithUnsafe() would otherwise pass straight through from raw HTML in a
// page body. It extends bluemonday's UGC baseline with the markup our own
// post-processing and goldmark's GFM extensions rely on: wiki-link/mermaid
// classes, footnote anchors, and task-list checkboxes.
var sanitizePolicy = newSanitizePolicy()

func newSanitizePolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("class").OnElements("a", "span", "pre", "code", "div", "li")
	p.AllowAttrs("role").OnElements("a", "div", "sup")
	p.AllowElements("input")
	p.AllowAttrs("type", "checked", "disabled").OnElements("input")
	return p
}

type Renderer struct {
	md     goldmark.Markdown
	exists func(slug string) bool
}

func NewRenderer(exists func(slug string) bool) *Renderer {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			extension.Footnote,
		),
		goldmark.WithRendererOptions(
			goldmarkhtml.WithUnsafe(),
		),
	)
	return &Renderer{
		md:     md,
		exists: exists,
	}
}

func (r *Renderer) Render(body string) (htmltemplate.HTML, error) {
	// Pre-process wiki-links
	body = r.processWikiLinks(body)

	// Render markdown
	var buf bytes.Buffer
	err := r.md.Convert([]byte(body), &buf)
	if err != nil {
		return "", fmt.Errorf("rendering markdown: %w", err)
	}

	// Post-process mermaid blocks
	htmlStr := buf.String()
	htmlStr = r.processMermaidBlocks(htmlStr)
	htmlStr = sanitizePolicy.Sanitize(htmlStr)

	return htmltemplate.HTML(htmlStr), nil
}

func (r *Renderer) processWikiLinks(body string) string {
	return wikiLinkOrCodeRe.ReplaceAllStringFunc(body, func(match string) string {
		if !strings.HasPrefix(match, "[[") {
			return match // fenced/inline code — leave untouched, not a real link
		}
		title := match[2 : len(match)-2]
		slug := Slugify(title)
		escaped := html.EscapeString(title)

		if r.exists(slug) {
			return fmt.Sprintf(`<a class="wiki" href="/page/%s"><span class="br">[[</span>%s<span class="br">]]</span></a>`, slug, escaped)
		}
		return fmt.Sprintf(`<a class="missing wiki" href="/page/%s"><span class="br">[[</span>%s<span class="br">]]</span><span class="missing-suffix">+</span></a>`, slug, escaped)
	})
}

func (r *Renderer) processMermaidBlocks(htmlStr string) string {
	// string post-processing, swap for a goldmark AST extension if it ever misfires
	// Replace <pre><code class="language-mermaid">...</code></pre> with <pre class="mermaid">...</pre>
	re := regexp.MustCompile(`<pre>\s*<code class="language-mermaid">([\s\S]*?)</code>\s*</pre>`)
	return re.ReplaceAllStringFunc(htmlStr, func(match string) string {
		// Use regex to extract the content
		matches := re.FindStringSubmatch(match)
		if len(matches) > 1 {
			// Keep goldmark's HTML-escaping intact: mermaid.js reads
			// textContent, which the browser decodes for us, so
			// re-embedding raw here would only reopen it to injection.
			content := matches[1]
			return fmt.Sprintf(`<pre class="mermaid">%s</pre>`, content)
		}
		return match
	})
}
