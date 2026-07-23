package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

type Config struct {
	Bind            string
	RepoDir         string
	AppDir          string
	RemoteURL       string
	GitUser         string
	GitToken        string
	GitAuthor       string
	AdminUser       string
	AdminPass       string
	SiteName        string
	Hostname        string
	PathLabel       string
	UserLabel       string
	MaxUploadBytes  int64
	SyncPollMs      int
	ShowTagsSidebar bool
	SyncMode        string
	DefaultBranch   string
	HomeFilename    string
	Debug           bool
	ThemeDark       map[string]string
	ThemeLight      map[string]string
	FontUI          string // named stack from fontStacks; "" = style.css default
	FontMono        string
	GardenEnabled   bool   // serve public pages unauthenticated under /garden/ (restart-required)
	GardenTitle     string // heading for the garden index/feed; defaults to SiteName
	MCPEnabled      bool   // serve the MCP endpoint at /mcp (restart-required, Bearer PAT auth)

	// OIDC single sign-on. Empty OIDCIssuer means OIDC is disabled.
	// All restart-required, not editable from the settings UI.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCLocalLogin   bool   // allow the password form alongside SSO
	OIDCButtonText   string // login button label, e.g. "Login with Authelia"
	OIDCIcon         string // button icon: Dashboard Icons name (e.g. "authelia") or path to a square SVG
	BaseURL          string // public base URL, used to build the OIDC redirect URI
}

