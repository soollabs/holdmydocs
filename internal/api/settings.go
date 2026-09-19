package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
		DefaultBranch: cfg.DefaultBranch,
		Git: store.GitOptions{
			RemoteURL: cfg.Git.RemoteURL,
			User:      cfg.Git.User,
			Token:     cfg.Git.Token,
		},
	}
}

// ServerSettingsInput is the submitted install-wide server configuration. It
// accepts secrets (Git token, OIDC client secret) only to persist them when
// asked; secrets are never returned through a configuration DTO.
type ServerSettingsInput struct {
	Bind           string
	RepoDir        string
	MaxUploadBytes int64
	SyncPollMs     int
	SyncMode       string
	DefaultBranch  string
	Skin           string
	Debug          bool
	BaseURL        string
	TrustedProxies []string

	RemoteURL     string
	GitUser       string
	GitAuthor     string
	GitToken      string
	StoreGitToken bool
	GitTokenFile  string

	MCPEnabled bool

	DocumentModel    string
	DocumentModelDir string
	DocumentIndexDir string

	OIDCIssuer                string
	OIDCClientID              string
	OIDCClientSecret          string
	OIDCClientSecretFile      string
	OIDCLocalLogin            bool
	OIDCButtonText            string
	OIDCIcon                  string
	OIDCDefaultScopes         []string
	OIDCAllowedSubjects       []string
	OIDCAllowedEmailDomains   []string
	OIDCAllowAnyAuthenticated bool
	OIDCAllowInsecureLoopback bool
}

// SaveServerSettings validates and persists the install-wide configuration,
// honouring the environment-override guard so a crafted form cannot move a
// value the environment owns. It reloads the configuration and rebuilds the
// store remote after a successful write.
func (a *API) SaveServerSettings(ctx context.Context, in ServerSettingsInput) error {
	if in.Bind == "" {
		return InvalidInput("bind cannot be empty", nil)
	}
	if in.RepoDir == "" {
		return InvalidInput("repo directory cannot be empty", nil)
	}
	if in.GitUser == "" {
		return InvalidInput("git user cannot be empty", nil)
	}
	if in.RemoteURL != "" && !strings.HasPrefix(in.RemoteURL, "https://") && !strings.HasPrefix(in.RemoteURL, "git@") {
		return InvalidInput("remote URL must be HTTPS or git@ SSH format", nil)
	}
	if in.SyncMode != "push" && in.SyncMode != "bidirectional" {
		return InvalidInput("sync mode must be push or bidirectional", nil)
	}
	if err := ValidateGitAuthor(in.GitAuthor); err != nil {
		return InvalidInput(err.Error(), err)
	}
	if in.MaxUploadBytes < 1 {
		return InvalidInput("invalid upload size", nil)
	}
	if in.SyncPollMs < 100 {
		return InvalidInput("sync poll must be at least 100ms", nil)
	}

	configPath := a.Config().ConfigFile
	fc, err := config.LoadFileConfig(configPath)
	if err != nil && !os.IsNotExist(err) {
		return Unavailable("reading config", err)
	}

	// Environment variables are authoritative. Do not let a crafted form change
	// their on-disk fallback values (and, especially, do not turn an omitted
	// disabled checkbox into false).
	set := func(path string, apply func()) {
		if a.Config().EnvOverrides[path] == "" {
			apply()
		}
	}
	set("Bind", func() { fc.Bind = in.Bind })
	set("RepoDir", func() { fc.RepoDir = in.RepoDir })
	set("Git.RemoteURL", func() { fc.Git.RemoteURL = in.RemoteURL })
	set("Git.User", func() { fc.Git.User = in.GitUser })
	set("Git.Author", func() { fc.Git.Author = in.GitAuthor })
	set("MaxUploadBytes", func() { v := in.MaxUploadBytes; fc.MaxUploadBytes = &v })
	set("SyncPollMs", func() { v := in.SyncPollMs; fc.SyncPollMs = &v })
	set("SyncMode", func() { fc.SyncMode = in.SyncMode })
	set("DefaultBranch", func() { fc.DefaultBranch = in.DefaultBranch })
	set("Skin", func() { fc.Skin = in.Skin })
	set("Debug", func() { fc.Debug = in.Debug })
	set("BaseURL", func() { fc.BaseURL = in.BaseURL })
	set("TrustedProxies", func() { fc.TrustedProxies = in.TrustedProxies })
	set("Git.TokenFile", func() { fc.Git.TokenFile = in.GitTokenFile })
	set("MCP.Enabled", func() { fc.MCP.Enabled = in.MCPEnabled })
	set("DocumentSearch.Model", func() { fc.DocumentSearch.Model = in.DocumentModel })
	set("DocumentSearch.ModelDir", func() { fc.DocumentSearch.ModelDir = in.DocumentModelDir })
	set("DocumentSearch.IndexDir", func() { fc.DocumentSearch.IndexDir = in.DocumentIndexDir })
	set("OIDC.Issuer", func() { fc.OIDC.Issuer = in.OIDCIssuer })
	set("OIDC.ClientID", func() { fc.OIDC.ClientID = in.OIDCClientID })
	set("OIDC.ClientSecretFile", func() { fc.OIDC.ClientSecretFile = in.OIDCClientSecretFile })
	set("OIDC.ClientSecret", func() {
		if in.OIDCClientSecret != "" {
			fc.OIDC.ClientSecret = in.OIDCClientSecret
		}
	})
	set("OIDC.LocalLogin", func() { localLogin := in.OIDCLocalLogin; fc.OIDC.LocalLogin = &localLogin })
	set("OIDC.ButtonText", func() { fc.OIDC.ButtonText = in.OIDCButtonText })
	set("OIDC.Icon", func() { fc.OIDC.Icon = in.OIDCIcon })
	set("OIDC.DefaultScopes", func() { fc.OIDC.DefaultScopes = in.OIDCDefaultScopes })
	set("OIDC.AllowedSubjects", func() { fc.OIDC.AllowedSubjects = in.OIDCAllowedSubjects })
	set("OIDC.AllowedEmailDomains", func() { fc.OIDC.AllowedEmailDomains = in.OIDCAllowedEmailDomains })
	set("OIDC.AllowAnyAuthenticated", func() { fc.OIDC.AllowAnyAuthenticated = in.OIDCAllowAnyAuthenticated })
	set("OIDC.AllowInsecureLoopback", func() { fc.OIDC.AllowInsecureLoopback = in.OIDCAllowInsecureLoopback })
	set("Git.Token", func() {
		if in.GitToken != "" && in.StoreGitToken {
			fc.Git.Token = in.GitToken
		}
	})

	if err := config.SaveFileConfig(configPath, fc); err != nil {
		return Unavailable("saving config", err)
	}

	newCfg, err := config.LoadConfig()
	if err != nil {
		return Unavailable("config saved but reload failed", err)
	}
	a.SetConfig(newCfg)

	if err := a.store.UpdateRemote(storeOptions(newCfg)); err != nil {
		slog.Warn("updating store remote", "err", err)
	}
	slog.Info("settings updated")
	return nil
}

