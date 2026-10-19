package wiki

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// ExportConfig configures the published snapshot independently of the live server.
type ExportConfig struct {
	BaseURL     string       `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	Sitemap     *bool        `yaml:"sitemap,omitempty" json:"sitemap,omitempty"`
	RobotsAllow []string     `yaml:"robots_allow,omitempty" json:"robots_allow,omitempty"`
	Links       []ExportLink `yaml:"links,omitempty" json:"links,omitempty"`
	Image       string       `yaml:"image,omitempty" json:"image,omitempty"` // link preview fallback: "<page>/<filename>" of a namespace attachment
}

type ExportLink struct {
	Label    string `yaml:"label" json:"label"`
	URL      string `yaml:"url" json:"url"`
	Icon     string `yaml:"icon,omitempty" json:"icon,omitempty"`
	Location string `yaml:"location,omitempty" json:"location,omitempty"`
	IconOnly bool   `yaml:"icon_only,omitempty" json:"icon_only,omitempty"`
}

var robotName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var faIcon = regexp.MustCompile(`^fa-(brands|solid|regular) fa-[a-z0-9-]{1,80}$`)

func exportURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && !strings.ContainsAny(raw, "\r\n\t ")
}

// NormaliseExportConfig validates operator-controlled URLs and robots directives.
func NormaliseExportConfig(c ExportConfig, requireURL bool) (ExportConfig, error) {
	c.BaseURL = strings.TrimSpace(c.BaseURL)
	if c.BaseURL == "" && requireURL {
		return c, fmt.Errorf("export base URL is required: set export.base_url or -export-base-url (or explicitly use -export-use-main-base-url)")
	}
	if c.BaseURL != "" {
		if !exportURL(c.BaseURL) {
			return c, fmt.Errorf("export base URL must be an absolute HTTP(S) URL without credentials, query or fragment")
		}
		c.BaseURL = strings.TrimRight(c.BaseURL, "/") + "/"
	}
	c.Image = strings.TrimSpace(c.Image)
	if c.Image != "" {
		if _, _, ok := SplitExportImage(c.Image); !ok {
			return c, fmt.Errorf("export image must name a PNG or JPEG attachment as page/filename, e.g. index/hero.png")
		}
	}
	if len(c.RobotsAllow) > 64 || len(c.Links) > 32 {
		return c, fmt.Errorf("export supports at most 64 allowed bots and 32 external links")
	}
	c.RobotsAllow = append([]string(nil), c.RobotsAllow...)
	c.Links = append([]ExportLink(nil), c.Links...)
	seen := map[string]bool{}
	for i, name := range c.RobotsAllow {
		name = strings.TrimSpace(name)
		if (name != "*" && !robotName.MatchString(name)) || seen[strings.ToLower(name)] {
			return c, fmt.Errorf("invalid or repeated robots bot name %q", name)
		}
		seen[strings.ToLower(name)] = true
		c.RobotsAllow[i] = name
	}
	if seen["*"] && len(c.RobotsAllow) > 1 {
		return c, fmt.Errorf(`robots bot "*" allows every bot and cannot be combined with named bots`)
	}
	var sidebarMode *bool
	for i, link := range c.Links {
		link.Label, link.URL, link.Icon = strings.TrimSpace(link.Label), strings.TrimSpace(link.URL), strings.TrimSpace(link.Icon)
		u, err := url.Parse(link.URL)
		if link.Label == "" || len(link.Label) > 256 || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || strings.ContainsAny(link.URL, "\r\n\t ") {
			return c, fmt.Errorf("external links require a label and an absolute HTTP(S) URL without credentials")
		}
		if link.Icon != "" && !faIcon.MatchString(link.Icon) {
			return c, fmt.Errorf("icon must use Font Awesome classes, e.g. fa-brands fa-github")
		}
		link.Location = strings.TrimSpace(link.Location)
		if link.Location != "" && link.Location != "sidebar" && link.Location != "topbar" {
			return c, fmt.Errorf("external link location must be sidebar or topbar")
		}
		if link.Location == "topbar" {
			link.IconOnly = true
		} else if sidebarMode == nil {
			mode := link.IconOnly
			sidebarMode = &mode
		} else if *sidebarMode != link.IconOnly {
			return c, fmt.Errorf("sidebar links must be all icon-only or all text")
		}
		if link.IconOnly && link.Icon == "" {
			return c, fmt.Errorf("icon-only links require an icon; the label remains required for accessibility")
		}
		c.Links[i] = link
	}
	return c, nil
}

// SplitExportImage splits an export image reference into the owning page,
// relative to the namespace, and the attachment filename.
func SplitExportImage(ref string) (page, filename string, ok bool) {
	i := strings.LastIndexByte(ref, '/')
	if i < 1 {
		return "", "", false
	}
	page, filename = ref[:i], ref[i+1:]
	return page, filename, ValidPagePath(page) && !strings.ContainsAny(filename, `\`) && !strings.HasPrefix(filename, ".") && PreviewImageFile(filename)
}

// PreviewImageFile reports whether a filename is a format every link preview
// service accepts.
func PreviewImageFile(filename string) bool {
	switch strings.ToLower(path.Ext(filename)) {
	case ".png", ".jpg", ".jpeg":
		return true
	}
	return false
}

func (c ExportConfig) SitemapEnabled() bool { return c.Sitemap == nil || *c.Sitemap }
