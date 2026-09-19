package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"hmd/internal/config"
	"hmd/internal/store"
	"hmd/internal/wiki"
)

// wikiLinkRe matches one [[wiki-link]] in a page body; its contents are the
// link target, matched by title or by namespaced slug.
var wikiLinkRe = regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)

// Author resolves the git author for the caller: the user's stored author
// override when set, otherwise the repository default, falling back to the
// caller's username. Identity comes from the request context, never a caller
// field.
func (a *API) Author(ctx context.Context) (name, email string) {
	username, _ := Username(ctx)
	return a.authorFor(username)
}

// authorFor resolves the git author for an already-identified actor. It backs
// Author for context-derived identities and capability uploads, whose actor is
// taken from the server-issued capability rather than the request context.
func (a *API) authorFor(username string) (name, email string) {
	raw := ""
	if a.auth != nil {
		raw = a.auth.AuthorFor(username)
	}
	if raw == "" {
		raw = a.Config().Git.Author
	}
	return config.ParseAuthor(raw, username)
}

// fetchBeforeWrite applies the repository's save-time fetch policy: in
// bidirectional mode, a best-effort fast-forward before the checked write so a
// save is not built on a stale head. A fetch failure is logged, not fatal; the
// checked write remains the concurrency boundary.
func (a *API) fetchBeforeWrite() {
	cfg := a.Config()
	if cfg.SyncMode == "bidirectional" && cfg.Git.RemoteURL != "" {
		if _, err := a.store.FetchAndFF(); err != nil {
			slog.Warn("save-time fetch", "err", err)
		}
	}
}

// indexPage refreshes the derived search index for a committed page. The Git
// commit is durable regardless of the index: a failure is returned as a
// warning to reconcile later, never as an operation error.
func (a *API) indexPage(page wiki.Page, hash string) string {
	if a.index == nil {
		return "search index unavailable; the commit is saved and will be indexed later"
	}
	if err := a.index.UpdatePage(page, hash); err != nil {
		slog.Error("updating search index after commit", "slug", page.Slug, "err", err)
		return "search index update failed; it will be reconciled in the background"
	}
	return ""
}

// removeFromIndex drops a slug from the derived index after a committed delete
// or move. Index maintenance never fails a committed write.
func (a *API) removeFromIndex(slug string) {
	if a.index != nil {
		a.index.Remove(slug)
	}
}

// SavePageInput is a full page replacement: omitted metadata resets to the
// operation's defaults (title falls back to the slug; tags and pin default to
// empty and false). It creates or replaces one page and never moves it.
type SavePageInput struct {
	Slug     string
	Title    string
	Tags     []string
	Body     string
	Pin      bool
	BaseHash string
	Hidden   bool
}

// SavePage creates or replaces a page with replacement semantics. An empty
// BaseHash creates; a BaseHash replaces the revision it names.
func (a *API) SavePage(ctx context.Context, in SavePageInput) (*Mutation, error) {
	if err := a.RequireScope(ctx, ScopeWrite); err != nil {
		return nil, err
	}
	if !wiki.ValidPageSlug(in.Slug) {
		return nil, InvalidInput("invalid page identifier", nil)
	}
	if !AllowSlug(ctx, in.Slug) {
		return nil, Forbidden("namespace access denied")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = in.Slug
	}
	if err := ValidatePageInput(title, in.Tags, in.Body); err != nil {
		return nil, InvalidInput(err.Error(), err)
	}
	page := wiki.Page{Slug: in.Slug, Title: title, Tags: in.Tags, Body: in.Body, Pin: in.Pin}
	file := wiki.PageFile(in.Slug)
	if in.Hidden {
		file = wiki.HiddenFile(in.Slug)
	}
	message := "Create " + title
	if in.BaseHash != "" {
		message = "Update " + title
	}
	a.fetchBeforeWrite()
	name, email := a.Author(ctx)
	hash, err := a.store.SaveChecked(file, file, in.BaseHash, page.Encode(), message, name, email)
	if errors.Is(err, store.ErrConflict) {
		return nil, Conflict("the page changed since basehash, or already exists")
	}
	if err != nil {
		return nil, Unavailable("saving page", err)
	}
	mutation := &Mutation{Slug: in.Slug, BlobHash: hash}
	if in.Hidden {
		a.removeFromIndex(in.Slug)
	} else {
		mutation.IndexWarning = a.indexPage(page, hash)
	}
	slog.Info("page saved", "slug", in.Slug, "author", name, "message", message)
	return mutation, nil
}

