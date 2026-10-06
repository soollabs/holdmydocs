package web

import (
	"net/http"
	"net/url"
	"strings"

	"hmd/internal/httpmiddleware"
	"hmd/internal/oauthserver"
)

func (app *App) handleOAuthAuthorizeGet(w http.ResponseWriter, r *http.Request) {
	if app.OAuth == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !app.OAuth.CheckRateLimit(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "same-origin")
	session := ""
	if cookie, err := r.Cookie("hmd_session"); err == nil {
		session = cookie.Value
	}
	started, err := app.OAuth.StartAuthorization(r, session, app.Namespaces().Names())
	if err != nil {
		// RFC 6749 section 4.1.2.1: once the client's redirect URI has been
		// exactly validated, authorisation errors are sent back to the client.
		if redirect, ok := oauthserver.ErrorRedirect(err, app.OAuth.Issuer()); ok {
			http.Redirect(w, r, redirect, http.StatusSeeOther)
			return
		}
		writeOAuthWebError(w, err)
		return
	}
	if started.FlowCookie != "" {
		http.SetCookie(w, &http.Cookie{
			Name: "hmd_oauth_flow", Value: started.FlowCookie, MaxAge: 600,
			HttpOnly: true, Secure: httpmiddleware.SecureCookie(r, app.config()),
			SameSite: http.SameSiteLaxMode, Path: "/",
		})
	}
	if started.LoginRequired {
		query := url.Values{"continue": {started.Handle}}
		http.Redirect(w, r, "/_/login?"+query.Encode(), http.StatusSeeOther)
		return
	}
	if started.Prompt == nil {
		http.Error(w, "authorisation request unavailable", http.StatusBadRequest)
		return
	}
	app.render(w, r, http.StatusOK, "oauth-consent", TemplateData{
		Authed: true, Username: started.Prompt.User,
		Title: "Connect application", OAuthPrompt: started.Prompt,
	})
}

func (app *App) handleOAuthAuthorizePost(w http.ResponseWriter, r *http.Request) {
	if app.OAuth == nil {
		http.NotFound(w, r)
		return
	}
	if !app.OAuth.CheckRateLimit(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "same-origin")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid consent form", http.StatusBadRequest)
		return
	}
	handles := r.PostForm["request"]
	decisions := r.PostForm["decision"]
	modes := r.PostForm["namespace_mode"]
	if len(handles) != 1 || len(decisions) != 1 || len(modes) != 1 ||
		decisions[0] != "approve" && decisions[0] != "deny" {
		http.Error(w, "invalid consent form", http.StatusBadRequest)
		return
	}
	sessionCookie, err := r.Cookie("hmd_session")
	if err != nil || sessionCookie.Value == "" {
		http.Error(w, "sign in before approving a connection", http.StatusUnauthorized)
		return
	}
	user, ok := app.Auth.UserFor(sessionCookie.Value)
	if !ok {
		http.Error(w, "browser session has expired", http.StatusUnauthorized)
		return
	}
	flowCookie, err := r.Cookie("hmd_oauth_flow")
	if err != nil || flowCookie.Value == "" {
		http.Error(w, "OAuth browser binding is missing", http.StatusBadRequest)
		return
	}
	scopeValues := r.PostForm["scope"]
	namespaces := r.PostForm["namespace"]
	redirect, err := app.OAuth.CompleteAuthorization(
		handles[0], flowCookie.Value, user, sessionCookie.Value,
		decisions[0] == "approve", strings.Join(scopeValues, " "),
		modes[0], namespaces, app.Namespaces().Names(),
	)
	if err != nil {
		writeOAuthWebError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "hmd_oauth_flow", Value: "", MaxAge: -1,
		HttpOnly: true, Secure: httpmiddleware.SecureCookie(r, app.config()),
		SameSite: http.SameSiteLaxMode, Path: "/",
	})
	// A cross-origin redirect after a native form POST can be blocked by
	// form-action 'self'. Complete with a nonce-protected navigation and a
	// normal link fallback rather than weakening the page's CSP.
	app.render(w, r, http.StatusOK, "oauth-return", TemplateData{
		Authed: true, Username: user, Title: "Return to MCP client",
		OAuthRedirect: redirect,
	})
}

func writeOAuthWebError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if protocol, ok := err.(*oauthserver.ProtocolError); ok {
		status = protocol.Status
	}
	http.Error(w, err.Error(), status)
}

func (app *App) handleConnectionsGet(w http.ResponseWriter, r *http.Request) {
	if app.OAuth == nil {
		http.NotFound(w, r)
		return
	}
	if !app.OAuth.CheckRateLimit(w, r) {
		return
	}
	w.Header().Set("Referrer-Policy", "same-origin")
	session, err := r.Cookie("hmd_session")
	if err != nil || session.Value == "" {
		http.Error(w, "sign in to view connected applications", http.StatusUnauthorized)
		return
	}
	user, ok := app.Auth.UserFor(session.Value)
	if !ok {
		http.Error(w, "browser session has expired", http.StatusUnauthorized)
		return
	}
	connections, err := app.OAuth.Connections(user)
	if err != nil {
		http.Error(w, "could not read connections", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	app.render(w, r, http.StatusOK, "connections", TemplateData{
		Authed: true, Username: user, Title: "Connected applications",
		OAuthConnections: connections,
	})
}

func (app *App) handleConnectionRevoke(w http.ResponseWriter, r *http.Request) {
	if app.OAuth == nil {
		http.NotFound(w, r)
		return
	}
	if !app.OAuth.CheckRateLimit(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "same-origin")
	session, err := r.Cookie("hmd_session")
	if err != nil || session.Value == "" {
		http.Error(w, "sign in to disconnect an application", http.StatusUnauthorized)
		return
	}
	user, ok := app.Auth.UserFor(session.Value)
	if !ok {
		http.Error(w, "browser session has expired", http.StatusUnauthorized)
		return
	}
	if err := app.OAuth.RevokeConnection(user, r.PathValue("id")); err != nil {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/_/connections", http.StatusSeeOther)
}
