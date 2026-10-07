package oauthserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"hmd/internal/config"
	"hmd/internal/httpmiddleware"
)

func registerRequest(s *Service, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, registerPath, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.RegistrationHandler().ServeHTTP(w, r)
	return w
}

func TestDynamicRegistrationIsOptIn(t *testing.T) {
	s, _, _, _, _ := newOAuthServiceFixture(t)
	if got := registerRequest(s, `{}`).Code; got != http.StatusNotFound {
		t.Fatalf("disabled registration = %d", got)
	}
	for _, enabled := range []bool{false, true} {
		w := httptest.NewRecorder()
		MetadataHandler(ServerOptions{Issuer: s.options.Issuer, DynamicRegistration: enabled}).
			ServeHTTP(w, httptest.NewRequest("GET", authorizationMetadataPath, nil))
		if strings.Contains(w.Body.String(), `"registration_endpoint"`) != enabled {
			t.Fatalf("registration discovery = %s", w.Body.String())
		}
	}
}

func TestDynamicRegistrationCreatesClientWithoutGrant(t *testing.T) {
	s, store, _, _, _ := newOAuthServiceFixture(t)
	s.options.DynamicRegistration = true
	w := registerRequest(s, `{"client_name":"MCP test","redirect_uris":["https://client.example/callback"],"scope":"read write"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	c := state.Clients[response["client_id"].(string)]
	if !c.Dynamic || c.AuthMethod != clientAuthBasic || c.SecretDigest == "" || len(state.Grants) != 0 {
		t.Fatalf("registration or grant state is wrong: %#v", c)
	}
	if c.SecretDigest == response["client_secret"] {
		t.Fatal("plaintext secret persisted")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("secret response is cacheable")
	}
}

func TestDynamicClientUsesNormalAuthorizationFlow(t *testing.T) {
	s, _, authn, session, _ := newOAuthServiceFixture(t)
	s.options.DynamicRegistration = true
	w := registerRequest(s, `{"redirect_uris":["https://client.example.test/callback"],"token_endpoint_auth_method":"none","scope":"read write"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("registration = %d: %s", w.Code, w.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	id := response["client_id"].(string)
	if _, ok := response["client_secret"]; ok {
		t.Fatal("public client received secret")
	}
	code, _, _ := authoriseFixture(t, s, authn, session, id)
	if code == "" {
		t.Fatal("dynamic client could not complete consent")
	}
}

func TestDynamicRegistrationDefaultsToReadAndWrite(t *testing.T) {
	s, store, _, _, _ := newOAuthServiceFixture(t)
	s.options.DynamicRegistration = true
	w := registerRequest(s, `{"redirect_uris":["https://client.example.test/callback"],"token_endpoint_auth_method":"none"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("registration = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		ClientID string `json:"client_id"`
		Scope    string `json:"scope"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Scope != "read write" {
		t.Fatalf("response scope = %q, want read write", response.Scope)
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Clients[response.ClientID].AllowedScopes; !slices.Equal(got, []string{"read", "write"}) {
		t.Fatalf("client scopes = %v, want [read write]", got)
	}
}

func TestRegistrationThroughAuthenticationAndSecurity(t *testing.T) {
	s, _, authn, session, _ := newOAuthServiceFixture(t)
	s.options.DynamicRegistration = true
	security := httpmiddleware.Security{Auth: authn, Config: func() config.Config {
		return config.Config{BaseURL: s.options.Issuer}
	}}
	handler := authn.Middleware(security.Handler(s.ProtocolHandler()))
	r := httptest.NewRequest("POST", registerPath, strings.NewReader(`{"redirect_uris":["https://client.example/cb"],"token_endpoint_auth_method":"none"}`))
	r.Header.Set("Content-Type", "application/json")
	// Cookies neither authorise registration nor require browser CSRF.
	r.AddCookie(&http.Cookie{Name: "hmd_session", Value: session})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("registration middleware = %d: %s", w.Code, w.Body.String())
	}
}

func TestDynamicRegistrationRejectsInvalidMetadata(t *testing.T) {
	for _, body := range []string{
		`{"redirect_uris":["https://*.example/cb"]}`,
		`{"redirect_uris":["http://client.example/cb"]}`,
		`{"redirect_uris":["https://client.example/cb#"]}`,
		`{"redirect_uris":["https://user:password@client.example/cb"]}`,
		`{"redirect_uris":[]}`,
		`{"redirect_uris":["https://client.example/cb"],"scope":"settings"}`,
		`{"redirect_uris":["https://client.example/cb"],"grant_types":["client_credentials"]}`,
		`{"redirect_uris":["https://client.example/cb"],"response_types":["token"]}`,
		`{"redirect_uris":["https://client.example/cb"],"token_endpoint_auth_method":"invalid"}`,
		`{} {}`,
	} {
		t.Run(body, func(t *testing.T) {
			s, _, _, _, _ := newOAuthServiceFixture(t)
			s.options.DynamicRegistration = true
			w := registerRequest(s, body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestDynamicRegistrationGlobalBudget(t *testing.T) {
	s, _, _, _, _ := newOAuthServiceFixture(t)
	s.options.DynamicRegistration = true
	for i := 0; i < 5; i++ {
		registerRequest(s, `{}`)
	}
	w := registerRequest(s, `{}`)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("registration budget = %d", w.Code)
	}
}

func TestDynamicRegistrationReservesManualCapacity(t *testing.T) {
	s, store, _, _, _ := newOAuthServiceFixture(t)
	for i := 0; i < 32; i++ {
		if _, err := store.provisionClient("MCP", []string{"https://client.example/cb"}, "none", []string{"read"}, false, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.provisionClient("MCP", []string{"https://client.example/cb"}, "none", []string{"read"}, false, true); err == nil {
		t.Fatal("dynamic cap not enforced")
	}
	if _, err := s.ProvisionClient("Admin MCP", []string{"https://client.example/cb"}, "none", []string{"read"}); err != nil {
		t.Fatal(err)
	}
}
