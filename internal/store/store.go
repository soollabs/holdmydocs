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
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/filesystem"
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
	ReadOnly      bool
	DefaultBranch string
	Git           GitOptions
}

type Store struct {
	readOnly  bool
	repo      *git.Repository
	dir       string
	remote    string
	auth      *githttp.BasicAuth
	mu        sync.RWMutex
	syncState string
	syncErr   string
	// historyCache caches history results by path.
	historyCache map[string][]CommitInfo
	// historyGen counts history cache invalidations, protected by mu. A cold
	// walk publishes its result only if no invalidation happened meanwhile.
	historyGen uint64
	// knownHead is the HEAD hash after the last commit or reset made by this process, protected by mu.
	knownHead plumbing.Hash
	walkMu    sync.Mutex
	// historyWalk is the cold history walk in progress, protected by walkMu.
	historyWalk *historyWalk
	NeedsSetup  atomic.Bool
	// ForceSetup requests displaying the setup modal.
	ForceSetup      atomic.Bool
	lastSuccessUnix atomic.Int64

	pushMu sync.Mutex
	// pushDirty requests another push after the current push completes.
	pushDirty atomic.Bool
	pushWG    sync.WaitGroup

	fetchMu sync.Mutex
	// lastFetchNano supports fetch throttling.
	lastFetchNano atomic.Int64
}

