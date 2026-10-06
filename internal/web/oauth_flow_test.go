package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hmd/internal/oauthserver"
)

func TestOAuthAuthorizationResumesPasswordLoginAndRendersConsent(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	state, err := oauthserver.OpenStore(app.config().AppDir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	client, err := state.ProvisionClient("Browser-flow test",
		[]string{"https://client.example.test/callback"}, "none", []string{"read"}, false)
	if err != nil {
		t.Fatal(err)
	}
	app.OAuth, err = oauthserver.NewService(oauthserver.ServerOptions{Issuer: "https://wiki.example.test"}, state, app.Auth)
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	server = httptest.NewServer(testHandler(app.App))
	defer server.Close()
	params := url.Values{
		"client_id":             {client.ClientID},
		"redirect_uri":          {"https://client.example.test/callback"},
		"response_type":         {"code"},
		"state":                 {"browser-flow-state"},
		"scope":                 {"read"},
		"resource":              {"https://wiki.example.test/_/mcp"},
		"code_challenge":        {"7Cf9_Fvrp7a3sr2lTFtL8Y7unb12dzT_5c1NvGm2HSo"},
		"code_challenge_method": {"S256"},
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{
		Jar:           jar,
		Transport:     csrfTestTransport{base: http.DefaultTransport, jar: jar, auth: app.Auth},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := browser.Get(server.URL + "/_/oauth/authorize?" + params.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("authorisation start = %d", response.StatusCode)
	}
	loginURL := response.Header.Get("Location")
	_ = response.Body.Close()
	if !strings.HasPrefix(loginURL, "/_/login?continue=") {
		t.Fatalf("login continuation redirect = %q", loginURL)
	}
	response, err = browser.Get(server.URL + loginURL)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login page = %d", response.StatusCode)
	}
	loginBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !strings.Contains(string(loginBody), "oauth_continue") {
		t.Fatal("login page did not preserve the opaque continuation")
	}
	loginQuery, err := url.Parse(loginURL)
	if err != nil {
		t.Fatal(err)
	}
	loginValues := url.Values{
		"username": {"admin"}, "password": {"password12345"},
		"oauth_continue": {loginQuery.Query().Get("continue")},
	}
	response, err = browser.PostForm(server.URL+"/_/login", loginValues)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSeeOther ||
		!strings.HasPrefix(response.Header.Get("Location"), "/_/oauth/authorize?request=") {
		t.Fatalf("successful login did not resume OAuth: %d %q", response.StatusCode, response.Header.Get("Location"))
	}
	authorizeURL := response.Header.Get("Location")
	_ = response.Body.Close()
	response, err = browser.Get(server.URL + authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("consent page = %d: %s", response.StatusCode, body)
	}
	consent, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(consent), "Browser-flow test") ||
		!strings.Contains(string(consent), "Approve connection") ||
		!strings.Contains(string(consent), "notes") {
		t.Fatalf("consent page omitted client or namespace details: %s", consent)
	}
	authorizeQuery, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	consentValues := url.Values{
		"request":  {authorizeQuery.Query().Get("request")},
		"decision": {"approve"}, "scope": {"read"},
		"namespace_mode": {"selected"}, "namespace": {"notes"},
	}
	response, err = browser.PostForm(server.URL+"/_/oauth/authorize", consentValues)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	callback, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSeeOther ||
		callback.Host != "client.example.test" ||
		callback.Query().Get("state") != "browser-flow-state" ||
		callback.Query().Get("iss") != "https://wiki.example.test" ||
		callback.Query().Get("code") == "" {
		t.Fatalf("consent approval redirect = %d %q", response.StatusCode, response.Header.Get("Location"))
	}
	response, err = browser.Get(server.URL + "/_/connections")
	if err != nil {
		t.Fatal(err)
	}
	connectionBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(connectionBody), "Browser-flow test") {
		t.Fatalf("connections page = %d: %s", response.StatusCode, connectionBody)
	}
	connections, err := app.OAuth.Connections("admin")
	if err != nil || len(connections) != 1 {
		t.Fatalf("listed connections = %#v, %v", connections, err)
	}
	response, err = browser.PostForm(server.URL+"/_/connections/"+connections[0].GrantID+"/revoke", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/_/connections" {
		t.Fatalf("disconnect response = %d %q", response.StatusCode, response.Header.Get("Location"))
	}
	connections, err = app.OAuth.Connections("admin")
	if err != nil || len(connections) != 1 || !connections[0].Revoked {
		t.Fatalf("disconnected connection = %#v, %v", connections, err)
	}
}
