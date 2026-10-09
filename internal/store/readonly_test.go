package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hmd/internal/api"
	"hmd/internal/config"
	storepkg "hmd/internal/store"
)

func TestReadOnlyLocalRepository(t *testing.T) {
	dir := t.TempDir()
	writer, err := storepkg.Open(storepkg.Options{RepoDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Save("notes/page.md", []byte("original"), "add page", "tester", "tester@hmd.local"); err != nil {
		t.Fatal(err)
	}
	reader, err := storepkg.Open(storepkg.Options{RepoDir: dir, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reader.ReadOnly() || reader.NeedsSetup.Load() {
		t.Fatal("read-only repository must not request setup")
	}
	before, hash, err := reader.Read("notes/page.md")
	if err != nil {
		t.Fatal(err)
	}
	operations := map[string]func() error{
		"save": func() error {
			_, err := reader.Save("notes/page.md", []byte("changed"), "edit", "tester", "tester@hmd.local")
			return err
		},
		"checked save": func() error {
			_, err := reader.SaveChecked("notes/page.md", "notes/moved.md", hash, []byte("changed"), "move", "tester", "tester@hmd.local")
			return err
		},
		"bulk save": func() error {
			_, err := reader.SaveAll(map[string][]byte{"notes/page.md": []byte("changed")}, "edit", "tester", "tester@hmd.local")
			return err
		},
		"remove":        func() error { return reader.Remove("notes/page.md", "delete", "tester", "tester@hmd.local") },
		"namespace":     func() error { return reader.DeleteNamespace("notes", "delete", "tester", "tester@hmd.local") },
		"namespace all": func() error { return reader.DeleteNamespaceAll("notes", "delete", "tester", "tester@hmd.local") },
		"remote":        func() error { return reader.UpdateRemote(storepkg.Options{}) },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, storepkg.ErrReadOnly) {
				t.Fatalf("mutation error = %v, want ErrReadOnly", err)
			}
		})
	}
	if state, _ := reader.PushNow(); state != "read-only" {
		t.Fatalf("push state = %q", state)
	}
	after, afterHash, err := reader.Read("notes/page.md")
	if err != nil || string(after) != string(before) || afterHash != hash {
		t.Fatalf("repository changed: %q, %s, %v", after, afterHash, err)
	}
	history, err := reader.History("notes/page.md")
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %v, %v", history, err)
	}
}

func TestReadOnlyCloneAndRefreshWithoutToken(t *testing.T) {
	restore := setFetchThrottle(0)
	defer restore()
	remote := t.TempDir()
	if _, err := initBareRepo(remote); err != nil {
		t.Fatal(err)
	}
	writer, err := storepkg.Open(storepkg.Options{RepoDir: t.TempDir(), Git: storepkg.GitOptions{RemoteURL: remote}})
	if err != nil {
		t.Fatal(err)
	}
	save := func(content string) {
		t.Helper()
		if _, err := writer.Save("notes/page.md", []byte(content), "update page", "tester", "tester@hmd.local"); err != nil {
			t.Fatal(err)
		}
		if err := waitForPushes(writer, 5*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	save("original")
	dir := t.TempDir()
	reader, err := storepkg.Open(storepkg.Options{RepoDir: dir, ReadOnly: true, Git: storepkg.GitOptions{RemoteURL: remote}})
	if err != nil {
		t.Fatal(err)
	}
	save("updated")
	client := api.New(reader, nil, nil)
	client.SetConfig(config.Config{ReadOnly: true, Git: config.GitConfig{RemoteURL: remote}})
	client.RefreshReadOnlyRemote()
	content, _, err := reader.Read("notes/page.md")
	if err != nil || string(content) != "updated" {
		t.Fatalf("refreshed page = %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".help.md")); !os.IsNotExist(err) {
		t.Fatalf("read-only clone must not create setup files: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes/page.md"), []byte("local changes"), 0600); err != nil {
		t.Fatal(err)
	}
	save("upstream changes")
	if _, err := reader.FetchAndFF(); err == nil {
		t.Fatal("refresh must refuse a dirty worktree")
	}
	content, _, err = reader.Read("notes/page.md")
	if err != nil || string(content) != "local changes" {
		t.Fatalf("refresh discarded local changes: %q, %v", content, err)
	}
	if _, err := storepkg.Open(storepkg.Options{RepoDir: dir, ReadOnly: true, Git: storepkg.GitOptions{RemoteURL: "different-remote"}}); !errors.Is(err, storepkg.ErrReadOnly) {
		t.Fatalf("mismatched origin = %v", err)
	}
}

func TestReadOnlyRejectsMissingAndEmptyRepositories(t *testing.T) {
	t.Run("missing local", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "missing")
		_, err := storepkg.Open(storepkg.Options{RepoDir: dir, ReadOnly: true})
		if !errors.Is(err, storepkg.ErrReadOnly) {
			t.Fatalf("open = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
			t.Fatalf("must not initialise a missing repository: %v", err)
		}
	})
	t.Run("empty remote", func(t *testing.T) {
		remote := t.TempDir()
		if _, err := initBareRepo(remote); err != nil {
			t.Fatal(err)
		}
		_, err := storepkg.Open(storepkg.Options{RepoDir: t.TempDir(), ReadOnly: true, Git: storepkg.GitOptions{RemoteURL: remote}})
		if !errors.Is(err, storepkg.ErrReadOnly) {
			t.Fatalf("open = %v", err)
		}
	})
}
