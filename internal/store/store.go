package store

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

var gitNetworkTimeout atomic.Int64

func init() {
	gitNetworkTimeout.Store(int64(30 * time.Second))
	fetchThrottle.Store(int64(2 * time.Second))
}

func networkTimeout() time.Duration { return time.Duration(gitNetworkTimeout.Load()) }

func SetGitNetworkTimeout(timeout time.Duration) func() {
	previous := gitNetworkTimeout.Swap(int64(timeout))
	return func() { gitNetworkTimeout.Store(previous) }
}

type CommitInfo struct {
	Hash    string
	Message string
	Author  string
	When    time.Time
}

type GitOptions struct {
	RemoteURL string
	User      string
	Token     string
}

type Options struct {
	RepoDir       string
	DefaultBranch string
	Git           GitOptions
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
	historyCache map[string][]CommitInfo
	// knownHead is the HEAD hash after the last commit or reset made by this process, protected by mu.
	knownHead  plumbing.Hash
	NeedsSetup atomic.Bool
	// ForceSetup is set by the "re-run setup" button in settings — it shows the modal even when a namespace,
	// .wiki.yaml and .help.md already exist, unlike NeedsSetup which only reflects what is actually missing.
	ForceSetup      atomic.Bool
	lastSuccessUnix atomic.Int64

	// pushMu serializes pushes against each other; it is held for the network round trip instead of mu, so reads
	// and saves never queue behind a push. pushDirty is set when a save arrives while a push is already running,
	// so that push loops once more instead of a second goroutine piling onto pushMu.
	pushMu    sync.Mutex
	pushDirty atomic.Bool
	pushWG    sync.WaitGroup

	// fetchMu serializes fetches against each other. lastFetchNano throttles FetchAndFF so a burst of sync polls
	// across open tabs collapses to one network call every fetchThrottle.
	fetchMu       sync.Mutex
	lastFetchNano atomic.Int64
}

const (
	attachmentsDir         = "attachments"
	namespaceConfigFile    = ".namespace.yaml"
	extractedAttachmentDir = ".hmd/extracted"
	slowLockWait           = time.Second
)

func traceSlowLockWait(operation string) func() {
	started := time.Now()
	timer := time.AfterFunc(slowLockWait, func() {
		slog.Warn("repository lock wait exceeded threshold", "operation", operation, "threshold", slowLockWait)
	})
	return func() {
		if !timer.Stop() {
			slog.Info("repository lock acquired after slow wait", "operation", operation, "wait", time.Since(started))
		}
	}
}

func namespaceConfigPath(namespace string) string { return namespace + "/" + namespaceConfigFile }

