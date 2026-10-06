package oauthserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	oauth2 "github.com/go-oauth2/oauth2/v4"
	"hmd/internal/auth"
)

func TestCodeConsumptionRollsBackLibraryPKCEFailure(t *testing.T) {
	service, store, authn, session, clientID := newOAuthServiceFixture(t)
	code, verifier, _ := authoriseFixture(t, service, authn, session, clientID)
	redirect := "https://client.example.test/callback"
	ctx := context.WithValue(t.Context(), redirectURIContextKey{}, redirect)
	request := requestWithTrustedResource(
		httptest.NewRequest(http.MethodPost, tokenPath, nil),
		service.options.Issuer, service.options.Issuer+mcpResourcePath, redirect,
	)
	err := store.Transaction(ctx, func(ctx context.Context) error {
		_, err := service.manager.GenerateAccessToken(ctx, oauth2.AuthorizationCode, &oauth2.TokenGenerateRequest{
			ClientID: clientID, RedirectURI: redirect, Code: code,
			CodeVerifier: strings.Repeat("x", 43), Request: request,
		})
		return err
	})
	if err == nil {
		t.Fatal("library accepted the wrong PKCE verifier")
	}
	state, err := store.Snapshot()
	if err != nil || !state.Tokens["c:"+tokenDigest(code)].ConsumedAt.IsZero() || len(state.Tokens) != 1 {
		t.Fatalf("library failure partially committed code consumption: %v", err)
	}
	response := tokenPost(t, service.TokenHandler(), url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {redirect}, "code_verifier": {verifier},
		"resource": {service.options.Issuer + mcpResourcePath},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("rolled-back code could not be redeemed: %d %s", response.Code, response.Body.String())
	}
}

func TestTokenTransactionUsesCurrentPolicy(t *testing.T) {
	for _, change := range []string{"disconnect", "family replay", "client disable", "user downgrade", "client downgrade", "expiry"} {
		t.Run(change, func(t *testing.T) {
			service, store, authn, session, clientID := newOAuthServiceFixture(t)
			code, _, _ := authoriseFixture(t, service, authn, session, clientID)
			// This represents the endpoint's pre-transaction snapshot. The
			// transaction must not trust its policy after another operation.
			stale, err := store.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			token := stale.Tokens["c:"+tokenDigest(code)]
			if change == "user downgrade" {
				if err := authn.SetScopes("alice", []string{"read"}); err != nil {
					t.Fatal(err)
				}
			}
			err = store.Transaction(t.Context(), func(ctx context.Context) error {
				state := transactionState(ctx)
				switch change {
				case "disconnect":
					grant := state.Grants[token.GrantID]
					grant.RevokedAt = time.Now()
					state.Grants[grant.ID] = grant
				case "family replay":
					family := state.Families[token.FamilyID]
					family.RevokedAt = time.Now()
					state.Families[family.ID] = family
				case "client disable":
					client := state.Clients[clientID]
					client.Disabled = true
					state.Clients[clientID] = client
				case "client downgrade":
					client := state.Clients[clientID]
					client.AllowedScopes = []string{"read"}
					state.Clients[clientID] = client
				case "expiry":
					family := state.Families[token.FamilyID]
					family.ExpiresAt = time.Now().Add(-time.Second)
					state.Families[family.ID] = family
				}
				effective, err := service.currentTokenScopes(state, token, clientID)
				if change == "user downgrade" || change == "client downgrade" {
					if err != nil || strings.Join(effective, " ") != "read" {
						t.Fatalf("transaction retained stale write permission: scopes=%v err=%v", effective, err)
					}
					if scopesSemanticallyEqual(effective, token.Scopes) {
						t.Fatal("code exchange would accept stale consent permissions")
					}
				} else if err == nil {
					t.Fatal("transaction accepted a revoked or expired binding")
				}
				// Deliberately roll back the synthetic competing policy change.
				return context.Canceled
			})
			if err != context.Canceled {
				t.Fatalf("transaction = %v", err)
			}
		})
	}
}

