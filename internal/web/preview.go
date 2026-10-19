package web

import (
	"html/template"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"

	staticexport "hmd/internal/export"
	"hmd/internal/wiki"
)

// maxDescriptionRunes is about what search results show of a description.
const maxDescriptionRunes = 155

// voidElements have no end tag, so they never change paragraph nesting.
var voidElements = map[string]bool{"br": true, "img": true, "wbr": true, "input": true}

// linkPreview is what search engines and chat apps show for a shared page.
// It needs absolute URLs, so it is only built where the public URL is known.
type linkPreview struct {
	URL   string // canonical page URL
	Type  string // og:type: website for the namespace home, article otherwise
	Image string // absolute image URL; empty shows a text-only card
}

// pageSummary reads a rendered page for its link preview: the text of the
// first paragraph, shortened to a description, and the first PNG or JPEG
// attachment of namespace ns, as the page/filename an export image names.
func pageSummary(content template.HTML, ns string) (description, image string) {
	prefix := "/_/attachments/" + ns + "/"
	tokenizer := html.NewTokenizer(strings.NewReader(string(content)))
	var text strings.Builder
	depth := 0 // nesting inside the paragraph being read; 0 when outside one
	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			return description, image
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := tokenizer.TagName()
			if image == "" && string(name) == "img" {
				for hasAttr {
					var key, val []byte
					key, val, hasAttr = tokenizer.TagAttr()
					if string(key) != "src" {
						continue
					}
					if ref, ok := strings.CutPrefix(string(val), prefix); ok {
						if ref, err := url.PathUnescape(ref); err == nil {
							if _, _, valid := wiki.SplitExportImage(ref); valid {
								image = ref
							}
						}
					}
				}
			}
			if depth > 0 && string(name) == "br" {
				text.WriteByte(' ')
			}
			if tt == html.StartTagToken && description == "" && !voidElements[string(name)] && (depth > 0 || string(name) == "p") {
				depth++
			}
		case html.EndTagToken:
			if depth > 0 {
				depth--
				if depth == 0 {
					description = shortenDescription(text.String())
					text.Reset()
				}
			}
		case html.TextToken:
			if depth > 0 {
				text.Write(tokenizer.Text())
			}
		}
		if description != "" && image != "" {
			return description, image
		}
	}
}

// shortenDescription collapses whitespace and cuts text at a word boundary to
// fit a meta description.
func shortenDescription(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= maxDescriptionRunes {
		return text
	}
	runes := []rune(text)[:maxDescriptionRunes]
	cut := string(runes)
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:.-") + "…"
}

// exportLinkPreview builds the link preview of an exported page, or of the
// namespace home when rest is the index. A page without its own image uses
// the namespace's export image.
func exportLinkPreview(base *url.URL, rest, index, image, fallbackImage string) *linkPreview {
	if base == nil {
		return nil
	}
	preview := &linkPreview{URL: staticexport.PageURL(base, rest, index), Type: "article"}
	if rest == index {
		preview.Type = "website"
	}
	if image == "" {
		image = fallbackImage
	}
	if page, filename, ok := wiki.SplitExportImage(image); ok {
		preview.Image = staticexport.AttachmentURL(base, page, filename)
	}
	return preview
}

// liveLinkPreview builds the link preview of a public page served by this
// server, or of the namespace home when page is empty. baseURL is the
// server's public URL; without it there is no preview.
func liveLinkPreview(baseURL, namespace, page string, cfg wiki.NamespaceConfig, image string) *linkPreview {
	base, err := url.Parse(strings.TrimRight(baseURL, "/") + "/")
	if baseURL == "" || err != nil {
		return nil
	}
	preview := &linkPreview{Type: "article"}
	if page == "" || page == cfg.Index {
		preview.Type = "website"
		preview.URL = base.ResolveReference(&url.URL{Path: namespace + "/"}).String()
	} else {
		preview.URL = base.ResolveReference(&url.URL{Path: wiki.NamespaceSlug(namespace, page)}).String()
	}
	if image == "" {
		image = cfg.Export.Image
	}
	if imagePage, filename, ok := wiki.SplitExportImage(image); ok {
		preview.Image = base.ResolveReference(&url.URL{Path: "_/attachments/" + namespace + "/" + imagePage + "/" + filename}).String()
	}
	return preview
}
