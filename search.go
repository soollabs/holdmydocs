package main

import (
	"log/slog"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/blevesearch/bleve/v2"
)

type TagCount struct {
	Tag   string
	Slug  string
	Count int
}

type Index struct {
	mu       sync.RWMutex
	bleve    bleve.Index
	titles   map[string]string
	forward  map[string][]string
	backward map[string]map[string]bool
	tags     map[string]map[string]bool
	tagNames map[string]string
	pageTags map[string][]string
	widget   map[string]widgetMeta // slug -> pin/unread/source/author/read_time, for the widgets that read frontmatter directly
}

// widgetMeta is the subset of a page's frontmatter the widgets in
// specs/2026-07-25-profiles-widgets.md widget implementation read: pin, unread, source,
// author, read_time.
type widgetMeta struct {
	Pin      bool
	Unread   bool
	Source   string
	Author   string
	ReadTime string
}

type SearchHit struct {
	Slug    string
	Title   string
	Snippet string
	Tags    []string
}

func BuildIndex(pages []Page) (*Index, error) {
	mapping := bleve.NewIndexMapping()
	blevIdx, err := bleve.NewMemOnly(mapping)
	if err != nil {
		return nil, err
	}

	ix := &Index{
		bleve:    blevIdx,
		titles:   make(map[string]string),
		forward:  make(map[string][]string),
		backward: make(map[string]map[string]bool),
		tags:     make(map[string]map[string]bool),
		tagNames: make(map[string]string),
		pageTags: make(map[string][]string),
		widget:   make(map[string]widgetMeta),
	}

	for _, p := range pages {
		ix.titles[p.Slug] = p.Title
		ix.widget[p.Slug] = widgetMeta{Pin: p.Pin, Unread: p.Unread, Source: p.Source, Author: p.Author, ReadTime: p.ReadTime}

		// Index the page
		doc := map[string]interface{}{
			"Title": p.Title,
			"Body":  p.Body,
			"Tags":  p.Tags,
		}
		if err := blevIdx.Index(p.Slug, doc); err != nil {
			return nil, err
		}

		// Index tags
		ix.pageTags[p.Slug] = p.Tags
		for _, tag := range p.Tags {
			tagSlug := Slugify(tag)
			if ix.tags[tagSlug] == nil {
				ix.tags[tagSlug] = make(map[string]bool)
			}
			ix.tags[tagSlug][p.Slug] = true
			ix.tagNames[tagSlug] = tag
		}

		// Build forward links
		links := WikiLinks(p.Body)
		ix.forward[p.Slug] = links

		// Build backward links
		for _, link := range links {
			linkSlug := Slugify(link)
			if ix.backward[linkSlug] == nil {
				ix.backward[linkSlug] = make(map[string]bool)
			}
			ix.backward[linkSlug][p.Slug] = true
		}
	}

	return ix, nil
}

// pollFS periodically rescans the store directory so pages written outside
// the UI (e.g. `git pull`, an editor, a script) get picked up. hashes tracks
// the last-seen blob hash per slug; it belongs solely to this goroutine.
//
// polling, not fsnotify — this is a personal wiki, a 5s lag on
// externally-written pages is fine. Switch to fsnotify if that stops being true.
func pollFS(store *Store, ix *Index, hashes map[string]string) {
	for range time.Tick(5 * time.Second) {
		store.DropHistoryOnExternalCommit()

		paths, err := store.List()
		if err != nil {
			slog.Warn("pollFS: list failed", "err", err)
			continue
		}
		dailySlugs, err := store.DailyPages()
		if err != nil {
			slog.Warn("pollFS: daily list failed", "err", err)
			dailySlugs = nil
		}

		type entry struct{ slug, path string }
		entries := make([]entry, 0, len(paths)+len(dailySlugs))
		for _, path := range paths {
			entries = append(entries, entry{slug: path[:len(path)-3], path: path})
		}
		for _, slug := range dailySlugs {
			entries = append(entries, entry{slug: slug, path: pageFile(slug)})
		}

		seen := make(map[string]bool, len(entries))
		for _, e := range entries {
			seen[e.slug] = true

			content, hash, err := store.Read(e.path)
			if err != nil {
				slog.Warn("pollFS: read failed", "path", e.path, "err", err)
				continue
			}
			if hashes[e.slug] == hash {
				continue
			}
			slog.Debug("pollFS: page changed", "slug", e.slug)
			hashes[e.slug] = hash
			if err := ix.Update(ParsePage(e.slug, content)); err != nil {
				slog.Error("pollFS: updating search index", "slug", e.slug, "err", err)
			}
		}

		for slug := range hashes {
			if !seen[slug] {
				slog.Debug("pollFS: page removed", "slug", slug)
				delete(hashes, slug)
				ix.Remove(slug)
			}
		}
	}
}

