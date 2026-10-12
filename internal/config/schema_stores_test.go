package config_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/presentation"
	"hmd/internal/wiki"
)

// tagKeys flattens a struct's tagged fields into dotted keys, descending into
// nested structs and struct pointers but treating slices as leaves.
func tagKeys(t reflect.Type, tag, prefix string) []string {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get(tag), ",")
		if name == "" || name == "-" {
			continue
		}
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		ft := t.Field(i).Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			keys = append(keys, tagKeys(ft, tag, key)...)
			continue
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func schemaKeys(store string) []string {
	var keys []string
	for _, s := range config.StoreSettings(store) {
		keys = append(keys, s.Key)
	}
	return keys
}

func TestWikiSchemaMatchesWikiConfig(t *testing.T) {
	if got, want := schemaKeys(config.StoreWiki), tagKeys(reflect.TypeOf(wiki.WikiConfig{}), "yaml", ""); !slices.Equal(got, want) {
		t.Errorf("wiki schema keys = %v, want %v", got, want)
	}
}

func TestNamespaceSchemaMatchesNamespaceConfig(t *testing.T) {
	if got, want := schemaKeys(config.StoreNamespace), tagKeys(reflect.TypeOf(wiki.NamespaceConfig{}), "yaml", ""); !slices.Equal(got, want) {
		t.Errorf("namespace schema keys = %v, want %v", got, want)
	}
}

// TestPreferencesSchemaMatchesUserRecord checks that every documented
// preference is a users.json field. Account data (password hash, OIDC
// identity, tokens and scopes) is not a preference and is listed here so a new
// field must be classified deliberately.
func TestPreferencesSchemaMatchesUserRecord(t *testing.T) {
	account := []string{"hash", "oidc.issuer", "oidc.subject", "tokens", "scopes"}
	var want []string
	for _, key := range tagKeys(reflect.TypeOf(auth.UserRecord{}), "json", "") {
		if !slices.Contains(account, key) {
			want = append(want, key)
		}
	}
	if got := schemaKeys(config.StorePreferences); !slices.Equal(got, want) {
		t.Errorf("preferences schema keys = %v, want %v", got, want)
	}
}

func TestSchemaSkinEnumsMatchPresentation(t *testing.T) {
	for store, key := range map[string]string{config.StoreConfig: "server.skin", config.StoreNamespace: "skin", config.StorePreferences: "skin"} {
		s, ok := config.LookupIn(store, key)
		if !ok {
			t.Fatalf("%s schema has no skin", store)
		}
		if !slices.Equal(s.Enum, presentation.SkinNames) {
			t.Errorf("%s skin enum = %v, want %v", store, s.Enum, presentation.SkinNames)
		}
	}
}
