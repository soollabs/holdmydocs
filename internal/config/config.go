package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"

	"hmd/internal/presentation"
)

const (
	defaultSkin       = presentation.DefaultSkin
	semanticModelName = "BAAI/bge-small-en-v1.5"
)

var skinNames = presentation.SkinNames

// GitConfig contains Git remote settings.
type GitConfig struct {
	RemoteURL string `yaml:"remote_url,omitempty"`
	User      string `yaml:"user,omitempty"`
	Token     string `yaml:"token,omitempty"`
	TokenFile string `yaml:"token_file,omitempty"`
	Author    string `yaml:"author,omitempty"`
}

// MCPConfig contains MCP server settings.
type MCPConfig struct {
	Enabled bool `yaml:"enabled,omitempty"`
}

// OAuthConfig contains opt-in MCP OAuth policy. Client registrations and
// credentials live separately in private AppDir state.
type OAuthConfig struct {
	Enabled               bool `yaml:"enabled,omitempty"`
	DynamicRegistration   bool `yaml:"dynamic_registration,omitempty"`
	AllowInsecureLoopback bool `yaml:"allow_insecure_loopback,omitempty"`
	AllowAdminDelegation  bool `yaml:"allow_admin_delegation,omitempty"`
}

// DocumentSearchConfig contains local model and index settings.
type DocumentSearchConfig struct {
	Model    string `yaml:"model,omitempty"`
	ModelDir string `yaml:"model_dir,omitempty"`
	IndexDir string `yaml:"index_dir,omitempty"`
}

type oidcFileConfig struct {
	Issuer                string   `yaml:"issuer,omitempty"`
	ClientID              string   `yaml:"client_id,omitempty"`
	ClientSecretFile      string   `yaml:"client_secret_file,omitempty"`
	LocalLogin            *bool    `yaml:"local_login,omitempty"`
	ButtonText            string   `yaml:"button_text,omitempty"`
	Icon                  string   `yaml:"icon,omitempty"`
	DefaultScopes         []string `yaml:"default_scopes,omitempty"`
	AllowedSubjects       []string `yaml:"allowed_subjects,omitempty"`
	AllowedEmailDomains   []string `yaml:"allowed_email_domains,omitempty"`
	AllowAnyAuthenticated bool     `yaml:"allow_any_authenticated,omitempty"`
	AllowInsecureLoopback bool     `yaml:"allow_insecure_loopback,omitempty"`
}

// OIDCConfig contains resolved OIDC settings.
type OIDCConfig struct {
	Issuer                string
	ClientID              string
	ClientSecret          string
	ClientSecretFile      string
	LocalLogin            bool     // allow the password form alongside SSO
	ButtonText            string   // login button label, e.g. "Login with Authelia"
	Icon                  string   // Dashboard Icons name (e.g. "authelia") or path to a square SVG
	DefaultScopes         []string // scopes granted to newly provisioned OIDC users
	AllowedSubjects       []string // immutable identities permitted to sign in
	AllowedEmailDomains   []string // verified email domains permitted to sign in
	AllowAnyAuthenticated bool     // delegate admission to the identity provider
	AllowInsecureLoopback bool     // development only: permits http URLs on loopback
}

// Config is the install-wide runtime configuration.
type Config struct {
	ConfigFile string // resolved path of the YAML config file
	// EnvOverrides maps dotted config paths to their environment variable names.
	EnvOverrides map[string]string
	AppDir       string
	AdminUser    string
	AdminPass    string

	Bind           string
	RepoDir        string
	ReadOnly       bool
	MaxUploadBytes int64
	SyncPollMs     int
	SyncMode       string
	DefaultBranch  string
	Skin           string
	Debug          bool
	BaseURL        string
	TrustedProxies []string

	Git            GitConfig
	MCP            MCPConfig
	OAuth          OAuthConfig
	OIDC           OIDCConfig
	DocumentSearch DocumentSearchConfig
	TikaURL        string
}

