package wiki

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ExportConfig configures the published snapshot independently of the live server.
type ExportConfig struct {
	BaseURL     string       `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	Sitemap     *bool        `yaml:"sitemap,omitempty" json:"sitemap,omitempty"`
	RobotsAllow []string     `yaml:"robots_allow,omitempty" json:"robots_allow,omitempty"`
	Links       []ExportLink `yaml:"links,omitempty" json:"links,omitempty"`
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
	if len(c.RobotsAllow) > 64 || len(c.Links) > 32 {
		return c, fmt.Errorf("export supports at most 64 allowed bots and 32 external links")
	}
	c.RobotsAllow = append([]string(nil), c.RobotsAllow...)
	c.Links = append([]ExportLink(nil), c.Links...)
	seen := map[string]bool{}
	for i, name := range c.RobotsAllow {
		name = strings.TrimSpace(name)
		if !robotName.MatchString(name) || seen[strings.ToLower(name)] {
			return c, fmt.Errorf("invalid or repeated robots bot name %q", name)
		}
		seen[strings.ToLower(name)] = true
		c.RobotsAllow[i] = name
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

func (c ExportConfig) SitemapEnabled() bool { return c.Sitemap == nil || *c.Sitemap }
