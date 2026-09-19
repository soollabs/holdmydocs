package httpapi

import (
	"context"
	"net/http"
	"time"

	"hmd/internal/api"
)

// okResult is the JSON body of a mutation that has nothing else to report.
type okResult struct {
	OK bool `json:"ok"`
}

// serverSettingsInput is the JSON body of the install-wide configuration form.
// It mirrors the settings screen's field names so the browser posts the typed
// values directly; secrets are accepted only to persist them and are never
// echoed back.
type serverSettingsInput struct {
	Bind           string   `json:"bind"`
	RepoDir        string   `json:"repo_dir"`
	MaxUploadBytes int64    `json:"max_upload_bytes"`
	SyncPollMs     int      `json:"sync_poll_ms"`
	SyncMode       string   `json:"sync_mode"`
	DefaultBranch  string   `json:"default_branch"`
	Skin           string   `json:"skin"`
	Debug          bool     `json:"debug"`
	BaseURL        string   `json:"base_url"`
	TrustedProxies []string `json:"trusted_proxies"`

	RemoteURL     string `json:"remote_url"`
	GitUser       string `json:"git_user"`
	GitAuthor     string `json:"git_author"`
	GitToken      string `json:"git_token"`
	StoreGitToken bool   `json:"store_git_token"`
	GitTokenFile  string `json:"git_token_file"`

	MCPEnabled bool `json:"mcp_enabled"`

	DocumentModel    string `json:"document_model"`
	DocumentModelDir string `json:"document_model_dir"`
	DocumentIndexDir string `json:"document_index_dir"`

	OIDCIssuer                string   `json:"oidc_issuer"`
	OIDCClientID              string   `json:"oidc_client_id"`
	OIDCClientSecret          string   `json:"oidc_client_secret"`
	OIDCClientSecretFile      string   `json:"oidc_client_secret_file"`
	OIDCLocalLogin            bool     `json:"oidc_local_login"`
	OIDCButtonText            string   `json:"oidc_button_text"`
	OIDCIcon                  string   `json:"oidc_icon"`
	OIDCDefaultScopes         []string `json:"oidc_default_scopes"`
	OIDCAllowedSubjects       []string `json:"oidc_allowed_subjects"`
	OIDCAllowedEmailDomains   []string `json:"oidc_allowed_email_domains"`
	OIDCAllowAnyAuthenticated bool     `json:"oidc_allow_any_authenticated"`
	OIDCAllowInsecureLoopback bool     `json:"oidc_allow_insecure_loopback"`
}

// saveServerSettings persists the install-wide configuration.
func (h *Handlers) saveServerSettings(ctx context.Context, in serverSettingsInput) (okResult, error) {
	if err := h.api.SaveServerSettings(ctx, api.ServerSettingsInput{
		Bind:           in.Bind,
		RepoDir:        in.RepoDir,
		MaxUploadBytes: in.MaxUploadBytes,
		SyncPollMs:     in.SyncPollMs,
		SyncMode:       in.SyncMode,
		DefaultBranch:  in.DefaultBranch,
		Skin:           in.Skin,
		Debug:          in.Debug,
		BaseURL:        in.BaseURL,
		TrustedProxies: in.TrustedProxies,

		RemoteURL:     in.RemoteURL,
		GitUser:       in.GitUser,
		GitAuthor:     in.GitAuthor,
		GitToken:      in.GitToken,
		StoreGitToken: in.StoreGitToken,
		GitTokenFile:  in.GitTokenFile,

		MCPEnabled: in.MCPEnabled,

		DocumentModel:    in.DocumentModel,
		DocumentModelDir: in.DocumentModelDir,
		DocumentIndexDir: in.DocumentIndexDir,

		OIDCIssuer:                in.OIDCIssuer,
		OIDCClientID:              in.OIDCClientID,
		OIDCClientSecret:          in.OIDCClientSecret,
		OIDCClientSecretFile:      in.OIDCClientSecretFile,
		OIDCLocalLogin:            in.OIDCLocalLogin,
		OIDCButtonText:            in.OIDCButtonText,
		OIDCIcon:                  in.OIDCIcon,
		OIDCDefaultScopes:         in.OIDCDefaultScopes,
		OIDCAllowedSubjects:       in.OIDCAllowedSubjects,
		OIDCAllowedEmailDomains:   in.OIDCAllowedEmailDomains,
		OIDCAllowAnyAuthenticated: in.OIDCAllowAnyAuthenticated,
		OIDCAllowInsecureLoopback: in.OIDCAllowInsecureLoopback,
	}); err != nil {
		return okResult{}, err
	}
	return okResult{OK: true}, nil
}

