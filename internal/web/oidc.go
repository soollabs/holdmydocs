package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"

	"golang.org/x/oauth2"
	internalauth "hmd/internal/auth"
)

type oidcClaims = internalauth.OIDCClaims
type OIDCAuth = internalauth.OIDC

func oidcUsername(claims oidcClaims) string { return claims.Username() }
func validateIconSVG(body []byte) error     { return internalauth.ValidateIconSVG(body) }
func loadOIDCIcon(ctx context.Context, icon string) ([]byte, error) {
	return internalauth.LoadOIDCIcon(ctx, icon)
}

const maxIconBytes = internalauth.MaxIconBytes

func NewOIDCAuth(ctx context.Context, cfg Config) (*OIDCAuth, error) {
	return internalauth.NewOIDC(ctx, internalauth.OIDCOptions{
		Issuer: cfg.OIDC.Issuer, ClientID: cfg.OIDC.ClientID, ClientSecret: cfg.OIDC.ClientSecret,
		BaseURL: cfg.OIDC.BaseURL, Icon: cfg.OIDC.Icon,
	})
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
		Name: name, Value: value, MaxAge: 300, HttpOnly: true, Secure: app.isSecureRequest(r),
		SameSite: http.SameSiteLaxMode, Path: "/_/auth/oidc/",
	})
}

func (app *App) clearOIDCFlowCookies(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{"hmd_oidc_state", "hmd_oidc_pkce"} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", MaxAge: -1, HttpOnly: true, Secure: app.isSecureRequest(r), SameSite: http.SameSiteLaxMode, Path: "/_/auth/oidc/"})
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
	http.Redirect(w, r, app.OIDC.AuthCodeURL(state, pkce), http.StatusSeeOther)
}

func (app *App) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if app.OIDC == nil {
		http.NotFound(w, r)
		return
	}
	defer app.clearOIDCFlowCookies(w, r)
	stateCookie, err := r.Cookie("hmd_oidc_state")
	if err != nil || stateCookie.Value == "" || r.URL.Query().Get("state") != stateCookie.Value {
		slog.Warn("OIDC callback state mismatch")
		http.Error(w, "OIDC login failed", http.StatusBadRequest)
		return
	}
	pkceCookie, err := r.Cookie("hmd_oidc_pkce")
	if err != nil || pkceCookie.Value == "" {
		http.Error(w, "OIDC login failed", http.StatusBadRequest)
		return
	}
	claims, issuer, err := app.OIDC.Authenticate(r.Context(), r.URL.Query().Get("code"), pkceCookie.Value)
	if err != nil {
		slog.Warn("OIDC authentication failed", "error", err)
		http.Error(w, "OIDC login failed", http.StatusBadGateway)
		return
	}
	oidcConfig := app.config().OIDC
	if claims.Subject == "" || !claims.Admitted(oidcConfig.AllowedSubjects, oidcConfig.AllowedEmailDomains) {
		slog.Warn("OIDC login rejected")
		http.Error(w, "OIDC login failed", http.StatusUnauthorized)
		return
	}
	username := claims.Username()
	if !validUsername(username) {
		http.Error(w, "OIDC login failed", http.StatusUnauthorized)
		return
	}
	username, err = app.Auth.EnsureOIDCUser(oidcIdentity{Issuer: issuer, Subject: claims.Subject}, username, claims.GitAuthor(), oidcConfig.DefaultScopes)
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
	http.SetCookie(w, &http.Cookie{Name: "hmd_session", Value: sessionToken, HttpOnly: true, Secure: app.isSecureRequest(r), SameSite: http.SameSiteLaxMode, Path: "/", MaxAge: 30 * 24 * 60 * 60})
	http.Redirect(w, r, app.landingPath(), http.StatusSeeOther)
}
