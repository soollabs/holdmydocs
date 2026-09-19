package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hmd/internal/auth"
	"hmd/internal/wiki"
)

func principal(scopes ...string) context.Context {
	return auth.WithTokenPrincipal(context.Background(), auth.TokenPrincipal{User: "tester", Scopes: scopes})
}

// No transport is involved: every operation must reject an unauthorised caller
// before touching even its dependencies. This catches accidental reliance on
// HTTP middleware or the MCP tool wrapper.
func TestOperationScopeGates(t *testing.T) {
	a := New(nil, nil, nil)
	operations := []struct {
		name  string
		scope string
		call  func(context.Context) error
	}{
		{"save", "write", func(ctx context.Context) error {
			_, err := a.SavePage(ctx, SavePageInput{Slug: "notes/page"})
			return err
		}},
		{"update", "write", func(ctx context.Context) error {
			_, err := a.UpdatePage(ctx, UpdatePageInput{Slug: "notes/page"})
			return err
		}},
		{"move", "write", func(ctx context.Context) error {
			_, err := a.MovePage(ctx, MovePageInput{FromSlug: "notes/page", ToSlug: "notes/other"})
			return err
		}},
		{"edit", "write", func(ctx context.Context) error {
			_, err := a.EditPage(ctx, EditPageInput{Slug: "notes/page"})
			return err
		}},
		{"tags", "write", func(ctx context.Context) error { _, err := a.SetPageTags(ctx, "notes/page", nil); return err }},
		{"rename", "write", func(ctx context.Context) error {
			_, err := a.RenamePage(ctx, RenamePageInput{Slug: "notes/page"})
			return err
		}},
		{"delete", "write", func(ctx context.Context) error { _, err := a.DeletePage(ctx, "notes/page", false); return err }},
		{"revert", "write", func(ctx context.Context) error { _, err := a.RevertPage(ctx, "notes/page", "hash"); return err }},
		{"issue upload", "write", func(ctx context.Context) error {
			_, err := a.IssueUploadCapability(ctx, "notes/page", "file.txt")
			return err
		}},
		{"upload", "write", func(ctx context.Context) error {
			_, err := a.UploadAttachment(ctx, AttachmentUploadInput{Slug: "notes/page"})
			return err
		}},
		{"push", "write", func(ctx context.Context) error { _, err := a.PushNow(ctx); return err }},
		{"list", "read", func(ctx context.Context) error { _, err := a.ListPages(ctx); return err }},
		{"hidden list", "read", func(ctx context.Context) error { _, err := a.HiddenPages(ctx); return err }},
		{"hidden page", "read", func(ctx context.Context) error { _, err := a.ViewHidden(ctx, "notes/page"); return err }},
		{"search", "read", func(ctx context.Context) error { _, err := a.SearchPages(ctx, "term"); return err }},
		{"attachment search", "read", func(ctx context.Context) error { _, err := a.SearchAttachments(ctx, "term", 1); return err }},
		{"attachment text", "read", func(ctx context.Context) error { _, err := a.ReadAttachment(ctx, "notes/page", "file.txt"); return err }},
		{"health", "read", func(ctx context.Context) error { _, err := a.Health(ctx, ""); return err }},
		{"changes", "read", func(ctx context.Context) error { _, err := a.RecentChanges(ctx, 10); return err }},
		{"history", "read", func(ctx context.Context) error { _, err := a.PageHistory(ctx, "notes/page"); return err }},
		{"revision", "read", func(ctx context.Context) error { _, err := a.RevisionPage(ctx, "notes/page", "hash"); return err }},
		{"diff", "read", func(ctx context.Context) error { _, err := a.PageDiff(ctx, "notes/page", "a", "b"); return err }},
		{"sync", "read", func(ctx context.Context) error { _, err := a.Sync(ctx); return err }},
		{"server settings", "settings", func(ctx context.Context) error { return a.SaveServerSettings(ctx, ServerSettingsInput{}) }},
		{"export config", "settings", a.ExportServerConfig},
		{"wiki settings", "settings", func(ctx context.Context) error { return a.SaveWikiConfig(ctx, WikiConfigInput{}) }},
		{"help", "settings", a.ResetHelp},
		{"appearance", "settings", func(ctx context.Context) error { return a.SetAppearance(ctx, "", "", "", "", false) }},
		{"author", "settings", func(ctx context.Context) error { return a.SetGitAuthor(ctx, "") }},
		{"rerun setup", "settings", a.RerunSetup},
		{"setup", "settings", func(ctx context.Context) error { return a.CompleteFirstRunSetup(ctx, FirstRunSetupInput{}) }},
		{"seed", "settings", func(ctx context.Context) error { return a.SeedFirstNamespace(ctx, "notes") }},
		{"create user", "settings", func(ctx context.Context) error { return a.CreateUser(ctx, "", "", nil) }},
		{"user scopes", "settings", func(ctx context.Context) error { return a.SetUserScopes(ctx, "", nil) }},
		{"create token", "settings", func(ctx context.Context) error { _, err := a.CreateToken(ctx, "", time.Time{}, nil, nil); return err }},
		{"revoke token", "settings", func(ctx context.Context) error { return a.RevokeToken(ctx, "") }},
		{"read namespace", "settings", func(ctx context.Context) error { _, err := a.ReadNamespace(ctx, "notes"); return err }},
		{"save namespace", "settings", func(ctx context.Context) error {
			_, err := a.SaveNamespace(ctx, SaveNamespaceInput{Name: "notes"})
			return err
		}},
		{"reset namespace", "settings", func(ctx context.Context) error { return a.ResetNamespace(ctx, "notes") }},
		{"delete namespace", "settings", func(ctx context.Context) error { return a.DeleteNamespace(ctx, "notes") }},
		{"delete namespace all", "settings", func(ctx context.Context) error { return a.DeleteNamespaceAll(ctx, "notes") }},
		{"export namespace", "settings", func(ctx context.Context) error { return a.ExportNamespace(ctx, "notes", "", nil, "", nil) }},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			if err := op.call(context.Background()); CategoryOf(err) != CategoryUnauthenticated {
				t.Fatalf("anonymous: %v", err)
			}
			// Settings is an administrator scope and implies read and write.
			for _, scope := range []string{"read", "write"} {
				if scope == op.scope {
					continue
				}
				if err := op.call(principal(scope)); CategoryOf(err) != CategoryForbidden {
					t.Errorf("%s-only principal: %v", scope, err)
				}
			}
		})
	}
}

