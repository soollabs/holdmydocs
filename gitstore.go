package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// gitNetworkTimeout bounds push/fetch network calls, which run with s.mu
// held — without a cap, a stalled remote would block every save and read.
// A var (not const) so tests can shrink it to exercise the timeout path.
var gitNetworkTimeout = 30 * time.Second

type CommitInfo struct {
	Hash    string
	Message string
	Author  string
	When    time.Time
}

type Store struct {
	repo      *git.Repository
	dir       string
	remote    string
	auth      *githttp.BasicAuth
	mu        sync.RWMutex
	syncState string
	syncErr   string
	// historyCache holds History() results keyed by path, protected by mu.
	// Save/Remove update it incrementally (O(1)); FetchAndFF drops it
	// wholesale since a pull can touch any path. Without this, every page
	// view re-walks the entire repo history (go-git's path-filtered Log has
	// no shortcut - it diffs every commit), which is fine at dozens of
	// commits and unusable at thousands.
	historyCache map[string][]CommitInfo
	// knownHead is the HEAD hash after the last commit or reset made by this
	// process, protected by mu. When HEAD differs from it, a commit was made
	// outside the UI (git CLI on the server) and historyCache is stale.
	// Zero value means "not yet observed" and safely triggers one drop of an
	// empty cache.
	knownHead  plumbing.Hash
	NeedsSetup atomic.Bool
	// ForceSetup is set by the "re-run setup" button in settings — it shows
	// the modal even when the home file/.help.md already exist, unlike NeedsSetup
	// which only reflects files actually missing.
	ForceSetup      atomic.Bool
	lastSuccessUnix atomic.Int64
}

// defaultHomeMD is the clean welcome page seeded into any repo that does not
// already contain the configured home file (HMD_HOME_FILENAME, default
// readme.md). It has a TOC token listing all pages. Seeding readme.md rather
// than home.md means the same file shows up rendered on the git host's front
// page (GitHub, git, etc. all render readme.md case-insensitively).
const defaultHomeMD = `# Welcome to hold my docs (hmd)

This wiki is plain markdown files in a git repository. Every save is a commit
that auto-pushes to the configured remote. Pages are flat, no folders, and
linked with ` + "`[[Page Title]]`" + ` wiki-links.

## All pages

` + "<!-- hmd:toc -->" + `
`

