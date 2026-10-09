package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"hmd/internal/wiki"
)

type exportCLIOptions struct {
	BaseURL  string
	UseMain  bool
	Sitemap  bool
	Bots     string
	Links    string
	Provided map[string]bool
}

func resolveExportSettings(c wiki.ExportConfig, mainURL string, options exportCLIOptions) (wiki.ExportConfig, error) {
	if options.UseMain && options.Provided["export-base-url"] {
		return c, fmt.Errorf("choose either -export-base-url or -export-use-main-base-url")
	}
	if options.Provided["export-base-url"] {
		c.BaseURL = options.BaseURL
	}
	if options.UseMain {
		c.BaseURL = mainURL
	}
	if options.Provided["export-sitemap"] {
		c.Sitemap = &options.Sitemap
	}
	if options.Provided["export-robots-allow"] {
		c.RobotsAllow = nil
		if strings.TrimSpace(options.Bots) != "" {
			c.RobotsAllow = strings.Split(options.Bots, ",")
		}
	}
	if options.Provided["export-links"] {
		c.Links = nil
		if err := json.Unmarshal([]byte(options.Links), &c.Links); err != nil {
			return c, fmt.Errorf("invalid -export-links JSON: %w", err)
		}
	}
	return wiki.NormaliseExportConfig(c, true)
}