func validNamespaceName(name string) bool {
	return name != "" && name != "_" && name != attachmentsDir &&
		!strings.ContainsAny(name, `/\`) && !strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "_")
}

func validPageSlug(slug string) bool {
	namespace, rest, ok := strings.Cut(slug, "/")
	if !ok || !validNamespaceName(namespace) || rest == "" {
		return false
	}
	for segment := range strings.SplitSeq(rest, "/") {
		if segment == "" || strings.ContainsAny(segment, `/\`) || strings.HasPrefix(segment, ".") || strings.HasPrefix(segment, "_") {
			return false
		}
	}
	return true
}

func parseAttachmentPath(path string) (owner, filename string, ok bool) {
	path = filepath.ToSlash(path)
	rest, ok := strings.CutPrefix(path, attachmentsDir+"/")
	if !ok {
		return "", "", false
	}
	i := strings.LastIndexByte(rest, '/')
	if i < 1 || i == len(rest)-1 {
		return "", "", false
	}
	owner, filename = rest[:i], rest[i+1:]
	if !validPageSlug(owner) || strings.ContainsAny(filename, `/\`) || strings.HasPrefix(filename, ".") {
		return "", "", false
	}
	return owner, filename, true
}

func extractedAttachmentPath(path string) string {
	owner, filename, ok := parseAttachmentPath(path)
	if !ok {
		return ""
	}
	return attachmentsDir + "/" + owner + "/" + extractedAttachmentDir + "/" + filename + ".txt"
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) ReadRepositoryFile(path string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readRepositoryFile(path)
}

var fetchThrottle atomic.Int64

func fetchThrottleDuration() time.Duration { return time.Duration(fetchThrottle.Load()) }

func SetFetchThrottle(interval time.Duration) func() {
	previous := fetchThrottle.Swap(int64(interval))
	return func() { fetchThrottle.Store(previous) }
}

func (s *Store) RemoteURLs(name string) ([]string, error) {
	remote, err := s.repo.Remote(name)
	if err != nil {
		return nil, err
	}
	return remote.Config().URLs, nil
}

func (s *Store) CachedHistoryLength(path string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	history, ok := s.historyCache[path]
	return len(history), ok
}

const defaultIndexPage = "readme"

const defaultHomeMD = `# Welcome to hold my docs (hmd)

This wiki is plain markdown files in a git repository. Every save is a commit
that auto-pushes to the configured remote. Pages are organised into
namespaces (top-level directories) and linked with ` + "`[[Page Title]]`" + ` wiki-links.

## Pages in this namespace

` + "<!-- hmd:toc -->" + `
`

const rootReadmeMD = `# hold my docs (hmd)

A wiki, stored as markdown. Each top-level directory is a namespace and each
` + "`.md`" + ` file inside one is a page; ` + "`[[Page Title]]`" + ` links pages together.
Edit the files here directly or through the app — both are commits either way.
`

const (
	DefaultIndexPage = defaultIndexPage
	DefaultHomeMD    = defaultHomeMD
	RootReadmeMD     = rootReadmeMD
)

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
editable through the UI at ` + "`/_/hidden/<slug>`" + `. The ` + "`/_/hidden`" + ` index
lists them all. Toggle the "hidden" checkbox in the editor to move a page in
or out of this set.

## Settings

Two pages, both behind the "settings" scope. ` + "`/_/settings`" + ` is personal:
skin, palette, fonts, your git author and your access tokens.
` + "`/_/admin`" + ` manages the instance: git remote, behaviour like upload
limits and users. Its separate wiki configuration section edits the repository's
` + "`.wiki.yaml`" + ` (site name and landing page). "Re-run setup" on ` + "`/_/admin`" + ` reopens the
first-namespace setup prompt if you ever need it again.

Namespace management has its own settings page at ` + "`/_/namespaces`" + `.

Everything else the app owns lives under ` + "`/_/`" + ` too — no page can ever
take those URLs.

## Sync status

The bottom-right of the statusline shows git sync state: pending, ok, or
failed, since every save is an auto-pushed commit.

## File names

- One page per Markdown file, lowercase, hyphenated: ` + "`docs/running-the-app.md`" + `.
- Every page lives in a namespace directory; the slug is the path without the ` + "`.md`" + ` suffix, e.g. ` + "`docs/running-the-app`" + `.
- A namespace's ` + "`index`" + ` setting names the page that serves ` + "`/<namespace>/`" + `; ` + "`/`" + ` itself goes wherever ` + "`.wiki.yaml`" + `'s ` + "`landing`" + ` key points.
- ` + "`.wiki.yaml`" + ` sits at the repository root and sets the wiki's ` + "`site_name`" + ` and ` + "`landing`" + `; it travels with the content to another hmd instance.
- Attachments live under ` + "`attachments/<slug>/<file>`" + `.

## Frontmatter

Every page starts with a frontmatter block:

    ---
    title: Human-Friendly Title
    tags: one, two, three
    ---

` + "`title`" + ` is the display heading. ` + "`tags`" + ` is a comma-separated list.

Optional keys, read by specific widgets — ignored everywhere else, and
untouched by an ordinary editor save even though there's no dedicated UI
for them yet:

- ` + "`pin: true`" + ` — surfaces the page in the pinned sidebar widget.

## Body

- Use ` + "`#`" + ` for the title only; start sections at ` + "`##`" + ` (h2), subsections at ` + "`###`" + ` (h3).
- Link other pages with wiki-links: ` + "`[[Page Title]]`" + ` resolves to the matching slug.

## Table of contents

Insert ` + "`<!-- hmd:toc -->`" + ` anywhere to list every page in the current
page's namespace, or ` + "`<!-- hmd:toc:tag1,tag2 -->`" + ` to list only pages
in that namespace matching any of the given tags (OR). The list never
reaches into other namespaces.

## Diagrams

Mermaid code fences render as diagrams:

` + "    ```mermaid\n    graph LR\n    A --> B\n    ```" + `

## Attachments

Permitted types: png, jpg, jpeg, gif, webp, pdf, doc, docx, xls, xlsx, ppt,
pptx, odt, ods and odp. SVG is excluded (XSS risk).
Paste or drag an image into the editor to upload it, or reference one
already uploaded by its served URL: ` + "`/_/attachments/<slug>/<file>`" + `.

Editing the repo directly (agents, scripts): commit the file straight to
` + "`attachments/<slug>/<file>`" + `, no upload API call needed. The type
whitelist and SVG exclusion above are only enforced by the upload
endpoint, not when serving, so stick to them by convention on direct
writes too.

## Namespaces

A namespace is a top-level directory in the repo, and every page lives in
one: ` + "`blog/post.md`" + ` is in the ` + "`blog`" + ` namespace. Anything
deeper (` + "`blog/drafts/post.md`" + `) is just filing — namespaces are exactly
one level deep.

The namespace index is ` + "`/<namespace>/`" + ` (or the bare
` + "`/<namespace>`" + `, which serves the same thing) and a page is
` + "`/<namespace>/<page>`" + `. Public namespaces are visible without signing
in; private and unknown namespace indexes both return the same not-found page.

A namespace decides three things for the pages in it, via an optional
` + "`.namespace.yaml`" + ` beside them:

    widgets: [search, pages, tags, log, outline, page-meta, backlinks]
    public: true
    new:
      template: entry
      slug: '{{.Now.Format "2006-01-02"}}'

- ` + "`widgets`" + ` — which widgets mount, in this order within each slot.
  Omit it for the built-in set.
- ` + "`public: true`" + ` — pages here are readable without logging in.
  Everything is private by default, and a private page is indistinguishable
  from one that doesn't exist to an anonymous visitor.
- ` + "`new`" + ` — what the **Quick-create page** setting (ctrl-j) creates here:
  ` + "`template`" + ` names a hidden page in the namespace to copy, ` + "`slug`" + `
  names the page it creates.

## Template pages

Every namespace made from the settings page gets a hidden template page
(` + "`template`" + `, so ` + "`.blog/template.md`" + ` for ` + "`blog`" + `). Pages
created in that namespace start as a copy of it, with its **title, tags and
body** substituted through Go's ` + "`text/template`" + `. Three fields exist,
and nothing else:

- ` + "`{{.Now}}`" + ` — the moment the page is created. Format it with Go's
  layout syntax, which is the reference time written in the shape you want:
  ` + "`{{.Now.Format \"2006-01-02\"}}`" + ` for 2026-07-28,
  ` + "`{{.Now.Format \"Monday, 2 January 2006\"}}`" + ` for the long date,
  ` + "`{{.Now.Format \"15:04\"}}`" + ` for the time.
- ` + "`{{.User}}`" + ` — who pressed ctrl-j.
- ` + "`{{.Namespace}}`" + ` — the namespace name.

The same fields name the page itself, via the namespace's slug pattern. The
seeded template documents all of this in its own body — read it, then delete
it and write yours.

Manage namespaces at ` + "`/_/namespaces`" + `. Saving a new name creates its
configuration. **Reset namespace settings** removes only ` + "`.namespace.yaml`" + `;
it keeps every page and hidden template. True deletion is available only when
there are no pages or hidden files, so move or remove those first. Writing
` + "`.namespace.yaml`" + ` by hand works too; the app rescans on a timer.

MCP clients can access ordinary pages only by default; when document search is
enabled they also get attachment search. Page tools accept a
` + "`namespace/page`" + ` slug, reads require read scope, and writes require write
scope. Wiki settings, namespace settings, hidden templates and attachment
source files are never exposed through MCP.

## Skins

A skin is how the app looks: typography, spacing, markers and what the
statusline shows. It does not decide which widgets mount — that's the
namespace — and it never changes how or where pages are stored, so the same
repo opens correctly under any of them. Pick one on ` + "`/_/settings`" + `:
` + "`phosphor`" + ` (default, terminal), ` + "`newsprint`" + ` (broadsheet
serif), ` + "`journal`" + ` (writing first — serif, wide measure),
` + "`soft`" + ` (rounded, low-contrast sans), ` + "`bare`" + ` (stripped down). Each skin arrives in the colour
**palette** it was designed for; you can pick a different one afterwards.

## History

Every save is a git commit. The wiki auto-pushes to the configured remote after
each save. Reverting creates a new commit; history is never rewritten.
`

const DefaultHelpMD = defaultHelpMD

func seedOrFlagSetup(store *Store) {
	_, wikiErr := os.Stat(filepath.Join(store.dir, ".wiki.yaml"))
	_, helpErr := os.Stat(filepath.Join(store.dir, ".help.md"))
	if !hasNamespace(store.dir) || wikiErr != nil || helpErr != nil {
		store.NeedsSetup.Store(true)
	}
}

func hasNamespace(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && validNamespaceName(e.Name()) {
			return true
		}
	}
	return false
}

func HasNamespace(dir string) bool { return hasNamespace(dir) }
func SeedOrFlagSetup(store *Store) { seedOrFlagSetup(store) }

func Open(cfg Options) (*Store, error) {
	repo, err := git.PlainOpen(cfg.RepoDir)
	if err == nil {
		remote := ""
		if cfg.Git.RemoteURL != "" {
			remote = "origin"
		}

		var auth *githttp.BasicAuth
		if cfg.Git.RemoteURL != "" && cfg.Git.Token != "" {
			auth = &githttp.BasicAuth{Username: cfg.Git.User, Password: cfg.Git.Token}
		}

		store := &Store{
			repo:      repo,
			dir:       cfg.RepoDir,
			remote:    remote,
			auth:      auth,
			syncState: "ok",
		}
		store.lastSuccessUnix.Store(time.Now().Unix())
		seedOrFlagSetup(store)
		slog.Info("opened existing repo", "dir", cfg.RepoDir, "remote", remote != "")
		return store, nil
	}

	if cfg.Git.RemoteURL != "" {
		var auth *githttp.BasicAuth
		if cfg.Git.Token != "" {
			auth = &githttp.BasicAuth{Username: cfg.Git.User, Password: cfg.Git.Token}
		}

		cloneOpts := &git.CloneOptions{
			URL:  cfg.Git.RemoteURL,
			Auth: auth,
		}

		cloneCtx, cloneCancel := context.WithTimeout(context.Background(), networkTimeout())
		repo, err := git.PlainCloneContext(cloneCtx, cfg.RepoDir, false, cloneOpts)
		cloneCancel()
		if err != nil {
			if err == transport.ErrEmptyRemoteRepository {
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

		slog.Info("cloned repo", "dir", cfg.RepoDir, "remote", cfg.Git.RemoteURL)

		seedOrFlagSetup(store)

		return store, nil
	}

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

	if cfg.Git.RemoteURL != "" {
		_, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{cfg.Git.RemoteURL}})
		if err != nil {
			return nil, fmt.Errorf("creating remote: %w", err)
		}

		store.remote = "origin"
		if cfg.Git.Token != "" {
			store.auth = &githttp.BasicAuth{Username: cfg.Git.User, Password: cfg.Git.Token}
		}
	}

	seedOrFlagSetup(store)

	return store, nil
}

// Read reads path's content and blob hash.
func (s *Store) Read(path string) (content []byte, blobHash string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	content, err = s.readRepositoryFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("reading %s: %w", path, err)
	}

	hash := plumbing.ComputeHash(plumbing.BlobObject, content)
	return content, hash.String(), nil
}

