package api

import (
	"context"
	"strings"
	"testing"

	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/search"
	"hmd/internal/store"
	"hmd/internal/wiki"
)

func newMutationAPI(t *testing.T, ix *search.Index) (*API, *store.Store) {
	t.Helper()
	st, err := store.Open(store.Options{RepoDir: t.TempDir(), Git: store.GitOptions{User: "tester"}})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	a := New(st, ix, nil)
	a.SetConfig(config.Config{Git: config.GitConfig{Author: "Tester <tester@hmd.local>"}})
	return a, st
}

func writeCtx() context.Context {
	return auth.WithTokenPrincipal(context.Background(), auth.TokenPrincipal{User: "tester", Scopes: []string{"write", "read"}})
}

// TestSavePageCommitSucceedsWhenIndexFails separates a committed write from a
// derived-index failure: the page is durable, the operation is a success, and
// the failure surfaces only as an index warning.
func TestSavePageCommitSucceedsWhenIndexFails(t *testing.T) {
	ix, err := search.BuildIndex(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.Close(); err != nil {
		t.Fatal(err)
	}
	a, st := newMutationAPI(t, ix)

	mutation, err := a.SavePage(writeCtx(), SavePageInput{Slug: "notes/page", Title: "Page", Body: "body"})
	if err != nil {
		t.Fatalf("SavePage after index failure = %v, want a committed success", err)
	}
	if mutation.Slug != "notes/page" || mutation.BlobHash == "" {
		t.Fatalf("mutation = %+v, want committed slug and hash", mutation)
	}
	if mutation.IndexWarning == "" {
		t.Fatal("mutation has no index warning after an index failure")
	}
	content, _, err := st.Read(wiki.PageFile("notes/page"))
	if err != nil {
		t.Fatalf("reading committed page: %v", err)
	}
	if !strings.Contains(string(content), "body") {
		t.Fatalf("committed content = %q, want the written body", content)
	}
}

// TestSavePagePreCommitConflictDoesNotCommit checks a rejected optimistic write
// leaves the committed revision untouched.
func TestSavePagePreCommitConflictDoesNotCommit(t *testing.T) {
	ix, err := search.BuildIndex(nil)
	if err != nil {
		t.Fatal(err)
	}
	a, st := newMutationAPI(t, ix)
	ctx := writeCtx()
	if _, err := a.SavePage(ctx, SavePageInput{Slug: "notes/page", Title: "Page", Body: "first"}); err != nil {
		t.Fatal(err)
	}
	_, err = a.SavePage(ctx, SavePageInput{Slug: "notes/page", Title: "Page", Body: "second"})
	if err == nil || CategoryOf(err) != CategoryConflict {
		t.Fatalf("create over an existing page = %v, want a conflict", err)
	}
	content, _, err := st.Read(wiki.PageFile("notes/page"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "first") {
		t.Fatalf("content changed after a rejected write: %q", content)
	}
}

// TestUpdatePagePatchesOmittedMetadata verifies patch semantics: an omitted
// metadata field keeps its on-disk value.
func TestUpdatePagePatchesOmittedMetadata(t *testing.T) {
	ix, err := search.BuildIndex(nil)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := newMutationAPI(t, ix)
	ctx := writeCtx()
	created, err := a.SavePage(ctx, SavePageInput{Slug: "notes/page", Title: "Page", Tags: []string{"keep"}, Body: "body", Pin: true})
	if err != nil {
		t.Fatal(err)
	}
	body := "changed"
	if _, err := a.UpdatePage(ctx, UpdatePageInput{Slug: "notes/page", BaseHash: created.BlobHash, Body: &body}); err != nil {
		t.Fatal(err)
	}
	view, err := a.ViewPage(ctx, "notes/page")
	if err != nil {
		t.Fatal(err)
	}
	if view.Title != "Page" || strings.Join(view.Tags, ",") != "keep" || !view.Pin {
		t.Fatalf("patch dropped metadata: %+v", view)
	}
	if view.Body != "changed" {
		t.Fatalf("patch body = %q, want %q", view.Body, "changed")
	}
}

// TestSavePageReplacementResetsOmittedMetadata verifies replacement semantics:
// an omitted metadata field resets to the operation's default.
func TestSavePageReplacementResetsOmittedMetadata(t *testing.T) {
	ix, err := search.BuildIndex(nil)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := newMutationAPI(t, ix)
	ctx := writeCtx()
	created, err := a.SavePage(ctx, SavePageInput{Slug: "notes/page", Title: "Page", Tags: []string{"old"}, Body: "body", Pin: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SavePage(ctx, SavePageInput{Slug: "notes/page", Title: "Page", Body: "body2", BaseHash: created.BlobHash}); err != nil {
		t.Fatal(err)
	}
	view, err := a.ViewPage(ctx, "notes/page")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Tags) != 0 || view.Pin {
		t.Fatalf("replacement kept omitted metadata: %+v", view)
	}
}