// UpdatePageInput is a partial update of an existing page: a nil metadata
// field keeps the on-disk value (patch semantics). FromHidden and ToHidden name
// the hidden trees so a normal/hidden toggle is a real path move.
type UpdatePageInput struct {
	Slug       string
	BaseHash   string
	FromHidden bool
	ToHidden   bool
	Title      *string
	Tags       *[]string
	Body       *string
	Pin        *bool
}

// UpdatePage applies a patch to an existing page. It preserves every metadata
// field the caller omits and uses the checked write as the concurrency
// boundary.
func (a *API) UpdatePage(ctx context.Context, in UpdatePageInput) (*Mutation, error) {
	if err := a.RequireScope(ctx, ScopeWrite); err != nil {
		return nil, err
	}
	if !wiki.ValidPageSlug(in.Slug) {
		return nil, InvalidInput("invalid page identifier", nil)
	}
	if !AllowSlug(ctx, in.Slug) {
		return nil, Forbidden("namespace access denied")
	}
	oldFile := wiki.PageFile(in.Slug)
	if in.FromHidden {
		oldFile = wiki.HiddenFile(in.Slug)
	}
	newFile := wiki.PageFile(in.Slug)
	if in.ToHidden {
		newFile = wiki.HiddenFile(in.Slug)
	}
	content, _, err := a.store.Read(oldFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, Conflict("the page does not exist")
		}
		return nil, Unavailable("reading page", err)
	}
	page := wiki.ParsePage(in.Slug, content)
	if in.Title != nil {
		page.Title = *in.Title
	}
	if in.Body != nil {
		page.Body = *in.Body
	}
	if in.Tags != nil {
		page.Tags = *in.Tags
	}
	if in.Pin != nil {
		page.Pin = *in.Pin
	}
	if err := ValidatePageInput(page.Title, page.Tags, page.Body); err != nil {
		return nil, InvalidInput(err.Error(), err)
	}
	message := "Update " + page.Title
	if oldFile != newFile {
		message = "Move " + page.Title
	}
	a.fetchBeforeWrite()
	name, email := a.Author(ctx)
	hash, err := a.store.SaveChecked(oldFile, newFile, in.BaseHash, page.Encode(), message, name, email)
	if errors.Is(err, store.ErrConflict) {
		return nil, Conflict("the page changed since basehash")
	}
	if err != nil {
		return nil, Unavailable("saving page", err)
	}
	mutation := &Mutation{Slug: in.Slug, BlobHash: hash}
	if in.ToHidden {
		a.removeFromIndex(in.Slug)
	} else {
		mutation.IndexWarning = a.indexPage(page, hash)
	}
	slog.Info("page updated", "slug", in.Slug, "author", name)
	return mutation, nil
}

// MovePageInput is a checked move of a page to a new slug, optionally between
// the normal and hidden trees. Title, tags and body replace the metadata
// explicitly; pin is carried over from the source revision.
type MovePageInput struct {
	FromSlug   string
	ToSlug     string
	FromHidden bool
	ToHidden   bool
	BaseHash   string
	Title      string
	Tags       []string
	Body       string
}

