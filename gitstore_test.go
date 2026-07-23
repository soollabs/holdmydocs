package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func initBareRepo(dir string) (*git.Repository, error) {
	return git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		Bare: true,
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.NewBranchReferenceName("main"),
		},
	})
}

func TestInitNoRemote(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		RepoDir:      tmpDir,
		AppDir:       t.TempDir(),
		Git:          GitConfig{User: "test"},
		HomeFilename: "readme.md",
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	// Nothing is written without consent — a fresh repo flags for setup,
	// it does not auto-seed readme.md or .help.md.
	if !store.NeedsSetup.Load() {
		t.Errorf("NeedsSetup should be true for a fresh repo — nothing is auto-seeded")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "readme.md")); err == nil {
		t.Errorf("readme.md should not be auto-seeded")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".help.md")); err == nil {
		t.Errorf(".help.md should not be auto-seeded")
	}

	// Check sync state
	state, _ := store.SyncState()
	if state != "no remote" {
		t.Errorf("SyncState = %q, want %q", state, "no remote")
	}
}

func TestHomeNotTouchedWhenPresent(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		RepoDir:      tmpDir,
		AppDir:       t.TempDir(),
		Git:          GitConfig{User: "test"},
		HomeFilename: "readme.md",
	}

	homePath := filepath.Join(tmpDir, "readme.md")
	custom := []byte("---\ntitle: Home\ntags: \n---\n\n# My custom home\n")
	if err := os.WriteFile(homePath, custom, 0644); err != nil {
		t.Fatalf("writing custom readme.md: %v", err)
	}
	helpPath := filepath.Join(tmpDir, ".help.md")
	if err := os.WriteFile(helpPath, []byte("---\ntitle: Help\n---\n\n# My custom help\n"), 0644); err != nil {
		t.Fatalf("writing custom .help.md: %v", err)
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	if store.NeedsSetup.Load() {
		t.Errorf("NeedsSetup should be false when both readme.md and .help.md already exist")
	}
	after, err := os.ReadFile(homePath)
	if err != nil {
		t.Fatalf("reading readme.md after open: %v", err)
	}
	if string(after) != string(custom) {
		t.Error("readme.md was overwritten on open; existing files must not be touched")
	}
}

func TestExistingRepoWithContentNeedsSetup(t *testing.T) {
	// Pre-create a git repo with a commit (content but no readme.md).
	tmpDir := t.TempDir()
	repo, err := git.PlainInit(tmpDir, false)
	if err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree failed: %v", err)
	}
	// Write and commit an existing page so the repo has content.
	existingPage := filepath.Join(tmpDir, "existing-page.md")
	if err := os.WriteFile(existingPage, []byte("# Existing\n"), 0644); err != nil {
		t.Fatalf("writing existing page: %v", err)
	}
	wt.Add("existing-page.md")
	wt.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@hmd.local", When: time.Now()},
	})

	cfg := Config{
		RepoDir:      tmpDir,
		AppDir:       t.TempDir(),
		Git:          GitConfig{User: "test"},
		HomeFilename: "readme.md",
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	// Existing repo with content but no readme.md should flag for setup.
	if !store.NeedsSetup.Load() {
		t.Errorf("NeedsSetup should be true for existing repo missing readme.md")
	}

	// readme.md and .help.md must NOT have been auto-seeded.
	if _, err := os.Stat(filepath.Join(tmpDir, "readme.md")); err == nil {
		t.Errorf("readme.md should not be auto-seeded on existing repo with content")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".help.md")); err == nil {
		t.Errorf(".help.md should not be auto-seeded on existing repo with content")
	}
}

func TestEmptyRepoNeedsSetup(t *testing.T) {
	// Pre-create an empty git repo (git init, no commits, no files).
	tmpDir := t.TempDir()
	if _, err := git.PlainInit(tmpDir, false); err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	cfg := Config{
		RepoDir:      tmpDir,
		AppDir:       t.TempDir(),
		Git:          GitConfig{User: "test"},
		HomeFilename: "readme.md",
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	// Empty repo (no commits) still requires consent — no auto-seeding.
	if !store.NeedsSetup.Load() {
		t.Errorf("NeedsSetup should be true for empty repo — nothing is auto-seeded")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "readme.md")); err == nil {
		t.Errorf("readme.md should not be auto-seeded on empty repo")
	}
}