// fileConfig is config.yaml. Each top-level section is one settings page and
// each nested object a group on it; a key's environment variable is HMD_ plus
// its upper-case dotted path (server.bind is HMD_SERVER_BIND).
type fileConfig struct {
	Server         serverFileConfig         `yaml:"server,omitempty"`
	Git            gitFileConfig            `yaml:"git,omitempty"`
	MCP            mcpFileConfig            `yaml:"mcp,omitempty"`
	OIDC           oidcFileConfig           `yaml:"oidc,omitempty"`
	DocumentSearch documentSearchFileConfig `yaml:"document_search,omitempty"`
}

type serverFileConfig struct {
	Bind           string   `yaml:"bind,omitempty"`
	RepoDir        string   `yaml:"repo_dir,omitempty"`
	BaseURL        string   `yaml:"base_url,omitempty"`
	TrustedProxies []string `yaml:"trusted_proxies,omitempty"`
	ReadOnly       bool     `yaml:"read_only,omitempty"`
	Debug          bool     `yaml:"debug,omitempty"`
	MaxUploadBytes *int64   `yaml:"max_upload_bytes,omitempty"`
	Skin           string   `yaml:"skin,omitempty"`
}

type gitFileConfig struct {
	RemoteURL  string `yaml:"remote_url,omitempty"`
	User       string `yaml:"user,omitempty"`
	Author     string `yaml:"author,omitempty"`
	TokenFile  string `yaml:"token_file,omitempty"`
	Branch     string `yaml:"branch,omitempty"`
	SyncMode   string `yaml:"sync_mode,omitempty"`
	SyncPollMs *int   `yaml:"sync_poll_ms,omitempty"`
}

type mcpFileConfig struct {
	Enabled bool        `yaml:"enabled,omitempty"`
	OAuth   OAuthConfig `yaml:"oauth,omitempty"`
}

type documentSearchFileConfig struct {
	TikaURL  string `yaml:"tika_url,omitempty"`
	Model    string `yaml:"model,omitempty"`
	ModelDir string `yaml:"model_dir,omitempty"`
	IndexDir string `yaml:"index_dir,omitempty"`
}

// EnvName returns the environment variable the schema names for a setting.
// Code and messages take names from here rather than spelling them out.
func EnvName(key string) string {
	s, _ := Lookup(key)
	return s.Env
}

// envOnly returns an x-env-only setting's environment variable and value.
func envOnly(key string) (name, value string) {
	name = EnvName(key)
	return name, os.Getenv(name)
}

// envOnlyOr returns an x-env-only setting's value, or def when it is unset.
func envOnlyOr(key, def string) string {
	if _, v := envOnly(key); v != "" {
		return v
	}
	return def
}

// envBool parses a boolean environment variable; empty means false.
func envBool(key, raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return b, nil
}

func applyEnvOverrides(v reflect.Value, envPrefix, pathPrefix string, applied map[string]string) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		tag, _, _ := strings.Cut(sf.Tag.Get("yaml"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		envKey := envPrefix + "_" + strings.ToUpper(tag)
		path := tag // EnvOverrides is keyed by the schema key, e.g. git.remote_url
		if pathPrefix != "" {
			path = pathPrefix + "." + tag
		}
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct {
			if err := applyEnvOverrides(fv, envKey, path, applied); err != nil {
				return err
			}
			continue
		}
		raw, ok := os.LookupEnv(envKey)
		if !ok || raw == "" {
			continue
		}
		switch {
		case fv.Kind() == reflect.String:
			fv.SetString(raw)
		case fv.Kind() == reflect.Bool:
			b, err := envBool(envKey, raw)
			if err != nil {
				return err
			}
			fv.SetBool(b)
		case fv.Kind() == reflect.Pointer && fv.Type().Elem().Kind() == reflect.Bool:
			b, err := envBool(envKey, raw)
			if err != nil {
				return err
			}
			fv.Set(reflect.ValueOf(&b))
		case fv.Kind() == reflect.Pointer && fv.Type().Elem().Kind() == reflect.Int:
			n, err := strconv.Atoi(raw)
			if err != nil {
				return fmt.Errorf("%s must be an integer: %w", envKey, err)
			}
			fv.Set(reflect.ValueOf(&n))
		case fv.Kind() == reflect.Pointer && fv.Type().Elem().Kind() == reflect.Int64:
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return fmt.Errorf("%s must be an integer: %w", envKey, err)
			}
			fv.Set(reflect.ValueOf(&n))
		default:
			continue
		}
		applied[path] = envKey
	}
	return nil
}

