package api

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	texttemplate "text/template"
	"time"

	"hmd/internal/store"
	"hmd/internal/wiki"
)

// BuildNamespaceRegistry scans repoDir for namespaces: one directory per
// non-dot-prefixed, non-reserved top-level subdirectory.
func BuildNamespaceRegistry(repoDir string) (wiki.NamespaceRegistry, error) {
	return buildNamespaceRegistry(repoDir, os.ReadFile)
}

// BuildNamespaceRegistryFromStore reads namespace configuration through the
// Store boundary so a synchronised repository cannot smuggle in a symlink.
func BuildNamespaceRegistryFromStore(store *store.Store) (wiki.NamespaceRegistry, error) {
	return buildNamespaceRegistry(store.Dir(), func(path string) ([]byte, error) {
		rel, err := filepath.Rel(store.Dir(), path)
		if err != nil {
			return nil, err
		}
		return store.ReadRepositoryFile(filepath.ToSlash(rel))
	})
}

func buildNamespaceRegistry(repoDir string, readFile func(string) ([]byte, error)) (wiki.NamespaceRegistry, error) {
	reg := wiki.NamespaceRegistry{}

	slog.Debug("namespace registry reading repository directory", "path", repoDir)
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return nil, fmt.Errorf("reading repo directory: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() || !wiki.ValidNamespaceName(e.Name()) {
			continue
		}
		slog.Debug("namespace registry loading namespace", "namespace", e.Name())
		reg[e.Name()] = wiki.LoadNamespaceConfigWith(filepath.Join(repoDir, e.Name()), e.Name(), readFile)
	}
	slog.Debug("namespace registry built", "namespaces", len(reg))
	return reg, nil
}

// RefreshNamespaces rebuilds the registry from the Store and replaces the shared snapshot.
func (a *API) RefreshNamespaces() error {
	reg, err := BuildNamespaceRegistryFromStore(a.store)
	if err != nil {
		return err
	}
	a.SetNamespaces(reg)
	return nil
}

// NewPageTemplateData is the field set a namespace's new-page template sees.
type NewPageTemplateData struct {
	Now       time.Time
	User      string
	Namespace string
}

// RenderNewPageText renders one new-page template field.
func RenderNewPageText(src string, data NewPageTemplateData) (string, error) {
	t, err := texttemplate.New("new").Parse(src)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// NormaliseNamespaceConfig validates and normalises a full namespace config,
// including the rendered new-page slug. Empty name means the root namespace.
func NormaliseNamespaceConfig(name string, cfg wiki.NamespaceConfig, data NewPageTemplateData) (wiki.NamespaceConfig, error) {
	var err error
	cfg, err = wiki.NormaliseNamespaceConfigBase(name, cfg)
	if err != nil {
		return wiki.NamespaceConfig{}, err
	}
	if cfg.New == nil {
		return cfg, nil
	}
	cfg.New.Template = strings.TrimSpace(cfg.New.Template)
	if cfg.New.Template == "" {
		cfg.New.Template = wiki.DefaultNewPageTemplate
	}
	if !wiki.ValidPageSegment(cfg.New.Template) {
		return wiki.NamespaceConfig{}, fmt.Errorf("%q is not a valid template page name", cfg.New.Template)
	}
	cfg.New.Slug = strings.TrimSpace(cfg.New.Slug)
	if cfg.New.Slug == "" {
		return wiki.NamespaceConfig{}, fmt.Errorf("new-page slug pattern is required")
	}
	rendered, err := RenderNewPageText(cfg.New.Slug, data)
	if err != nil {
		return wiki.NamespaceConfig{}, fmt.Errorf("slug pattern is not a valid template: %w", err)
	}
	if !wiki.ValidPageSegment(rendered) {
		return wiki.NamespaceConfig{}, fmt.Errorf("slug pattern renders unusable page name %q", rendered)
	}
	return cfg, nil
}
