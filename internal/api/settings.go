package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/presentation"
	"hmd/internal/store"
	"hmd/internal/wiki"
)

// storeOptions projects the runtime configuration onto Store options so a
// settings change can rebuild the store's remote.
func storeOptions(cfg config.Config) store.Options {
	return store.Options{
		RepoDir:       cfg.RepoDir,
		ReadOnly:      cfg.ReadOnly,
		DefaultBranch: cfg.DefaultBranch,
		Git: store.GitOptions{
			RemoteURL: cfg.Git.RemoteURL,
			User:      cfg.Git.User,
			Token:     cfg.Git.Token,
		},
	}
}

// ServerSettingsInput is the submitted install-wide configuration, keyed by
// config.yaml setting key (git.remote_url) as the settings form posts it.
// A null value removes the key, so its default applies again.
type ServerSettingsInput struct {
	Values map[string]any
}

// SaveServerSettings validates and persists the install-wide configuration.
// Every setting is applied the same way, from the settings schema: a value an
// environment variable supplies is never changed, so a crafted form cannot move
// it (or turn an omitted disabled checkbox into false). It reloads the
// configuration and rebuilds the store remote after a successful write.
func (a *API) SaveServerSettings(ctx context.Context, in ServerSettingsInput) error {
	if err := a.RequireScope(ctx, ScopeAdmin); err != nil {
		return err
	}
	for key := range in.Values {
		s, ok := config.Lookup(key)
		if !ok {
			return InvalidInput(fmt.Sprintf("unknown setting %q", key), nil)
		}
		if s.EnvOnly {
			return InvalidInput(fmt.Sprintf("%s can only be set in the environment (%s)", key, s.Env), nil)
		}
	}

	configPath := a.Config().ConfigFile
	fc, err := config.LoadFileConfig(configPath)
	if err != nil && !os.IsNotExist(err) {
		return Unavailable("reading config", err)
	}

	overrides := a.Config().EnvOverrides
	for _, s := range config.Settings() {
		if s.EnvOnly || overrides[s.Key] != "" {
			continue
		}
		value, ok := in.Values[s.Key]
		if !ok {
			continue
		}
		if err := config.SetFileValue(&fc, s.Key, value); err != nil {
			return InvalidInput(err.Error(), err)
		}
	}
	if err := ValidateGitAuthor(fc.Git.Author); err != nil {
		return InvalidInput(err.Error(), err)
	}

	currentCfg := a.Config()
	if currentCfg.OAuth.Enabled {
		baseURL := currentCfg.BaseURL
		if currentCfg.EnvOverrides["server.base_url"] == "" {
			baseURL = strings.TrimRight(fc.Server.BaseURL, "/")
		}
		mcpEnabled := currentCfg.MCP.Enabled
		if currentCfg.EnvOverrides["mcp.enabled"] == "" {
			mcpEnabled = fc.MCP.Enabled
		}
		if baseURL != currentCfg.BaseURL || !mcpEnabled {
			return InvalidInput("OAuth issuer and MCP availability require a restart; change them in deployment configuration", nil)
		}
	}

	// Resolve the candidate exactly as a restart would (schema rules,
	// environment overrides and cross-field checks) before anything is
	// written, so a saved config.yaml always loads.
	if _, err := config.Resolve(fc); err != nil {
		return InvalidInput(err.Error(), err)
	}

	if err := config.SaveFileConfig(configPath, fc); err != nil {
		return Unavailable("saving config", err)
	}

	newCfg, err := config.LoadConfig()
	if err != nil {
		return Unavailable("config saved but reload failed", err)
	}
	a.applySavedConfig(newCfg)

	if err := a.store.UpdateRemote(storeOptions(newCfg)); err != nil {
		slog.Warn("updating store remote", "err", err)
	}
	slog.Info("settings updated")
	return nil
}