func (s *Store) readHashLocked(path string) (string, error) {
	content, err := s.readRepositoryFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	hash := plumbing.ComputeHash(plumbing.BlobObject, content)
	return hash.String(), nil
}

// ErrConflict is returned by SaveChecked when the file's current blob hash doesn't match the caller's
// expected hash.
var ErrConflict = errors.New("optimistic lock conflict")

// SaveChecked atomically saves content when expectedHash matches oldPath, optionally moving it to newPath.
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

	if oldPath != newPath {
		newHash, err := s.readHashLocked(newPath)
		if err != nil {
			return "", err
		}
		if newHash != "" {
			return "", ErrConflict
		}
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

// SaveAll writes files in one Git commit.
func (s *Store) SaveAll(files map[string][]byte, message, authorName, authorEmail string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	changed := make([]string, 0, len(paths))
	for _, path := range paths {
		content := files[path]
		if existing, err := s.readRepositoryFile(path); err == nil && string(existing) == string(content) {
			continue
		}
		if err := s.writeRepositoryFile(path, content); err != nil {
			return nil, fmt.Errorf("writing file: %w", err)
		}
		changed = append(changed, path)
	}

	if len(changed) > 0 {
		wt, err := s.repo.Worktree()
		if err != nil {
			return nil, fmt.Errorf("getting worktree: %w", err)
		}
		for _, path := range changed {
			if err := wt.AddWithOptions(&git.AddOptions{Path: path, SkipStatus: true}); err != nil {
				return nil, fmt.Errorf("adding file to index: %w", err)
			}
		}
		preHead := s.headHash()
		when := time.Now()
		commitHash, err := wt.Commit(message, &git.CommitOptions{Author: &object.Signature{Name: authorName, Email: authorEmail, When: when}})
		if err != nil {
			return nil, fmt.Errorf("committing: %w", err)
		}
		s.noteCommit(commitHash, preHead)
		for _, path := range changed {
			s.prependHistory(path, CommitInfo{Hash: commitHash.String(), Message: message, Author: authorName, When: when})
		}
		if s.remote != "" {
			s.syncState = "pending"
			s.startPush()
		}
	}

	hashes := make(map[string]string, len(files))
	for path, content := range files {
		hashes[path] = plumbing.ComputeHash(plumbing.BlobObject, content).String()
	}
	return hashes, nil
}

func (s *Store) saveLocked(path string, content []byte, message, authorName, authorEmail string) (blobHash string, err error) {
	// Skip the write and commit entirely if the content is unchanged, so an
	// untouched file (including its on-disk mode) never produces a no-op commit.
	if existing, readErr := s.readRepositoryFile(path); readErr == nil && string(existing) == string(content) {
		slog.Debug("save skipped, content unchanged", "path", path)
		hash := plumbing.ComputeHash(plumbing.BlobObject, content)
		return hash.String(), nil
	}

	err = s.writeRepositoryFile(path, content)
	if err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}

	wt, err := s.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("getting worktree: %w", err)
	}

	// SkipStatus avoids hashing the whole worktree after writing path.
	err = wt.AddWithOptions(&git.AddOptions{Path: path, SkipStatus: true})
	if err != nil {
		return "", fmt.Errorf("adding file to index: %w", err)
	}

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

	hash := plumbing.ComputeHash(plumbing.BlobObject, content)
	blobHash = hash.String()

	if s.remote != "" {
		s.syncState = "pending"
		s.startPush()
	}

	return blobHash, nil
}

