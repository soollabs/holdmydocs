package auth

import (
	"errors"
	"sync"
	"testing"
	"time"
)

const userTestPassword = "a-unique-long-test-password"

func TestValidCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, password string
		want           bool
	}{
		{"alice", userTestPassword, true},
		{"alice", "short", false},
		{"longusername", "LONGUSERNAME", false},
		{"alice", "replace this with your own unique password", false},
		{"alice", "a-password-with\ncontrols", false},
	} {
		if got := validCredentials(tc.name, tc.password); got != tc.want {
			t.Errorf("validCredentials(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestLastAdminGuard(t *testing.T) {
	a, err := Open(Options{AppDir: t.TempDir(), AdminUser: "alice", AdminPass: userTestPassword})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetScopes("alice", []string{"read"}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demotion: %v", err)
	}
	if err := a.AddUserWithScopes("alice", "another-unique-test-password", []string{"read"}); err == nil {
		t.Fatal("duplicate creation bypassed the last-admin guard")
	}
	if err := a.DeleteUser("alice"); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("deletion: %v", err)
	}
	if err := a.AddUser("bob", userTestPassword); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteUser("bob"); err != nil {
		t.Fatalf("deleting one of two administrators: %v", err)
	}
	if err := a.AddUser("bob", userTestPassword); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"alice", "bob"} {
		wg.Go(func() { results <- a.SetScopes(name, []string{"read"}) })
	}
	wg.Wait()
	close(results)
	refused := 0
	for err := range results {
		if errors.Is(err, ErrLastAdmin) {
			refused++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if refused != 1 {
		t.Fatalf("concurrent demotions refused = %d", refused)
	}
}

func TestDeleteUserPersistsAndRevokes(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(Options{AppDir: dir, AdminUser: "admin", AdminPass: userTestPassword})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AddUserWithScopes("alice", userTestPassword, []string{"read"}); err != nil {
		t.Fatal(err)
	}
	token, err := a.AddToken("alice", "test", time.Time{}, []string{"read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := a.Login("alice", userTestPassword)
	if err := a.DeleteUser("alice"); err != nil {
		t.Fatal(err)
	}
	if a.UserExists("alice") {
		t.Fatal("user still exists")
	}
	if _, ok := a.UserFor(session); ok {
		t.Fatal("session still works")
	}
	if _, ok := a.UserForBearer(token); ok {
		t.Fatal("token still works")
	}
	reopened, err := Open(Options{AppDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.UserExists("alice") {
		t.Fatal("deleted user persisted")
	}
	if _, ok := reopened.UserFor(session); ok {
		t.Fatal("deleted session persisted")
	}
	if err := a.DeleteUser("alice"); !errors.Is(err, ErrUnknownUser) {
		t.Fatal(err)
	}
}

func TestPasswordChanges(t *testing.T) {
	a, err := Open(Options{AppDir: t.TempDir(), AdminUser: "alice", AdminPass: userTestPassword})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := a.Login("alice", userTestPassword)
	// Seed a second session to test future multiple-session support.
	a.sessions["other"] = sessionRecord{User: "alice", Expires: time.Now().Add(time.Hour)}
	next := "another-unique-test-password"
	if err := a.ChangePassword("alice", userTestPassword, userTestPassword, session); !errors.Is(err, ErrSamePassword) {
		t.Fatal(err)
	}
	if err := a.ChangePassword("alice", userTestPassword, "short", session); !errors.Is(err, ErrInvalidPassword) {
		t.Fatal(err)
	}
	if err := a.ChangePassword("alice", userTestPassword, next, session); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.UserFor(session); !ok {
		t.Fatal("current session revoked")
	}
	if _, ok := a.UserFor("other"); ok {
		t.Fatal("other session survived")
	}
	if err := a.SetPassword("alice", userTestPassword); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.UserFor(session); ok {
		t.Fatal("admin reset kept session")
	}
	if err := a.ChangePassword("alice", "wrong-password", next, ""); !errors.Is(err, ErrWrongPassword) {
		t.Fatal(err)
	}
	if err := a.ChangePassword("alice", "wrong-password", next, ""); !errors.Is(err, ErrPasswordThrottled) {
		t.Fatal(err)
	}
	if _, err := a.EnsureOIDCUser(OIDCIdentity{Issuer: "https://id.example.test", Subject: "one"}, "sso", "", []string{"read"}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetPassword("sso", next); !errors.Is(err, ErrSSOUser) {
		t.Fatal(err)
	}
	if err := a.ChangePassword("sso", "", next, ""); !errors.Is(err, ErrSSOUser) {
		t.Fatal(err)
	}
}

func TestPasswordResetRejectsStaleLoginAndKeepsTokens(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(Options{AppDir: dir, AdminUser: "alice", AdminPass: userTestPassword})
	if err != nil {
		t.Fatal(err)
	}
	oldHash := a.Prefs("alice").Hash
	token, err := a.AddToken("alice", "keep", time.Time{}, []string{"read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := "another-unique-test-password"
	if err := a.SetPassword("alice", next); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.newSessionForHash("alice", oldHash); ok {
		t.Fatal("stale password check created a session after reset")
	}
	if _, ok := a.UserForBearer(token); !ok {
		t.Fatal("password reset revoked a personal token")
	}
	reopened, err := Open(Options{AppDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Login("alice", next); !ok {
		t.Fatal("new password not persisted")
	}
	if _, ok := reopened.Login("alice", userTestPassword); ok {
		t.Fatal("old password still works")
	}
}

func TestLockAppDir(t *testing.T) {
	dir := t.TempDir()
	unlock, err := LockAppDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if release, err := LockAppDir(dir); err == nil {
		_ = release()
		t.Fatal("second lock succeeded")
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	release, err := LockAppDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}
