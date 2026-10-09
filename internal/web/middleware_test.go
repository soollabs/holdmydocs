package web

import (
	"hmd/internal/testhttp"

	"net/http"
	"testing"
)

func TestProbeEndpointsAreUnauthenticated(t *testing.T) {
	app, server, _ := newTestAppFull(t)
	defer server.Close()

	for _, path := range []string{"/_/live", "/_/ready"} {
		resp, err := testhttp.Get(t, http.DefaultClient, server.URL+path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing response body: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
	if !app.Index.Ready() {
		t.Fatal("test index should be ready")
	}
	if err := app.Index.Close(); err != nil {
		t.Fatal(err)
	}
	response, err := testhttp.Get(t, http.DefaultClient, server.URL+"/_/ready")
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readiness after index close = %d, want 503", response.StatusCode)
	}
	response, err = testhttp.Get(t, http.DefaultClient, server.URL+"/_/live")
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("liveness after index close = %d, want 200", response.StatusCode)
	}
}
