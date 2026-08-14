package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// oidcClaims are the ID-token claims we care about.
type oidcClaims struct {
	PreferredUsername string `json:"preferred_username"`
	Email             string `json:"email"`
	EmailVerified     bool   `json:"email_verified"`
	Name              string `json:"name"`
	Subject           string `json:"sub"`
}

// oidcUsername resolves the local username from ID-token claims:
// preferred_username, falling back to the email local part. Empty means
// the token is unusable and login must be rejected.
func oidcUsername(c oidcClaims) string {
	if c.PreferredUsername != "" {
		return c.PreferredUsername
	}
	if c.EmailVerified {
		if i := strings.Index(c.Email, "@"); i > 0 {
			return c.Email[:i]
		}
	}
	return ""
}

func (c oidcClaims) admitted(cfg OIDCConfig) bool {
	if !c.EmailVerified {
		return false
	}
	for _, subject := range cfg.AllowedSubjects {
		if c.Subject == subject {
			return true
		}
	}
	_, domain, ok := strings.Cut(c.Email, "@")
	if !ok || domain == "" {
		return false
	}
	for _, allowed := range cfg.AllowedEmailDomains {
		if strings.EqualFold(domain, strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}

// oidcGitAuthor builds a "Name <email>" author string from claims, or ""
// when either part is missing.
func oidcGitAuthor(c oidcClaims) string {
	if c.Name == "" || c.Email == "" {
		return ""
	}
	return c.Name + " <" + c.Email + ">"
}

// OIDCAuth holds the provider wiring built once at startup.
type OIDCAuth struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	icon     []byte // SVG served at /auth/oidc/icon, nil when unset
}

const maxIconBytes = 256 * 1024

// loadOIDCIcon resolves the configured icon. A value containing a path
// separator or ending in .svg is read from disk; anything else is treated
// as a Dashboard Icons name (https://dashboardicons.com/) and fetched once
// from its CDN at startup, so the login page never depends on the CDN.
func loadOIDCIcon(ctx context.Context, icon string) ([]byte, error) {
	var b []byte
	if strings.ContainsAny(icon, "/\\") || strings.HasSuffix(icon, ".svg") {
		var err error
		b, err = os.ReadFile(icon)
		if err != nil {
			return nil, fmt.Errorf("reading icon file: %w", err)
		}
	} else {
		url := "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/svg/" + icon + ".svg"
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching Dashboard Icon %q: %w", icon, err)
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				slog.Debug("closing OIDC icon response", "err", err)
			}
		}()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetching Dashboard Icon %q: %s (check the name at dashboardicons.com)", icon, resp.Status)
		}
		b, err = io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
		if err != nil {
			return nil, fmt.Errorf("fetching Dashboard Icon %q: %w", icon, err)
		}
	}
	if err := validateIconSVG(b); err != nil {
		return nil, fmt.Errorf("icon %q: %w", icon, err)
	}
	return b, nil
}

// validateIconSVG enforces that b is a square SVG of sane size, so an
// arbitrary custom file cannot distort the login button.
func validateIconSVG(b []byte) error {
	if len(b) > maxIconBytes {
		return fmt.Errorf("larger than %d KiB", maxIconBytes/1024)
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("not valid SVG: %v", err)
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if el.Name.Local != "svg" {
			return fmt.Errorf("not an SVG (root element <%s>)", el.Name.Local)
		}
		attrs := make(map[string]string)
		for _, a := range el.Attr {
			attrs[a.Name.Local] = a.Value
		}
		// Square by viewBox when present, else by width/height.
		if vb := strings.Fields(attrs["viewBox"]); len(vb) == 4 {
			if vb[2] != vb[3] {
				return fmt.Errorf("must be square, viewBox is %s x %s", vb[2], vb[3])
			}
			return nil
		}
		if w, h := attrs["width"], attrs["height"]; w != "" && w == h {
			return nil
		}
		return fmt.Errorf("must be square (equal viewBox or width/height dimensions)")
	}
}

func (app *App) handleOIDCIcon(w http.ResponseWriter, r *http.Request) {
	if app.OIDC == nil || app.OIDC.icon == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	// SVG can carry scripts; harmless in an <img>, but lock it down for
	// direct navigation too.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(app.OIDC.icon); err != nil {
		slog.Error("writing OIDC icon", "err", err)
	}
}

