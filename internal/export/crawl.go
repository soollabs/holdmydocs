package export

import (
	"encoding/xml"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"hmd/internal/wiki"
)

// Reserve both names even when sitemaps are disabled, so changing that setting
// cannot turn a page directory into a crawler file or delete a page directory.
func validateCrawlPagePaths(pages []wiki.Page) error {
	for _, page := range pages {
		_, rest := wiki.NamespaceFor(page.Slug)
		first, _, _ := strings.Cut(rest, "/")
		if strings.EqualFold(first, "robots.txt") || strings.EqualFold(first, "sitemap.xml") {
			return fmt.Errorf("page %q conflicts with reserved static export path %q; rename its first page segment before exporting", page.Slug, first)
		}
	}
	return nil
}

// PageURL is a page's canonical published URL; the namespace index is the
// site root.
func PageURL(base *url.URL, rest, index string) string {
	if rest == index {
		return base.String()
	}
	return base.ResolveReference(&url.URL{Path: strings.TrimSuffix(PagePath(rest), "index.html")}).String()
}

// AttachmentURL is the published URL of a namespace attachment, named by its
// page relative to the namespace and its filename.
func AttachmentURL(base *url.URL, page, filename string) string {
	return base.ResolveReference(&url.URL{Path: "attachments/" + page + "/" + filename}).String()
}

func writeCrawlFiles(request NamespaceRequest, pages []wiki.Page) error {
	c := request.Config.Export
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return err
	}
	var robots strings.Builder
	if len(c.RobotsAllow) == 1 && c.RobotsAllow[0] == "*" {
		robots.WriteString("User-agent: *\nAllow: /\n")
	} else {
		robots.WriteString("User-agent: *\nDisallow: /\n")
		for _, bot := range c.RobotsAllow {
			fmt.Fprintf(&robots, "\nUser-agent: %s\nAllow: /\n", bot)
		}
	}
	if c.SitemapEnabled() {
		type location struct {
			Loc string `xml:"loc"`
		}
		type sitemap struct {
			XMLName xml.Name   `xml:"urlset"`
			XMLNS   string     `xml:"xmlns,attr"`
			URLs    []location `xml:"url"`
		}
		doc := sitemap{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
		doc.URLs = append(doc.URLs, location{base.String()})
		for _, page := range pages {
			_, rest := wiki.NamespaceFor(page.Slug)
			if rest != request.Config.Index {
				doc.URLs = append(doc.URLs, location{PageURL(base, rest, request.Config.Index)})
			}
		}
		data, err := xml.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(request.OutDir, "sitemap.xml"), append([]byte(xml.Header), data...), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(&robots, "\nSitemap: %s\n", base.ResolveReference(&url.URL{Path: "sitemap.xml"}))
	} else if err := os.Remove(filepath.Join(request.OutDir, "sitemap.xml")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing disabled sitemap: %w", err)
	}
	return os.WriteFile(filepath.Join(request.OutDir, "robots.txt"), []byte(robots.String()), 0o644)
}
