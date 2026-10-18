package web

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"

	"hmd/internal/config"
	"hmd/internal/i18n"
)

// settingInfoProps is the help shown beside a setting's label. Everything
// comes from the settings schema, so the UI and the reference documentation
// describe a setting in the same words; SetBy adds the live override state.
type settingInfoProps struct {
	ID, Name, Description, Env, StoredIn string
	SetBy                                string // environment variable currently supplying the value
	Accepts                              string // accepted values, from the schema rules
	Restart                              bool
	EnvOnly                              bool // exists only in the environment, never on this page
}

// storedIn maps a store to the message key naming the file or place it lives in.
var storedIn = map[string]string{
	config.StoreConfig:      "web.setting_info.stored_in.config",
	config.StoreWiki:        "web.setting_info.stored_in.wiki",
	config.StoreNamespace:   "web.setting_info.stored_in.namespace",
	config.StorePreferences: "web.setting_info.stored_in.preferences",
}

// makeSettingInfo resolves a schema setting for the setting-info component.
// An unknown store or key is a template error, so a renamed setting cannot
// silently lose its help. Passing the page's *SettingsData adds whether an
// environment variable currently supplies the value.
func makeSettingInfo(store, key string, state ...*SettingsData) (settingInfoProps, error) {
	p := settingInfoProps{
		ID:       "info-" + store + "-" + strings.NewReplacer(".", "-", "_", "-").Replace(strings.ToLower(key)),
		Name:     key,
		StoredIn: i18n.T(storedIn[store]),
	}
	s, ok := config.LookupIn(store, key)
	if !ok {
		return p, fmt.Errorf("unknown %s setting %q", store, key)
	}
	p.Description, p.Env, p.Restart, p.EnvOnly = s.Description, s.Env, s.Restart, s.EnvOnly
	p.Accepts = s.Rules.Hint()
	if len(state) > 0 && state[0] != nil {
		if f, ok := state[0].Fields[key]; ok {
			p.SetBy = f.EnvVar
		}
	}
	return p, nil
}

// formatPatterns give the browser an approximate pattern for each named
// format; the server applies the full check when the form is saved.
var formatPatterns = map[string]string{
	// Host-port validation stays server-side so IPv6 (including zone IDs)
	// is accepted exactly as net.SplitHostPort accepts it.
	"origin": `[a-z]+:\/\/[^\/?#@]+\/?`,
	"url":    `[a-z]+:\/\/[^@#]+`,
}

// makeSettingAttrs renders a config.yaml setting's rules as input attributes
// (pattern, min, max, maxlength and a title describing the accepted values),
// so the browser reports a mistake before the form is submitted.
func makeSettingAttrs(store, key string) (template.HTMLAttr, error) {
	s, ok := config.LookupIn(store, key)
	if !ok {
		return "", fmt.Errorf("unknown %s setting %q", store, key)
	}
	r := s.Rules
	var b strings.Builder
	attr := func(name, value string) {
		fmt.Fprintf(&b, ` %s="%s"`, name, template.HTMLEscapeString(value))
	}
	pattern := r.Pattern
	if pattern == "" {
		pattern = formatPatterns[r.Format]
		if len(r.Schemes) > 0 && pattern != "" {
			pattern = "(" + strings.Join(r.Schemes, "|") + strings.TrimPrefix(pattern, "[a-z]+")
			pattern = strings.Replace(pattern, ":", "):", 1)
		}
	}
	if pattern != "" {
		attr("pattern", pattern)
	}
	if r.Min != nil {
		attr("min", strconv.FormatInt(*r.Min, 10))
	}
	if r.Max != nil {
		attr("max", strconv.FormatInt(*r.Max, 10))
	}
	if r.MaxLength != nil {
		attr("maxlength", strconv.Itoa(*r.MaxLength))
	}
	if hint := r.Hint(); hint != "" {
		attr("title", i18n.T("web.setting_info.accepts", "Hint", hint))
	}
	return template.HTMLAttr(b.String()), nil
}

// settingGroupView is one subheading of a settings page and its rows.
type settingGroupView struct {
	Key, Title string
	Rows       []settingRowView
}

// settingRowView is everything the setting-row template needs to render one
// setting: its control kind and value, current state and schema help.
type settingRowView struct {
	Key, ID, Title    string
	Placeholder       string
	Kind, InputType   string // Kind: text, number, list, checkbox, select or readonly
	Value             string
	Checked, Editable bool
	Options           []settingOptionView
	Info              settingInfoProps
	Attrs             template.HTMLAttr
}

type settingOptionView struct {
	Value, Label string
	Selected     bool
}

// makeSettingGroups builds a config.yaml settings page from the schema layout.
func makeSettingGroups(section string, state *SettingsData) ([]settingGroupView, error) {
	layout, ok := config.Layout(section)
	if !ok {
		return nil, fmt.Errorf("unknown settings section %q", section)
	}
	var groups []settingGroupView
	for _, g := range layout.Groups {
		view := settingGroupView{Key: g.Key, Title: g.Title}
		for _, key := range g.Rows {
			row, err := makeSettingRow(key, state)
			if err != nil {
				return nil, err
			}
			view.Rows = append(view.Rows, row)
		}
		groups = append(groups, view)
	}
	return groups, nil
}

func makeSettingRow(key string, state *SettingsData) (settingRowView, error) {
	field := state.Fields[key]
	row := settingRowView{
		Key: key, Value: field.Value, Editable: field.Editable,
		ID: "setting-" + strings.NewReplacer(".", "-", "_", "-").Replace(strings.ToLower(key)),
	}
	s, ok := config.Lookup(key)
	if !ok {
		return row, fmt.Errorf("unknown setting %q", key)
	}
	info, err := makeSettingInfo(config.StoreConfig, key, state)
	if err != nil {
		return row, err
	}
	attrs, err := makeSettingAttrs(config.StoreConfig, key)
	if err != nil {
		return row, err
	}
	row.Title, row.Info, row.Attrs, row.InputType, row.Placeholder = s.Title, info, attrs, "text", s.Example
	switch {
	case s.EnvOnly:
		row.Kind = "readonly"
	case s.Type == "boolean":
		row.Kind, row.Checked = "checkbox", field.Value == "true"
	case len(s.Enum) > 0:
		row.Kind = "select"
		for _, v := range s.Enum {
			label := v
			if t := s.EnumTitles[v]; t != "" {
				label = t
			}
			row.Options = append(row.Options, settingOptionView{Value: v, Label: label, Selected: v == field.Value || (field.Value == "" && v == s.Default)})
		}
	case s.Type == "integer":
		row.Kind, row.InputType = "number", "number"
	case s.Type == "array":
		row.Kind = "list"
	default:
		row.Kind = "text"
		if s.Rules.Format == "url" || s.Rules.Format == "origin" {
			row.InputType = "url"
		}
	}
	return row, nil
}
