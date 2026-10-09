package web

import (
	"bytes"
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

	wFS, _ := fs.Sub(webFS, "web/static")
	staticHandler := http.StripPrefix("/_/static/", http.FileServerFS(wFS))
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

	// These assets contain the build hash in their own source, so they are
	// rendered rather than served as frozen files. Exact routes outrank the
	// /_/static/ subtree their bytes live under.
	mux.HandleFunc("GET /_/static/sw.js", app.handleServiceWorker)
	mux.HandleFunc("GET /_/static/style.css", app.handleStyleSheet)

	mux.HandleFunc("GET /_/login", app.handleLoginGet)
	mux.HandleFunc("POST /_/login", app.handleLoginPost)
	mux.HandleFunc("POST /_/logout", app.handleLogout)
	mux.HandleFunc("GET /_/auth/oidc/login", app.handleOIDCLogin)
	mux.HandleFunc("GET /_/auth/oidc/callback", app.handleOIDCCallback)
	mux.HandleFunc("GET /_/auth/oidc/icon", app.handleOIDCIcon)
	if app.OAuth != nil {
		mux.HandleFunc("GET /_/oauth/authorize", app.handleOAuthAuthorizeGet)
		mux.HandleFunc("POST /_/oauth/authorize", app.handleOAuthAuthorizePost)
		mux.HandleFunc("GET /_/connections", app.handleConnectionsGet)
		mux.HandleFunc("POST /_/connections/{id}/revoke", app.handleConnectionRevoke)
		mux.HandleFunc("GET /_/admin/oauth", app.handleOAuthClientsGet)
		mux.HandleFunc("POST /_/admin/oauth", app.handleOAuthClientCreate)
		mux.HandleFunc("POST /_/admin/oauth/{id}/disable", app.handleOAuthClientDisable)
		mux.HandleFunc("POST /_/admin/oauth/{id}/delete", app.handleOAuthClientDelete)
		mux.HandleFunc("POST /_/admin/oauth/delete-disabled", app.handleOAuthClientsDeleteDisabled)
	}

	mux.HandleFunc("GET /_/tags", app.handleTagsIndex)
	mux.HandleFunc("GET /_/tags/{tag}", app.handleTagPages)

	mux.HandleFunc("GET /_/settings", app.handleSettingsGet)
	mux.HandleFunc("GET /_/admin", app.handleAdminGet)
	mux.HandleFunc("GET /_/namespaces", app.handleNamespacesGet)
	mux.HandleFunc("GET /_/namespaces/new", app.handleNamespaceNewGet)
	mux.HandleFunc("GET /_/namespaces/{name}/edit", app.handleNamespaceEditGet)
	mux.HandleFunc("GET /_/settings/namespaces/{name}/export", app.handleExportNamespace)
	mux.HandleFunc("GET /_/settings/namespaces/{name}/preview", app.handlePreviewExportNamespace)
	mux.HandleFunc("GET /_/export-preview/{id}/{path...}", app.handleExportPreviewFile)

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

func (app *App) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	app.serveVersionedStatic(w, "web/static/sw.js", "text/javascript; charset=utf-8")
}

func (app *App) handleStyleSheet(w http.ResponseWriter, r *http.Request) {
	app.serveVersionedStatic(w, "web/static/style.css", "text/css; charset=utf-8")
}

func (app *App) serveVersionedStatic(w http.ResponseWriter, path, contentType string) {
	body, err := webFS.ReadFile(path)
	if err != nil {
		http.Error(w, "static asset unavailable", http.StatusInternalServerError)
		return
	}
	body = bytes.ReplaceAll(body, []byte("__ASSET_VERSION__"), []byte(staticVersion))
	w.Header().Set("Content-Type", contentType)
	// Versioned URLs are safe to cache, but the service worker script itself
	// must be revalidated so a new build's hash reaches clients.
	if path == "web/static/sw.js" {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	_, _ = w.Write(body)
}

func (app *App) handleReady(w http.ResponseWriter, r *http.Request) {
	slog.Debug("readiness check listing repository")
	if !app.apiClient().Ready() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}
func reservedPath(path string) bool {
	return path == "_" || strings.HasPrefix(path, "_/")
}
