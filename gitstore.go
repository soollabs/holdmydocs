package main

import (
	"fmt"
	"log"
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
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

type CommitInfo struct {
	Hash    string
	Message string
	Author  string
	When    time.Time
}

type Store struct {
	repo       *git.Repository
	dir        string
	remote     string
	auth       *githttp.BasicAuth
	mu         sync.Mutex
	syncState  string
	syncErr    string
	NeedsSetup atomic.Bool
	// ForceSetup is set by the "re-run setup" button in settings — it shows
	// the modal even when home.md/.help.md already exist, unlike NeedsSetup
	// which only reflects files actually missing.
	ForceSetup atomic.Bool
}

// defaultHomeMD is the clean welcome page seeded into any repo that does not
// already contain a home.md. It has a TOC token listing all pages.
const defaultHomeMD = `# Welcome to hold my docs (hmd)

This wiki is plain markdown files in a git repository. Every save is a commit
that auto-pushes to the configured remote. Pages are flat, no folders, and
linked with ` + "`[[Page Title]]`" + ` wiki-links.

## All pages

` + "<!-- hmd:toc -->" + `
`

// defaultHelpMD is the built-in "how hold my docs (hmd) works" guide seeded as a hidden
// dot-file (.help.md). Unlike home.md, this is not a user choice during
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
- The slug is the filename without the ` + "`.md`" + ` suffix. The home page is ` + "`home.md`" + `.
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
// NeedsSetup flag when home.md and/or .help.md is missing, on any repo —
// fresh, cloned, or existing. The setup modal decides what actually gets
// seeded, based on what the user selects.
func seedOrFlagSetup(store *Store, cfg Config) {
	_, homeErr := os.Stat(filepath.Join(store.dir, "home.md"))
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
		seedOrFlagSetup(store, cfg)
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

		repo, err := git.PlainClone(cfg.RepoDir, false, cloneOpts)
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

		// Seed if the cloned repo is fresh or missing pages
		seedOrFlagSetup(store, cfg)

		return store, nil
	}

	// Initialize repo without remote
init_empty_remote:
	repo, err = git.PlainInit(cfg.RepoDir, false)
	if err != nil {
		return nil, fmt.Errorf("initializing repo: %w", err)
	}

	store := &Store{
		repo:      repo,
		dir:       cfg.RepoDir,
		remote:    "",
		syncState: "no remote",
	}

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

func (s *Store) Read(path string) (content []byte, blobHash string, err error) {
	fullPath := filepath.Join(s.dir, path)
	content, err = os.ReadFile(fullPath)
	if err != nil {
		return nil, "", fmt.Errorf("reading %s: %w", path, err)
	}

	hash := plumbing.ComputeHash(plumbing.BlobObject, content)
	return content, hash.String(), nil
}

func (s *Store) Save(path string, content []byte, message, authorName, authorEmail string) (blobHash string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Write file
	fullPath := filepath.Join(s.dir, path)
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
	_, err = wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  authorName,
			Email: authorEmail,
			When:  time.Now(),
		},
	})
	if err != nil {
		return "", fmt.Errorf("committing: %w", err)
	}

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

	_, err = wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  authorName,
			Email: authorEmail,
			When:  time.Now(),
		},
	})
	if err != nil {
		return fmt.Errorf("committing: %w", err)
	}

	if s.remote != "" {
		s.syncState = "pending"
		go s.push()
	}
	return nil
}

func (s *Store) push() {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.repo.Push(&git.PushOptions{
		RemoteName: "origin",
		Auth:       s.auth,
	})

	if err == git.NoErrAlreadyUpToDate || err == nil {
		s.syncState = "ok"
		s.syncErr = ""
	} else {
		s.syncState = "failed"
		s.syncErr = err.Error()
	}
}

func (s *Store) List() ([]string, error) {
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

	return commits, nil
}

func (s *Store) FileAt(path, commitHash string) ([]byte, error) {
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
				log.Printf("deleting remote: %v", err)
			}
		}
		s.remote = ""
		s.auth = nil
		s.syncState = "no remote"
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
			log.Printf("deleting old remote: %v", err)
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

	err := s.repo.Fetch(&git.FetchOptions{
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

	s.syncState = "ok"
	s.syncErr = ""
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