// Remove deletes path from disk and the git index, committing the removal.
func (s *Store) Remove(path, message, authorName, authorEmail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeLocked(path, message, authorName, authorEmail)
}

var errInvalidNamespaceName = errors.New("invalid namespace name")
var ErrInvalidNamespaceName = errInvalidNamespaceName

// DeleteNamespace removes a configured namespace only when its configuration is the directory's sole regular
// entry.
func (s *Store) DeleteNamespace(name, message, authorName, authorEmail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !validNamespaceName(name) {
		return errInvalidNamespaceName
	}

	namespaceDir := filepath.Join(s.dir, name)
	info, err := os.Lstat(namespaceDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unable to inspect namespace contents; deletion was not performed")
	}
	entries, err := os.ReadDir(namespaceDir)
	if err != nil {
		return errors.New("unable to inspect namespace contents; deletion was not performed")
	}

	configPath := namespaceConfigPath(name)
	configFound := false
	for _, entry := range entries {
		if entry.Name() == namespaceConfigFile {
			if !entry.Type().IsRegular() {
				return errors.New("the namespace configuration is not a regular file; deletion was not performed")
			}
			configFound = true
			continue
		}
		if strings.HasSuffix(entry.Name(), ".md") {
			return errors.New("this namespace still contains indexed pages; deletion was not performed")
		}
		return errors.New("this namespace still contains directory content; deletion was not performed")
	}
	if !configFound {
		return errors.New("this namespace has no configuration to delete")
	}

	config, err := s.readRepositoryFile(configPath)
	if err != nil {
		return errors.New("unable to read namespace configuration; deletion was not performed")
	}
	restore := func() error {
		_, restoreErr := s.saveLocked(configPath, config, "Restore namespace config "+configPath, authorName, authorEmail)
		return restoreErr
	}
	if err := s.removeLocked(configPath, message, authorName, authorEmail); err != nil {
		if restoreErr := restore(); restoreErr != nil {
			return fmt.Errorf("failed to remove namespace config: %w (restoring namespace config: %v)", err, restoreErr)
		}
		return fmt.Errorf("failed to remove namespace config: %w", err)
	}
	if err := os.Remove(namespaceDir); err != nil && !os.IsNotExist(err) {
		// The Store lock prevents another Store operation from changing the
		// directory, but restore the config if an external filesystem change
		// made the final directory removal fail.
		if restoreErr := restore(); restoreErr != nil {
			return fmt.Errorf("failed to remove namespace directory: %w (restoring namespace config: %v)", err, restoreErr)
		}
		return fmt.Errorf("failed to remove namespace directory: %w", err)
	}
	return nil
}

