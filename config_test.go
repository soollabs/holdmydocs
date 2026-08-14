package main

import (
	"os"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name        string
		setupEnv    func(t *testing.T)
		wantBind    string
		wantRepo    string
		wantApp     string
		wantGitUser string
		wantToken   string
	}{
		{
			name:        "defaults",
			setupEnv:    func(t *testing.T) {},
			wantBind:    ":8080",
			wantRepo:    "/data/repo",
			wantApp:     "/data/app",
			wantGitUser: "hmd",
		},
		{
			name: "all env set",
			setupEnv: func(t *testing.T) {
				t.Setenv("HMD_BIND", ":9000")
				t.Setenv("HMD_REPO_DIR", "/custom/repo")
				t.Setenv("HMD_APP_DIR", "/custom/app")
				t.Setenv("HMD_GIT_USER", "alice")
				t.Setenv("HMD_GIT_TOKEN", "env-token")
			},
			wantBind:    ":9000",
			wantRepo:    "/custom/repo",
			wantApp:     "/custom/app",
			wantGitUser: "alice",
			wantToken:   "env-token",
		},
		{
			name: "token from file overrides env",
			setupEnv: func(t *testing.T) {
				tmpFile := t.TempDir() + "/token.txt"
				err := os.WriteFile(tmpFile, []byte("file-token\n"), 0644)
				if err != nil {
					t.Fatalf("failed to write token file: %v", err)
				}
				t.Setenv("HMD_GIT_TOKEN_FILE", tmpFile)
				t.Setenv("HMD_GIT_TOKEN", "env-token")
			},
			wantBind:    ":8080",
			wantRepo:    "/data/repo",
			wantApp:     "/data/app",
			wantGitUser: "hmd",
			wantToken:   "file-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupEnv(t)
			runLoadConfigCase(t, tt.wantBind, tt.wantRepo, tt.wantApp, tt.wantGitUser, tt.wantToken)
		})
	}
}

func runLoadConfigCase(t *testing.T, wantBind, wantRepo, wantApp, wantGitUser, wantToken string) {
	t.Helper()
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.Bind != wantBind {
		t.Errorf("Bind = %q, want %q", cfg.Bind, wantBind)
	}
	if cfg.RepoDir != wantRepo {
		t.Errorf("RepoDir = %q, want %q", cfg.RepoDir, wantRepo)
	}
	if cfg.AppDir != wantApp {
		t.Errorf("AppDir = %q, want %q", cfg.AppDir, wantApp)
	}
	if cfg.Git.User != wantGitUser {
		t.Errorf("GitUser = %q, want %q", cfg.Git.User, wantGitUser)
	}
	if wantToken != "" && cfg.Git.Token != wantToken {
		t.Errorf("GitToken = %q, want %q", cfg.Git.Token, wantToken)
	}
}

func TestLoadConfigYAMLFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := dir + "/config.yaml"
	yaml := "# hmd configuration\nbind: \":7000\"\nrepo_dir: /yaml/repo\ngit:\n  user: yaml-user\n"
	if err := os.WriteFile(cfgFile, []byte(yaml), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	t.Setenv("HMD_CONFIG_FILE", cfgFile)

	t.Run("file values used with defaults for the rest", func(t *testing.T) {
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig failed: %v", err)
		}
		if cfg.Bind != ":7000" {
			t.Errorf("Bind = %q, want %q", cfg.Bind, ":7000")
		}
		if cfg.RepoDir != "/yaml/repo" {
			t.Errorf("RepoDir = %q, want %q", cfg.RepoDir, "/yaml/repo")
		}
		if cfg.Git.User != "yaml-user" {
			t.Errorf("GitUser = %q, want %q", cfg.Git.User, "yaml-user")
		}
		if cfg.AppDir != "/data/app" {
			t.Errorf("AppDir = %q, want default %q", cfg.AppDir, "/data/app")
		}
	})

	t.Run("environment overrides file", func(t *testing.T) {
		t.Setenv("HMD_BIND", ":9999")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig failed: %v", err)
		}
		if cfg.Bind != ":9999" {
			t.Errorf("Bind = %q, want %q", cfg.Bind, ":9999")
		}
	})

	t.Run("unknown key is an error", func(t *testing.T) {
		bad := dir + "/typo.yaml"
		if err := os.WriteFile(bad, []byte("site_nmae: Oops\n"), 0644); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}
		t.Setenv("HMD_CONFIG_FILE", bad)
		if _, err := LoadConfig(); err == nil {
			t.Error("LoadConfig succeeded, want error for unknown key")
		}
	})

	t.Run("malformed file is an error", func(t *testing.T) {
		bad := dir + "/bad.yaml"
		if err := os.WriteFile(bad, []byte("just a bare line\n"), 0644); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}
		t.Setenv("HMD_CONFIG_FILE", bad)
		if _, err := LoadConfig(); err == nil {
			t.Error("LoadConfig succeeded, want error for malformed file")
		}
	})
}