func TestTokenPersistenceFailureDoesNotReturnCredentials(t *testing.T) {
	for _, stage := range []string{"rename", "directory sync"} {
		t.Run(stage, func(t *testing.T) {
			service, store, authn, session, clientID := newOAuthServiceFixture(t)
			code, verifier, _ := authoriseFixture(t, service, authn, session, clientID)
			if stage == "rename" {
				store.fs.rename = func(string, string) error { return context.Canceled }
			} else {
				store.fs.sync = func(file *os.File) error {
					if file.Name() == filepath.Dir(store.path) {
						return context.Canceled
					}
					return file.Sync()
				}
			}
			response := tokenPost(t, service.TokenHandler(), url.Values{
				"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
				"redirect_uri":  {"https://client.example.test/callback"},
				"code_verifier": {verifier}, "resource": {"https://wiki.example.test/_/mcp"},
			})
			if response.Code == http.StatusOK || strings.Contains(response.Body.String(), "access_token") {
				t.Fatalf("failed commit exposed credentials: %d %s", response.Code, response.Body.String())
			}
			if stage == "rename" {
				state, err := store.Snapshot()
				if err != nil || !state.Tokens["c:"+tokenDigest(code)].ConsumedAt.IsZero() {
					t.Fatalf("failed rename consumed the code: %v", err)
				}
			} else if _, err := store.Snapshot(); !errors.Is(err, errStoreUncertain) {
				t.Fatalf("post-rename failure did not disable the store: %v", err)
			}
		})
	}
}

func newOAuthServiceFixture(t *testing.T) (*Service, *Store, *auth.Auth, string, string) {
	t.Helper()
	authn, err := auth.Open(auth.Options{
		AppDir: t.TempDir(), AdminUser: "alice", AdminPass: "a-unique-long-test-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := authn.SetScopes("alice", []string{"read", "write"}); err != nil {
		t.Fatal(err)
	}
	session, ok := authn.Login("alice", "a-unique-long-test-password")
	if !ok {
		t.Fatal("could not create test session")
	}
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	client, err := store.ProvisionClient("Test MCP client",
		[]string{"https://client.example.test/callback"},
		clientAuthNone, []string{"read", "write"}, false)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServerOptions{Issuer: "https://wiki.example.test"}, store, authn)
	if err != nil {
		t.Fatal(err)
	}
	return service, store, authn, session, client.ClientID
}

