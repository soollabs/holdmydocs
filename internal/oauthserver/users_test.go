package oauthserver

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestRevokeUserGrantsSurvivesAccountRecreation(t *testing.T) {
	t.Run("enabled", func(t *testing.T) { checkRevokeUserGrants(t, false) })
	t.Run("disabled", func(t *testing.T) { checkRevokeUserGrants(t, true) })
}

func TestStoredUserRevokerWithoutState(t *testing.T) {
	dir := t.TempDir()
	if err := NewStoredUserRevoker(dir).RevokeUserGrants("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "oauth.json")); !os.IsNotExist(err) {
		t.Fatalf("revocation created OAuth state: %v", err)
	}
}

func checkRevokeUserGrants(t *testing.T, disabled bool) {
	t.Helper()
	service, store, authn, session, clientID := newOAuthServiceFixture(t)
	code, verifier, _ := authoriseFixture(t, service, session, clientID)
	response := tokenPost(t, service.TokenHandler(), url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {"https://client.example.test/callback"}, "code_verifier": {verifier},
		"resource": {service.options.Issuer + mcpResourcePath},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("exchange: %s", response.Body.String())
	}
	var tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	if err := authn.DeleteUser("alice"); err != nil {
		t.Fatal(err)
	}
	service.pendingMu.Lock()
	service.pending["deleted-user-flow"] = &pendingAuthorization{user: "alice"}
	service.pendingMu.Unlock()
	if disabled {
		appDir := filepath.Dir(store.path)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if err := NewStoredUserRevoker(appDir).RevokeUserGrants("alice"); err != nil {
			t.Fatal(err)
		}
		reopened, err := OpenStore(appDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		service, err = NewService(service.options, reopened, authn)
		if err != nil {
			t.Fatal(err)
		}
		store = reopened
	} else {
		if err := service.RevokeUserGrants("alice"); err != nil {
			t.Fatal(err)
		}
	}
	service.pendingMu.Lock()
	_, pending := service.pending["deleted-user-flow"]
	service.pendingMu.Unlock()
	if pending {
		t.Fatal("deleted user's pending authorisation survived")
	}
	if err := authn.AddUser("alice", "a-unique-long-test-password"); err != nil {
		t.Fatal(err)
	}
	if _, ok := service.BearerVerifier().VerifyBearer(t.Context(), tokens.AccessToken, mcpResourcePath); ok {
		t.Fatal("re-created account inherited access token")
	}
	response = tokenPost(t, service.TokenHandler(), url.Values{
		"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {tokens.RefreshToken},
		"resource": {service.options.Issuer + mcpResourcePath},
	})
	if response.Code == http.StatusOK {
		t.Fatal("re-created account inherited refresh token")
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range state.Grants {
		if grant.User == "alice" && grant.RevokedAt.IsZero() {
			t.Fatal("grant remains active")
		}
	}
}
