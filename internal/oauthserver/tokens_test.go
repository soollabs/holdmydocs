package oauthserver

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-oauth2/oauth2/v4"
	"github.com/go-oauth2/oauth2/v4/models"
)

func TestOAuthTokenStoreIndexesDigestsAndRehydratesPresentedToken(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	tokens := tokenStore{store: store}
	const code = "hmd_oc_synthetic-code-marker"
	const access = "hmd_oa_synthetic-access-marker"
	const refresh = "hmd_or_synthetic-refresh-marker"

	token := models.NewToken()
	token.SetClientID("client")
	token.SetUserID("alice")
	token.SetRedirectURI("https://client.example.test/callback")
	token.SetScope("read")
	token.SetCode(code)
	token.SetCodeCreateAt(time.Now().UTC())
	token.SetCodeExpiresIn(2 * time.Minute)
	token.SetCodeChallenge("valid-s256-challenge")
	token.SetCodeChallengeMethod(oauth2.CodeChallengeS256)
	token.SetAccess(access)
	token.SetAccessCreateAt(time.Now().UTC())
	token.SetAccessExpiresIn(15 * time.Minute)
	token.SetRefresh(refresh)
	token.SetRefreshCreateAt(time.Now().UTC())
	token.SetRefreshExpiresIn(30 * 24 * time.Hour)
	token.SetExtension(url.Values{
		"hmd_grant_id": {"grant-1"}, "hmd_family_id": {"family-1"},
		"hmd_issuer":         {"https://wiki.example.test"},
		"hmd_resource":       {"https://wiki.example.test/_/mcp"},
		"hmd_namespace_mode": {"selected"}, "hmd_namespaces": {"notes"},
	})
	expires := time.Now().Add(30 * 24 * time.Hour)
	if err := store.Transaction(t.Context(), func(ctx context.Context) error {
		state := transactionState(ctx)
		state.Clients["client"] = ClientRecord{
			ID: "client", Name: "Test client",
			RedirectURIs: []string{"https://client.example.test/callback"},
			AuthMethod:   clientAuthNone, AllowedScopes: []string{"read"},
		}
		state.Grants["grant-1"] = GrantRecord{
			ID: "grant-1", User: "alice", ClientID: "client",
			Issuer:   "https://wiki.example.test",
			Resource: "https://wiki.example.test/_/mcp",
			Scopes:   []string{"read"}, NamespaceMode: "selected",
			Namespaces: []string{"notes"}, CreatedAt: time.Now().UTC(),
			ExpiresAt: expires,
		}
		state.Families["family-1"] = FamilyRecord{
			ID: "family-1", GrantID: "grant-1", ExpiresAt: expires,
		}
		return tokens.Create(ctx, token)
	}); err != nil {
		t.Fatal(err)
	}

	file, err := os.ReadFile(filepath.Join(dir, "oauth.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{code, access, refresh} {
		if strings.Contains(string(file), marker) {
			t.Fatalf("OAuth state persisted plaintext credential marker %q", marker)
		}
	}

	got, err := tokens.GetByCode(t.Context(), code)
	if err != nil || got == nil {
		t.Fatalf("GetByCode = %v, %v", got, err)
	}
	extendable, ok := got.(oauth2.ExtendableTokenInfo)
	if !ok || got.GetCode() != code || got.GetClientID() != "client" ||
		extendable.GetExtension().Get("hmd_grant_id") != "grant-1" {
		t.Fatalf("rehydrated authorization code has wrong binding: %#v", got)
	}
	accessInfo, err := tokens.GetByAccess(t.Context(), access)
	if err != nil || accessInfo == nil || accessInfo.GetAccess() != access {
		t.Fatalf("GetByAccess = %v, %v", accessInfo, err)
	}
	refreshInfo, err := tokens.GetByRefresh(t.Context(), refresh)
	if err != nil || refreshInfo == nil || refreshInfo.GetRefresh() != refresh {
		t.Fatalf("GetByRefresh = %v, %v", refreshInfo, err)
	}

	if err := store.Transaction(t.Context(), func(ctx context.Context) error {
		return tokens.RemoveByCode(ctx, code)
	}); err != nil {
		t.Fatal(err)
	}
	if consumed, err := tokens.GetByCode(t.Context(), code); err != nil || consumed != nil {
		t.Fatalf("consumed code remained redeemable: %v, %v", consumed, err)
	}
}

func TestOAuthTokenStoreMutationsRequireTransaction(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	err = (tokenStore{store: store}).Create(t.Context(), models.NewToken())
	if err != errOAuthTransactionRequired {
		t.Fatalf("Create outside transaction = %v", err)
	}
}