func authoriseFixture(t *testing.T, service *Service, authn *auth.Auth, session, clientID string) (string, string, string) {
	t.Helper()
	verifier := strings.Repeat("v", 43)
	challenge, err := s256Challenge(verifier)
	if err != nil {
		t.Fatal(err)
	}
	params := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {"https://client.example.test/callback"},
		"response_type":         {"code"},
		"state":                 {"opaque-state"},
		"scope":                 {"read write"},
		"resource":              {"https://wiki.example.test/_/mcp"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	request := httptest.NewRequest(http.MethodGet, "/_/oauth/authorize?"+params.Encode(), nil)
	started, err := service.StartAuthorization(request, session, []string{"notes"})
	if err != nil {
		t.Fatal(err)
	}
	if started.Prompt == nil || started.LoginRequired {
		t.Fatalf("authorization did not produce consent: %#v", started)
	}
	redirect, err := service.CompleteAuthorization(
		started.Handle, started.FlowCookie, "alice", session, true,
		"read write", "selected", []string{"notes"}, []string{"notes"},
	)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	if callback.Query().Get("state") != "opaque-state" ||
		callback.Query().Get("iss") != "https://wiki.example.test" {
		t.Fatalf("authorization redirect lost state/issuer: %s", redirect)
	}
	return callback.Query().Get("code"), verifier, started.Handle
}

func s256Challenge(verifier string) (string, error) {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

func tokenPost(t *testing.T, handler http.Handler, fields url.Values) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, tokenPath, strings.NewReader(fields.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestOAuthCodeExchangeAndMCPBearerResolution(t *testing.T) {
	service, _, authn, session, clientID := newOAuthServiceFixture(t)
	code, verifier, _ := authoriseFixture(t, service, authn, session, clientID)
	handler := service.TokenHandler()
	fields := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {"https://client.example.test/callback"},
		"code_verifier": {verifier},
		"resource":      {"https://wiki.example.test/_/mcp"},
	}
	response := tokenPost(t, handler, fields)
	if response.Code != http.StatusOK {
		t.Fatalf("token response = %d: %s", response.Code, response.Body.String())
	}
	var issued struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issued.AccessToken, "hmd_oa_") ||
		!strings.HasPrefix(issued.RefreshToken, "hmd_or_") ||
		issued.TokenType != "Bearer" || issued.Scope != "read write" {
		t.Fatalf("unexpected token response: %#v", issued)
	}
	principal, ok := service.BearerVerifier().VerifyBearer(t.Context(), issued.AccessToken, mcpResourcePath)
	if !ok || principal.User != "alice" || principal.GrantID == "" ||
		len(principal.Namespaces) != 1 || principal.Namespaces[0] != "notes" {
		t.Fatalf("MCP bearer principal = %#v, ok=%v", principal, ok)
	}
	if _, ok := service.BearerVerifier().VerifyBearer(t.Context(), issued.AccessToken, "/_/api/settings"); ok {
		t.Fatal("OAuth access token was accepted outside the MCP resource")
	}
	if !service.AuthorizeUpload(t.Context(), principal.GrantID, principal.FamilyID, principal.User, "notes/page", principal.Scopes) {
		t.Fatal("live write grant could not authorise its one-use upload capability")
	}
	if service.AuthorizeUpload(t.Context(), principal.GrantID, principal.FamilyID, principal.User, "private/page", principal.Scopes) {
		t.Fatal("selected namespace grant authorised an upload elsewhere")
	}
	if err := authn.SetScopes("alice", []string{"read"}); err != nil {
		t.Fatal(err)
	}
	if service.AuthorizeUpload(t.Context(), principal.GrantID, principal.FamilyID, principal.User, "notes/page", principal.Scopes) {
		t.Fatal("permission downgrade did not invalidate the detached upload capability")
	}
}

func TestOAuthConsentHandleCanCompleteOnlyOnceConcurrently(t *testing.T) {
	service, _, _, session, clientID := newOAuthServiceFixture(t)
	challenge, _ := s256Challenge(strings.Repeat("v", 43))
	params := url.Values{
		"client_id": {clientID}, "redirect_uri": {"https://client.example.test/callback"},
		"response_type": {"code"}, "state": {"one-use-state"},
		"scope": {"read"}, "resource": {"https://wiki.example.test/_/mcp"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	started, err := service.StartAuthorization(
		httptest.NewRequest(http.MethodGet, "/_/oauth/authorize?"+params.Encode(), nil),
		session, []string{"notes"},
	)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := service.CompleteAuthorization(
				started.Handle, started.FlowCookie, "alice", session,
				true, "read", "selected", []string{"notes"}, []string{"notes"},
			)
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent consent completion successes = %d, want exactly one", successes)
	}
	state, err := service.store.Snapshot()
	if err != nil || len(state.Grants) != 1 {
		t.Fatalf("concurrent consent created %d grants: %v", len(state.Grants), err)
	}
}

func TestOAuthRefreshRotationReplayRevokesFamily(t *testing.T) {
	service, _, authn, session, clientID := newOAuthServiceFixture(t)
	code, verifier, _ := authoriseFixture(t, service, authn, session, clientID)
	handler := service.TokenHandler()
	codeFields := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID},
		"code": {code}, "redirect_uri": {"https://client.example.test/callback"},
		"code_verifier": {verifier}, "resource": {"https://wiki.example.test/_/mcp"},
	}
	firstResponse := tokenPost(t, handler, codeFields)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("code exchange = %d: %s", firstResponse.Code, firstResponse.Body.String())
	}
	var first struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	refreshFields := url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {first.RefreshToken}}
	secondResponse := tokenPost(t, handler, refreshFields)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("refresh = %d: %s", secondResponse.Code, secondResponse.Body.String())
	}
	var second struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh did not rotate both credentials")
	}
	state, err := service.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	refreshRecord := state.Tokens["r:"+tokenDigest(second.RefreshToken)]
	if family := state.Families[refreshRecord.FamilyID]; !refreshRecord.RefreshExpiresAt.Equal(family.ExpiresAt) {
		t.Fatalf("rotated refresh expiry %s does not preserve absolute family expiry %s", refreshRecord.RefreshExpiresAt, family.ExpiresAt)
	}
	replayResponse := tokenPost(t, handler, refreshFields)
	if replayResponse.Code != http.StatusBadRequest ||
		!strings.Contains(replayResponse.Body.String(), "invalid_grant") {
		t.Fatalf("refresh replay = %d: %s", replayResponse.Code, replayResponse.Body.String())
	}
	if _, ok := service.BearerVerifier().VerifyBearer(t.Context(), second.AccessToken, mcpResourcePath); ok {
		t.Fatal("refresh-token replay did not revoke the surviving access token")
	}
}

