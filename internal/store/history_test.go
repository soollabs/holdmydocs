package store

import (
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func openHistoryStore(t *testing.T, pages ...string) *Store {
	t.Helper()
	s, err := Open(Options{RepoDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		if _, err := s.Save(page, []byte(page), "add "+page, "alice", "alice@hmd.local"); err != nil {
			t.Fatalf("saving %s: %v", page, err)
		}
	}
	return s
}

// pauseHistoryWalk blocks the next walks inside the commit loop until release
// is called, and reports how many walks reached it.
func pauseHistoryWalk(t *testing.T) (entered <-chan struct{}, walks *atomic.Int32, release func()) {
	t.Helper()
	enteredCh, releaseCh := make(chan struct{}, 8), make(chan struct{})
	walks = new(atomic.Int32)
	historyWalkHook = func() {
		walks.Add(1)
		enteredCh <- struct{}{}
		<-releaseCh
	}
	released := false
	release = func() {
		if !released {
			released = true
			close(releaseCh)
		}
	}
	t.Cleanup(func() {
		release()
		historyWalkHook = nil
	})
	return enteredCh, walks, release
}

func within(t *testing.T, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s blocked behind a cold history walk", name)
	}
}

func TestHistoryColdWalkDoesNotBlockRead(t *testing.T) {
	s := openHistoryStore(t, "docs/one.md", "docs/two.md")
	entered, walks, release := pauseHistoryWalk(t)

	type result struct {
		history []CommitInfo
		err     error
	}
	results := make(chan result, 2)
	for _, page := range []string{"docs/one.md", "docs/two.md"} {
		go func() {
			history, err := s.History(page)
			results <- result{history, err}
		}()
	}
	<-entered

	within(t, "Read", func() {
		if _, _, err := s.Read("docs/one.md"); err != nil {
			t.Errorf("Read: %v", err)
		}
	})
	within(t, "SyncState", func() { s.SyncState() })
	within(t, "DropHistoryOnExternalCommit", s.DropHistoryOnExternalCommit)

	release()
	for range 2 {
		r := <-results
		if r.err != nil || len(r.history) != 1 {
			t.Fatalf("History = %+v, %v; want one entry", r.history, r.err)
		}
	}
	if n := walks.Load(); n != 1 {
		t.Fatalf("concurrent cold lookups ran %d walks, want 1", n)
	}
	if length, ok := s.CachedHistoryLength("docs/two.md"); !ok || length != 1 {
		t.Fatalf("walk result was not cached: %d, %v", length, ok)
	}
}

func TestHistoryWalkOverlappingSaveIsNotCached(t *testing.T) {
	s := openHistoryStore(t, "docs/page.md")
	entered, _, release := pauseHistoryWalk(t)

	done := make(chan []CommitInfo)
	go func() {
		history, _ := s.History("docs/page.md")
		done <- history
	}()
	<-entered
	within(t, "Save", func() {
		if _, err := s.Save("docs/page.md", []byte("v2"), "edit", "alice", "alice@hmd.local"); err != nil {
			t.Errorf("Save: %v", err)
		}
	})
	release()
	if history := <-done; len(history) != 1 {
		t.Fatalf("overlapping walk = %d entries, want the 1 from its starting head", len(history))
	}
	if _, ok := s.CachedHistoryLength("docs/page.md"); ok {
		t.Fatal("a walk overlapping a save was cached")
	}
	history, err := s.History("docs/page.md")
	if err != nil || len(history) != 2 || history[0].Message != "edit" {
		t.Fatalf("History after save = %+v, %v; want 2 entries, newest 'edit'", history, err)
	}
}

// TestHistoryWalkMatchesPatchPaths compares the walk with the previous
// rename-detecting patch walk across adds, edits, renames and removals.
func TestHistoryWalkMatchesPatchPaths(t *testing.T) {
	s := openHistoryStore(t, "docs/one.md", "docs/two.md", "notes/three.md")
	body := []byte("a page long enough for rename detection to pair it\nwith its new name\n")
	steps := []func() error{
		func() error {
			_, err := s.Save("docs/one.md", body, "edit one", "alice", "alice@hmd.local")
			return err
		},
		func() error {
			_, hash, err := s.Read("docs/one.md")
			if err != nil {
				return err
			}
			_, err = s.SaveChecked("docs/one.md", "docs/renamed.md", hash, body, "rename one", "alice", "alice@hmd.local")
			return err
		},
		func() error { return s.Remove("docs/two.md", "remove two", "alice", "alice@hmd.local") },
		func() error {
			_, err := s.SaveAll(map[string][]byte{"notes/three.md": []byte("3"), "notes/four.md": []byte("4")}, "batch", "alice", "alice@hmd.local")
			return err
		},
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}

	got, err := s.walkHistory(s.headHash())
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][]CommitInfo{}
	iter, err := s.repo.Log(&git.LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := iter.ForEach(func(c *object.Commit) error {
		current, _ := c.Tree()
		previous := &object.Tree{}
		if c.NumParents() > 0 {
			parent, _ := c.Parent(0)
			previous, _ = parent.Tree()
		}
		patch, err := previous.Patch(current)
		if err != nil {
			return err
		}
		paths := map[string]bool{}
		for _, fp := range patch.FilePatches() {
			from, to := fp.Files()
			if from != nil {
				paths[from.Path()] = true
			}
			if to != nil {
				paths[to.Path()] = true
			}
		}
		for path := range paths {
			want[path] = append(want[path], CommitInfo{Hash: c.Hash.String(), Message: c.Message, Author: c.Author.Name, When: c.Author.When, Path: path})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The moved page's history follows it to its new path.
	want["docs/renamed.md"] = append(want["docs/renamed.md"], want["docs/one.md"][1:]...)
	delete(want, "docs/one.md")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walk paths differ from the patch walk:\ngot  %v\nwant %v", got, want)
	}
}

// TestHistoryCacheSurvivesFirstRefreshAfterOpen covers a restart: the opening
// head is not an external commit, so neither the refresh worker's first pass
// nor the first save may drop history walked since.
func TestHistoryCacheSurvivesFirstRefreshAfterOpen(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(Options{RepoDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Save("docs/page.md", []byte("v1"), "add", "alice", "alice@hmd.local"); err != nil {
		t.Fatal(err)
	}

	s, err := Open(Options{RepoDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.History("docs/page.md"); err != nil {
		t.Fatal(err)
	}
	s.DropHistoryOnExternalCommit()
	if length, ok := s.CachedHistoryLength("docs/page.md"); !ok || length != 1 {
		t.Fatalf("first refresh after open dropped the history cache: %d, %v", length, ok)
	}
	if _, err := s.Save("docs/page.md", []byte("v2"), "edit", "alice", "alice@hmd.local"); err != nil {
		t.Fatal(err)
	}
	if length, ok := s.CachedHistoryLength("docs/page.md"); !ok || length != 2 {
		t.Fatalf("first save after open dropped the history cache: %d, %v", length, ok)
	}
}

func TestWarmHistoryCachesEveryPath(t *testing.T) {
	s := openHistoryStore(t, "docs/one.md", "notes/two.md")
	if err := s.WarmHistory(); err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"docs/one.md", "notes/two.md"} {
		if length, ok := s.CachedHistoryLength(page); !ok || length != 1 {
			t.Fatalf("WarmHistory did not cache %s: %d, %v", page, length, ok)
		}
	}
}

func TestWarmHistoryOnEmptyRepositoryFails(t *testing.T) {
	s := openHistoryStore(t)
	if err := s.WarmHistory(); err == nil {
		t.Fatal("WarmHistory on a repository without commits succeeded")
	}
}
