package web

import (
	"net/http"
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
	if err := app.Index.Close(); err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(server.URL + "/_/ready")
	if err != nil {
		t.Fatal(err)
	}
	closeTestBody(t, response.Body)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readiness after index close = %d, want 503", response.StatusCode)
	}
	response, err = http.Get(server.URL + "/_/live")
	if err != nil {
		t.Fatal(err)
	}
	closeTestBody(t, response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("liveness after index close = %d, want 200", response.StatusCode)
	}
}