// defaultHelpMD is the built-in "how hold my docs (hmd) works" guide seeded as a hidden
// dot-file (.help.md). Unlike the home file, this is not a user choice during
// setup — it's app documentation, seeded unconditionally. Covers both UI
// usage and the on-disk markdown conventions, for humans and agents alike.
const defaultHelpMD = `# Help

## Finding pages

Press ` + "`ctrl k`" + ` (or tap "search" on mobile) to open the jump palette: type
to search, arrow keys to move, enter to open, ctrl-enter to open in edit mode.
Press ` + "`/`" + ` outside any text field to open it too.

## Creating a page

Type a title into the palette that doesn't match an existing page and a
"+ create page" row appears. Selecting it opens a blank editor for that
title. The "+ New Document" button in the header opens the same palette
straight into that create mode.

## Keyboard shortcuts

| Shortcut | Action |
| --- | --- |
| ` + "`ctrl k`" + ` | Open/close the jump palette |
| ` + "`/`" + ` | Open the jump palette (outside text fields) |
| ` + "`ctrl e`" + ` | Edit the current page |
| ` + "`ctrl s`" + ` | Save (while editing) |
| ` + "`esc`" + ` | Close the palette, or exit full-screen mode |
| ` + "`ctrl b`" + ` / ` + "`ctrl i`" + ` | Bold / italic (while editing) |
| ` + "`ctrl shift k`" + ` | Insert a wiki-link (while editing) |
| ` + "`alt 1`" + ` / ` + "`alt 2`" + ` / ` + "`alt 3`" + ` | Heading level 1 / 2 / 3 (while editing) |
| ` + "`ctrl shift f`" + ` | Toggle full-screen editing |
| ` + "`ctrl shift t`" + ` | Toggle typewriter scroll |
| ` + "`ctrl shift d`" + ` | Toggle focus mode (dims inactive lines) |

Use ` + "`cmd`" + ` instead of ` + "`ctrl`" + ` on macOS.

## Hidden pages

A filename starting with a dot (` + "`.help.md`" + `) is a "hidden page": excluded
from the page list, search, tags, and backlinks, but still viewable and
editable through the UI at ` + "`/hidden/<slug>`" + `. The ` + "`/hidden`" + ` index lists
them all. Toggle the "hidden" checkbox in the editor to move a page in or
out of this namespace.

## Settings

The settings page (` + "`/settings`" + `) covers git remote, theme colours, and
behaviour like upload limits. "Re-run setup" there reopens the home page
setup prompt if you ever need to re-add it.

## Sync status

The bottom-right of the statusline shows git sync state: pending, ok, or
failed, since every save is an auto-pushed commit.

## File names

- One page per Markdown file, lowercase, hyphenated: ` + "`running-the-app.md`" + `.
- The slug is the filename without the ` + "`.md`" + ` suffix. The home page is ` + "`readme.md`" + ` (configurable via ` + "`HMD_HOME_FILENAME`" + `).
- Attachments live under ` + "`attachments/<slug>/<file>`" + `.

## Frontmatter

Every page starts with a frontmatter block:

    ---
    title: Human-Friendly Title
    tags: one, two, three
    ---

` + "`title`" + ` is the display heading. ` + "`tags`" + ` is a comma-separated list.

## Body

- Use ` + "`#`" + ` for the title only; start sections at ` + "`##`" + ` (h2), subsections at ` + "`###`" + ` (h3).
- Link other pages with wiki-links: ` + "`[[Page Title]]`" + ` resolves to the matching slug.

## Table of contents

Insert ` + "`<!-- hmd:toc -->`" + ` anywhere to list every page, or
` + "`<!-- hmd:toc:tag1,tag2 -->`" + ` to list only pages matching any of the
given tags (OR).

## Diagrams

Mermaid code fences render as diagrams:

` + "    ```mermaid\n    graph LR\n    A --> B\n    ```" + `

## Attachments

Permitted types: png, jpg, jpeg, gif, webp, pdf. SVG is excluded (XSS risk).
Paste or drag an image into the editor to upload it, or reference one
already uploaded by its served URL: ` + "`/attachments/<slug>/<file>`" + `.

Editing the repo directly (agents, scripts): commit the file straight to
` + "`attachments/<slug>/<file>`" + `, no upload API call needed. The type
whitelist and SVG exclusion above are only enforced by the upload
endpoint, not when serving, so stick to them by convention on direct
writes too.

## History

Every save is a git commit. The wiki auto-pushes to the configured remote after
each save. Reverting creates a new commit; history is never rewritten.
`

// HelpDrifted reports whether .help.md on disk differs from the built-in
// defaultHelpMD for the running version. Used to warn on the settings page.
func HelpDrifted(store *Store) bool {
	raw, err := os.ReadFile(filepath.Join(store.dir, ".help.md"))
	if err != nil {
		return false
	}
	return ParsePage("help", raw).Body != strings.TrimRight(defaultHelpMD, "\n")
}

// seedOrFlagSetup never writes anything without consent: it only sets the
// NeedsSetup flag when the configured home file and/or .help.md is missing,
// on any repo — fresh, cloned, or existing. The setup modal decides what
// actually gets seeded, based on what the user selects. The home file is
// named by HMD_HOME_FILENAME (default README.md); .help.md is fixed.
func seedOrFlagSetup(store *Store, cfg Config) {
	_, homeErr := os.Stat(filepath.Join(store.dir, cfg.HomeFilename))
	_, helpErr := os.Stat(filepath.Join(store.dir, ".help.md"))
	if homeErr != nil || helpErr != nil {
		store.NeedsSetup.Store(true)
	}
}

