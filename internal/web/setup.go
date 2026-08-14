package web

import (
	"fmt"
	"html/template"
	"net/http"
	"time"
)

var webFS = FS

func parseTemplates() (map[string]*template.Template, error) {
	templates := make(map[string]*template.Template)
	widgetFiles := []string{
		"web/templates/widgets/tree.html", "web/templates/widgets/pages.html",
		"web/templates/widgets/namespaces.html", "web/templates/widgets/pinned.html",
		"web/templates/widgets/tags.html", "web/templates/widgets/log.html",
		"web/templates/widgets/health.html", "web/templates/widgets/keys.html",
		"web/templates/widgets/calendar.html", "web/templates/widgets/writing-stats.html",
		"web/templates/widgets/outline.html", "web/templates/widgets/page-meta.html",
		"web/templates/widgets/backlinks.html", "web/templates/widgets/prev-entries.html",
	}
	for _, name := range []string{"login", "page", "edit", "conflict", "create", "error", "search", "history", "tags", "settings", "admin", "hidden", "namespace", "namespaces", "namespace-edit"} {
		files := append([]string{"web/templates/base.html", "web/templates/" + name + ".html"}, widgetFiles...)
		parsed, err := template.ParseFS(webFS, files...)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", name, err)
		}
		templates[name] = parsed
	}
	return templates, nil
}

func checkReadiness() error { return checkReadinessAt("http://127.0.0.1:8080/_/ready") }

func checkReadinessAt(url string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("checking readiness: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("checking readiness: HTTP %d", response.StatusCode)
	}
	return nil
}
