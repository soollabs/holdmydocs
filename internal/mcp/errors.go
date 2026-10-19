package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"hmd/internal/api"
	"hmd/internal/auth"
	"hmd/internal/wiki"
)

// user returns the authenticated username, or the empty string.
func (s *Server) user(ctx context.Context) string {
	name, _ := api.Username(ctx)
	return name
}

// requireScope enforces the scope a tool needs, delegating to the shared
// application access primitive so bearer and session scopes are treated alike.
func (s *Server) requireScope(ctx context.Context, scope api.Scope) error {
	err := s.api.RequireScope(ctx, scope)
	if api.CategoryOf(err) != api.CategoryForbidden {
		return err
	}
	if principal, ok := auth.TokenPrincipalFromContext(ctx); ok && principal.GrantID != "" {
		return fmt.Errorf("insufficient_scope: this action requires the %q scope; reconnect and approve it if your current HMD permissions allow it", scope)
	}
	return fmt.Errorf("insufficient scope: the %q permission is required", scope)
}

// requireNamespace rejects a namespace outside the caller's namespace access.
func (s *Server) requireNamespace(ctx context.Context, namespace string) error {
	if api.AllowNamespace(ctx, namespace) {
		return nil
	}
	if principal, ok := auth.TokenPrincipalFromContext(ctx); ok && principal.GrantID != "" {
		return fmt.Errorf("namespace access denied: reconnect and approve access to namespace %q", namespace)
	}
	return fmt.Errorf("forbidden: namespace %q is not allowed", namespace)
}

// requireSlug rejects a page outside the caller's namespace access.
func (s *Server) requireSlug(ctx context.Context, slug string) error {
	if api.AllowSlug(ctx, slug) {
		return nil
	}
	if principal, ok := auth.TokenPrincipalFromContext(ctx); ok && principal.GrantID != "" {
		namespace, _, _ := strings.Cut(slug, "/")
		return fmt.Errorf("namespace access denied: reconnect and approve access to namespace %q", namespace)
	}
	return fmt.Errorf("forbidden: namespace access denied")
}

// mcpPageSlug validates a page identifier, naming what is wrong with an
// invalid one and showing the expected form.
func mcpPageSlug(slug string) (string, error) {
	if err := wiki.CheckPageSlug(slug); err != nil {
		return "", fmt.Errorf("invalid page identifier %q: %w; expected namespace/page, for example notes/my-page", slug, err)
	}
	return slug, nil
}

// conflictError is the error returned when a basehash is stale. Its message is
// a JSON object carrying the current hash, empty when the target no longer
// exists, so the caller can re-read and retry. A page save also carries the
// current body so the caller can merge without another read.
type conflictError struct {
	message, hash string
	body          *string
}

func (e conflictError) Error() string {
	payload, _ := json.Marshal(struct {
		Error string  `json:"error"`
		Hash  string  `json:"hash"`
		Body  *string `json:"body,omitempty"`
	}{"conflict: " + e.message + "; re-read and retry with the current hash", e.hash, e.body})
	return string(payload)
}

func mcpConflict(message, hash string) error { return conflictError{message: message, hash: hash} }

// mcpPageConflict is mcpConflict for a page save, carrying the current body.
func mcpPageConflict(message string, current *api.PageView) error {
	if current == nil {
		return conflictError{message: message}
	}
	return conflictError{message: message, hash: current.Hash, body: &current.Body}
}
