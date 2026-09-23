package wiki

import (
	"bytes"
	"fmt"
	"html"
	htmltemplate "html/template"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark/v2/extension"
	"github.com/yuin/goldmark/v2/parser"
	goldmarkhtml "github.com/yuin/goldmark/v2/renderer/html"
)

var sanitizePolicy = newSanitizePolicy()

func newSanitizePolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("class").OnElements("a", "span", "pre", "code", "div", "li")
	p.AllowAttrs("role").OnElements("a", "div", "sup")
	p.AllowElements("input")
	p.AllowAttrs("type", "checked", "disabled").OnElements("input")
	// Allow plain-text alt text without UGC's restrictive character matching.
	p.AllowAttrs("alt").OnElements("img")
	return p
}

type Renderer struct {
	parser  parser.Parser
	html    goldmarkhtml.Renderer
	resolve func(title, ns string) (slug string, ok bool)
}

// NewRenderer creates a renderer with a namespace-aware wiki-link resolver.
func NewRenderer(resolve func(title, ns string) (slug string, ok bool)) *Renderer {
	return &Renderer{
		parser: parser.New(parser.WithExtensions(
			extension.GFMParser,
			extension.FootnoteParser,
		)),
		html: goldmarkhtml.New(
			goldmarkhtml.WithUnsafe(),
			goldmarkhtml.WithExtensions(
				extension.GFMHTMLRenderer,
				extension.FootnoteHTMLRenderer,
			),
		),
		resolve: resolve,
	}
}

func (r *Renderer) convert(body string) (string, error) {
	source := []byte(body)
	doc := r.parser.Parse(source)
	var buf bytes.Buffer
	if err := r.html.Render(&buf, source, doc); err != nil {
		return "", fmt.Errorf("rendering markdown: %w", err)
	}
	return sanitizePolicy.Sanitize(r.processMermaidBlocks(buf.String())), nil
}

// Render renders body for an authenticated viewer using the page's namespace for link resolution.
func (r *Renderer) Render(body, ns string) (htmltemplate.HTML, error) {
	body = r.processWikiLinks(body, ns)
	htmlStr, err := r.convert(body)
	if err != nil {
		return "", err
	}
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
		// Guess a namespace-local slug so the link can create the page in the correct namespace.
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

	htmlStr, err := r.convert(body)
	if err != nil {
		return "", err
	}
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

	htmlStr, err := r.convert(body)
	if err != nil {
		return "", err
	}
	return htmltemplate.HTML(htmlStr), nil
}

func (r *Renderer) processMermaidBlocks(htmlStr string) string {
	re := regexp.MustCompile(`<pre>\s*<code class="language-mermaid">([\s\S]*?)</code>\s*</pre>`)
	return re.ReplaceAllStringFunc(htmlStr, func(match string) string {
		matches := re.FindStringSubmatch(match)
		if len(matches) > 1 {
			// Preserve escaped content; mermaid.js reads the browser-decoded textContent.
			content := matches[1]
			return fmt.Sprintf(`<pre class="mermaid">%s</pre>`, content)
		}
		return match
	})
}
