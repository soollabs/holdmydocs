package store_test

import (
	"context"
	"errors"
	"fmt"
	"hmd/internal/api"
	appconfig "hmd/internal/config"
	staticexport "hmd/internal/export"
	"hmd/internal/search"
	storepkg "hmd/internal/store"
	"hmd/internal/web"
	"hmd/internal/wiki"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type Config = appconfig.Config
type GitConfig = appconfig.GitConfig
type Page = wiki.Page
type NamespaceRegistry = wiki.NamespaceRegistry
type Store = storepkg.Store

var (
	BuildIndex                      = search.BuildIndex
	NewRenderer                     = wiki.NewRenderer
	BuildNamespaceRegistryFromStore = api.BuildNamespaceRegistryFromStore
	ErrConflict                     = storepkg.ErrConflict
	setGitNetworkTimeout            = storepkg.SetGitNetworkTimeout
	setFetchThrottle                = storepkg.SetFetchThrottle
)

const (
	defaultHelpMD  = storepkg.DefaultHelpMD
	wikiConfigFile = wiki.ConfigFile
)

func storeOptions(cfg Config) storepkg.Options {
	return storepkg.Options{RepoDir: cfg.RepoDir, DefaultBranch: cfg.DefaultBranch, Git: storepkg.GitOptions{RemoteURL: cfg.Git.RemoteURL, User: cfg.Git.User, Token: cfg.Git.Token}}
}

func OpenStore(cfg Config) (*storepkg.Store, error) { return storepkg.Open(storeOptions(cfg)) }
func seedOrFlagSetup(content *storepkg.Store)       { storepkg.SeedOrFlagSetup(content) }

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
		RepoDir: tmpDir,
		AppDir:  t.TempDir(),
		Git:     GitConfig{User: "test"},
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	if !store.NeedsSetup.Load() {
		t.Errorf("NeedsSetup should be true for a fresh repo — nothing is auto-seeded")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "readme.md")); err == nil {
		t.Errorf("readme.md should not be auto-seeded")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".help.md")); err == nil {
		t.Errorf(".help.md should not be auto-seeded")
	}

	state, _ := store.SyncState()
	if state != "no remote" {
		t.Errorf("SyncState = %q, want %q", state, "no remote")
	}
}