func TestLoadConfigYAMLZeroValuesAreHonoured(t *testing.T) {
	dir := t.TempDir()
	cfgFile := dir + "/config.yaml"
	yaml := "sync_poll_ms: 0\nmax_upload_bytes: 0\n"
	if err := os.WriteFile(cfgFile, []byte(yaml), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	t.Setenv("HMD_CONFIG_FILE", cfgFile)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.SyncPollMs != 0 {
		t.Errorf("SyncPollMs = %d, want 0 (explicit file value, not the default)", cfg.SyncPollMs)
	}
	if cfg.MaxUploadBytes != 0 {
		t.Errorf("MaxUploadBytes = %d, want 0 (explicit file value, not the default)", cfg.MaxUploadBytes)
	}
}

func TestLoadConfigEnvConflictWarning(t *testing.T) {
	dir := t.TempDir()
	cfgFile := dir + "/config.yaml"
	yamlContent := "bind: \":7000\"\ngit:\n  user: yaml-user\n"
	if err := os.WriteFile(cfgFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	t.Setenv("HMD_CONFIG_FILE", cfgFile)
	t.Setenv("HMD_BIND", ":9999")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.Bind != ":9999" {
		t.Errorf("Bind = %q, want %q (env should win)", cfg.Bind, ":9999")
	}
	if cfg.Git.User != "yaml-user" {
		t.Errorf("GitUser = %q, want %q", cfg.Git.User, "yaml-user")
	}
}

func TestSaveAndLoadFileConfig(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.yaml"

	fc := fileConfig{
		Bind:    ":7000",
		RepoDir: "/custom/repo",
		Git: GitConfig{
			RemoteURL: "https://example.com/repo.git",
			User:      "alice",
			Token:     "secret-token",
		},
	}

	if err := SaveFileConfig(path, fc); err != nil {
		t.Fatalf("SaveFileConfig failed: %v", err)
	}

	loaded, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig failed: %v", err)
	}

	if loaded.Bind != fc.Bind {
		t.Errorf("Bind = %q, want %q", loaded.Bind, fc.Bind)
	}
	if loaded.RepoDir != fc.RepoDir {
		t.Errorf("RepoDir = %q, want %q", loaded.RepoDir, fc.RepoDir)
	}
	if loaded.Git.RemoteURL != fc.Git.RemoteURL {
		t.Errorf("Git.RemoteURL = %q, want %q", loaded.Git.RemoteURL, fc.Git.RemoteURL)
	}
	if loaded.Git.Token != fc.Git.Token {
		t.Errorf("Git.Token = %q, want %q", loaded.Git.Token, fc.Git.Token)
	}
	if loaded.Git.TokenFile != "" {
		t.Errorf("Git.TokenFile = %q, want empty", loaded.Git.TokenFile)
	}
}

func TestLoadFileConfigMissingFile(t *testing.T) {
	_, err := LoadFileConfig("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("LoadFileConfig succeeded on missing file, want error")
	}
}

func TestSaveFileConfigOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.yaml"

	fc1 := fileConfig{Bind: ":7000"}
	if err := SaveFileConfig(path, fc1); err != nil {
		t.Fatalf("first SaveFileConfig failed: %v", err)
	}

	fc2 := fileConfig{Bind: ":8000"}
	if err := SaveFileConfig(path, fc2); err != nil {
		t.Fatalf("second SaveFileConfig failed: %v", err)
	}

	loaded, err := LoadFileConfig(path)
	if err != nil {
		t.Fatalf("LoadFileConfig failed: %v", err)
	}
	if loaded.Bind != ":8000" {
		t.Errorf("Bind = %q, want %q", loaded.Bind, ":8000")
	}
}

func TestSyncModeDefault(t *testing.T) {
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SyncMode != "push" {
		t.Errorf("SyncMode = %q, want push (default)", cfg.SyncMode)
	}
}

func TestSyncModeEnv(t *testing.T) {
	t.Setenv("HMD_SYNC_MODE", "bidirectional")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SyncMode != "bidirectional" {
		t.Errorf("SyncMode = %q, want bidirectional", cfg.SyncMode)
	}
}

func TestSyncModeInvalid(t *testing.T) {
	t.Setenv("HMD_SYNC_MODE", "bogus")
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("LoadConfig should reject invalid sync_mode")
	}
}

func TestSkinDefault(t *testing.T) {
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Skin != defaultSkin {
		t.Errorf("Skin = %q, want %q", cfg.Skin, defaultSkin)
	}
}

func TestSkinInvalid(t *testing.T) {
	t.Setenv("HMD_SKIN", "bogus")
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("LoadConfig should reject invalid skin")
	}
}

func TestOIDCConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		ok   bool
	}{
		{"valid", "oidc:\n  issuer: https://idp.example.com\n  client_id: hmd\n  client_secret: secret\n  base_url: https://wiki.example.com\n  allowed_subjects: [alice]\n", true},
		{"missing admission", "oidc:\n  issuer: https://idp.example.com\n  client_id: hmd\n  client_secret: secret\n  base_url: https://wiki.example.com\n", false},
		{"http public", "oidc:\n  issuer: http://idp.example.com\n  client_id: hmd\n  client_secret: secret\n  base_url: https://wiki.example.com\n  allowed_subjects: [alice]\n", false},
		{"http loopback opted in", "oidc:\n  issuer: http://127.0.0.1:5556\n  client_id: hmd\n  client_secret: secret\n  base_url: http://localhost:8080\n  allowed_subjects: [alice]\n  allow_insecure_loopback: true\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := t.TempDir() + "/config.yaml"
			if err := os.WriteFile(path, []byte(tt.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HMD_CONFIG_FILE", path)
			cfg, err := LoadConfig()
			if tt.ok && err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("LoadConfig succeeded, want error")
			}
			if tt.ok && (len(cfg.OIDC.DefaultScopes) != 1 || cfg.OIDC.DefaultScopes[0] != "read") {
				t.Errorf("OIDC default scopes = %v, want [read]", cfg.OIDC.DefaultScopes)
			}
		})
	}
}
