package api

import (
	"testing"

	"hmd/internal/config"
	"hmd/internal/store"
)

func TestReadOnlyStoreCannotBeBypassedByConfig(t *testing.T) {
	dir := t.TempDir()
	if _, err := store.Open(store.Options{RepoDir: dir}); err != nil {
		t.Fatal(err)
	}
	reader, err := store.Open(store.Options{RepoDir: dir, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	a := New(reader, nil, nil)
	a.SetConfig(config.Config{ReadOnly: false})
	ctx := principal("settings")
	if !a.HasScope(ctx, ScopeRead) || a.HasScope(ctx, ScopeWrite) || a.HasScope(ctx, ScopeSettings) {
		t.Fatal("read-only policy must cap administrator scopes")
	}
	if _, err := a.RedeemUploadCapability(ctx, "token", "file.txt", nil); CategoryOf(err) != CategoryForbidden {
		t.Fatalf("detached upload = %v", err)
	}
}