// appearanceInput is the JSON body of the per-user appearance form. The
// browser resolves a skin switch's palette reset and reports the palette it
// actually intends with PaletteExplicit.
type appearanceInput struct {
	Palette         string `json:"palette"`
	FontUI          string `json:"font_ui"`
	FontMono        string `json:"font_mono"`
	Skin            string `json:"skin"`
	PaletteExplicit bool   `json:"palette_explicit"`
}

// setAppearance stores the caller's appearance preferences.
func (h *Handlers) setAppearance(ctx context.Context, in appearanceInput) (okResult, error) {
	if err := h.api.SetAppearance(ctx, in.Palette, in.FontUI, in.FontMono, in.Skin, in.PaletteExplicit); err != nil {
		return okResult{}, err
	}
	return okResult{OK: true}, nil
}

// authorInput is the JSON body of the per-user git author form.
type authorInput struct {
	GitAuthor string `json:"git_author"`
}

// setGitAuthor stores the caller's git author override.
func (h *Handlers) setGitAuthor(ctx context.Context, in authorInput) (okResult, error) {
	if err := h.api.SetGitAuthor(ctx, in.GitAuthor); err != nil {
		return okResult{}, err
	}
	return okResult{OK: true}, nil
}

// noneIn is the empty JSON body of a mutation that takes no input.
type noneIn struct{}

// exportServerConfig bakes the resolved runtime configuration into the file.
func (h *Handlers) exportServerConfig(ctx context.Context, _ noneIn) (okResult, error) {
	if err := h.api.ExportServerConfig(ctx); err != nil {
		return okResult{}, err
	}
	return okResult{OK: true}, nil
}

// wikiConfigInput is the JSON body of the repository-level wiki settings form.
type wikiConfigInput struct {
	SiteName string `json:"site_name"`
	Landing  string `json:"landing"`
}

// saveWikiConfig persists the repository landing and site title.
func (h *Handlers) saveWikiConfig(ctx context.Context, in wikiConfigInput) (okResult, error) {
	if err := h.api.SaveWikiConfig(ctx, api.WikiConfigInput{Landing: in.Landing, SiteName: in.SiteName}); err != nil {
		return okResult{}, err
	}
	return okResult{OK: true}, nil
}

// resetHelp rewrites the built-in help guide.
func (h *Handlers) resetHelp(ctx context.Context, _ noneIn) (okResult, error) {
	if err := h.api.ResetHelp(ctx); err != nil {
		return okResult{}, err
	}
	return okResult{OK: true}, nil
}

// rerunSetup requests the first-run setup wizard on the next navigation.
func (h *Handlers) rerunSetup(ctx context.Context, _ noneIn) (okResult, error) {
	if err := h.api.RerunSetup(ctx); err != nil {
		return okResult{}, err
	}
	return okResult{OK: true}, nil
}

// createUserInput is the JSON body of the admin "create user" form.
type createUserInput struct {
	Name     string   `json:"name"`
	Password string   `json:"password"`
	Scopes   []string `json:"scopes"`
}

// createUser adds a user with the given scopes.
func (h *Handlers) createUser(ctx context.Context, in createUserInput) (okResult, error) {
	if err := h.api.CreateUser(ctx, in.Name, in.Password, in.Scopes); err != nil {
		return okResult{}, err
	}
	return okResult{OK: true}, nil
}

// setUserScopesInput is the JSON body of the admin scope-update form.
type setUserScopesInput struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

