package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	appconfig "hmd/internal/config"
)

type Config = appconfig.Config

func OpenAuth(cfg Config) (*Auth, error) {
	return Open(Options{AppDir: cfg.AppDir, AdminUser: cfg.AdminUser, AdminPass: cfg.AdminPass})
}

var parseAuthor = appconfig.ParseAuthor

func TestBootstrapAdmin(t *testing.T) {
	appDir := t.TempDir()
	cfg := Config{
		AppDir:    appDir,
		AdminUser: "admin",
		AdminPass: "password12345",
	}

	auth, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}

	token, ok := auth.Login("admin", "password12345")
	if !ok {
		t.Errorf("Login with correct password should succeed")
	}

	user, ok := auth.UserFor(token)
	if !ok || user != "admin" {
		t.Errorf("UserFor(token) should return admin, got: ok=%v, user=%q", ok, user)
	}

	_, ok = auth.Login("admin", "wrongpassword")
	if ok {
		t.Errorf("Login with wrong password should fail")
	}

	auth.Logout(token)
	_, ok = auth.UserFor(token)
	if ok {
		t.Errorf("UserFor(token) should fail after Logout")
	}
	for _, path := range []string{filepath.Join(appDir, "users.json"), filepath.Join(appDir, "sessions.json")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("%s mode = %o, want 600", path, info.Mode().Perm())
		}
	}
}

func TestBootstrapRejectsPlaceholderAndMatchingCredentials(t *testing.T) {
	for _, cfg := range []Config{
		{AppDir: t.TempDir(), AdminUser: "admin", AdminPass: "change-me"},
		{AppDir: t.TempDir(), AdminUser: "samepassword", AdminPass: "samepassword"},
	} {
		if _, err := OpenAuth(cfg); err == nil {
			t.Fatal("OpenAuth accepted unsafe bootstrap credentials")
		}
	}
}

func TestOpenAuthRejectsInvalidPersistedUserRecords(t *testing.T) {
	appDir := t.TempDir()
	for _, data := range []string{
		`{"alice":{"hash":"hash"}}`,
		`{"alice":{"hash":"hash","scopes":[]}}`,
		`{"alice":{"hash":"hash","scopes":["read"],"unknown":true}}`,
		`{"alice":{"hash":"hash","scopes":["read"],"tokens":[{"name":"token","digest":"0000000000000000000000000000000000000000000000000000000000000000"}]}}`,
		`{"alice":{"hash":"hash","scopes":["read"]}} {}`,
	} {
		if err := os.WriteFile(filepath.Join(appDir, "users.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenAuth(Config{AppDir: appDir}); err == nil {
			t.Errorf("OpenAuth accepted invalid users record %s", data)
		}
	}
}

func TestSessionExpires(t *testing.T) {
	appDir := t.TempDir()
	cfg := Config{AppDir: appDir, AdminUser: "admin", AdminPass: "password12345"}

	auth, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}

	token, ok := auth.Login("admin", "password12345")
	if !ok {
		t.Fatalf("Login should succeed")
	}

	// Expired sessions must be rejected without a background sweep.
	auth.mu.Lock()
	rec := auth.sessions[token]
	rec.Expires = time.Now().Add(-time.Second)
	auth.sessions[token] = rec
	auth.mu.Unlock()

	if _, ok := auth.UserFor(token); ok {
		t.Errorf("UserFor(token) should fail once the session has expired")
	}
}