// NewOIDCAuth runs discovery against cfg.OIDC.Issuer and builds the OAuth2
// config. Called at startup only when the issuer is set; a failure here is
// fatal, consistent with other startup errors.
func NewOIDCAuth(ctx context.Context, cfg Config) (*OIDCAuth, error) {
	provider, err := oidc.NewProvider(ctx, cfg.OIDC.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery for %s: %w", cfg.OIDC.Issuer, err)
	}
	var icon []byte
	if cfg.OIDC.Icon != "" {
		if icon, err = loadOIDCIcon(ctx, cfg.OIDC.Icon); err != nil {
			return nil, err
		}
	}
	return &OIDCAuth{
		icon: icon,
		oauth: oauth2.Config{
			ClientID:     cfg.OIDC.ClientID,
			ClientSecret: cfg.OIDC.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  strings.TrimSuffix(cfg.OIDC.BaseURL, "/") + "/_/auth/oidc/callback",
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.OIDC.ClientID}),
	}, nil
}

// oidcFlowCookie sets a short-lived HttpOnly cookie carrying flow state
// (state nonce, PKCE verifier) between the login redirect and the callback.
func oidcFlowCookie(w http.ResponseWriter, r *http.Request, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		MaxAge:   300,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		Path:     "/_/auth/oidc/",
	})
}

func clearOIDCFlowCookies(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{"hmd_oidc_state", "hmd_oidc_pkce"} {
		// MaxAge below zero asks the browser to delete the cookie immediately.
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", MaxAge: -1, HttpOnly: true, Secure: isSecureRequest(r), SameSite: http.SameSiteLaxMode, Path: "/_/auth/oidc/"})
	}
}

func (app *App) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if app.OIDC == nil {
		http.NotFound(w, r)
		return
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state := hex.EncodeToString(b)
	pkce := oauth2.GenerateVerifier()

	oidcFlowCookie(w, r, "hmd_oidc_state", state)
	oidcFlowCookie(w, r, "hmd_oidc_pkce", pkce)

	http.Redirect(w, r, app.OIDC.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(pkce)), http.StatusSeeOther)
}

func (app *App) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if app.OIDC == nil {
		http.NotFound(w, r)
		return
	}
	defer clearOIDCFlowCookies(w, r)

	stateCookie, err := r.Cookie("hmd_oidc_state")
	if err != nil || stateCookie.Value == "" || r.URL.Query().Get("state") != stateCookie.Value {
		slog.Warn("OIDC callback state mismatch", "remote", r.RemoteAddr)
		http.Error(w, "OIDC login failed", http.StatusBadRequest)
		return
	}
	pkceCookie, err := r.Cookie("hmd_oidc_pkce")
	if err != nil || pkceCookie.Value == "" {
		http.Error(w, "OIDC login failed", http.StatusBadRequest)
		return
	}

	token, err := app.OIDC.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(pkceCookie.Value))
	if err != nil {
		slog.Warn("OIDC code exchange failed", "error", err)
		http.Error(w, "OIDC login failed", http.StatusBadGateway)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "OIDC login failed", http.StatusBadGateway)
		return
	}
	idToken, err := app.OIDC.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		slog.Warn("OIDC ID token verification failed", "error", err)
		http.Error(w, "OIDC login failed", http.StatusUnauthorized)
		return
	}

	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "OIDC login failed", http.StatusBadGateway)
		return
	}
	if claims.Subject == "" || !claims.EmailVerified || !claims.admitted(app.config().OIDC) {
		slog.Warn("OIDC login rejected", "remote", r.RemoteAddr)
		http.Error(w, "OIDC login failed", http.StatusUnauthorized)
		return
	}
	username := oidcUsername(claims)
	if !validUsername(username) {
		http.Error(w, "OIDC login failed", http.StatusUnauthorized)
		return
	}

	username, err = app.Auth.EnsureOIDCUser(oidcIdentity{Issuer: idToken.Issuer, Subject: claims.Subject}, username, oidcGitAuthor(claims), app.config().OIDC.DefaultScopes)
	if err != nil {
		slog.Warn("OIDC provisioning rejected", "remote", r.RemoteAddr, "error", err)
		http.Error(w, "OIDC login failed", http.StatusUnauthorized)
		return
	}
	sessionToken, ok := app.Auth.newSession(username)
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	slog.Info("OIDC login", "username", username, "remote", r.RemoteAddr)
	http.SetCookie(w, &http.Cookie{
		Name:     "hmd_session",
		Value:    sessionToken,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
		MaxAge:   30 * 24 * 60 * 60, // SSO users get the "remember me" lifetime
	})
	http.Redirect(w, r, app.landingPath(), http.StatusSeeOther)
}