// ExportServerConfig bakes the resolved saved configuration (including
// environment-derived values) into the configuration file. It exports the
// saved rather than the running values so settings waiting for a restart
// are kept.
func (a *API) ExportServerConfig(ctx context.Context) error {
	if err := a.RequireScope(ctx, ScopeAdmin); err != nil {
		return err
	}
	cfg := a.SavedConfig()
	if err := config.SaveFileConfig(cfg.ConfigFile, cfg.ToFileConfig()); err != nil {
		return Unavailable("exporting config", err)
	}
	newCfg, err := config.LoadConfig()
	if err != nil {
		return Unavailable("config exported but reload failed", err)
	}
	a.applySavedConfig(newCfg)
	slog.Info("settings exported to config file")
	return nil
}

// WikiConfigInput is the submitted repository landing and site title.
type WikiConfigInput struct {
	Landing  string
	SiteName string
}

// SaveWikiConfig validates and persists the repository-level settings and
// updates the runtime snapshot. It is used by the settings screen and the
// first-run setup wizard.
func (a *API) SaveWikiConfig(ctx context.Context, in WikiConfigInput) error {
	if err := a.RequireScope(ctx, ScopeAdmin); err != nil {
		return err
	}
	cfg := wiki.WikiConfig{Landing: strings.TrimSpace(in.Landing), SiteName: strings.TrimSpace(in.SiteName)}
	if cfg.SiteName == "" {
		return InvalidInput("site name cannot be empty", nil)
	}
	if !wiki.ValidLanding(cfg.Landing) {
		return InvalidInput("landing must be a namespace index such as notes/ or a page such as notes/inbox", nil)
	}
	data, err := cfg.Encode()
	if err != nil {
		return Unavailable("encoding wiki settings", err)
	}
	name, email := a.Author(ctx)
	if _, err := a.store.Save(wiki.ConfigFile, data, "Configure wiki settings", name, email); err != nil {
		return Unavailable("saving wiki settings", err)
	}
	a.SetWikiConfig(cfg)
	slog.Info("wiki settings updated")
	return nil
}

// ResetHelp rewrites the built-in help guide.
func (a *API) ResetHelp(ctx context.Context) error {
	if err := a.RequireScope(ctx, ScopeAdmin); err != nil {
		return err
	}
	name, email := a.Author(ctx)
	content := wiki.Page{Slug: "help", Title: "Help", Tags: []string{"meta"}, Body: store.DefaultHelpMD}.Encode()
	if _, err := a.store.Save(".help.md", content, "Reset .help.md to built-in", name, email); err != nil {
		return Unavailable("resetting .help.md", err)
	}
	return nil
}

// SetAppearance stores the caller's per-user appearance preferences. It
// validates the catalogue identifiers and applies the skin-switch palette
// reset: selecting a new skin discards the submitted palette unless the
// browser reports an explicit later palette choice.
func (a *API) SetAppearance(ctx context.Context, palette, fontUI, fontMono, skin string, paletteExplicit bool) error {
	if err := a.RequireOwnSettings(ctx); err != nil {
		return err
	}
	if palette != "" && !presentation.ValidPalette(palette) {
		return InvalidInput("unknown palette", nil)
	}
	if fontUI != "" && !presentation.ValidFont(fontUI) {
		return InvalidInput("unknown UI font", nil)
	}
	if fontMono != "" && !presentation.ValidFont(fontMono) {
		return InvalidInput("unknown monospace font", nil)
	}
	if skin != "" && !presentation.ValidSkin(skin) {
		return InvalidInput("unknown skin", nil)
	}

	username, _ := Username(ctx)
	prevSkin := a.auth.Prefs(username).Skin
	if prevSkin == "" {
		prevSkin = a.Config().Skin
	}
	if presentation.SkinName(skin) != presentation.SkinName(prevSkin) && !paletteExplicit {
		palette = presentation.ResolveSkin(skin).Palette
	}

	if err := a.auth.SetPrefs(username, palette, fontUI, fontMono, skin); err != nil {
		return Unavailable("saving appearance", err)
	}
	slog.Info("appearance updated", "user", username)
	return nil
}

// SetGitAuthor stores the caller's per-user git author override.
func (a *API) SetGitAuthor(ctx context.Context, author string) error {
	if err := a.RequireOwnSettings(ctx); err != nil {
		return err
	}
	author = strings.TrimSpace(author)
	if err := ValidateGitAuthor(author); err != nil {
		return InvalidInput(err.Error(), err)
	}
	username, _ := Username(ctx)
	if err := a.auth.SetAuthor(username, author); err != nil {
		return Unavailable("saving git author", err)
	}
	return nil
}

