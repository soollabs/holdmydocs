package web

import (
	"strings"
	"testing"
	"text/template/parse"

	"hmd/internal/auth"
	"hmd/internal/config"
)

// TestEverySettingIsOnASettingsPage keeps the settings pages in step with the
// schema: every config.yaml setting must appear exactly once in the layout, as
// an editable control posted under its own key.
func TestEverySettingIsOnASettingsPage(t *testing.T) {
	state := buildSettingsData(config.Config{EnvOverrides: map[string]string{}}, auth.UserRecord{})
	seen := map[string]int{}
	for _, section := range config.Sections() {
		groups, err := makeSettingGroups(section.Key, &state)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range groups {
			for _, row := range g.Rows {
				seen[row.Key]++
				if s, isSetting := config.Lookup(row.Key); isSetting && !s.EnvOnly && (row.Kind == "readonly" || !row.Editable) {
					t.Errorf("%s is not editable", row.Key)
				}
			}
		}
	}
	for _, s := range config.Settings() {
		if s.NotOnPage {
			if seen[s.Key] != 0 {
				t.Errorf("%s must be absent from settings pages", s.Key)
			}
			continue
		}
		if seen[s.Key] != 1 {
			t.Errorf("%s appears %d times on the settings pages, want 1", s.Key, seen[s.Key])
		}
	}
}

// TestSecretSettingsAreNeverRendered checks that environment-only secrets
// only reach the settings screen as "set" or "not set".
func TestSecretSettingsAreNeverRendered(t *testing.T) {
	t.Setenv("HMD_GIT_TOKEN", "git-secret-value")
	t.Setenv("HMD_OIDC_CLIENT_SECRET", "oidc-secret-value")
	t.Setenv("HMD_ADMIN_PASSWORD", "admin-secret-value")
	cfg := config.Config{
		AdminPass: "admin-secret-value",
		Git:       config.GitConfig{Token: "git-secret-value"},
		OIDC:      config.OIDCConfig{ClientSecret: "oidc-secret-value"},
	}
	fields := buildSettingsData(cfg, auth.UserRecord{}).Fields
	for name, f := range fields {
		if strings.Contains(f.Value, "secret-value") {
			t.Errorf("%s exposes a secret value", name)
		}
	}
	for _, s := range config.Settings() {
		if s.Secret && fields[s.Key].Value != "set" {
			t.Errorf("%s: value = %q, want set", s.Key, fields[s.Key].Value)
		}
	}
}

// TestEverySettingHasInfo requires a setting-info icon for every schema
// setting, and that each settinginfo call names a real setting, so the help
// beside a label always comes from the schema.
func TestEverySettingHasInfo(t *testing.T) {
	templates, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for page, tmpl := range templates {
		for _, def := range tmpl.Templates() {
			if def.Tree == nil {
				continue
			}
			walkTemplateNode(def.Tree.Root, func(cmd *parse.CommandNode) {
				name, ok := cmd.Args[0].(*parse.IdentifierNode)
				if !ok || name.Ident != "settinginfo" {
					return
				}
				if len(cmd.Args) != 3 && len(cmd.Args) != 4 {
					t.Errorf("%s: settinginfo takes a store, a key and optionally the settings state: %s", page, cmd)
					return
				}
				store, ok1 := cmd.Args[1].(*parse.StringNode)
				key, ok2 := cmd.Args[2].(*parse.StringNode)
				if !ok1 || !ok2 {
					t.Errorf("%s: settinginfo arguments must be string literals: %s", page, cmd)
					return
				}
				if _, err := makeSettingInfo(store.Text, key.Text); err != nil {
					t.Errorf("%s: %v", page, err)
				}
				seen[store.Text+" "+key.Text] = true
			})
		}
	}
	// config.yaml rows come from the schema layout, and every row has an info icon.
	state := buildSettingsData(config.Config{EnvOverrides: map[string]string{}}, auth.UserRecord{})
	for _, section := range config.Sections() {
		groups, err := makeSettingGroups(section.Key, &state)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range groups {
			for _, row := range g.Rows {
				if row.Info.Description == "" {
					t.Errorf("%s row has no info", row.Key)
				}
				seen[config.StoreConfig+" "+row.Key] = true
			}
		}
	}
	for _, store := range config.Stores {
		for _, s := range config.StoreSettings(store) {
			if !s.NotOnPage && !seen[store+" "+s.Key] {
				t.Errorf("%s setting %s has no info icon on any settings page", store, s.Key)
			}
		}
	}
}

