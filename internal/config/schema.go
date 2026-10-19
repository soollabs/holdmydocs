package config

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// schemaFS holds one JSON Schema per settings store. Each file is the single
// source of truth for its store's keys, descriptions, rules and settings-page
// layout, and is read by the documentation build to generate the reference.
//
//go:embed schema/*.schema.json
var schemaFS embed.FS

// Settings stores, each described by schema/<store>.schema.json.
const (
	StoreConfig      = "config"      // config.yaml in the app directory
	StoreWiki        = "wiki"        // .wiki.yaml at the repository root
	StoreNamespace   = "namespace"   // <namespace>/.namespace.yaml
	StorePreferences = "preferences" // per-user preferences in users.json
)

// Stores lists every settings store in display order.
var Stores = []string{StoreConfig, StoreWiki, StoreNamespace, StorePreferences}

// Setting describes one key as declared in its store's schema. Standard JSON
// Schema keywords carry most of it: title, description, type, default, enum
// or oneOf, and the value rules. Extension keywords mark restarts and
// environment-only settings.
type Setting struct {
	Store       string            // settings store, e.g. config
	Key         string            // dotted path, e.g. git.remote_url; also the form field name
	Title       string            // label on the settings page
	Group       string            // title of the enclosing object, e.g. "OIDC single sign-on"
	Env         string            // environment variable that overrides the key; empty when none
	EnvOnly     bool              // read from the environment only, never from config.yaml
	NotOnPage   bool              // retained in the schema and reference, but absent from settings pages
	Secret      bool              // never shown back; reported only as set or not set
	Type        string            // JSON Schema type
	Default     string            // default as text; empty when the schema declares none
	Enum        []string          // allowed values, from enum or oneOf
	EnumTitles  map[string]string // option labels, from oneOf titles
	Restart     bool              // change only takes effect after restart
	Description string            // shown in the settings UI and the reference docs
	Example     string            // first of examples; used as the input placeholder
	Rules       Rules             // value constraints checked on load, on save and in the browser
}

// LayoutSection is one settings page and the setting keys it shows, in order;
// rows from a nested object are grouped under that object's title.
type LayoutSection struct {
	Key, Title string
	Groups     []LayoutGroup
}

// LayoutGroup is a run of rows under one subheading; the first group of a
// section usually has no title. Key is the nested object's dotted path, such
// as server.admin, and empty for the first group.
type LayoutGroup struct {
	Key, Title string
	Rows       []string
}

type schemaOption struct {
	Const string `json:"const"`
	Title string `json:"title"`
}

type schemaNode struct {
	Type         string         `json:"type"`
	Title        string         `json:"title"`
	Default      any            `json:"default"`
	Enum         []string       `json:"enum"`
	OneOf        []schemaOption `json:"oneOf"`
	Description  string         `json:"description"`
	Examples     []any          `json:"examples"`
	Properties   orderedProps   `json:"properties"`
	Restart      bool           `json:"x-restart"`
	EnvOnly      bool           `json:"x-env-only"`
	SettingsPage *bool          `json:"x-settings-page"`
	Env          string         `json:"x-env"`
	Secret       bool           `json:"x-secret"`
	Pattern      string         `json:"pattern"`
	Minimum      *int64         `json:"minimum"`
	Maximum      *int64         `json:"maximum"`
	MaxLength    *int           `json:"maxLength"`
	Format       string         `json:"format"`
	Schemes      []string       `json:"x-schemes"`
	Items        *schemaNode    `json:"items"`
}

// orderedProps keeps properties in schema order, which a Go map would lose;
// the order decides how a nested object's settings appear on the page.
type orderedProps struct {
	names []string
	nodes map[string]schemaNode
}

func (p *orderedProps) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return fmt.Errorf("properties must be an object")
	}
	p.nodes = make(map[string]schemaNode)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		name := tok.(string)
		var node schemaNode
		if err := dec.Decode(&node); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		p.names = append(p.names, name)
		p.nodes[name] = node
	}
	_, err := dec.Token()
	return err
}

type schemaFile struct {
	Properties orderedProps `json:"properties"`
}

type settingsIndex struct {
	settings []Setting
	byKey    map[string]Setting
	objects  map[string][]string // object key -> its setting keys in schema order
	titles   map[string]string   // object key -> title
	layout   []LayoutSection
}

var schemas = mustLoadSchemas()

// Settings returns every config.yaml setting, flattened to dotted keys and sorted by key.
func Settings() []Setting { return StoreSettings(StoreConfig) }

// Lookup returns the setting for a dotted config.yaml key.
func Lookup(key string) (Setting, bool) { return LookupIn(StoreConfig, key) }

// StoreSettings returns every setting in a store, sorted by key.
func StoreSettings(store string) []Setting {
	return slices.Clone(schemas[store].settings)
}

// LookupIn returns the setting for a dotted key in a store.
func LookupIn(store, key string) (Setting, bool) {
	s, ok := schemas[store].byKey[key]
	return s, ok
}

// Layout returns a config.yaml settings page by its key, such as git.
func Layout(section string) (LayoutSection, bool) {
	for _, s := range schemas[StoreConfig].layout {
		if s.Key == section {
			return s, true
		}
	}
	return LayoutSection{}, false
}

