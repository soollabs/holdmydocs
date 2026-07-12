package main

import (
	"fmt"
	"log"
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
		AdminUser:       pick("HMD_ADMIN_USER", file.AdminUser, ""),
		AdminPass:       pick("HMD_ADMIN_PASSWORD", file.AdminPass, ""),
		SiteName:        pick("HMD_SITE_NAME", file.SiteName, "hold my docs (hmd)"),
		Hostname:        pick("HMD_HOSTNAME", file.Hostname, "homelab"),
		PathLabel:       pick("HMD_PATH_LABEL", file.PathLabel, "~/wiki"),
		UserLabel:       pick("HMD_USER_LABEL", file.UserLabel, ""),
		MaxUploadBytes:  pickInt64("HMD_MAX_UPLOAD_BYTES", file.MaxUploadBytes, 10*1024*1024),
		SyncPollMs:      pickInt("HMD_SYNC_POLL_MS", file.SyncPollMs, 10000),
		ShowTagsSidebar: pickBool("HMD_SHOW_TAGS_SIDEBAR", file.ShowTagsSidebar, true),
		SyncMode:       pick("HMD_SYNC_MODE", file.SyncMode, "push"),
		ThemeDark:       file.ThemeDark,
		ThemeLight:      file.ThemeLight,
	}

	// Warn when an env var overrides a non-empty YAML value.
	if file.Bind != "" && os.Getenv("HMD_BIND") != "" {
		log.Printf("Warning: HMD_BIND overriding config file value")
	}
	if file.RepoDir != "" && os.Getenv("HMD_REPO_DIR") != "" {
		log.Printf("Warning: HMD_REPO_DIR overriding config file value")
	}
	if file.AppDir != "" && os.Getenv("HMD_APP_DIR") != "" {
		log.Printf("Warning: HMD_APP_DIR overriding config file value")
	}
	if file.RemoteURL != "" && os.Getenv("HMD_REMOTE_URL") != "" {
		log.Printf("Warning: HMD_REMOTE_URL overriding config file value")
	}
	if file.GitUser != "" && os.Getenv("HMD_GIT_USER") != "" {
		log.Printf("Warning: HMD_GIT_USER overriding config file value")
	}
	if file.GitToken != "" && os.Getenv("HMD_GIT_TOKEN") != "" {
		log.Printf("Warning: HMD_GIT_TOKEN overriding config file value")
	}
	if file.AdminUser != "" && os.Getenv("HMD_ADMIN_USER") != "" {
		log.Printf("Warning: HMD_ADMIN_USER overriding config file value")
	}
	if file.AdminPass != "" && os.Getenv("HMD_ADMIN_PASSWORD") != "" {
		log.Printf("Warning: HMD_ADMIN_PASSWORD overriding config file value")
	}
	if file.SiteName != "" && os.Getenv("HMD_SITE_NAME") != "" {
		log.Printf("Warning: HMD_SITE_NAME overriding config file value")
	}
	if file.Hostname != "" && os.Getenv("HMD_HOSTNAME") != "" {
		log.Printf("Warning: HMD_HOSTNAME overriding config file value")
	}
	if file.PathLabel != "" && os.Getenv("HMD_PATH_LABEL") != "" {
		log.Printf("Warning: HMD_PATH_LABEL overriding config file value")
	}
	if file.UserLabel != "" && os.Getenv("HMD_USER_LABEL") != "" {
		log.Printf("Warning: HMD_USER_LABEL overriding config file value")
	}
	if file.MaxUploadBytes != 0 && os.Getenv("HMD_MAX_UPLOAD_BYTES") != "" {
		log.Printf("Warning: HMD_MAX_UPLOAD_BYTES overriding config file value")
	}
	if file.SyncPollMs != 0 && os.Getenv("HMD_SYNC_POLL_MS") != "" {
		log.Printf("Warning: HMD_SYNC_POLL_MS overriding config file value")
	}
	if file.ShowTagsSidebar != nil && os.Getenv("HMD_SHOW_TAGS_SIDEBAR") != "" {
		log.Printf("Warning: HMD_SHOW_TAGS_SIDEBAR overriding config file value")
	}
	if file.SyncMode != "" && os.Getenv("HMD_SYNC_MODE") != "" {
		log.Printf("Warning: HMD_SYNC_MODE overriding config file value")
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