func TestDisablingOAuthClientRevokesItsGrantAndTokens(t *testing.T) {
	service, store, authn, session, clientID := newOAuthServiceFixture(t)
	code, verifier, _ := authoriseFixture(t, service, authn, session, clientID)
	response := tokenPost(t, service.TokenHandler(), url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID},
		"code": {code}, "redirect_uri": {"https://client.example.test/callback"},
		"code_verifier": {verifier}, "resource": {"https://wiki.example.test/_/mcp"},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("code exchange = %d: %s", response.Code, response.Body.String())
	}
	var issued struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if _, ok := service.BearerVerifier().VerifyBearer(t.Context(), issued.AccessToken, mcpResourcePath); !ok {
		t.Fatal("test token was not initially valid")
	}
	if err := store.DisableClient(clientID); err != nil {
		t.Fatal(err)
	}
	if _, ok := service.BearerVerifier().VerifyBearer(t.Context(), issued.AccessToken, mcpResourcePath); ok {
		t.Fatal("disabled client retained a usable access token")
	}
	connections, err := service.Connections("alice")
	if err != nil || len(connections) != 1 || !connections[0].Revoked {
		t.Fatalf("disabled client grant = %#v, %v", connections, err)
	}
}

func TestOAuthTokenEndpointRejectsDuplicateParametersAndWrongClient(t *testing.T) {
	service, _, authn, session, clientID := newOAuthServiceFixture(t)
	code, verifier, _ := authoriseFixture(t, service, authn, session, clientID)
	handler := service.TokenHandler()
	fields := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID, "other-client"},
		"code":          {code},
		"redirect_uri":  {"https://client.example.test/callback"},
		"code_verifier": {verifier},
		"resource":      {"https://wiki.example.test/_/mcp"},
	}
	response := tokenPost(t, handler, fields)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("duplicate parameters returned %d: %s", response.Code, response.Body.String())
	}
	fields.Set("client_id", "other-client")
	response = tokenPost(t, handler, fields)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unknown client returned %d: %s", response.Code, response.Body.String())
	}
}

