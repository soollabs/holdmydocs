package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"hmd/internal/oauthserver"
)

func TestOAuthClientAdministration(t *testing.T) {
	app, server, browser := newTestAppFull(t)
	store, err := oauthserver.OpenStore(app.config().AppDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app.OAuth, err = oauthserver.NewService(oauthserver.ServerOptions{Issuer: "https://wiki.example.test"}, store, app.Auth)
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	security := app.Security()
	server = httptest.NewServer(app.Auth.Middleware(security.Handler(app.Routes())))
	defer server.Close()
	adminLogin(t, server, browser)
	browser.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	token, err := app.Auth.AddToken("admin", "admin-test", time.Time{}, []string{"settings"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bearerRequest, _ := http.NewRequest("GET", server.URL+"/_/admin/oauth", nil)
	bearerRequest.Header.Set("Authorization", "Bearer "+token)
	bearerResponse, err := http.DefaultClient.Do(bearerRequest)
	if err != nil {
		t.Fatal(err)
	}
	bearerResponse.Body.Close()
	if bearerResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bearer administration = %d", bearerResponse.StatusCode)
	}
	form := url.Values{
		"name": {"MCP client"}, "redirect_uris": {"https://client.example/cb"},
		"auth_method": {"client_secret_post"}, "scope": {"read", "write"},
	}
	response, err := browser.PostForm(server.URL+"/_/admin/oauth", form)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated || !strings.Contains(string(body), "hmd_cs_") {
		t.Fatalf("create = %d: %s", response.StatusCode, body)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("secret page is cacheable")
	}
	if response.Header.Get("Referrer-Policy") != "same-origin" {
		t.Fatal("native client forms need a same-origin referrer policy")
	}
	clients, err := app.OAuth.Clients()
	if err != nil || len(clients) != 1 {
		t.Fatalf("clients = %#v, %v", clients, err)
	}
	response, err = browser.Get(server.URL + "/_/admin/oauth")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.Header.Get("Referrer-Policy") != "same-origin" {
		t.Fatal("client list must preserve the native form Origin")
	}
	if strings.Contains(string(body), "hmd_cs_") || strings.Contains(string(body), "$2a$") {
		t.Fatal("list exposes credentials")
	}
	// A session without its Origin/CSRF proof cannot mutate clients.
	request, _ := http.NewRequest("POST", server.URL+"/_/admin/oauth", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range browser.Jar.Cookies(request.URL) {
		request.AddCookie(c)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF = %d", response.StatusCode)
	}
	response, err = browser.PostForm(server.URL+"/_/admin/oauth/"+clients[0].ID+"/disable", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("disable = %d", response.StatusCode)
	}
	clients, _ = app.OAuth.Clients()
	if !clients[0].Disabled {
		t.Fatal("client was not disabled")
	}
	response, err = browser.PostForm(server.URL+"/_/admin/oauth/"+clients[0].ID+"/delete", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete = %d", response.StatusCode)
	}
	clients, _ = app.OAuth.Clients()
	if len(clients) != 0 {
		t.Fatal("deleted client remains in list")
	}
}
