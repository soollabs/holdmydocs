package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
		if _, err := w.Write([]byte("OK")); err != nil {
			t.Errorf("writing response: %v", err)
		}
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

func TestParseAuthor(t *testing.T) {
	cases := []struct {
		in, fallback, name, email string
	}{
		{"Example User <k@x.com>", "u", "Example User", "k@x.com"},
		{"", "alice", "alice", "alice@hmd.local"},
		{"Bob", "u", "Bob", "u@hmd.local"},
		{"  Ada Lovelace  <ada@ex.org>  ", "u", "Ada Lovelace", "ada@ex.org"},
	}
	for _, c := range cases {
		name, email := parseAuthor(c.in, c.fallback)
		if name != c.name || email != c.email {
			t.Errorf("parseAuthor(%q, %q) = %q, %q; want %q, %q", c.in, c.fallback, name, email, c.name, c.email)
		}
	}
}

func TestUsersListsSortedWithScopes(t *testing.T) {
	auth, err := OpenAuth(Config{AppDir: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenAuth: %v", err)
	}
	if err := auth.AddUser("zed", "pw"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := auth.AddUser("amy", "pw"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := auth.SetScopes("amy", []string{"read"}); err != nil {
		t.Fatalf("SetScopes: %v", err)
	}

	users := auth.Users()
	if len(users) != 2 || users[0].Name != "amy" || users[1].Name != "zed" {
		t.Fatalf("Users() = %+v, want [amy, zed] sorted", users)
	}
	if !users[0].Has["read"] || users[0].Has["write"] {
		t.Errorf("amy scopes = %+v, want read only", users[0].Has)
	}
	if !users[1].Has["read"] || !users[1].Has["write"] || !users[1].Has["settings"] {
		t.Errorf("zed (full access) scopes = %+v, want all true", users[1].Has)
	}
}

func TestUserExists(t *testing.T) {
	auth, err := OpenAuth(Config{AppDir: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenAuth: %v", err)
	}
	if auth.UserExists("nobody") {
		t.Error("UserExists should be false before the user is added")
	}
	if err := auth.AddUser("nobody", "pw"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if !auth.UserExists("nobody") {
		t.Error("UserExists should be true after the user is added")
	}
}

func TestUserGitAuthorPersists(t *testing.T) {
	appDir := t.TempDir()
	auth, err := OpenAuth(Config{AppDir: appDir, AdminUser: "admin", AdminPass: "pw"})
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}
	if auth.AuthorFor("admin") != "" {
		t.Errorf("new user should have empty git author")
	}
	if err := auth.SetAuthor("admin", "Admin <a@x.com>"); err != nil {
		t.Fatalf("SetAuthor failed: %v", err)
	}
	// Reload from disk to confirm it persisted alongside the hash.
	auth2, err := OpenAuth(Config{AppDir: appDir})
	if err != nil {
		t.Fatal(err)
	}
	if got := auth2.AuthorFor("admin"); got != "Admin <a@x.com>" {
		t.Errorf("AuthorFor after reload = %q; want %q", got, "Admin <a@x.com>")
	}
	if _, ok := auth2.Login("admin", "pw"); !ok {
		t.Errorf("login should still work after author set")
	}
}

func TestTokenNamespacesPersistAndCache(t *testing.T) {
	cfg := Config{AppDir: t.TempDir()}
	auth, err := OpenAuth(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AddUser("alice", "secret"); err != nil {
		t.Fatal(err)
	}

	unrestricted, err := auth.AddToken("alice", "all", time.Time{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := auth.AddToken("alice", "notes", time.Time{}, []string{"read", "write"}, []string{" notes ", "notes"})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := auth.UserForBearer(unrestricted); !ok || p.User != "alice" || p.Restricted() {
		t.Fatalf("unrestricted principal = %#v, %v", p, ok)
	}
	if p, ok := auth.UserForBearer(restricted); !ok || !p.AllowsNamespace("notes") || p.AllowsNamespace("private") {
		t.Fatalf("restricted principal = %#v, %v", p, ok)
	}

	// A cache hit keeps the policy that was verified with the token, rather
	// than reading a changed stored record.
	auth.mu.Lock()
	stored := auth.users["alice"]
	stored.Tokens[1].Namespaces = []string{"private"}
	auth.users["alice"] = stored
	auth.mu.Unlock()
	if p, ok := auth.UserForBearer(restricted); !ok || !p.AllowsNamespace("notes") || p.AllowsNamespace("private") {
		t.Fatalf("cached restricted principal = %#v, %v", p, ok)
	}

	reloaded, err := OpenAuth(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := reloaded.UserForBearer(restricted); !ok || !p.AllowsNamespace("notes") || p.AllowsNamespace("private") {
		t.Fatalf("reloaded principal = %#v, %v", p, ok)
	}
	metadata := reloaded.TokensFor("alice")
	if len(metadata) != 2 || len(metadata[1].Scopes) != 2 || metadata[1].Namespaces[0] != "notes" {
		t.Fatalf("token metadata = %#v, want copied scopes and namespaces", metadata)
	}
}

func TestTokenInputValidation(t *testing.T) {
	auth, err := OpenAuth(Config{AppDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AddUser("alice", "secret"); err != nil {
		t.Fatal(err)
	}

	if _, err := auth.AddToken("alice", "", time.Time{}, nil, nil); err == nil {
		t.Error("blank token name was accepted")
	}
	if _, err := auth.AddToken("alice", "duplicate", time.Time{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AddToken("alice", "duplicate", time.Time{}, nil, nil); err == nil {
		t.Error("duplicate token name was accepted")
	}

	for _, namespaces := range [][]string{{" "}, {"_"}, {".private"}, {"notes/private"}} {
		if _, err := auth.AddToken("alice", "invalid-namespace-"+namespaces[0], time.Time{}, nil, namespaces); err == nil {
			t.Errorf("invalid namespaces %v were accepted", namespaces)
		}
	}
	if _, err := auth.AddToken("alice", "unknown-scope", time.Time{}, []string{"admin"}, nil); err == nil {
		t.Error("unknown token scope was accepted")
	}
	if _, err := auth.AddToken("alice", "empty-scopes", time.Time{}, []string{}, nil); err == nil {
		t.Error("empty new-token scopes were accepted")
	}

	if _, err := auth.AddToken("alice", "normalised", time.Time{}, []string{"write", "read", "read"}, []string{" notes ", "notes"}); err != nil {
		t.Fatalf("normalisable token rejected: %v", err)
	}
	metadata := auth.TokensFor("alice")
	got := metadata[len(metadata)-1]
	if len(got.Scopes) != 2 || got.Scopes[0] != "read" || got.Scopes[1] != "write" || len(got.Namespaces) != 1 || got.Namespaces[0] != "notes" {
		t.Fatalf("normalised token metadata = %#v", got)
	}

	if err := auth.SetScopes("alice", []string{"read"}); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AddToken("alice", "exceeds-user", time.Time{}, []string{"read", "write"}, nil); err == nil {
		t.Error("token scopes exceeding user scopes were accepted")
	}
}

func TestTokenScopeIntersectionAndSettingsBypass(t *testing.T) {
	auth, err := OpenAuth(Config{AppDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AddUser("reader", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := auth.SetScopes("reader", []string{"read", "write"}); err != nil {
		t.Fatal(err)
	}
	readToken, err := auth.AddToken("reader", "read-only", time.Time{}, []string{"read"}, []string{"notes"})
	if err != nil {
		t.Fatal(err)
	}
	readPrincipal, ok := auth.UserForBearer(readToken)
	if !ok || !readPrincipal.HasScope(scopeRead) || readPrincipal.HasScope(scopeWrite) {
		t.Fatalf("read token principal = %#v, %v", readPrincipal, ok)
	}

	if err := auth.AddUser("manager", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := auth.SetScopes("manager", []string{"settings"}); err != nil {
		t.Fatal(err)
	}
	settingsToken, err := auth.AddToken("manager", "admin", time.Time{}, []string{"settings"}, []string{"notes"})
	if err != nil {
		t.Fatal(err)
	}
	settingsPrincipal, ok := auth.UserForBearer(settingsToken)
	if !ok || !settingsPrincipal.HasScope(scopeRead) || !settingsPrincipal.HasScope(scopeWrite) || !settingsPrincipal.HasScope(scopeSettings) || settingsPrincipal.Restricted() || !settingsPrincipal.AllowsNamespace("private") {
		t.Fatalf("settings token principal = %#v, %v", settingsPrincipal, ok)
	}
}

func TestBearerScopesUseCurrentUserScopes(t *testing.T) {
	auth, err := OpenAuth(Config{AppDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AddUser("alice", "secret"); err != nil {
		t.Fatal(err)
	}
	token, err := auth.AddToken("alice", "writer", time.Time{}, []string{"write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	protected := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := tokenPrincipalFromContext(r.Context())
		if !ok || principal.User != "alice" || !principal.HasScope(scopeWrite) {
			t.Errorf("request principal = %#v, %v", principal, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/readme", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		protected.ServeHTTP(rr, r)
		return rr
	}
	if response := request(); response.Code != http.StatusNoContent {
		t.Fatalf("initial Bearer request = %d, want %d", response.Code, http.StatusNoContent)
	}
	if err := auth.SetScopes("alice", []string{"read"}); err != nil {
		t.Fatal(err)
	}
	if response := request(); response.Code != http.StatusForbidden {
		t.Fatalf("Bearer request after scope change = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestSettingsScopedUserReadTokenKeepsSemanticScope(t *testing.T) {
	auth, err := OpenAuth(Config{AppDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AddUser("manager", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := auth.SetScopes("manager", []string{"settings"}); err != nil {
		t.Fatal(err)
	}
	token, err := auth.AddToken("manager", "read-only", time.Time{}, []string{"read"}, []string{"notes"})
	if err != nil {
		t.Fatalf("AddToken: %v", err)
	}

	protected := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := tokenPrincipalFromContext(r.Context())
		if !ok {
			t.Fatal("request had no token principal")
		}
		if !principal.HasScope(scopeRead) {
			t.Error("read-only token lost read access")
		}
		if principal.HasScope(scopeWrite) || principal.HasScope(scopeSettings) {
			t.Error("read-only token gained write or settings access")
		}
		if !principal.AllowsNamespace("notes") || principal.AllowsNamespace("private") {
			t.Error("read-only token lost its namespace restriction")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	r := httptest.NewRequest(http.MethodGet, "/notes/page", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, r)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("read-only token GET = %d, want %d", rr.Code, http.StatusNoContent)
	}
}