// ConfigFilePath returns the configured path to the YAML config file.
func ConfigFilePath() string {
	if _, f := envOnly("server.config_file"); f != "" {
		return f
	}
	return filepath.Join(envOnlyOr("server.app_dir", "/data/app"), "config.yaml")
}

// LoadConfig loads configuration from the YAML file and environment variables.
func LoadConfig() (Config, error) {
	file, err := LoadFileConfig(ConfigFilePath())
	if err != nil {
		if !os.IsNotExist(err) {
			return Config{}, err
		}
		file = fileConfig{}
	}
	return Resolve(file)
}

// Resolve applies environment overrides and defaults to a config.yaml and
// runs every check LoadConfig does. The settings screen resolves a candidate
// before writing it, so a saved file always loads.
func Resolve(file fileConfig) (Config, error) {
	path := ConfigFilePath()
	envOverrides := make(map[string]string)
	if err := applyEnvOverrides(reflect.ValueOf(&file).Elem(), "HMD", "", envOverrides); err != nil {
		return Config{}, err
	}
	tikaURL := strings.TrimSpace(file.DocumentSearch.TikaURL)
	if tikaURL != "" {
		tikaURL = strings.TrimRight(tikaURL, "/")
	}
	modelDir := file.DocumentSearch.ModelDir
	if modelDir == "" {
		modelDir = filepath.Join(envOnlyOr("server.app_dir", "/data/app"), "models")
	}

	or := func(fileVal, def string) string {
		if fileVal != "" {
			return fileVal
		}
		return def
	}
	orInt := func(fileVal *int, def int) int {
		if fileVal != nil {
			return *fileVal
		}
		return def
	}
	orInt64 := func(fileVal *int64, def int64) int64 {
		if fileVal != nil {
			return *fileVal
		}
		return def
	}
	orBool := func(fileVal *bool, def bool) bool {
		if fileVal != nil {
			return *fileVal
		}
		return def
	}

	cfg := Config{
		ConfigFile:   path,
		EnvOverrides: envOverrides,
		AppDir:       envOnlyOr("server.app_dir", "/data/app"),
		AdminUser:    envOnlyOr("server.admin.user", ""),
		AdminPass:    envOnlyOr("server.admin.password", ""),

		Bind:           or(file.Server.Bind, ":8080"),
		RepoDir:        or(file.Server.RepoDir, "/data/repo"),
		ReadOnly:       file.Server.ReadOnly,
		MaxUploadBytes: orInt64(file.Server.MaxUploadBytes, 10*1024*1024),
		SyncPollMs:     orInt(file.Git.SyncPollMs, 10000),
		SyncMode:       or(file.Git.SyncMode, "push"),
		DefaultBranch:  or(file.Git.Branch, "main"),
		Skin:           or(file.Server.Skin, defaultSkin),
		Debug:          file.Server.Debug,
		BaseURL:        strings.TrimRight(file.Server.BaseURL, "/"),
		TrustedProxies: append([]string(nil), file.Server.TrustedProxies...),

		Git: GitConfig{
			RemoteURL: file.Git.RemoteURL,
			User:      or(file.Git.User, "hmd"),
			Token:     envOnlyOr("git.token", ""),
			TokenFile: file.Git.TokenFile,
			Author:    file.Git.Author,
		},
		MCP:   MCPConfig{Enabled: file.MCP.Enabled},
		OAuth: file.MCP.OAuth,
		OIDC: OIDCConfig{
			Issuer:                file.OIDC.Issuer,
			ClientID:              file.OIDC.ClientID,
			ClientSecret:          envOnlyOr("oidc.client_secret", ""),
			ClientSecretFile:      file.OIDC.ClientSecretFile,
			LocalLogin:            orBool(file.OIDC.LocalLogin, true),
			ButtonText:            or(file.OIDC.ButtonText, "Sign in with SSO"),
			Icon:                  file.OIDC.Icon,
			DefaultScopes:         append([]string(nil), file.OIDC.DefaultScopes...),
			AllowedSubjects:       append([]string(nil), file.OIDC.AllowedSubjects...),
			AllowedEmailDomains:   append([]string(nil), file.OIDC.AllowedEmailDomains...),
			AllowAnyAuthenticated: file.OIDC.AllowAnyAuthenticated,
			AllowInsecureLoopback: file.OIDC.AllowInsecureLoopback,
		},
		DocumentSearch: DocumentSearchConfig{
			Model:    or(file.DocumentSearch.Model, semanticModelName),
			ModelDir: modelDir,
			IndexDir: file.DocumentSearch.IndexDir,
		},
		TikaURL: tikaURL,
	}

	if cfg.Git.Token != "" && cfg.Git.TokenFile != "" {
		name, _ := envOnly("git.token")
		return Config{}, fmt.Errorf("%s and git.token_file are mutually exclusive", name)
	}
	if cfg.OIDC.ClientSecret != "" && cfg.OIDC.ClientSecretFile != "" {
		name, _ := envOnly("oidc.client_secret")
		return Config{}, fmt.Errorf("%s and oidc.client_secret_file are mutually exclusive", name)
	}
	if f := cfg.Git.TokenFile; f != "" {
		secret, err := readSecretFile(f)
		if err != nil {
			return Config{}, fmt.Errorf("reading git token file: %w", err)
		}
		cfg.Git.Token = secret
	}
	if f := cfg.OIDC.ClientSecretFile; f != "" {
		secret, err := readSecretFile(f)
		if err != nil {
			return Config{}, fmt.Errorf("reading oidc client secret file: %w", err)
		}
		cfg.OIDC.ClientSecret = secret
	}

	if err := ValidateFile(cfg.toFileConfig()); err != nil {
		return Config{}, err
	}
	if cfg.MCP.Enabled && cfg.BaseURL == "" {
		return Config{}, fmt.Errorf("server.base_url must be set when mcp.enabled is true")
	}
	if cfg.OAuth.Enabled {
		if !cfg.MCP.Enabled {
			return Config{}, fmt.Errorf("mcp.enabled must be true when mcp.oauth.enabled is true")
		}
		if cfg.BaseURL == "" {
			return Config{}, fmt.Errorf("server.base_url must be set when mcp.oauth.enabled is true")
		}
		if err := validateOAuthOrigin(cfg.BaseURL, cfg.OAuth.AllowInsecureLoopback); err != nil {
			return Config{}, err
		}
	}

	if cfg.OIDC.Issuer != "" {
		if cfg.OIDC.ClientID == "" || cfg.OIDC.ClientSecret == "" {
			name, _ := envOnly("oidc.client_secret")
			return Config{}, fmt.Errorf("oidc.client_id and %s (or oidc.client_secret_file) must be set when oidc.issuer is set", name)
		}
		if cfg.BaseURL == "" {
			return Config{}, fmt.Errorf("server.base_url must be set when oidc.issuer is set (needed for the redirect URI)")
		}
		if err := validateOIDCURL("oidc.issuer", cfg.OIDC.Issuer, cfg.OIDC.AllowInsecureLoopback); err != nil {
			return Config{}, err
		}
		if !cfg.OIDC.AllowAnyAuthenticated && len(cfg.OIDC.AllowedSubjects) == 0 && len(cfg.OIDC.AllowedEmailDomains) == 0 {
			return Config{}, fmt.Errorf("oidc.allowed_subjects or oidc.allowed_email_domains must be set when oidc.issuer is set")
		}
		if len(cfg.OIDC.DefaultScopes) == 0 {
			cfg.OIDC.DefaultScopes = []string{"read"}
		}
		scopes, err := normaliseTokenScopes(cfg.OIDC.DefaultScopes)
		if err != nil || len(scopes) == 0 {
			return Config{}, fmt.Errorf("invalid oidc.default_scopes")
		}
		cfg.OIDC.DefaultScopes = scopes
	}

	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) toFileConfig() fileConfig {
	return fileConfig{
		Server: serverFileConfig{
			Bind:           c.Bind,
			RepoDir:        c.RepoDir,
			BaseURL:        c.BaseURL,
			TrustedProxies: append([]string(nil), c.TrustedProxies...),
			ReadOnly:       c.ReadOnly,
			Debug:          c.Debug,
			MaxUploadBytes: new(c.MaxUploadBytes),
			Skin:           c.Skin,
		},
		Git: gitFileConfig{
			RemoteURL:  c.Git.RemoteURL,
			User:       c.Git.User,
			Author:     c.Git.Author,
			TokenFile:  c.Git.TokenFile,
			Branch:     c.DefaultBranch,
			SyncMode:   c.SyncMode,
			SyncPollMs: new(c.SyncPollMs),
		},
		MCP: mcpFileConfig{Enabled: c.MCP.Enabled, OAuth: c.OAuth},
		OIDC: oidcFileConfig{
			Issuer:                c.OIDC.Issuer,
			ClientID:              c.OIDC.ClientID,
			ClientSecretFile:      c.OIDC.ClientSecretFile,
			LocalLogin:            new(c.OIDC.LocalLogin),
			ButtonText:            c.OIDC.ButtonText,
			Icon:                  c.OIDC.Icon,
			DefaultScopes:         append([]string(nil), c.OIDC.DefaultScopes...),
			AllowedSubjects:       append([]string(nil), c.OIDC.AllowedSubjects...),
			AllowedEmailDomains:   append([]string(nil), c.OIDC.AllowedEmailDomains...),
			AllowAnyAuthenticated: c.OIDC.AllowAnyAuthenticated,
			AllowInsecureLoopback: c.OIDC.AllowInsecureLoopback,
		},
		DocumentSearch: documentSearchFileConfig{
			TikaURL:  c.TikaURL,
			Model:    c.DocumentSearch.Model,
			ModelDir: c.DocumentSearch.ModelDir,
			IndexDir: c.DocumentSearch.IndexDir,
		},
	}
}