func TestOAuthClientAuthenticationMethodEnforcement(t *testing.T) {
	service, store, _, _, publicID := newOAuthServiceFixture(t)
	basicClient, err := store.ProvisionClient("Basic confidential client",
		[]string{"https://basic.example.test/callback"}, clientAuthBasic, []string{"read"}, false)
	if err != nil {
		t.Fatal(err)
	}
	postClient, err := store.ProvisionClient("Post confidential client",
		[]string{"https://post.example.test/callback"}, clientAuthPost, []string{"read"}, false)
	if err != nil {
		t.Fatal(err)
	}
	basicHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(basicClient.ClientID)+":"+url.QueryEscape(basicClient.Secret)))
	if _, err := service.authenticateClient(httptest.NewRequest(http.MethodPost, tokenPath, nil).WithContext(t.Context()), url.Values{}); err == nil {
		t.Fatal("empty client credentials were accepted")
	}
	request := httptest.NewRequest(http.MethodPost, tokenPath, nil)
	request.Header.Set("Authorization", strings.Replace(basicHeader, "Basic ", "basic ", 1))
	if credentials, err := service.authenticateClient(request, url.Values{}); err != nil || credentials.id != basicClient.ClientID {
		t.Fatalf("valid client_secret_basic credentials = %#v, %v", credentials, err)
	}
	if _, err := service.authenticateClient(request, url.Values{"client_id": {basicClient.ClientID}}); err == nil {
		t.Fatal("mixed Basic and form client credentials were accepted")
	}
	if _, err := service.authenticateClient(httptest.NewRequest(http.MethodPost, tokenPath, nil), url.Values{
		"client_id": {basicClient.ClientID}, "client_secret": {basicClient.Secret},
	}); err == nil {
		t.Fatal("client_secret_post was accepted for a Basic client")
	}
	if _, err := service.authenticateClient(httptest.NewRequest(http.MethodPost, tokenPath, nil), url.Values{
		"client_id": {postClient.ClientID}, "client_secret": {postClient.Secret},
	}); err != nil {
		t.Fatalf("valid client_secret_post credentials: %v", err)
	}
	postBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(postClient.ClientID)+":"+url.QueryEscape(postClient.Secret)))
	request = httptest.NewRequest(http.MethodPost, tokenPath, nil)
	request.Header.Set("Authorization", postBasic)
	if _, err := service.authenticateClient(request, url.Values{}); err == nil {
		t.Fatal("client_secret_basic was accepted for a post-authenticated client")
	}
	if _, err := service.authenticateClient(httptest.NewRequest(http.MethodPost, tokenPath, nil), url.Values{
		"client_id": {publicID}, "client_secret": {"unexpected"},
	}); err == nil {
		t.Fatal("public client unexpectedly accepted a secret")
	}
}

func TestOAuthRevocationIsClientBoundAndDisconnectRevokesGrant(t *testing.T) {
	service, store, authn, session, clientID := newOAuthServiceFixture(t)
	code, verifier, _ := authoriseFixture(t, service, authn, session, clientID)
	issuedResponse := tokenPost(t, service.TokenHandler(), url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID},
		"code": {code}, "redirect_uri": {"https://client.example.test/callback"},
		"code_verifier": {verifier}, "resource": {"https://wiki.example.test/_/mcp"},
	})
	if issuedResponse.Code != http.StatusOK {
		t.Fatalf("code exchange = %d: %s", issuedResponse.Code, issuedResponse.Body.String())
	}
	var issued struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(issuedResponse.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	other, err := store.ProvisionClient("Other client",
		[]string{"https://other.example.test/callback"}, clientAuthNone, []string{"read"}, false)
	if err != nil {
		t.Fatal(err)
	}
	revoke := service.RevocationHandler()
	wrongClient := tokenPost(t, revoke, url.Values{
		"client_id": {other.ClientID}, "token": {issued.AccessToken},
		"token_type_hint": {"access_token"},
	})
	if wrongClient.Code != http.StatusOK {
		t.Fatalf("wrong-client revocation = %d: %s", wrongClient.Code, wrongClient.Body.String())
	}
	if _, ok := service.BearerVerifier().VerifyBearer(t.Context(), issued.AccessToken, mcpResourcePath); !ok {
		t.Fatal("wrong client revoked another client's token")
	}
	unknown := tokenPost(t, revoke, url.Values{"client_id": {clientID}, "token": {"unknown-token"}})
	if unknown.Code != http.StatusOK || unknown.Body.Len() != 0 {
		t.Fatalf("unknown-token revocation = %d %q", unknown.Code, unknown.Body.String())
	}
	revoked := tokenPost(t, revoke, url.Values{
		"client_id": {clientID}, "token": {issued.AccessToken},
		"token_type_hint": {"access_token"},
	})
	if revoked.Code != http.StatusOK || revoked.Body.Len() != 0 {
		t.Fatalf("valid-token revocation = %d %q", revoked.Code, revoked.Body.String())
	}
	if _, ok := service.BearerVerifier().VerifyBearer(t.Context(), issued.AccessToken, mcpResourcePath); ok {
		t.Fatal("client revocation left the access token usable")
	}
	connections, err := service.Connections("alice")
	if err != nil || len(connections) != 1 || !connections[0].Revoked {
		t.Fatalf("connections = %#v, %v", connections, err)
	}
	if err := service.RevokeConnection("bob", connections[0].GrantID); err == nil {
		t.Fatal("another user revoked this connection")
	}
	if err := service.RevokeConnection("alice", connections[0].GrantID); err != nil {
		t.Fatalf("owner disconnect: %v", err)
	}
}

