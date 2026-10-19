package api

import (
	"context"

	"hmd/internal/auth"
	"hmd/internal/wiki"
)

// Scope identifies a permission scope required by an operation.
type Scope = auth.Scope

// Scope constants are re-exported so callers that depend only on api can name
// the built-in scopes without importing auth.
const (
	ScopeRead        = auth.ScopeRead
	ScopeWrite       = auth.ScopeWrite
	ScopeAdmin       = auth.ScopeAdmin
	ScopePreferences = auth.ScopePreferences
)

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
	if a.readOnly() && (scope == ScopeWrite || scope == ScopeAdmin) {
		return false
	}
	return a.grantedScope(ctx, scope)
}

// CanViewSettings reports whether the caller holds the admin scope,
// ignoring read-only mode. A read-only instance still shows administrators
// its settings pages, but nothing on them can be saved.
func (a *API) CanViewSettings(ctx context.Context) bool {
	return a.grantedScope(ctx, ScopeAdmin)
}

// CanViewEditor reports whether the caller holds the write scope, ignoring
// read-only mode. A read-only instance, such as a demo, still lets writers try
// the editor, but nothing they type is saved.
func (a *API) CanViewEditor(ctx context.Context) bool {
	return a.grantedScope(ctx, ScopeWrite)
}

// ReadOnly reports whether the instance refuses writes and settings changes.
func (a *API) ReadOnly() bool {
	return a.readOnly()
}

// grantedScope reports whether the principal was granted the scope, before
// read-only mode is applied.
func (a *API) grantedScope(ctx context.Context, scope Scope) bool {
	if principal, ok := auth.TokenPrincipalFromContext(ctx); ok {
		return principal.User != "" && principal.HasScope(scope)
	}
	if name := auth.UserFromContext(ctx); name != "" && a.auth != nil {
		return a.auth.UserExists(name) && a.auth.Prefs(name).HasScope(scope)
	}
	return false
}

// canReadPage includes the deliberate public-page exception, but never widens
// an authenticated principal's scope or namespace policy.
func (a *API) canReadPage(ctx context.Context, slug string) bool {
	if !wiki.ValidPageSlug(slug) || !AllowSlug(ctx, slug) {
		return false
	}
	if _, ok := Username(ctx); ok {
		return a.HasScope(ctx, ScopeRead)
	}
	return a.Namespaces().IsPublic(slug)
}

func (a *API) requirePageRead(ctx context.Context, slug string) error {
	if !wiki.ValidPageSlug(slug) {
		return InvalidInput("invalid page identifier", nil)
	}
	if !AllowSlug(ctx, slug) {
		return Forbidden("namespace access denied")
	}
	if a.canReadPage(ctx, slug) {
		return nil
	}
	if _, authenticated := Username(ctx); !authenticated {
		return NotFound("page not found")
	}
	return a.RequireScope(ctx, ScopeRead)
}

// Repository-wide operations cannot disclose or mutate namespaces outside a
// restricted token's policy. Settings principals are unrestricted by design.
func (a *API) requireGlobalScope(ctx context.Context, scope Scope) error {
	if err := a.RequireScope(ctx, scope); err != nil {
		return err
	}
	if principal, ok := auth.TokenPrincipalFromContext(ctx); ok && principal.Restricted() {
		return Forbidden("namespace-restricted tokens cannot access repository-wide operations")
	}
	return nil
}

// RequireScope returns an unauthenticated or forbidden error unless the caller
// holds the required scope.
func (a *API) RequireScope(ctx context.Context, scope Scope) error {
	if a.readOnly() && (scope == ScopeWrite || scope == ScopeAdmin) {
		return Forbidden("instance is read-only")
	}
	if a.HasScope(ctx, scope) {
		return nil
	}
	if _, ok := Username(ctx); !ok {
		return Unauthenticated("authentication required")
	}
	return Forbidden("insufficient scope")
}

// RequireOwnSettings gates changes to the caller's own account: appearance,
// git author and personal access tokens. Any signed-in browser session may
// make them, whatever its scopes; the admin scope only adds the server panels.
// A token also needs the preferences scope, so by default a token cannot
// change the account or mint more tokens. Read-only mode refuses them all.
func (a *API) RequireOwnSettings(ctx context.Context) error {
	if a.readOnly() {
		return Forbidden("instance is read-only")
	}
	if !a.signedIn(ctx) {
		return Unauthenticated("authentication required")
	}
	if principal, ok := auth.TokenPrincipalFromContext(ctx); ok && !principal.HasScope(ScopePreferences) {
		return Forbidden("insufficient scope")
	}
	return nil
}

// CanChangeOwnSettings reports whether RequireOwnSettings would let the
// caller save their own account settings.
func (a *API) CanChangeOwnSettings(ctx context.Context) bool {
	return a.RequireOwnSettings(ctx) == nil
}

func (a *API) signedIn(ctx context.Context) bool {
	if principal, ok := auth.TokenPrincipalFromContext(ctx); ok {
		return principal.User != ""
	}
	name := auth.UserFromContext(ctx)
	return name != "" && a.auth != nil && a.auth.UserExists(name)
}

func (a *API) readOnly() bool {
	return a.Config().ReadOnly || (a.store != nil && a.store.ReadOnly())
}
