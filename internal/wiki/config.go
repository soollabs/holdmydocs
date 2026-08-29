package wiki

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

// ConfigFile is the repository configuration filename.
const ConfigFile = ".wiki.yaml"

const DefaultSiteName = "hold my docs (hmd)"

const (
	wikiConfigFile  = ConfigFile
	defaultSiteName = DefaultSiteName
)

// WikiConfig is repository-level configuration.
type WikiConfig struct {
	Landing  string `yaml:"landing,omitempty"`
	SiteName string `yaml:"site_name,omitempty"`
}

func DefaultConfig() WikiConfig { return WikiConfig{SiteName: DefaultSiteName} }

func (c WikiConfig) Normalised() WikiConfig {
	c.Landing = strings.TrimSpace(c.Landing)
	c.SiteName = strings.TrimSpace(c.SiteName)
	if c.SiteName == "" {
		c.SiteName = DefaultSiteName
	}
	return c
}

// Encode marshals the configuration as YAML.
func (c WikiConfig) Encode() ([]byte, error) { return yaml.Marshal(c.Normalised()) }

// LoadWikiConfig loads the repository configuration.
func LoadWikiConfig(repoDir string) (WikiConfig, bool, error) {
	data, err := os.ReadFile(filepath.Join(repoDir, ConfigFile))
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), false, nil
	}
	if err != nil {
		return DefaultConfig(), false, fmt.Errorf("reading %s: %w", ConfigFile, err)
	}
	var cfg WikiConfig
	if err := yaml.UnmarshalWithOptions(data, &cfg, yaml.Strict()); err != nil {
		return DefaultConfig(), true, fmt.Errorf("parsing %s: %w", ConfigFile, err)
	}
	cfg = cfg.Normalised()
	if !ValidLanding(cfg.Landing) {
		return DefaultConfig(), true, fmt.Errorf("invalid landing %q in %s", cfg.Landing, ConfigFile)
	}
	return cfg, true, nil
}

// validWikiLanding accepts an empty value (the first namespace is used), a namespace index such as notes/, or
// a namespace page such as notes/inbox.
func ValidLanding(slug string) bool {
	if slug == "" {
		return true
	}
	if before, ok := strings.CutSuffix(slug, "/"); ok {
		return ValidNamespaceName(before)
	}
	return ValidPageSlug(slug)
}