func OpenStore(cfg Config) (*Store, error) {
	// First, try to open existing repo
	repo, err := git.PlainOpen(cfg.RepoDir)
	if err == nil {
		// Repo exists
		remote := ""
		if cfg.RemoteURL != "" {
			remote = "origin"
		}

		var auth *githttp.BasicAuth
		if cfg.RemoteURL != "" && cfg.GitToken != "" {
			auth = &githttp.BasicAuth{Username: cfg.GitUser, Password: cfg.GitToken}
		}

		store := &Store{
			repo:      repo,
			dir:       cfg.RepoDir,
			remote:    remote,
			auth:      auth,
			syncState: "ok",
		}
		store.lastSuccessUnix.Store(time.Now().Unix())
		seedOrFlagSetup(store, cfg)
		slog.Info("opened existing repo", "dir", cfg.RepoDir, "remote", remote != "")
		return store, nil
	}
	// Try to clone if remote is set
	if cfg.RemoteURL != "" {
		var auth *githttp.BasicAuth
		if cfg.GitToken != "" {
			auth = &githttp.BasicAuth{Username: cfg.GitUser, Password: cfg.GitToken}
		}

		cloneOpts := &git.CloneOptions{
			URL:  cfg.RemoteURL,
			Auth: auth,
		}

		cloneCtx, cloneCancel := context.WithTimeout(context.Background(), gitNetworkTimeout)
		repo, err := git.PlainCloneContext(cloneCtx, cfg.RepoDir, false, cloneOpts)
		cloneCancel()
		if err != nil {
			// Check if this is an empty remote repo error
			if err == transport.ErrEmptyRemoteRepository {
				// Fall through to init with empty repo
				goto init_empty_remote
			}
			return nil, fmt.Errorf("cloning repo: %w", err)
		}

		store := &Store{
			repo:      repo,
			dir:       cfg.RepoDir,
			remote:    "origin",
			auth:      auth,
			syncState: "ok",
		}
		store.lastSuccessUnix.Store(time.Now().Unix())

		slog.Info("cloned repo", "dir", cfg.RepoDir, "remote", cfg.RemoteURL)

		// Seed if the cloned repo is fresh or missing pages
		seedOrFlagSetup(store, cfg)

		return store, nil
	}

	// Initialize repo without remote
init_empty_remote:
	defaultBranch := cfg.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	repo, err = git.PlainInitWithOptions(cfg.RepoDir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.NewBranchReferenceName(defaultBranch),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("initializing repo: %w", err)
	}

	store := &Store{
		repo:      repo,
		dir:       cfg.RepoDir,
		remote:    "",
		syncState: "no remote",
	}
	store.lastSuccessUnix.Store(time.Now().Unix())

	slog.Info("initialised new repo", "dir", cfg.RepoDir, "branch", defaultBranch)

	// Create index.md for local-only repos, or for empty remote case
	if cfg.RemoteURL != "" {
		// Empty remote case: add remote and push
		_, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{cfg.RemoteURL}})
		if err != nil {
			return nil, fmt.Errorf("creating remote: %w", err)
		}

		store.remote = "origin"
		if cfg.GitToken != "" {
			store.auth = &githttp.BasicAuth{Username: cfg.GitUser, Password: cfg.GitToken}
		}
	}

	// Seed help + home for fresh repos
	seedOrFlagSetup(store, cfg)

	return store, nil
}

// Read reads path's content and blob hash. Takes a read lock so it can't
// observe a Save() mid-write (os.WriteFile is open/truncate/write, not
// atomic) or a FetchAndFF() mid fast-forward checkout.
func (s *Store) Read(path string) (content []byte, blobHash string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fullPath := filepath.Join(s.dir, path)
	content, err = os.ReadFile(fullPath)
	if err != nil {
		return nil, "", fmt.Errorf("reading %s: %w", path, err)
	}

	hash := plumbing.ComputeHash(plumbing.BlobObject, content)
	return content, hash.String(), nil
}

