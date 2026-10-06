package oauthserver

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

type Connection struct {
	GrantID       string
	ClientName    string
	Scopes        []string
	NamespaceMode string
	Namespaces    []string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	Revoked       bool
	Expired       bool
}

// Connections returns grants owned by user, never exposing other users' grants.
func (s *Service) Connections(user string) ([]Connection, error) {
	state, err := s.store.Snapshot()
	if err != nil {
		return nil, err
	}
	revokedFamilies := make(map[string]bool)
	for _, family := range state.Families {
		allRevoked, seen := revokedFamilies[family.GrantID]
		revokedFamilies[family.GrantID] = (!seen || allRevoked) && !family.RevokedAt.IsZero()
	}
	var result []Connection
	for _, grant := range state.Grants {
		if grant.User != user {
			continue
		}
		client := state.Clients[grant.ClientID]
		result = append(result, Connection{
			GrantID: grant.ID, ClientName: client.Name,
			Scopes:        append([]string(nil), grant.Scopes...),
			NamespaceMode: grant.NamespaceMode,
			Namespaces:    append([]string(nil), grant.Namespaces...),
			CreatedAt:     grant.CreatedAt, ExpiresAt: grant.ExpiresAt,
			Revoked: !grant.RevokedAt.IsZero() || client.Disabled || revokedFamilies[grant.ID],
			Expired: !grant.ExpiresAt.IsZero() && time.Now().After(grant.ExpiresAt),
		})
	}
	slices.SortFunc(result, func(a, b Connection) int {
		switch {
		case a.CreatedAt.After(b.CreatedAt):
			return -1
		case a.CreatedAt.Before(b.CreatedAt):
			return 1
		default:
			return 0
		}
	})
	return result, nil
}

// RevokeConnection revokes a complete grant only when user owns it.
func (s *Service) RevokeConnection(user, grantID string) error {
	return s.store.Update(func(state *oauthState) error {
		grant, ok := state.Grants[grantID]
		if !ok || grant.User != user {
			return errors.New("connection not found")
		}
		now := time.Now().UTC()
		grant.RevokedAt = now
		grant.RevokedReason = "disconnected by user"
		state.Grants[grantID] = grant
		families := make(map[string]bool)
		for id, family := range state.Families {
			if family.GrantID != grantID {
				continue
			}
			family.RevokedAt = now
			family.RevokedCause = "disconnected by user"
			state.Families[id] = family
			families[id] = true
		}
		for key, token := range state.Tokens {
			if token.GrantID != grantID && !families[token.FamilyID] {
				continue
			}
			if len(key) > 2 && key[:2] == "a:" {
				delete(state.Tokens, key)
			} else {
				token.RevokedAt = now
				state.Tokens[key] = token
			}
		}
		return nil
	})
}

// AuthorizeUpload rechecks an OAuth-derived upload capability against current
// grant, family, client and HMD permission state. The capability itself is
// never sufficient after revocation or a permission downgrade.
func (s *Service) AuthorizeUpload(_ context.Context, grantID, familyID, user, slug string, issuedScopes []string) bool {
	if grantID == "" || familyID == "" || user == "" {
		return false
	}
	state, err := s.store.Snapshot()
	if err != nil {
		return false
	}
	grant, ok := state.Grants[grantID]
	if !ok || grant.User != user || !grant.RevokedAt.IsZero() ||
		grant.Issuer != s.options.Issuer || grant.Resource != s.options.Issuer+mcpResourcePath ||
		!grant.ExpiresAt.IsZero() && time.Now().After(grant.ExpiresAt) {
		return false
	}
	family, ok := state.Families[familyID]
	if !ok || family.GrantID != grantID || !family.RevokedAt.IsZero() ||
		!family.ExpiresAt.IsZero() && time.Now().After(family.ExpiresAt) {
		return false
	}
	client, ok := state.Clients[grant.ClientID]
	if !ok || client.Disabled || !s.auth.UserExists(user) {
		return false
	}
	clientScopes := client.AllowedScopes
	if !s.options.AllowAdminDelegation {
		clientScopes = withoutSettings(clientScopes)
	}
	effective := EffectiveScopes(issuedScopes, grant.Scopes, s.auth.Prefs(user).Scopes, clientScopes)
	if !scopeSubset([]string{"write"}, effective) {
		return false
	}
	if grant.NamespaceMode == "all" || slices.Contains(grant.Scopes, "settings") {
		return true
	}
	if grant.NamespaceMode != "selected" {
		return false
	}
	namespace, _, ok := strings.Cut(slug, "/")
	return ok && slices.Contains(grant.Namespaces, namespace)
}
