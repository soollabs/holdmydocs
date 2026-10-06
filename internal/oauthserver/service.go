package oauthserver

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	oauth2 "github.com/go-oauth2/oauth2/v4"
	oautherrors "github.com/go-oauth2/oauth2/v4/errors"
	"github.com/go-oauth2/oauth2/v4/manage"
	"hmd/internal/auth"
)

const (
	authorizationCodeTTL = 2 * time.Minute
	accessTokenTTL       = 15 * time.Minute
	grantFamilyTTL       = 30 * 24 * time.Hour
	maxPendingRequests   = 256
)

type trustedContextKey string

const (
	trustedGrantIDKey       trustedContextKey = "oauth-grant-id"
	trustedFamilyIDKey      trustedContextKey = "oauth-family-id"
	trustedIssuerKey        trustedContextKey = "oauth-issuer"
	trustedResourceKey      trustedContextKey = "oauth-resource"
	trustedNamespaceModeKey trustedContextKey = "oauth-namespace-mode"
	trustedNamespacesKey    trustedContextKey = "oauth-namespaces"
	trustedRefreshGenKey    trustedContextKey = "oauth-refresh-generation"
	trustedScopeKey         trustedContextKey = "oauth-scope"
)

// Service is the configured MCP OAuth implementation. It owns protocol state
// adapters but delegates browser rendering to internal/web.
type Service struct {
	store              *Store
	auth               *auth.Auth
	options            ServerOptions
	manager            *manage.Manager
	tokenStore         tokenStore
	clients            clientStore
	pendingMu          sync.Mutex
	pending            map[string]*pendingAuthorization
	rateMu             sync.Mutex
	rateLimits         map[string]requestWindow
	registrationWindow requestWindow
}

type requestWindow struct {
	start time.Time
	count int
}

const (
	oauthRateWindow = time.Minute
	oauthRateLimit  = 60
	maxRateEntries  = 4096
)

// NewService configures the OAuth library with explicit safe grant, PKCE and
// lifetime settings. The client and token stores are the private transactional
// AppDir adapters.
func NewService(opts ServerOptions, store *Store, authn *auth.Auth) (*Service, error) {
	if opts.Issuer == "" || store == nil || authn == nil {
		return nil, errors.New("OAuth issuer, state store and auth service are required")
	}
	issuer, err := url.Parse(opts.Issuer)
	if err != nil || !issuer.IsAbs() || issuer.Host == "" || issuer.Path != "" ||
		issuer.RawQuery != "" || issuer.ForceQuery || issuer.Fragment != "" || issuer.User != nil {
		return nil, errors.New("OAuth issuer must be an origin")
	}
	s := &Service{
		store: store, auth: authn, options: opts,
		tokenStore: tokenStore{store: store}, clients: clientStore{store: store},
		pending: make(map[string]*pendingAuthorization), rateLimits: make(map[string]requestWindow),
	}
	manager := manage.NewDefaultManager()
	manager.MapClientStorage(s.clients)
	manager.MapTokenStorage(s.tokenStore)
	manager.MapAuthorizeGenerate(authorizeGenerator{})
	manager.MapAccessGenerate(accessGenerator{})
	manager.SetAuthorizeCodeExp(authorizationCodeTTL)
	manager.SetAuthorizeCodeTokenCfg(&manage.Config{
		AccessTokenExp:    accessTokenTTL,
		RefreshTokenExp:   grantFamilyTTL,
		IsGenerateRefresh: true,
	})
	manager.SetRefreshTokenCfg(&manage.RefreshingConfig{
		AccessTokenExp:     accessTokenTTL,
		RefreshTokenExp:    grantFamilyTTL,
		IsGenerateRefresh:  true,
		IsResetRefreshTime: false,
		IsRemoveAccess:     true,
		// Retain old refresh digests so reuse can revoke the complete family.
		IsRemoveRefreshing: false,
	})
	manager.SetValidateURIHandler(func(registered, requested string) error {
		if registered == "" || requested != registered {
			return oautherrors.ErrInvalidRedirectURI
		}
		return nil
	})
	manager.SetExtractExtensionHandler(func(request *oauth2.TokenGenerateRequest, token oauth2.ExtendableTokenInfo) {
		if request == nil || request.Request == nil {
			return
		}
		values := token.GetExtension()
		if values == nil {
			values = make(url.Values)
		}
		ctx := request.Request.Context()
		copyTrusted := func(key trustedContextKey, field string) {
			if value, ok := ctx.Value(key).(string); ok && value != "" {
				values.Set(field, value)
			}
		}
		copyTrusted(trustedGrantIDKey, "hmd_grant_id")
		copyTrusted(trustedFamilyIDKey, "hmd_family_id")
		copyTrusted(trustedIssuerKey, "hmd_issuer")
		copyTrusted(trustedResourceKey, "hmd_resource")
		copyTrusted(trustedNamespaceModeKey, "hmd_namespace_mode")
		copyTrusted(trustedRefreshGenKey, "hmd_refresh_generation")
		if namespaces, ok := ctx.Value(trustedNamespacesKey).([]string); ok {
			values["hmd_namespaces"] = append([]string(nil), namespaces...)
		}
		if scope, ok := ctx.Value(trustedScopeKey).(string); ok && scope != "" {
			token.SetScope(scope)
		}
		token.SetExtension(values)
	})
	s.manager = manager
	if err := s.invalidateMismatchedState(); err != nil {
		return nil, err
	}
	return s, nil
}

