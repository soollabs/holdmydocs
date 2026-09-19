package web

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
)

func (app *App) landingPath() string {
	if slug := app.wikiConfig().Landing; slug != "" {
		return "/" + slug
	}
	if names := app.Namespaces().Names(); len(names) > 0 {
		return "/" + names[0] + "/"
	}
	return "/_/namespaces"
}

func (app *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, app.landingPath(), http.StatusSeeOther)
}

func (app *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /_/ready", app.handleReady)

	mux.HandleFunc("GET /{$}", app.handleRoot)

	mux.HandleFunc("GET /_/new", app.handleNewPage)

	fsys, _ := fs.Sub(webFS, "web/static")
	staticHandler := http.StripPrefix("/_/static/", http.FileServerFS(fsys))
	mux.Handle("GET /_/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// FileServer renders directory listings. Assets are public, but listing
		// embedded directories needlessly advertises every shipped file and can
		// expose one added by mistake in a later build.
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		staticHandler.ServeHTTP(w, r)
	}))

	mux.HandleFunc("GET /_/login", app.handleLoginGet)
	mux.HandleFunc("POST /_/login", app.handleLoginPost)
	mux.HandleFunc("POST /_/logout", app.handleLogout)
	mux.HandleFunc("GET /_/auth/oidc/login", app.handleOIDCLogin)
	mux.HandleFunc("GET /_/auth/oidc/callback", app.handleOIDCCallback)
	mux.HandleFunc("GET /_/auth/oidc/icon", app.handleOIDCIcon)

	mux.HandleFunc("GET /_/tags", app.handleTagsIndex)
	mux.HandleFunc("GET /_/tags/{tag}", app.handleTagPages)

	mux.HandleFunc("GET /_/settings", app.handleSettingsGet)
	mux.HandleFunc("GET /_/admin", app.handleAdminGet)
	mux.HandleFunc("GET /_/namespaces", app.handleNamespacesGet)
	mux.HandleFunc("GET /_/namespaces/new", app.handleNamespaceNewGet)
	mux.HandleFunc("GET /_/namespaces/{name}/edit", app.handleNamespaceEditGet)
	mux.HandleFunc("GET /_/settings/namespaces/{name}/export", app.handleExportNamespace)

	mux.HandleFunc("GET /_/search", app.handleSearch)
	if app.apiClient().DocumentsEnabled() {
		mux.HandleFunc("GET /_/search/attachments", app.handleAttachmentSearch)
	}
	mux.HandleFunc("GET /_/health-report", app.handleHealthReport)

	mux.HandleFunc("GET /_/hidden", app.handleHiddenIndex)
	mux.HandleFunc("GET /_/hidden/{path...}", app.handleHiddenGet)

	// The wildcard handler serves content after the reserved routes above.
	// Page mutations live on the JSON surface in httpapi, not here.
	mux.HandleFunc("GET /{path...}", app.handlePageGet)

	return mux
}

func (app *App) handleReady(w http.ResponseWriter, r *http.Request) {
	slog.Debug("readiness check listing repository")
	if !app.apiClient().Ready() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (app *App) externalBaseURL() string { return app.config().BaseURL }

func reservedPath(path string) bool {
	return path == "_" || strings.HasPrefix(path, "_/")
}
