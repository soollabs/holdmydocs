package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"hmd/internal/oauthserver"
)

func TestOAuthBrowserTokenMCPAndDisconnectLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process lifecycle is Unix-specific")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "hmd")
	build := exec.Command("go", "build", "-o", binary, "./cmd/hmd")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	baseURL := "http://" + address
	data := t.TempDir()
	appDir := filepath.Join(data, "app")
	configPath := filepath.Join(data, "config.yaml")
	if err := os.MkdirAll(appDir, 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(
		"base_url: %q\nmcp:\n  enabled: true\noauth:\n  enabled: true\n  allow_insecure_loopback: true\n",
		baseURL,
	)
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := oauthserver.OpenStore(appDir)
	if err != nil {
		t.Fatal(err)
	}
	client, err := state.ProvisionClient(
		"E2E MCP client", []string{"https://client.example.test/callback"},
		"none", []string{"read", "write"}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary)
	command.Env = append(os.Environ(),
		"HMD_BIND="+address,
		"HMD_APP_DIR="+appDir,
		"HMD_REPO_DIR="+filepath.Join(data, "repo"),
		"HMD_CONFIG_FILE="+configPath,
		"HMD_ADMIN_USER=admin",
		"HMD_ADMIN_PASSWORD=password12345",
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	t.Cleanup(func() {
		if command.Process != nil && !finished {
			_ = command.Process.Kill()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		}
	})

	base := "http://" + address
	waitReady(t, base+"/_/ready")
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	deniedRedirect := beginOAuthConsent(t, browser, base, client.ClientID, "denied-state", strings.Repeat("d", 43), true)
	deniedQuery := mustQuery(t, deniedRedirect)
	if deniedQuery.Get("error") != "access_denied" || deniedQuery.Get("state") != "denied-state" ||
		deniedQuery.Get("iss") != base {
		t.Fatalf("consent denial redirect = %q", deniedRedirect)
	}

	verifier := strings.Repeat("v", 43)
	codeRedirect := beginOAuthConsent(t, browser, base, client.ClientID, "approved-state", verifier, false)
	codeQuery := mustQuery(t, codeRedirect)
	code := codeQuery.Get("code")
	if code == "" || codeQuery.Get("state") != "approved-state" || codeQuery.Get("iss") != base {
		t.Fatalf("approval redirect = %q", codeRedirect)
	}
	tokenForm := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {client.ClientID},
		"code": {code}, "redirect_uri": {"https://client.example.test/callback"},
		"code_verifier": {verifier}, "resource": {base + "/_/mcp"},
	}
	first := postOAuthForm(t, browser, base+"/_/oauth/token", tokenForm, "")
	if first.StatusCode != http.StatusOK {
		t.Fatalf("code exchange = %d: %s", first.StatusCode, readE2EBody(t, first))
	}
	var issued struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	decodeE2EJSON(t, first, &issued)
	if issued.AccessToken == "" || issued.RefreshToken == "" {
		t.Fatal("code exchange returned incomplete tokens")
	}

	assertMCPTools(t, base, issued.AccessToken)
	assertLegacyOAuthMCP(t, base, issued.AccessToken)
	refresh := postOAuthForm(t, browser, base+"/_/oauth/token", url.Values{
		"grant_type": {"refresh_token"}, "client_id": {client.ClientID},
		"refresh_token": {issued.RefreshToken},
	}, "")
	if refresh.StatusCode != http.StatusOK {
		t.Fatalf("refresh = %d: %s", refresh.StatusCode, readE2EBody(t, refresh))
	}
	var rotated struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	decodeE2EJSON(t, refresh, &rotated)
	if rotated.AccessToken == issued.AccessToken || rotated.RefreshToken == issued.RefreshToken {
		t.Fatal("refresh did not rotate both credentials")
	}
	assertMCPTools(t, base, rotated.AccessToken)

	connections := getE2E(t, browser, base+"/_/connections")
	connectionPage := readE2EBody(t, connections)
	if connections.StatusCode != http.StatusOK || !strings.Contains(connectionPage, "E2E MCP client") {
		t.Fatalf("connections page = %d: %s", connections.StatusCode, connectionPage)
	}
	grantID := captureE2E(t, `/_/connections/([^/" ]+)/revoke`, connectionPage)
	csrf := captureE2E(t, `name="csrf_token" value="([^"]+)"`, connectionPage)
	disconnected := postOAuthForm(t, browser, base+"/_/connections/"+grantID+"/revoke", url.Values{}, csrf)
	if disconnected.StatusCode != http.StatusSeeOther || disconnected.Header.Get("Location") != "/_/connections" {
		t.Fatalf("disconnect = %d %q", disconnected.StatusCode, disconnected.Header.Get("Location"))
	}
	_ = disconnected.Body.Close()

	for _, version := range []string{"2025-03-26", "2025-06-18", "2025-11-25", "2026-07-28"} {
		response, _ := postMCPRPC(t, base, rotated.AccessToken, version,
			`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
		if response.StatusCode != http.StatusUnauthorized ||
			!strings.Contains(response.Header.Get("WWW-Authenticate"), `error="invalid_token"`) {
			t.Fatalf("disconnected bearer (%s): status=%d challenge=%q",
				version, response.StatusCode, response.Header.Get("WWW-Authenticate"))
		}
	}
	_ = command.Process.Signal(os.Interrupt)
	select {
	case err := <-done:
		finished = true
		if err != nil {
			t.Fatalf("server exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop gracefully")
	}
}

func beginOAuthConsent(t *testing.T, browser *http.Client, base, clientID, state, verifier string, deny bool) string {
	t.Helper()
	digest := sha256.Sum256([]byte(verifier))
	params := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {"https://client.example.test/callback"},
		"response_type": {"code"}, "scope": {"read"},
		"resource":              {base + "/_/mcp"},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(digest[:])},
		"code_challenge_method": {"S256"},
	}
	response := getE2E(t, browser, base+"/_/oauth/authorize?"+params.Encode())
	location := response.Header.Get("Location")
	var consentBody string
	switch response.StatusCode {
	case http.StatusOK:
		consentBody = readE2EBody(t, response)
	case http.StatusSeeOther:
		_ = response.Body.Close()
	default:
		t.Fatalf("authorization start = %d: %s", response.StatusCode, readE2EBody(t, response))
	}
	if strings.HasPrefix(location, "/_/login") {
		login := getE2E(t, browser, base+location)
		loginBody := readE2EBody(t, login)
		if login.StatusCode != http.StatusOK {
			t.Fatalf("login page = %d: %s", login.StatusCode, loginBody)
		}
		continueHandle := captureE2E(t, `name="oauth_continue" value="([^"]+)"`, loginBody)
		response = postOAuthForm(t, browser, base+"/_/login", url.Values{
			"username": {"admin"}, "password": {"password12345"},
			"oauth_continue": {continueHandle},
		}, "")
		if response.StatusCode != http.StatusSeeOther {
			t.Fatalf("local login = %d: %s", response.StatusCode, readE2EBody(t, response))
		}
		location = response.Header.Get("Location")
		_ = response.Body.Close()
	}
	if consentBody == "" {
		consent := getE2E(t, browser, base+location)
		consentBody = readE2EBody(t, consent)
		if consent.StatusCode != http.StatusOK {
			t.Fatalf("consent page = %d: %s", consent.StatusCode, consentBody)
		}
	}
	if !strings.Contains(consentBody, "Connect to HoldMyDocs") {
		t.Fatalf("consent page missing title: %s", consentBody)
	}
	csrf := captureE2E(t, `name="csrf_token" value="([^"]+)"`, consentBody)
	handle := captureE2E(t, `name="request" value="([^"]+)"`, consentBody)
	form := url.Values{
		"csrf_token": {csrf}, "request": {handle},
		"namespace_mode": {"selected"},
	}
	if deny {
		form.Set("decision", "deny")
	} else {
		form.Set("decision", "approve")
		form.Set("scope", "read")
		form.Set("namespace_mode", "all")
	}
	response = postOAuthForm(t, browser, base+"/_/oauth/authorize", form, csrf)
	returnBody := readE2EBody(t, response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("consent submission = %d: %s", response.StatusCode, returnBody)
	}
	location = html.UnescapeString(captureE2E(t, `id="oauth-return-link" href="([^"]+)"`, returnBody))
	return location
}