func TestPublicReadsAndRestrictedPrincipals(t *testing.T) {
	a, st := newAttachmentAPI(t)
	a.SetNamespaces(wiki.NamespaceRegistry{"public": {Public: true}, "private": {}})
	for _, slug := range []string{"public/page", "private/page"} {
		if _, err := a.SavePage(writeCtx(), SavePageInput{Slug: slug, Body: "body"}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.UploadAttachment(writeCtx(), AttachmentUploadInput{Slug: slug, Filename: "file.txt", Content: []byte("attachment")}); err != nil {
			t.Fatal(err)
		}
	}
	anon := context.Background()
	if _, err := a.ViewPage(anon, "public/page"); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"private/page", "private/missing"} {
		if _, err := a.ViewPage(anon, slug); CategoryOf(err) != CategoryNotFound {
			t.Fatalf("anonymous %s: %v", slug, err)
		}
		if _, err := a.OpenAttachmentForRead(anon, slug, "file.txt"); CategoryOf(err) != CategoryNotFound {
			t.Fatalf("anonymous attachment %s: %v", slug, err)
		}
	}
	file, err := a.OpenAttachmentForRead(anon, "public/page", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(file)
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || string(body) != "attachment" {
		t.Fatalf("public attachment: %q, %v", body, err)
	}
	if _, err := a.ViewPage(principal("write"), "public/page"); CategoryOf(err) != CategoryForbidden {
		t.Fatalf("public flag widened authenticated scope: %v", err)
	}
	restricted := auth.WithTokenPrincipal(anon, auth.TokenPrincipal{
		User: "tester", Scopes: []string{"read", "write"}, Namespaces: []string{"private"},
	})
	if _, err := a.ViewPage(restricted, "public/page"); CategoryOf(err) != CategoryForbidden {
		t.Fatalf("public flag widened token namespace: %v", err)
	}
	if _, err := a.SavePage(restricted, SavePageInput{Slug: "public/denied"}); CategoryOf(err) != CategoryForbidden {
		t.Fatalf("restricted write: %v", err)
	}
	if _, err := a.IssueUploadCapability(restricted, "public/page", "file.txt"); CategoryOf(err) != CategoryForbidden {
		t.Fatalf("restricted capability: %v", err)
	}
	if _, err := a.Sync(restricted); CategoryOf(err) != CategoryForbidden {
		t.Fatalf("restricted sync: %v", err)
	}
	if _, err := a.PushNow(restricted); CategoryOf(err) != CategoryForbidden {
		t.Fatalf("restricted push: %v", err)
	}
	if _, _, err := st.Read("public/denied.md"); err == nil {
		t.Fatal("denied write reached persistence")
	}
}

func TestSessionAndSettingsPrincipalOperations(t *testing.T) {
	a, _ := newAttachmentAPI(t)
	authn, err := auth.Open(auth.Options{AppDir: t.TempDir(), AdminUser: "admin", AdminPass: "test-password-long"})
	if err != nil {
		t.Fatal(err)
	}
	a.auth = authn
	token, ok := authn.NewSession("admin")
	if !ok {
		t.Fatal("creating session")
	}
	request := httptest.NewRequest(http.MethodPost, "/_/mcp", nil)
	request.AddCookie(&http.Cookie{Name: "hmd_session", Value: token})
	called := false
	authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if _, err := a.SavePage(r.Context(), SavePageInput{Slug: "notes/session"}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.ViewPage(r.Context(), "notes/session"); err != nil {
			t.Fatal(err)
		}
		if _, err := a.SaveNamespace(r.Context(), SaveNamespaceInput{Name: "notes"}); err != nil {
			t.Fatal(err)
		}
	})).ServeHTTP(httptest.NewRecorder(), request)
	if !called {
		t.Fatal("session did not authenticate")
	}
	if _, err := a.ReadNamespace(principal("settings"), "notes"); err != nil {
		t.Fatalf("settings-only principal: %v", err)
	}
}

func TestCapabilityRedemptionOwnsAuthorisation(t *testing.T) {
	a, st := newAttachmentAPI(t)
	grant, err := a.IssueUploadCapability(writeCtx(), "notes/page", "Report.TXT")
	if err != nil {
		t.Fatal(err)
	}
	upload, err := a.RedeemUploadCapability(context.Background(), grant.Token, "Report.TXT", []byte("report"))
	if err != nil || upload.Slug != "notes/page" || upload.Filename != "report.txt" {
		t.Fatalf("redemption: %+v, %v", upload, err)
	}
	if _, err := a.RedeemUploadCapability(context.Background(), grant.Token, "Report.TXT", nil); CategoryOf(err) != CategoryNotFound {
		t.Fatalf("replay: %v", err)
	}
	if err := a.addUploadCapability("expired", UploadCapability{Slug: "notes/page", Filename: "expired.txt", User: "tester", Expires: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RedeemUploadCapability(context.Background(), "expired", "expired.txt", nil); CategoryOf(err) != CategoryNotFound {
		t.Fatalf("expired: %v", err)
	}
	if _, _, err := st.Read("attachments/notes/page/expired.txt"); err == nil {
		t.Fatal("expired capability committed")
	}
}
