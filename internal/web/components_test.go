package web

import (
	"bytes"
	"strings"
	"testing"
)

func TestButtonComponentContract(t *testing.T) {
	templates, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		label, variant string
		options        []string
		want           []string
	}{
		{"Save", "primary", []string{"type", "submit", "form", "appearance-form"}, []string{`<button class="ui-button ui-button--primary"`, `type="submit" form="appearance-form"`, `>Save</button>`}},
		{"Cancel", "secondary", []string{"href", "/_/settings/namespaces", "new-tab", "true"}, []string{`<a class="ui-button ui-button--secondary"`, `href="/_/settings/namespaces"`, `target="_blank" rel="noopener noreferrer"`}},
		{"Delete", "danger", []string{"type", "submit", "formaction", "/_/api/namespaces/delete", "confirm", "Delete everything?", "aria-label", "Delete notes"}, []string{`ui-button--danger`, `formaction="/_/api/namespaces/delete"`, `data-confirm="Delete everything?"`, `aria-label="Delete notes"`}},
		{"Approve", "primary", []string{"type", "submit", "name", "decision", "value", "approve", "disabled", "true"}, []string{`name="decision" value="approve" disabled`}},
		{"Deny", "secondary", []string{"type", "submit", "formnovalidate", "true"}, []string{`formnovalidate>Deny`}},
		{"Copy", "secondary", []string{"copy", "client-id", "hidden", "true"}, []string{`data-oauth-copy="client-id" hidden`, `type="button"`}},
		{"tree", "secondary", []string{"hook", "widget-chip", "widget", "tree", "pressed", "false"}, []string{`ui-button--secondary widget-chip`, `data-widget="tree" aria-pressed="false"`}},
		{"Icon action", "secondary", []string{"size", "icon", "aria-label", "Open menu"}, []string{`ui-button--icon`, `aria-label="Open menu"`}},
		{"Editor action", "secondary", []string{"size", "compact", "data-action", "bold"}, []string{`ui-button--compact`, `data-action="bold"`}},
		{"Zen action", "secondary", []string{"size", "compact", "data-zen", "focus"}, []string{`data-zen="focus"`}},
		{"Sidebar toggle", "secondary", []string{"size", "icon", "aria-label", "Toggle navigation", "aria-controls", "sidebar", "aria-expanded", "false"}, []string{`aria-controls="sidebar" aria-expanded="false"`}},
		{"Dialog trigger", "secondary", []string{"aria-haspopup", "dialog"}, []string{`aria-haspopup="dialog"`}},
		{"Disabled link", "secondary", []string{"href", "/_/settings", "disabled", "true"}, []string{`aria-disabled="true" tabindex="-1"`}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			props, err := makeButton(tc.label, tc.variant, tc.options...)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := templates["settings"].ExecuteTemplate(&out, "ui-button", props); err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q in %s", want, out.String())
				}
			}
			if tc.label == "Disabled link" && strings.Contains(out.String(), `href=`) {
				t.Error("disabled links must not navigate")
			}
		})
	}
}

