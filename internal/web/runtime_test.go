package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateWritableDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := validateWritableDataDir(dir); err != nil {
		t.Fatalf("validateWritableDataDir: %v", err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := validateWritableDataDir(dir); err == nil {
		t.Fatal("validateWritableDataDir accepted a non-private directory")
	}
}

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