// fileConfig mirrors Config with the YAML keys accepted in the file
// named by HMD_CONFIG_FILE.
type fileConfig struct {
	Bind            string            `yaml:"bind"`
	RepoDir         string            `yaml:"repo_dir"`
	AppDir          string            `yaml:"app_dir"`
	RemoteURL       string            `yaml:"remote_url"`
	GitUser         string            `yaml:"git_user"`
	GitToken        string            `yaml:"git_token"`
	GitTokenFile    string            `yaml:"git_token_file"`
	GitAuthor       string            `yaml:"git_author"`
	AdminUser       string            `yaml:"admin_user"`
	AdminPass       string            `yaml:"admin_password"`
	SiteName        string            `yaml:"site_name"`
	Hostname        string            `yaml:"hostname"`
	PathLabel       string            `yaml:"path_label"`
	UserLabel       string            `yaml:"user_label"`
	MaxUploadBytes  *int64            `yaml:"max_upload_bytes"`
	SyncPollMs      *int              `yaml:"sync_poll_ms"`
	ShowTagsSidebar *bool             `yaml:"show_tags_sidebar"`
	SyncMode        string            `yaml:"sync_mode"`
	DefaultBranch   string            `yaml:"default_branch"`
	HomeFilename    string            `yaml:"home_filename"`
	Debug           *bool             `yaml:"debug"`
	ThemeDark       map[string]string `yaml:"theme_dark"`
	ThemeLight      map[string]string `yaml:"theme_light"`
	FontUI          string            `yaml:"font_ui"`
	FontMono        string            `yaml:"font_mono"`
	GardenEnabled   *bool             `yaml:"garden_enabled"`
	GardenTitle     string            `yaml:"garden_title"`
	MCPEnabled      *bool             `yaml:"mcp_enabled"`

	OIDCIssuer       string `yaml:"oidc_issuer"`
	OIDCClientID     string `yaml:"oidc_client_id"`
	OIDCClientSecret string `yaml:"oidc_client_secret"`
	OIDCLocalLogin   *bool  `yaml:"oidc_local_login"`
	OIDCButtonText   string `yaml:"oidc_button_text"`
	OIDCIcon         string `yaml:"oidc_icon"`
	BaseURL          string `yaml:"base_url"`
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// LoadConfig builds the configuration. Precedence, highest first:
// environment variables, then the YAML config file named by
// HMD_CONFIG_FILE, then built-in defaults.
func LoadConfig() (Config, error) {
	var file fileConfig
	if f := os.Getenv("HMD_CONFIG_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return Config{}, fmt.Errorf("reading config file: %w", err)
		}
		// Strict so a typoed key fails loudly instead of being ignored.
		if err := yaml.UnmarshalWithOptions(b, &file, yaml.Strict()); err != nil {
			return Config{}, fmt.Errorf("parsing config file %s: %w", f, err)
		}
	}

	// pick returns the env value, else the config-file value, else the default.
	pick := func(envKey, fileVal, def string) string {
		if v := os.Getenv(envKey); v != "" {
			return v
		}
		if fileVal != "" {
			return fileVal
		}
		return def
	}
	pickInt := func(envKey string, fileVal *int, def int) int {
		if v := os.Getenv(envKey); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
		if fileVal != nil {
			return *fileVal
		}
		return def
	}
	pickInt64 := func(envKey string, fileVal *int64, def int64) int64 {
		if v := os.Getenv(envKey); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				return n
			}
		}
		if fileVal != nil {
			return *fileVal
		}
		return def
	}
	pickBool := func(envKey string, fileVal *bool, def bool) bool {
		if v := os.Getenv(envKey); v != "" {
			if b, err := strconv.ParseBool(v); err == nil {
				return b
			}
		}
		if fileVal != nil {
			return *fileVal
		}
		return def
	}

	cfg := Config{
		Bind:            pick("HMD_BIND", file.Bind, ":8080"),
		RepoDir:         pick("HMD_REPO_DIR", file.RepoDir, "/data/repo"),
		AppDir:          pick("HMD_APP_DIR", file.AppDir, "/data/app"),
		RemoteURL:       pick("HMD_REMOTE_URL", file.RemoteURL, ""),
		GitUser:         pick("HMD_GIT_USER", file.GitUser, "hmd"),
		GitToken:        pick("HMD_GIT_TOKEN", file.GitToken, ""),
		GitAuthor:       pick("HMD_GIT_AUTHOR", file.GitAuthor, ""),
		AdminUser:       pick("HMD_ADMIN_USER", file.AdminUser, ""),
		AdminPass:       pick("HMD_ADMIN_PASSWORD", file.AdminPass, ""),
		SiteName:        pick("HMD_SITE_NAME", file.SiteName, "hold my docs (hmd)"),
		Hostname:        pick("HMD_HOSTNAME", file.Hostname, "homelab"),
		PathLabel:       pick("HMD_PATH_LABEL", file.PathLabel, "~/wiki"),
		UserLabel:       pick("HMD_USER_LABEL", file.UserLabel, ""),
		MaxUploadBytes:  pickInt64("HMD_MAX_UPLOAD_BYTES", file.MaxUploadBytes, 10*1024*1024),
		SyncPollMs:      pickInt("HMD_SYNC_POLL_MS", file.SyncPollMs, 10000),
		ShowTagsSidebar: pickBool("HMD_SHOW_TAGS_SIDEBAR", file.ShowTagsSidebar, true),
		SyncMode:        pick("HMD_SYNC_MODE", file.SyncMode, "push"),
		DefaultBranch:   pick("HMD_DEFAULT_BRANCH", file.DefaultBranch, "main"),
		HomeFilename:    pick("HMD_HOME_FILENAME", file.HomeFilename, "readme.md"),
		Debug:           pickBool("HMD_DEBUG", file.Debug, false),
		ThemeDark:       file.ThemeDark,
		ThemeLight:      file.ThemeLight,
		FontUI:          file.FontUI,
		FontMono:        file.FontMono,
		GardenEnabled:   pickBool("HMD_GARDEN_ENABLED", file.GardenEnabled, false),
		GardenTitle:     pick("HMD_GARDEN_TITLE", file.GardenTitle, ""),
		MCPEnabled:      pickBool("HMD_MCP_ENABLED", file.MCPEnabled, false),

		OIDCIssuer:       pick("HMD_OIDC_ISSUER", file.OIDCIssuer, ""),
		OIDCClientID:     pick("HMD_OIDC_CLIENT_ID", file.OIDCClientID, ""),
		OIDCClientSecret: pick("HMD_OIDC_CLIENT_SECRET", file.OIDCClientSecret, ""),
		OIDCLocalLogin:   pickBool("HMD_OIDC_LOCAL_LOGIN", file.OIDCLocalLogin, true),
		OIDCButtonText:   pick("HMD_OIDC_BUTTON_TEXT", file.OIDCButtonText, "Sign in with SSO"),
		OIDCIcon:         pick("HMD_OIDC_ICON", file.OIDCIcon, ""),
		BaseURL:          pick("HMD_BASE_URL", file.BaseURL, ""),
	}

	// Warn when an env var overrides a non-empty YAML value.
	if file.Bind != "" && os.Getenv("HMD_BIND") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_BIND")
	}
	if file.RepoDir != "" && os.Getenv("HMD_REPO_DIR") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_REPO_DIR")
	}
	if file.AppDir != "" && os.Getenv("HMD_APP_DIR") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_APP_DIR")
	}
	if file.RemoteURL != "" && os.Getenv("HMD_REMOTE_URL") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_REMOTE_URL")
	}
	if file.GitUser != "" && os.Getenv("HMD_GIT_USER") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_GIT_USER")
	}
	if file.GitToken != "" && os.Getenv("HMD_GIT_TOKEN") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_GIT_TOKEN")
	}
	if file.GitAuthor != "" && os.Getenv("HMD_GIT_AUTHOR") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_GIT_AUTHOR")
	}
	if file.AdminUser != "" && os.Getenv("HMD_ADMIN_USER") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_ADMIN_USER")
	}
	if file.AdminPass != "" && os.Getenv("HMD_ADMIN_PASSWORD") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_ADMIN_PASSWORD")
	}
	if file.SiteName != "" && os.Getenv("HMD_SITE_NAME") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_SITE_NAME")
	}
	if file.Hostname != "" && os.Getenv("HMD_HOSTNAME") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_HOSTNAME")
	}
	if file.PathLabel != "" && os.Getenv("HMD_PATH_LABEL") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_PATH_LABEL")
	}
	if file.UserLabel != "" && os.Getenv("HMD_USER_LABEL") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_USER_LABEL")
	}
	if file.MaxUploadBytes != nil && os.Getenv("HMD_MAX_UPLOAD_BYTES") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_MAX_UPLOAD_BYTES")
	}
	if file.SyncPollMs != nil && os.Getenv("HMD_SYNC_POLL_MS") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_SYNC_POLL_MS")
	}
	if file.ShowTagsSidebar != nil && os.Getenv("HMD_SHOW_TAGS_SIDEBAR") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_SHOW_TAGS_SIDEBAR")
	}
	if file.SyncMode != "" && os.Getenv("HMD_SYNC_MODE") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_SYNC_MODE")
	}
	if file.DefaultBranch != "" && os.Getenv("HMD_DEFAULT_BRANCH") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_DEFAULT_BRANCH")
	}
	if file.HomeFilename != "" && os.Getenv("HMD_HOME_FILENAME") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_HOME_FILENAME")
	}
	if file.Debug != nil && os.Getenv("HMD_DEBUG") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_DEBUG")
	}
	if file.GardenEnabled != nil && os.Getenv("HMD_GARDEN_ENABLED") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_GARDEN_ENABLED")
	}
	if file.GardenTitle != "" && os.Getenv("HMD_GARDEN_TITLE") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_GARDEN_TITLE")
	}
	if file.MCPEnabled != nil && os.Getenv("HMD_MCP_ENABLED") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_MCP_ENABLED")
	}
	if file.OIDCIssuer != "" && os.Getenv("HMD_OIDC_ISSUER") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_OIDC_ISSUER")
	}
	if file.OIDCClientID != "" && os.Getenv("HMD_OIDC_CLIENT_ID") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_OIDC_CLIENT_ID")
	}
	if file.OIDCClientSecret != "" && os.Getenv("HMD_OIDC_CLIENT_SECRET") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_OIDC_CLIENT_SECRET")
	}
	if file.OIDCLocalLogin != nil && os.Getenv("HMD_OIDC_LOCAL_LOGIN") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_OIDC_LOCAL_LOGIN")
	}
	if file.OIDCButtonText != "" && os.Getenv("HMD_OIDC_BUTTON_TEXT") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_OIDC_BUTTON_TEXT")
	}
	if file.OIDCIcon != "" && os.Getenv("HMD_OIDC_ICON") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_OIDC_ICON")
	}
	if file.BaseURL != "" && os.Getenv("HMD_BASE_URL") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_BASE_URL")
	}

	// Token file overrides the token value, whichever source named it.
	if f := pick("HMD_GIT_TOKEN_FILE", file.GitTokenFile, ""); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return Config{}, fmt.Errorf("reading git token file: %w", err)
		}
		cfg.GitToken = strings.TrimSpace(string(b))
	}

	if cfg.GardenTitle == "" {
		cfg.GardenTitle = cfg.SiteName
	}

	if cfg.SyncMode != "push" && cfg.SyncMode != "bidirectional" {
		return Config{}, fmt.Errorf("invalid HMD_SYNC_MODE %q: must be push or bidirectional", cfg.SyncMode)
	}

	if err := cfg.validateHomeFilename(); err != nil {
		return Config{}, err
	}

	if cfg.OIDCIssuer != "" {
		if cfg.OIDCClientID == "" || cfg.OIDCClientSecret == "" {
			return Config{}, fmt.Errorf("HMD_OIDC_ISSUER is set but HMD_OIDC_CLIENT_ID/HMD_OIDC_CLIENT_SECRET are not")
		}
		if cfg.BaseURL == "" {
			return Config{}, fmt.Errorf("HMD_OIDC_ISSUER is set but HMD_BASE_URL is not (needed for the redirect URI)")
		}
	}

	return cfg, nil
}