// TestSettingInfoShowsLiveOverride checks that the info tooltip names the
// environment variable currently supplying a value, and flags restarts.
func TestSettingInfoShowsLiveOverride(t *testing.T) {
	state := &SettingsData{Fields: map[string]FieldState{"server.bind": {EnvVar: "HMD_SERVER_BIND"}}}
	p, err := makeSettingInfo(config.StoreConfig, "server.bind", state)
	if err != nil {
		t.Fatal(err)
	}
	if p.SetBy != "HMD_SERVER_BIND" || !p.Restart {
		t.Errorf("bind info = %+v, want set by HMD_SERVER_BIND and restart required", p)
	}
	p, err = makeSettingInfo(config.StoreConfig, "server.max_upload_bytes", state)
	if err != nil {
		t.Fatal(err)
	}
	if p.SetBy != "" || p.Restart {
		t.Errorf("max_upload_bytes info = %+v, want no override and no restart", p)
	}
}

// TestSettingAttrsFollowSchema checks the browser attributes generated from
// schema rules, and that every settingattrs call in a template names a real
// config.yaml setting.
func TestSettingAttrsFollowSchema(t *testing.T) {
	for key, want := range map[string][]string{
		"server.bind":             {`title="Accepts host:port`},
		"git.remote_url":          {`pattern="(https):\/\/`, `title="Accepts https URL`},
		"git.sync_poll_ms":        {`min="100"`},
		"server.max_upload_bytes": {`min="1"`, `max="1073741824"`},
	} {
		attrs, err := makeSettingAttrs(config.StoreConfig, key)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if !strings.Contains(string(attrs), w) {
				t.Errorf("%s attrs %s missing %s", key, attrs, w)
			}
		}
	}
	templates, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for page, tmpl := range templates {
		for _, def := range tmpl.Templates() {
			if def.Tree == nil {
				continue
			}
			walkTemplateNode(def.Tree.Root, func(cmd *parse.CommandNode) {
				name, ok := cmd.Args[0].(*parse.IdentifierNode)
				if !ok || name.Ident != "settingattrs" {
					return
				}
				calls++
				store, ok1 := cmd.Args[1].(*parse.StringNode)
				key, ok2 := cmd.Args[2].(*parse.StringNode)
				if len(cmd.Args) != 3 || !ok1 || !ok2 {
					t.Errorf("%s: settingattrs takes a store and a key as string literals: %s", page, cmd)
					return
				}
				if _, err := makeSettingAttrs(store.Text, key.Text); err != nil {
					t.Errorf("%s: %v", page, err)
				}
			})
		}
	}
}

func TestBindValidationAcceptsIPv6(t *testing.T) {
	attrs, err := makeSettingAttrs(config.StoreConfig, "server.bind")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(attrs), "pattern=") {
		t.Fatal("bind must defer host-port validation to the server")
	}
	s, ok := config.Lookup("server.bind")
	if !ok {
		t.Fatal("missing bind setting")
	}
	for _, value := range []string{":8080", "127.0.0.1:8080", "[::1]:8080", "[::]:8080", "[fe80::1%eth0]:8080"} {
		if err := s.Rules.Check(s.Key, value); err != nil {
			t.Errorf("%s: %v", value, err)
		}
	}
	for _, value := range []string{"::1:8080", "[::1]", "[::1]:65536"} {
		if err := s.Rules.Check(s.Key, value); err == nil {
			t.Errorf("accepted invalid bind %q", value)
		}
	}
}

// TestEverySchemaSectionHasASettingsPage keeps the settings routes in step
// with config.yaml: a new top-level section must get a page in settingsPanels.
func TestEverySchemaSectionHasASettingsPage(t *testing.T) {
	for _, section := range config.Sections() {
		if settingsPanels[section.Key].Template != "admin" {
			t.Errorf("config.yaml section %s has no admin settings page", section.Key)
		}
	}
}

// TestEnvOnlySettingInfoDoesNotOfferEditing checks that an environment-only
// setting is never described as editable on the settings page.
func TestEnvOnlySettingInfoDoesNotOfferEditing(t *testing.T) {
	state := &SettingsData{Fields: map[string]FieldState{"server.config_file": {EnvVar: "HMD_CONFIG_FILE"}}}
	p, err := makeSettingInfo(config.StoreConfig, "server.config_file", state)
	if err != nil {
		t.Fatal(err)
	}
	if !p.EnvOnly || p.SetBy != "HMD_CONFIG_FILE" {
		t.Errorf("info = %+v, want an environment-only setting set by HMD_CONFIG_FILE", p)
	}
}