// TestExistingContentSkipsSetup ensures complete repositories need no setup.
func TestExistingContentSkipsSetup(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		RepoDir: tmpDir,
		AppDir:  t.TempDir(),
		Git:     GitConfig{User: "test"},
	}

	homePath := filepath.Join(tmpDir, "notes", "readme.md")
	if err := os.MkdirAll(filepath.Dir(homePath), 0755); err != nil {
		t.Fatalf("creating notes namespace: %v", err)
	}
	custom := []byte("---\ntitle: Home\ntags: \n---\n\n# My custom home\n")
	if err := os.WriteFile(homePath, custom, 0644); err != nil {
		t.Fatalf("writing custom notes/readme.md: %v", err)
	}
	helpPath := filepath.Join(tmpDir, ".help.md")
	if err := os.WriteFile(helpPath, []byte("---\ntitle: Help\n---\n\n# My custom help\n"), 0644); err != nil {
		t.Fatalf("writing custom .help.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, wikiConfigFile), []byte("landing: notes/\nsite_name: Existing Wiki\n"), 0644); err != nil {
		t.Fatalf("writing %s: %v", wikiConfigFile, err)
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	if store.NeedsSetup.Load() {
		t.Errorf("NeedsSetup should be false when a namespace, .wiki.yaml and .help.md already exist")
	}
	after, err := os.ReadFile(homePath)
	if err != nil {
		t.Fatalf("reading notes/readme.md after open: %v", err)
	}
	if string(after) != string(custom) {
		t.Error("notes/readme.md was overwritten on open; existing files must not be touched")
	}
	if err := os.Remove(filepath.Join(tmpDir, wikiConfigFile)); err != nil {
		t.Fatalf("removing %s: %v", wikiConfigFile, err)
	}
	seedOrFlagSetup(store)
	if !store.NeedsSetup.Load() {
		t.Errorf("NeedsSetup should be true when %s is missing", wikiConfigFile)
	}
}

func TestBuiltInHelpDocumentsNamespaces(t *testing.T) {
	for _, want := range []string{
		"`/_/namespaces`",
		"`/<namespace>/`",
		"`/<namespace>/<page>`",
		"Quick-create page",
		"Reset namespace settings",
		"ordinary pages only",
	} {
		if !strings.Contains(defaultHelpMD, want) {
			t.Errorf("built-in help missing %q", want)
		}
	}
}

// TestListRecurses ensures page listing handles nested paths and excludes non-page files.
func TestListRecurses(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := Config{
		RepoDir: tmpDir,
		AppDir:  t.TempDir(),
		Git:     GitConfig{User: "test"},
	}

	write := func(rel string) {
		full := filepath.Join(tmpDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte("---\ntitle: x\n---\n\nbody\n"), 0644); err != nil {
			t.Fatalf("writing %s: %v", rel, err)
		}
	}
	write("readme.md")
	write("blog/post.md")
	write("blog/drafts/deep-post.md")
	write("attachments/blog/post/pic.png")
	write("attachments/.note.md")
	write(".help.md")
	write("notes/.entry.md")
	if err := os.MkdirAll(filepath.Join(tmpDir, ".git-like-dir"), 0755); err != nil {
		t.Fatalf("mkdir .git-like-dir: %v", err)
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	paths, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	want := map[string]bool{"blog/post.md": true, "blog/drafts/deep-post.md": true}
	got := make(map[string]bool, len(paths))
	for _, p := range paths {
		got[p] = true
	}
	for p := range want {
		if !got[p] {
			t.Errorf("List() missing %q, got %v", p, paths)
		}
	}
	for p := range got {
		if !want[p] {
			t.Errorf("List() unexpectedly included %q (attachments/dot-prefixed should be excluded)", p)
		}
	}

	hidden, err := store.ListHidden()
	if err != nil {
		t.Fatalf("ListHidden failed: %v", err)
	}
	wantHidden := map[string]bool{".help.md": true, "notes/.entry.md": true}
	gotHidden := make(map[string]bool, len(hidden))
	for _, p := range hidden {
		gotHidden[p] = true
	}
	for p := range wantHidden {
		if !gotHidden[p] {
			t.Errorf("ListHidden() missing %q, got %v", p, hidden)
		}
	}
	for p := range gotHidden {
		if !wantHidden[p] {
			t.Errorf("ListHidden() unexpectedly included %q (ordinary pages and .git are not hidden pages)", p)
		}
	}
}

func TestRepositoryPathsRejectSymlinksAndTraversal(t *testing.T) {
	repoDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(Config{RepoDir: repoDir, AppDir: t.TempDir(), Git: GitConfig{User: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"notes/linked.md", "notes/.linked.md", "notes/.namespace.yaml"} {
		full := filepath.Join(repoDir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, full); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Read(path); err == nil {
			t.Errorf("Read(%q) followed a symlink", path)
		}
	}
	if paths, err := store.List(); err != nil || len(paths) != 0 {
		t.Errorf("List() = %v, %v; want no symlink pages", paths, err)
	}
	if hidden, err := store.ListHidden(); err != nil || len(hidden) != 0 {
		t.Errorf("ListHidden() = %v, %v; want no symlink pages", hidden, err)
	}
	if reg, err := BuildNamespaceRegistryFromStore(store); err != nil || reg["notes"].Configured {
		t.Errorf("BuildNamespaceRegistryFromStore() = %#v, %v; want default notes config", reg["notes"], err)
	}

	for _, path := range []string{"../outside.md", "/tmp/outside.md", `notes\\page.md`, ".git/config", "notes/../page.md"} {
		if _, err := store.Save(path, []byte("new"), "save", "test", "test@hmd.local"); err == nil {
			t.Errorf("Save(%q) succeeded", path)
		}
	}
	if _, err := store.Save("notes/linked.md", []byte("new"), "save", "test", "test@hmd.local"); err == nil {
		t.Error("Save over symlink succeeded")
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "outside" {
		t.Errorf("outside file = %q, %v", got, err)
	}
}

func TestRepositoryReadCannotRaceIntoSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("descriptor-relative no-follow reads are Unix-specific")
	}
	repoDir := t.TempDir()
	store, err := OpenStore(Config{RepoDir: repoDir, AppDir: t.TempDir(), Git: GitConfig{User: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("notes/page.md", []byte("inside"), "seed", "test", "test@hmd.local"); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(repoDir, "notes", "page.md")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Remove(page)
			_ = os.Symlink(outside, page)
			_ = os.Remove(page)
			_ = os.WriteFile(page, []byte("inside"), 0o644)
		}
	}()
	for range 2_000 {
		content, _, err := store.Read("notes/page.md")
		if err == nil && strings.Contains(string(content), "outside-secret") {
			close(stop)
			<-done
			t.Fatalf("read escaped repository: %q", content)
		}
	}
	close(stop)
	<-done
}

func TestAttachmentsAndExportRejectSymlinks(t *testing.T) {
	repoDir := t.TempDir()
	store, err := OpenStore(Config{RepoDir: repoDir, AppDir: t.TempDir(), Git: GitConfig{User: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("attachments/docs/page/real.txt", []byte("real"), "seed", "test", "test@hmd.local"); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(repoDir, "attachments", "docs", "page", "linked.txt")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.OpenAttachment("attachments/docs/page/linked.txt"); err == nil {
		t.Error("OpenAttachment followed a symlink")
	}
	paths, err := store.ListAttachments()
	if err != nil || len(paths) != 1 || paths[0] != "attachments/docs/page/real.txt" {
		t.Errorf("ListAttachments() = %v, %v", paths, err)
	}
	outDir := t.TempDir()
	pages := []Page{{Slug: "docs/page", Title: "Page", Body: "body"}}
	reg := NamespaceRegistry{"docs": {}}
	if err := staticexport.Namespace(staticexport.NamespaceRequest{
		Pages:      pages,
		Namespace:  "docs",
		Config:     reg["docs"],
		OutDir:     outDir,
		Assets:     web.StaticAssets(),
		AssetsRoot: "web/static",
		Store:      store,
	}, web.NewStaticExporter(NewRenderer(func(string, string) (string, bool) { return "", false }))); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(outDir, "attachments", "page", "real.txt")); err != nil || string(got) != "real" {
		t.Errorf("exported real attachment = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "attachments", "page", "linked.txt")); !os.IsNotExist(err) {
		t.Errorf("exported symlink attachment: %v", err)
	}
}

func TestExistingRepoWithContentNeedsSetup(t *testing.T) {
	tmpDir := t.TempDir()
	repo, err := git.PlainInit(tmpDir, false)
	if err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree failed: %v", err)
	}

	existingPage := filepath.Join(tmpDir, "existing-page.md")
	if err := os.WriteFile(existingPage, []byte("# Existing\n"), 0644); err != nil {
		t.Fatalf("writing existing page: %v", err)
	}
	if _, err := wt.Add("existing-page.md"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if _, err := wt.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@hmd.local", When: time.Now()},
	}); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	cfg := Config{
		RepoDir: tmpDir,
		AppDir:  t.TempDir(),
		Git:     GitConfig{User: "test"},
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	if !store.NeedsSetup.Load() {
		t.Errorf("NeedsSetup should be true for existing root-only content")
	}

	if _, err := os.Stat(filepath.Join(tmpDir, "readme.md")); err == nil {
		t.Errorf("readme.md should not be auto-seeded on existing repo with content")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".help.md")); err == nil {
		t.Errorf(".help.md should not be auto-seeded on existing repo with content")
	}
}

func TestEmptyRepoNeedsSetup(t *testing.T) {
	tmpDir := t.TempDir()
	if _, err := git.PlainInit(tmpDir, false); err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	cfg := Config{
		RepoDir: tmpDir,
		AppDir:  t.TempDir(),
		Git:     GitConfig{User: "test"},
	}

	store, err := OpenStore(cfg)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

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

	oldContent, err := store.FileAt("test.md", history[1].Hash)
	if err != nil {
		t.Fatalf("FileAt failed: %v", err)
	}
	if string(oldContent) != "v1" {
		t.Errorf("Old content = %q, want %q", string(oldContent), "v1")
	}

	_, err = store.Save("test.md", []byte("v1"), "revert", "alice", "alice@hmd.local")
	if err != nil {
		t.Fatalf("Revert save failed: %v", err)
	}

	history, err = store.History("test.md")
	if err != nil {
		t.Fatalf("History after revert failed: %v", err)
	}
	if len(history) != 3 {
		t.Errorf("History length after revert = %d, want 3", len(history))
	}

	content, _, err = store.Read("test.md")
	if err != nil {
		t.Fatalf("Read after revert failed: %v", err)
	}
	if string(content) != "v1" {
		t.Errorf("Content after revert = %q, want %q", string(content), "v1")
	}
}

// TestSaveCheckedConcurrentSameBasehash verifies concurrent saves from one base hash conflict.
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
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.SaveChecked("test.md", "test.md", baseHash,
				fmt.Appendf(nil, "v-from-%d", i), "concurrent update", "alice", "alice@hmd.local")
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

func TestSaveCheckedMoveDoesNotOverwriteDestination(t *testing.T) {
	store, err := OpenStore(Config{RepoDir: t.TempDir(), AppDir: t.TempDir(), Git: GitConfig{User: "test"}})
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	hash, err := store.Save("source.md", []byte("source"), "seed source", "alice", "alice@hmd.local")
	if err != nil {
		t.Fatalf("seeding source path: %v", err)
	}
	if _, err := store.Save("new.md", []byte("new"), "seed new", "alice", "alice@hmd.local"); err != nil {
		t.Fatalf("seeding destination: %v", err)
	}
	if _, err := store.SaveChecked("source.md", "new.md", hash, []byte("replacement"), "move", "alice", "alice@hmd.local"); !errors.Is(err, ErrConflict) {
		t.Fatalf("move to existing destination = %v, want ErrConflict", err)
	}
	content, _, err := store.Read("new.md")
	if err != nil || string(content) != "new" {
		t.Fatalf("destination after rejected move = %q, %v", content, err)
	}
}

func TestPushToLocalBareRemote(t *testing.T) {
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

	_, err = store.Save("page.md", []byte("content"), "add page", "bob", "bob@hmd.local")
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	if err := waitForPushes(store, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	state, _ := store.SyncState()
	if state != "ok" {
		t.Errorf("SyncState = %q, want %q", state, "ok")
	}

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

	if err := os.RemoveAll(bareDir); err != nil {
		t.Fatalf("removing bare repo: %v", err)
	}

	_, err = store.Save("page.md", []byte("content"), "add page", "bob", "bob@hmd.local")
	if err != nil {
		t.Errorf("Save should succeed even if push fails: %v", err)
	}

	history, err := store.History("page.md")
	if err != nil {
		t.Fatalf("History failed: %v", err)
	}
	if len(history) == 0 {
		t.Errorf("History should have entries after save")
	}

	if err := waitForPushes(store, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	state, detail := store.SyncState()
	if state != "failed" {
		t.Errorf("SyncState = %q, want failed (detail: %s)", state, detail)
	}
	if detail == "" {
		t.Error("sync failure detail is empty")
	}
}

// TestPushTimeoutReleasesLock ensures a stalled push does not block the store lock.
func TestPushTimeoutReleasesLock(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()

	defer close(block)

	timeout := 200 * time.Millisecond
	defer setGitNetworkTimeout(timeout)()

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
	if err := store.UpdateRemote(storeOptions(cfg)); err != nil {
		t.Fatalf("UpdateRemote failed: %v", err)
	}

	start := time.Now()
	if _, err := store.Save("page.md", []byte("content"), "add page", "bob", "bob@hmd.local"); err != nil {
		t.Fatalf("Save should succeed even though the async push will stall: %v", err)
	}

	if err := waitForPushes(store, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	state, _ := store.SyncState()
	if state != "failed" {
		t.Fatalf("SyncState = %q, want %q — push should have timed out", state, "failed")
	}
	if elapsed > 2*time.Second {
		t.Errorf("push held s.mu for %v, want it bounded by gitNetworkTimeout (%v)", elapsed, timeout)
	}
}

// TestOpenStoreCloneTimeout ensures initial cloning honours the network timeout.
func TestOpenStoreCloneTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	timeout := 200 * time.Millisecond
	defer setGitNetworkTimeout(timeout)()

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
		t.Errorf("OpenStore blocked for %v, want it bounded by gitNetworkTimeout (%v)", elapsed, timeout)
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

	cfg2 := cfg
	cfg2.Git.RemoteURL = "https://example.com/repo.git"
	cfg2.Git.Token = "tok123"
	if err := store.UpdateRemote(storeOptions(cfg2)); err != nil {
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

	cfgAdd := cfg
	cfgAdd.Git.RemoteURL = "https://example.com/old.git"
	cfgAdd.Git.Token = "old-token"
	if err := store.UpdateRemote(storeOptions(cfgAdd)); err != nil {
		t.Fatalf("initial UpdateRemote failed: %v", err)
	}

	cfg2 := cfg
	cfg2.Git.RemoteURL = "https://example.com/new.git"
	cfg2.Git.Token = "new-token"
	if err := store.UpdateRemote(storeOptions(cfg2)); err != nil {
		t.Fatalf("UpdateRemote failed: %v", err)
	}

	urls, err := store.RemoteURLs("origin")
	if err != nil {
		t.Fatalf("getting origin remote: %v", err)
	}
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

	cfgAdd := cfg
	cfgAdd.Git.RemoteURL = "https://example.com/repo.git"
	cfgAdd.Git.Token = "tok"
	if err := store.UpdateRemote(storeOptions(cfgAdd)); err != nil {
		t.Fatalf("initial UpdateRemote failed: %v", err)
	}

	cfg2 := cfg
	cfg2.Git.RemoteURL = ""
	if err := store.UpdateRemote(storeOptions(cfg2)); err != nil {
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
	if err := waitForPushes(storeA, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	dirB := t.TempDir()
	cfgB := Config{RepoDir: dirB, AppDir: t.TempDir(), Git: GitConfig{RemoteURL: bareDir, User: "B"}}
	storeB, err := OpenStore(cfgB)
	if err != nil {
		t.Fatalf("OpenStore B: %v", err)
	}

	if _, err := storeA.Save("page2.md", []byte("world"), "add page2", "alice", "alice@hmd.local"); err != nil {
		t.Fatalf("save A 2: %v", err)
	}
	if err := waitForPushes(storeA, 2*time.Second); err != nil {
		t.Fatal(err)
	}

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
	if err := waitForPushes(storeA, 2*time.Second); err != nil {
		t.Fatal(err)
	}

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
	if err := waitForPushes(storeA, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	dirB := t.TempDir()
	cfgB := Config{RepoDir: dirB, AppDir: t.TempDir(), Git: GitConfig{RemoteURL: bareDir, User: "B"}}
	storeB, err := OpenStore(cfgB)
	if err != nil {
		t.Fatalf("OpenStore B: %v", err)
	}

	if _, err := storeA.Save("page2.md", []byte("from A"), "add page2", "alice", "alice@hmd.local"); err != nil {
		t.Fatalf("save A 2: %v", err)
	}
	if err := waitForPushes(storeA, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	if _, err := storeB.Save("page3.md", []byte("from B"), "add page3", "bob", "bob@hmd.local"); err != nil {
		t.Fatalf("save B: %v", err)
	}
	if err := waitForPushes(storeB, 2*time.Second); err != nil {
		t.Fatal(err)
	}

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

// TestStalledPushDoesNotBlockReadOrSave ensures a stalled push does not block reads or saves.
func TestStalledPushDoesNotBlockReadOrSave(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	timeout := 5 * time.Second
	defer setGitNetworkTimeout(timeout)()

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
	if err := store.UpdateRemote(storeOptions(cfg)); err != nil {
		t.Fatalf("UpdateRemote failed: %v", err)
	}

	if _, err := store.Save("page.md", []byte("content"), "add page", "bob", "bob@hmd.local"); err != nil {
		t.Fatalf("Save should succeed: %v", err)
	}

	done := make(chan error, 2)
	start := time.Now()
	go func() {
		_, _, err := store.Read("page.md")
		done <- err
	}()
	go func() {
		_, err := store.Save("page2.md", []byte("more"), "add page2", "bob", "bob@hmd.local")
		done <- err
	}()

	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("op failed: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("Read/Save did not return within 2s; still queued behind the stalled push")
		}
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Read/Save took %v, want well under gitNetworkTimeout (%v)", elapsed, timeout)
	}
}

func TestWaitForPushesHonoursContext(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-release
	}))
	defer unblock()
	defer server.Close()

	store, err := OpenStore(Config{RepoDir: t.TempDir(), AppDir: t.TempDir(), Git: GitConfig{User: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRemote(storepkg.Options{Git: storepkg.GitOptions{RemoteURL: server.URL + "/repo.git", User: "test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("notes/page.md", []byte("content"), "save", "test", "test@hmd.local"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("push did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.WaitForPushes(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitForPushes = %v, want context cancellation", err)
	}
	unblock()
	if err := waitForPushes(store, 2*time.Second); err != nil {
		t.Fatal(err)
	}
}

// TestFetchThrottle ensures fetches within the throttle interval are coalesced.
func TestFetchThrottle(t *testing.T) {
	defer setFetchThrottle(200 * time.Millisecond)()

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
	if err := waitForPushes(storeA, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	dirB := t.TempDir()
	cfgB := Config{RepoDir: dirB, AppDir: t.TempDir(), Git: GitConfig{RemoteURL: bareDir, User: "B"}}
	storeB, err := OpenStore(cfgB)
	if err != nil {
		t.Fatalf("OpenStore B: %v", err)
	}

	if _, err := storeA.Save("page2.md", []byte("world"), "add page2", "alice", "alice@hmd.local"); err != nil {
		t.Fatalf("save A 2: %v", err)
	}
	if err := waitForPushes(storeA, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	if _, err := storeB.FetchAndFF(); err != nil {
		t.Fatalf("first FetchAndFF: %v", err)
	}

	result, err := storeB.FetchAndFF()
	if err != nil {
		t.Fatalf("second FetchAndFF: %v", err)
	}
	if len(result.ChangedPaths) != 0 || len(result.Commits) != 0 {
		t.Errorf("throttled FetchAndFF = %+v, want empty result", result)
	}
}

func waitForPushes(store *Store, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return store.WaitForPushes(ctx)
}

// TestHistoryCacheExternalCommit ensures cached history reflects internal and external commits.
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

	store.DropHistoryOnExternalCommit()
	history, err = store.History("page.md")
	if err != nil {
		t.Fatalf("History after external commit failed: %v", err)
	}
	if len(history) != 3 || history[0].Message != "external" || history[0].Author != "bob" {
		t.Fatalf("history after external commit = %+v, want 3 entries, newest 'external' by bob", history)
	}
}

func TestHistoryColdLookupCachesEveryPath(t *testing.T) {
	store, err := OpenStore(Config{RepoDir: t.TempDir(), AppDir: t.TempDir(), Git: GitConfig{User: "test"}})
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	for _, path := range []string{"one.md", "two.md"} {
		if _, err := store.Save(path, []byte(path), "add "+path, "alice", "alice@hmd.local"); err != nil {
			t.Fatalf("Save %s failed: %v", path, err)
		}
	}
	content, hash, err := store.Read("two.md")
	if err != nil {
		t.Fatalf("Read two.md failed: %v", err)
	}
	if _, err := store.SaveChecked("two.md", "renamed.md", hash, content, "rename two", "alice", "alice@hmd.local"); err != nil {
		t.Fatalf("renaming two.md failed: %v", err)
	}

	if _, err := store.History("one.md"); err != nil {
		t.Fatalf("History failed: %v", err)
	}
	if length, ok := store.CachedHistoryLength("renamed.md"); !ok || length != 1 {
		t.Fatalf("cold lookup did not cache renamed.md history: %d", length)
	}
}