// CheckRateLimit bounds public OAuth requests per remote IP. RemoteAddr is
// deliberately used rather than untrusted forwarding headers.
func (s *Service) CheckRateLimit(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		host = r.RemoteAddr
	}
	now := time.Now()
	s.rateMu.Lock()
	if len(s.rateLimits) >= maxRateEntries {
		for key, window := range s.rateLimits {
			if now.Sub(window.start) >= oauthRateWindow {
				delete(s.rateLimits, key)
			}
		}
		if len(s.rateLimits) >= maxRateEntries {
			s.rateMu.Unlock()
			w.Header().Set("Retry-After", "60")
			writeOAuthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "OAuth request rate limit exceeded")
			return false
		}
	}
	window := s.rateLimits[host]
	if now.Sub(window.start) >= oauthRateWindow {
		window = requestWindow{start: now}
	}
	window.count++
	s.rateLimits[host] = window
	limited := window.count > oauthRateLimit
	s.rateMu.Unlock()
	if limited {
		w.Header().Set("Retry-After", "60")
		writeOAuthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "OAuth request rate limit exceeded")
		return false
	}
	return true
}

func (s *Service) invalidateMismatchedState() error {
	state, err := s.store.Snapshot()
	if err != nil {
		return err
	}
	mismatched := make(map[string]bool)
	for id, grant := range state.Grants {
		if grant.Issuer != s.options.Issuer || grant.Resource != s.options.Issuer+mcpResourcePath {
			mismatched[id] = true
		}
	}
	for _, token := range state.Tokens {
		if token.GrantID != "" &&
			(token.Issuer != s.options.Issuer || token.Resource != s.options.Issuer+mcpResourcePath) {
			mismatched[token.GrantID] = true
		}
	}
	if len(mismatched) == 0 {
		return nil
	}
	return s.store.Update(func(working *oauthState) error {
		now := time.Now().UTC()
		families := make(map[string]bool)
		for id, grant := range working.Grants {
			if !mismatched[id] {
				continue
			}
			grant.RevokedAt = now
			grant.RevokedReason = "OAuth issuer or resource changed"
			working.Grants[id] = grant
		}
		for id, family := range working.Families {
			if !mismatched[family.GrantID] {
				continue
			}
			family.RevokedAt = now
			family.RevokedCause = "OAuth issuer or resource changed"
			working.Families[id] = family
			families[id] = true
		}
		for key, token := range working.Tokens {
			if !mismatched[token.GrantID] && !families[token.FamilyID] {
				continue
			}
			if strings.HasPrefix(key, "a:") {
				delete(working.Tokens, key)
			} else {
				token.RevokedAt = now
				working.Tokens[key] = token
			}
		}
		return nil
	})
}

func (s *Service) BearerVerifier() auth.OAuthBearerVerifier {
	return NewBearerVerifier(s.store, s.auth, s.options.Issuer, s.options.AllowAdminDelegation)
}

func (s *Service) MetadataHandler() http.Handler {
	document := MetadataHandler(s.options)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.CheckRateLimit(w, r) {
			return
		}
		document.ServeHTTP(w, r)
	})
}

// ProtocolHandler registers discovery, token and revocation endpoints. Browser
// authorisation remains in internal/web so consent is rendered with HMD's
// existing session, CSRF and template boundary.
func (s *Service) ProtocolHandler() http.Handler {
	mux := http.NewServeMux()
	metadata := s.MetadataHandler()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		mux.Handle(method+" "+authorizationMetadataPath, metadata)
		mux.Handle(method+" "+protectedMetadataPath, metadata)
		mux.Handle(method+" "+protectedMetadataPath+mcpResourcePath, metadata)
	}
	mux.Handle("POST "+tokenPath, s.TokenHandler())
	mux.Handle("POST "+revokePath, s.RevocationHandler())
	mux.Handle("POST "+registerPath, s.RegistrationHandler())
	return mux
}

func (s *Service) Close() error {
	if s.store == nil {
		return nil
	}
	return s.store.Close()
}

// Issuer returns the configured OAuth origin used in error redirects.
func (s *Service) Issuer() string {
	return s.options.Issuer
}
