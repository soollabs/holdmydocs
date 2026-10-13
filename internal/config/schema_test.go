package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// fileConfigLeaves walks the yaml-tagged fields of a config struct and records every leaf by dotted key.
func fileConfigLeaves(t reflect.Type, prefix string, out map[string]reflect.StructField) {
	for sf := range t.Fields() {
		tag, _, _ := strings.Cut(sf.Tag.Get("yaml"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		key := tag
		if prefix != "" {
			key = prefix + "." + tag
		}
		if sf.Type.Kind() == reflect.Struct {
			fileConfigLeaves(sf.Type, key, out)
			continue
		}
		out[key] = sf
	}
}

func TestSettingsSchemaMatchesFileConfig(t *testing.T) {
	fields := make(map[string]reflect.StructField)
	fileConfigLeaves(reflect.TypeFor[fileConfig](), "", fields)

	schemaKeys := make(map[string]Setting)
	for _, s := range Settings() {
		if s.EnvOnly {
			if _, ok := fields[s.Key]; ok {
				t.Errorf("environment-only key %q must not be a fileConfig field", s.Key)
			}
			continue
		}
		schemaKeys[s.Key] = s
	}

	for key := range fields {
		if _, ok := schemaKeys[key]; !ok {
			t.Errorf("config key %q has no entry in schema/config.schema.json", key)
		}
	}
	for key := range schemaKeys {
		if _, ok := fields[key]; !ok {
			t.Errorf("schema/config.schema.json key %q has no fileConfig field", key)
		}
	}
}

func TestSettingsSchemaEnvNames(t *testing.T) {
	fields := make(map[string]reflect.StructField)
	fileConfigLeaves(reflect.TypeFor[fileConfig](), "", fields)

	for key, sf := range fields {
		s, ok := Lookup(key)
		if !ok {
			continue // reported by TestSettingsSchemaMatchesFileConfig
		}
		if sf.Type.Kind() == reflect.Slice {
			if s.Env != "" {
				t.Errorf("list key %q must not declare an env var, got %q", key, s.Env)
			}
			continue
		}
		want := "HMD_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
		if s.Env != want {
			t.Errorf("key %q: x-env = %q, want %q", key, s.Env, want)
		}
	}
}

func TestSettingsSchemaEnums(t *testing.T) {
	skin, ok := Lookup("server.skin")
	if !ok {
		t.Fatal("skin missing from schema/config.schema.json")
	}
	if !slices.Equal(skin.Enum, skinNames) {
		t.Errorf("skin enum = %v, want skinNames %v", skin.Enum, skinNames)
	}

	syncMode, ok := Lookup("git.sync_mode")
	if !ok {
		t.Fatal("sync_mode missing from schema/config.schema.json")
	}
	if want := []string{"push", "bidirectional"}; !slices.Equal(syncMode.Enum, want) {
		t.Errorf("sync_mode enum = %v, want %v", syncMode.Enum, want)
	}
}

func TestSettingsSchemaEnvOnly(t *testing.T) {
	want := map[string]string{
		"server.app_dir": "HMD_APP_DIR", "server.config_file": "HMD_CONFIG_FILE", "server.read_only": "HMD_SERVER_READ_ONLY",
		"server.admin.user": "HMD_ADMIN_USER", "server.admin.password": "HMD_ADMIN_PASSWORD",
		"git.token": "HMD_GIT_TOKEN", "oidc.client_secret": "HMD_OIDC_CLIENT_SECRET",
	}
	for key, env := range want {
		if s, ok := Lookup(key); !ok || !s.EnvOnly || s.Env != env {
			t.Errorf("%s: setting = %+v, want environment-only %s", key, s, env)
		}
	}
	for _, s := range Settings() {
		if _, ok := envOnlyValues[s.Key]; s.EnvOnly != ok {
			t.Errorf("%s: environment-only = %v but has value accessor = %v", s.Key, s.EnvOnly, ok)
		}
	}
}

// TestSchemaRules checks each declared rule against a bad value, and that the
// schema defaults pass, so the rules cannot silently stop applying.
func TestSchemaRules(t *testing.T) {
	bad := map[string]any{
		"server.bind":                ":0",
		"server.skin":                "bogus",
		"git.sync_mode":              "sideways",
		"server.max_upload_bytes":    int64(1<<30 + 1),
		"server.tika_timeout":        3601,
		"git.sync_poll_ms":           50,
		"git.branch":                 "bad..name",
		"server.base_url":            "https://wiki.example.com/docs",
		"document_search.tika_url":   "ftp://tika",
		"server.trusted_proxies":     []string{"10.0.0.0"},
		"git.remote_url":             "git@github.com:soollabs/docs.git",
		"oidc.issuer":                "ldap://idp.example.com",
		"oidc.default_scopes":        []string{"read", "admin"},
		"oidc.allowed_email_domains": []string{"user@example.com"},
		"document_search.model":      "no-owner",
	}
	for key, value := range bad {
		s, ok := Lookup(key)
		if !ok {
			t.Fatalf("%s missing from the schema", key)
		}
		if err := s.Rules.Check(key, value); err == nil {
			t.Errorf("%s accepted %v", key, value)
		}
	}
	good := map[string]any{
		"server.bind":                "127.0.0.1:8080",
		"server.base_url":            "https://wiki.example.com/",
		"git.remote_url":             "https://github.com/soollabs/docs.git",
		"server.trusted_proxies":     []string{"10.0.0.0/8", "::1/128"},
		"oidc.allowed_email_domains": []string{"example.com"},
		"document_search.model":      "BAAI/bge-small-en-v1.5",
	}
	for key, value := range good {
		s, _ := Lookup(key)
		if err := s.Rules.Check(key, value); err != nil {
			t.Errorf("%s rejected %v: %v", key, value, err)
		}
	}
	for _, s := range Settings() {
		if s.Default == "" || s.Type != "string" {
			continue
		}
		if err := s.Rules.Check(s.Key, s.Default); err != nil {
			t.Errorf("%s default %q breaks its own rules: %v", s.Key, s.Default, err)
		}
	}
}

// TestTikaURLFromFileAndEnvironment keeps HMD_DOCUMENT_SEARCH_TIKA_URL working now that
// tika_url is also a config.yaml key.
func TestTikaURLFromFileAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("document_search:\n  tika_url: http://tika.internal:9998/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HMD_CONFIG_FILE", path)
	t.Setenv("HMD_APP_DIR", filepath.Join(dir, "app"))
	t.Setenv("HMD_SERVER_REPO_DIR", filepath.Join(dir, "repo"))
	t.Setenv("HMD_DOCUMENT_SEARCH_TIKA_URL", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TikaURL != "http://tika.internal:9998" || cfg.EnvOverrides["document_search.tika_url"] != "" {
		t.Fatalf("file: tika = %q, override = %q", cfg.TikaURL, cfg.EnvOverrides["document_search.tika_url"])
	}
	t.Setenv("HMD_DOCUMENT_SEARCH_TIKA_URL", "https://tika.example:9998")
	if cfg, err = LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if cfg.TikaURL != "https://tika.example:9998" || cfg.EnvOverrides["document_search.tika_url"] != "HMD_DOCUMENT_SEARCH_TIKA_URL" {
		t.Fatalf("environment: tika = %q, override = %q", cfg.TikaURL, cfg.EnvOverrides["document_search.tika_url"])
	}
	t.Setenv("HMD_DOCUMENT_SEARCH_TIKA_URL", "ftp://tika")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("a non-http Tika URL must be rejected")
	}
}

// TestSaveFileConfigOmitsUnsetValues keeps config.yaml to the values an
// operator set: unset keys stay out, so defaults can change between releases
// and an older release can still read the file.
func TestSaveFileConfigOmitsUnsetValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	off := false
	fc := fileConfig{Server: serverFileConfig{Bind: "127.0.0.1:8080"}, OIDC: oidcFileConfig{LocalLogin: &off}}
	if err := SaveFileConfig(path, fc); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, unset := range []string{"document_search.tika_url", "server.repo_dir", "server.read_only", "git:", "oauth:", "document_search:"} {
		if strings.Contains(text, unset) {
			t.Errorf("unset %s written to config.yaml:\n%s", unset, text)
		}
	}
	for _, set := range []string{"bind: 127.0.0.1:8080", "local_login: false"} {
		if !strings.Contains(text, set) {
			t.Errorf("config.yaml missing %q:\n%s", set, text)
		}
	}
	back, err := LoadFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.OIDC.LocalLogin == nil || *back.OIDC.LocalLogin {
		t.Error("an explicit local_login: false did not survive a round trip")
	}
}