// DeleteNamespaceAll removes a configured namespace and every file beneath it in one commit.
func (s *Store) DeleteNamespaceAll(name, message, authorName, authorEmail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !validNamespaceName(name) {
		return errInvalidNamespaceName
	}
	namespaceDir := filepath.Join(s.dir, name)
	info, err := os.Lstat(namespaceDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unable to inspect namespace contents; deletion was not performed")
	}
	config := filepath.Join(namespaceDir, namespaceConfigFile)
	if info, err := os.Lstat(config); err != nil || !info.Mode().IsRegular() {
		return errors.New("this namespace has no configuration to delete")
	}

	var paths []string
	err = filepath.WalkDir(namespaceDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.dir, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return fmt.Errorf("reading namespace contents: %w", err)
	}

	wt, err := s.repo.Worktree()
	if err != nil {
		return fmt.Errorf("getting worktree: %w", err)
	}
	// wt.Status() only lists paths that differ from HEAD, so computed before
	// any of these files are removed it would omit every unmodified tracked
	// file. The index itself lists every tracked path regardless of
	// modification state, and reading it once is cheap (no per-file
	// hashing), unlike calling Status() again on each loop iteration.
	idx, err := s.repo.Storer.Index()
	if err != nil {
		return fmt.Errorf("reading index: %w", err)
	}
	tracked := make(map[string]bool, len(idx.Entries))
	for _, e := range idx.Entries {
		tracked[e.Name] = true
	}
	for _, path := range paths {
		if err := os.Remove(filepath.Join(s.dir, path)); err != nil {
			return fmt.Errorf("removing namespace file: %w", err)
		}
		if !tracked[path] {
			continue
		}
		if _, err := wt.Remove(path); err != nil {
			return fmt.Errorf("removing namespace file from index: %w", err)
		}
		delete(s.historyCache, path)
	}
	if err := os.RemoveAll(namespaceDir); err != nil {
		return fmt.Errorf("removing namespace directory: %w", err)
	}

	preHead := s.headHash()
	commitHash, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{Name: authorName, Email: authorEmail, When: time.Now()},
	})
	if errors.Is(err, git.ErrEmptyCommit) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("committing: %w", err)
	}
	s.noteCommit(commitHash, preHead)
	if s.remote != "" {
		s.syncState = "pending"
		s.startPush()
	}
	return nil
}

