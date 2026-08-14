package auth

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type OIDCClaims struct {
	PreferredUsername string `json:"preferred_username"`
	Email             string `json:"email"`
	EmailVerified     bool   `json:"email_verified"`
	Name              string `json:"name"`
	Subject           string `json:"sub"`
}

func (claims OIDCClaims) Username() string {
	if claims.PreferredUsername != "" {
		return claims.PreferredUsername
	}
	if claims.EmailVerified {
		if index := strings.Index(claims.Email, "@"); index > 0 {
			return claims.Email[:index]
		}
	}
	return ""
}

func (claims OIDCClaims) Admitted(allowedSubjects, allowedDomains []string) bool {
	if !claims.EmailVerified {
		return false
	}
	if slices.Contains(allowedSubjects, claims.Subject) {
		return true
	}
	_, domain, ok := strings.Cut(claims.Email, "@")
	if !ok || domain == "" {
		return false
	}
	for _, allowed := range allowedDomains {
		if strings.EqualFold(domain, strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}

func (claims OIDCClaims) GitAuthor() string {
	if claims.Name == "" || claims.Email == "" {
		return ""
	}
	return claims.Name + " <" + claims.Email + ">"
}

type OIDCOptions struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	BaseURL      string
	Icon         string
}

type OIDC struct {
	OAuth            oauth2.Config
	Verifier         *oidc.IDTokenVerifier
	IconData         []byte
	AuthenticateFunc func(context.Context, string, string) (OIDCClaims, string, error)
}

const maxIconBytes = 256 * 1024
const MaxIconBytes = maxIconBytes

func NewOIDC(ctx context.Context, options OIDCOptions) (*OIDC, error) {
	provider, err := oidc.NewProvider(ctx, options.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery for %s: %w", options.Issuer, err)
	}
	var icon []byte
	if options.Icon != "" {
		icon, err = loadOIDCIcon(ctx, options.Icon)
		if err != nil {
			return nil, err
		}
	}
	return &OIDC{
		IconData: icon,
		OAuth: oauth2.Config{
			ClientID: options.ClientID, ClientSecret: options.ClientSecret,
			Endpoint: provider.Endpoint(), RedirectURL: strings.TrimSuffix(options.BaseURL, "/") + "/_/auth/oidc/callback",
			Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
		},
		Verifier: provider.Verifier(&oidc.Config{ClientID: options.ClientID}),
	}, nil
}

func (auth *OIDC) Icon() []byte { return auth.IconData }
func (auth *OIDC) AuthCodeURL(state, verifier string) string {
	return auth.OAuth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
}
func (auth *OIDC) Exchange(ctx context.Context, code, verifier string) (*oauth2.Token, error) {
	return auth.OAuth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
}
func (auth *OIDC) Verify(ctx context.Context, raw string) (*oidc.IDToken, error) {
	return auth.Verifier.Verify(ctx, raw)
}

func (auth *OIDC) Authenticate(ctx context.Context, code, verifier string) (OIDCClaims, string, error) {
	if auth.AuthenticateFunc != nil {
		return auth.AuthenticateFunc(ctx, code, verifier)
	}
	token, err := auth.Exchange(ctx, code, verifier)
	if err != nil {
		return OIDCClaims{}, "", err
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return OIDCClaims{}, "", fmt.Errorf("OIDC response has no ID token")
	}
	idToken, err := auth.Verify(ctx, raw)
	if err != nil {
		return OIDCClaims{}, "", err
	}
	var claims OIDCClaims
	if err := idToken.Claims(&claims); err != nil {
		return OIDCClaims{}, "", err
	}
	return claims, idToken.Issuer, nil
}

func LoadOIDCIcon(ctx context.Context, icon string) ([]byte, error) { return loadOIDCIcon(ctx, icon) }

func loadOIDCIcon(ctx context.Context, icon string) ([]byte, error) {
	var body []byte
	if strings.ContainsAny(icon, "/\\") || strings.HasSuffix(icon, ".svg") {
		var err error
		body, err = os.ReadFile(icon)
		if err != nil {
			return nil, fmt.Errorf("reading icon file: %w", err)
		}
	} else {
		url := "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/svg/" + icon + ".svg"
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
		if err != nil {
			return nil, fmt.Errorf("fetching Dashboard Icon %q: %w", icon, err)
		}
		defer func() {
			if err := response.Body.Close(); err != nil {
				slog.Debug("closing OIDC icon response", "err", err)
			}
		}()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetching Dashboard Icon %q: %s (check the name at dashboardicons.com)", icon, response.Status)
		}
		body, err = io.ReadAll(io.LimitReader(response.Body, maxIconBytes+1))
		if err != nil {
			return nil, fmt.Errorf("fetching Dashboard Icon %q: %w", icon, err)
		}
	}
	if err := ValidateIconSVG(body); err != nil {
		return nil, fmt.Errorf("icon %q: %w", icon, err)
	}
	return body, nil
}

func ValidateIconSVG(body []byte) error {
	if len(body) > maxIconBytes {
		return fmt.Errorf("larger than %d KiB", maxIconBytes/1024)
	}
	decoder := xml.NewDecoder(bytes.NewReader(body))
	for {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("not valid SVG: %v", err)
		}
		element, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if element.Name.Local != "svg" {
			return fmt.Errorf("not an SVG (root element <%s>)", element.Name.Local)
		}
		attributes := make(map[string]string)
		for _, attribute := range element.Attr {
			attributes[attribute.Name.Local] = attribute.Value
		}
		if viewBox := strings.Fields(attributes["viewBox"]); len(viewBox) == 4 {
			if viewBox[2] != viewBox[3] {
				return fmt.Errorf("must be square, viewBox is %s x %s", viewBox[2], viewBox[3])
			}
			return nil
		}
		if width, height := attributes["width"], attributes["height"]; width != "" && width == height {
			return nil
		}
		return fmt.Errorf("must be square (equal viewBox or width/height dimensions)")
	}
}
