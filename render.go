package main

import (
	"bytes"
	"fmt"
	"html"
	htmltemplate "html/template"
	"regexp"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
)

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

	return htmltemplate.HTML(htmlStr), nil
}

func (r *Renderer) processWikiLinks(body string) string {
	re := regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)
	return re.ReplaceAllStringFunc(body, func(match string) string {
		// Extract the title from [[...]]
		title := match[2 : len(match)-2]
		slug := Slugify(title)

		if r.exists(slug) {
			return fmt.Sprintf(`<a class="wiki" href="/page/%s"><span class="br">[[</span>%s<span class="br">]]</span></a>`, slug, title)
		}
		return fmt.Sprintf(`<a class="missing wiki" href="/page/%s"><span class="br">[[</span>%s<span class="br">]]</span><span class="missing-suffix">+</span></a>`, slug, title)
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
			content := matches[1]
			// HTML-unescape the content
			content = html.UnescapeString(content)
			return fmt.Sprintf(`<pre class="mermaid">%s</pre>`, content)
		}
		return match
	})
}
