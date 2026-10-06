package oauthserver

import (
	"context"
	"testing"
	"time"

	"hmd/internal/auth"
)

func TestOAuthBearerVerifierRechecksGrantAndCurrentPolicy(t *testing.T) {
	appDir := t.TempDir()
	authn, err := auth.Open(auth.Options{AppDir: appDir, AdminUser: "alice", AdminPass: "a-unique-long-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	const issuer = "https://wiki.example.test"
	const raw = "hmd_oa_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
	const clientID = "client-a"
	const grantID = "grant-a"
	const familyID = "family-a"
	now := time.Now().UTC()
	err = store.Update(func(state *oauthState) error {
		state.Clients[clientID] = ClientRecord{
			ID: clientID, Name: "Test", RedirectURIs: []string{"https://client.example.test/cb"},
			AuthMethod: clientAuthNone, AllowedScopes: []string{"read", "write"},
		}
		state.Grants[grantID] = GrantRecord{
			ID: grantID, User: "alice", ClientID: clientID, Issuer: issuer,
			Resource: issuer + mcpResourcePath, Scopes: []string{"read"},
			NamespaceMode: "selected", Namespaces: []string{"notes"},
			ExpiresAt: now.Add(time.Hour),
		}
		state.Families[familyID] = FamilyRecord{ID: familyID, GrantID: grantID, ExpiresAt: now.Add(time.Hour)}
		record := TokenRecord{
			AccessDigest: tokenDigest(raw), GrantID: grantID, FamilyID: familyID,
			ClientID: clientID, User: "alice", Issuer: issuer, Resource: issuer + mcpResourcePath,
			Scopes: []string{"read"}, CreatedAt: now, AccessExpiresAt: now.Add(time.Minute),
		}
		state.Tokens["a:"+record.AccessDigest] = record
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	verifier := NewBearerVerifier(store, authn, issuer, false)
	principal, ok := verifier.VerifyBearer(context.Background(), raw, mcpResourcePath)
	if !ok || principal.User != "alice" || principal.GrantID != grantID ||
		len(principal.Namespaces) != 1 || principal.Namespaces[0] != "notes" {
		t.Fatalf("valid token principal = %#v, ok=%v", principal, ok)
	}
	if _, ok := verifier.VerifyBearer(context.Background(), raw, "/_/api/settings"); ok {
		t.Fatal("OAuth token was accepted outside MCP")
	}

	if err := authn.SetScopes("alice", []string{"write"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := verifier.VerifyBearer(context.Background(), raw, mcpResourcePath); ok {
		t.Fatal("token remained valid after current HMD permissions removed read access")
	}
}

func TestOAuthBearerVerifierRejectsRevokedGrantAndClient(t *testing.T) {
	appDir := t.TempDir()
	authn, err := auth.Open(auth.Options{AppDir: appDir, AdminUser: "alice", AdminPass: "a-unique-long-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	const issuer = "https://wiki.example.test"
	const raw = "hmd_oa_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
	now := time.Now().UTC()
	record := TokenRecord{
		AccessDigest: tokenDigest(raw), GrantID: "g", FamilyID: "f",
		ClientID: "c", User: "alice", Issuer: issuer, Resource: issuer + mcpResourcePath,
		Scopes: []string{"read"}, CreatedAt: now, AccessExpiresAt: now.Add(time.Hour),
	}
	if err := store.Update(func(state *oauthState) error {
		state.Clients["c"] = ClientRecord{ID: "c", Name: "Test", RedirectURIs: []string{"https://client.test/cb"}, AuthMethod: clientAuthNone, AllowedScopes: []string{"read"}}
		state.Grants["g"] = GrantRecord{ID: "g", User: "alice", ClientID: "c", Issuer: issuer, Resource: issuer + mcpResourcePath, Scopes: []string{"read"}, NamespaceMode: "all", ExpiresAt: now.Add(time.Hour)}
		state.Families["f"] = FamilyRecord{ID: "f", GrantID: "g", ExpiresAt: now.Add(time.Hour)}
		state.Tokens["a:"+record.AccessDigest] = record
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	verifier := NewBearerVerifier(store, authn, issuer, false)
	if _, ok := verifier.VerifyBearer(context.Background(), raw, mcpResourcePath); !ok {
		t.Fatal("valid fixture token was rejected")
	}
	if err := store.Update(func(state *oauthState) error {
		client := state.Clients["c"]
		client.Disabled = true
		state.Clients["c"] = client
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := verifier.VerifyBearer(context.Background(), raw, mcpResourcePath); ok {
		t.Fatal("token remained valid after client was disabled")
	}
}
