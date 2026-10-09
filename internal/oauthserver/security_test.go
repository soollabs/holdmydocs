package oauthserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func authorizationFields(clientID string) url.Values {
	challenge, _ := s256Challenge(strings.Repeat("v", 43))
	return url.Values{
		"client_id": {clientID}, "redirect_uri": {"https://client.example.test/callback"},
		"response_type": {"code"}, "state": {"security-test"}, "scope": {"read write"},
		"resource":       {"https://wiki.example.test/_/mcp"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
}

func TestAuthorizationSecurityMatrix(t *testing.T) {
	service, _, _, session, clientID := newOAuthServiceFixture(t)
	for _, field := range []string{"client_id", "redirect_uri", "response_type", "state", "scope", "resource", "code_challenge", "code_challenge_method"} {
		t.Run("duplicate "+field, func(t *testing.T) {
			values := authorizationFields(clientID)
			values.Add(field, values.Get(field))
			_, err := service.StartAuthorization(httptest.NewRequestWithContext(t.Context(), "GET", authorizePath+"?"+values.Encode(), nil), session, []string{"notes"})
			if err == nil {
				t.Fatalf("duplicate %s was accepted", field)
			}
		})
	}
	for _, tc := range []struct{ name, field, value string }{
		{"redirect suffix", "redirect_uri", "https://client.example.test.attacker.test/callback"},
		{"redirect path", "redirect_uri", "https://client.example.test/callback/extra"},
		{"redirect query", "redirect_uri", "https://client.example.test/callback?next=other"},
		{"implicit flow", "response_type", "token"},
		{"plain PKCE", "code_challenge_method", "plain"},
		{"missing PKCE", "code_challenge", ""},
		{"weak PKCE", "code_challenge", "short"},
		{"resource confusion", "resource", "https://wiki.example.test/_/api"},
		{"scope escalation", "scope", "settings"},
		{"duplicate scope", "scope", "read read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := authorizationFields(clientID)
			values.Set(tc.field, tc.value)
			started, err := service.StartAuthorization(httptest.NewRequestWithContext(t.Context(), "GET", authorizePath+"?"+values.Encode(), nil), session, []string{"notes"})
			if err == nil || started.Handle != "" {
				t.Fatal("invalid request created a browser flow")
			}
		})
	}
	for _, suffix := range []string{"&state=%zz", "&redirect_uri=%zz", "&hint=%zz", "&hint=bad;encoding"} {
		t.Run("malformed "+suffix, func(t *testing.T) {
			_, err := service.StartAuthorization(httptest.NewRequestWithContext(t.Context(), "GET", authorizePath+"?"+authorizationFields(clientID).Encode()+suffix, nil), session, []string{"notes"})
			if err == nil {
				t.Fatal("partially parsed authorisation query was accepted")
			}
		})
	}
}

func TestConsentBindingAndPrivilegeBoundaries(t *testing.T) {
	service, store, _, session, clientID := newOAuthServiceFixture(t)
	for _, attack := range []string{"flow cookie", "session", "user", "scope", "empty namespaces", "unknown namespace", "implicit all"} {
		t.Run(attack, func(t *testing.T) {
			started, err := service.StartAuthorization(httptest.NewRequestWithContext(t.Context(), "GET", authorizePath+"?"+authorizationFields(clientID).Encode(), nil), session, []string{"notes"})
			if err != nil {
				t.Fatal(err)
			}
			flow, sessionValue, user, scopes, mode, names := started.FlowCookie, session, "alice", "read", "selected", []string{"notes"}
			switch attack {
			case "flow cookie":
				flow = "wrong"
			case "session":
				sessionValue = "wrong"
			case "user":
				user = "other"
			case "scope":
				scopes = "settings"
			case "empty namespaces":
				names = nil
			case "unknown namespace":
				names = []string{"private"}
			case "implicit all":
				mode = ""
			}
			if _, err := service.CompleteAuthorization(started.Handle, flow, user, sessionValue, true, scopes, mode, names, []string{"notes"}); err == nil {
				t.Fatal("invalid consent was accepted")
			}
			state, err := store.Snapshot()
			if err != nil || len(state.Grants) != 0 || len(state.Tokens) != 0 {
				t.Fatal("invalid consent persisted credentials")
			}
			// Failed validation must still allow the rightful browser to deny.
			callback, err := service.CompleteAuthorization(started.Handle, started.FlowCookie, "alice", session, false, "", "", nil, nil)
			if err != nil || !strings.Contains(callback, "error=access_denied") {
				t.Fatalf("denial failed: %v", err)
			}
		})
	}
}

func TestCodeReplayRevokesOnlyAfterProofOfBinding(t *testing.T) {
	service, store, authn, session, clientID := newOAuthServiceFixture(t)
	code, verifier, _ := authoriseFixture(t, service, authn, session, clientID)
	fields := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {"https://client.example.test/callback"}, "code_verifier": {verifier},
		"resource": {"https://wiki.example.test/_/mcp"},
	}
	response := tokenPost(t, service.TokenHandler(), fields)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	var issued struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	principal, ok := service.BearerVerifier().VerifyBearer(t.Context(), issued.Access, mcpResourcePath)
	if !ok {
		t.Fatal("issued token is invalid")
	}
	for _, field := range []string{"code_verifier", "redirect_uri", "resource", "client_id"} {
		original := fields.Get(field)
		fields.Set(field, "wrong")
		response = tokenPost(t, service.TokenHandler(), fields)
		fields.Set(field, original)
		if response.Code == http.StatusOK {
			t.Fatalf("replay with wrong %s was accepted", field)
		}
		if _, ok := service.BearerVerifier().VerifyBearer(t.Context(), issued.Access, mcpResourcePath); !ok {
			t.Fatalf("wrong %s let an attacker revoke the connection", field)
		}
	}
	// The code is no longer redeemable, but retained consumption evidence
	// must still protect its longer-lived tokens against replay.
	if err := store.Update(func(state *oauthState) error {
		key := "c:" + tokenDigest(code)
		record := state.Tokens[key]
		record.CodeExpiresAt = time.Now().Add(-time.Second)
		state.Tokens[key] = record
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response = tokenPost(t, service.TokenHandler(), fields)
	if response.Code != http.StatusBadRequest {
		t.Fatal("reused code was accepted")
	}
	if _, ok := service.BearerVerifier().VerifyBearer(t.Context(), issued.Access, mcpResourcePath); ok {
		t.Fatal("code replay left its access token active")
	}
	if service.AuthorizeUpload(t.Context(), principal.GrantID, principal.FamilyID, principal.User, "notes/page", principal.Scopes) {
		t.Fatal("code replay left a delegated upload active")
	}
	connections, err := service.Connections("alice")
	if err != nil || len(connections) != 1 || !connections[0].Revoked {
		t.Fatal("replay-revoked family still appears connected")
	}
	response = tokenPost(t, service.TokenHandler(), url.Values{
		"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {issued.Refresh},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatal("code replay left its refresh token active")
	}
}

func TestClientAuthenticationRejectsRepeatedHeaders(t *testing.T) {
	service, _, _, _, clientID := newOAuthServiceFixture(t)
	r := httptest.NewRequestWithContext(t.Context(), "POST", tokenPath, nil)
	r.Header.Add("Authorization", "Basic first")
	r.Header.Add("Authorization", "Basic second")
	if _, err := service.authenticateClient(r, url.Values{"client_id": {clientID}}); err == nil {
		t.Fatal("ambiguous client authentication was accepted")
	}
}

func TestRevocationIgnoresUnknownTypeHint(t *testing.T) {
	service, _, authn, session, clientID := newOAuthServiceFixture(t)
	code, verifier, _ := authoriseFixture(t, service, authn, session, clientID)
	response := tokenPost(t, service.TokenHandler(), url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {"https://client.example.test/callback"}, "code_verifier": {verifier},
		"resource": {"https://wiki.example.test/_/mcp"},
	})
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	var issued struct {
		Access string `json:"access_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	response = tokenPost(t, service.RevocationHandler(), url.Values{
		"client_id": {clientID}, "token": {issued.Access}, "token_type_hint": {"future_token_type"},
	})
	if response.Code != http.StatusOK {
		t.Fatal("unknown hint prevented revocation")
	}
	if _, ok := service.BearerVerifier().VerifyBearer(t.Context(), issued.Access, mcpResourcePath); ok {
		t.Fatal("unknown hint left the access token active")
	}
}

func TestProvisionRejectsReservedCallbackParameters(t *testing.T) {
	for _, suffix := range []string{"?code=x", "?state=x", "?iss=x", "?error=x", "?error_description=x", "?%63ode=x", "?hint=%zz", "?a=b;c"} {
		if err := validateRedirectURI("https://client.example.test/callback" + suffix); err == nil {
			t.Errorf("accepted unsafe callback query %q", suffix)
		}
	}
	if err := validateRedirectURI("https://client.example.test/callback?tenant=example"); err != nil {
		t.Fatal("ordinary registered query parameters must remain supported")
	}
}

func FuzzOAuthFormSingletons(f *testing.F) {
	for _, seed := range []string{"client_id=example&grant_type=authorization_code", "code=a&code=b", "code=a&%63ode=b", "code=%zz", "code=a;b"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		r := httptest.NewRequestWithContext(t.Context(), "POST", tokenPath, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		form, err := parseOAuthForm(httptest.NewRecorder(), r)
		if err != nil {
			return
		}
		for key, values := range form {
			if len(values) != 1 {
				t.Fatalf("accepted ambiguous parameter %q", key)
			}
		}
	})
}

func TestAuthorizationErrorsRedirectAfterRedirectValidation(t *testing.T) {
	service, _, _, session, clientID := newOAuthServiceFixture(t)

	// Errors before the redirect URI is proven must never redirect.
	for _, corrupt := range []struct{ field, value string }{
		{"redirect_uri", "https://attacker.example.test/callback"},
		{"client_id", "hmd_c_unknown"},
	} {
		values := authorizationFields(clientID)
		values.Set(corrupt.field, corrupt.value)
		_, err := service.StartAuthorization(httptest.NewRequestWithContext(t.Context(), "GET", authorizePath+"?"+values.Encode(), nil), session, []string{"notes"})
		if err == nil {
			t.Fatal("invalid request was accepted")
		}
		if _, ok := ErrorRedirect(err, service.options.Issuer); ok {
			t.Fatalf("unvalidated %s produced an error redirect", corrupt.field)
		}
	}
	// Once the redirect URI is exactly validated, parameter errors redirect.
	for _, corrupt := range []struct{ field, value, code string }{
		{"response_type", "token", "unsupported_response_type"},
		{"resource", "https://wiki.example.test/_/api", "invalid_target"},
		{"code_challenge_method", "plain", "invalid_request"},
		{"scope", "settings", "invalid_scope"},
	} {
		t.Run(corrupt.code, func(t *testing.T) {
			values := authorizationFields(clientID)
			values.Set(corrupt.field, corrupt.value)
			_, err := service.StartAuthorization(httptest.NewRequestWithContext(t.Context(), "GET", authorizePath+"?"+values.Encode(), nil), session, []string{"notes"})
			if err == nil {
				t.Fatal("invalid request was accepted")
			}
			redirect, ok := ErrorRedirect(err, service.options.Issuer)
			if !ok {
				t.Fatalf("%s error did not redirect", corrupt.field)
			}
			parsed, parseErr := url.Parse(redirect)
			if parseErr != nil || parsed.Host != "client.example.test" || parsed.Path != "/callback" {
				t.Fatalf("error redirect target = %q", redirect)
			}
			query := parsed.Query()
			if query.Get("error") != corrupt.code || query.Get("state") != "security-test" ||
				query.Get("iss") != "https://wiki.example.test" || query.Get("error_description") == "" {
				t.Fatalf("error redirect parameters = %q", redirect)
			}
		})
	}
}

func TestDynamicClientAuthorizationErrorsDoNotRedirect(t *testing.T) {
	service, store, _, session, _ := newOAuthServiceFixture(t)
	dynamic, err := store.provisionClient("Dynamic test client",
		[]string{"https://dynamic.example.test/callback"}, clientAuthNone, []string{"read"}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	values := authorizationFields(dynamic.ClientID)
	values.Set("redirect_uri", "https://dynamic.example.test/callback")
	values.Set("scope", "read")
	values.Set("response_type", "token")
	_, err = service.StartAuthorization(httptest.NewRequestWithContext(t.Context(), "GET", authorizePath+"?"+values.Encode(), nil), session, []string{"notes"})
	if err == nil {
		t.Fatal("invalid request was accepted")
	}
	if redirect, ok := ErrorRedirect(err, service.options.Issuer); ok {
		t.Fatalf("dynamically registered client received an error redirect: %q", redirect)
	}
}
