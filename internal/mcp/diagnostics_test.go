package mcp

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPDiagnosticsAreOptInAndExcludeSecrets(t *testing.T) {
	original := slog.Default()
	defer slog.SetDefault(original)
	for _, debug := range []bool{false, true} {
		var logs bytes.Buffer
		level := slog.LevelInfo
		if debug {
			level = slog.LevelDebug
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: level})))
		for _, method := range []string{"server/discover", "private-method-secret"} {
			r := httptest.NewRequestWithContext(t.Context(), "POST", "/_/mcp", strings.NewReader(
				`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":{"secret":"private-body-secret"}}`))
			r.Header.Set("Authorization", "Bearer private-token-secret")
			r.Header.Set("Cookie", "hmd_session=private-cookie-secret")
			r.Header.Set("Mcp-Session-Id", "private-session-secret")
			w := httptest.NewRecorder()
			w.Header().Set("X-Request-ID", "test-correlation")
			mcpProtocolGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(w, r)
		}
		text := logs.String()
		if !debug && text != "" {
			t.Fatal("MCP diagnostics logged while debug disabled")
		}
		if debug && (!strings.Contains(text, "method=server/discover") ||
			!strings.Contains(text, "method=unknown") ||
			!strings.Contains(text, "request_id=test-correlation")) {
			t.Fatalf("missing MCP diagnostics: %s", text)
		}
		if strings.Contains(text, "private-") {
			t.Fatal("MCP diagnostics exposed private data")
		}
	}
}