// ToFileConfig returns the safe persisted configuration form.
func (c Config) ToFileConfig() fileConfig { return c.toFileConfig() }

func validateOAuthOrigin(raw string, allowInsecureLoopback bool) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("server.base_url must be an origin without path, query, fragment or user-info when OAuth is enabled")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && allowInsecureLoopback && isLoopbackHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("OAuth requires an https server.base_url (http is allowed only for loopback with mcp.oauth.allow_insecure_loopback)")
}

func validateOIDCURL(field, raw string, allowInsecureLoopback bool) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%s must be an absolute URL without user-info or fragment", field)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && allowInsecureLoopback && isLoopbackHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%s must use https (http is allowed only for loopback with oidc.allow_insecure_loopback)", field)
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

// parseAuthor splits a git author string in the standard "Name <email>" form into name and email.
func ParseAuthor(s, fallbackName string) (name, email string) {
	s = strings.TrimSpace(s)
	if lt := strings.LastIndex(s, "<"); lt != -1 && strings.HasSuffix(s, ">") {
		email = strings.TrimSpace(s[lt+1 : len(s)-1])
		name = strings.TrimSpace(s[:lt])
	} else {
		name = s
	}
	if name == "" {
		name = fallbackName
	}
	if email == "" {
		email = fallbackName + "@hmd.local"
	}
	return name, email
}

