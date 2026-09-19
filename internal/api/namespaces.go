package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	texttemplate "text/template"
	"time"

	"hmd/internal/search"
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

// NamespaceSummary is the shared catalogue entry for one namespace: its name,
// resolved configuration and the pages it contains. It is derived from the
// registry and indexed page titles and is presented as the namespace listing.
type NamespaceSummary struct {
	Name   string
	Config wiki.NamespaceConfig
	Count  int
	Pages  []search.BacklinkEntry
}

// NamespaceSummaries returns the full namespace catalogue without caller
// filtering. It lists every configured namespace plus every namespace that
// holds a page, each with its pages sorted by slug, in name order.
func (a *API) NamespaceSummaries() []NamespaceSummary {
	reg := a.Namespaces()
	titles := a.index.Titles()
	entries := make(map[string]*NamespaceSummary)
	include := func(name string, cfg wiki.NamespaceConfig) *NamespaceSummary {
		if entry, ok := entries[name]; ok {
			return entry
		}
		entry := &NamespaceSummary{Name: name, Config: cfg}
		entries[name] = entry
		return entry
	}

	for name, cfg := range reg {
		if cfg.Configured {
			include(name, cfg)
		}
	}
	for slug, title := range titles {
		name, rest := wiki.NamespaceFor(slug)
		if rest == "" {
			continue
		}
		entry := include(name, reg.Resolve(slug))
		if title == "" {
			title = slug
		}
		entry.Pages = append(entry.Pages, search.BacklinkEntry{Slug: slug, Title: title})
	}

	result := make([]NamespaceSummary, 0, len(entries))
	for _, entry := range entries {
		sort.Slice(entry.Pages, func(i, j int) bool { return entry.Pages[i].Slug < entry.Pages[j].Slug })
		entry.Count = len(entry.Pages)
		result = append(result, *entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// ListNamespaces returns the namespace catalogue filtered to the caller's
// namespace access, with each entry's pages filtered by slug access and its
// count recomputed. Adapters render their namespace listings from it.
func (a *API) ListNamespaces(ctx context.Context) []NamespaceSummary {
	summaries := a.NamespaceSummaries()
	filtered := make([]NamespaceSummary, 0, len(summaries))
	for _, summary := range summaries {
		if !AllowNamespace(ctx, summary.Name) {
			continue
		}
		pages := make([]search.BacklinkEntry, 0, len(summary.Pages))
		for _, page := range summary.Pages {
			if AllowSlug(ctx, page.Slug) {
				pages = append(pages, page)
			}
		}
		summary.Pages = pages
		summary.Count = len(pages)
		filtered = append(filtered, summary)
	}
	return filtered
}

// NamespaceSummary returns one caller-visible catalogue entry, or nil.
func (a *API) NamespaceSummary(ctx context.Context, name string) *NamespaceSummary {
	for _, summary := range a.ListNamespaces(ctx) {
		if summary.Name == name {
			return &summary
		}
	}
	return nil
}

// NamespaceDetail is a namespace's full settings with its current config hash.
type NamespaceDetail struct {
	Name   string
	Config wiki.NamespaceConfig
	Hash   string
}

// ReadNamespace reads one namespace's settings and current config hash. The
// caller must be allowed the namespace; an unknown namespace is not found.
func (a *API) ReadNamespace(ctx context.Context, name string) (*NamespaceDetail, error) {
	if !wiki.ValidNamespaceName(name) {
		return nil, InvalidInput("invalid namespace", nil)
	}
	if !AllowNamespace(ctx, name) {
		return nil, Forbidden("namespace access denied")
	}
	cfg, ok := a.Namespaces()[name]
	if !ok {
		return nil, NotFound("namespace not found")
	}
	_, hash, err := a.store.Read(wiki.NamespaceConfigPath(name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, Unavailable("reading namespace", err)
	}
	return &NamespaceDetail{Name: name, Config: cfg, Hash: hash}, nil
}