func (ix *Index) Update(p Page) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	ix.titles[p.Slug] = p.Title
	ix.widget[p.Slug] = widgetMeta{Pin: p.Pin, Unread: p.Unread, Source: p.Source, Author: p.Author, ReadTime: p.ReadTime}

	// Remove old forward links from backward map
	if oldLinks, ok := ix.forward[p.Slug]; ok {
		for _, link := range oldLinks {
			linkSlug := Slugify(link)
			delete(ix.backward[linkSlug], p.Slug)
		}
	}

	// Re-index the page
	doc := map[string]interface{}{
		"Title": p.Title,
		"Body":  p.Body,
		"Tags":  p.Tags,
	}
	indexErr := ix.bleve.Index(p.Slug, doc)

	// Remove old tag associations for this page
	if oldTags, ok := ix.pageTags[p.Slug]; ok {
		for _, tag := range oldTags {
			tagSlug := Slugify(tag)
			delete(ix.tags[tagSlug], p.Slug)
			if len(ix.tags[tagSlug]) == 0 {
				delete(ix.tags, tagSlug)
				delete(ix.tagNames, tagSlug)
			}
		}
	}

	// Add new tag associations
	ix.pageTags[p.Slug] = p.Tags
	for _, tag := range p.Tags {
		tagSlug := Slugify(tag)
		if ix.tags[tagSlug] == nil {
			ix.tags[tagSlug] = make(map[string]bool)
		}
		ix.tags[tagSlug][p.Slug] = true
		ix.tagNames[tagSlug] = tag
	}

	// Build new forward links
	newLinks := WikiLinks(p.Body)
	ix.forward[p.Slug] = newLinks

	// Add new backward links
	for _, link := range newLinks {
		linkSlug := Slugify(link)
		if ix.backward[linkSlug] == nil {
			ix.backward[linkSlug] = make(map[string]bool)
		}
		ix.backward[linkSlug][p.Slug] = true
	}

	return indexErr
}

// Remove deletes a page from the index — used when a page is toggled to
// hidden (or deleted), since hidden pages are excluded from search/tags/backlinks.
func (ix *Index) Remove(slug string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	if err := ix.bleve.Delete(slug); err != nil {
		slog.Error("removing page from search index", "slug", slug, "err", err)
	}
	delete(ix.titles, slug)
	delete(ix.widget, slug)

	if oldLinks, ok := ix.forward[slug]; ok {
		for _, link := range oldLinks {
			delete(ix.backward[Slugify(link)], slug)
		}
	}
	delete(ix.forward, slug)
	delete(ix.backward, slug)

	if oldTags, ok := ix.pageTags[slug]; ok {
		for _, tag := range oldTags {
			tagSlug := Slugify(tag)
			delete(ix.tags[tagSlug], slug)
			if len(ix.tags[tagSlug]) == 0 {
				delete(ix.tags, tagSlug)
				delete(ix.tagNames, tagSlug)
			}
		}
	}
	delete(ix.pageTags, slug)
}

func (ix *Index) Exists(slug string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	_, ok := ix.titles[slug]
	return ok
}

func (ix *Index) Titles() map[string]string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	result := make(map[string]string)
	for k, v := range ix.titles {
		result[k] = v
	}
	return result
}

// TagsFor returns slug's tags as stored in the index.
func (ix *Index) TagsFor(slug string) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.pageTags[slug]
}

func (ix *Index) Search(q string) ([]SearchHit, error) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	query := bleve.NewQueryStringQuery(q)
	search := bleve.NewSearchRequest(query)
	search.Highlight = bleve.NewHighlightWithStyle("html")
	search.Fields = []string{"Title"}
	search.Size = 20

	results, err := ix.bleve.Search(search)
	if err != nil {
		return nil, err
	}

	var hits []SearchHit
	for _, match := range results.Hits {
		snippet := ""
		if frags, ok := match.Fragments["Body"]; ok && len(frags) > 0 {
			snippet = frags[0]
		}
		hits = append(hits, SearchHit{
			Slug:    match.ID,
			Title:   ix.titles[match.ID],
			Snippet: snippet,
			Tags:    ix.pageTags[match.ID],
		})
	}

	return hits, nil
}

func (ix *Index) Backlinks(slug string) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	backlinks := make([]string, 0, len(ix.backward[slug]))
	for source := range ix.backward[slug] {
		backlinks = append(backlinks, source)
	}
	sort.Strings(backlinks)
	return backlinks
}

