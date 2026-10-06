package web

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"hmd/internal/httpmiddleware"
	"hmd/internal/oauthserver"
)

// Opt-in real-browser test: HMD_PLAYWRIGHT_MODULE points at an installed
// playwright-core module. All users, registrations and content are disposable.
func TestOAuthBrowserUI(t *testing.T) {
	if os.Getenv("HMD_PLAYWRIGHT_MODULE") == "" {
		t.Skip("set HMD_PLAYWRIGHT_MODULE to run the Chromium OAuth UI checks")
	}
	app, initial, _ := newTestAppFull(t)
	initial.Close()
	store, err := oauthserver.OpenStore(app.config().AppDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client, err := store.ProvisionClient("Example documentation assistant",
		[]string{"https://client.example.test/callback"}, "none", []string{"read", "write", "settings"}, true)
	if err != nil {
		t.Fatal(err)
	}
	app.OAuth, err = oauthserver.NewService(oauthserver.ServerOptions{
		Issuer: "https://wiki.example.test", AllowAdminDelegation: true,
	}, store, app.Auth)
	if err != nil {
		t.Fatal(err)
	}
	security := app.Security()
	server := httptest.NewServer(httpmiddleware.SecurityHeaders(httpmiddleware.Compression(app.Auth.Middleware(security.Handler(app.Routes())))))
	defer server.Close()
	script, err := filepath.Abs("../../test/ui/oauth.browser.cjs")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "node", script)
	cmd.Env = append(os.Environ(), "HMD_UI_URL="+server.URL, "HMD_UI_CLIENT_ID="+client.ClientID)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("OAuth browser checks: %v\n%s", err, output)
	}
	t.Log(string(output))
}
