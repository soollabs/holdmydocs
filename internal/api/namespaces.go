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

	"hmd/internal/auth"
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

// NewPageTemplateFields are the fields a namespace's new-page template may
// interpolate; they are documented in the seeded template body.
var NewPageTemplateFields = []string{
	`.Now.Format "2006-01-02"`,
	`.Now.Format "Monday, 2 January 2006"`,
	`.Now.Format "15:04"`,
	`.User`,
	`.Namespace`,
}

// newPageTemplateBody is the self-documenting body seeded into a namespace's
// new-page template page.
func newPageTemplateBody(ns string) string {
	where := "the root of the wiki"
	if ns != "" {
		where = "`" + ns + "/`"
	}

	var table strings.Builder
	table.WriteString("| field | what it puts on the page |\n| --- | --- |\n")
	for _, f := range NewPageTemplateFields {
		table.WriteString("| `" + f + "` | {{" + f + "}} |\n")
	}

	return "This is the template page for " + where + ". Every page created here " +
		"starts as a copy of it, so whatever you leave in it — headings, a " +
		"checklist, tags — is what a new page begins with.\n\n" +
		"Wrap a field in double braces to have it filled in when the page is " +
		"created; the title of this page does exactly that. Title, tags and " +
		"body are all substituted, and these are the only fields there are:\n\n" +
		table.String() +
		"\nDates use Go's layout syntax: write out the reference time " +
		"`2006-01-02 15:04` in the shape you want it, so `02/01/2006` gives " +
		`{{.Now.Format "02/01/2006"}}` + " and `Jan 2` gives " +
		`{{.Now.Format "Jan 2"}}` + ".\n\n" +
		"This page is hidden — it never shows up in the page list, search, tags " +
		"or backlinks. Delete all of this and make it yours.\n"
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
	Pages  []wiki.BacklinkEntry
}

