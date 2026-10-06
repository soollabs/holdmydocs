package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
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

// mcpPageIdentifier resolves the slug/path pair accepted by page tools. Exactly
// one of the two aliases must be supplied and the result must be a valid page
// slug.
func mcpPageIdentifier(slug, path string) (string, error) {
	if slug != "" && path != "" {
		return "", errors.New("provide slug or path, not both")
	}
	identifier := slug
	if identifier == "" {
		identifier = path
	}
	if identifier == "" {
		return "", errors.New("slug or path is required")
	}
	if !wiki.ValidPageSlug(identifier) {
		return "", fmt.Errorf("invalid page identifier %q: expected namespace/page", identifier)
	}
	return identifier, nil
}

// mcpEditConflict renders the conflict payload returned when an edit's base
// hash is stale, carrying the current hash so the caller can re-read.
func mcpEditConflict(hash string) *sdk.CallToolResult {
	payload, _ := json.Marshal(map[string]string{"error": "conflict: page changed since basehash; re-read before retrying", "hash": hash})
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: string(payload)}}}
}

// mcpToolError renders a structured error result with a JSON payload.
func mcpToolError(payload map[string]string) *sdk.CallToolResult {
	encoded, _ := json.Marshal(payload)
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: string(encoded)}}}
}
