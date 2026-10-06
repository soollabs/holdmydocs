package web

import (
	"fmt"
	"html/template"
)

var webFS = FS

func parseTemplates() (map[string]*template.Template, error) {
	templates := make(map[string]*template.Template)
	funcs := template.FuncMap{"static": staticURL}
	widgetFiles := []string{
		"web/templates/widgets/tree.html", "web/templates/widgets/pages.html",
		"web/templates/widgets/namespaces.html", "web/templates/widgets/pinned.html",
		"web/templates/widgets/tags.html", "web/templates/widgets/log.html",
		"web/templates/widgets/health.html", "web/templates/widgets/keys.html",
		"web/templates/widgets/calendar.html", "web/templates/widgets/writing-stats.html",
		"web/templates/widgets/outline.html", "web/templates/widgets/page-meta.html",
		"web/templates/widgets/backlinks.html", "web/templates/widgets/prev-entries.html",
	}
	for _, name := range []string{"login", "page", "edit", "create", "error", "search", "history", "tags", "settings", "admin", "hidden", "namespace", "namespaces", "namespace-edit", "oauth-consent", "oauth-return", "connections", "oauth-clients"} {
		files := append([]string{"web/templates/base.html", "web/templates/" + name + ".html"}, widgetFiles...)
		parsed, err := template.New(name).Funcs(funcs).ParseFS(webFS, files...)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", name, err)
		}
		templates[name] = parsed
	}
	return templates, nil
}
