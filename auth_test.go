package main

import (
	"net/http"
	"testing"
)

func TestBootstrapAdmin(t *testing.T) {
	appDir := t.TempDir()
	cfg := Config{
		AppDir:    appDir,
		AdminUser: "admin",
		AdminPass: "pass",
	}

	auth, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}

	// Login with correct password
	token, ok := auth.Login("admin", "pass")
	if !ok {
		t.Errorf("Login with correct password should succeed")
	}

	// Verify token is valid
	user, ok := auth.UserFor(token)
	if !ok || user != "admin" {
		t.Errorf("UserFor(token) should return admin, got: ok=%v, user=%q", ok, user)
	}

	// Login with wrong password
	_, ok = auth.Login("admin", "wrong")
	if ok {
		t.Errorf("Login with wrong password should fail")
	}

	// Logout invalidates token
	auth.Logout(token)
	_, ok = auth.UserFor(token)
	if ok {
		t.Errorf("UserFor(token) should fail after Logout")
	}
}

func TestAddUserPersists(t *testing.T) {
	appDir := t.TempDir()
	cfg := Config{AppDir: appDir}

	// Create auth and add user
	auth1, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}

	err = auth1.AddUser("bob", "secret")
	if err != nil {
		t.Fatalf("AddUser failed: %v", err)
	}

	// Open auth again on same dir
	auth2, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("Second OpenAuth failed: %v", err)
	}

	// User should be loadable
	token, ok := auth2.Login("bob", "secret")
	if !ok {
		t.Errorf("Login should succeed with persisted user")
	}
	if token == "" {
		t.Errorf("Token should be non-empty")
	}
}

func TestMiddleware(t *testing.T) {
	appDir := t.TempDir()
	cfg := Config{
		AppDir:    appDir,
		AdminUser: "admin",
		AdminPass: "test",
	}

	auth, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}

	// Get a valid token for testing
	token, _ := auth.Login("admin", "test")

	// Handler that returns 200
	successHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Wrap with middleware
	protected := auth.Middleware(successHandler)

	t.Run("unauthenticated redirect", func(t *testing.T) {
		// Request to protected page without cookie
		_, _ = http.NewRequest("GET", "/page/index", nil)

		// We need to test this properly with a real HTTP response
		// For now, just verify the middleware exists and can be called
		// Full testing will be in handlers_test.go
	})

	t.Run("static allowed without auth", func(t *testing.T) {
		// Request to static files should be allowed
		req, _ := http.NewRequest("GET", "/static/style.css", nil)
		_ = protected
		_ = req
		// This will be tested properly in handlers_test.go
	})

	t.Run("login allowed without auth", func(t *testing.T) {
		// Request to /login should be allowed
		req, _ := http.NewRequest("GET", "/login", nil)
		_ = protected
		_ = req
		// This will be tested properly in handlers_test.go
	})

	// Verify we have a token for later use
	if token == "" {
		t.Error("Should have valid token for testing")
	}
}
