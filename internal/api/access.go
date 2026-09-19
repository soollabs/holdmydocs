package api

import (
	"context"

	"hmd/internal/auth"
)

// Scope identifies a permission scope required by an operation.
type Scope = auth.Scope

// Username returns the authenticated principal's username from ctx. It never
// trusts a caller-supplied value: direct Go calls cannot assert identity by
// passing a username. An empty result means the caller is unauthenticated.
func Username(ctx context.Context) (string, bool) {
	if principal, ok := auth.TokenPrincipalFromContext(ctx); ok && principal.User != "" {
		return principal.User, true
	}
	if name := auth.UserFromContext(ctx); name != "" {
		return name, true
	}
	return "", false
}

// AllowNamespace reports whether the caller may act within a namespace.
func AllowNamespace(ctx context.Context, namespace string) bool {
	return auth.TokenAllowsNamespace(ctx, namespace)
}

// AllowSlug reports whether the caller may act on a slug.
func AllowSlug(ctx context.Context, slug string) bool {
	return auth.TokenAllowsSlug(ctx, slug)
}

// HasScope reports whether the caller holds the required scope. Bearer-token
// scopes come from the token principal; session scopes come from the user
// record. Direct calls are checked here, not left to HTTP middleware.
func (a *API) HasScope(ctx context.Context, scope Scope) bool {
	if principal, ok := auth.TokenPrincipalFromContext(ctx); ok {
		return principal.HasScope(scope)
	}
	if name := auth.UserFromContext(ctx); name != "" && a.auth != nil {
		return a.auth.Prefs(name).HasScope(scope)
	}
	return false
}

// RequireScope returns an unauthenticated or forbidden error unless the caller
// holds the required scope.
func (a *API) RequireScope(ctx context.Context, scope Scope) error {
	if a.HasScope(ctx, scope) {
		return nil
	}
	if _, ok := Username(ctx); !ok {
		return Unauthenticated("authentication required")
	}
	return Forbidden("insufficient scope")
}
