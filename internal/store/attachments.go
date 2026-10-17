package store

// This file is the only code that knows where attachments are stored. Every
// other package names an attachment by its owning page and filename, or by
// the opaque Path of an Attachment it was handed, and asks the store.
//
// Attachments live in each namespace at <namespace>/.attachments/<page>/<file>,
// with extracted text beside them in .hmd/extracted/<file>.txt. The previous
// layout, attachments/<namespace>/<page>/<file>, is still read until an admin
// migrates it; a file in the namespace layout wins over a legacy copy.

import (
	"errors"
	"fmt"
	"hmd/internal/wiki"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	namespaceAttachmentDir = ".attachments"
	extractedAttachmentDir = ".hmd/extracted"
)

// Attachment is one stored attachment. Path is its repository-relative path:
// callers may use it as an opaque key and hand it back to the store, but must
// not build or interpret it.
type Attachment struct {
	Slug, Filename, Path string
}

// ParseAttachmentPath maps a repository-relative path, such as one touched by
// a commit or kept as an index key, to the attachment's owning page and
// filename. Sidecars and other files are rejected.
func ParseAttachmentPath(path string) (slug, filename string, ok bool) {
	dir, filename, ok := cutLast(path)
	if !ok || !validAttachmentFilename(filename) {
		return "", "", false
	}
	if rest, legacy := strings.CutPrefix(dir, attachmentsDir+"/"); legacy {
		slug = rest
	} else {
		namespace, page, found := strings.Cut(dir, "/"+namespaceAttachmentDir+"/")
		if !found || !wiki.ValidNamespaceName(namespace) {
			return "", "", false
		}
		slug = wiki.NamespaceSlug(namespace, page)
	}
	if !wiki.ValidPageSlug(slug) {
		return "", "", false
	}
	return slug, filename, true
}

func attachmentPath(slug, filename string) string {
	namespace, page := wiki.NamespaceFor(slug)
	return namespace + "/" + namespaceAttachmentDir + "/" + page + "/" + filename
}

func legacyAttachmentPath(slug, filename string) string {
	return attachmentsDir + "/" + slug + "/" + filename
}

func isLegacyAttachmentPath(path string) bool {
	return strings.HasPrefix(path, attachmentsDir+"/")
}

// extractedAttachmentPath is the sidecar kept beside a source attachment in
// the same layout; "" if path is not a source attachment.
func extractedAttachmentPath(path string) string {
	if _, _, ok := ParseAttachmentPath(path); !ok {
		return ""
	}
	dir, filename, _ := cutLast(path)
	return dir + "/" + extractedAttachmentDir + "/" + filename + ".txt"
}

// migratedAttachmentPath maps any file below the legacy tree, sidecars
// included, to its place in the namespace layout.
func migratedAttachmentPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, attachmentsDir+"/")
	if !ok {
		return "", false
	}
	namespace, inner, ok := strings.Cut(rest, "/")
	if !ok || !wiki.ValidNamespaceName(namespace) || inner == "" {
		return "", false
	}
	return namespace + "/" + namespaceAttachmentDir + "/" + inner, true
}

func cutLast(path string) (dir, name string, ok bool) {
	i := strings.LastIndexByte(path, '/')
	if i < 1 || i == len(path)-1 {
		return "", "", false
	}
	return path[:i], path[i+1:], true
}