const (
	attachmentsDir      = "attachments"
	namespaceConfigFile = ".namespace.yaml"
	slowLockWait        = time.Second
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

func validNamespaceName(name string) bool {
	return name != "" && name != "_" && name != attachmentsDir &&
		!strings.ContainsAny(name, `/\`) && !strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "_")
}

func (s *Store) Dir() string { return s.dir }

// ReadOnly reports the immutable repository policy selected at startup.
func (s *Store) ReadOnly() bool { return s.readOnly }

var ErrReadOnly = errors.New("repository is read-only")

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
Edit the files here directly or through the app. Both end up as commits.
`

const (
	DefaultIndexPage = defaultIndexPage
	DefaultHomeMD    = defaultHomeMD
	RootReadmeMD     = rootReadmeMD
)

const defaultHelpMD = `# Help

This page covers the basics. The full guide is at https://hmd.sool.dev.

## Finding pages

Press ` + "`ctrl k`" + `, or the search icon in the top bar, to open the jump palette. Type to search, use the arrow keys to move and press enter to open. Ctrl-enter opens the page in the editor. Pressing ` + "`/`" + ` outside a text field opens the palette too.

Start with ` + "`>`" + ` to run a command: ` + "`rename`" + `, ` + "`tag`" + `, ` + "`hist`" + ` (history), ` + "`new`" + `, ` + "`health`" + `, ` + "`ns`" + ` (namespaces and templates), ` + "`sync`" + ` (push now), ` + "`pin`" + ` and ` + "`delete`" + `.

## Creating a page

Type a title into the palette that doesn't match an existing page and a "+ create page" row appears, naming the namespace it will use. Selecting it opens a blank editor for that title.

When a namespace has a **Quick-create page** setting, ` + "`ctrl j`" + ` and the **New page** button in the top bar create a page from its template straight away. See Namespaces below.

## Editing

The editor shows one column at reading width, with the title and tags above the text. ` + "`ctrl e`" + ` switches between writing and preview. The page path, the hidden flag and delete are under **page details**.

Focus mode (` + "`ctrl shift f`" + `) hides everything except the title and the text, and dims all but the current line. Its bar has buttons to make the text smaller or larger.

## Keyboard shortcuts

| Shortcut | Action |
| --- | --- |
| ` + "`ctrl k`" + ` | Open or close the jump palette |
| ` + "`/`" + ` | Open the jump palette (outside text fields) |
| ` + "`ctrl j`" + ` | Quick-create a page, when the namespace allows it |
| ` + "`ctrl e`" + ` | Edit the current page; in the editor, switch between writing and preview |
| ` + "`ctrl s`" + ` | Save (while editing) |
| ` + "`ctrl b`" + ` / ` + "`ctrl i`" + ` | Bold / italic (while editing) |
| ` + "`ctrl shift k`" + ` | Insert a wiki-link (while editing) |
| ` + "`alt 1`" + ` / ` + "`alt 2`" + ` / ` + "`alt 3`" + ` | Heading level 1 / 2 / 3 (while editing) |
| ` + "`ctrl shift f`" + ` | Turn focus mode on or off (while editing) |
| ` + "`esc`" + ` | Close the palette, or leave focus mode |

Use ` + "`cmd`" + ` instead of ` + "`ctrl`" + ` on macOS.

## Hidden pages

A filename starting with a dot (` + "`.help.md`" + `) is a hidden page. It is left out of the page list, search, tags and backlinks, but you can still view and edit it at ` + "`/_/hidden/<slug>`" + `, and ` + "`/_/hidden`" + ` lists them all. To hide or unhide a page, tick **hidden** under **page details** in the editor.

## Settings

Settings open from the menu at the right of the top bar. Your own settings are ` + "`/_/settings/appearance`" + ` (skin, palette and fonts), ` + "`/_/settings/git-author`" + ` and ` + "`/_/settings/access-tokens`" + `. With the "admin" scope you also see server, Git, MCP, OIDC single sign-on, document search, wiki, users and environment overrides.

` + "`/_/settings/wiki`" + ` edits the repository's ` + "`.wiki.yaml`" + ` (site name and landing page). Its **Re-run setup** button brings back the first-namespace prompt. When this help page differs from the built-in text for your version, admins see a dismissible banner, and the admin settings have a button to reset it.

Manage namespaces at ` + "`/_/settings/namespaces`" + `. Connected applications appear under ` + "`/_/settings/connections`" + ` when OAuth is enabled.

Everything else the app owns also lives under ` + "`/_/`" + `, so no page can take those URLs.

## Saving and sync

Every save is a Git commit, and reverting creates a new commit, so history is never rewritten. When a Git remote is configured, the wiki pushes after each save; with ` + "`sync_mode: bidirectional`" + ` it also pulls changes from the remote. The sync segment at the bottom right of the statusline shows whether the last sync is pending, ok or failed.

## File names

- One page per Markdown file, lowercase, hyphenated: ` + "`docs/running-the-app.md`" + `.
- Every page lives in a namespace directory; the slug is the path without the ` + "`.md`" + ` suffix, for example ` + "`docs/running-the-app`" + `.
- A namespace's ` + "`index`" + ` setting names the page that serves ` + "`/<namespace>/`" + `; ` + "`/`" + ` itself goes wherever the ` + "`landing`" + ` key in ` + "`.wiki.yaml`" + ` points.
- ` + "`.wiki.yaml`" + ` sits at the repository root and sets the wiki's ` + "`site_name`" + ` and ` + "`landing`" + `. It moves with the content to another hmd instance.
- Attachments live in their namespace under ` + "`<namespace>/.attachments/<page>/<file>`" + `.

## Frontmatter

Every page starts with a frontmatter block:

    ---
    title: Human-Friendly Title
    tags: one, two, three
    ---

` + "`title`" + ` is the display heading. ` + "`tags`" + ` is a comma-separated list.

` + "`pin: true`" + ` puts the page in the pinned sidebar widget for everyone, and an ordinary editor save keeps it. The palette's ` + "`pin`" + ` command is separate: it pins the page for you only, in this browser, at the top of the pages widget.

## Body

- Use ` + "`#`" + ` for the title only; start sections at ` + "`##`" + ` (h2) and subsections at ` + "`###`" + ` (h3).
- Link other pages with wiki-links: ` + "`[[Page Title]]`" + ` resolves to the matching slug.

## Table of contents

Insert ` + "`<!-- hmd:toc -->`" + ` anywhere to list every page in the current page's namespace, or ` + "`<!-- hmd:toc:tag1,tag2 -->`" + ` to list only the pages in that namespace that have any of the given tags. The list never reaches into other namespaces.

## Diagrams

Mermaid code fences render as diagrams:

` + "    ```mermaid\n    graph LR\n    A --> B\n    ```" + `

## Attachments

Permitted types are png, jpg, jpeg, gif, webp, pdf, doc, docx, xls, xlsx, ppt, pptx, odt, ods and odp. SVG is excluded because of the XSS risk. Paste or drag an image into the editor to upload it, or reference one already uploaded by its URL: ` + "`/_/attachments/<slug>/<file>`" + `.

Agents and scripts that edit the repository directly can commit a file straight to ` + "`<namespace>/.attachments/<page>/<file>`" + ` without the upload API. Only the upload endpoint enforces the type list and the SVG exclusion, so follow them by convention for direct writes too.

## Namespaces

A namespace is a top-level directory in the repo, and every page lives in one: ` + "`blog/post.md`" + ` is in the ` + "`blog`" + ` namespace. Anything deeper (` + "`blog/drafts/post.md`" + `) is a plain subfolder; namespaces are exactly one level deep.

The namespace index is ` + "`/<namespace>/`" + ` (or the bare ` + "`/<namespace>`" + `, which serves the same thing) and a page is ` + "`/<namespace>/<page>`" + `. Public namespaces are visible without signing in; private and unknown namespace indexes both return the same not-found page.

An optional ` + "`.namespace.yaml`" + ` beside the pages configures the namespace, for example:

    title: Blog
    public: true
    widgets: [pages, tags, log, page-meta, backlinks]
    new:
      template: template
      slug: '{{.Now.Format "2006-01-02"}}'

- ` + "`title`" + ` and ` + "`description`" + ` name and describe the namespace for readers and static exports.
- ` + "`public: true`" + ` makes pages readable without signing in. Everything is private by default, and to an anonymous visitor a private page looks the same as one that doesn't exist.
- ` + "`index`" + ` names a page to show at ` + "`/<namespace>/`" + ` instead of the page list, and ` + "`tree`" + ` sets the order of pages and folders in the sidebar.
- ` + "`widgets`" + ` lists the widgets to mount, in order within each slot. Omit it for the built-in set. The choices are ` + "`pages`" + `, ` + "`namespaces`" + `, ` + "`pinned`" + `, ` + "`tags`" + `, ` + "`log`" + `, ` + "`health`" + `, ` + "`page-meta`" + `, ` + "`backlinks`" + ` and ` + "`prev-entries`" + `; search, the page tree and the on-this-page outline are always shown.
- ` + "`skin`" + ` and ` + "`palette`" + ` set what anonymous readers and static exports see.
- ` + "`new`" + ` sets what the **Quick-create page** setting (` + "`ctrl j`" + `) creates here: ` + "`template`" + ` names a hidden page in the namespace to copy, and ` + "`slug`" + ` names the page it creates.
- ` + "`export`" + ` configures static export, which the docs site explains.

## Template pages

Creating a namespace from the settings page also creates a hidden template page (` + "`template`" + `, so ` + "`.blog/template.md`" + ` for ` + "`blog`" + `). Pages created in that namespace start as a copy of it, with its **title, tags and body** substituted through Go's ` + "`text/template`" + `. Three fields are available:

- ` + "`{{.Now}}`" + ` is the moment the page is created. Format it with Go's layout syntax, which is the reference time written in the shape you want: ` + "`{{.Now.Format \"2006-01-02\"}}`" + ` for 2026-07-28, ` + "`{{.Now.Format \"Monday, 2 January 2006\"}}`" + ` for the long date, ` + "`{{.Now.Format \"15:04\"}}`" + ` for the time.
- ` + "`{{.User}}`" + ` is the user who pressed ctrl-j.
- ` + "`{{.Namespace}}`" + ` is the namespace name.

The same fields name the page itself, through the namespace's slug pattern. The seeded template explains all of this in its own body. Read it, then delete it and write your own.

Manage namespaces at ` + "`/_/settings/namespaces`" + `. Saving a new name creates its configuration. **Reset namespace settings** removes only ` + "`.namespace.yaml`" + `; it keeps every page and hidden template. True deletion is available only when there are no pages or hidden files, so move or remove those first. Writing ` + "`.namespace.yaml`" + ` by hand works too; the app rescans on a timer.

MCP clients can access ordinary pages only by default; when document search is enabled they can also search attachments. Page tools accept a ` + "`namespace/page`" + ` slug, reads require read scope, and writes require write scope. Wiki settings, namespace settings, hidden templates and attachment source files are never exposed through MCP.

## Skins

A skin is how the app looks: typography, spacing, markers and what the statusline shows. The namespace decides which widgets mount, and a skin never changes how or where pages are stored, so the same repo opens correctly under any of them. Pick one on ` + "`/_/settings/appearance`" + `: ` + "`phosphor`" + ` (default, terminal), ` + "`newsprint`" + ` (broadsheet serif), ` + "`journal`" + ` (writing first: serif, wide measure), ` + "`soft`" + ` (rounded, low-contrast sans) or ` + "`bare`" + ` (stripped down). Each skin comes with the colour **palette** it was designed for, and you can pick a different one afterwards.
`

const DefaultHelpMD = defaultHelpMD

func seedOrFlagSetup(store *Store) {
	if store.readOnly {
		return
	}
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
			if cfg.ReadOnly {
				origin, remoteErr := repo.Remote("origin")
				if remoteErr != nil || !slices.Contains(origin.Config().URLs, cfg.Git.RemoteURL) {
					return nil, fmt.Errorf("%w: configured URL must match the existing origin; use a fresh repo_dir to clone a different repository", ErrReadOnly)
				}
			}
			remote = "origin"
		}

		var auth *githttp.BasicAuth
		if cfg.Git.RemoteURL != "" && cfg.Git.Token != "" {
			auth = &githttp.BasicAuth{Username: cfg.Git.User, Password: cfg.Git.Token}
		}

		store := &Store{
			readOnly:  cfg.ReadOnly,
			repo:      repo,
			dir:       cfg.RepoDir,
			remote:    remote,
			auth:      auth,
			syncState: "ok",
		}
		if remote == "" {
			store.syncState = "no remote"
		}
		store.lastSuccessUnix.Store(time.Now().Unix())
		// Without the opening head, the first refresh or save would mistake it
		// for an external commit and drop the history cache.
		store.knownHead = store.headHash()
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
			if errors.Is(err, transport.ErrEmptyRemoteRepository) {
				goto init_empty_remote
			}
			return nil, fmt.Errorf("cloning repo: %w", err)
		}

		store := &Store{
			readOnly:  cfg.ReadOnly,
			repo:      repo,
			dir:       cfg.RepoDir,
			remote:    "origin",
			auth:      auth,
			syncState: "ok",
		}
		store.lastSuccessUnix.Store(time.Now().Unix())
		store.knownHead = store.headHash()

		slog.Info("cloned repo", "dir", cfg.RepoDir, "remote", cfg.Git.RemoteURL)

		seedOrFlagSetup(store)

		return store, nil
	}

init_empty_remote:
	if cfg.ReadOnly {
		return nil, fmt.Errorf("%w: provide an existing repository or a non-empty remote", ErrReadOnly)
	}
	defaultBranch := cfg.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	repo, err = git.PlainInitWithOptions(cfg.RepoDir, &git.PlainInitOptions{
		DefaultBranch: plumbing.NewBranchReferenceName(defaultBranch),
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
	if s.readOnly {
		return "", ErrReadOnly
	}
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
	if s.readOnly {
		return nil, ErrReadOnly
	}
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
	if s.readOnly {
		return "", ErrReadOnly
	}
	// Avoid no-op commits when content is unchanged.
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

// RemoveChecked deletes path like Remove, but only while its blob hash is
// still expectedHash; otherwise it returns ErrConflict without a commit.
func (s *Store) RemoveChecked(path, expectedHash, message, authorName, authorEmail string) error {
	if s.readOnly {
		return ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	currentHash, err := s.readHashLocked(path)
	if err != nil {
		return err
	}
	if currentHash != expectedHash {
		return ErrConflict
	}
	return s.removeLocked(path, message, authorName, authorEmail)
}

var errInvalidNamespaceName = errors.New("invalid namespace name")
var ErrInvalidNamespaceName = errInvalidNamespaceName

// NamespaceFileCount counts the files beneath a namespace other than its
// configuration, i.e. what DeleteNamespace would remove besides the config.
func (s *Store) NamespaceFileCount(name string) (int, error) {
	if !validNamespaceName(name) {
		return 0, errInvalidNamespaceName
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	namespaceDir := filepath.Join(s.dir, name)
	count := 0
	err := filepath.WalkDir(namespaceDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && path != filepath.Join(namespaceDir, namespaceConfigFile) {
			count++
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return count, err
}

// DeleteNamespace removes a configured namespace and every file beneath it in one commit.
func (s *Store) DeleteNamespace(name, message, authorName, authorEmail string) error {
	if s.readOnly {
		return ErrReadOnly
	}
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
	// Read the index so unmodified tracked files are also removed.
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
	if s.readOnly {
		return ErrReadOnly
	}
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
	if s.readOnly {
		return
	}
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
	if errors.Is(err, git.NoErrAlreadyUpToDate) || err == nil {
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

// List returns sorted ordinary page paths, excluding attachments and hidden files.
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

// OpenAttachment validates and opens an attachment while the store lock is held.
func (s *Store) OpenAttachment(path string) (*os.File, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, _, ok := ParseAttachmentPath(path); !ok {
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
			if rel == ".git" || rel == attachmentsDir || d.Name() == namespaceAttachmentDir {
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

// historyWalk is a cold history walk from one HEAD, shared by every caller
// that misses the cache while it runs.
type historyWalk struct {
	head      plumbing.Hash
	done      chan struct{}
	histories map[string][]CommitInfo
	err       error
}

// historyWalkHook, when set by tests, runs inside the walk after the first commit.
var historyWalkHook func()

func (s *Store) History(path string) ([]CommitInfo, error) {
	// A cache hit needs only a read lock and does not wait for network operations.
	s.mu.RLock()
	cached, ok := s.historyCache[path]
	gen, head := s.historyGen, s.headHash()
	s.mu.RUnlock()
	if ok {
		return cached, nil
	}

	// The walk holds no Store lock, so a cold cache never blocks reads, saves
	// or the refresh worker. Callers missing the cache meanwhile share it.
	s.walkMu.Lock()
	walk := s.historyWalk
	leader := walk == nil || walk.head != head
	if leader {
		// A walk that finished since the miss above has already published
		// its result, so look again rather than walk a second time.
		s.mu.RLock()
		cached, ok := s.historyCache[path]
		s.mu.RUnlock()
		if ok {
			s.walkMu.Unlock()
			return cached, nil
		}
		walk = &historyWalk{head: head, done: make(chan struct{})}
		s.historyWalk = walk
	}
	s.walkMu.Unlock()
	if !leader {
		<-walk.done
		if walk.err != nil {
			return nil, walk.err
		}
		return walk.histories[path], nil
	}

	walk.histories, walk.err = s.walkHistory(head)
	// A commit or reset during the walk makes its result stale; it still
	// answers this call but is not cached. The result is published before the
	// walk is cleared, so a later miss either joins the walk or finds it cached.
	if walk.err == nil {
		s.mu.Lock()
		if s.historyGen == gen {
			s.historyCache = walk.histories
		}
		s.mu.Unlock()
	}
	s.walkMu.Lock()
	if s.historyWalk == walk {
		s.historyWalk = nil
	}
	s.walkMu.Unlock()
	close(walk.done)
	if walk.err != nil {
		return nil, walk.err
	}
	return walk.histories[path], nil
}

// IsNoCommits reports whether err means the repository has no commits yet, so
// no page has a history, rather than a failure to read one.
func IsNoCommits(err error) bool { return errors.Is(err, plumbing.ErrReferenceNotFound) }

// WarmHistory fills the history cache so the first page view does not walk.
func (s *Store) WarmHistory() error {
	_, err := s.History("")
	return err
}

// walkHistory lists the commits touching each path, newest first, from head.
// It uses a private object storage: by default go-git reopens a pack or loose
// object file for every object it reads, which is slow on network storage,
// and the shared storage is not safe for concurrent use with kept
// descriptors. Changed paths come from a tree diff without rename detection,
// which yields the same paths as a patch without reading blobs.
func (s *Store) walkHistory(head plumbing.Hash) (map[string][]CommitInfo, error) {
	started := time.Now()
	if head.IsZero() {
		return nil, fmt.Errorf("getting log: %w", plumbing.ErrReferenceNotFound)
	}
	shared, ok := s.repo.Storer.(*filesystem.Storage)
	if !ok {
		return nil, fmt.Errorf("walking history: unsupported storage %T", s.repo.Storer)
	}
	storage := filesystem.NewStorageWithOptions(shared.Filesystem(), cache.NewObjectLRUDefault(), filesystem.Options{KeepDescriptors: true})
	defer func() { _ = storage.Close() }()
	objects := cacheFirstStorage{Storage: storage, cache: cache.NewObjectLRUDefault()}

	commit, err := object.GetCommit(objects, head)
	if err != nil {
		return nil, fmt.Errorf("getting log: %w", err)
	}

	histories := make(map[string][]CommitInfo)
	commits := 0
	if err := object.NewCommitPreorderIter(commit, nil, nil).ForEach(func(c *object.Commit) error {
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
		changes, err := object.DiffTreeWithOptions(context.Background(), previous, current, &object.DiffTreeOptions{})
		if err != nil {
			return err
		}
		entry := CommitInfo{
			Hash:    c.Hash.String(),
			Message: c.Message,
			Author:  c.Author.Name,
			When:    c.Author.When,
		}
		paths := map[string]bool{}
		for _, change := range changes {
			if change.From.Name != "" {
				paths[change.From.Name] = true
			}
			if change.To.Name != "" {
				paths[change.To.Name] = true
			}
		}
		for path := range paths {
			histories[path] = append(histories[path], entry)
		}
		commits++
		if commits == 1 && historyWalkHook != nil {
			historyWalkHook()
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walking history: %w", err)
	}
	slog.Debug("history walk completed", "commits", commits, "paths", len(histories), "duration", time.Since(started))
	return histories, nil
}

// cacheFirstStorage answers object reads from its cache before the storage,
// which otherwise opens the object's file even when it has the object cached.
type cacheFirstStorage struct {
	*filesystem.Storage
	cache cache.Object
}

func (s cacheFirstStorage) EncodedObject(t plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	if obj, ok := s.cache.Get(h); ok && (t == plumbing.AnyObject || obj.Type() == t) {
		return obj, nil
	}
	obj, err := s.Storage.EncodedObject(t, h)
	if err == nil {
		s.cache.Put(obj)
	}
	return obj, err
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
	s.historyGen++
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
		s.historyGen++
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
	if s.readOnly {
		return "read-only", ErrReadOnly.Error()
	}
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
	if s.readOnly {
		return ErrReadOnly
	}
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
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
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
	if s.readOnly {
		status, err := wt.Status()
		if err != nil {
			s.failSync("worktree-status", err)
			return FetchResult{}, err
		}
		if !status.IsClean() {
			err := errors.New("read-only refresh refused: worktree has uncommitted changes")
			s.failSync("worktree-status", err)
			return FetchResult{}, err
		}
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
	s.historyGen++
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

// changedPaths lists every path that differs between two commits, including
// both sides of a rename. Only names are needed, so it diffs the trees
// directly: Commit.Patch also detects renames and builds line-level patches
// for every changed file, which dominated fast-forward time after a large sync
// and ran while the store write lock was held.
func changedPaths(oldCommit, newCommit *object.Commit) ([]string, error) {
	oldTree, err := oldCommit.Tree()
	if err != nil {
		return nil, err
	}
	newTree, err := newCommit.Tree()
	if err != nil {
		return nil, err
	}
	changes, err := object.DiffTreeWithOptions(context.Background(), oldTree, newTree, &object.DiffTreeOptions{})
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, change := range changes {
		from, to := change.From.Name, change.To.Name
		if from != "" {
			changed = append(changed, from)
		}
		if to != "" && to != from {
			changed = append(changed, to)
		}
	}
	return changed, nil
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

	changed, err := changedPaths(oldCommit, newCommit)
	if err != nil {
		return FetchResult{}, err
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