func TestConsumerTemplatesUseSharedPrimitivesAcrossScreenFamilies(t *testing.T) {
	coverage := map[string][]string{
		"base":                  {`ui-button`, `ui-field-slot-start`, `ui-choice-slot-start`, `ui-panel-start`, `ui-dialog-slot-start`, `components.css`, `components.js`},
		"login":                 {`ui-field-start`, `ui-choice-slot-start`, `ui-panel-start`, `ui-notice-start`},
		"edit":                  {`ui-button-start`, `ui-field-start`, `ui-choice-slot-start`, `ui-notice-start`, `data-action`, `data-zen`},
		"page":                  {`ui-notice-start`, `ui-button`},
		"create":                {`ui-panel-start`, `ui-button`},
		"error":                 {`ui-panel-start`, `ui-button`},
		"search":                {`ui-field-slot-start`, `ui-panel-start`, `ui-empty-start`},
		"tags":                  {`ui-empty-start`},
		"hidden":                {`ui-empty-start`},
		"history":               {`ui-choice-slot-start`, `ui-panel-start`, `ui-button`},
		"settings":              {`ui-field-start`, `ui-field-slot-start`, `ui-choice-slot-start`, `ui-notice-start`},
		"admin":                 {`ui-field-slot-start`, `ui-choice-slot-start`, `ui-notice-start`},
		"namespace-edit":        {`ui-field-slot-start`, `ui-choice-slot-start`, `ui-panel-start`, `export-link-row-template`},
		"namespaces":            {`ui-button`, `ui-empty-start`},
		"oauth-consent":         {`ui-choice-slot-start`, `ui-disclosure-start`, `ui-panel-start`, `ui-notice-start`},
		"oauth-clients":         {`ui-field-slot-start`, `ui-choice-slot-start`, `ui-disclosure-slot-start`, `ui-panel-start`, `ui-empty-start`},
		"connections":           {`ui-panel-start`, `ui-empty-start`},
		"export-options":        {`ui-notice-start`, `ui-panel-start`},
		"widgets/calendar":      {`ui-panel-start`},
		"widgets/health":        {`ui-notice-start`},
		"widgets/log":           {`ui-disclosure-slot-start`},
		"widgets/namespaces":    {`ui-panel-start`},
		"widgets/pages":         {`ui-panel-start`},
		"widgets/pinned":        {`ui-panel-start`},
		"widgets/tags":          {`ui-panel-start`},
		"widgets/writing-stats": {`ui-panel-start`},
		"widgets/backlinks":     {`ui-panel-start`},
		"widgets/prev-entries":  {`ui-panel-start`},
	}
	for name, fragments := range coverage {
		templatePath := "web/templates/" + name + ".html"
		source, err := webFS.ReadFile(templatePath)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(source), fragment) {
				t.Errorf("%s is missing shared primitive use %q", templatePath, fragment)
			}
		}
	}
	base, err := webFS.ReadFile("web/templates/base.html")
	if err != nil {
		t.Fatal(err)
	}
	baseHTML := string(base)
	if strings.Index(baseHTML, `skins.css`) > strings.Index(baseHTML, `components.css`) {
		t.Error("components.css must follow skin definitions")
	}
	if strings.Index(baseHTML, `components.js`) > strings.Index(baseHTML, `app.js`) {
		t.Error("shared runtime must precede dependent feature scripts")
	}
	login, err := webFS.ReadFile("web/templates/login.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(login), `<a href="/_/auth/oidc/login`) && strings.Contains(string(login), `<button`) {
		t.Error("SSO navigation must not nest a button inside a link")
	}
}

func TestComponentSlotsPreserveConsumerOwnedNativeControls(t *testing.T) {
	templates, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	choice, err := makeComponent("choice", "id", "approval", "type", "checkbox")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := templates["settings"].ExecuteTemplate(&out, "ui-choice-slot-start", choice); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`<label for="approval"><input id="approval" type="checkbox" name="approval" value="yes" checked disabled required> Approval</label>`)
	if err := templates["settings"].ExecuteTemplate(&out, "ui-choice-end", choice); err != nil {
		t.Fatal(err)
	}
	for _, attr := range []string{`id="approval"`, `type="checkbox"`, `name="approval"`, `value="yes"`, `checked`, `disabled`, `required`, `<label for="approval">`} {
		if !strings.Contains(out.String(), attr) {
			t.Errorf("choice slot lost native attribute %q: %s", attr, out.String())
		}
	}
}

func TestComponentExplicitFalseStateRemainsClosedAndUnchecked(t *testing.T) {
	templates, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind, template string
		options        []string
		forbidden      string
	}{
		{"choice", "ui-choice-start", []string{"id", "terms", "checked", "false"}, ` checked`},
		{"disclosure", "ui-disclosure-start", []string{"label", "Advanced", "open", "false"}, ` open`},
	} {
		props, err := makeComponent(tc.kind, tc.options...)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := templates["settings"].ExecuteTemplate(&out, tc.template, props); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), tc.forbidden) {
			t.Errorf("%s false state unexpectedly rendered %q", tc.kind, tc.forbidden)
		}
	}
}

func TestButtonComponentSupportsConsumerOwnedRichContent(t *testing.T) {
	templates, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	props, err := makeButton("Bold", "secondary", "size", "compact", "data-action", "bold")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := templates["settings"].ExecuteTemplate(&out, "ui-button-start", props); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`<strong>B</strong>`)
	if err := templates["settings"].ExecuteTemplate(&out, "ui-button-end", props); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `data-action="bold"`) || !strings.Contains(out.String(), `<strong>B</strong></button>`) {
		t.Fatalf("rich button slot output %q", out.String())
	}
}

func TestButtonComponentEscapesContentAndRejectsUnsafeOptions(t *testing.T) {
	for _, tc := range []struct {
		variant string
		options []string
	}{
		{"bespoke", nil},
		{"primary", []string{"type"}},
		{"primary", []string{"style", "border-radius:99px"}},
		{"primary", []string{"onclick", "alert(1)"}},
		{"primary", []string{"hook", "bespoke-button"}},
		{"primary", []string{"type", "unknown"}},
		{"primary", []string{"disabled", "unknown"}},
		{"primary", []string{"size", "huge"}},
		{"secondary", []string{"size", "icon"}},
		{"secondary", []string{"data-action", "Not Real"}},
		{"secondary", []string{"data-zen", "x\" onclick=\"y"}},
		{"secondary", []string{"aria-controls", "#sidebar"}},
		{"secondary", []string{"aria-haspopup", "popover"}},
	} {
		if _, err := makeButton("Action", tc.variant, tc.options...); err == nil {
			t.Errorf("accepted invalid options: %+v", tc)
		}
	}
	templates, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	props, err := makeButton(`<script>alert(1)</script>`, "secondary", "href", "javascript:alert(1)")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := templates["settings"].ExecuteTemplate(&out, "ui-button", props); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "<script>") || strings.Contains(out.String(), `href="javascript:`) {
		t.Fatalf("unsafe component output: %s", out.String())
	}
	if !strings.Contains(out.String(), "#ZgotmplZ") {
		t.Fatal("URL filtering should be preserved")
	}
}