// setUserScopes replaces a user's scopes.
func (h *Handlers) setUserScopes(ctx context.Context, in setUserScopesInput) (okResult, error) {
	if err := h.api.SetUserScopes(ctx, in.Name, in.Scopes); err != nil {
		return okResult{}, err
	}
	return okResult{OK: true}, nil
}

// createTokenInput is the JSON body of the personal access token form. Expiry
// is the form's choice key; the adapter maps it to an absolute time.
type createTokenInput struct {
	Label      string   `json:"label"`
	Expiry     string   `json:"expiry"`
	Scopes     []string `json:"scopes"`
	Namespaces []string `json:"namespaces"`
}

// tokenCreated is the JSON result of a minted token, shown exactly once.
type tokenCreated struct {
	Token string `json:"token"`
}

// createToken mints a personal access token for the caller.
func (h *Handlers) createToken(ctx context.Context, in createTokenInput) (tokenCreated, error) {
	ttl, ok := api.TokenTTLs[in.Expiry]
	if !ok {
		ttl = api.TokenTTLs["30d"]
	}
	var expires time.Time
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}
	token, err := h.api.CreateToken(ctx, in.Label, expires, in.Scopes, in.Namespaces)
	if err != nil {
		return tokenCreated{}, err
	}
	return tokenCreated{Token: token}, nil
}

// revokeTokenInput is the JSON body of the token revoke form.
type revokeTokenInput struct {
	Label string `json:"label"`
}

// revokeToken removes one of the caller's personal access tokens.
func (h *Handlers) revokeToken(ctx context.Context, in revokeTokenInput) (okResult, error) {
	if err := h.api.RevokeToken(ctx, in.Label); err != nil {
		return okResult{}, err
	}
	return okResult{OK: true}, nil
}

// setupInput is the JSON body of the first-run setup wizard.
type setupInput struct {
	Action           string `json:"action"`
	SetupWiki        bool   `json:"setup_wiki"`
	DefaultNamespace string `json:"default_namespace"`
	NewNamespace     string `json:"new_namespace"`
	SiteName         string `json:"site_name"`
	AddNamespace     bool   `json:"add_namespace"`
	Namespace        string `json:"namespace"`
	AddHelp          bool   `json:"add_help"`
}

// completeSetup applies the first-run setup wizard's selected seeds.
func (h *Handlers) completeSetup(ctx context.Context, in setupInput) (okResult, error) {
	if err := h.api.CompleteFirstRunSetup(ctx, api.FirstRunSetupInput{
		Action:           in.Action,
		SetupWiki:        in.SetupWiki,
		DefaultNamespace: in.DefaultNamespace,
		NewNamespace:     in.NewNamespace,
		SiteName:         in.SiteName,
		AddNamespace:     in.AddNamespace,
		Namespace:        in.Namespace,
		AddHelp:          in.AddHelp,
	}); err != nil {
		return okResult{}, err
	}
	return okResult{OK: true}, nil
}

// registerSettings mounts the settings, admin and setup mutation endpoints.
func (h *Handlers) registerSettings(mux *http.ServeMux) {
	h.registerJSON(mux, "POST /_/api/settings/server", h.saveServerSettings)
	h.registerJSON(mux, "POST /_/api/settings/appearance", h.setAppearance)
	h.registerJSON(mux, "POST /_/api/settings/author", h.setGitAuthor)
	h.registerJSON(mux, "POST /_/api/settings/export", h.exportServerConfig)
	h.registerJSON(mux, "POST /_/api/settings/wiki", h.saveWikiConfig)
	h.registerJSON(mux, "POST /_/api/settings/help/reset", h.resetHelp)
	h.registerJSON(mux, "POST /_/api/settings/setup", h.rerunSetup)
	h.registerJSON(mux, "POST /_/api/settings/users", h.createUser)
	h.registerJSON(mux, "POST /_/api/settings/users/scopes", h.setUserScopes)
	h.registerJSON(mux, "POST /_/api/settings/tokens", h.createToken)
	h.registerJSON(mux, "POST /_/api/settings/tokens/revoke", h.revokeToken)
	h.registerJSON(mux, "POST /_/api/setup", h.completeSetup)
}