// TestLayoutPlacesEverySettingOnce keeps x-layout complete: each config.yaml
// setting must appear on exactly one settings page.
func TestLayoutPlacesEverySettingOnce(t *testing.T) {
	seen := map[string]int{}
	for _, layout := range Sections() {
		for _, g := range layout.Groups {
			for _, row := range g.Rows {
				seen[row]++
			}
		}
	}
	for _, s := range Settings() {
		if seen[s.Key] != 1 {
			t.Errorf("%s appears %d times in x-layout, want 1", s.Key, seen[s.Key])
		}
	}
}

func TestSetFileValueByKey(t *testing.T) {
	var fc fileConfig
	for key, value := range map[string]any{
		"server.bind": "127.0.0.1:9000", "git.remote_url": "https://example.com/r.git", "server.max_upload_bytes": float64(2048),
		"oidc.local_login": false, "server.trusted_proxies": []any{"10.0.0.0/8", " "}, "oidc.default_scopes": "read, write",
	} {
		if err := SetFileValue(&fc, key, value); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	if fc.Server.Bind != "127.0.0.1:9000" || fc.Git.RemoteURL != "https://example.com/r.git" || *fc.Server.MaxUploadBytes != 2048 {
		t.Errorf("scalars not set: %+v", fc)
	}
	if fc.OIDC.LocalLogin == nil || *fc.OIDC.LocalLogin || !slices.Equal(fc.Server.TrustedProxies, []string{"10.0.0.0/8"}) || !slices.Equal(fc.OIDC.DefaultScopes, []string{"read", "write"}) {
		t.Errorf("pointer or list not set: %+v", fc)
	}
	if got := FileValue(fc, "server.max_upload_bytes"); got != int64(2048) {
		t.Errorf("FileValue = %v", got)
	}
	for key, value := range map[string]any{"server.bind": true, "server.max_upload_bytes": 1.5, "server.debug": "yes", "nope": "x"} {
		if err := SetFileValue(&fc, key, value); err == nil {
			t.Errorf("%s accepted %v", key, value)
		}
	}
}

// TestExampleConfigParses keeps config.yaml.example in step with the real
// keys: the strict loader rejects any key the example still uses wrongly.
func TestExampleConfigParses(t *testing.T) {
	fc, err := LoadFileConfig(filepath.Join("..", "..", "config.yaml.example"))
	if err != nil {
		t.Fatalf("config.yaml.example: %v", err)
	}
	if fc.Server.Bind != ":8080" || fc.Git.User != "you" {
		t.Errorf("example parsed unexpectedly: %+v", fc)
	}
	if err := ValidateFile(fc); err != nil {
		t.Errorf("example breaks a schema rule: %v", err)
	}
}
