package oauthserver

import (
	"context"
	"time"

	"hmd/internal/auth"
)

type bearerVerifier struct {
	store      *Store
	auth       *auth.Auth
	issuer     string
	resource   string
	allowAdmin bool
}

// NewBearerVerifier builds the optional live OAuth resolver used by HMD auth.
func NewBearerVerifier(store *Store, authn *auth.Auth, issuer string, allowAdmin bool) auth.OAuthBearerVerifier {
 return &bearerVerifier{
		store: store, auth: authn, issuer: issuer,
		resource: issuer + mcpResourcePath, allowAdmin: allowAdmin,
	}
}

func (v *bearerVerifier) Challenge() string {
	return `Bearer resource_metadata="` + ResourceMetadataURL(v.issuer) + `", scope="read write"`
}

func (v *bearerVerifier) VerifyBearer(ctx context.Context, raw, path string) (auth.TokenPrincipal, bool) {
	if path != mcpResourcePath || len(raw) < len("hmd_oa_")+32 ||
		raw[:len("hmd_oa_")] != "hmd_oa_" {
		return auth.TokenPrincipal{}, false
	}
	state, err := v.store.Snapshot()
	if err != nil {
		return auth.TokenPrincipal{}, false
	}
	record, ok := state.Tokens["a:"+tokenDigest(raw)]
	if !ok || record.AccessDigest != tokenDigest(raw) || !record.RevokedAt.IsZero() ||
		record.Issuer != v.issuer || record.Resource != v.resource ||
		!record.AccessExpiresAt.IsZero() && time.Now().After(record.AccessExpiresAt) {
		return auth.TokenPrincipal{}, false
	}
	grant, ok := state.Grants[record.GrantID]
	if !ok || grant.ID != record.GrantID || grant.User != record.User ||
		grant.ClientID != record.ClientID || grant.Issuer != v.issuer ||
		grant.Resource != v.resource || !grant.RevokedAt.IsZero() ||
		!grant.ExpiresAt.IsZero() && time.Now().After(grant.ExpiresAt) {
		return auth.TokenPrincipal{}, false
	}
	client, ok := state.Clients[record.ClientID]
	if !ok || client.Disabled {
		return auth.TokenPrincipal{}, false
	}
	if record.FamilyID != "" {
		family, ok := state.Families[record.FamilyID]
		if !ok || family.GrantID != grant.ID || !family.RevokedAt.IsZero() ||
			!family.ExpiresAt.IsZero() && time.Now().After(family.ExpiresAt) ||
			record.RefreshGeneration != family.Generation {
			return auth.TokenPrincipal{}, false
		}
	}
	if !v.auth.UserExists(record.User) {
		return auth.TokenPrincipal{}, false
	}
	userScopes := v.auth.Prefs(record.User).Scopes
	clientScopes := client.AllowedScopes
	if !v.allowAdmin {
		clientScopes = withoutSettings(clientScopes)
	}
	effective := EffectiveScopes(record.Scopes, grant.Scopes, userScopes, clientScopes)
	if len(effective) == 0 {
		return auth.TokenPrincipal{}, false
	}
	var namespaces []string
	switch grant.NamespaceMode {
	case "all":
		// nil is the established representation for unrestricted namespace use.
	case "selected":
		if grant.Namespaces == nil {
			return auth.TokenPrincipal{}, false
		}
		namespaces = append([]string{}, grant.Namespaces...)
	default:
		return auth.TokenPrincipal{}, false
	}
	return auth.TokenPrincipal{
		User: record.User, Namespaces: namespaces, Scopes: effective,
		GrantID: grant.ID, FamilyID: record.FamilyID,
	}, true
}

func withoutSettings(scopes []string) []string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if scope != "settings" {
			out = append(out, scope)
		}
	}
	return out
}