func TestSemanticComponentContracts(t *testing.T) {
	templates, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind               string
		opts               []string
		template, contains string
	}{
		{"field", []string{"id", "search", "label", "Search"}, "ui-field-start", `<label class="ui-field__label" for="search">Search</label>`},
		{"choice", []string{"id", "terms", "type", "checkbox", "name", "terms", "value", "yes", "checked", "true"}, "ui-choice-start", `type="checkbox" name="terms" value="yes" checked`},
		{"notice", []string{"variant", "danger", "role", "alert", "hidden", "true"}, "ui-notice-start", `role="alert" hidden`},
		{"notice", []string{"role", "status", "dismissible", "true", "banner", "help"}, "ui-notice-start", `ui-notice--banner" role="status" data-dismiss="help"`},
		{"disclosure", []string{"label", "Advanced", "open", "true"}, "ui-disclosure-start", `<details class="ui-disclosure" open>`},
		{"dialog", []string{"id", "confirm", "label", "Confirm"}, "ui-dialog-start", `aria-labelledby="confirm-title"`},
		{"menu", []string{"label", "Actions"}, "ui-menu-start", `role="group"`},
	} {
		p, err := makeComponent(tc.kind, tc.opts...)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err = templates["settings"].ExecuteTemplate(&out, tc.template, p); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), tc.contains) {
			t.Errorf("%s output %q missing %q", tc.kind, out.String(), tc.contains)
		}
	}
	for _, tc := range []struct {
		kind string
		opts []string
	}{{"wat", nil}, {"field", []string{"id", "x", "type", "file"}}, {"choice", []string{"id", "x", "type", "button"}}, {"notice", []string{"role", "dialog"}}, {"field", []string{"id"}}, {"dialog", []string{"id", "x"}}, {"menu", nil}, {"choice", []string{"id", "x", "checked", "invalid"}}, {"field", []string{"id", "x", "checked", "true"}}, {"disclosure", []string{"open", "invalid"}}, {"panel", []string{"open", "true"}}} {
		if _, err := makeComponent(tc.kind, tc.opts...); err == nil {
			t.Errorf("accepted invalid component: %+v", tc)
		}
	}
	p, err := makeComponent("field", "id", "q", "label", `<img src=x onerror=alert(1)>`)
	if err != nil {
		t.Fatal(err)
	}
	var escaped bytes.Buffer
	if err = templates["settings"].ExecuteTemplate(&escaped, "ui-field-start", p); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(escaped.String(), "<img") || !strings.Contains(escaped.String(), "&lt;img") {
		t.Fatalf("component label not escaped: %s", escaped.String())
	}
}

func TestComponentStylesOwnSharedAppearanceAndStates(t *testing.T) {
	components, err := webFS.ReadFile("web/static/components.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{":where(.ui-button)", ":hover", ".ui-button:disabled", ":user-invalid", "focus-visible", "@media (max-width: 899px)", "min-height:44px"} {
		if !strings.Contains(string(components), rule) {
			t.Errorf("component stylesheet lacks shared state/layout rule %q", rule)
		}
	}
	for _, name := range []string{"web/static/style.css", "web/static/skins.css", "web/static/export.css"} {
		css, err := webFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(css), ".ui-button {") || strings.Contains(string(css), ".ui-field {") {
			t.Errorf("%s still owns shared component appearance", name)
		}
	}
}

func TestSettingsScreensUseSharedButtonComponent(t *testing.T) {
	for _, name := range []string{"settings", "admin", "namespaces", "namespace-edit", "oauth-clients", "connections", "export-options", "oauth-consent"} {
		source, err := webFS.ReadFile("web/templates/" + name + ".html")
		if err != nil {
			t.Fatal(err)
		}
		for _, obsolete := range []string{"<button", "ghost-btn", "oauth-primary", "oauth-danger", "danger-btn", `class="primary"`, `style=`} {
			if strings.Contains(string(source), obsolete) {
				t.Errorf("%s bypasses shared buttons with %q", name, obsolete)
			}
		}
		// A settings link standing alone is a button; one inside a note's prose is text.
		for line := range strings.SplitSeq(string(source), "\n") {
			if strings.Contains(line, `<a href="/_/settings`) && !strings.Contains(line, `class="settings-note"`) {
				t.Errorf("%s bypasses shared buttons with a bare settings link: %s", name, strings.TrimSpace(line))
			}
		}
		if !strings.Contains(string(source), `template "ui-button"`) {
			t.Errorf("%s does not use the shared component", name)
		}
		for _, redundant := range []string{"Manage namespaces", "Back to settings", "system configuration &rarr; namespaces"} {
			if strings.Contains(string(source), redundant) {
				t.Errorf("%s retains redundant section navigation: %s", name, redundant)
			}
		}
	}
	css, err := webFS.ReadFile("web/static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, obsolete := range []string{".oauth-page .oauth-primary", ".oauth-page button", ".settings-actions .primary", ".settings-actions .ghost-btn", ".widget-chip {", ".widget-chip,"} {
		if strings.Contains(string(css), obsolete) {
			t.Errorf("page-specific button rule remains: %s", obsolete)
		}
	}
}