// readHashLocked returns path's current blob hash, or "" if the file
// doesn't exist. Callers must hold s.mu.
func (s *Store) readHashLocked(path string) (string, error) {
	fullPath := filepath.Join(s.dir, path)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	hash := plumbing.ComputeHash(plumbing.BlobObject, content)
	return hash.String(), nil
}

// ErrConflict is returned by SaveChecked when the file's current blob hash
// doesn't match the caller's expected hash.
var ErrConflict = errors.New("optimistic lock conflict")

// SaveChecked performs an optimistic-lock-protected save: the check against
// oldPath's current blob hash and the write (with an optional move to
// newPath) happen as one operation under s.mu, so two concurrent saves
// against the same basehash can't both succeed the way they could with a
// separate unlocked Read() followed by Save(). Returns ErrConflict if
// oldPath's current hash doesn't match expectedHash.
//
// On a move (oldPath != newPath), newPath is written before oldPath is
// removed, so a failure partway through leaves the content reachable at
// both paths rather than lost entirely.
func (s *Store) SaveChecked(oldPath, newPath, expectedHash string, content []byte, message, authorName, authorEmail string) (blobHash string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	currentHash, err := s.readHashLocked(oldPath)
	if err != nil {
		return "", err
	}
	if currentHash != expectedHash {
		return "", ErrConflict
	}

	blobHash, err = s.saveLocked(newPath, content, message, authorName, authorEmail)
	if err != nil {
		return "", err
	}
	if oldPath != newPath {
		if err := s.removeLocked(oldPath, message, authorName, authorEmail); err != nil {
			return blobHash, err
		}
	}
	return blobHash, nil
}

func (s *Store) Save(path string, content []byte, message, authorName, authorEmail string) (blobHash string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(path, content, message, authorName, authorEmail)
}

// saveLocked is Save's body. Callers must hold s.mu.
func (s *Store) saveLocked(path string, content []byte, message, authorName, authorEmail string) (blobHash string, err error) {
	fullPath := filepath.Join(s.dir, path)

	// Skip the write and commit entirely if the content is unchanged, so an
	// untouched file (including its on-disk mode) never produces a no-op commit.
	if existing, readErr := os.ReadFile(fullPath); readErr == nil && string(existing) == string(content) {
		slog.Debug("save skipped, content unchanged", "path", path)
		hash := plumbing.ComputeHash(plumbing.BlobObject, content)
		return hash.String(), nil
	}

	// Write file
	dir := filepath.Dir(fullPath)
	err = os.MkdirAll(dir, 0755)
	if err != nil {
		return "", fmt.Errorf("creating directory: %w", err)
	}

	err = os.WriteFile(fullPath, content, 0644)
	if err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}

	// Get worktree and add file
	wt, err := s.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("getting worktree: %w", err)
	}

	_, err = wt.Add(path)
	if err != nil {
		return "", fmt.Errorf("adding file to index: %w", err)
	}

	// Commit
	preHead := s.headHash()
	when := time.Now()
	commitHash, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  authorName,
			Email: authorEmail,
			When:  when,
		},
	})
	if err != nil {
		return "", fmt.Errorf("committing: %w", err)
	}
	s.noteCommit(commitHash, preHead)
	s.prependHistory(path, CommitInfo{Hash: commitHash.String(), Message: message, Author: authorName, When: when})
	slog.Debug("committed", "path", path, "hash", commitHash.String()[:8], "author", authorName)

	// Compute blob hash
	hash := plumbing.ComputeHash(plumbing.BlobObject, content)
	blobHash = hash.String()

	// Async push if remote configured
	if s.remote != "" {
		s.syncState = "pending"
		go s.push()
	}

	return blobHash, nil
}