// MovePage moves a page in one checked write. A move across namespaces only
// succeeds when the destination namespace already exists; a destination that
// already holds a page is rejected.
func (a *API) MovePage(ctx context.Context, in MovePageInput) (*Mutation, error) {
	if err := a.RequireScope(ctx, ScopeWrite); err != nil {
		return nil, err
	}
	if !wiki.ValidPageSlug(in.FromSlug) || !wiki.ValidPageSlug(in.ToSlug) {
		return nil, InvalidInput("invalid page identifier", nil)
	}
	if !AllowSlug(ctx, in.FromSlug) || !AllowSlug(ctx, in.ToSlug) {
		return nil, Forbidden("namespace access denied")
	}
	fromNS, _ := wiki.NamespaceFor(in.FromSlug)
	toNS, toPage := wiki.NamespaceFor(in.ToSlug)
	if !wiki.ValidPagePath(toPage) {
		return nil, InvalidInput("invalid destination filename", nil)
	}
	if fromNS != toNS {
		if _, ok := a.Namespaces()[toNS]; !ok {
			return nil, InvalidInput("unknown namespace", nil)
		}
	}
	oldFile := wiki.PageFile(in.FromSlug)
	if in.FromHidden {
		oldFile = wiki.HiddenFile(in.FromSlug)
	}
	newFile := wiki.PageFile(in.ToSlug)
	if in.ToHidden {
		newFile = wiki.HiddenFile(in.ToSlug)
	}
	// Destination collision is checked before the write; SaveChecked re-checks
	// atomically so a concurrent create still fails the move.
	if _, _, err := a.store.Read(newFile); err == nil {
		return nil, Conflict("a page with that filename already exists")
	}
	pin := false
	if content, _, err := a.store.Read(oldFile); err == nil {
		pin = wiki.ParsePage(in.FromSlug, content).Pin
	}
	page := wiki.Page{Slug: in.ToSlug, Title: in.Title, Tags: in.Tags, Body: in.Body, Pin: pin}
	if err := ValidatePageInput(page.Title, page.Tags, page.Body); err != nil {
		return nil, InvalidInput(err.Error(), err)
	}
	a.fetchBeforeWrite()
	name, email := a.Author(ctx)
	hash, err := a.store.SaveChecked(oldFile, newFile, in.BaseHash, page.Encode(), "Move "+page.Title, name, email)
	if errors.Is(err, store.ErrConflict) {
		return nil, Conflict("the destination exists or the page changed since basehash")
	}
	if err != nil {
		return nil, Unavailable("moving page", err)
	}
	a.removeFromIndex(in.FromSlug)
	mutation := &Mutation{Slug: in.ToSlug, BlobHash: hash}
	if in.ToHidden {
		a.removeFromIndex(in.ToSlug)
	} else {
		mutation.IndexWarning = a.indexPage(page, hash)
	}
	slog.Info("page moved", "from", in.FromSlug, "to", in.ToSlug, "author", name)
	return mutation, nil
}

// EditPageInput is an ordered set of exact-text replacements against one
// revision. BaseHash is required; DryRun validates and diffs without writing.
type EditPageInput struct {
	Slug     string
	BaseHash string
	Edits    []PageEdit
	DryRun   bool
}

// EditResult reports an exact-text edit. A dry run returns the would-be hash
// and diff without committing; a committed edit adds an index warning when the
// derived index could not be refreshed.
type EditResult struct {
	Slug         string
	BlobHash     string
	Changed      bool
	Diff         string
	IndexWarning string
}

// EditPage applies exact-text edits to an existing page's body, preserving its
// metadata. An unchanged body is a no-op that still reports the current hash.
func (a *API) EditPage(ctx context.Context, in EditPageInput) (*EditResult, error) {
	if err := a.RequireScope(ctx, ScopeWrite); err != nil {
		return nil, err
	}
	if !wiki.ValidPageSlug(in.Slug) {
		return nil, InvalidInput("invalid page identifier", nil)
	}
	if !AllowSlug(ctx, in.Slug) {
		return nil, Forbidden("namespace access denied")
	}
	if in.BaseHash == "" {
		return nil, InvalidInput("basehash is required; read the target page first", nil)
	}
	a.fetchBeforeWrite()
	file := wiki.PageFile(in.Slug)
	content, hash, err := a.store.Read(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, NotFound("page not found")
		}
		return nil, Unavailable("reading page", err)
	}
	if hash != in.BaseHash {
		return nil, Conflict("the page changed since basehash")
	}
	page := wiki.ParsePage(in.Slug, content)
	body, err := ApplyPageEdits(page.Body, in.Edits)
	if err != nil {
		return nil, InvalidInput(err.Error(), err)
	}
	if err := ValidatePageInput(page.Title, page.Tags, body); err != nil {
		return nil, InvalidInput(err.Error(), err)
	}
	changed := body != page.Body
	diff := PageEditDiff(page.Body, body)
	if in.DryRun {
		return &EditResult{Slug: in.Slug, BlobHash: hash, Changed: changed, Diff: diff}, nil
	}
	if changed {
		page.Body = body
		encoded := page.Encode()
		changed = !bytes.Equal(content, encoded)
		content = encoded
	}
	name, email := a.Author(ctx)
	newHash, err := a.store.SaveChecked(file, file, in.BaseHash, content, "Edit "+page.Title, name, email)
	if errors.Is(err, store.ErrConflict) {
		return nil, Conflict("the page changed since basehash")
	}
	if err != nil {
		return nil, Unavailable("editing page", err)
	}
	result := &EditResult{Slug: in.Slug, BlobHash: newHash, Changed: changed, Diff: diff}
	if changed {
		result.IndexWarning = a.indexPage(page, newHash)
		slog.Info("page edited", "slug", in.Slug, "author", name)
	}
	return result, nil
}