// ExportServerConfig bakes the resolved runtime configuration (including
// environment-derived values) into the configuration file.
func (a *API) ExportServerConfig(ctx context.Context) error {
	cfg := a.Config()
	if err := config.SaveFileConfig(cfg.ConfigFile, cfg.ToFileConfig()); err != nil {
		return Unavailable("exporting config", err)
	}
	newCfg, err := config.LoadConfig()
	if err != nil {
		return Unavailable("config exported but reload failed", err)
	}
	a.SetConfig(newCfg)
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
	a.store.ForceSetup.Store(true)
	return nil
}

// DefaultSetupNamespace is the namespace name the first-run wizard suggests
// when the user does not name one.
const DefaultSetupNamespace = "notes"

// NewSetupNamespaceOption is the wizard's select value for "create a new
// namespace" rather than picking a detected one.
const NewSetupNamespaceOption = "_new"

// FirstRunSetupInput is the submitted first-run setup wizard. Action is
// "add" to seed the selected items or anything else to dismiss the wizard;
// the wizard is state-gated rather than scope-gated.
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

// SetUserScopes replaces a user's scopes, guarding the bootstrap admin and the
// caller's own settings access.
func (a *API) SetUserScopes(ctx context.Context, name string, scopes []string) error {
	if name == a.Config().AdminUser {
		return InvalidInput("the bootstrap admin user always has full access", nil)
	}
	username, _ := Username(ctx)
	if name == username {
		hasSettings := len(scopes) == 0
		for _, s := range scopes {
			if s == string(auth.ScopeSettings) {
				hasSettings = true
			}
		}
		if !hasSettings {
			return InvalidInput("cannot remove your own settings access", nil)
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
	label = strings.TrimSpace(label)
	if label == "" {
		return "", InvalidInput("token needs a name", nil)
	}
	if len(scopes) == 0 {
		return "", InvalidInput("token needs at least one scope", nil)
	}
	known := make(map[string]struct{})
	for _, summary := range a.NamespaceSummaries() {
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
	username, _ := Username(ctx)
	if err := a.auth.RemoveToken(username, label); err != nil {
		return InvalidInput(err.Error(), err)
	}
	slog.Info("token revoked", "user", username, "label", label)
	return nil
}

// SeedFirstNamespace creates the initial namespace configuration, its index
// page and the root readme, then refreshes the registry. It is used by the
// first-run setup wizard, which is state-gated rather than scope-gated.
func (a *API) SeedFirstNamespace(ctx context.Context, name string) error {
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
