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
)

const (
	defaultSkin       = "phosphor"
	semanticModelName = "BAAI/bge-small-en-v1.5"
)

var skinNames = []string{"phosphor", "newsprint", "journal", "soft", "bare"}

// GitConfig contains Git remote settings.
type GitConfig struct {
	RemoteURL string `yaml:"remote_url"`
	User      string `yaml:"user"`
	Token     string `yaml:"token"`
	TokenFile string `yaml:"token_file"`
	Author    string `yaml:"author"`
}

// MCPConfig contains MCP server settings.
type MCPConfig struct {
	Enabled bool `yaml:"enabled"`
}

// DocumentSearchConfig contains local model and index settings.
type DocumentSearchConfig struct {
	Model    string `yaml:"model"`
	ModelDir string `yaml:"model_dir"`
	IndexDir string `yaml:"index_dir"`
}

type oidcFileConfig struct {
	Issuer                string   `yaml:"issuer"`
	ClientID              string   `yaml:"client_id"`
	ClientSecret          string   `yaml:"client_secret"`
	ClientSecretFile      string   `yaml:"client_secret_file"`
	LocalLogin            *bool    `yaml:"local_login"`
	ButtonText            string   `yaml:"button_text"`
	Icon                  string   `yaml:"icon"`
	DefaultScopes         []string `yaml:"default_scopes"`
	AllowedSubjects       []string `yaml:"allowed_subjects"`
	AllowedEmailDomains   []string `yaml:"allowed_email_domains"`
	AllowAnyAuthenticated bool     `yaml:"allow_any_authenticated"`
	AllowInsecureLoopback bool     `yaml:"allow_insecure_loopback"`
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
	OIDC           OIDCConfig
	DocumentSearch DocumentSearchConfig
	TikaURL        string
}

type fileConfig struct {
	Bind           string   `yaml:"bind"`
	RepoDir        string   `yaml:"repo_dir"`
	MaxUploadBytes *int64   `yaml:"max_upload_bytes"`
	SyncPollMs     *int     `yaml:"sync_poll_ms"`
	SyncMode       string   `yaml:"sync_mode"`
	DefaultBranch  string   `yaml:"default_branch"`
	Skin           string   `yaml:"skin"`
	Debug          bool     `yaml:"debug"`
	BaseURL        string   `yaml:"base_url"`
	TrustedProxies []string `yaml:"trusted_proxies"`

	Git            GitConfig            `yaml:"git"`
	MCP            MCPConfig            `yaml:"mcp"`
	OIDC           oidcFileConfig       `yaml:"oidc"`
	DocumentSearch DocumentSearchConfig `yaml:"document_search"`
}

func EnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func applyEnvOverrides(v reflect.Value, envPrefix, pathPrefix string, applied map[string]string) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		tag := sf.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		envKey := envPrefix + "_" + strings.ToUpper(tag)
		path := sf.Name
		if pathPrefix != "" {
			path = pathPrefix + "." + sf.Name
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
			b, err := strconv.ParseBool(raw)
			if err != nil {
				return fmt.Errorf("%s must be a boolean: %w", envKey, err)
			}
			fv.SetBool(b)
		case fv.Kind() == reflect.Pointer && fv.Type().Elem().Kind() == reflect.Bool:
			b, err := strconv.ParseBool(raw)
			if err != nil {
				return fmt.Errorf("%s must be a boolean: %w", envKey, err)
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
	if f := os.Getenv("HMD_CONFIG_FILE"); f != "" {
		return f
	}
	return filepath.Join(EnvOr("HMD_APP_DIR", "/data/app"), "config.yaml")
}

// LoadConfig loads configuration from the YAML file and environment variables.
func LoadConfig() (Config, error) {
	path := ConfigFilePath()
	file, err := LoadFileConfig(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return Config{}, err
		}
		file = fileConfig{}
	}
	envOverrides := make(map[string]string)
	if err := applyEnvOverrides(reflect.ValueOf(&file).Elem(), "HMD", "", envOverrides); err != nil {
		return Config{}, err
	}
	tikaURL := strings.TrimSpace(os.Getenv("HMD_TIKA_URL"))
	if tikaURL != "" {
		tikaURL = strings.TrimRight(tikaURL, "/")
		u, parseErr := url.Parse(tikaURL)
		if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return Config{}, fmt.Errorf("HMD_TIKA_URL must be an http or https URL with a host and no user-info")
		}
		envOverrides["TikaURL"] = "HMD_TIKA_URL"
	}
	modelDir := file.DocumentSearch.ModelDir
	if modelDir == "" {
		modelDir = filepath.Join(EnvOr("HMD_APP_DIR", "/data/app"), "models")
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
		AppDir:       EnvOr("HMD_APP_DIR", "/data/app"),
		AdminUser:    os.Getenv("HMD_ADMIN_USER"),
		AdminPass:    os.Getenv("HMD_ADMIN_PASSWORD"),

		Bind:           or(file.Bind, ":8080"),
		RepoDir:        or(file.RepoDir, "/data/repo"),
		MaxUploadBytes: orInt64(file.MaxUploadBytes, 10*1024*1024),
		SyncPollMs:     orInt(file.SyncPollMs, 10000),
		SyncMode:       or(file.SyncMode, "push"),
		DefaultBranch:  or(file.DefaultBranch, "main"),
		Skin:           or(file.Skin, defaultSkin),
		Debug:          file.Debug,
		BaseURL:        strings.TrimRight(file.BaseURL, "/"),
		TrustedProxies: append([]string(nil), file.TrustedProxies...),

		Git: GitConfig{
			RemoteURL: file.Git.RemoteURL,
			User:      or(file.Git.User, "hmd"),
			Token:     file.Git.Token,
			TokenFile: file.Git.TokenFile,
			Author:    file.Git.Author,
		},
		MCP: file.MCP,
		OIDC: OIDCConfig{
			Issuer:                file.OIDC.Issuer,
			ClientID:              file.OIDC.ClientID,
			ClientSecret:          file.OIDC.ClientSecret,
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
		return Config{}, fmt.Errorf("git.token and git.token_file are mutually exclusive")
	}
	if cfg.OIDC.ClientSecret != "" && cfg.OIDC.ClientSecretFile != "" {
		return Config{}, fmt.Errorf("oidc.client_secret and oidc.client_secret_file are mutually exclusive")
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

	if cfg.SyncMode != "push" && cfg.SyncMode != "bidirectional" {
		return Config{}, fmt.Errorf("invalid sync_mode %q: must be push or bidirectional", cfg.SyncMode)
	}

	if !slices.Contains(skinNames, cfg.Skin) {
		return Config{}, fmt.Errorf("invalid skin %q: must be one of %v", cfg.Skin, skinNames)
	}
	if cfg.BaseURL != "" {
		if err := validateExternalURL("base_url", cfg.BaseURL); err != nil {
			return Config{}, err
		}
	}
	for _, cidr := range cfg.TrustedProxies {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return Config{}, fmt.Errorf("invalid trusted_proxies entry %q", cidr)
		}
	}
	if cfg.MCP.Enabled && cfg.BaseURL == "" {
		return Config{}, fmt.Errorf("base_url must be set when mcp.enabled is true")
	}

	if cfg.OIDC.Issuer != "" {
		if cfg.OIDC.ClientID == "" || cfg.OIDC.ClientSecret == "" {
			return Config{}, fmt.Errorf("oidc.client_id/oidc.client_secret must be set when oidc.issuer is set")
		}
		if cfg.BaseURL == "" {
			return Config{}, fmt.Errorf("base_url must be set when oidc.issuer is set (needed for the redirect URI)")
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
		for _, domain := range cfg.OIDC.AllowedEmailDomains {
			if strings.TrimSpace(domain) == "" || strings.ContainsAny(domain, "@/ ") {
				return Config{}, fmt.Errorf("invalid oidc.allowed_email_domains entry %q", domain)
			}
		}
	}

	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) toFileConfig() fileConfig {
	git := c.Git
	git.Token = ""
	return fileConfig{
		Bind:           c.Bind,
		RepoDir:        c.RepoDir,
		MaxUploadBytes: new(c.MaxUploadBytes),
		SyncPollMs:     new(c.SyncPollMs),
		SyncMode:       c.SyncMode,
		DefaultBranch:  c.DefaultBranch,
		Skin:           c.Skin,
		Debug:          c.Debug,
		BaseURL:        c.BaseURL,
		TrustedProxies: append([]string(nil), c.TrustedProxies...),
		Git:            git,
		MCP:            c.MCP,
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
		DocumentSearch: c.DocumentSearch,
	}
}

// ToFileConfig returns the safe persisted configuration form.
func (c Config) ToFileConfig() fileConfig { return c.toFileConfig() }

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

func validateExternalURL(field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s must be an http or https origin without a path, query, fragment or user-info", field)
	}
	return nil
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

const (
	maxUploadBytes = 1 << 30
	minSyncPollMs  = 100
	maxSecretBytes = 64 << 10
)

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
	if _, port, err := net.SplitHostPort(cfg.Bind); err != nil || port == "" {
		return fmt.Errorf("bind must be a host:port address")
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("bind must use a port from 1 to 65535")
	}
	if cfg.MaxUploadBytes < 1 || cfg.MaxUploadBytes > maxUploadBytes {
		return fmt.Errorf("max_upload_bytes must be from 1 to %d", maxUploadBytes)
	}
	if cfg.SyncPollMs < minSyncPollMs {
		return fmt.Errorf("sync_poll_ms must be at least %d", minSyncPollMs)
	}
	if !validBranchName(cfg.DefaultBranch) {
		return fmt.Errorf("invalid default_branch %q", cfg.DefaultBranch)
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
