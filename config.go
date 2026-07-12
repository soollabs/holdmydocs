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
	Debug           bool
	ThemeDark       map[string]string
	ThemeLight      map[string]string
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
	MaxUploadBytes  int64             `yaml:"max_upload_bytes"`
	SyncPollMs      int               `yaml:"sync_poll_ms"`
	ShowTagsSidebar *bool             `yaml:"show_tags_sidebar"`
	SyncMode        string            `yaml:"sync_mode"`
	DefaultBranch   string            `yaml:"default_branch"`
	Debug           *bool             `yaml:"debug"`
	ThemeDark       map[string]string `yaml:"theme_dark"`
	ThemeLight      map[string]string `yaml:"theme_light"`
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
	pickInt := func(envKey string, fileVal, def int) int {
		if v := os.Getenv(envKey); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
		if fileVal != 0 {
			return fileVal
		}
		return def
	}
	pickInt64 := func(envKey string, fileVal, def int64) int64 {
		if v := os.Getenv(envKey); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				return n
			}
		}
		if fileVal != 0 {
			return fileVal
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
		Debug:           pickBool("HMD_DEBUG", file.Debug, false),
		ThemeDark:       file.ThemeDark,
		ThemeLight:      file.ThemeLight,
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
	if file.MaxUploadBytes != 0 && os.Getenv("HMD_MAX_UPLOAD_BYTES") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_MAX_UPLOAD_BYTES")
	}
	if file.SyncPollMs != 0 && os.Getenv("HMD_SYNC_POLL_MS") != "" {
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
	if file.Debug != nil && os.Getenv("HMD_DEBUG") != "" {
		slog.Warn("env overriding config file value", "var", "HMD_DEBUG")
	}

	// Token file overrides the token value, whichever source named it.
	if f := pick("HMD_GIT_TOKEN_FILE", file.GitTokenFile, ""); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return Config{}, fmt.Errorf("reading git token file: %w", err)
		}
		cfg.GitToken = strings.TrimSpace(string(b))
	}

	if cfg.SyncMode != "push" && cfg.SyncMode != "bidirectional" {
		return Config{}, fmt.Errorf("invalid HMD_SYNC_MODE %q: must be push or bidirectional", cfg.SyncMode)
	}

	return cfg, nil
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
