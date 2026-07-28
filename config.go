package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// GitConfig groups the git-remote settings. Used as-is for both the file
// shape and the runtime shape — no field here needs to distinguish "unset"
// from its zero value, so one type covers both.
type GitConfig struct {
	RemoteURL string `yaml:"remote_url"`
	User      string `yaml:"user"`
	Token     string `yaml:"token"`
	TokenFile string `yaml:"token_file"`
	Author    string `yaml:"author"`
}

// MCPConfig groups the MCP-server settings. Restart-required.
type MCPConfig struct {
	Enabled bool `yaml:"enabled"`
}

// oidcFileConfig is the YAML/env shape of the OIDC settings. LocalLogin
// needs a pointer because its default is true, not the bool zero value.
type oidcFileConfig struct {
	Issuer       string `yaml:"issuer"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	LocalLogin   *bool  `yaml:"local_login"`
	ButtonText   string `yaml:"button_text"`
	Icon         string `yaml:"icon"`
	BaseURL      string `yaml:"base_url"`
}

// OIDCConfig is the resolved runtime shape of the OIDC settings (LocalLogin
// defaulted to a concrete bool). Empty Issuer means OIDC is disabled. All
// restart-required, not editable from the settings UI.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	LocalLogin   bool   // allow the password form alongside SSO
	ButtonText   string // login button label, e.g. "Login with Authelia"
	Icon         string // Dashboard Icons name (e.g. "authelia") or path to a square SVG
	BaseURL      string // public base URL, used to build the OIDC redirect URI
}

// Config is the install-wide runtime configuration. Everything comes from
// the YAML config file except a handful of env vars: HMD_APP_DIR and
// HMD_CONFIG_FILE (bootstrap — they say where the file lives),
// HMD_ADMIN_USER / HMD_ADMIN_PASSWORD (first-run bootstrap credentials)
// and HMD_GIT_TOKEN / HMD_GIT_TOKEN_FILE / HMD_OIDC_CLIENT_SECRET
// (secrets, so they can come from a secret store instead of the file).
// Per-user preferences (theme, fonts, sidebar) live in users.json.
type Config struct {
	ConfigFile string // resolved path of the YAML config file
	// EnvOverrides maps a dotted config field path (e.g. "Git.RemoteURL")
	// to the env var name, for every field sourced from the environment
	// this run (see applyEnvOverrides). Used by the settings UI to mark
	// fields read-only with a "set via X" badge.
	EnvOverrides map[string]string
	AppDir       string
	AdminUser    string
	AdminPass    string

	Bind           string
	RepoDir        string
	SiteName       string
	MaxUploadBytes int64
	SyncPollMs     int
	SyncMode       string
	DefaultBranch  string
	HomeFilename   string
	Landing        string // slug "/" redirects to; empty = HomeSlug()
	Skin           string
	Debug          bool

	Git  GitConfig
	MCP  MCPConfig
	OIDC OIDCConfig
}

// fileConfig mirrors Config with the YAML keys accepted in the config file.
type fileConfig struct {
	Bind           string `yaml:"bind"`
	RepoDir        string `yaml:"repo_dir"`
	SiteName       string `yaml:"site_name"`
	MaxUploadBytes *int64 `yaml:"max_upload_bytes"`
	SyncPollMs     *int   `yaml:"sync_poll_ms"`
	SyncMode       string `yaml:"sync_mode"`
	DefaultBranch  string `yaml:"default_branch"`
	HomeFilename   string `yaml:"home_filename"`
	Landing        string `yaml:"landing"`
	Skin           string `yaml:"skin"`
	Debug          bool   `yaml:"debug"`

	Git  GitConfig      `yaml:"git"`
	MCP  MCPConfig      `yaml:"mcp"`
	OIDC oidcFileConfig `yaml:"oidc"`
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// applyEnvOverrides walks v's fields, overwriting each from an env var
// named <envPrefix>_<yaml tag, upper-cased> wherever that var is set and
// non-empty — e.g. under prefix "HMD", yaml:"repo_dir" is overridden by
// HMD_REPO_DIR. Struct fields (GitConfig, OIDC, ...) recurse with the
// field's own tag folded into the prefix, so GitConfig.RemoteURL becomes
// HMD_GIT_REMOTE_URL. This gives every config-file field, nested or not,
// an env var equivalent without hand-writing a pick call per field.
// applied collects path -> env var name for every override actually made,
// keyed by dotted Go field path (e.g. "Git.RemoteURL"), for the settings UI.
func applyEnvOverrides(v reflect.Value, envPrefix, pathPrefix string, applied map[string]string) {
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
			applyEnvOverrides(fv, envKey, path, applied)
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
				continue
			}
			fv.SetBool(b)
		case fv.Kind() == reflect.Pointer && fv.Type().Elem().Kind() == reflect.Bool:
			b, err := strconv.ParseBool(raw)
			if err != nil {
				continue
			}
			fv.Set(reflect.ValueOf(&b))
		case fv.Kind() == reflect.Pointer && fv.Type().Elem().Kind() == reflect.Int:
			n, err := strconv.Atoi(raw)
			if err != nil {
				continue
			}
			fv.Set(reflect.ValueOf(&n))
		case fv.Kind() == reflect.Pointer && fv.Type().Elem().Kind() == reflect.Int64:
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				continue
			}
			fv.Set(reflect.ValueOf(&n))
		default:
			continue
		}
		applied[path] = envKey
	}
}

// ConfigFilePath resolves where the config file lives: HMD_CONFIG_FILE if
// set, otherwise config.yaml next to users.json in the app dir.
func ConfigFilePath() string {
	if f := os.Getenv("HMD_CONFIG_FILE"); f != "" {
		return f
	}
	return filepath.Join(envOr("HMD_APP_DIR", "/data/app"), "config.yaml")
}

// LoadConfig builds the configuration from the YAML config file (missing
// file = all defaults), then applies env var overrides (see
// applyEnvOverrides) on top. HMD_APP_DIR / HMD_CONFIG_FILE (bootstrap, say
// where the file lives) and HMD_ADMIN_USER / HMD_ADMIN_PASSWORD (first-run
// credentials) aren't config-file fields, so they're read directly.
func LoadConfig() (Config, error) {
	path := ConfigFilePath()
	file, err := LoadFileConfig(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return Config{}, err
		}
		file = fileConfig{} // no file yet: defaults
	}
	envOverrides := make(map[string]string)
	applyEnvOverrides(reflect.ValueOf(&file).Elem(), "HMD", "", envOverrides)

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
		AppDir:       envOr("HMD_APP_DIR", "/data/app"),
		AdminUser:    os.Getenv("HMD_ADMIN_USER"),
		AdminPass:    os.Getenv("HMD_ADMIN_PASSWORD"),

		Bind:           or(file.Bind, ":8080"),
		RepoDir:        or(file.RepoDir, "/data/repo"),
		SiteName:       or(file.SiteName, "hold my docs (hmd)"),
		MaxUploadBytes: orInt64(file.MaxUploadBytes, 10*1024*1024),
		SyncPollMs:     orInt(file.SyncPollMs, 10000),
		SyncMode:       or(file.SyncMode, "push"),
		DefaultBranch:  or(file.DefaultBranch, "main"),
		HomeFilename:   or(file.HomeFilename, "readme.md"),
		Landing:        file.Landing,
		Skin:           or(file.Skin, defaultSkin),
		Debug:          file.Debug,

		Git: GitConfig{
			RemoteURL: file.Git.RemoteURL,
			User:      or(file.Git.User, "hmd"),
			Token:     file.Git.Token,
			TokenFile: file.Git.TokenFile,
			Author:    file.Git.Author,
		},
		MCP: file.MCP,
		OIDC: OIDCConfig{
			Issuer:       file.OIDC.Issuer,
			ClientID:     file.OIDC.ClientID,
			ClientSecret: file.OIDC.ClientSecret,
			LocalLogin:   orBool(file.OIDC.LocalLogin, true),
			ButtonText:   or(file.OIDC.ButtonText, "Sign in with SSO"),
			Icon:         file.OIDC.Icon,
			BaseURL:      file.OIDC.BaseURL,
		},
	}

	// Token file overrides the token value, whichever source named it.
	if f := file.Git.TokenFile; f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return Config{}, fmt.Errorf("reading git token file: %w", err)
		}
		cfg.Git.Token = strings.TrimSpace(string(b))
	}

	if cfg.SyncMode != "push" && cfg.SyncMode != "bidirectional" {
		return Config{}, fmt.Errorf("invalid sync_mode %q: must be push or bidirectional", cfg.SyncMode)
	}

	if _, ok := skins[cfg.Skin]; !ok {
		return Config{}, fmt.Errorf("invalid skin %q: must be one of %v", cfg.Skin, skinNames)
	}

	if err := cfg.validateHomeFilename(); err != nil {
		return Config{}, err
	}

	if cfg.OIDC.Issuer != "" {
		if cfg.OIDC.ClientID == "" || cfg.OIDC.ClientSecret == "" {
			return Config{}, fmt.Errorf("oidc.client_id/oidc.client_secret must be set when oidc.issuer is set")
		}
		if cfg.OIDC.BaseURL == "" {
			return Config{}, fmt.Errorf("oidc.base_url must be set when oidc.issuer is set (needed for the redirect URI)")
		}
	}

	return cfg, nil
}

// validateHomeFilename enforces the constraints on home_filename:
// must end in .md, contain no path separators, and not be dot-prefixed
// (dot-prefixed files are the hidden-page namespace). The home page is
// special-cased throughout the app, so a malformed value fails loudly at
// startup rather than producing surprising behaviour later.
func (c Config) validateHomeFilename() error {
	f := c.HomeFilename
	if !strings.HasSuffix(f, ".md") {
		return fmt.Errorf("invalid home_filename %q: must end in .md", f)
	}
	if strings.ContainsAny(f, "/\\") {
		return fmt.Errorf("invalid home_filename %q: must not contain a path separator", f)
	}
	if strings.HasPrefix(f, ".") {
		return fmt.Errorf("invalid home_filename %q: must not be dot-prefixed (reserved for hidden pages)", f)
	}
	return nil
}

// HomeSlug returns the page slug derived from HomeFilename (the filename
// without its .md suffix, lowercased). The home page is excluded from TOC
// listings and served at /page/<HomeSlug>.
func (c Config) HomeSlug() string {
	return strings.ToLower(strings.TrimSuffix(c.HomeFilename, ".md"))
}

// LandingSlug returns the page slug "/" redirects to: the configured
// landing slug if set, else the home page.
func (c Config) LandingSlug() string {
	if c.Landing != "" {
		return c.Landing
	}
	return c.HomeSlug()
}

// toFileConfig snapshots the currently effective config (file values plus
// any env var overrides) into the shape written to config.yaml. Used by
// the "export" settings action to bake env-sourced values into the file.
//
// Git.Token is the resolved token — if TokenFile named the source, that
// resolution already read the secret off disk, and exporting it here would
// duplicate it in plaintext right next to the file it was kept out of for.
// So when TokenFile is set, Token is left out of the export; TokenFile
// itself is exported unchanged and stays the source of truth.
func (c Config) toFileConfig() fileConfig {
	git := c.Git
	if git.TokenFile != "" {
		git.Token = ""
	}
	return fileConfig{
		Bind:           c.Bind,
		RepoDir:        c.RepoDir,
		SiteName:       c.SiteName,
		MaxUploadBytes: int64Ptr(c.MaxUploadBytes),
		SyncPollMs:     intPtr(c.SyncPollMs),
		SyncMode:       c.SyncMode,
		DefaultBranch:  c.DefaultBranch,
		HomeFilename:   c.HomeFilename,
		Landing:        c.Landing,
		Skin:           c.Skin,
		Debug:          c.Debug,
		Git:            git,
		MCP:            c.MCP,
		OIDC: oidcFileConfig{
			Issuer:       c.OIDC.Issuer,
			ClientID:     c.OIDC.ClientID,
			ClientSecret: c.OIDC.ClientSecret,
			LocalLogin:   boolPtr(c.OIDC.LocalLogin),
			ButtonText:   c.OIDC.ButtonText,
			Icon:         c.OIDC.Icon,
			BaseURL:      c.OIDC.BaseURL,
		},
	}
}

// parseAuthor splits a git author string in the standard "Name <email>" form
// into name and email. If no <email> is present, email falls back to
// "<fallbackName>@hmd.local". An empty string yields the fallback identity.
func parseAuthor(s, fallbackName string) (name, email string) {
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
// A missing file is returned as an os.IsNotExist error for callers that
// treat it as "defaults".
func LoadFileConfig(path string) (fileConfig, error) {
	var fc fileConfig
	b, err := os.ReadFile(path)
	if err != nil {
		return fileConfig{}, err
	}
	// Strict so a typoed key fails loudly instead of being ignored.
	if err := yaml.UnmarshalWithOptions(b, &fc, yaml.Strict()); err != nil {
		return fileConfig{}, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	return fc, nil
}

// int64Ptr returns a pointer to n, used when writing fileConfig.MaxUploadBytes.
func int64Ptr(n int64) *int64 {
	return &n
}

// intPtr returns a pointer to n, used when writing fileConfig.SyncPollMs.
func intPtr(n int) *int {
	return &n
}

// boolPtr returns a pointer to b, used when writing oidcFileConfig.LocalLogin.
func boolPtr(b bool) *bool {
	return &b
}

// SaveFileConfig marshals fc to YAML and writes it to path atomically
// (tmp file + rename), matching the pattern used in auth.go.
func SaveFileConfig(path string, fc fileConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	b, err := yaml.MarshalWithOptions(fc, yaml.Indent(2))
	if err != nil {
		return fmt.Errorf("marshalling config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return fmt.Errorf("writing tmp config file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming tmp config file: %w", err)
	}
	return nil
}