func assertMCPTools(t *testing.T, endpoint, token string) {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "oauth-e2e", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:   endpoint + "/_/mcp",
		HTTPClient: &http.Client{Transport: e2eBearerTransport{token}},
	}, nil)
	if err != nil {
		t.Fatalf("MCP OAuth connection: %v", err)
	}
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		_ = session.Close()
		t.Fatalf("MCP OAuth tools/list: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("MCP OAuth tools/list returned no tools")
	}
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name: "list_namespaces", Arguments: map[string]any{},
	})
	if err != nil || result.IsError {
		t.Fatalf("MCP OAuth tools/call: result=%v error=%v", result, err)
	}
	if err := session.Close(); err != nil {
		t.Errorf("closing MCP OAuth session: %v", err)
	}
}

func assertLegacyOAuthMCP(t *testing.T, base, token string) {
	t.Helper()
	for _, version := range []string{"2025-03-26", "2025-06-18", "2025-11-25"} {
		response, body := postMCPRPC(t, base, token, "", fmt.Sprintf(
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":%q,"clientInfo":{"name":"oauth-e2e","version":"test"},"capabilities":{}}}`, version))
		if response.StatusCode != http.StatusOK || !strings.Contains(body, `"protocolVersion":"`+version+`"`) {
			t.Fatalf("legacy OAuth initialise (%s): %d %s", version, response.StatusCode, body)
		}
		response, body = postMCPRPC(t, base, token, version, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
		if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusOK {
			t.Fatalf("legacy OAuth initialised (%s): %d %s", version, response.StatusCode, body)
		}
		for _, header := range []string{version, ""} {
			response, body = postMCPRPC(t, base, token, header,
				`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_namespaces","arguments":{}}}`)
			var rpc struct {
				Result *struct {
					IsError bool `json:"isError"`
				} `json:"result"`
				Error json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal([]byte(body), &rpc); err != nil ||
				response.StatusCode != http.StatusOK || rpc.Result == nil || rpc.Result.IsError || len(rpc.Error) != 0 {
				t.Fatalf("legacy OAuth tools/call (%s, header %q): %d %s (%v)", version, header, response.StatusCode, body, err)
			}
		}
	}
}

func postMCPRPC(t *testing.T, base, token, version, body string) (*http.Response, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, base+"/_/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if version != "" {
		request.Header.Set("MCP-Protocol-Version", version)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	reply := readE2EBody(t, response)
	if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		var data []string
		for line := range strings.SplitSeq(reply, "\n") {
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, strings.TrimSpace(after))
			}
		}
		reply = strings.Join(data, "\n")
	}
	return response, reply
}

type e2eBearerTransport struct{ token string }

func (t e2eBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(request)
}

func waitReady(t *testing.T, endpoint string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(endpoint)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("server did not become ready at %s", endpoint)
}

func getE2E(t *testing.T, client *http.Client, target string) *http.Response {
	t.Helper()
	response, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func postOAuthForm(t *testing.T, client *http.Client, target string, form url.Values, csrf string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", originFromURL(target))
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func originFromURL(raw string) string {
	parsed, _ := url.Parse(raw)
	return parsed.Scheme + "://" + parsed.Host
}

func captureE2E(t *testing.T, pattern, body string) string {
	t.Helper()
	match := regexp.MustCompile(pattern).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("pattern %q not found in response", pattern)
	}
	return match[1]
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query()
}

func readE2EBody(t *testing.T, response *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func decodeE2EJSON(t *testing.T, response *http.Response, result any) {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, result); err != nil {
		t.Fatalf("decode JSON %q: %v", body, err)
	}
}
