package api

import (
	"context"
	"errors"
	"os"
	"sort"

	"hmd/internal/wiki"
)

// PageSummary is one entry in an authorised page listing or backlink set.
type PageSummary struct {
	Slug  string
	Title string
	Tags  []string
}

// PageView is a page read back through the API with its current blob hash.
type PageView struct {
	Slug  string
	Title string
	Tags  []string
	Body  string
	Pin   bool
	Hash  string
}

// ListPages lists every page the caller may read, sorted by slug. Results are
// filtered by the caller's namespace access before leaving the application
// boundary; denied pages never reach an adapter.
func (a *API) ListPages(ctx context.Context) ([]PageSummary, error) {
	if err := a.RequireScope(ctx, ScopeRead); err != nil {
		return nil, err
	}
	if a.index == nil {
		return nil, Unavailable("page listing is unavailable", nil)
	}
	titles := a.index.Titles()
	pages := make([]PageSummary, 0, len(titles))
	for slug, title := range titles {
		if !AllowSlug(ctx, slug) {
			continue
		}
		pages = append(pages, PageSummary{Slug: slug, Title: title, Tags: a.index.TagsFor(slug)})
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Slug < pages[j].Slug })
	return pages, nil
}

// PageExists reports whether the index holds a page with the slug. It serves
// rendering decisions — index-page handoff and public wikilink filtering —
// without exposing the index to adapters.
func (a *API) PageExists(ctx context.Context, slug string) bool {
	return a.canReadPage(ctx, slug) && a.index != nil && a.index.Exists(slug)
}

// PageTitles returns the current page-title snapshot keyed by slug, for
// rendering navigation, tables of contents and folder trees.
func (a *API) PageTitles(ctx context.Context) map[string]string {
	if a.index == nil {
		return map[string]string{}
	}
	titles := a.index.Titles()
	for slug := range titles {
		if !a.canReadPage(ctx, slug) {
			delete(titles, slug)
		}
	}
	return titles
}

// PagesForTags returns the slugs carrying any of the given tag slugs, for
// rendering a tag-filtered table of contents.
func (a *API) PagesForTags(ctx context.Context, tags []string) []string {
	if a.index == nil {
		return nil
	}
	slugs := make([]string, 0)
	for _, slug := range a.index.PagesForTags(tags) {
		if a.canReadPage(ctx, slug) {
			slugs = append(slugs, slug)
		}
	}
	return slugs
}

// PinnedPages returns the pinned pages for the sidebar pin widget.
func (a *API) PinnedPages(ctx context.Context) []wiki.BacklinkEntry {
	if a.index == nil {
		return nil
	}
	pages := make([]wiki.BacklinkEntry, 0)
	for _, page := range a.index.PinnedPages() {
		if a.canReadPage(ctx, page.Slug) {
			pages = append(pages, page)
		}
	}
	return pages
}

// ViewPage reads one page the caller may read. A caller without slug access
// gets a forbidden result and a missing page a categorised not-found carrying
// os.ErrNotExist, so an adapter can still tell "missing" from "unreadable".
func (a *API) ViewPage(ctx context.Context, slug string) (*PageView, error) {
	if err := a.requirePageRead(ctx, slug); err != nil {
		return nil, err
	}
	content, hash, err := a.store.Read(wiki.PageFile(slug))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, NotFoundCause("page not found", err)
		}
		return nil, Unavailable("reading page", err)
	}
	page := wiki.ParsePage(slug, content)
	return &PageView{
		Slug: page.Slug, Title: page.Title, Tags: page.Tags, Body: page.Body, Pin: page.Pin, Hash: hash,
	}, nil
}

// Backlinks lists the caller-visible pages whose wiki-links resolve to slug.
func (a *API) Backlinks(ctx context.Context, slug string) ([]PageSummary, error) {
	if err := a.requirePageRead(ctx, slug); err != nil {
		return nil, err
	}
	titles := a.index.Titles()
	backlinks := make([]PageSummary, 0, 4)
	for _, source := range a.index.Backlinks(slug) {
		if !a.canReadPage(ctx, source) {
			continue
		}
		backlinks = append(backlinks, PageSummary{Slug: source, Title: titles[source], Tags: a.index.TagsFor(source)})
	}
	return backlinks, nil
}

