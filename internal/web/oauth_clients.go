package web

import (
	"net/http"
	"strings"

	"hmd/internal/auth"
	"hmd/internal/oauthserver"
)

// Client administration requires a settings-capable browser session, never a
// bearer token. The shared security middleware enforces Origin and CSRF.
func (app *App) oauthClientAdmin(w http.ResponseWriter, r *http.Request) bool {
	if app.OAuth == nil {
		http.NotFound(w, r)
		return false
	}
	cookie, err := r.Cookie("hmd_session")
	if err != nil || r.Header.Get("Authorization") != "" {
		http.Error(w, "administrator browser session required", http.StatusUnauthorized)
		return false
	}
	user, ok := app.Auth.UserFor(cookie.Value)
	if !ok || !app.Auth.Prefs(user).HasScope(auth.ScopeSettings) {
		http.Error(w, "administrator access required", http.StatusForbidden)
		return false
	}
	return true
}

func (app *App) renderOAuthClients(w http.ResponseWriter, r *http.Request, status int, created *oauthserver.ProvisionedClient, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	// Native same-origin form POSTs need a non-opaque Origin. Suppress
	// cross-origin referrers without turning the form's Origin into null.
	w.Header().Set("Referrer-Policy", "same-origin")
	clients, err := app.OAuth.Clients()
	if err != nil {
		http.Error(w, "could not read OAuth clients", http.StatusInternalServerError)
		return
	}
	app.render(w, r, status, "oauth-clients", TemplateData{
		Authed: true, Title: "MCP OAuth clients", OAuthClients: clients,
		OAuthCreatedClient: created, Error: message,
	})
}

func (app *App) handleOAuthClientsGet(w http.ResponseWriter, r *http.Request) {
	if app.oauthClientAdmin(w, r) {
		app.renderOAuthClients(w, r, http.StatusOK, nil, "")
	}
}

func (app *App) handleOAuthClientCreate(w http.ResponseWriter, r *http.Request) {
	if !app.oauthClientAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid client form", http.StatusBadRequest)
		return
	}
	redirects := strings.Fields(r.PostForm.Get("redirect_uris"))
	client, err := app.OAuth.ProvisionClient(r.PostForm.Get("name"), redirects,
		r.PostForm.Get("auth_method"), r.PostForm["scope"])
	if err != nil {
		app.renderOAuthClients(w, r, http.StatusBadRequest, nil, err.Error())
		return
	}
	// Render directly: the one-time secret never enters a URL or cookie.
	app.renderOAuthClients(w, r, http.StatusCreated, &client, "")
}

func (app *App) handleOAuthClientDisable(w http.ResponseWriter, r *http.Request) {
	if !app.oauthClientAdmin(w, r) {
		return
	}
	if err := app.OAuth.DisableClient(r.PathValue("id")); err != nil {
		http.Error(w, "could not disable OAuth client", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/_/admin/oauth", http.StatusSeeOther)
}

func (app *App) handleOAuthClientDelete(w http.ResponseWriter, r *http.Request) {
	if !app.oauthClientAdmin(w, r) {
		return
	}
	if err := app.OAuth.DeleteClient(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/_/admin/oauth", http.StatusSeeOther)
}

func (app *App) handleOAuthClientsDeleteDisabled(w http.ResponseWriter, r *http.Request) {
	if !app.oauthClientAdmin(w, r) {
		return
	}
	if err := app.OAuth.DeleteDisabledClients(); err != nil {
		http.Error(w, "could not delete disabled MCP clients", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/_/admin/oauth", http.StatusSeeOther)
}
