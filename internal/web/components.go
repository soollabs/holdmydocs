package web

import (
	"fmt"
	"regexp"
	"slices"
)

// buttonProps is the shared action control contract. Pages supply content and
// behaviour; geometry and states belong to the component, not the page.
type buttonProps struct {
	Label, Variant, Type, Size, Href, ID, Hook, Form, FormAction string
	Name, Value, Title, ARIALabel, DescribedBy                   string
	Confirm, CopyTarget, Widget, Pressed                         string
	Action, Zen, Controls, Expanded, HasPopup                    string
	Disabled, Hidden, FormNoValidate, NewTab                     bool
}

// optionToken matches behavioural hook values such as data-action names and
// element IDs. html/template escapes every value; this only keeps them tidy.
var optionToken = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

var boolValues = []string{"true", "false"}

// buttonEnums restricts options whose values change appearance or carry fixed
// semantics. Hooks attach an existing screen treatment (for example the compact
// top bar toolbar), so new ones must be added here deliberately.
var buttonEnums = map[string][]string{
	"size":          {"regular", "compact", "icon"},
	"type":          {"button", "submit", "reset"},
	"hook":          {"export-link-remove", "widget-chip", "zen-exit", "topbar-icon", "topbar-action", "topbar-action topbar-primary", "ui-notice__dismiss"},
	"aria-expanded": boolValues,
	"aria-haspopup": {"true", "menu", "listbox", "tree", "grid", "dialog"},
	"pressed":       boolValues, "disabled": boolValues, "hidden": boolValues,
	"formnovalidate": boolValues, "new-tab": boolValues,
}

// makeButton uses explicit option names rather than raw HTML attributes, so
// html/template continues to escape every value and reject unsafe URLs.
func makeButton(label, variant string, options ...string) (buttonProps, error) {
	p := buttonProps{Label: label, Variant: variant, Type: "button"}
	if !slices.Contains([]string{"primary", "secondary", "danger"}, variant) {
		return p, fmt.Errorf("unknown button variant %q", variant)
	}
	if len(options)%2 != 0 {
		return p, fmt.Errorf("button options must be name/value pairs")
	}
	for i := 0; i < len(options); i += 2 {
		key, value := options[i], options[i+1]
		if allowed, ok := buttonEnums[key]; ok && !slices.Contains(allowed, value) {
			return p, fmt.Errorf("invalid button %s %q", key, value)
		}
		switch key {
		case "data-action", "data-zen", "aria-controls":
			if !optionToken.MatchString(value) {
				return p, fmt.Errorf("invalid button %s %q", key, value)
			}
		}
		switch key {
		case "size":
			p.Size = value
		case "type":
			p.Type = value
		case "href":
			p.Href = value
		case "id":
			p.ID = value
		case "data-action":
			p.Action = value
		case "data-zen":
			p.Zen = value
		case "hook":
			p.Hook = value
		case "form":
			p.Form = value
		case "formaction":
			p.FormAction = value
		case "name":
			p.Name = value
		case "value":
			p.Value = value
		case "title":
			p.Title = value
		case "aria-label":
			p.ARIALabel = value
		case "aria-describedby":
			p.DescribedBy = value
		case "aria-controls":
			p.Controls = value
		case "aria-expanded":
			p.Expanded = value
		case "aria-haspopup":
			p.HasPopup = value
		case "confirm":
			p.Confirm = value
		case "copy":
			p.CopyTarget = value
		case "widget":
			p.Widget = value
		case "pressed":
			p.Pressed = value
		case "disabled":
			p.Disabled = value == "true"
		case "hidden":
			p.Hidden = value == "true"
		case "formnovalidate":
			p.FormNoValidate = value == "true"
		case "new-tab":
			p.NewTab = value == "true"
		default:
			return p, fmt.Errorf("unknown button option %q", key)
		}
	}
	if p.Size == "icon" && p.ARIALabel == "" {
		return p, fmt.Errorf("icon-only buttons require aria-label")
	}
	return p, nil
}

// componentProps contains only constrained presentation and native semantics.
// Arbitrary content belongs between the component's explicit start/end templates.
type componentProps struct {
	Kind, Variant, Label, ID, Role, Name, Value, Type, For, State, Layout string
	Disabled, Required, Hidden, Multiple, Checked, Open, Dismissible      bool
}

// componentRule lists the options a kind accepts. A nil value list accepts any
// string; otherwise the value must be one of the list, and the first entry is
// the default. Required options must be present and non-empty.
type componentRule struct {
	options  map[string][]string
	required []string
}

var commonComponentOptions = map[string][]string{"label": nil, "id": nil, "hidden": boolValues}

var componentRules = map[string]componentRule{
	"field": {options: map[string][]string{
		"type":   {"text", "email", "password", "search", "url", "number", "select", "textarea"},
		"layout": {"row", "stacked", "bare"},
		"name":   nil, "value": nil, "for": nil, "disabled": boolValues, "required": boolValues, "multiple": boolValues,
	}, required: []string{"id"}},
	"choice": {options: map[string][]string{
		"type": {"checkbox", "radio"}, "name": nil, "value": nil,
		"checked": boolValues, "disabled": boolValues, "required": boolValues,
	}, required: []string{"id"}},
	"panel":      {options: map[string][]string{"variant": {"default", "raised"}}},
	"notice":     {options: map[string][]string{"variant": {"info", "success", "warning", "danger"}, "role": {"status", "alert"}, "dismissible": boolValues}},
	"empty":      {options: map[string][]string{"state": {"default", "error", "loading"}}},
	"table":      {},
	"disclosure": {options: map[string][]string{"open": boolValues}},
	"menu":       {required: []string{"label"}},
	"dialog":     {required: []string{"id", "label"}},
}

func makeComponent(kind string, options ...string) (componentProps, error) {
	p := componentProps{Kind: kind}
	rule, ok := componentRules[kind]
	if !ok {
		return p, fmt.Errorf("unknown component %q", kind)
	}
	if len(options)%2 != 0 {
		return p, fmt.Errorf("component options must be name/value pairs")
	}
	values := map[string]string{}
	for key, allowed := range rule.options {
		if allowed != nil && !slices.Equal(allowed, boolValues) {
			values[key] = allowed[0]
		}
	}
	for i := 0; i < len(options); i += 2 {
		key, value := options[i], options[i+1]
		allowed, ok := rule.options[key]
		if !ok {
			allowed, ok = commonComponentOptions[key]
		}
		if !ok {
			return p, fmt.Errorf("option %q is not valid for %s", key, kind)
		}
		if allowed != nil && !slices.Contains(allowed, value) {
			return p, fmt.Errorf("invalid %s %s %q", kind, key, value)
		}
		values[key] = value
	}
	for _, key := range rule.required {
		if values[key] == "" {
			return p, fmt.Errorf("%s requires %s", kind, key)
		}
	}
	p.Variant, p.Label, p.ID, p.Role = values["variant"], values["label"], values["id"], values["role"]
	p.Name, p.Value, p.Type, p.For, p.State, p.Layout = values["name"], values["value"], values["type"], values["for"], values["state"], values["layout"]
	p.Disabled, p.Required, p.Hidden = values["disabled"] == "true", values["required"] == "true", values["hidden"] == "true"
	p.Multiple, p.Checked, p.Open = values["multiple"] == "true", values["checked"] == "true", values["open"] == "true"
	p.Dismissible = values["dismissible"] == "true"
	return p, nil
}