// RerunSetup requests the first-run setup wizard on the next navigation.
func (a *API) RerunSetup(ctx context.Context) error {
	if err := a.RequireScope(ctx, ScopeAdmin); err != nil {
		return err
	}
	a.store.ForceSetup.Store(true)
	return nil
}

// Shutdown begins a graceful process shutdown. The process exits; it only
// comes back if a supervisor such as Docker or systemd restarts it.
func (a *API) Shutdown(ctx context.Context) error {
	if err := a.RequireScope(ctx, ScopeAdmin); err != nil {
		return err
	}
	if a.shutdown == nil {
		return Unavailable("shutdown is not available", nil)
	}
	slog.Warn("shutdown requested from settings", "user", auth.UserFromContext(ctx))
	a.shutdown()
	return nil
}

// DefaultSetupNamespace is the namespace name the first-run wizard suggests
// when the user does not name one.
const DefaultSetupNamespace = "notes"

// DefaultHomeBody and DefaultHelpBody expose the built-in setup previews
// without granting a presentation adapter access to persistence.
func DefaultHomeBody() string { return store.DefaultHomeMD }
func DefaultHelpBody() string { return store.DefaultHelpMD }

// NewSetupNamespaceOption is the wizard's select value for "create a new
// namespace" rather than picking a detected one.
const NewSetupNamespaceOption = "_new"

// FirstRunSetupInput is the submitted first-run setup wizard. Action is
// "add" to seed the selected items or anything else to dismiss the wizard;
// the wizard requires admin access.
type FirstRunSetupInput struct {
	Action           string
	SetupWiki        bool
	DefaultNamespace string
	NewNamespace     string
	SiteName         string
	AddNamespace     bool
	Namespace        string
	AddHelp          bool
}

// CompleteFirstRunSetup validates and applies the wizard's selected seeds: the
// wiki configuration, a namespace, and the help guide. It then clears the
// first-run flags so the wizard stops appearing. Every write goes through the
// shared operations, so the wizard cannot diverge from the settings screens.
func (a *API) CompleteFirstRunSetup(ctx context.Context, in FirstRunSetupInput) error {
	if err := a.RequireScope(ctx, ScopeAdmin); err != nil {
		return err
	}
	if in.Action == "add" {
		_, wikiExists, wikiErr := wiki.LoadWikiConfig(a.Config().RepoDir)
		if wikiErr != nil {
			return Unavailable("failed to read wiki config", wikiErr)
		}
		if !wikiExists && !in.SetupWiki {
			return InvalidInput("wiki setup is required", nil)
		}
		if in.SetupWiki {
			selected := strings.TrimSpace(in.DefaultNamespace)
			landing := ""
			switch selected {
			case NewSetupNamespaceOption:
				name := strings.Trim(strings.TrimSpace(in.NewNamespace), "/")
				if name == "" {
					name = DefaultSetupNamespace
				}
				if !wiki.ValidNamespaceName(name) {
					return InvalidInput("invalid namespace name", nil)
				}
				if _, exists := a.Namespaces()[name]; exists {
					return InvalidInput("namespace already exists; select it as the default instead", nil)
				}
				if err := a.SeedFirstNamespace(ctx, name); err != nil {
					return err
				}
				landing = name + "/"
			default:
				if _, ok := a.Namespaces()[selected]; !ok {
					return InvalidInput("choose a detected namespace or create a new one", nil)
				}
				landing = selected + "/"
			}
			if err := a.SaveWikiConfig(ctx, WikiConfigInput{Landing: landing, SiteName: strings.TrimSpace(in.SiteName)}); err != nil {
				return err
			}
		}
		if in.AddNamespace {
			name := strings.Trim(strings.TrimSpace(in.Namespace), "/")
			if name == "" {
				name = DefaultSetupNamespace
			}
			if !wiki.ValidNamespaceName(name) {
				return InvalidInput("invalid namespace name", nil)
			}
			if err := a.SeedFirstNamespace(ctx, name); err != nil {
				return err
			}
		}
		if in.AddHelp {
			if err := a.ResetHelp(ctx); err != nil {
				return err
			}
		}
	}
	a.store.NeedsSetup.Store(false)
	a.store.ForceSetup.Store(false)
	return nil
}

