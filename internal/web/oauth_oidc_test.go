package web

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"hmd/internal/api"
	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/oauthserver"
)

func TestOAuthAuthorizationContinuationSurvivesOIDCLogin(t *testing.T) {
	authn, err := auth.Open(auth.Options{AppDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	state, err := oauthserver.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	client, err := state.ProvisionClient("OIDC-flow test",
		[]string{"https://client.example.test/callback"}, "none", []string{"read"}, false)
	if err != nil {
		t.Fatal(err)
	}
	service, err := oauthserver.NewService(oauthserver.ServerOptions{Issuer: "https://wiki.example.test"}, state, authn)
	if err != nil {
		t.Fatal(err)
	}
	provider := newFakeOIDCProvider(t)
	defer provider.server.Close()
	cfg := config.Config{
		BaseURL: "https://wiki.example.test",
		OIDC: config.OIDCConfig{
			Issuer: provider.server.URL, ClientID: "hmd-test-client",
			ClientSecret:        "test-client-secret",
			AllowedEmailDomains: []string{"example.test"}, DefaultScopes: []string{"read"},
		},
	}
	oidcClient, err := NewOIDCAuth(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		API: api.New(nil, nil, authn), Auth: authn, OAuth: service,
		OIDC: oidcClient,
	}
	app.SetConfig(cfg)

	challenge := "7Cf9_Fvrp7a3sr2lTFtL8Y7unb12dzT_5c1NvGm2HSo"
	authorizeParams := url.Values{
		"client_id":     {client.ClientID},
		"redirect_uri":  {"https://client.example.test/callback"},
		"response_type": {"code"}, "state": {"oidc-flow-state"},
		"scope": {"read"}, "resource": {"https://wiki.example.test/_/mcp"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	startRequest := httptest.NewRequest(http.MethodGet, "/_/oauth/authorize?"+authorizeParams.Encode(), nil)
	started, err := service.StartAuthorization(startRequest, "", []string{"notes"})
	if err != nil || !started.LoginRequired || started.Handle == "" || started.FlowCookie == "" {
		t.Fatalf("OAuth login start = %#v, %v", started, err)
	}

	loginRequest := httptest.NewRequest(http.MethodGet, "/_/auth/oidc/login?continue="+url.QueryEscape(started.Handle), nil)
	loginResponse := httptest.NewRecorder()
	app.handleOIDCLogin(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusSeeOther {
		t.Fatalf("OIDC login = %d", loginResponse.Code)
	}
	providerURL, err := url.Parse(loginResponse.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	providerState := providerURL.Query().Get("state")
	provider.expectedChallenge = providerURL.Query().Get("code_challenge")
	if providerURL.Query().Get("code_challenge_method") != "S256" || provider.expectedChallenge == "" {
		t.Fatal("OIDC request did not include S256 PKCE")
	}
	cookies := make(map[string]string)
	for _, cookie := range loginResponse.Result().Cookies() {
		cookies[cookie.Name] = cookie.Value
	}
	if providerState == "" || cookies["hmd_oidc_continue"] != providerState+"."+started.Handle {
		t.Fatal("OIDC continuation was not bound to its upstream state")
	}

	callback := httptest.NewRequest(http.MethodGet,
		"/_/auth/oidc/callback?state="+url.QueryEscape(providerState)+"&code=upstream-code", nil)
	for name, value := range cookies {
		callback.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	callback.AddCookie(&http.Cookie{Name: "hmd_oauth_flow", Value: started.FlowCookie})
	callbackResponse := httptest.NewRecorder()
	app.handleOIDCCallback(callbackResponse, callback)
	if callbackResponse.Code != http.StatusSeeOther ||
		!strings.Contains(callbackResponse.Header().Get("Location"), "/_/oauth/authorize?request=") {
		t.Fatalf("OIDC callback did not resume authorisation: %d %q", callbackResponse.Code, callbackResponse.Header().Get("Location"))
	}
	var session string
	for _, cookie := range callbackResponse.Result().Cookies() {
		if cookie.Name == "hmd_session" {
			session = cookie.Value
		}
	}
	if session == "" {
		t.Fatal("OIDC callback did not set an HMD session")
	}
	if user, ok := authn.UserFor(session); !ok || user != "alice" {
		t.Fatalf("fake-provider login user = %q, valid=%v", user, ok)
	}
	resume := httptest.NewRequest(http.MethodGet, callbackResponse.Header().Get("Location"), nil)
	resume.AddCookie(&http.Cookie{Name: "hmd_oauth_flow", Value: started.FlowCookie})
	result, err := service.StartAuthorization(resume, session, []string{"notes"})
	if err != nil || result.Prompt == nil || result.Prompt.User != "alice" {
		t.Fatalf("resumed OIDC authorisation = %#v, %v", result, err)
	}
}

type fakeOIDCProvider struct {
	server            *httptest.Server
	privateKey        *rsa.PrivateKey
	expectedChallenge string
}

func newFakeOIDCProvider(t *testing.T) *fakeOIDCProvider {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeOIDCProvider{privateKey: privateKey}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                provider.server.URL,
			"authorization_endpoint":                provider.server.URL + "/authorize",
			"token_endpoint":                        provider.server.URL + "/token",
			"jwks_uri":                              provider.server.URL + "/keys",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_post"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		exponent := make([]byte, 4)
		binary.BigEndian.PutUint32(exponent, uint32(privateKey.PublicKey.E))
		exponent = bytes.TrimLeft(exponent, "\x00")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA", "use": "sig", "kid": "hmd-test-key", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString([]byte(exponent)),
			}},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "authorization_code" ||
			r.Form.Get("code") != "upstream-code" || r.Form.Get("client_id") != "hmd-test-client" ||
			r.Form.Get("client_secret") != "test-client-secret" {
			http.Error(w, "invalid token request", http.StatusBadRequest)
			return
		}
		verifier := r.Form.Get("code_verifier")
		digest := sha256.Sum256([]byte(verifier))
		if base64.RawURLEncoding.EncodeToString(digest[:]) != provider.expectedChallenge {
			http.Error(w, "invalid PKCE verifier", http.StatusBadRequest)
			return
		}
		idToken, err := provider.signIDToken()
		if err != nil {
			http.Error(w, "could not sign test token", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-provider-access-token",
			"token_type":   "Bearer", "expires_in": 300, "id_token": idToken,
		})
	})
	provider.server = httptest.NewServer(mux)
	return provider
}

func (p *fakeOIDCProvider) signIDToken() (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": "hmd-test-key", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	now := time.Now()
	claims, err := json.Marshal(map[string]any{
		"iss": p.server.URL, "sub": "oidc-subject", "aud": "hmd-test-client",
		"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
		"preferred_username": "alice", "email": "alice@example.test",
		"email_verified": true, "name": "Alice Example",
	})
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, p.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
