package store

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// patchPaths is the previous implementation of changedPaths, kept as the
// reference: it lists paths through a full rename-detecting patch.
func patchPaths(t *testing.T, oldCommit, newCommit *object.Commit) []string {
	t.Helper()
	patch, err := oldCommit.Patch(newCommit)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	var changed []string
	for _, fp := range patch.FilePatches() {
		from, to := fp.Files()
		if from != nil {
			changed = append(changed, from.Path())
		}
		if to != nil && (from == nil || from.Path() != to.Path()) {
			changed = append(changed, to.Path())
		}
	}
	return changed
}

// TestChangedPathsMatchesPatchListing ensures listing paths from a tree diff
// reports the same paths as the patch-based listing it replaced, including
// renames (with and without edits), deletes, adds, moves between directories
// and binary content.
func TestChangedPathsMatchesPatchListing(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	remove := func(path string) {
		t.Helper()
		if err := os.Remove(filepath.Join(dir, path)); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(msg string) *object.Commit {
		t.Helper()
		if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			t.Fatal(err)
		}
		hash, err := wt.Commit(msg, &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Unix(1700000000, 0)}})
		if err != nil {
			t.Fatal(err)
		}
		c, err := repo.CommitObject(hash)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	long := "line one\nline two\nline three\nline four\nline five\nline six\n"
	write("a/keep.md", "unchanged\n")
	write("a/edit.md", long)
	write("a/rename.md", long+"renamed body\n")
	write("a/renamed-edit.md", long+"another body\n")
	write("a/delete.md", "to delete\n")
	write("a/move.md", long+"moving\n")
	write("bin/blob.bin", "\x00\x01\x02\x03")
	base := commit("base")

	write("a/edit.md", long+"edited\n")
	remove("a/rename.md")
	write("b/renamed-now.md", long+"renamed body\n")
	remove("a/renamed-edit.md")
	write("a/renamed-edit-2.md", long+"another body, changed\n")
	remove("a/delete.md")
	remove("a/move.md")
	write("c/deeper/move.md", long+"moving\n")
	write("a/new.md", "brand new\n")
	write("bin/blob.bin", "\x00\x01\x02\x04")
	head := commit("changes")

	got, err := changedPaths(base, head)
	if err != nil {
		t.Fatal(err)
	}
	want := patchPaths(t, base, head)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("changedPaths = %v, want %v", got, want)
	}
	if len(got) == 0 {
		t.Fatal("no changed paths found; fixture did not change anything")
	}

	// No changes between a commit and itself.
	if same, err := changedPaths(head, head); err != nil || len(same) != 0 {
		t.Errorf("changedPaths(head, head) = %v, %v; want none", same, err)
	}
}
