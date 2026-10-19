package mcp

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"hmd/internal/api"
	"hmd/internal/presentation"
	"hmd/internal/wiki"
)

// registeredScopes registers every tool on a fresh server and returns the
// scope recorded for each.
func registeredScopes(t *testing.T, documents bool) map[string]api.Scope {
	t.Helper()
	env, _, _ := newMCPTestAppWithApp(t, true)
	s := NewServer(env.api, Options{Version: "test", DocumentsEnabled: documents})
	s.newSDKServer()
	return s.scopes
}

// TestToolDefinitionsRegistered checks that schema/tools.json and the Go
// handlers describe the same tools, with x-document-search marking exactly the
// tools that need document search.
func TestToolDefinitionsRegistered(t *testing.T) {
	all := registeredScopes(t, true)
	if got, want := slices.Sorted(maps.Keys(all)), slices.Sorted(maps.Keys(toolDefinitions)); !slices.Equal(got, want) {
		t.Fatalf("registered tools %v, defined tools %v", got, want)
	}
	plain := registeredScopes(t, false)
	for name, def := range toolDefinitions {
		if _, ok := plain[name]; ok == def.DocumentSearch {
			t.Errorf("%s: x-document-search is %v, but it is registered without document search: %v", name, def.DocumentSearch, ok)
		}
	}
}

func property(t *testing.T, tool, path string) *jsonschema.Schema {
	t.Helper()
	schema := toolDefinitions[tool].InputSchema
	for part := range strings.SplitSeq(path, ".") {
		if part == "[]" {
			schema = schema.Items
		} else {
			schema = schema.Properties[part]
		}
		if schema == nil {
			t.Fatalf("%s has no %s", tool, path)
		}
	}
	return schema
}

// TestToolSchemaLimitsMatchCode checks the limits and enums the schema repeats
// from the Go code that enforces them for every adapter.
func TestToolSchemaLimitsMatchCode(t *testing.T) {
	intOf := func(p *int) int {
		if p == nil {
			return -1
		}
		return *p
	}
	floatOf := func(p *float64) int {
		if p == nil {
			return -1
		}
		return int(*p)
	}
	xDefault := func(s *jsonschema.Schema) int {
		value, ok := s.Extra["x-default"].(float64)
		if !ok {
			return -1
		}
		return int(value)
	}
	for _, check := range []struct {
		name      string
		got, want int
	}{
		{"edit_page edits maxItems", intOf(property(t, "edit_page", "edits").MaxItems), api.MaxPageEdits},
		{"save_page title maxLength", intOf(property(t, "save_page", "title").MaxLength), api.MaxPageTitleRunes},
		{"save_page tags maxItems", intOf(property(t, "save_page", "tags").MaxItems), api.MaxPageTags},
		{"save_page tag maxLength", intOf(property(t, "save_page", "tags.[]").MaxLength), api.MaxPageTagRunes},
		{"save_page body maxLength", intOf(property(t, "save_page", "body").MaxLength), api.MaxPageBodyBytes},
		{"save_namespace title maxLength", intOf(property(t, "save_namespace", "title").MaxLength), wiki.MaxNamespaceTitleRunes},
		{"save_namespace description maxLength", intOf(property(t, "save_namespace", "description").MaxLength), wiki.MaxNamespaceDescriptionRunes},
		{"search limit maximum", floatOf(property(t, "search", "limit").Maximum), api.MaxPageSearchResults},
		{"search limit x-default", xDefault(property(t, "search", "limit")), api.DefaultPageSearchResults},
		{"search offset maximum", floatOf(property(t, "search", "offset").Maximum), api.MaxPageSearchOffset},
		{"search_attachments limit maximum", floatOf(property(t, "search_attachments", "limit").Maximum), api.MaxAttachmentSearchResults},
		{"search_attachments limit x-default", xDefault(property(t, "search_attachments", "limit")), api.DefaultAttachmentSearchResults},
		{"recent_changes limit maximum", floatOf(property(t, "recent_changes", "limit").Maximum), api.MaxRecentChanges},
		{"recent_changes limit x-default", xDefault(property(t, "recent_changes", "limit")), api.DefaultRecentChanges},
		// read_attachment shares read_page's window, which windowText applies.
		{"read_attachment limit maximum", floatOf(property(t, "read_attachment", "limit").Maximum), floatOf(property(t, "read_page", "limit").Maximum)},
		{"read_attachment limit x-default", xDefault(property(t, "read_attachment", "limit")), defaultTextWindowChars},
	} {
		if check.got != check.want {
			t.Errorf("%s = %d, code uses %d", check.name, check.got, check.want)
		}
	}

	var widgets []string
	for _, id := range presentation.WidgetIDs {
		if presentation.ValidWidget(id) {
			widgets = append(widgets, id)
		}
	}
	for _, check := range []struct {
		name string
		enum []any
		want []string
	}{
		{"save_namespace skin", property(t, "save_namespace", "skin").Enum, append([]string{""}, presentation.SkinNames...)},
		{"save_namespace palette", property(t, "save_namespace", "palette").Enum, append([]string{""}, presentation.ThemePresetNames...)},
		{"save_namespace widgets", property(t, "save_namespace", "widgets.[]").Enum, widgets},
	} {
		got := make([]string, 0, len(check.enum))
		for _, value := range check.enum {
			text, _ := value.(string)
			got = append(got, text)
		}
		if !slices.Equal(got, check.want) {
			t.Errorf("%s enum = %q, code allows %q", check.name, got, check.want)
		}
	}
}

func TestLoadToolsRejects(t *testing.T) {
	valid := `{"name":"x","title":"X","description":"Does x.","x-scope":"read","annotations":{},` +
		`"inputSchema":{"type":"object"},"outputSchema":{"type":"object"}}`
	if _, err := loadTools([]byte("[" + valid + "]")); err != nil {
		t.Fatalf("valid definition rejected: %v", err)
	}
	for name, definition := range map[string]string{
		"unknown field":   strings.Replace(valid, `"title"`, `"titel":"X","title"`, 1),
		"unknown keyword": strings.Replace(valid, `{"type":"object"},"output`, `{"type":"object","maxLenght":3},"output`, 1),
		"unknown scope":   strings.Replace(valid, `"read"`, `"admin"`, 1),
		"no output":       strings.Replace(valid, `,"outputSchema":{"type":"object"}`, ``, 1),
		"duplicate":       valid + "," + valid,
	} {
		if _, err := loadTools([]byte("[" + definition + "]")); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestToolSchemaShapeMismatch(t *testing.T) {
	type input struct {
		Slug  string `json:"slug"`
		Limit int    `json:"limit,omitempty"`
	}
	schema := func(required string) *jsonschema.Schema {
		return &jsonschema.Schema{
			Type: "object", Required: []string{required},
			Properties: map[string]*jsonschema.Schema{
				"slug": {Type: "string"}, "limit": {Type: "integer"},
			},
			AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
		}
	}
	if err := matchesType[input](schema("slug")); err != nil {
		t.Fatalf("matching schema rejected: %v", err)
	}
	if err := matchesType[input](schema("limit")); err == nil {
		t.Error("schema with the wrong required field accepted")
	}
	wrongType := schema("slug")
	wrongType.Properties["limit"].Type = "string"
	if err := matchesType[input](wrongType); err == nil {
		t.Error("schema with the wrong property type accepted")
	}
}
