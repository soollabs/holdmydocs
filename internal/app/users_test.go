package app

import (
	"io"
	"os"
	"strings"
	"testing"

	"hmd/internal/auth"
	"hmd/internal/config"
)

func TestResetAdmin(t *testing.T) {
	cfg := config.Config{AppDir: t.TempDir()}
	if err := os.Chmod(cfg.AppDir, 0700); err != nil {
		t.Fatal(err)
	}
	password := "a-unique-long-test-password"
	if err := ResetAdmin(cfg, "recovered", strings.NewReader(password+"\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	a, err := auth.Open(auth.Options{AppDir: cfg.AppDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Login("recovered", password); !ok {
		t.Fatal("new account cannot sign in")
	}
	if !a.Prefs("recovered").HasScope(auth.ScopeAdmin) {
		t.Fatal("account lacks admin")
	}
	if err := a.AddUserWithScopes("reader", password, []string{"read"}); err != nil {
		t.Fatal(err)
	}
	next := "another-unique-test-password"
	if err := ResetAdmin(cfg, "reader", strings.NewReader(next+"\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	a, err = auth.Open(auth.Options{AppDir: cfg.AppDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Login("reader", next); !ok {
		t.Fatal("reset password cannot sign in")
	}
	if !a.Prefs("reader").HasScope(auth.ScopeAdmin) {
		t.Fatal("recovered reader lacks admin")
	}
	if _, err := a.EnsureOIDCUser(auth.OIDCIdentity{Issuer: "https://id.example.test", Subject: "one"}, "sso", "", []string{"admin"}); err != nil {
		t.Fatal(err)
	}
	if err := ResetAdmin(cfg, "sso", strings.NewReader(next+"\n"), io.Discard); err == nil || !strings.Contains(err.Error(), "choose another name") {
		t.Fatalf("SSO reset: %v", err)
	}
	unlock, err := auth.LockAppDir(cfg.AppDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unlock() }()
	if err := ResetAdmin(cfg, "reader", strings.NewReader(next+"\n"), io.Discard); err == nil || !strings.Contains(err.Error(), "stop it first") {
		t.Fatalf("locked reset: %v", err)
	}
}