// TokenTTLs maps the settings screen's expiry choices to their duration. A
// "never" choice has zero duration and no expiry.
var TokenTTLs = map[string]time.Duration{
	"1d":    24 * time.Hour,
	"7d":    7 * 24 * time.Hour,
	"30d":   30 * 24 * time.Hour,
	"1y":    365 * 24 * time.Hour,
	"never": 0,
}

// CreateUser adds a user with the given scopes.
func (a *API) CreateUser(ctx context.Context, name, password string, scopes []string) error {
	if err := a.RequireScope(ctx, ScopeAdmin); err != nil {
		return err
	}
	if name == "" || password == "" {
		return InvalidInput("name and password are required", nil)
	}
	if a.auth.UserExists(name) {
		return InvalidInput(fmt.Sprintf("user %q already exists", name), nil)
	}
	if err := a.auth.AddUserWithScopes(name, password, scopes); err != nil {
		return InvalidInput(err.Error(), err)
	}
	slog.Info("user created", "user", name)
	return nil
}

// SetUserScopes replaces a user's scopes, keeping the caller's own admin access.
func (a *API) SetUserScopes(ctx context.Context, name string, scopes []string) error {
	if err := a.RequireScope(ctx, ScopeAdmin); err != nil {
		return err
	}
	username, _ := Username(ctx)
	if name == username {
		hasAdmin := false
		for _, s := range scopes {
			if s == string(auth.ScopeAdmin) {
				hasAdmin = true
			}
		}
		if !hasAdmin {
			return InvalidInput("cannot remove your own admin access", nil)
		}
	}
	if err := a.auth.SetScopes(name, scopes); err != nil {
		return InvalidInput(err.Error(), err)
	}
	slog.Info("user scopes updated", "user", name)
	return nil
}

// CreateToken mints a personal access token for the caller. The adapter maps
// form TTL choices to an absolute expiry before calling.
func (a *API) CreateToken(ctx context.Context, label string, expires time.Time, scopes, namespaces []string) (string, error) {
	if err := a.RequireOwnSettings(ctx); err != nil {
		return "", err
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return "", InvalidInput("token needs a name", nil)
	}
	if len(scopes) == 0 {
		return "", InvalidInput("token needs at least one scope", nil)
	}
	known := make(map[string]struct{})
	for _, summary := range a.namespaceSummaries() {
		known[summary.Name] = struct{}{}
	}
	cleaned := make([]string, 0, len(namespaces))
	for _, raw := range namespaces {
		namespace := strings.TrimSpace(raw)
		if namespace == "" {
			return "", InvalidInput("invalid namespace", nil)
		}
		if _, ok := known[namespace]; !ok {
			return "", InvalidInput(fmt.Sprintf("unknown namespace %q", namespace), nil)
		}
		cleaned = append(cleaned, namespace)
	}
	if len(cleaned) == 0 {
		cleaned = nil
	}
	// AddToken refuses scopes the user lacks. A token must not mint a broader
	// one either: a read token or one limited to some namespaces could
	// otherwise turn into a token with the user's full access.
	if caller, ok := auth.TokenPrincipalFromContext(ctx); ok {
		for _, raw := range scopes {
			// Unknown names are left for AddToken to reject as invalid input.
			if scope := Scope(strings.TrimSpace(raw)); slices.Contains(auth.AllScopes, scope) && !caller.HasScope(scope) {
				return "", Forbidden("a token cannot create a token with more scopes than its own")
			}
		}
		if caller.Restricted() && (cleaned == nil || slices.ContainsFunc(cleaned, func(namespace string) bool { return !caller.AllowsNamespace(namespace) })) {
			return "", Forbidden("a token cannot create a token for namespaces outside its own")
		}
	}
	username, _ := Username(ctx)
	token, err := a.auth.AddToken(username, label, expires, scopes, cleaned)
	if err != nil {
		return "", InvalidInput(err.Error(), err)
	}
	slog.Info("token created", "user", username, "label", label)
	return token, nil
}

