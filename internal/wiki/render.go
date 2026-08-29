package wiki

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

var sanitizePolicy = newSanitizePolicy()

func newSanitizePolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("class").OnElements("a", "span", "pre", "code", "div", "li")
	p.AllowAttrs("role").OnElements("a", "div", "sup")
	p.AllowElements("input")
	p.AllowAttrs("type", "checked", "disabled").OnElements("input")
	// UGC's img alt policy is Matching(Paragraph): a value containing any
	// character outside letters/numbers/space/-_',[]!.\() silently drops the
	// whole attribute, so author-written alt text like "+ / ? / —" is lost.
	// Allow alt outright — the HTML tokenizer and bluemonday's output
	// escaping already prevent attribute breakout, so there is no injection
	// surface beyond what a plain-text alt policy would allow.
	p.AllowAttrs("alt").OnElements("img")
	return p
}

type Renderer struct {
	md      goldmark.Markdown
	resolve func(title, ns string) (slug string, ok bool)
}

// NewRenderer creates a renderer with a namespace-aware wiki-link resolver.
func NewRenderer(resolve func(title, ns string) (slug string, ok bool)) *Renderer {
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
		md:      md,
		resolve: resolve,
	}
}

// Render renders body for an authenticated viewer using the page's namespace for link resolution.
func (r *Renderer) Render(body, ns string) (htmltemplate.HTML, error) {
	body = r.processWikiLinks(body, ns)

	var buf bytes.Buffer
	err := r.md.Convert([]byte(body), &buf)
	if err != nil {
		return "", fmt.Errorf("rendering markdown: %w", err)
	}

	htmlStr := buf.String()
	htmlStr = r.processMermaidBlocks(htmlStr)
	htmlStr = sanitizePolicy.Sanitize(htmlStr)

	return htmltemplate.HTML(htmlStr), nil
}

func (r *Renderer) processWikiLinks(body, ns string) string {
	return WikiLinkOrCodeRE.ReplaceAllStringFunc(body, func(match string) string {
		if !strings.HasPrefix(match, "[[") {
			return match
		}
		title := match[2 : len(match)-2]
		escaped := html.EscapeString(title)

		if slug, ok := r.resolve(title, ns); ok {
			return fmt.Sprintf(`<a class="wiki" href="/%s"><span class="br">[[</span>%s<span class="br">]]</span></a>`, slug, escaped)
		}
		// No page has this title yet: guess a slug inside the current
		// namespace so following the link to create the page starts it in the
		// right place. Without a namespace there is nowhere to file it, so the
		// link stays unresolved rather than pointing at a slug that can't exist.
		if ns == "" {
			return fmt.Sprintf(`<span class="missing wiki"><span class="br">[[</span>%s<span class="br">]]</span></span>`, escaped)
		}
		slug := NamespaceSlug(ns, Slugify(title))
		return fmt.Sprintf(`<a class="missing wiki" href="/%s"><span class="br">[[</span>%s<span class="br">]]</span><span class="missing-suffix">+</span></a>`, slug, escaped)
	})
}

// RenderPublic renders a page body for an anonymous viewer, hiding private and nonexistent link targets.
func (r *Renderer) RenderPublic(body, ns string, isPublicLink func(slug string) bool) (htmltemplate.HTML, error) {
	body = WikiLinkOrCodeRE.ReplaceAllStringFunc(body, func(match string) string {
		if !strings.HasPrefix(match, "[[") {
			return match
		}
		title := match[2 : len(match)-2]
		escaped := html.EscapeString(title)
		slug, ok := r.resolve(title, ns)
		if ok && isPublicLink(slug) {
			return fmt.Sprintf(`<a class="wiki" href="/%s">%s</a>`, slug, escaped)
		}
		return escaped
	})

	var buf bytes.Buffer
	if err := r.md.Convert([]byte(body), &buf); err != nil {
		return "", fmt.Errorf("rendering markdown: %w", err)
	}
	htmlStr := r.processMermaidBlocks(buf.String())
	htmlStr = sanitizePolicy.Sanitize(htmlStr)
	return htmltemplate.HTML(htmlStr), nil
}

// RenderStatic renders body for a namespace export, hiding links to pages outside the export.
func (r *Renderer) RenderStatic(body, ns string, hrefFor func(slug string) (href string, ok bool)) (htmltemplate.HTML, error) {
	body = WikiLinkOrCodeRE.ReplaceAllStringFunc(body, func(match string) string {
		if !strings.HasPrefix(match, "[[") {
			return match
		}
		title := match[2 : len(match)-2]
		escaped := html.EscapeString(title)
		slug, ok := r.resolve(title, ns)
		if !ok {
			return escaped
		}
		href, ok := hrefFor(slug)
		if !ok {
			return escaped
		}
		return fmt.Sprintf(`<a class="wiki" href="%s">%s</a>`, href, escaped)
	})

	var buf bytes.Buffer
	if err := r.md.Convert([]byte(body), &buf); err != nil {
		return "", fmt.Errorf("rendering markdown: %w", err)
	}
	htmlStr := r.processMermaidBlocks(buf.String())
	htmlStr = sanitizePolicy.Sanitize(htmlStr)
	return htmltemplate.HTML(htmlStr), nil
}

func (r *Renderer) processMermaidBlocks(htmlStr string) string {
	re := regexp.MustCompile(`<pre>\s*<code class="language-mermaid">([\s\S]*?)</code>\s*</pre>`)
	return re.ReplaceAllStringFunc(htmlStr, func(match string) string {
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
