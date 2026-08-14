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
	md      goldmark.Markdown
	resolve func(title, ns string) (slug string, ok bool)
}

// NewRenderer takes resolve — how a [[title]] wiki-link is turned into a
// slug — rather than a flat existence check, so link resolution can prefer a
// match in the current page's own namespace (see Index.ResolveLink).
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

// Render renders body for an authenticated viewer. ns is the namespace of
// the page being rendered (namespaceFor(slug)), used to scope wiki-link
// resolution — pass "" when there is no page context to resolve against
// (e.g. the raw markdown preview).
func (r *Renderer) Render(body, ns string) (htmltemplate.HTML, error) {
	// Pre-process wiki-links
	body = r.processWikiLinks(body, ns)

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

func (r *Renderer) processWikiLinks(body, ns string) string {
	return WikiLinkOrCodeRE.ReplaceAllStringFunc(body, func(match string) string {
		if !strings.HasPrefix(match, "[[") {
			return match // fenced/inline code — leave untouched, not a real link
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

// RenderPublic renders a page body for an anonymous viewer. isPublicLink
// reports whether a wiki-link's target may be advertised to that viewer — a
// link to anything else (private or nonexistent) unwraps to plain text so no
// private slug or its existence leaks. hmd:toc is deliberately NOT expanded
// here (the caller must not run injectTOC on this body first) since a TOC
// would leak private page titles by construction.
func (r *Renderer) RenderPublic(body, ns string, isPublicLink func(slug string) bool) (htmltemplate.HTML, error) {
	body = WikiLinkOrCodeRE.ReplaceAllStringFunc(body, func(match string) string {
		if !strings.HasPrefix(match, "[[") {
			return match // fenced/inline code — leave untouched, not a real link
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

// RenderStatic renders body for a namespace static export (see export.go).
// hrefFor maps a resolved wiki-link's slug to the exported page's relative
// href; a slug hrefFor doesn't know about (cross-namespace, private, or
// simply not exported) unwraps to plain text, same leakage rule as
// RenderPublic — a static export has no server to check a viewer's auth
// against later, so an unresolvable link must never survive as a live href.
func (r *Renderer) RenderStatic(body, ns string, hrefFor func(slug string) (href string, ok bool)) (htmltemplate.HTML, error) {
	body = WikiLinkOrCodeRE.ReplaceAllStringFunc(body, func(match string) string {
		if !strings.HasPrefix(match, "[[") {
			return match // fenced/inline code — leave untouched, not a real link
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