// RevokeToken removes one of the caller's personal access tokens.
func (a *API) RevokeToken(ctx context.Context, label string) error {
	if err := a.RequireOwnSettings(ctx); err != nil {
		return err
	}
	username, _ := Username(ctx)
	if err := a.auth.RemoveToken(username, label); err != nil {
		return InvalidInput(err.Error(), err)
	}
	slog.Info("token revoked", "user", username, "label", label)
	return nil
}

// SeedFirstNamespace creates the initial namespace configuration, its index
// page and the root readme, then refreshes the registry. It is used by the
// first-run setup wizard, which requires admin access.
func (a *API) SeedFirstNamespace(ctx context.Context, name string) error {
	if err := a.RequireScope(ctx, ScopeAdmin); err != nil {
		return err
	}
	if !wiki.ValidNamespaceName(name) {
		return InvalidInput("invalid namespace name", nil)
	}
	authorName, authorEmail := a.Author(ctx)
	nsCfg := wiki.NamespaceConfig{Widgets: wiki.BuiltinNamespaceWidgets, Index: store.DefaultIndexPage}
	data, err := nsCfg.Encode()
	if err != nil {
		return Unavailable("encoding namespace config", err)
	}
	path := wiki.NamespaceConfigPath(name)
	if _, err := a.store.Save(path, data, "Configure namespace "+path, authorName, authorEmail); err != nil {
		return Unavailable("saving namespace config", err)
	}

	indexSlug := wiki.NamespaceSlug(name, store.DefaultIndexPage)
	if _, _, err := a.store.Read(wiki.PageFile(indexSlug)); err != nil {
		content := wiki.Page{Slug: indexSlug, Title: name, Body: store.DefaultHomeMD}.Encode()
		if _, err := a.store.Save(wiki.PageFile(indexSlug), content, "Add "+indexSlug, authorName, authorEmail); err != nil {
			return Unavailable("saving index page", err)
		}
		if a.index != nil {
			if err := a.index.Update(wiki.ParsePage(indexSlug, content)); err != nil {
				slog.Error("updating search index", "slug", indexSlug, "err", err)
			}
		}
	}

	if _, _, err := a.store.Read("readme.md"); err != nil {
		if _, err := a.store.Save("readme.md", []byte(store.RootReadmeMD), "Add readme.md", authorName, authorEmail); err != nil {
			return Unavailable("saving root readme", err)
		}
	}

	a.refreshNamespaces()
	return nil
}

// HelpDrifted reports whether the repository's .help.md differs from the
// shipped default help text, so the settings screen can offer a reset.
func (a *API) HelpDrifted() bool {
	if a.store == nil {
		return false
	}
	raw, _, err := a.store.Read(".help.md")
	return err == nil && wiki.ParsePage("help", raw).Body != strings.TrimRight(store.DefaultHelpMD, "\n")
}

// SetupState summarises the first-run setup requirements derived from
// repository state, so the browser can render the setup banner and wizard.
type SetupState struct {
	Needed         bool
	Wiki           bool
	Namespace      bool
	Help           bool
	HelpFileExists bool
}

// SetupState reads the store's setup flags and repository contents. It returns
// the zero value once setup is complete and not forced.
func (a *API) SetupState() SetupState {
	if a.store == nil {
		return SetupState{}
	}
	if !a.store.NeedsSetup.Load() && !a.store.ForceSetup.Load() {
		return SetupState{}
	}
	forced := a.store.ForceSetup.Load()
	repoDir := a.Config().RepoDir
	var state SetupState
	if _, wikiExists, wikiErr := wiki.LoadWikiConfig(repoDir); wikiErr == nil && !wikiExists {
		state.Wiki = true
	}
	if (!store.HasNamespace(repoDir) || forced) && !state.Wiki {
		state.Namespace = true
	}
	_, helpErr := os.Stat(filepath.Join(repoDir, ".help.md"))
	helpMissing := helpErr != nil
	if helpMissing || forced {
		state.Help = true
		state.HelpFileExists = !helpMissing
	}
	state.Needed = state.Wiki || state.Namespace || state.Help
	return state
}
