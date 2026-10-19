package web

import (
	"fmt"
	"html/template"

	"hmd/internal/config"
	"hmd/internal/i18n"
	"hmd/internal/wiki"
)

var webFS = FS

var templateFuncs = template.FuncMap{"static": staticURL, "pageurl": wiki.PageURL, "hiddenpageurl": wiki.HiddenPageURL, "namespaceurl": wiki.NamespaceURL, "button": makeButton, "component": makeComponent, "settinginfo": makeSettingInfo, "settingattrs": makeSettingAttrs, "settinggroups": makeSettingGroups, "settingsections": config.Sections, "envname": config.EnvName, "t": i18n.T, "tl": translateHTML, "jsstrings": func() map[string]any { return i18n.Messages("js.") }, "code": codeHTML, "strong": strongHTML, "link": linkHTML, "tn": i18n.TN, "lang": i18n.Language}

func parseTemplates() (map[string]*template.Template, error) {
	templates := make(map[string]*template.Template)
	funcs := templateFuncs
	widgetFiles := []string{
		"web/templates/widgets/tree.html", "web/templates/widgets/pages.html",
		"web/templates/widgets/namespaces.html", "web/templates/widgets/pinned.html",
		"web/templates/widgets/tags.html", "web/templates/widgets/log.html",
		"web/templates/widgets/health.html", "web/templates/widgets/keys.html",
		"web/templates/widgets/calendar.html", "web/templates/widgets/writing-stats.html",
		"web/templates/widgets/outline.html", "web/templates/widgets/page-meta.html",
		"web/templates/widgets/backlinks.html", "web/templates/widgets/prev-entries.html",
	}
	for _, name := range []string{"login", "page", "edit", "create", "error", "search", "history", "tags", "settings", "admin", "hidden", "namespace", "namespaces", "namespace-edit", "export-options", "oauth-consent", "oauth-return", "connections", "oauth-clients"} {
		files := append([]string{"web/templates/base.html", "web/templates/components/*.html", "web/templates/" + name + ".html"}, widgetFiles...)
		parsed, err := template.New(name).Funcs(funcs).ParseFS(webFS, files...)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", name, err)
		}
		templates[name] = parsed
	}
	return templates, nil
}