// OpenExtractedAttachment opens an internal sidecar for a valid source attachment.
func (s *Store) OpenExtractedAttachment(path string) (*os.File, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sidecar := extractedAttachmentPath(path)
	if sidecar == "" {
		return nil, fmt.Errorf("invalid attachment path %q", path)
	}
	return s.openRepositoryFile(sidecar, os.O_RDONLY, 0, false)
}

func (s *Store) removeLocked(path, message, authorName, authorEmail string) error {
	if _, err := s.readRepositoryFile(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspecting file to remove: %w", err)
	}
	if err := s.removeRepositoryFile(path); err != nil {
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
		s.startPush()
	}
	return nil
}

func (s *Store) prependHistory(path string, entry CommitInfo) {
	cached, ok := s.historyCache[path]
	if !ok {
		return
	}
	s.historyCache[path] = append([]CommitInfo{entry}, cached...)
}

func (s *Store) push() {
	if !s.pushMu.TryLock() {
		s.pushDirty.Store(true)
		return
	}
	defer s.pushMu.Unlock()

	for {
		s.pushDirty.Store(false)
		s.pushOnce()
		if !s.pushDirty.Load() {
			return
		}
	}
}

func (s *Store) startPush() {
	s.pushWG.Go(func() {
		s.push()
	})
}

// WaitForPushes waits for asynchronous pushes already started by saves.
func (s *Store) WaitForPushes(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.pushWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) pushOnce() {
	s.mu.RLock()
	repo, auth := s.repo, s.auth
	s.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), networkTimeout())
	defer cancel()

	err := repo.PushContext(ctx, &git.PushOptions{
		RemoteName: "origin",
		Auth:       auth,
	})

	s.mu.Lock()
	defer s.mu.Unlock()
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
		s.failSync("push", err)
	}
}

// List returns every ordinary page path in the store (repo-relative, "/"-separated), sorted: top-level .md
// files plus, recursively, .md files inside any non-dot-prefixed subdirectory other than attachments/
// (assets, not pages).
func (s *Store) List() ([]string, error) {
	lockWaitDone := traceSlowLockWait("list repository")
	s.mu.RLock()
	lockWaitDone()
	defer s.mu.RUnlock()

	var paths []string
	walkStarted := time.Now()
	slog.Debug("repository walk started", "path", s.dir)
	err := filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			slog.Debug("repository walk entering directory", "path", path)
		}
		if path == s.dir {
			return nil
		}
		name := d.Name()
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || name == attachmentsDir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || filepath.Ext(name) != ".md" {
			return nil
		}
		rel, err := filepath.Rel(s.dir, path)
		if err != nil {
			return err
		}
		// Every page lives in a namespace, so a top-level .md file is repo
		// furniture (rootReadmeMD) rather than content.
		if !strings.Contains(rel, string(filepath.Separator)) {
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading directory: %w", err)
	}

	sort.Strings(paths)
	slog.Debug("repository walk completed", "path", s.dir, "pages", len(paths), "duration", time.Since(walkStarted))
	return paths, nil
}