// Remove deletes path from disk and the git index, committing the removal.
// A no-op (returns nil) if the file is already gone.
func (s *Store) Remove(path, message, authorName, authorEmail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeLocked(path, message, authorName, authorEmail)
}

// removeLocked is Remove's body. Callers must hold s.mu.
func (s *Store) removeLocked(path, message, authorName, authorEmail string) error {
	fullPath := filepath.Join(s.dir, path)
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return nil
	}
	if err := os.Remove(fullPath); err != nil {
		return fmt.Errorf("removing file: %w", err)
	}

	wt, err := s.repo.Worktree()
	if err != nil {
		return fmt.Errorf("getting worktree: %w", err)
	}
	if _, err := wt.Remove(path); err != nil {
		return fmt.Errorf("removing file from index: %w", err)
	}

	preHead := s.headHash()
	commitHash, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  authorName,
			Email: authorEmail,
			When:  time.Now(),
		},
	})
	if err != nil {
		return fmt.Errorf("committing: %w", err)
	}
	s.noteCommit(commitHash, preHead)
	delete(s.historyCache, path)
	slog.Debug("removed", "path", path, "hash", commitHash.String()[:8])

	if s.remote != "" {
		s.syncState = "pending"
		go s.push()
	}
	return nil
}

// prependHistory adds a newly-created commit to the cached history for path,
// if a cache entry already exists (i.e. some earlier History() call paid the
// full-walk cost). Callers must hold s.mu.
func (s *Store) prependHistory(path string, entry CommitInfo) {
	cached, ok := s.historyCache[path]
	if !ok {
		return
	}
	s.historyCache[path] = append([]CommitInfo{entry}, cached...)
}

func (s *Store) push() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Bound how long a stalled remote can hold s.mu — every save, read, and
	// sync-status check serializes on this lock while a push is in flight.
	ctx, cancel := context.WithTimeout(context.Background(), gitNetworkTimeout)
	defer cancel()

	err := s.repo.PushContext(ctx, &git.PushOptions{
		RemoteName: "origin",
		Auth:       s.auth,
	})

	if err == git.NoErrAlreadyUpToDate || err == nil {
		s.syncState = "ok"
		s.syncErr = ""
		s.lastSuccessUnix.Store(time.Now().Unix())
		if err == nil {
			slog.Info("pushed", "remote", s.remote)
		} else {
			slog.Debug("push already up to date")
		}
	} else {
		s.syncState = "failed"
		s.syncErr = err.Error()
		slog.Warn("push failed", "err", err)
	}
}

func (s *Store) List() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("reading directory: %w", err)
	}

	var paths []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".md" && !strings.HasPrefix(e.Name(), ".") {
			paths = append(paths, e.Name())
		}
	}

	sort.Strings(paths)
	return paths, nil
}

// ListHidden returns dot-prefixed .md files (hidden pages).
func (s *Store) ListHidden() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("reading directory: %w", err)
	}

	var paths []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), ".") && filepath.Ext(e.Name()) == ".md" {
			paths = append(paths, e.Name())
		}
	}

	sort.Strings(paths)
	return paths, nil
}

func (s *Store) History(path string) ([]CommitInfo, error) {
	// The walk runs under mu so a concurrent Save can't commit mid-walk and
	// then have its prependHistory no-op'd by us caching a pre-commit result.
	// mu is already held across network pushes, so a one-time cold walk here
	// is no worse.
	s.mu.Lock()
	defer s.mu.Unlock()

	if cached, ok := s.historyCache[path]; ok {
		return cached, nil
	}

	iter, err := s.repo.Log(&git.LogOptions{FileName: &path})
	if err != nil {
		return nil, fmt.Errorf("getting log: %w", err)
	}

	var commits []CommitInfo
	iter.ForEach(func(c *object.Commit) error {
		commits = append(commits, CommitInfo{
			Hash:    c.Hash.String(),
			Message: c.Message,
			Author:  c.Author.Name,
			When:    c.Author.When,
		})
		return nil
	})

	if s.historyCache == nil {
		s.historyCache = make(map[string][]CommitInfo)
	}
	s.historyCache[path] = commits

	return commits, nil
}

