package api

import (
	"errors"
	"testing"

	"hmd/internal/auth"
)

func TestUserManagementPermissions(t *testing.T) {
	authn, err := auth.Open(auth.Options{AppDir: t.TempDir(), AdminUser: "admin", AdminPass: "a-unique-long-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	a := New(nil, nil, authn)
	admin := auth.WithTokenPrincipal(t.Context(), auth.TokenPrincipal{User: "admin", Scopes: []string{"admin"}})
	reader := auth.WithTokenPrincipal(t.Context(), auth.TokenPrincipal{User: "reader", Scopes: []string{"read", "preferences"}})
	for _, call := range []func() error{
		func() error { return a.DeleteUser(reader, "admin") },
		func() error { return a.ResetUserPassword(reader, "admin", "another-unique-test-password") },
		func() error { return a.ChangeOwnPassword(reader, "", "another-unique-test-password", "") },
	} {
		var apiErr *Error
		if err := call(); !errors.As(err, &apiErr) || apiErr.Category != CategoryForbidden {
			t.Fatalf("permission error = %v", err)
		}
	}
	if err := a.DeleteUser(admin, "admin"); err == nil {
		t.Fatal("self-delete accepted")
	}
	if err := a.ResetUserPassword(admin, "missing", "another-unique-test-password"); err == nil {
		t.Fatal("unknown user accepted")
	}
	if _, err := authn.EnsureOIDCUser(auth.OIDCIdentity{Issuer: "https://id.example.test", Subject: "one"}, "sso", "", []string{"read"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		err      error
		category Category
	}{
		{a.ResetUserPassword(admin, "missing", "another-unique-test-password"), CategoryNotFound},
		{a.ResetUserPassword(admin, "admin", "short"), CategoryInvalidInput},
		{a.ResetUserPassword(admin, "sso", "another-unique-test-password"), CategoryInvalidInput},
		{a.DeleteUser(admin, "admin"), CategoryInvalidInput},
	} {
		var apiErr *Error
		if !errors.As(tc.err, &apiErr) || apiErr.Category != tc.category {
			t.Fatalf("mutation category = %v, want %v", tc.err, tc.category)
		}
	}
	if err := authn.AddUser("second", "a-unique-long-test-password"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetUserScopes(admin, "second", []string{"read"}); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteUser(admin, "second"); err != nil {
		t.Fatal(err)
	}
	if err := authn.AddUserWithScopes("doomed", "a-unique-long-test-password", []string{"read"}); err != nil {
		t.Fatal(err)
	}
	a.SetOAuthUserRevoker(failedUserRevoker{})
	err = a.DeleteUser(admin, "doomed")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Category != CategoryUnavailable || authn.UserExists("doomed") {
		t.Fatalf("revocation failure must report an already-deleted account: %v", err)
	}
}

type failedUserRevoker struct{}

func (failedUserRevoker) RevokeUserGrants(string) error {
	return errors.New("revocation failed")
}