func TestSaveCommitHistoryRevertFlow(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		RepoDir: tmpDir,
		AppDir:  t.TempDir(),
		Git:     GitConfig{User: "test"},
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	// Save twice
	hash1, err := store.Save("test.md", []byte("v1"), "first", "alice", "alice@hmd.local")
	if err != nil {
		t.Fatalf("Save v1 failed: %v", err)
	}

	hash2, err := store.Save("test.md", []byte("v2"), "second", "alice", "alice@hmd.local")
	if err != nil {
		t.Fatalf("Save v2 failed: %v", err)
	}

	if hash1 == hash2 {
		t.Errorf("Hashes should be different for different content")
	}

	// Read returns v2 with hash2
	content, hash, err := store.Read("test.md")
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if string(content) != "v2" {
		t.Errorf("Content = %q, want %q", string(content), "v2")
	}
	if hash != hash2 {
		t.Errorf("Hash = %q, want %q", hash, hash2)
	}

	// History has 2 entries, newest first
	history, err := store.History("test.md")
	if err != nil {
		t.Fatalf("History failed: %v", err)
	}
	if len(history) != 2 {
		t.Errorf("History length = %d, want 2", len(history))
	}
	if history[0].Message != "second" || history[0].Author != "alice" {
		t.Errorf("First history entry: %+v", history[0])
	}
	if history[1].Message != "first" {
		t.Errorf("Second history entry message: %q, want %q", history[1].Message, "first")
	}

	// FileAt with older hash returns v1
	oldContent, err := store.FileAt("test.md", history[1].Hash)
	if err != nil {
		t.Fatalf("FileAt failed: %v", err)
	}
	if string(oldContent) != "v1" {
		t.Errorf("Old content = %q, want %q", string(oldContent), "v1")
	}

	// Revert: save v1 again
	_, err = store.Save("test.md", []byte("v1"), "revert", "alice", "alice@hmd.local")
	if err != nil {
		t.Fatalf("Revert save failed: %v", err)
	}

	// History now has 3 entries
	history, err = store.History("test.md")
	if err != nil {
		t.Fatalf("History after revert failed: %v", err)
	}
	if len(history) != 3 {
		t.Errorf("History length after revert = %d, want 3", len(history))
	}

	// Read returns v1
	content, _, err = store.Read("test.md")
	if err != nil {
		t.Fatalf("Read after revert failed: %v", err)
	}
	if string(content) != "v1" {
		t.Errorf("Content after revert = %q, want %q", string(content), "v1")
	}
}

// TestSaveCheckedConcurrentSameBasehash reproduces the race the old
// handleSave had: two writers both read the same starting hash, then both
// try to save against it. Only one may win; the other must see ErrConflict,
// never a silent overwrite.
func TestSaveCheckedConcurrentSameBasehash(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		RepoDir: tmpDir,
		AppDir:  t.TempDir(),
		Git:     GitConfig{User: "test"},
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	baseHash, err := store.Save("test.md", []byte("v0"), "create", "alice", "alice@hmd.local")
	if err != nil {
		t.Fatalf("initial Save failed: %v", err)
	}

	const writers = 8
	var wg sync.WaitGroup
	results := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.SaveChecked("test.md", "test.md", baseHash,
				[]byte(fmt.Sprintf("v-from-%d", i)), "concurrent update", "alice", "alice@hmd.local")
			results[i] = err
		}(i)
	}
	wg.Wait()

	successes, conflicts := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Errorf("successes = %d, want exactly 1", successes)
	}
	if conflicts != writers-1 {
		t.Errorf("conflicts = %d, want %d", conflicts, writers-1)
	}
}