// CommitDetail is CommitInfo plus the files the commit touched, for the MCP
// recent_changes tool.
type CommitDetail struct {
	Hash    string
	Message string
	Author  string
	When    time.Time
	Files   []string
}

// RecentCommits walks the log head-first and returns the newest n commits
// with the files each touched.
func (s *Store) RecentCommits(n int) ([]CommitDetail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	iter, err := s.repo.Log(&git.LogOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting log: %w", err)
	}

	var commits []CommitDetail
	err = iter.ForEach(func(c *object.Commit) error {
		if len(commits) >= n {
			return storer.ErrStop
		}
		var files []string
		// Stats diffs each commit against its parent; fine for a bounded n
		if stats, statErr := c.Stats(); statErr == nil {
			for _, st := range stats {
				files = append(files, st.Name)
			}
		}
		commits = append(commits, CommitDetail{
			Hash:    c.Hash.String(),
			Message: strings.TrimSpace(c.Message),
			Author:  c.Author.Name,
			When:    c.Author.When,
			Files:   files,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return commits, nil
}

// headHash returns the current HEAD hash, or ZeroHash for an unborn branch.
// Callers must hold s.mu.
func (s *Store) headHash() plumbing.Hash {
	ref, err := s.repo.Head()
	if err != nil {
		return plumbing.ZeroHash
	}
	return ref.Hash()
}

// noteCommit records a commit made by this process. If HEAD had moved since
// our last known commit (an external commit slipped in), the cache may miss
// it for any path, so drop it wholesale. Callers must hold s.mu.
func (s *Store) noteCommit(newHead, preHead plumbing.Hash) {
	if s.knownHead != preHead {
		s.historyCache = nil
	}
	s.knownHead = newHead
}

// DropHistoryOnExternalCommit invalidates the history cache when HEAD has
// moved to a commit this process didn't create (e.g. git CLI on the server,
// which pollFS exists to pick up). Cheap when nothing changed; called every
// poll tick.
func (s *Store) DropHistoryOnExternalCommit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if head := s.headHash(); head != s.knownHead {
		s.historyCache = nil
		s.knownHead = head
	}
}

func (s *Store) FileAt(path, commitHash string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	hash := plumbing.NewHash(commitHash)
	commit, err := s.repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("getting commit: %w", err)
	}

	file, err := commit.File(path)
	if err != nil {
		return nil, fmt.Errorf("getting file from commit: %w", err)
	}

	content, err := file.Contents()
	if err != nil {
		return nil, fmt.Errorf("reading file contents: %w", err)
	}

	return []byte(content), nil
}

func (s *Store) SyncState() (state, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncState, s.syncErr
}

// LastSyncUnix returns the Unix timestamp of the last successful sync.
func (s *Store) LastSyncUnix() int64 {
	return s.lastSuccessUnix.Load()
}

// PushNow performs an immediate synchronous push (the ">sync" palette verb)
// and returns the resulting sync state.
func (s *Store) PushNow() (state, detail string) {
	s.mu.Lock()
	noRemote := s.remote == ""
	if !noRemote {
		s.syncState = "pending"
	}
	s.mu.Unlock()
	if !noRemote {
		s.push()
	}
	return s.SyncState()
}

// UpdateRemote reconfigures the store's remote URL and auth credentials
// on the live repository. If cfg.RemoteURL is empty, the remote is removed.
// If non-empty, the origin remote is created or updated with set-url.
func (s *Store) UpdateRemote(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cfg.RemoteURL == "" {
		if s.remote != "" {
			if err := s.repo.DeleteRemote("origin"); err != nil {
				// not fatal — may already be gone
				slog.Warn("deleting remote", "err", err)
			}
		}
		s.remote = ""
		s.auth = nil
		s.syncState = "no remote"
		slog.Info("remote removed")
		return nil
	}

	if s.remote == "" {
		_, err := s.repo.CreateRemote(&config.RemoteConfig{
			Name: "origin",
			URLs: []string{cfg.RemoteURL},
		})
		if err != nil {
			return fmt.Errorf("creating remote: %w", err)
		}
	} else {
		err := s.repo.DeleteRemote("origin")
		if err != nil {
			slog.Warn("deleting old remote", "err", err)
		}
		_, err = s.repo.CreateRemote(&config.RemoteConfig{
			Name: "origin",
			URLs: []string{cfg.RemoteURL},
		})
		if err != nil {
			return fmt.Errorf("recreating remote: %w", err)
		}
	}

	s.remote = "origin"
	if cfg.GitToken != "" {
		s.auth = &githttp.BasicAuth{
			Username: cfg.GitUser,
			Password: cfg.GitToken,
		}
	} else {
		s.auth = nil
	}
	s.syncState = "ok"
	slog.Info("remote configured", "url", cfg.RemoteURL)
	return nil
}

// FetchResult describes what changed when a fetch+ff brought in remote commits.
type FetchResult struct {
	ChangedPaths []string
	Commits      []CommitInfo
}

// FetchAndFF fetches from origin and fast-forwards the local branch if the
// remote is ahead. Returns the paths and commits that came in. If local and
// remote are equal or local is ahead, returns empty. If divergent, sets
// syncState to "failed" and returns an error.
func (s *Store) FetchAndFF() (FetchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.remote == "" {
		return FetchResult{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), gitNetworkTimeout)
	defer cancel()

	err := s.repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       s.auth,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		s.syncState = "failed"
		s.syncErr = err.Error()
		return FetchResult{}, err
	}

	headRef, err := s.repo.Head()
	if err != nil {
		s.syncState = "failed"
		s.syncErr = err.Error()
		return FetchResult{}, err
	}
	localHash := headRef.Hash()

	remoteRefName := plumbing.NewRemoteReferenceName("origin", headRef.Name().Short())
	remoteRef, err := s.repo.Reference(remoteRefName, true)
	if err != nil {
		s.syncState = "failed"
		s.syncErr = err.Error()
		return FetchResult{}, err
	}
	remoteHash := remoteRef.Hash()

	if localHash == remoteHash {
		s.syncState = "ok"
		s.syncErr = ""
		slog.Debug("fetch: already up to date")
		return FetchResult{}, nil
	}

	localIsAncestor, err := s.isAncestor(localHash, remoteHash)
	if err != nil {
		s.syncState = "failed"
		s.syncErr = err.Error()
		return FetchResult{}, err
	}

	if !localIsAncestor {
		remoteIsAncestor, err := s.isAncestor(remoteHash, localHash)
		if err != nil {
			s.syncState = "failed"
			s.syncErr = err.Error()
			return FetchResult{}, err
		}
		if remoteIsAncestor {
			s.syncState = "ok"
			s.syncErr = ""
			return FetchResult{}, nil
		}
		s.syncState = "failed"
		s.syncErr = "divergent: local and remote have diverged; resolve via git on the server"
		slog.Warn("fetch: divergent branches", "local", localHash.String()[:8], "remote", remoteHash.String()[:8])
		return FetchResult{}, fmt.Errorf("%s", s.syncErr)
	}

	result, err := s.diffCommits(localHash, remoteHash)
	if err != nil {
		s.syncState = "failed"
		s.syncErr = err.Error()
		return FetchResult{}, err
	}

	wt, err := s.repo.Worktree()
	if err != nil {
		s.syncState = "failed"
		s.syncErr = err.Error()
		return FetchResult{}, err
	}
	if err := wt.Reset(&git.ResetOptions{
		Commit: remoteHash,
		Mode:   git.HardReset,
	}); err != nil {
		s.syncState = "failed"
		s.syncErr = err.Error()
		return FetchResult{}, err
	}

	s.historyCache = nil
	s.knownHead = remoteHash
	s.syncState = "ok"
	s.syncErr = ""
	slog.Info("fast-forwarded", "paths", len(result.ChangedPaths), "commits", len(result.Commits))
	return result, nil
}

// isAncestor walks the commit graph from descendant toward roots, checking
// whether ancestor is reachable.
func (s *Store) isAncestor(ancestor, descendant plumbing.Hash) (bool, error) {
	if ancestor == descendant {
		return true, nil
	}
	commit, err := s.repo.CommitObject(descendant)
	if err != nil {
		return false, err
	}
	queue := []*object.Commit{commit}
	seen := make(map[plumbing.Hash]bool)
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c.Hash == ancestor {
			return true, nil
		}
		if seen[c.Hash] {
			continue
		}
		seen[c.Hash] = true
		for i := 0; i < c.NumParents(); i++ {
			p, err := c.Parent(i)
			if err != nil {
				continue
			}
			queue = append(queue, p)
		}
	}
	return false, nil
}

