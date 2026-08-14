package web

import (
	"context"
	"hmd/internal/auth"
)

type Auth = auth.Auth
type userRecord = auth.UserRecord
type tokenPrincipal = auth.TokenPrincipal
type scope = auth.Scope
type oidcIdentity = auth.OIDCIdentity
type UserSummary = auth.UserSummary

const (
	scopeRead     = auth.ScopeRead
	scopeWrite    = auth.ScopeWrite
	scopeSettings = auth.ScopeSettings
)

var (
	validUsername        = auth.ValidUsername
	effectiveTokenScopes = auth.EffectiveTokenScopes
)

func OpenAuth(cfg Config) (*Auth, error) {
	return auth.Open(auth.Options{AppDir: cfg.AppDir, AdminUser: cfg.AdminUser, AdminPass: cfg.AdminPass})
}

func userFromContext(ctx context.Context) string { return auth.UserFromContext(ctx) }

func tokenPrincipalFromContext(ctx context.Context) (tokenPrincipal, bool) {
	return auth.TokenPrincipalFromContext(ctx)
}

func tokenAllowsNamespace(ctx context.Context, namespace string) bool {
	return auth.TokenAllowsNamespace(ctx, namespace)
}

func tokenAllowsSlug(ctx context.Context, slug string) bool { return auth.TokenAllowsSlug(ctx, slug) }