func TestPushToLocalBareRemote(t *testing.T) {
	// Create a bare remote repo
	bareDir := t.TempDir()
	_, err := initBareRepo(bareDir)
	if err != nil {
		t.Fatalf("Failed to init bare repo: %v", err)
	}

	repoDir := t.TempDir()
	cfg := Config{
		RepoDir: repoDir,
		AppDir:  t.TempDir(),
		Git:     GitConfig{RemoteURL: bareDir, User: "test"},
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	// Save a page
	_, err = store.Save("page.md", []byte("content"), "add page", "bob", "bob@hmd.local")
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Poll until sync state is "ok" (max 2s)
	start := time.Now()
	for time.Since(start) < 2*time.Second {
		state, _ := store.SyncState()
		if state == "ok" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	state, _ := store.SyncState()
	if state != "ok" {
		t.Errorf("SyncState = %q, want %q", state, "ok")
	}

	// Open the bare repo and check HEAD contains the file
	bareRepo, err := git.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("Failed to open bare repo: %v", err)
	}

	ref, err := bareRepo.Head()
	if err != nil {
		t.Fatalf("Failed to get HEAD: %v", err)
	}

	commit, err := bareRepo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("Failed to get commit: %v", err)
	}

	_, err = commit.File("page.md")
	if err != nil {
		t.Errorf("File not found in remote: %v", err)
	}
}