// namespaceSummaries returns the full namespace catalogue without caller
// filtering. It lists every configured namespace plus every namespace that
// holds a page, each with its pages sorted by slug, in name order.
func (a *API) namespaceSummaries() []NamespaceSummary {
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
		entry.Pages = append(entry.Pages, wiki.BacklinkEntry{Slug: slug, Title: title})
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
	summaries := a.namespaceSummaries()
	filtered := make([]NamespaceSummary, 0, len(summaries))
	for _, summary := range summaries {
		if !a.HasScope(ctx, ScopeRead) && !a.HasScope(ctx, ScopeSettings) {
			if _, authenticated := Username(ctx); authenticated || !a.Namespaces()[summary.Name].Public {
				continue
			}
		}
		if !AllowNamespace(ctx, summary.Name) {
			continue
		}
		pages := make([]wiki.BacklinkEntry, 0, len(summary.Pages))
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
	if err := a.RequireScope(ctx, ScopeSettings); err != nil {
		return nil, err
	}
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

// SaveNamespaceInput is a full namespace configuration replacement. The
// adapter supplies the already-decoded config; normalisation and encoding are
// shared. An empty BaseHash creates; a set BaseHash replaces the revision it
// names. SeedTemplate asks the operation to create the namespace's new-page
// template page when none exists; it never overwrites an existing template.
// Adapters decide when seeding applies (browser creation or enabling new
// pages), so the operation itself has no browser-specific defaults.
type SaveNamespaceInput struct {
	Name         string
	Config       wiki.NamespaceConfig
	TemplateData NewPageTemplateData
	BaseHash     string
	SeedTemplate bool
	TemplateName string
}

// SaveNamespace normalises and persists a namespace configuration with a
// checked write, then refreshes the registry. A stale BaseHash or a
// concurrently created configuration is reported as a conflict carrying the
// current settings; the existing configuration is left untouched.
func (a *API) SaveNamespace(ctx context.Context, in SaveNamespaceInput) (*NamespaceDetail, error) {
	if !wiki.ValidNamespaceName(in.Name) {
		return nil, InvalidInput("invalid namespace", nil)
	}
	if err := a.RequireScope(ctx, auth.ScopeSettings); err != nil {
		return nil, err
	}
	if !AllowNamespace(ctx, in.Name) {
		return nil, Forbidden("namespace access denied")
	}
	if in.SeedTemplate && !wiki.ValidPageSegment(in.TemplateName) {
		return nil, InvalidInput(fmt.Sprintf("%q is not a valid template page name", in.TemplateName), nil)
	}
	cfg, err := NormaliseNamespaceConfig(in.Name, in.Config, in.TemplateData)
	if err != nil {
		return nil, InvalidInput(err.Error(), err)
	}
	data, err := cfg.Encode()
	if err != nil {
		return nil, Unavailable("encoding namespace config", err)
	}
	name, email := a.Author(ctx)
	path := wiki.NamespaceConfigPath(in.Name)
	hash, err := a.store.SaveChecked(path, path, in.BaseHash, data, "Configure namespace "+path, name, email)
	if errors.Is(err, store.ErrConflict) {
		return nil, Conflict("the namespace changed since basehash, or already exists")
	}
	if err != nil {
		return nil, Unavailable("saving namespace config", err)
	}
	if in.SeedTemplate {
		if err := a.seedNewPageTemplate(in.Name, in.TemplateName, name, email); err != nil {
			slog.Warn("seeding namespace template page", "namespace", in.Name, "err", err)
		}
	}
	a.refreshNamespaces()
	slog.Info("namespace configured", "namespace", in.Name, "by", name)
	return &NamespaceDetail{Name: in.Name, Config: cfg, Hash: hash}, nil
}

// seedNewPageTemplate creates a namespace's new-page template page when none
// exists. It never overwrites an existing template.
func (a *API) seedNewPageTemplate(ns, template, authorName, authorEmail string) error {
	slug := wiki.NamespaceSlug(ns, template)
	if _, _, err := a.store.Read(wiki.HiddenFile(slug)); err == nil {
		return nil
	}
	page := wiki.Page{
		Slug:  slug,
		Title: `{{.Now.Format "Monday, 2 January 2006"}}`,
		Body:  newPageTemplateBody(ns),
	}
	if ns != "" {
		page.Tags = []string{ns}
	}
	_, err := a.store.Save(wiki.HiddenFile(slug), page.Encode(), "Add new-page template "+slug, authorName, authorEmail)
	return err
}

// ResetNamespace removes a namespace's configuration file, leaving its pages
// in place, and refreshes the registry.
func (a *API) ResetNamespace(ctx context.Context, name string) error {
	if !wiki.ValidNamespaceName(name) {
		return InvalidInput("invalid namespace", nil)
	}
	if err := a.RequireScope(ctx, auth.ScopeSettings); err != nil {
		return err
	}
	if !AllowNamespace(ctx, name) {
		return Forbidden("namespace access denied")
	}
	authorName, authorEmail := a.Author(ctx)
	path := wiki.NamespaceConfigPath(name)
	if err := a.store.Remove(path, "Reset namespace settings "+path, authorName, authorEmail); err != nil {
		return Unavailable("resetting namespace settings", err)
	}
	a.refreshNamespaces()
	return nil
}

// DeleteNamespace removes a configured namespace's configuration only. It
// refuses when the namespace still contains content; that is a caller-fixable
// rejection, not an internal failure.
func (a *API) DeleteNamespace(ctx context.Context, name string) error {
	if !wiki.ValidNamespaceName(name) {
		return InvalidInput("invalid namespace", nil)
	}
	if err := a.RequireScope(ctx, auth.ScopeSettings); err != nil {
		return err
	}
	if !AllowNamespace(ctx, name) {
		return Forbidden("namespace access denied")
	}
	authorName, authorEmail := a.Author(ctx)
	path := wiki.NamespaceConfigPath(name)
	if err := a.store.DeleteNamespace(name, "Remove namespace config "+path, authorName, authorEmail); err != nil {
		return InvalidInput(err.Error(), err)
	}
	a.refreshNamespaces()
	slog.Info("namespace config removed", "namespace", name, "by", authorName)
	return nil
}

// DeleteNamespaceAll removes a configured namespace and every file beneath it,
// then prunes the derived index and refreshes the registry.
func (a *API) DeleteNamespaceAll(ctx context.Context, name string) error {
	if !wiki.ValidNamespaceName(name) {
		return InvalidInput("invalid namespace", nil)
	}
	if err := a.RequireScope(ctx, auth.ScopeSettings); err != nil {
		return err
	}
	if !AllowNamespace(ctx, name) {
		return Forbidden("namespace access denied")
	}
	authorName, authorEmail := a.Author(ctx)
	if err := a.store.DeleteNamespaceAll(name, "Delete namespace "+name, authorName, authorEmail); err != nil {
		return InvalidInput(err.Error(), err)
	}
	if a.index != nil {
		for slug := range a.index.Titles() {
			if ns, _ := wiki.NamespaceFor(slug); ns == name {
				a.removeFromIndex(slug)
			}
		}
	}
	a.refreshNamespaces()
	slog.Info("namespace deleted with all files", "namespace", name, "by", authorName)
	return nil
}

// refreshNamespaces rebuilds the registry, logging rather than failing so a
// committed write is never reported as an error for a derived snapshot.
func (a *API) refreshNamespaces() {
	if err := a.RefreshNamespaces(); err != nil {
		slog.Warn("rebuilding namespace registry", "err", err)
	}
}
