package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLoopbackMCPAllowsOnlyConfiguredProxyHost(t *testing.T) {
	for _, configured := range []bool{true, false} {
		base := ""
		if configured {
			base = "https://wiki.example.test"
		}
		s := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "test"}, nil)
		server := httptest.NewServer(newMCPHTTPHandler(base, func(*http.Request) *sdk.Server { return s }))
		for _, host := range []string{"wiki.example.test", "attacker.example.test", "127.0.0.1"} {
			req, _ := http.NewRequestWithContext(t.Context(), "POST", server.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
			req.Host = host
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			allowed := host == "127.0.0.1" || configured && host == "wiki.example.test"
			if allowed && resp.StatusCode != http.StatusOK || !allowed && resp.StatusCode != http.StatusForbidden {
				t.Fatalf("configured=%v host=%s status=%d", configured, host, resp.StatusCode)
			}
		}
		server.Close()
	}
}