// LoadFileConfig reads and parses a YAML config file into a fileConfig.
func LoadFileConfig(path string) (fileConfig, error) {
	var fc fileConfig
	if err := tightenRegularFile(path); err != nil && !os.IsNotExist(err) {
		return fileConfig{}, fmt.Errorf("checking config file %s: %w", path, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fileConfig{}, err
	}

	if err := yaml.UnmarshalWithOptions(b, &fc, yaml.Strict()); err != nil {
		return fileConfig{}, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	return fc, nil
}

// SaveFileConfig atomically writes a YAML configuration file.
func SaveFileConfig(path string, fc fileConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	b, err := yaml.MarshalWithOptions(fc, yaml.Indent(2))
	if err != nil {
		return fmt.Errorf("marshalling config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return fmt.Errorf("writing tmp config file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming tmp config file: %w", err)
	}
	return nil
}

const maxSecretBytes = 64 << 10

func readSecretFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("must be an owned, non-world-readable regular file")
	}
	if info.Size() > maxSecretBytes {
		return "", fmt.Errorf("must not exceed %d bytes", maxSecretBytes)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(b))
	if secret == "" {
		return "", fmt.Errorf("must not be empty")
	}
	return secret, nil
}

func TightenRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !ownedByCurrentUser(info) {
		return fmt.Errorf("must be an owned regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return os.Chmod(path, 0600)
	}
	return nil
}

func tightenRegularFile(path string) error { return TightenRegularFile(path) }

func normaliseTokenScopes(scopes []string) ([]string, error) {
	allowed := map[string]bool{"read": true, "write": true, "settings": true}
	seen := make(map[string]bool, len(scopes))
	for _, raw := range scopes {
		name := strings.TrimSpace(raw)
		if !allowed[name] {
			return nil, fmt.Errorf("unknown scope %q", name)
		}
		seen[name] = true
	}
	if len(seen) == 0 {
		return nil, nil
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	slices.Sort(result)
	return result, nil
}

func validateConfig(cfg Config) error {
	// Per-value rules (formats, ranges, enums) live in the schema; the checks
	// below are the cross-field and filesystem rules a schema cannot express.
	if err := ValidateFile(cfg.toFileConfig()); err != nil {
		return err
	}
	app, err := filepath.Abs(cfg.AppDir)
	if err != nil {
		return fmt.Errorf("resolving app directory: %w", err)
	}
	repo, err := filepath.Abs(cfg.RepoDir)
	if err != nil {
		return fmt.Errorf("resolving repository directory: %w", err)
	}
	if pathsOverlap(app, repo) {
		return fmt.Errorf("app_dir and repo_dir must not overlap")
	}
	model, err := filepath.Abs(cfg.DocumentSearch.ModelDir)
	if err != nil {
		return fmt.Errorf("resolving model directory: %w", err)
	}
	if cfg.DocumentSearch.IndexDir != "" {
		index, err := filepath.Abs(cfg.DocumentSearch.IndexDir)
		if err != nil {
			return fmt.Errorf("resolving index directory: %w", err)
		}
		if pathsOverlap(model, index) || pathsOverlap(repo, index) {
			return fmt.Errorf("document model, index and repository directories must not overlap")
		}
	}
	if pathsOverlap(repo, model) {
		return fmt.Errorf("document model and repository directories must not overlap")
	}
	return nil
}

// ValidateWritableDataDir ensures the directory is private and owned by the runtime user.
func ValidateWritableDataDir(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0700); err != nil {
			return fmt.Errorf("creating: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || !ownedByCurrentUser(info) || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("must be an owned private directory (mode 0700); recreate root-owned container volumes")
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	rel, err := filepath.Rel(a, b)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

func validBranchName(name string) bool {
	return name != "" && !strings.HasPrefix(name, "-") && !strings.HasSuffix(name, "/") && !strings.Contains(name, "..") && !strings.ContainsAny(name, " ~^:?*[\\")
}
