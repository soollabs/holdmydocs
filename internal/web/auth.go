package web

import (
	"log/slog"
	"net/http"

	"hmd/internal/httpmiddleware"
)

func (app *App) loginData(errMsg string) TemplateData {
	cfg := app.config()
	return TemplateData{
		Title:          "Login",
		Error:          errMsg,
		OIDCEnabled:    cfg.OIDC.Issuer != "",
		OIDCButtonText: cfg.OIDC.ButtonText,
		OIDCLocalLogin: cfg.OIDC.Issuer == "" || cfg.OIDC.LocalLogin,
		OIDCIcon:       app.OIDC != nil && app.OIDC.Icon() != nil,
	}
}

func (app *App) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	slog.Debug("rendering login page")
	w.Header().Set("Cache-Control", "no-store")
	app.render(w, r, http.StatusOK, "login", app.loginData(""))
}

func (app *App) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	username := r.FormValue("username")

	token, ok := app.Auth.LoginLimited(r.RemoteAddr, username, r.FormValue("password"))
	if !ok {
		slog.Warn("login failed", "username", username)
		app.render(w, r, http.StatusUnauthorized, "login", app.loginData("Invalid username or password"))
		return
	}

	slog.Info("login", "username", username)

	cookie := &http.Cookie{
		Name:     "hmd_session",
		Value:    token,
		HttpOnly: true,
		Secure:   httpmiddleware.SecureCookie(r, app.config()),
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
	}
	if r.FormValue("remember") != "" {
		cookie.MaxAge = 30 * 24 * 60 * 60
	}
	http.SetCookie(w, cookie)

	http.Redirect(w, r, app.landingPath(), http.StatusSeeOther)
}

func (app *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie("hmd_session")
	if cookie != nil {
		app.Auth.Logout(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "hmd_session",
		Value:    "",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   httpmiddleware.SecureCookie(r, app.config()),
		Path:     "/",
	})

	http.Redirect(w, r, "/_/login", http.StatusSeeOther)
}