func validAttachmentFilename(name string) bool {
	return name != "" && utf8.ValidString(name) && !strings.ContainsFunc(name, unicode.IsControl) &&
		!strings.ContainsAny(name, `/\`) && !strings.HasPrefix(name, ".")
}

// FindAttachment resolves a page's attachment to the copy that is served:
// the namespace layout when it is there, otherwise the legacy one.
func (s *Store) FindAttachment(slug, filename string) (Attachment, bool) {
	if !wiki.ValidPageSlug(slug) || !validAttachmentFilename(filename) {
		return Attachment{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, path := range []string{attachmentPath(slug, filename), legacyAttachmentPath(slug, filename)} {
		if s.repositoryFileExists(path) {
			return Attachment{Slug: slug, Filename: filename, Path: path}, true
		}
	}
	return Attachment{}, false
}

// SaveAttachment commits a page's attachment, and its extracted text when
// given, in the namespace layout. A legacy copy of the same file is left for
// the migration and is no longer served.
func (s *Store) SaveAttachment(slug, filename string, content, extracted []byte, message, authorName, authorEmail string) (Attachment, error) {
	if !wiki.ValidPageSlug(slug) || !validAttachmentFilename(filename) {
		return Attachment{}, fmt.Errorf("invalid attachment %q on %q", filename, slug)
	}
	path := attachmentPath(slug, filename)
	files := map[string][]byte{path: content}
	if extracted != nil {
		files[extractedAttachmentPath(path)] = extracted
	}
	if _, err := s.SaveAll(files, message, authorName, authorEmail); err != nil {
		return Attachment{}, err
	}
	return Attachment{Slug: slug, Filename: filename, Path: path}, nil
}

// ListAttachments returns every served attachment in both layouts, sorted by
// path. A legacy copy shadowed by the namespace layout is left out.
func (s *Store) ListAttachments() ([]Attachment, error) {
	lockWaitDone := traceSlowLockWait("list attachments")
	s.mu.RLock()
	lockWaitDone()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("reading repository directory: %w", err)
	}
	roots := []string{attachmentsDir}
	for _, e := range entries {
		if e.IsDir() && validNamespaceName(e.Name()) {
			roots = append(roots, e.Name()+"/"+namespaceAttachmentDir)
		}
	}
	var paths []string
	for _, root := range roots {
		if err := s.walkAttachmentRoot(root, &paths); err != nil {
			return nil, err
		}
	}
	current := make(map[string]bool, len(paths))
	for _, path := range paths {
		current[path] = true
	}
	sort.Strings(paths)
	attachments := []Attachment{}
	for _, path := range paths {
		slug, filename, _ := ParseAttachmentPath(path)
		if isLegacyAttachmentPath(path) && current[attachmentPath(slug, filename)] {
			continue
		}
		attachments = append(attachments, Attachment{Slug: slug, Filename: filename, Path: path})
	}
	return attachments, nil
}

// walkAttachmentRoot appends the source attachments below one
// repository-relative root, skipping extracted-text sidecars and symlinks.
func (s *Store) walkAttachmentRoot(root string, paths *[]string) error {
	abs := filepath.Join(s.dir, filepath.FromSlash(root))
	if info, err := os.Lstat(abs); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspecting attachments directory: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("attachments directory %q is not a real directory", root)
	}
	err := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
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
		if _, _, ok := ParseAttachmentPath(rel); !ok {
			return nil
		}
		*paths = append(*paths, rel)
		return nil
	})
	if err != nil {
		return fmt.Errorf("reading attachments directory: %w", err)
	}
	return nil
}

// LegacyAttachments reports whether the legacy attachments/ tree holds files
// for a namespace that exists, so a migration has something to move. It only
// reads directory names, so it is cheap enough to call on every page render.
func (s *Store) LegacyAttachments() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(filepath.Join(s.dir, attachmentsDir))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && s.namespaceDirExists(e.Name()) {
			return true
		}
	}
	return false
}

// LegacyAttachmentNamespace is a namespace with source attachments in the
// legacy tree. A migration moves them when the namespace exists; the files of
// a gone namespace can only be deleted.
type LegacyAttachmentNamespace struct {
	Namespace string
	Files     int
	Exists    bool
}

// LegacyAttachmentStatus lists the namespaces that still have source
// attachments in the legacy tree, in name order.
func (s *Store) LegacyAttachmentStatus() ([]LegacyAttachmentNamespace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var paths []string
	if err := s.walkAttachmentRoot(attachmentsDir, &paths); err != nil {
		return nil, err
	}
	var namespaces []LegacyAttachmentNamespace
	index := map[string]int{}
	for _, path := range paths {
		namespace := legacyNamespace(path)
		i, seen := index[namespace]
		if !seen {
			i = len(namespaces)
			index[namespace] = i
			namespaces = append(namespaces, LegacyAttachmentNamespace{Namespace: namespace, Exists: s.namespaceDirExists(namespace)})
		}
		namespaces[i].Files++
	}
	slices.SortFunc(namespaces, func(a, b LegacyAttachmentNamespace) int { return strings.Compare(a.Namespace, b.Namespace) })
	return namespaces, nil
}

// DeleteLegacyAttachments removes every file a gone namespace still has in
// the legacy tree, sidecars included, in one commit, and returns how many
// source attachments went. The files stay in Git history. A namespace that
// exists is refused: its attachments are moved, not deleted.
func (s *Store) DeleteLegacyAttachments(namespace, message, authorName, authorEmail string) (int, error) {
	if s.readOnly {
		return 0, ErrReadOnly
	}
	if namespace == "" || namespace == "." || namespace == ".." || strings.ContainsAny(namespace, `/\`) {
		return 0, fmt.Errorf("invalid namespace %q", namespace)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.namespaceDirExists(namespace) {
		return 0, fmt.Errorf("namespace %q exists; move its attachments instead", namespace)
	}
	files, err := s.legacyAttachmentFiles()
	if err != nil {
		return 0, err
	}
	wt, tracked, err := s.trackedWorktree()
	if err != nil {
		return 0, err
	}
	deleted, found := 0, false
	for _, path := range files {
		if legacyNamespace(path) != namespace {
			continue
		}
		found = true
		if err := s.removeTracked(wt, tracked, path); err != nil {
			return 0, err
		}
		if _, _, ok := ParseAttachmentPath(path); ok {
			deleted++
		}
	}
	if !found {
		return 0, fmt.Errorf("no legacy attachments for namespace %q", namespace)
	}
	s.removeEmptyDirs(filepath.Join(s.dir, attachmentsDir))
	if err := s.commitWorktree(wt, message, authorName, authorEmail); err != nil {
		return 0, err
	}
	return deleted, nil
}

// legacyNamespace is the namespace folder a legacy file sits in.
func legacyNamespace(path string) string {
	namespace, _, _ := strings.Cut(strings.TrimPrefix(path, attachmentsDir+"/"), "/")
	return namespace
}

// MigrateLegacyAttachments moves every file below the legacy attachments/
// tree, extracted-text sidecars included, into its namespace's .attachments
// folder in one commit, and returns how many source attachments moved. A file
// whose new path is already taken is a stale copy of a newer upload and is
// dropped. Files of namespaces that no longer exist are left where they are,
// since moving them would create a namespace. An interrupted run leaves every
// file readable and is finished by running it again.
func (s *Store) MigrateLegacyAttachments(message, authorName, authorEmail string) (int, error) {
	if s.readOnly {
		return 0, ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	files, err := s.legacyAttachmentFiles()
	if err != nil {
		return 0, err
	}
	wt, tracked, err := s.trackedWorktree()
	if err != nil {
		return 0, err
	}

	moved := 0
	for _, path := range files {
		target, ok := s.legacyTarget(path)
		if !ok {
			continue
		}
		if !s.repositoryFileExists(target) {
			if err := s.copyRepositoryFile(path, target); err != nil {
				return 0, fmt.Errorf("moving attachment %q: %w", path, err)
			}
			if err := wt.AddWithOptions(&git.AddOptions{Path: target, SkipStatus: true}); err != nil {
				return 0, fmt.Errorf("adding attachment to index: %w", err)
			}
		}
		if err := s.removeTracked(wt, tracked, path); err != nil {
			return 0, err
		}
		delete(s.historyCache, target)
		if _, _, ok := ParseAttachmentPath(path); ok {
			moved++
		}
	}
	s.removeEmptyDirs(filepath.Join(s.dir, attachmentsDir))
	if err := s.commitWorktree(wt, message, authorName, authorEmail); err != nil {
		return 0, err
	}
	return moved, nil
}

// trackedWorktree returns the worktree and the set of paths in the Git index,
// so removals of untracked files skip the index.
func (s *Store) trackedWorktree() (*git.Worktree, map[string]bool, error) {
	wt, err := s.repo.Worktree()
	if err != nil {
		return nil, nil, fmt.Errorf("getting worktree: %w", err)
	}
	idx, err := s.repo.Storer.Index()
	if err != nil {
		return nil, nil, fmt.Errorf("reading index: %w", err)
	}
	tracked := make(map[string]bool, len(idx.Entries))
	for _, e := range idx.Entries {
		tracked[e.Name] = true
	}
	return wt, tracked, nil
}

// removeTracked deletes a legacy file from disk and, when tracked, the index.
func (s *Store) removeTracked(wt *git.Worktree, tracked map[string]bool, path string) error {
	if err := s.removeRepositoryFile(path); err != nil {
		return fmt.Errorf("removing legacy attachment: %w", err)
	}
	if tracked[path] {
		if _, err := wt.Remove(path); err != nil {
			return fmt.Errorf("removing legacy attachment from index: %w", err)
		}
	}
	delete(s.historyCache, path)
	return nil
}

// commitWorktree commits what has been staged; nothing staged is not an error.
func (s *Store) commitWorktree(wt *git.Worktree, message, authorName, authorEmail string) error {
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

// legacyAttachmentFiles lists every regular file below the legacy tree,
// sidecars included, skipping symlinks.
func (s *Store) legacyAttachmentFiles() ([]string, error) {
	root := filepath.Join(s.dir, attachmentsDir)
	var paths []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == root {
			return filepath.SkipDir
		}
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
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
		return nil, fmt.Errorf("reading legacy attachments: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

// legacyTarget maps a legacy file to its namespace-layout path, provided its
// namespace folder exists.
func (s *Store) legacyTarget(path string) (string, bool) {
	target, ok := migratedAttachmentPath(path)
	if !ok {
		return "", false
	}
	namespace, _, _ := strings.Cut(target, "/")
	return target, s.namespaceDirExists(namespace)
}

func (s *Store) namespaceDirExists(name string) bool {
	if !validNamespaceName(name) {
		return false
	}
	info, err := os.Lstat(filepath.Join(s.dir, name))
	return err == nil && info.IsDir()
}

func (s *Store) repositoryFileExists(path string) bool {
	f, err := s.openRepositoryFile(path, os.O_RDONLY, 0, false)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// copyRepositoryFile streams one repository file to a new path, refusing to
// overwrite or to follow symlinks on either side.
func (s *Store) copyRepositoryFile(from, to string) (err error) {
	src, err := s.openRepositoryFile(from, os.O_RDONLY, 0, false)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := s.openRepositoryFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644, true)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := dst.Close(); err == nil {
			err = closeErr
		}
	}()
	_, err = io.Copy(dst, src)
	return err
}

// removeEmptyDirs deletes the directories below and including root that are
// left empty, deepest first.
func (s *Store) removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	for _, dir := range slices.Backward(dirs) {
		_ = os.Remove(dir)
	}
}
