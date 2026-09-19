package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckReadiness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_/ready" {
			t.Errorf("path = %q, want /_/ready", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := checkReadinessAt(server.URL + "/_/ready"); err != nil {
		t.Fatalf("checkReadiness: %v", err)
	}
}
