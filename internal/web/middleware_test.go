package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeEndpointsAreUnauthenticated(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	for _, path := range []string{"/_/live", "/_/ready"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		closeTestBody(t, resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
	if !app.Index.Ready() {
		t.Fatal("test index should be ready")
	}
}

func TestRecoveryReturnsGenericErrorAndRequestID(t *testing.T) {
	handler := accessLog(recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret panic")
	})))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?token=secret", nil))
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != "internal error" {
		t.Fatalf("recovery response = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing request ID")
	}
}