func TestConcurrentOAuthCodeRedemptionIssuesAtMostOnce(t *testing.T) {
	service, _, authn, session, clientID := newOAuthServiceFixture(t)
	code, verifier, _ := authoriseFixture(t, service, authn, session, clientID)
	fields := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID},
		"code": {code}, "redirect_uri": {"https://client.example.test/callback"},
		"code_verifier": {verifier}, "resource": {"https://wiki.example.test/_/mcp"},
	}
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			<-start
			responses <- tokenPost(t, service.TokenHandler(), fields)
		}()
	}
	close(start)
	first, second := <-responses, <-responses
	successes := 0
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		if response.Code == http.StatusOK {
			successes++
		} else if response.Code != http.StatusBadRequest {
			t.Fatalf("code redemption returned unexpected status %d: %s", response.Code, response.Body.String())
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent code redemptions succeeded %d times", successes)
	}
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		if response.Code != http.StatusOK {
			continue
		}
		var issued struct {
			Access string `json:"access_token"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &issued); err != nil {
			t.Fatal(err)
		}
		if _, ok := service.BearerVerifier().VerifyBearer(t.Context(), issued.Access, mcpResourcePath); ok {
			t.Fatal("concurrent code replay left the issued access token active")
		}
	}
}

func TestConcurrentRefreshReplayRevokesSurvivingFamily(t *testing.T) {
	service, _, authn, session, clientID := newOAuthServiceFixture(t)
	code, verifier, _ := authoriseFixture(t, service, authn, session, clientID)
	issued := tokenPost(t, service.TokenHandler(), url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID},
		"code": {code}, "redirect_uri": {"https://client.example.test/callback"},
		"code_verifier": {verifier}, "resource": {"https://wiki.example.test/_/mcp"},
	})
	if issued.Code != http.StatusOK {
		t.Fatalf("code exchange = %d: %s", issued.Code, issued.Body.String())
	}
	var original struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	fields := url.Values{
		"grant_type": {"refresh_token"}, "client_id": {clientID},
		"refresh_token": {original.RefreshToken},
	}
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			<-start
			responses <- tokenPost(t, service.TokenHandler(), fields)
		}()
	}
	close(start)
	first, second := <-responses, <-responses
	var success *httptest.ResponseRecorder
	successes := 0
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		if response.Code == http.StatusOK {
			successes++
			success = response
		} else if response.Code != http.StatusBadRequest {
			t.Fatalf("concurrent refresh returned unexpected status %d: %s", response.Code, response.Body.String())
		}
	}
	if successes != 1 || success == nil {
		t.Fatalf("concurrent refreshes succeeded %d times", successes)
	}
	var rotated struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(success.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if _, ok := service.BearerVerifier().VerifyBearer(t.Context(), rotated.AccessToken, mcpResourcePath); ok {
		t.Fatal("concurrent replay left a rotated access token active")
	}
}