// SetPageTags replaces a page's tags, checking against the current revision.
// The tag replacement is atomic; an index failure is a warning, not an error.
func (a *API) SetPageTags(ctx context.Context, slug string, tags []string) (*Mutation, error) {
	if err := a.RequireScope(ctx, ScopeWrite); err != nil {
		return nil, err
	}
	if !wiki.ValidPageSlug(slug) {
		return nil, InvalidInput("invalid page identifier", nil)
	}
	if !AllowSlug(ctx, slug) {
		return nil, Forbidden("namespace access denied")
	}
	file := wiki.PageFile(slug)
	content, hash, err := a.store.Read(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, NotFound("page not found")
		}
		return nil, Unavailable("reading page", err)
	}
	page := wiki.ParsePage(slug, content)
	page.Tags = tags
	if err := ValidatePageInput(page.Title, page.Tags, page.Body); err != nil {
		return nil, InvalidInput(err.Error(), err)
	}
	name, email := a.Author(ctx)
	newHash, err := a.store.SaveChecked(file, file, hash, page.Encode(), "Update tags for "+page.Title, name, email)
	if errors.Is(err, store.ErrConflict) {
		return nil, Conflict("the page changed before the tag update")
	}
	if err != nil {
		return nil, Unavailable("updating tags", err)
	}
	mutation := &Mutation{Slug: slug, BlobHash: newHash}
	mutation.IndexWarning = a.indexPage(page, newHash)
	slog.Info("page tags updated", "slug", slug, "author", name)
	return mutation, nil
}

// RenamePageInput retitles a page in place, keeping its namespace.
type RenamePageInput struct {
	Slug     string
	NewTitle string
}

// RenameResult reports a retitle and its backlink repair. FailedLinks lists
// backlink sources that could not be rewritten: the rename itself is committed;
// link repair across multiple files is not atomic.
type RenameResult struct {
	Slug         string
	BlobHash     string
	IndexWarning string
	FailedLinks  []string
}