func (ix *Index) Tags() []TagCount {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	result := make([]TagCount, 0, len(ix.tags))
	for tagSlug, slugs := range ix.tags {
		result = append(result, TagCount{
			Tag:   ix.tagNames[tagSlug],
			Slug:  tagSlug,
			Count: len(slugs),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Tag < result[j].Tag })
	return result
}

func (ix *Index) PagesForTag(tagSlug string) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	slugs := make([]string, 0, len(ix.tags[tagSlug]))
	for slug := range ix.tags[tagSlug] {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	return slugs
}

// PagesForTags returns slugs matching ANY of the given tag slugs (OR),
// deduplicated and sorted. Used by the <!-- hmd:toc:... --> token.
func (ix *Index) PagesForTags(tagSlugs []string) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	seen := make(map[string]bool)
	for _, ts := range tagSlugs {
		for slug := range ix.tags[ts] {
			seen[slug] = true
		}
	}
	slugs := make([]string, 0, len(seen))
	for s := range seen {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	return slugs
}

// Health reports wiki-link problems: missing maps each wiki-linked slug that
// has no page to the sorted list of pages linking to it; orphans lists pages
// with no backlinks, excluding homeSlug (hidden pages are never indexed).
func (ix *Index) Health(homeSlug string) (missing map[string][]string, orphans []string) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	sources := make(map[string]map[string]bool)
	for src, links := range ix.forward {
		for _, link := range links {
			target := Slugify(link)
			if _, exists := ix.titles[target]; exists {
				continue
			}
			if sources[target] == nil {
				sources[target] = make(map[string]bool)
			}
			sources[target][src] = true
		}
	}
	missing = make(map[string][]string, len(sources))
	for target, srcs := range sources {
		list := make([]string, 0, len(srcs))
		for s := range srcs {
			list = append(list, s)
		}
		sort.Strings(list)
		missing[target] = list
	}

	for slug := range ix.titles {
		if slug == homeSlug {
			continue
		}
		if len(ix.backward[slug]) == 0 {
			orphans = append(orphans, slug)
		}
	}
	sort.Strings(orphans)
	return missing, orphans
}

func (ix *Index) TagName(tagSlug string) string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.tagNames[tagSlug]
}

// MetaFor returns the widget-relevant frontmatter (pin/unread/source/
// author/read_time) for slug, or a zero widgetMeta if the page doesn't
// exist or has none of those keys set.
func (ix *Index) MetaFor(slug string) widgetMeta {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.widget[slug]
}

// PinnedPages returns pages with `pin: true`, sorted by title.
func (ix *Index) PinnedPages() []BacklinkEntry {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	var result []BacklinkEntry
	for slug, m := range ix.widget {
		if m.Pin {
			result = append(result, BacklinkEntry{Slug: slug, Title: ix.titles[slug]})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Title < result[j].Title })
	return result
}

// UnreadEntry is one row in the inbox widget.
type UnreadEntry struct {
	Slug     string
	Title    string
	Source   string
	ReadTime string
}

// UnreadPages returns pages with `unread: true`, newest-looking first
// (sorted by slug descending — daily/clip slugs sort newest-first lexically
// when date-prefixed, otherwise this is just a stable order).
func (ix *Index) UnreadPages() []UnreadEntry {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	var result []UnreadEntry
	for slug, m := range ix.widget {
		if m.Unread {
			result = append(result, UnreadEntry{Slug: slug, Title: ix.titles[slug], Source: m.Source, ReadTime: m.ReadTime})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Slug > result[j].Slug })
	return result
}

// SourceCount is one row in the sources widget: a clipped page's origin
// host, and how many pages carry it.
type SourceCount struct {
	Host  string
	Count int
}

// SourceCounts returns the distinct hosts among pages' `source:` frontmatter,
// counted and sorted by count descending then host ascending. Pages with an
// unparseable or empty source are skipped.
func (ix *Index) SourceCounts() []SourceCount {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	counts := make(map[string]int)
	for _, m := range ix.widget {
		host := sourceHost(m.Source)
		if host == "" {
			continue
		}
		counts[host]++
	}
	result := make([]SourceCount, 0, len(counts))
	for host, n := range counts {
		result = append(result, SourceCount{Host: host, Count: n})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Host < result[j].Host
	})
	return result
}

// sourceHost extracts the host from a source URL, e.g.
// "https://example.com/a/b" -> "example.com". Returns "" for an empty or
// unparseable source.
func sourceHost(source string) string {
	if source == "" {
		return ""
	}
	u, err := url.Parse(source)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}
