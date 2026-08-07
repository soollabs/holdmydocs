package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

// wikiConfigFile lives beside the content, so these settings travel with the
// repository rather than belonging to one hmd installation.
const wikiConfigFile = ".wiki.yaml"

const defaultSiteName = "hold my docs (hmd)"

// WikiConfig is the portable, repository-level configuration. It deliberately
// contains only content-facing settings, never instance details or secrets.
type WikiConfig struct {
	Landing  string `yaml:"landing,omitempty"`
	SiteName string `yaml:"site_name,omitempty"`
}

func defaultWikiConfig() WikiConfig { return WikiConfig{SiteName: defaultSiteName} }

func (c WikiConfig) normalised() WikiConfig {
	c.Landing = strings.TrimSpace(c.Landing)
	c.SiteName = strings.TrimSpace(c.SiteName)
	if c.SiteName == "" {
		c.SiteName = defaultSiteName
	}
	return c
}

// Encode marshals the portable settings for saving in the content repository.
func (c WikiConfig) Encode() ([]byte, error) { return yaml.Marshal(c.normalised()) }

// LoadWikiConfig reads .wiki.yaml strictly. A missing file uses the built-in
// defaults; callers can keep serving with those defaults when an existing file
// is malformed.
func LoadWikiConfig(repoDir string) (WikiConfig, bool, error) {
	data, err := os.ReadFile(filepath.Join(repoDir, wikiConfigFile))
	if errors.Is(err, os.ErrNotExist) {
		return defaultWikiConfig(), false, nil
	}
	if err != nil {
		return defaultWikiConfig(), false, fmt.Errorf("reading %s: %w", wikiConfigFile, err)
	}
	var cfg WikiConfig
	if err := yaml.UnmarshalWithOptions(data, &cfg, yaml.Strict()); err != nil {
		return defaultWikiConfig(), true, fmt.Errorf("parsing %s: %w", wikiConfigFile, err)
	}
	cfg = cfg.normalised()
	if !validWikiLanding(cfg.Landing) {
		return defaultWikiConfig(), true, fmt.Errorf("invalid landing %q in %s", cfg.Landing, wikiConfigFile)
	}
	return cfg, true, nil
}

// validWikiLanding accepts an empty value (the first namespace is used), a
// namespace index such as notes/, or a namespace page such as notes/inbox.
func validWikiLanding(slug string) bool {
	if slug == "" {
		return true
	}
	if strings.HasSuffix(slug, "/") {
		return validNamespaceName(strings.TrimSuffix(slug, "/"))
	}
	return validMCPPageSlug(slug)
}