// RenamePage retitles a page and rewrites every accessible backlink source
// whose wiki-links pointed at it. All affected source and destination pages are
// authorised before any write.
func (a *API) RenamePage(ctx context.Context, in RenamePageInput) (*RenameResult, error) {
	if err := a.RequireScope(ctx, ScopeWrite); err != nil {
		return nil, err
	}
	if !wiki.ValidPageSlug(in.Slug) {
		return nil, InvalidInput("invalid page identifier", nil)
	}
	if !AllowSlug(ctx, in.Slug) {
		return nil, Forbidden("namespace access denied")
	}
	newTitle := strings.TrimSpace(in.NewTitle)
	if newTitle == "" {
		return nil, InvalidInput("missing title", nil)
	}
	renameNS, _ := wiki.NamespaceFor(in.Slug)
	if wiki.Slugify(newTitle) == "" {
		return nil, InvalidInput("invalid title", nil)
	}
	newSlug := wiki.NamespaceSlug(renameNS, wiki.Slugify(newTitle))
	if !AllowSlug(ctx, newSlug) {
		return nil, Forbidden("namespace access denied")
	}
	if a.index == nil {
		return nil, Unavailable("renaming page is unavailable", nil)
	}
	file := wiki.PageFile(in.Slug)
	content, hash, err := a.store.Read(file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, NotFound("page not found")
		}
		return nil, Unavailable("reading page", err)
	}
	if newSlug != in.Slug && a.index.Exists(newSlug) {
		return nil, Conflict("a page with that title already exists")
	}
	sources := a.index.Backlinks(in.Slug)
	for _, source := range sources {
		if !AllowSlug(ctx, source) {
			return nil, Forbidden("namespace access denied")
		}
	}
	page := wiki.ParsePage(in.Slug, content)
	oldTitle := page.Title
	page.Title = newTitle
	page.Slug = newSlug
	name, email := a.Author(ctx)
	message := fmt.Sprintf("Rename %s to %s", oldTitle, newTitle)
	newHash, err := a.store.SaveChecked(file, wiki.PageFile(newSlug), hash, page.Encode(), message, name, email)
	if errors.Is(err, store.ErrConflict) {
		return nil, Conflict("the page changed before the rename")
	}
	if err != nil {
		return nil, Unavailable("renaming page", err)
	}
	if newSlug != in.Slug {
		a.removeFromIndex(in.Slug)
	}
	result := &RenameResult{Slug: newSlug, BlobHash: newHash, IndexWarning: a.indexPage(page, newHash)}
	for _, src := range sources {
		srcContent, srcHash, err := a.store.Read(wiki.PageFile(src))
		if err != nil {
			result.FailedLinks = append(result.FailedLinks, src)
			continue
		}
		srcPage := wiki.ParsePage(src, srcContent)
		srcNS, _ := wiki.NamespaceFor(src)
		updated := wikiLinkRe.ReplaceAllStringFunc(srcPage.Body, func(m string) string {
			// Mirror Index.ResolveLink: the link pointed at the old page by
			// title, or — for casing that didn't match — by namespaced slug.
			inner := m[2 : len(m)-2]
			if inner == oldTitle || wiki.NamespaceSlug(srcNS, wiki.Slugify(inner)) == in.Slug || wiki.Slugify(inner) == in.Slug {
				return "[[" + newTitle + "]]"
			}
			return m
		})
		if updated == srcPage.Body {
			continue
		}
		srcPage.Body = updated
		sourceHash, err := a.store.SaveChecked(wiki.PageFile(src), wiki.PageFile(src), srcHash, srcPage.Encode(), "Update links after rename of "+oldTitle, name, email)
		if err != nil {
			result.FailedLinks = append(result.FailedLinks, src)
			continue
		}
		if warning := a.indexPage(srcPage, sourceHash); warning != "" && result.IndexWarning == "" {
			result.IndexWarning = warning
		}
	}
	slog.Info("page renamed", "from", in.Slug, "to", newSlug, "links", len(sources), "failures", len(result.FailedLinks))
	return result, nil
}

// DeletePage removes one page from the normal or hidden tree and drops it from
// the derived index.
func (a *API) DeletePage(ctx context.Context, slug string, hidden bool) (*Mutation, error) {
	if err := a.RequireScope(ctx, ScopeWrite); err != nil {
		return nil, err
	}
	if !wiki.ValidPageSlug(slug) {
		return nil, InvalidInput("invalid page identifier", nil)
	}
	if !AllowSlug(ctx, slug) {
		return nil, Forbidden("namespace access denied")
	}
	file := wiki.PageFile(slug)
	if hidden {
		file = wiki.HiddenFile(slug)
	}
	name, email := a.Author(ctx)
	if err := a.store.Remove(file, "Delete "+slug, name, email); err != nil {
		return nil, Unavailable("deleting page", err)
	}
	a.removeFromIndex(slug)
	slog.Info("page deleted", "slug", slug, "author", name)
	return &Mutation{Slug: slug}, nil
}

// RevertPage restores one committed revision of a page as a new commit.
func (a *API) RevertPage(ctx context.Context, slug, hash string) (*Mutation, error) {
	if err := a.RequireScope(ctx, ScopeWrite); err != nil {
		return nil, err
	}
	if !wiki.ValidPageSlug(slug) {
		return nil, InvalidInput("invalid page identifier", nil)
	}
	if !AllowSlug(ctx, slug) {
		return nil, Forbidden("namespace access denied")
	}
	content, err := a.store.FileAt(wiki.PageFile(slug), hash)
	if err != nil {
		return nil, NotFoundCause("revision not found", err)
	}
	name, email := a.Author(ctx)
	blobHash, err := a.store.Save(wiki.PageFile(slug), content, "Revert "+slug+" to "+shortHash(hash), name, email)
	if err != nil {
		return nil, Unavailable("reverting page", err)
	}
	page := wiki.ParsePage(slug, content)
	mutation := &Mutation{Slug: slug, BlobHash: blobHash}
	mutation.IndexWarning = a.indexPage(page, blobHash)
	slog.Info("page reverted", "slug", slug, "to", shortHash(hash), "author", name)
	return mutation, nil
}

func shortHash(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}