func TestPushFailureDoesNotBlockSave(t *testing.T) {
	// Create a bare remote repo
	bareDir := t.TempDir()
	_, err := initBareRepo(bareDir)
	if err != nil {
		t.Fatalf("Failed to init bare repo: %v", err)
	}

	repoDir := t.TempDir()
	cfg := Config{
		RepoDir: repoDir,
		AppDir:  t.TempDir(),
		Git:     GitConfig{RemoteURL: bareDir, User: "test"},
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	// Delete the bare repo to cause push to fail
	os.RemoveAll(bareDir)

	// Save should still succeed
	_, err = store.Save("page.md", []byte("content"), "add page", "bob", "bob@hmd.local")
	if err != nil {
		t.Errorf("Save should succeed even if push fails: %v", err)
	}

	// History should grow
	history, err := store.History("page.md")
	if err != nil {
		t.Fatalf("History failed: %v", err)
	}
	if len(history) == 0 {
		t.Errorf("History should have entries after save")
	}

	// Poll until sync state reports "failed"
	start := time.Now()
	for time.Since(start) < 2*time.Second {
		state, detail := store.SyncState()
		if state == "failed" {
			if detail == "" {
				t.Errorf("Detail should be non-empty for failed push")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	state, detail := store.SyncState()
	if state != "failed" {
		t.Errorf("SyncState should eventually report failed, got %q (detail: %s)", state, detail)
	}
}

// TestPushTimeoutReleasesLock reproduces the "app freezes" scenario: a
// remote that accepts the connection but never responds. Without a bounded
// context, push() would hold s.mu until the OS-level TCP timeout (minutes),
// blocking every other Store operation. SyncState() itself needs s.mu, so
// polling it here also proves the lock gets released.
func TestPushTimeoutReleasesLock(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	// Unblock the handler before srv.Close() (deferred above, so it runs
	// first) waits for it to return — defers execute in LIFO order.
	defer close(block)

	origTimeout := gitNetworkTimeout
	gitNetworkTimeout = 200 * time.Millisecond
	defer func() { gitNetworkTimeout = origTimeout }()

	// Open without a remote so OpenStore itself doesn't try to clone from
	// the (hung) server; attach the remote afterwards via UpdateRemote,
	// which is purely local (no network call).
	repoDir := t.TempDir()
	cfg := Config{
		RepoDir: repoDir,
		AppDir:  t.TempDir(),
		Git:     GitConfig{User: "test"},
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	cfg.Git.RemoteURL = srv.URL + "/repo.git"
	if err := store.UpdateRemote(cfg); err != nil {
		t.Fatalf("UpdateRemote failed: %v", err)
	}

	start := time.Now()
	if _, err := store.Save("page.md", []byte("content"), "add page", "bob", "bob@hmd.local"); err != nil {
		t.Fatalf("Save should succeed even though the async push will stall: %v", err)
	}

	// SyncState() blocks on s.mu, so this loop only returns once push()
	// (which is holding the lock during its network call) releases it.
	deadline := time.Now().Add(3 * time.Second)
	var state string
	for time.Now().Before(deadline) {
		state, _ = store.SyncState()
		if state == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	elapsed := time.Since(start)

	if state != "failed" {
		t.Fatalf("SyncState = %q, want %q — push should have timed out", state, "failed")
	}
	if elapsed > 2*time.Second {
		t.Errorf("push held s.mu for %v, want it bounded by gitNetworkTimeout (%v)", elapsed, gitNetworkTimeout)
	}
}

// TestOpenStoreCloneTimeout covers the same "accepts connection, never
// responds" scenario during initial clone. Without a bounded context this
// would hang app startup indefinitely, before any Store even exists.
func TestOpenStoreCloneTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	origTimeout := gitNetworkTimeout
	gitNetworkTimeout = 200 * time.Millisecond
	defer func() { gitNetworkTimeout = origTimeout }()

	cfg := Config{
		RepoDir: t.TempDir(),
		AppDir:  t.TempDir(),
		Git:     GitConfig{RemoteURL: srv.URL + "/repo.git", User: "test"},
	}

	start := time.Now()
	_, err := OpenStore(cfg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("OpenStore should fail when the clone stalls past gitNetworkTimeout")
	}
	if elapsed > 2*time.Second {
		t.Errorf("OpenStore blocked for %v, want it bounded by gitNetworkTimeout (%v)", elapsed, gitNetworkTimeout)
	}
}

func TestUpdateRemoteNoRemoteToSet(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		RepoDir: tmpDir,
		AppDir:  t.TempDir(),
		Git:     GitConfig{User: "test"},
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	state, _ := store.SyncState()
	if state != "no remote" {
		t.Fatalf("initial SyncState = %q, want %q", state, "no remote")
	}

	// Add a remote
	cfg2 := cfg
	cfg2.Git.RemoteURL = "https://example.com/repo.git"
	cfg2.Git.Token = "tok123"
	if err := store.UpdateRemote(cfg2); err != nil {
		t.Fatalf("UpdateRemote failed: %v", err)
	}

	state, _ = store.SyncState()
	if state == "no remote" {
		t.Errorf("SyncState = %q after adding remote, want non-'no remote'", state)
	}
}

func TestUpdateRemoteChangeURL(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		RepoDir: tmpDir,
		AppDir:  t.TempDir(),
		Git:     GitConfig{User: "test"},
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	// Add the initial remote via UpdateRemote (no network — config only)
	cfgAdd := cfg
	cfgAdd.Git.RemoteURL = "https://example.com/old.git"
	cfgAdd.Git.Token = "old-token"
	if err := store.UpdateRemote(cfgAdd); err != nil {
		t.Fatalf("initial UpdateRemote failed: %v", err)
	}

	// Change the remote URL and token
	cfg2 := cfg
	cfg2.Git.RemoteURL = "https://example.com/new.git"
	cfg2.Git.Token = "new-token"
	if err := store.UpdateRemote(cfg2); err != nil {
		t.Fatalf("UpdateRemote failed: %v", err)
	}

	// Verify the remote URL changed in the repo
	remote, err := store.repo.Remote("origin")
	if err != nil {
		t.Fatalf("getting origin remote: %v", err)
	}
	urls := remote.Config().URLs
	if len(urls) == 0 || urls[0] != "https://example.com/new.git" {
		t.Errorf("remote URL = %v, want [https://example.com/new.git]", urls)
	}
}

func TestUpdateRemoteRemoveRemote(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		RepoDir: tmpDir,
		AppDir:  t.TempDir(),
		Git:     GitConfig{User: "test"},
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	// Add a remote first via UpdateRemote
	cfgAdd := cfg
	cfgAdd.Git.RemoteURL = "https://example.com/repo.git"
	cfgAdd.Git.Token = "tok"
	if err := store.UpdateRemote(cfgAdd); err != nil {
		t.Fatalf("initial UpdateRemote failed: %v", err)
	}

	// Remove the remote
	cfg2 := cfg
	cfg2.Git.RemoteURL = ""
	if err := store.UpdateRemote(cfg2); err != nil {
		t.Fatalf("UpdateRemote failed: %v", err)
	}

	state, _ := store.SyncState()
	if state != "no remote" {
		t.Errorf("SyncState = %q after removing remote, want %q", state, "no remote")
	}
}

func TestFetchAndFFBehind(t *testing.T) {
	bareDir := t.TempDir()
	if _, err := initBareRepo(bareDir); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	dirA := t.TempDir()
	cfgA := Config{RepoDir: dirA, AppDir: t.TempDir(), Git: GitConfig{RemoteURL: bareDir, User: "A"}}
	storeA, err := OpenStore(cfgA)
	if err != nil {
		t.Fatalf("OpenStore A: %v", err)
	}
	if _, err := storeA.Save("page.md", []byte("hello"), "add page", "alice", "alice@hmd.local"); err != nil {
		t.Fatalf("save A: %v", err)
	}
	waitForSync(storeA, 2*time.Second)

	dirB := t.TempDir()
	cfgB := Config{RepoDir: dirB, AppDir: t.TempDir(), Git: GitConfig{RemoteURL: bareDir, User: "B"}}
	storeB, err := OpenStore(cfgB)
	if err != nil {
		t.Fatalf("OpenStore B: %v", err)
	}

	if _, err := storeA.Save("page2.md", []byte("world"), "add page2", "alice", "alice@hmd.local"); err != nil {
		t.Fatalf("save A 2: %v", err)
	}
	waitForSync(storeA, 2*time.Second)

	result, err := storeB.FetchAndFF()
	if err != nil {
		t.Fatalf("FetchAndFF: %v", err)
	}
	if len(result.Commits) != 1 {
		t.Errorf("Commits = %d, want 1", len(result.Commits))
	}
	found := false
	for _, p := range result.ChangedPaths {
		if p == "page2.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("ChangedPaths = %v, want page2.md", result.ChangedPaths)
	}
	if _, err := os.Stat(filepath.Join(dirB, "page2.md")); err != nil {
		t.Errorf("page2.md not on disk after ff: %v", err)
	}
}

func TestFetchAndFFEqual(t *testing.T) {
	bareDir := t.TempDir()
	if _, err := initBareRepo(bareDir); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	dirA := t.TempDir()
	cfgA := Config{RepoDir: dirA, AppDir: t.TempDir(), Git: GitConfig{RemoteURL: bareDir, User: "A"}}
	storeA, err := OpenStore(cfgA)
	if err != nil {
		t.Fatalf("OpenStore A: %v", err)
	}
	if _, err := storeA.Save("page.md", []byte("hello"), "add page", "alice", "alice@hmd.local"); err != nil {
		t.Fatalf("save A: %v", err)
	}
	waitForSync(storeA, 2*time.Second)

	dirB := t.TempDir()
	cfgB := Config{RepoDir: dirB, AppDir: t.TempDir(), Git: GitConfig{RemoteURL: bareDir, User: "B"}}
	storeB, err := OpenStore(cfgB)
	if err != nil {
		t.Fatalf("OpenStore B: %v", err)
	}

	result, err := storeB.FetchAndFF()
	if err != nil {
		t.Fatalf("FetchAndFF: %v", err)
	}
	if len(result.ChangedPaths) != 0 {
		t.Errorf("ChangedPaths = %v, want empty", result.ChangedPaths)
	}
	if len(result.Commits) != 0 {
		t.Errorf("Commits = %d, want 0", len(result.Commits))
	}
}

func TestFetchAndFFDivergent(t *testing.T) {
	bareDir := t.TempDir()
	if _, err := initBareRepo(bareDir); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	dirA := t.TempDir()
	cfgA := Config{RepoDir: dirA, AppDir: t.TempDir(), Git: GitConfig{RemoteURL: bareDir, User: "A"}}
	storeA, err := OpenStore(cfgA)
	if err != nil {
		t.Fatalf("OpenStore A: %v", err)
	}
	if _, err := storeA.Save("page.md", []byte("hello"), "add page", "alice", "alice@hmd.local"); err != nil {
		t.Fatalf("save A: %v", err)
	}
	waitForSync(storeA, 2*time.Second)

	dirB := t.TempDir()
	cfgB := Config{RepoDir: dirB, AppDir: t.TempDir(), Git: GitConfig{RemoteURL: bareDir, User: "B"}}
	storeB, err := OpenStore(cfgB)
	if err != nil {
		t.Fatalf("OpenStore B: %v", err)
	}

	if _, err := storeA.Save("page2.md", []byte("from A"), "add page2", "alice", "alice@hmd.local"); err != nil {
		t.Fatalf("save A 2: %v", err)
	}
	waitForSync(storeA, 2*time.Second)

	if _, err := storeB.Save("page3.md", []byte("from B"), "add page3", "bob", "bob@hmd.local"); err != nil {
		t.Fatalf("save B: %v", err)
	}
	waitForSync(storeB, 2*time.Second)

	_, err = storeB.FetchAndFF()
	if err == nil {
		t.Fatal("FetchAndFF should fail on divergent state")
	}
	state, detail := storeB.SyncState()
	if state != "failed" {
		t.Errorf("SyncState = %q, want failed", state)
	}
	if detail == "" {
		t.Error("syncErr should be non-empty for divergent state")
	}

	if _, err := os.Stat(filepath.Join(dirB, "page3.md")); err != nil {
		t.Errorf("page3.md should still exist after failed ff (working tree untouched): %v", err)
	}
}

func waitForSync(store *Store, timeout time.Duration) {
	start := time.Now()
	for time.Since(start) < timeout {
		state, _ := store.SyncState()
		if state == "ok" || state == "failed" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// History results are cached; the cache must stay correct across UI saves
// (incremental update) and be dropped when a commit lands outside the UI.
func TestHistoryCacheExternalCommit(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(Config{RepoDir: tmpDir, AppDir: t.TempDir(), Git: GitConfig{User: "test"}})
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	if _, err := store.Save("page.md", []byte("v1"), "first", "alice", "alice@hmd.local"); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if _, err := store.History("page.md"); err != nil {
		t.Fatalf("History failed: %v", err)
	}

	// A UI save after the cache is warm must appear without a re-walk.
	if _, err := store.Save("page.md", []byte("v2"), "second", "alice", "alice@hmd.local"); err != nil {
		t.Fatalf("Save v2 failed: %v", err)
	}
	history, err := store.History("page.md")
	if err != nil {
		t.Fatalf("History after save failed: %v", err)
	}
	if len(history) != 2 || history[0].Message != "second" {
		t.Fatalf("cached history after save = %+v, want 2 entries, newest 'second'", history)
	}

	// Commit outside the store (simulating git CLI on the server).
	if err := os.WriteFile(filepath.Join(tmpDir, "page.md"), []byte("v3"), 0644); err != nil {
		t.Fatalf("writing page.md: %v", err)
	}
	repo, err := git.PlainOpen(tmpDir)
	if err != nil {
		t.Fatalf("PlainOpen failed: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree failed: %v", err)
	}
	if _, err := wt.Add("page.md"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	sig := &object.Signature{Name: "bob", Email: "bob@hmd.local", When: time.Now()}
	if _, err := wt.Commit("external", &git.CommitOptions{Author: sig}); err != nil {
		t.Fatalf("external commit failed: %v", err)
	}

	// Poll tick notices the foreign HEAD and drops the cache.
	store.DropHistoryOnExternalCommit()
	history, err = store.History("page.md")
	if err != nil {
		t.Fatalf("History after external commit failed: %v", err)
	}
	if len(history) != 3 || history[0].Message != "external" || history[0].Author != "bob" {
		t.Fatalf("history after external commit = %+v, want 3 entries, newest 'external' by bob", history)
	}
}