// Tags lists the tags used by the caller-visible pages across the whole wiki,
// with counts restricted to those pages.
func (a *API) Tags(ctx context.Context) []wiki.TagCount {
	return a.filterTags(ctx, "", false)
}

// NamespaceTags lists the tags used by the caller-visible pages in one
// namespace, with counts restricted to that namespace. An empty namespace
// means the root namespace, matching search.Index.TagsInNamespace.
func (a *API) NamespaceTags(ctx context.Context, namespace string) []wiki.TagCount {
	return a.filterTags(ctx, namespace, true)
}

// filterTags drops tags with no caller-visible pages and recomputes each count
// from the pages the caller may read. When scoped, both the tag set and the
// counts are limited to one namespace.
func (a *API) filterTags(ctx context.Context, namespace string, scoped bool) []wiki.TagCount {
	var tags []wiki.TagCount
	if scoped {
		tags = a.index.TagsInNamespace(namespace)
	} else {
		tags = a.index.Tags()
	}
	filtered := make([]wiki.TagCount, 0, len(tags))
	for _, tag := range tags {
		count := 0
		for _, slug := range a.index.PagesForTag(tag.Slug) {
			if scoped {
				if pageNS, _ := wiki.NamespaceFor(slug); pageNS != namespace {
					continue
				}
			}
			if a.canReadPage(ctx, slug) {
				count++
			}
		}
		if count > 0 {
			tag.Count = count
			filtered = append(filtered, tag)
		}
	}
	return filtered
}

// TagPages returns a tag's display name and the caller-visible pages carrying
// it, sorted by slug.
func (a *API) TagPages(ctx context.Context, tagSlug string) (string, []PageSummary) {
	name := a.index.TagName(tagSlug)
	if name == "" {
		name = tagSlug
	}
	titles := a.index.Titles()
	pages := make([]PageSummary, 0)
	for _, slug := range a.index.PagesForTag(tagSlug) {
		if !a.canReadPage(ctx, slug) {
			continue
		}
		pages = append(pages, PageSummary{Slug: slug, Title: titles[slug], Tags: a.index.TagsFor(slug)})
	}
	return name, pages
}

// HiddenPages lists the hidden pages the caller may read, sorted by slug.
// Hidden pages are browser-only and never appear in the shared page index.
func (a *API) HiddenPages(ctx context.Context) ([]PageSummary, error) {
	if err := a.RequireScope(ctx, ScopeRead); err != nil {
		return nil, err
	}
	if a.store == nil {
		return nil, Unavailable("hidden page listing is unavailable", nil)
	}
	paths, err := a.store.ListHidden()
	if err != nil {
		return nil, Unavailable("listing hidden pages", err)
	}
	pages := make([]PageSummary, 0, len(paths))
	for _, path := range paths {
		slug := wiki.HiddenSlug(path)
		if !AllowSlug(ctx, slug) {
			continue
		}
		content, _, err := a.store.Read(path)
		if err != nil {
			continue
		}
		page := wiki.ParsePage(slug, content)
		pages = append(pages, PageSummary{Slug: slug, Title: page.Title})
	}
	return pages, nil
}

// ViewHidden reads one hidden page the caller may read. A caller without slug
// access gets a forbidden result and a missing page a categorised not-found
// carrying os.ErrNotExist, so the browser can offer to create it.
func (a *API) ViewHidden(ctx context.Context, slug string) (*PageView, error) {
	if err := a.RequireScope(ctx, ScopeRead); err != nil {
		return nil, err
	}
	if !AllowSlug(ctx, slug) {
		return nil, Forbidden("namespace access denied")
	}
	content, hash, err := a.store.Read(wiki.HiddenFile(slug))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, NotFoundCause("hidden page not found", err)
		}
		return nil, Unavailable("reading hidden page", err)
	}
	page := wiki.ParsePage(slug, content)
	return &PageView{
		Slug: page.Slug, Title: page.Title, Tags: page.Tags, Body: page.Body, Pin: page.Pin, Hash: hash,
	}, nil
}