// Diff returns the unified diff for a specific file between two commits.
// If either hash cannot be resolved, returns an error.
func (s *Store) Diff(filename, hashA, hashB string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	commitA, err := s.repo.CommitObject(plumbing.NewHash(hashA))
	if err != nil {
		return "", fmt.Errorf("resolving commit A: %w", err)
	}
	commitB, err := s.repo.CommitObject(plumbing.NewHash(hashB))
	if err != nil {
		return "", fmt.Errorf("resolving commit B: %w", err)
	}

	treeA, err := commitA.Tree()
	if err != nil {
		return "", fmt.Errorf("resolving tree A: %w", err)
	}
	treeB, err := commitB.Tree()
	if err != nil {
		return "", fmt.Errorf("resolving tree B: %w", err)
	}

	changes, err := object.DiffTree(treeA, treeB)
	if err != nil {
		return "", fmt.Errorf("diffing trees: %w", err)
	}

	// Only the requested file — the two commits may touch other pages too.
	var fileChanges object.Changes
	for _, c := range changes {
		if c.From.Name == filename || c.To.Name == filename {
			fileChanges = append(fileChanges, c)
		}
	}

	patch, err := fileChanges.Patch()
	if err != nil {
		return "", fmt.Errorf("creating patch: %w", err)
	}
	return patch.String(), nil
}

// diffCommits computes changed file paths and commit list between oldHash and
// newHash (exclusive of oldHash, inclusive of newHash).
func (s *Store) diffCommits(oldHash, newHash plumbing.Hash) (FetchResult, error) {
	oldCommit, err := s.repo.CommitObject(oldHash)
	if err != nil {
		return FetchResult{}, err
	}
	newCommit, err := s.repo.CommitObject(newHash)
	if err != nil {
		return FetchResult{}, err
	}

	patch, err := oldCommit.Patch(newCommit)
	if err != nil {
		return FetchResult{}, err
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

	var commits []CommitInfo
	visited := make(map[plumbing.Hash]bool)
	queue := []*object.Commit{newCommit}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c.Hash == oldHash || visited[c.Hash] {
			continue
		}
		visited[c.Hash] = true
		commits = append(commits, CommitInfo{
			Hash:    c.Hash.String(),
			Message: c.Message,
			Author:  c.Author.Name,
			When:    c.Author.When,
		})
		for i := 0; i < c.NumParents(); i++ {
			p, err := c.Parent(i)
			if err != nil {
				continue
			}
			queue = append(queue, p)
		}
	}

	return FetchResult{ChangedPaths: changed, Commits: commits}, nil
}