// ListAttachments returns regular source attachments below attachments/, sorted by repository-relative path.
func (s *Store) ListAttachments() ([]string, error) {
	lockWaitDone := traceSlowLockWait("list attachments")
	s.mu.RLock()
	lockWaitDone()
	defer s.mu.RUnlock()

	root := filepath.Join(s.dir, attachmentsDir)
	if info, err := os.Lstat(root); os.IsNotExist(err) {
		return []string{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("inspecting attachments directory: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("attachments directory is not a real directory")
	}
	var paths []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			slog.Debug("attachment walk entering directory", "path", path)
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".hmd" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(s.dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, _, ok := parseAttachmentPath(rel); !ok {
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading attachments directory: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

// OpenAttachment validates and opens an attachment while the store lock is held.
func (s *Store) OpenAttachment(path string) (*os.File, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, _, ok := parseAttachmentPath(path); !ok {
		return nil, "", fmt.Errorf("invalid attachment path %q", path)
	}
	file, err := s.openRepositoryFile(path, os.O_RDONLY, 0, false)
	if err != nil {
		return nil, "", err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, "", err
	}
	hash := sha1.New()
	_, _ = fmt.Fprintf(hash, "blob %d\x00", info.Size())
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return nil, "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, "", err
	}
	return file, hex.EncodeToString(hash.Sum(nil)), nil
}

// ListHidden returns every hidden page's path (repo-relative, "/"-separated), sorted.
func (s *Store) ListHidden() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var paths []string
	err := filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == s.dir {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(s.dir, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel == ".git" || rel == "attachments" {
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(rel)
		if filepath.Ext(rel) != ".md" || !strings.HasPrefix(base, ".") {
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading directory: %w", err)
	}

	sort.Strings(paths)
	return paths, nil
}

func (s *Store) History(path string) ([]CommitInfo, error) {
	// A cache hit needs only a read lock and does not wait for network operations.
	s.mu.RLock()
	cached, ok := s.historyCache[path]
	s.mu.RUnlock()
	if ok {
		return cached, nil
	}

	// The cache miss walk runs under the write lock to stay consistent with concurrent saves.
	s.mu.Lock()
	defer s.mu.Unlock()

	if cached, ok := s.historyCache[path]; ok {
		return cached, nil
	}

	iter, err := s.repo.Log(&git.LogOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting log: %w", err)
	}

	histories := make(map[string][]CommitInfo)
	if err := iter.ForEach(func(c *object.Commit) error {
		current, err := c.Tree()
		if err != nil {
			return err
		}
		previous := &object.Tree{}
		if c.NumParents() > 0 {
			parent, err := c.Parent(0)
			if err != nil {
				return err
			}
			previous, err = parent.Tree()
			if err != nil {
				return err
			}
		}
		patch, err := previous.Patch(current)
		if err != nil {
			return err
		}
		entry := CommitInfo{
			Hash:    c.Hash.String(),
			Message: c.Message,
			Author:  c.Author.Name,
			When:    c.Author.When,
		}
		for _, filePatch := range patch.FilePatches() {
			from, to := filePatch.Files()
			paths := map[string]bool{}
			if from != nil {
				paths[from.Path()] = true
			}
			if to != nil {
				paths[to.Path()] = true
			}
			for path := range paths {
				histories[path] = append(histories[path], entry)
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walking history: %w", err)
	}

	s.historyCache = histories
	return histories[path], nil
}

// CommitDetail is CommitInfo plus the files the commit touched, for the MCP recent_changes tool.
type CommitDetail struct {
	Hash    string
	Message string
	Author  string
	When    time.Time
	Files   []string
}

// RecentCommits walks the log head-first and returns the newest n commits with the files each touched.
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

func (s *Store) headHash() plumbing.Hash {
	ref, err := s.repo.Head()
	if err != nil {
		return plumbing.ZeroHash
	}
	return ref.Hash()
}

func (s *Store) noteCommit(newHead, preHead plumbing.Hash) {
	if s.knownHead != preHead {
		s.historyCache = nil
	}
	s.knownHead = newHead
}

// DropHistoryOnExternalCommit invalidates history after an external commit.
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

	if _, err := repositoryPathParts(path); err != nil {
		return nil, err
	}
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

func (s *Store) failSync(stage string, err error) {
	s.syncState = "failed"
	s.syncErr = err.Error()
	slog.Warn("sync failed", "stage", stage, "err", err)
}

func (s *Store) SyncState() (state, detail string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.syncState, s.syncErr
}

// LastSyncUnix returns the Unix timestamp of the last successful sync.
func (s *Store) LastSyncUnix() int64 {
	return s.lastSuccessUnix.Load()
}

// PushNow performs an immediate synchronous push (the ">sync" palette verb) and returns the resulting sync
// state.
func (s *Store) PushNow() (state, detail string) {
	s.mu.Lock()
	noRemote := s.remote == ""
	if !noRemote {
		s.syncState = "pending"
	}
	s.mu.Unlock()
	if !noRemote {
		// Block on pushMu rather than going through push()'s TryLock+dirty
		// coalescing: the caller is waiting synchronously for a result, so
		// wait out any in-flight push, then run one guaranteed push of our
		// own rather than piggy-backing on whichever state the other push
		// observed.
		s.pushMu.Lock()
		s.pushOnce()
		s.pushMu.Unlock()
	}
	return s.SyncState()
}

// UpdateRemote reconfigures the store's remote URL and auth credentials on the live repository.
func (s *Store) UpdateRemote(cfg Options) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cfg.Git.RemoteURL == "" {
		if s.remote != "" {
			if err := s.repo.DeleteRemote("origin"); err != nil {
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
			URLs: []string{cfg.Git.RemoteURL},
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
			URLs: []string{cfg.Git.RemoteURL},
		})
		if err != nil {
			return fmt.Errorf("recreating remote: %w", err)
		}
	}

	s.remote = "origin"
	if cfg.Git.Token != "" {
		s.auth = &githttp.BasicAuth{
			Username: cfg.Git.User,
			Password: cfg.Git.Token,
		}
	} else {
		s.auth = nil
	}
	s.syncState = "ok"
	slog.Info("remote configured", "url", cfg.Git.RemoteURL)
	return nil
}

// FetchResult describes what changed when a fetch+ff brought in remote commits.
type FetchResult struct {
	ChangedPaths []string
	Commits      []CommitInfo
}

// FetchAndFF fetches from origin and fast-forwards the local branch if the remote is ahead.
func (s *Store) FetchAndFF() (FetchResult, error) {
	fetchStarted := time.Now()
	slog.Debug("fetch and fast-forward started")
	s.mu.RLock()
	remote, repo, auth := s.remote, s.repo, s.auth
	s.mu.RUnlock()

	if remote == "" {
		return FetchResult{}, nil
	}

	if !s.fetchMu.TryLock() {
		return FetchResult{}, nil
	}
	defer s.fetchMu.Unlock()

	if time.Since(time.Unix(0, s.lastFetchNano.Load())) < fetchThrottleDuration() {
		return FetchResult{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), networkTimeout())
	defer cancel()

	err := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
	})
	slog.Debug("remote fetch completed", "duration", time.Since(fetchStarted), "err", err)
	s.lastFetchNano.Store(time.Now().UnixNano())
	if err != nil && err != git.NoErrAlreadyUpToDate {
		s.mu.Lock()
		s.failSync("fetch", err)
		s.mu.Unlock()
		return FetchResult{}, err
	}

	// The rest (HEAD comparison through worktree reset) is local and fast,
	// so it runs under the full write lock like the rest of Store's mutating
	// operations.
	lockWaitDone := traceSlowLockWait("apply fetched repository state")
	s.mu.Lock()
	lockWaitDone()
	defer s.mu.Unlock()

	headRef, err := s.repo.Head()
	if err != nil {
		s.failSync("head", err)
		return FetchResult{}, err
	}
	localHash := headRef.Hash()

	remoteRefName := plumbing.NewRemoteReferenceName("origin", headRef.Name().Short())
	remoteRef, err := s.repo.Reference(remoteRefName, true)
	if err != nil {
		s.failSync("remote-ref", err)
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
		s.failSync("ancestor-check", err)
		return FetchResult{}, err
	}

	if !localIsAncestor {
		remoteIsAncestor, err := s.isAncestor(remoteHash, localHash)
		if err != nil {
			s.failSync("ancestor-check", err)
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

	diffStarted := time.Now()
	slog.Debug("fetch diffing commits", "local", localHash.String()[:8], "remote", remoteHash.String()[:8])
	result, err := s.diffCommits(localHash, remoteHash)
	if err != nil {
		s.failSync("diff-commits", err)
		return FetchResult{}, err
	}
	slog.Debug("fetch diffed commits", "duration", time.Since(diffStarted), "paths", len(result.ChangedPaths), "commits", len(result.Commits))

	slog.Debug("fetch opening worktree")
	wt, err := s.repo.Worktree()
	if err != nil {
		s.failSync("worktree", err)
		return FetchResult{}, err
	}
	resetStarted := time.Now()
	slog.Debug("fetch resetting worktree", "remote", remoteHash.String()[:8])
	if err := wt.Reset(&git.ResetOptions{
		Commit: remoteHash,
		Mode:   git.HardReset,
	}); err != nil {
		s.failSync("worktree-reset", err)
		return FetchResult{}, err
	}
	slog.Debug("fetch reset worktree", "duration", time.Since(resetStarted))

	// Only the paths this fast-forward actually touched can be stale;
	// dropping the whole cache would force a full path-filtered git log walk
	// on the next view of every other page too.
	for _, p := range result.ChangedPaths {
		delete(s.historyCache, p)
	}
	s.knownHead = remoteHash
	s.syncState = "ok"
	s.syncErr = ""
	slog.Info("fast-forwarded", "paths", len(result.ChangedPaths), "commits", len(result.Commits))
	return result, nil
}

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
