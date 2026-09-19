package api

import (
	"context"
	"strings"
	"time"

	"hmd/internal/search"
	"hmd/internal/wiki"
)

// Commit is one caller-visible repository commit.
type Commit struct {
	Hash    string
	Message string
	Author  string
	When    time.Time
	Files   []string
}

// Revision is one committed revision of a single page.
type Revision struct {
	Hash    string
	Message string
	Author  string
	When    time.Time
}

// RevisionPage is a page read back at a historical commit.
type RevisionPage struct {
	Slug  string
	Title string
	Tags  []string
	Body  string
	When  time.Time
}

// RecentChanges returns the newest commits the caller may see, limited to n
// commits before access filtering. A commit is omitted unless every file it
// touches falls inside the caller's namespace access, so a commit spanning
// multiple namespaces is only visible to a caller allowed all of them.
func (a *API) RecentChanges(ctx context.Context, limit int) ([]Commit, error) {
	if err := a.RequireScope(ctx, ScopeRead); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	commits, err := a.store.RecentCommits(limit)
	if err != nil {
		return nil, Unavailable("reading recent changes", err)
	}
	out := make([]Commit, 0, len(commits))
	for _, commit := range commits {
		if !commitAllowed(ctx, commit.Files) {
			continue
		}
		out = append(out, Commit{
			Hash: commit.Hash, Message: commit.Message, Author: commit.Author,
			When: commit.When, Files: commit.Files,
		})
	}
	return out, nil
}

// PageHistory returns the committed revisions of an accessible page, newest
// first.
func (a *API) PageHistory(ctx context.Context, slug string) ([]Revision, error) {
	if err := a.RequireScope(ctx, ScopeRead); err != nil {
		return nil, err
	}
	if !AllowSlug(ctx, slug) {
		return nil, Forbidden("namespace access denied")
	}
	history, err := a.store.History(wiki.PageFile(slug))
	if err != nil {
		return nil, Unavailable("reading page history", err)
	}
	revisions := make([]Revision, 0, len(history))
	for _, commit := range history {
		revisions = append(revisions, Revision{
			Hash: commit.Hash, Message: commit.Message, Author: commit.Author, When: commit.When,
		})
	}
	return revisions, nil
}

// RevisionPage reads one historical revision of an accessible page and the
// time of that commit.
func (a *API) RevisionPage(ctx context.Context, slug, hash string) (*RevisionPage, error) {
	if err := a.RequireScope(ctx, ScopeRead); err != nil {
		return nil, err
	}
	if !AllowSlug(ctx, slug) {
		return nil, Forbidden("namespace access denied")
	}
	content, err := a.store.FileAt(wiki.PageFile(slug), hash)
	if err != nil {
		return nil, NotFoundCause("revision not found", err)
	}
	page := wiki.ParsePage(slug, content)
	revision := &RevisionPage{Slug: page.Slug, Title: page.Title, Tags: page.Tags, Body: page.Body}
	if history, err := a.store.History(wiki.PageFile(slug)); err == nil {
		for _, commit := range history {
			if commit.Hash == hash {
				revision.When = commit.When
				break
			}
		}
	}
	return revision, nil
}

// commitAllowed reports whether every file a commit touches lies within the
// caller's namespace access. Page files resolve to their page's namespace and
// attachment files to their owning page's namespace; configuration, hidden and
// otherwise unrecognised paths are never exposed.
func commitAllowed(ctx context.Context, files []string) bool {
	if len(files) == 0 {
		return false
	}
	for _, path := range files {
		namespace, ok := commitPathNamespace(path)
		if !ok || !AllowNamespace(ctx, namespace) {
			return false
		}
	}
	return true
}

// commitPathNamespace resolves a repository-relative commit path to the
// namespace whose access governs it.
func commitPathNamespace(path string) (string, bool) {
	if owner, _, ok := search.ParseAttachmentPath(path); ok {
		namespace, _ := wiki.NamespaceFor(owner)
		return namespace, true
	}
	if strings.HasPrefix(path, ".") || !strings.HasSuffix(path, ".md") {
		return "", false
	}
	slug := strings.TrimSuffix(path, ".md")
	if !wiki.ValidPageSlug(slug) {
		return "", false
	}
	namespace, _ := wiki.NamespaceFor(slug)
	return namespace, true
}

// PageDiff returns the unified diff between two committed revisions of an
// accessible page.
func (a *API) PageDiff(ctx context.Context, slug, fromHash, toHash string) (string, error) {
	if err := a.RequireScope(ctx, ScopeRead); err != nil {
		return "", err
	}
	if !AllowSlug(ctx, slug) {
		return "", Forbidden("namespace access denied")
	}
	diff, err := a.store.Diff(wiki.PageFile(slug), fromHash, toHash)
	if err != nil {
		return "", Unavailable("diffing page revisions", err)
	}
	return diff, nil
}
