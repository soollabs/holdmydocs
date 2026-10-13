package httpapi

import (
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"hmd/internal/config"
)

// configSaveEnv points configuration loading at a private config file so a
// settings save can write and reload it.
func configSaveEnv(t *testing.T, env *testEnv) string {
	t.Helper()
	cfg := env.api.Config()
	path := filepath.Join(cfg.AppDir, "config.yaml")
	t.Setenv("HMD_APP_DIR", cfg.AppDir)
	t.Setenv("HMD_CONFIG_FILE", path)
	cfg.ConfigFile = path
	env.api.SetConfig(cfg)
	return path
}

// serverSettingsBody is the settings-form payload: a valid baseline plus the
// given values, keyed by config.yaml setting key.
func serverSettingsBody(env *testEnv, extra map[string]any) map[string]any {
	settings := map[string]any{
		"server.bind":             "127.0.0.1:8080",
		"server.repo_dir":         env.api.Config().RepoDir,
		"git.user":                "test",
		"server.max_upload_bytes": 1024,
		"git.sync_poll_ms":        1000,
		"git.sync_mode":           "push",
		"git.branch":              "main",
		"server.skin":             "phosphor",
		"oidc.local_login":        true,
	}
	body := map[string]any{"settings": settings}
	maps.Copy(settings, extra)
	return body
}

// saveServerSettingsOK saves the settings form and returns the configuration
// as saved, including settings still waiting for a restart.
func saveServerSettingsOK(t *testing.T, env *testEnv, client *http.Client, extra map[string]any) config.Config {
	t.Helper()
	decodeOK(t, postMutation(t, client, env.server.URL+"/_/api/settings/server", serverSettingsBody(env, extra)), nil)
	return env.api.SavedConfig()
}

// TestSaveServerSettingsHoldsRestartSettings pins how a save treats settings
// that need a restart: they are saved and reported as pending, the running
// configuration keeps its value so nothing applies halfway, live settings
// apply at once, and saving the running value again clears the pending list.
func TestSaveServerSettingsHoldsRestartSettings(t *testing.T) {
	env, client := newTestEnv(t, false)
	configSaveEnv(t, env)
	save := func(extra map[string]any) []string {
		t.Helper()
		var result struct {
			OK             bool     `json:"ok"`
			PendingRestart []string `json:"pending_restart"`
		}
		decodeOK(t, postMutation(t, client, env.server.URL+"/_/api/settings/server", serverSettingsBody(env, extra)), &result)
		return result.PendingRestart
	}
	// The fixture's configuration is not fully resolved, so save a baseline
	// and restart on it, as a real server starts from its saved file.
	save(nil)
	env.api.SetConfig(env.api.SavedConfig())
	if pending := save(nil); len(pending) != 0 {
		t.Fatalf("pending after saving the running values = %v, want none", pending)
	}

	pending := save(map[string]any{"server.read_only": true, "server.debug": true, "git.sync_poll_ms": 2500})
	if want := []string{"server.debug", "server.read_only"}; !slices.Equal(pending, want) {
		t.Errorf("pending_restart = %v, want %v", pending, want)
	}
	running, saved := env.api.Config(), env.api.SavedConfig()
	if running.ReadOnly || running.Debug {
		t.Errorf("running read_only %v debug %v, want both still false until a restart", running.ReadOnly, running.Debug)
	}
	if !saved.ReadOnly || !saved.Debug {
		t.Errorf("saved read_only %v debug %v, want both true", saved.ReadOnly, saved.Debug)
	}
	if running.SyncPollMs != 2500 {
		t.Errorf("running sync_poll_ms = %d, want the live setting applied at once", running.SyncPollMs)
	}
	if got := len(env.api.PendingRestart()); got != 2 {
		t.Errorf("API PendingRestart lists %d settings, want 2", got)
	}

	if pending := save(map[string]any{"server.read_only": false, "server.debug": false}); len(pending) != 0 {
		t.Errorf("pending after restoring the running values = %v, want none", pending)
	}
}