// validateHomeFilename enforces the constraints on HMD_HOME_FILENAME:
// must end in .md, contain no path separators, and not be dot-prefixed
// (dot-prefixed files are the hidden-page namespace). The home page is
// special-cased throughout the app, so a malformed value fails loudly at
// startup rather than producing surprising behaviour later.
func (c Config) validateHomeFilename() error {
	f := c.HomeFilename
	if !strings.HasSuffix(f, ".md") {
		return fmt.Errorf("invalid HMD_HOME_FILENAME %q: must end in .md", f)
	}
	if strings.ContainsAny(f, "/\\") {
		return fmt.Errorf("invalid HMD_HOME_FILENAME %q: must not contain a path separator", f)
	}
	if strings.HasPrefix(f, ".") {
		return fmt.Errorf("invalid HMD_HOME_FILENAME %q: must not be dot-prefixed (reserved for hidden pages)", f)
	}
	return nil
}

// HomeSlug returns the page slug derived from HomeFilename (the filename
// without its .md suffix, lowercased). The home page is excluded from TOC
// listings and served at /page/<HomeSlug>.
func (c Config) HomeSlug() string {
	return strings.ToLower(strings.TrimSuffix(c.HomeFilename, ".md"))
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
// Returns an error if the file does not exist or fails to parse.
func LoadFileConfig(path string) (fileConfig, error) {
	var fc fileConfig
	b, err := os.ReadFile(path)
	if err != nil {
		return fileConfig{}, fmt.Errorf("reading config file %s: %w", path, err)
	}
	if err := yaml.UnmarshalWithOptions(b, &fc, yaml.Strict()); err != nil {
		return fileConfig{}, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	return fc, nil
}

// boolPtr returns a pointer to b, used when writing fileConfig.ShowTagsSidebar.
func boolPtr(b bool) *bool {
	return &b
}

// int64Ptr returns a pointer to n, used when writing fileConfig.MaxUploadBytes.
func int64Ptr(n int64) *int64 {
	return &n
}

// intPtr returns a pointer to n, used when writing fileConfig.SyncPollMs.
func intPtr(n int) *int {
	return &n
}

// SaveFileConfig marshals fc to YAML and writes it to path atomically
// (tmp file + rename), matching the pattern used in auth.go.
func SaveFileConfig(path string, fc fileConfig) error {
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
