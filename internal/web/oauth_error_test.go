package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"hmd/internal/oauthserver"
)

func TestOAuthWebErrorUnwrapsProtocolStatus(t *testing.T) {
	protocol := &oauthserver.ProtocolError{
		Code: "temporarily_unavailable", Description: "try again", Status: http.StatusServiceUnavailable,
	}
	for _, err := range []error{protocol, fmt.Errorf("authorisation failed: %w", protocol)} {
		response := httptest.NewRecorder()
		writeOAuthWebError(response, err)
		if response.Code != protocol.Status {
			t.Fatalf("status = %d, want %d", response.Code, protocol.Status)
		}
	}
}