func TestSaveServerSettingsWritesOAuthSettings(t *testing.T) {
	env, client := newTestEnv(t, false)
	configSaveEnv(t, env)
	cfg := saveServerSettingsOK(t, env, client, map[string]any{
		"server.base_url":                  "https://wiki.example.com",
		"mcp.enabled":                      true,
		"mcp.oauth.enabled":                true,
		"mcp.oauth.dynamic_registration":   true,
		"mcp.oauth.allow_admin_delegation": true,
	})
	if !cfg.OAuth.Enabled || !cfg.OAuth.DynamicRegistration || !cfg.OAuth.AllowAdminDelegation || cfg.OAuth.AllowInsecureLoopback {
		t.Fatalf("oauth = %+v, want enabled, dynamic registration and admin delegation only", cfg.OAuth)
	}
}

func TestSaveServerSettingsWritesReadOnly(t *testing.T) {
	env, client := newTestEnv(t, false)
	configSaveEnv(t, env)
	if cfg := saveServerSettingsOK(t, env, client, map[string]any{"server.read_only": true}); !cfg.ReadOnly {
		t.Fatal("read_only was not saved")
	}
}

// TestSaveServerSettingsRejectsEnvOnlySecrets checks that secrets cannot be
// saved to config.yaml: they are environment-only, not config settings.
func TestSaveServerSettingsRejectsEnvOnlySecrets(t *testing.T) {
	env, client := newTestEnv(t, false)
	configSaveEnv(t, env)
	for _, key := range []string{"git.token", "oidc.client_secret"} {
		resp := postMutation(t, client, env.server.URL+"/_/api/settings/server", serverSettingsBody(env, map[string]any{key: "secret"}))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", key, resp.StatusCode)
		}
	}
}

// TestSaveServerSettingsNullResetsToDefault checks that a cleared number field
// removes the stored value so the default applies again.
func TestSaveServerSettingsNullResetsToDefault(t *testing.T) {
	env, client := newTestEnv(t, false)
	path := configSaveEnv(t, env)
	if cfg := saveServerSettingsOK(t, env, client, map[string]any{"git.sync_poll_ms": 5000}); cfg.SyncPollMs != 5000 {
		t.Fatalf("sync_poll_ms = %d, want 5000", cfg.SyncPollMs)
	}
	if cfg := saveServerSettingsOK(t, env, client, map[string]any{"git.sync_poll_ms": nil}); cfg.SyncPollMs != 10000 {
		t.Fatalf("after reset: sync_poll_ms = %d, want default 10000", cfg.SyncPollMs)
	}
	fc, err := config.LoadFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if fc.Git.SyncPollMs != nil {
		t.Errorf("config.yaml still stores sync_poll_ms = %d", *fc.Git.SyncPollMs)
	}
}

// TestSaveServerSettingsRejectsSchemaViolations checks that the schema rules
// guard the settings form and that a rejected save leaves config.yaml alone.
func TestSaveServerSettingsRejectsSchemaViolations(t *testing.T) {
	env, client := newTestEnv(t, false)
	path := configSaveEnv(t, env)
	saveServerSettingsOK(t, env, client, nil)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for field, value := range map[string]any{
		"server.bind":              "localhost",
		"git.remote_url":           "git@github.com:soollabs/docs.git",
		"git.sync_poll_ms":         10,
		"server.max_upload_bytes":  0,
		"document_search.tika_url": "ftp://tika",
		"server.trusted_proxies":   []string{"not-a-cidr"},
		"server.base_url":          "https://wiki.example.com/path",
	} {
		resp := postMutation(t, client, env.server.URL+"/_/api/settings/server", serverSettingsBody(env, map[string]any{field: value}))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s=%v: status = %d, want 400", field, value, resp.StatusCode)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("a rejected save changed config.yaml")
	}
}