// Sections returns every config.yaml settings page in schema order.
func Sections() []LayoutSection {
	return slices.Clone(schemas[StoreConfig].layout)
}

// envName is the environment variable for a config.yaml key: HMD_ plus the
// upper-case dotted path with underscores. Lists have no environment form.
func envName(key, typ string) string {
	if typ == "array" {
		return ""
	}
	return "HMD_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

func mustLoadSchemas() map[string]settingsIndex {
	out := make(map[string]settingsIndex, len(Stores))
	for _, store := range Stores {
		data, err := schemaFS.ReadFile("schema/" + store + ".schema.json")
		if err != nil {
			panic("config: missing settings schema: " + err.Error())
		}
		idx, err := loadSettingsSchema(store, data)
		if err != nil {
			panic("config: invalid " + store + " settings schema: " + err.Error())
		}
		out[store] = idx
	}
	return out
}

func loadSettingsSchema(store string, data []byte) (settingsIndex, error) {
	var file schemaFile
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&file); err != nil {
		return settingsIndex{}, err
	}
	idx := settingsIndex{
		byKey:   make(map[string]Setting),
		objects: make(map[string][]string), titles: make(map[string]string),
	}
	if err := flattenSettings(store, "", "", file.Properties, &idx); err != nil {
		return settingsIndex{}, err
	}
	slices.SortFunc(idx.settings, func(a, b Setting) int { return strings.Compare(a.Key, b.Key) })
	if store == StoreConfig {
		layout, err := buildLayout(file, &idx)
		if err != nil {
			return settingsIndex{}, err
		}
		idx.layout = layout
	}
	return idx, nil
}

func flattenSettings(store, prefix, group string, props orderedProps, idx *settingsIndex) error {
	for _, name := range props.names {
		node := props.nodes[name]
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		if node.Type == "object" && node.Properties.nodes != nil {
			idx.titles[key] = node.Title
			before := len(idx.settings)
			if err := flattenSettings(store, key, node.Title, node.Properties, idx); err != nil {
				return err
			}
			for _, s := range idx.settings[before:] {
				idx.objects[key] = append(idx.objects[key], s.Key)
			}
			continue
		}
		if strings.TrimSpace(node.Description) == "" {
			return fmt.Errorf("%s: missing description", key)
		}
		rules := rulesFor(node)
		if err := checkRules(key, rules); err != nil {
			return err
		}
		s := Setting{
			Store:       store,
			Key:         key,
			Title:       node.Title,
			Group:       group,
			Type:        node.Type,
			Default:     defaultText(node.Default),
			Enum:        node.Enum,
			Restart:     node.Restart,
			EnvOnly:     node.EnvOnly,
			NotOnPage:   node.SettingsPage != nil && !*node.SettingsPage,
			Secret:      node.Secret,
			Description: node.Description,
			Rules:       rules,
		}
		if len(node.Examples) > 0 {
			s.Example = defaultText(node.Examples[0])
		}
		if len(node.OneOf) > 0 {
			s.EnumTitles = make(map[string]string, len(node.OneOf))
			for _, o := range node.OneOf {
				s.Enum = append(s.Enum, o.Const)
				s.EnumTitles[o.Const] = o.Title
			}
			s.Rules.Enum = s.Enum
		}
		if (node.Env != "" || node.Secret) && !node.EnvOnly {
			return fmt.Errorf("%s: x-env and x-secret apply only to x-env-only settings", key)
		}
		if store == StoreConfig {
			s.Env = envName(key, node.Type)
			if node.Env != "" {
				s.Env = node.Env
			}
			if s.Title == "" {
				return fmt.Errorf("%s: missing title", key)
			}
		}
		idx.settings = append(idx.settings, s)
		idx.byKey[key] = s
	}
	return nil
}

// buildLayout derives the settings pages from config.yaml's nesting: each
// top-level object is a page, its own settings form the first group, and each
// nested object is a further group under its title.
func buildLayout(file schemaFile, idx *settingsIndex) ([]LayoutSection, error) {
	var out []LayoutSection
	for _, name := range file.Properties.names {
		node := file.Properties.nodes[name]
		if node.Type != "object" {
			return nil, fmt.Errorf("%s: top-level settings must be grouped in a section object", name)
		}
		section := LayoutSection{Key: strings.ReplaceAll(name, "_", "-"), Title: node.Title}
		section.Groups = append(section.Groups, LayoutGroup{})
		for _, key := range idx.objects[name] {
			if idx.byKey[key].NotOnPage {
				continue
			}
			title := idx.byKey[key].Group
			if title == node.Title {
				section.Groups[0].Rows = append(section.Groups[0].Rows, key)
				continue
			}
			if last := &section.Groups[len(section.Groups)-1]; last.Title != title {
				section.Groups = append(section.Groups, LayoutGroup{Key: key[:strings.LastIndex(key, ".")], Title: title})
			}
			g := &section.Groups[len(section.Groups)-1]
			g.Rows = append(g.Rows, key)
		}
		out = append(out, section)
	}
	return out, nil
}

func defaultText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
