package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hmd/internal/httpmiddleware"
	"hmd/internal/web"
)

// TestStartupGateAnswersBeforeReady pins what a request gets while the server
// is still starting: a browser gets a self-refreshing loading page styled like
// the login page, its stylesheets load, anything else gets a 503 it can retry,
// readiness stays unready and liveness is up. Once Ready is called every
// request reaches the real handler.
func TestStartupGateAnswersBeforeReady(t *testing.T) {
	starting, err := web.StartupHandler()
	if err != nil {
		t.Fatal(err)
	}
	gate := startupGate{starting: httpmiddleware.SecurityHeaders(starting)}
	// serve returns the status, headers and body rather than the
	// *http.Response, so no caller has a response body left to close.
	type reply struct {
		StatusCode int
		Header     http.Header
		Body       string
	}
	serve := func(method, path, accept string) reply {
		t.Helper()
		request := httptest.NewRequestWithContext(t.Context(), method, path, nil)
		if accept != "" {
			request.Header.Set("Accept", accept)
		}
		recorder := httptest.NewRecorder()
		gate.ServeHTTP(recorder, request)
		response := recorder.Result()
		defer func() { _ = response.Body.Close() }()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return reply{StatusCode: response.StatusCode, Header: response.Header, Body: string(data)}
	}

	browser := "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	page := serve(http.MethodGet, "/docs/home", browser)
	if page.StatusCode != http.StatusServiceUnavailable || page.Header.Get("Retry-After") == "" {
		t.Fatalf("browser request = %d Retry-After %q, want 503 with Retry-After", page.StatusCode, page.Header.Get("Retry-After"))
	}
	html := page.Body
	if !strings.Contains(html, `http-equiv="refresh"`) || !strings.Contains(html, "starting") {
		t.Fatalf("loading page does not refresh itself:\n%s", html)
	}
	if !strings.Contains(html, `class="login-card"`) || !strings.Contains(html, "/_/static/style.css?v=") || !strings.Contains(html, "hmd-theme") {
		t.Errorf("loading page lacks the login page's card, stylesheet or theme script:\n%s", html)
	}
	csp := page.Header.Get("Content-Security-Policy")
	_, afterNonce, _ := strings.Cut(csp, "'nonce-")
	nonce, _, _ := strings.Cut(afterNonce, "'")
	// html/template escapes "+" in an attribute, which a browser decodes.
	if nonce == "" || !strings.Contains(strings.ReplaceAll(html, "&#43;", "+"), `nonce="`+nonce+`"`) {
		t.Errorf("theme script nonce does not match CSP %q", csp)
	}
	if style := serve(http.MethodGet, "/_/static/style.css", "text/css,*/*;q=0.1"); style.StatusCode != http.StatusOK || !strings.HasPrefix(style.Header.Get("Content-Type"), "text/css") {
		t.Errorf("stylesheet while starting = %d %q, want 200 text/css", style.StatusCode, style.Header.Get("Content-Type"))
	}
	if got := page.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("loading page Cache-Control = %q, want no-store", got)
	}
	for _, request := range []struct{ method, path, accept string }{
		{http.MethodGet, "/_/api/pages", "application/json"},
		{http.MethodPost, "/_/mcp", "application/json, text/event-stream"},
		{http.MethodPost, "/_/login", browser},
		{http.MethodGet, "/_/ready", ""},
	} {
		response := serve(request.method, request.path, request.accept)
		if response.StatusCode != http.StatusServiceUnavailable || strings.Contains(response.Body, "<html") {
			t.Errorf("%s %s while starting = %d, want a plain 503", request.method, request.path, response.StatusCode)
		}
	}
	if live := serve(http.MethodGet, "/_/live", ""); live.StatusCode != http.StatusOK {
		t.Errorf("liveness while starting = %d, want 200", live.StatusCode)
	}

	gate.Ready(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "real "+r.URL.Path)
	}))
	for _, path := range []string{"/docs/home", "/_/ready", "/_/live"} {
		response := serve(http.MethodGet, path, browser)
		if got := response.Body; response.StatusCode != http.StatusOK || got != "real "+path {
			t.Errorf("GET %s after Ready = %d %q, want the real handler", path, response.StatusCode, got)
		}
	}
}