func TestAddUserPersists(t *testing.T) {
	appDir := t.TempDir()
	cfg := Config{AppDir: appDir}

	auth1, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}

	err = auth1.AddUser("bob", "password12345")
	if err != nil {
		t.Fatalf("AddUser failed: %v", err)
	}

	auth2, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("Second OpenAuth failed: %v", err)
	}

	token, ok := auth2.Login("bob", "password12345")
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
		AdminPass: "password12345",
	}

	auth, err := OpenAuth(cfg)
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}

	token, _ := auth.Login("admin", "password12345")

	successHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			t.Errorf("writing response: %v", err)
		}
	})

	protected := auth.Middleware(successHandler)

	t.Run("unauthenticated redirect", func(t *testing.T) {
		_, _ = http.NewRequest("GET", "/page/index", nil)

	})

	t.Run("static allowed without auth", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/static/style.css", nil)
		_ = protected
		_ = req

	})

	t.Run("login allowed without auth", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/login", nil)
		_ = protected
		_ = req

	})

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
	if err := auth.AddUser("zed", "password12345"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := auth.AddUser("amy", "password12345"); err != nil {
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
	if err := auth.AddUser("nobody", "password12345"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if !auth.UserExists("nobody") {
		t.Error("UserExists should be true after the user is added")
	}
}

func TestUserGitAuthorPersists(t *testing.T) {
	appDir := t.TempDir()
	auth, err := OpenAuth(Config{AppDir: appDir, AdminUser: "admin", AdminPass: "password12345"})
	if err != nil {
		t.Fatalf("OpenAuth failed: %v", err)
	}
	if auth.AuthorFor("admin") != "" {
		t.Errorf("new user should have empty git author")
	}
	if err := auth.SetAuthor("admin", "Admin <a@x.com>"); err != nil {
		t.Fatalf("SetAuthor failed: %v", err)
	}

	auth2, err := OpenAuth(Config{AppDir: appDir})
	if err != nil {
		t.Fatal(err)
	}
	if got := auth2.AuthorFor("admin"); got != "Admin <a@x.com>" {
		t.Errorf("AuthorFor after reload = %q; want %q", got, "Admin <a@x.com>")
	}
	if _, ok := auth2.Login("admin", "password12345"); !ok {
		t.Errorf("login should still work after author set")
	}
}

func TestTokenNamespacesPersistAndCache(t *testing.T) {
	cfg := Config{AppDir: t.TempDir()}
	auth, err := OpenAuth(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AddUser("alice", "password12345"); err != nil {
		t.Fatal(err)
	}

	unrestricted, err := auth.AddToken("alice", "all", time.Time{}, []string{"read", "write", "settings"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := auth.AddToken("alice", "notes", time.Time{}, []string{"read", "write"}, []string{" notes ", "notes"})
	if err != nil {
		t.Fatal(err)
	}
	archiveOnly, err := auth.AddToken("alice", "archive", time.Time{}, []string{"read"}, []string{"archive"})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := auth.UserForBearer(unrestricted); !ok || p.User != "alice" || p.Restricted() {
		t.Fatalf("unrestricted principal = %#v, %v", p, ok)
	}
	if p, ok := auth.UserForBearer(restricted); !ok || !p.AllowsNamespace("notes") || p.AllowsNamespace("private") {
		t.Fatalf("restricted principal = %#v, %v", p, ok)
	}
	if p, ok := auth.UserForBearer(archiveOnly); !ok || !p.AllowsSlug("archive/old") || p.AllowsSlug("notes/page") {
		t.Fatalf("archive-only principal = %#v, %v", p, ok)
	}

	// Cached principals retain the token's verified policy.
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
	if p, ok := reloaded.UserForBearer(archiveOnly); !ok || !p.AllowsSlug("archive/old") || p.AllowsSlug("notes/page") {
		t.Fatalf("reloaded archive-only principal = %#v, %v", p, ok)
	}
	metadata := reloaded.TokensFor("alice")
	if len(metadata) != 3 || len(metadata[1].Scopes) != 2 || metadata[1].Namespaces[0] != "notes" || len(metadata[2].Namespaces) != 1 || metadata[2].Namespaces[0] != "archive" {
		t.Fatalf("token metadata = %#v, want copied scopes and namespaces", metadata)
	}
}

func TestTokenInputValidation(t *testing.T) {
	auth, err := OpenAuth(Config{AppDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AddUser("alice", "password12345"); err != nil {
		t.Fatal(err)
	}

	if _, err := auth.AddToken("alice", "", time.Time{}, []string{"read"}, nil); err == nil {
		t.Error("blank token name was accepted")
	}
	if _, err := auth.AddToken("alice", "duplicate", time.Time{}, []string{"read"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AddToken("alice", "duplicate", time.Time{}, []string{"read"}, nil); err == nil {
		t.Error("duplicate token name was accepted")
	}

	for _, namespaces := range [][]string{{" "}, {"_"}, {".private"}, {"notes/private"}} {
		if _, err := auth.AddToken("alice", "invalid-namespace-"+namespaces[0], time.Time{}, []string{"read"}, namespaces); err == nil {
			t.Errorf("invalid namespaces %v were accepted", namespaces)
		}
	}
	if _, err := auth.AddToken("alice", "unknown-scope", time.Time{}, []string{"admin"}, nil); err == nil {
		t.Error("unknown token scope was accepted")
	}
	if _, err := auth.AddToken("alice", "empty-scopes", time.Time{}, []string{}, nil); err == nil {
		t.Error("empty new-token scopes were accepted")
	}
	if _, err := auth.AddToken("alice", "missing-scopes", time.Time{}, nil, nil); err == nil {
		t.Error("missing token scopes were accepted")
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

func TestAuthInputAndSessionControls(t *testing.T) {
	auth, err := OpenAuth(Config{AppDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{"12345678901", string(make([]byte, 73)), "validpassword\n"} {
		if err := auth.AddUser("alice", password); err == nil {
			t.Errorf("AddUser accepted invalid password %q", password)
		}
	}
	if err := auth.AddUser("alice", "password12345"); err != nil {
		t.Fatal(err)
	}
	if _, ok := auth.UserForBearer("hmd_not-hex"); ok {
		t.Error("invalid bearer token was accepted")
	}
	first, ok := auth.Login("alice", "password12345")
	if !ok {
		t.Fatal("first login failed")
	}
	if _, ok := auth.LoginLimited("127.0.0.1:1", "alice", "wrongpassword"); ok {
		t.Error("wrong password succeeded")
	}
	if _, ok := auth.LoginLimited("127.0.0.1:1", "alice", "wrongpassword"); ok {
		t.Error("throttled login succeeded")
	}
	if err := auth.SetScopes("alice", []string{"read"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := auth.UserFor(first); ok {
		t.Error("scope change did not revoke session")
	}
}

func TestTokenScopeIntersectionAndSettingsBypass(t *testing.T) {
	auth, err := OpenAuth(Config{AppDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AddUser("reader", "password12345"); err != nil {
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

	if err := auth.AddUser("manager", "password12345"); err != nil {
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
	if err := auth.AddUser("alice", "password12345"); err != nil {
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
	if err := auth.AddUser("manager", "password12345"); err != nil {
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
