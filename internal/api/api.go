// Package api holds shared in-process application operations: permissions,
// validation, mutation coordination and the runtime snapshots they read. It is
// transport-independent and is consumed directly by the browser, HTTP data and
// MCP adapters.
package api

import (
	"context"
	"sync"
	"sync/atomic"

	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/i18n"
	"hmd/internal/search"
	"hmd/internal/store"
	"hmd/internal/wiki"
)

// API holds the explicit private dependencies and shared runtime snapshots used
// by application operations. It is not a public bag of Store and Index fields;
// adapters reach persistence and indexing through operations, not directly.
type API struct {
	store            *store.Store
	index            *search.Index
	auth             *auth.Auth
	uploadAuthorizer OAuthUploadAuthorizer
	shutdown         func()

	cfg        atomic.Pointer[config.Config]
	savedCfg   atomic.Pointer[config.Config]
	wikiCfg    atomic.Pointer[wiki.WikiConfig]
	namespaces atomic.Pointer[wiki.NamespaceRegistry]

	uploadMu sync.Mutex
	uploads  map[string]UploadCapability

	indexFailures atomic.Int64
}

// OAuthUploadAuthorizer rechecks an OAuth grant when a detached upload
// capability is redeemed. It is deliberately narrow and keeps api independent
// of the OAuth implementation.
type OAuthUploadAuthorizer interface {
	AuthorizeUpload(ctx context.Context, grantID, familyID, user, slug string, issuedScopes []string) bool
}

// SetOAuthUploadAuthorizer installs the OAuth-specific capability verifier.
// It is called during application composition before serving requests.
func (a *API) SetOAuthUploadAuthorizer(authorizer OAuthUploadAuthorizer) {
	a.uploadAuthorizer = authorizer
}

// New constructs an API over its dependencies. Snapshots start empty and are
// filled by SetConfig, SetWikiConfig and SetNamespaces during startup.
func New(content *store.Store, index *search.Index, authn *auth.Auth) *API {
	a := &API{store: content, index: index, auth: authn}
	empty := wiki.NamespaceRegistry{}
	a.namespaces.Store(&empty)
	return a
}

// SetShutdown installs the function that begins a graceful process shutdown.
// It is called during application composition before serving requests.
func (a *API) SetShutdown(shutdown func()) {
	a.shutdown = shutdown
}

// Config returns the current runtime configuration.
func (a *API) Config() config.Config {
	if cfg := a.cfg.Load(); cfg != nil {
		return *cfg
	}
	return config.Config{}
}

// HasConfig reports whether a configuration snapshot has been stored.
func (a *API) HasConfig() bool { return a.cfg.Load() != nil }

// SetConfig stores a new configuration value atomically.
func (a *API) SetConfig(cfg config.Config) {
	a.cfg.Store(&cfg)
	// The language is server-wide, so the process-wide translator follows it.
	// Config validation already limited it to a known catalogue.
	_ = i18n.SetLanguage(cfg.Language)
}

// SavedConfig returns the configuration as saved, which can differ from
// Config in settings that only take effect after a restart.
func (a *API) SavedConfig() config.Config {
	if cfg := a.savedCfg.Load(); cfg != nil {
		return *cfg
	}
	return a.Config()
}

// applySavedConfig stores a configuration that was just saved. Settings that
// take effect live apply now; restart-only ones keep their running value
// until the process restarts, so they never apply halfway.
func (a *API) applySavedConfig(saved config.Config) {
	a.savedCfg.Store(&saved)
	a.SetConfig(config.KeepRestartValues(a.Config(), saved))
}

// PendingRestart lists the saved settings that wait for a restart to take
// effect, in schema order. It is empty when the saved and running
// configuration agree.
func (a *API) PendingRestart() []config.Setting {
	return config.PendingRestart(a.Config(), a.SavedConfig())
}

// WikiConfig returns the current repository-level settings.
func (a *API) WikiConfig() wiki.WikiConfig {
	if cfg := a.wikiCfg.Load(); cfg != nil {
		return *cfg
	}
	return wiki.DefaultConfig()
}

// SetWikiConfig stores new repository-level settings atomically.
func (a *API) SetWikiConfig(cfg wiki.WikiConfig) {
	cfg = cfg.Normalised()
	a.wikiCfg.Store(&cfg)
}

// Namespaces returns the current namespace registry snapshot.
func (a *API) Namespaces() wiki.NamespaceRegistry {
	if reg := a.namespaces.Load(); reg != nil {
		return *reg
	}
	return wiki.NamespaceRegistry{}
}

// SetNamespaces stores a new namespace registry snapshot atomically.
func (a *API) SetNamespaces(reg wiki.NamespaceRegistry) {
	if reg == nil {
		reg = wiki.NamespaceRegistry{}
	}
	a.namespaces.Store(&reg)
}
