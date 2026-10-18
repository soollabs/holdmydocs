package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
	internalauth "hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/httpmiddleware"
)

type oidcClaims = internalauth.OIDCClaims
type OIDCAuth = internalauth.OIDC

func oidcUsername(claims oidcClaims) string { return claims.Username() }
func validateIconSVG(body []byte) error     { return internalauth.ValidateIconSVG(body) }
func loadOIDCIcon(ctx context.Context, icon string) ([]byte, error) {
	return internalauth.LoadOIDCIcon(ctx, icon)
}

const maxIconBytes = internalauth.MaxIconBytes

func NewOIDCAuth(ctx context.Context, cfg config.Config) (*OIDCAuth, error) {
	return internalauth.NewOIDC(ctx, internalauth.OIDCOptions{
		Issuer: cfg.OIDC.Issuer, ClientID: cfg.OIDC.ClientID, ClientSecret: cfg.OIDC.ClientSecret,
		BaseURL: cfg.BaseURL, Icon: cfg.OIDC.Icon,
	})
}

func oidcAdmitted(claims oidcClaims, cfg config.OIDCConfig) bool {
	return claims.Subject != "" && (cfg.AllowAnyAuthenticated || claims.Admitted(cfg.AllowedSubjects, cfg.AllowedEmailDomains))
}

func (app *App) handleOIDCIcon(w http.ResponseWriter, r *http.Request) {
	if app.OIDC == nil || app.OIDC.Icon() == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(app.OIDC.Icon()); err != nil {
		slog.Error("writing OIDC icon", "err", err)
	}
}

func (app *App) oidcFlowCookie(w http.ResponseWriter, r *http.Request, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, MaxAge: 300, HttpOnly: true, Secure: httpmiddleware.SecureCookie(r, app.config()),
		SameSite: http.SameSiteLaxMode, Path: "/_/auth/oidc/",
	})
}

func (app *App) clearOIDCFlowCookies(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{"hmd_oidc_state", "hmd_oidc_pkce", "hmd_oidc_continue"} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", MaxAge: -1, HttpOnly: true, Secure: httpmiddleware.SecureCookie(r, app.config()), SameSite: http.SameSiteLaxMode, Path: "/_/auth/oidc/"})
	}
}

func (app *App) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if app.OIDC == nil {
		http.NotFound(w, r)
		return
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state := hex.EncodeToString(random)
	pkce := oauth2.GenerateVerifier()
	app.oidcFlowCookie(w, r, "hmd_oidc_state", state)
	app.oidcFlowCookie(w, r, "hmd_oidc_pkce", pkce)
	if continuation := r.URL.Query()["continue"]; len(continuation) == 1 && continuation[0] != "" {
		app.oidcFlowCookie(w, r, "hmd_oidc_continue", state+"."+continuation[0])
	} else {
		http.SetCookie(w, &http.Cookie{
			Name: "hmd_oidc_continue", Value: "", MaxAge: -1,
			HttpOnly: true, Secure: httpmiddleware.SecureCookie(r, app.config()),
			SameSite: http.SameSiteLaxMode, Path: "/_/auth/oidc/",
		})
	}
	http.Redirect(w, r, app.OIDC.AuthCodeURL(state, pkce), http.StatusSeeOther)
}

func (app *App) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	continuationCookie, _ := r.Cookie("hmd_oidc_continue")
	app.clearOIDCFlowCookies(w, r)
	if app.OIDC == nil {
		http.NotFound(w, r)
		return
	}
	stateCookie, err := r.Cookie("hmd_oidc_state")
	states := r.URL.Query()["state"]
	if err != nil || stateCookie.Value == "" || len(states) != 1 ||
		subtle.ConstantTimeCompare([]byte(states[0]), []byte(stateCookie.Value)) != 1 {
		slog.Warn("OIDC callback state mismatch")
		http.Error(w, "OIDC login failed", http.StatusBadRequest)
		return
	}
	continuation := ""
	if continuationCookie != nil && continuationCookie.Value != "" {
		state, handle, ok := strings.Cut(continuationCookie.Value, ".")
		if !ok || handle == "" || subtle.ConstantTimeCompare([]byte(state), []byte(stateCookie.Value)) != 1 {
			http.Error(w, "OIDC login failed", http.StatusBadRequest)
			return
		}
		continuation = handle
	}
	pkceCookie, err := r.Cookie("hmd_oidc_pkce")
	codes := r.URL.Query()["code"]
	if err != nil || pkceCookie.Value == "" || len(codes) != 1 || codes[0] == "" {
		http.Error(w, "OIDC login failed", http.StatusBadRequest)
		return
	}
	claims, issuer, err := app.OIDC.Authenticate(r.Context(), codes[0], pkceCookie.Value)
	if err != nil {
		slog.Warn("OIDC authentication failed", "error", err)
		http.Error(w, "OIDC login failed", http.StatusBadGateway)
		return
	}
	oidcConfig := app.config().OIDC
	if !oidcAdmitted(claims, oidcConfig) {
		slog.Warn("OIDC login rejected")
		http.Error(w, "OIDC login failed", http.StatusUnauthorized)
		return
	}
	username := claims.Username()
	if !internalauth.ValidUsername(username) {
		http.Error(w, "OIDC login failed", http.StatusUnauthorized)
		return
	}
	username, err = app.Auth.EnsureOIDCUser(internalauth.OIDCIdentity{Issuer: issuer, Subject: claims.Subject}, username, claims.GitAuthor(), oidcConfig.DefaultScopes)
	if err != nil {
		slog.Warn("OIDC provisioning rejected", "error", err)
		http.Error(w, "OIDC login failed", http.StatusUnauthorized)
		return
	}
	sessionToken, ok := app.Auth.NewSession(username)
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	slog.Info("OIDC login", "username", username)
	http.SetCookie(w, &http.Cookie{Name: "hmd_session", Value: sessionToken, HttpOnly: true, Secure: httpmiddleware.SecureCookie(r, app.config()), SameSite: http.SameSiteLaxMode, Path: "/", MaxAge: 30 * 24 * 60 * 60})
	if continuation != "" {
		flowCookie, _ := r.Cookie("hmd_oauth_flow")
		if app.OAuth == nil || flowCookie == nil ||
			app.OAuth.BindAuthorization(continuation, flowCookie.Value, username, sessionToken) != nil {
			app.Auth.Logout(sessionToken)
			http.Error(w, "OAuth login continuation expired; start the connection again", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/_/oauth/authorize?request="+url.QueryEscape(continuation), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, app.landingPath(), http.StatusSeeOther)
}
