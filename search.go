package main

import (
	"log/slog"
	"sort"
	"strings"
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
	mu                sync.RWMutex
	bleve             bleve.Index
	indexDir          string
	manifestPath      string
	manifest          searchManifest
	documents         *DocumentSearch
	failedAttachments map[string]string
	// forward maps a page slug to the raw [[titles]] it links to. There is
	// no backward map: link targets are resolved on read via ResolveLink, so
	// backlinks agree with what the renderer actually linked to and start
	// counting as soon as the target page exists.
	titles   map[string]string
	forward  map[string][]string
	tags     map[string]map[string]bool
	tagNames map[string]string
	pageTags map[string][]string
	pinned   map[string]bool // slug -> pin: true, for the pinned widget
}

func newIndex(blevIdx bleve.Index) *Index {
	return &Index{
		bleve:             blevIdx,
		titles:            make(map[string]string),
		forward:           make(map[string][]string),
		tags:              make(map[string]map[string]bool),
		tagNames:          make(map[string]string),
		pageTags:          make(map[string][]string),
		pinned:            make(map[string]bool),
		failedAttachments: make(map[string]string),
	}
}

func pageDocumentID(slug string) string { return "page:" + slug }

type SearchHit struct {
	Slug    string
	Title   string
	Snippet string
	Tags    []string
}

func BuildIndex(pages []Page) (*Index, error) {
	mapping := attachmentIndexMapping()
	blevIdx, err := bleve.NewMemOnly(mapping)
	if err != nil {
		return nil, err
	}

	ix := newIndex(blevIdx)

	for _, p := range pages {
		ix.titles[p.Slug] = p.Title
		ix.pinned[p.Slug] = p.Pin

		// Index the page
		if err := blevIdx.Index(pageDocumentID(p.Slug), pageDocument(p)); err != nil {
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
		ix.forward[p.Slug] = WikiLinks(p.Body)
	}

	return ix, nil
}

// pollFS periodically rescans the store directory so pages written outside
// the UI (e.g. `git pull`, an editor, a script) get picked up. hashes tracks
// the last-seen blob hash per slug; it belongs solely to this goroutine.
//
// polling, not fsnotify — this is a personal wiki, a 5s lag on
// externally-written pages is fine. Switch to fsnotify if that stops being true.
func pollFS(store *Store, ix *Index, hashes map[string]string, setNamespaces func(NamespaceRegistry), setWikiConfig func(WikiConfig)) {
	for range time.Tick(5 * time.Second) {
		store.DropHistoryOnExternalCommit()

		if reg, err := BuildNamespaceRegistry(store.dir); err != nil {
			slog.Warn("pollFS: namespace registry rebuild failed", "err", err)
		} else {
			setNamespaces(reg)
		}
		if wiki, _, err := LoadWikiConfig(store.dir); err != nil {
			slog.Warn("pollFS: wiki config reload failed", "err", err)
		} else {
			setWikiConfig(wiki)
		}

		paths, err := store.List()
		if err != nil {
			slog.Warn("pollFS: list failed", "err", err)
			continue
		}

		type entry struct{ slug, path string }
		entries := make([]entry, 0, len(paths))
		for _, path := range paths {
			entries = append(entries, entry{slug: path[:len(path)-3], path: path})
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
			if err := ix.UpdatePage(ParsePage(e.slug, content), hash); err != nil {
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

		if ix.documents != nil {
			attachmentPaths, attachmentErr := store.ListAttachments()
			if attachmentErr != nil {
				slog.Warn("pollFS: attachment list failed", "err", attachmentErr)
				continue
			}
			attachmentHashes := make(map[string]string, len(attachmentPaths))
			for _, path := range attachmentPaths {
				file, hash, hashErr := store.OpenAttachment(path)
				if hashErr != nil {
					slog.Warn("pollFS: attachment hash failed", "path", path, "err", hashErr)
					continue
				}
				_ = file.Close()
				attachmentHashes[path] = hash
			}
			ix.ReconcileAttachments(attachmentHashes)
		}
	}
}

func (ix *Index) Update(p Page) error {
	return ix.UpdatePage(p, "")
}

func (ix *Index) UpdatePage(p Page, hash string) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	ix.titles[p.Slug] = p.Title
	ix.pinned[p.Slug] = p.Pin

	// Re-index the page
	indexErr := ix.bleve.Index(pageDocumentID(p.Slug), pageDocument(p))

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
	ix.forward[p.Slug] = WikiLinks(p.Body)

	if indexErr != nil {
		return indexErr
	}
	if ix.indexDir != "" && hash != "" {
		ix.manifest.Pages[p.Slug] = hash
		if err := writeSearchManifest(ix.manifestPath, ix.manifest); err != nil {
			return err
		}
	}
	return nil
}

// Remove deletes a page from the index — used when a page is toggled to
// hidden (or deleted), since hidden pages are excluded from search/tags/backlinks.
func (ix *Index) Remove(slug string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	if err := ix.bleve.Delete(pageDocumentID(slug)); err != nil {
		slog.Error("removing page from search index", "slug", slug, "err", err)
	}
	delete(ix.titles, slug)
	delete(ix.pinned, slug)

	delete(ix.forward, slug)

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
	if ix.indexDir != "" {
		delete(ix.manifest.Pages, slug)
		if err := writeSearchManifest(ix.manifestPath, ix.manifest); err != nil {
			slog.Warn("updating search manifest", "slug", slug, "err", err)
		}
	}
}

func (ix *Index) AttachmentIndexed(path, hash string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	entry, ok := ix.manifest.Attachments[path]
	return ok && entry.SourceHash == hash && len(entry.ChunkIDs) > 0
}

func (ix *Index) Exists(slug string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	_, ok := ix.titles[slug]
	return ok
}

// ResolveLink finds the slug a [[title]] wiki-link should point to: an exact
// title match within ns wins, so a link on a namespace page resolves inside
// that namespace first — matching injectTOC's self-containment — falling
// back to any other namespace if ns has no match. Reports ok=false if no
// page has that title anywhere. Ties (two pages sharing a title) resolve to
// the lexicographically first slug — deterministic, since map iteration
// order isn't — rather than picking arbitrarily per request.
//
// Titles must match exactly, so a link whose casing/punctuation differs from
// the page title ([[overview]] → "Overview") falls back to the old
// slug lookup, namespace first.
func (ix *Index) ResolveLink(title, ns string) (slug string, ok bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.resolveLink(title, ns)
}

// resolveLink is ResolveLink's body, for callers already holding ix.mu —
// sync.RWMutex isn't recursive, so re-taking RLock could deadlock against a
// waiting writer.
func (ix *Index) resolveLink(title, ns string) (slug string, ok bool) {
	var matches []string
	for s, t := range ix.titles {
		if t == title {
			matches = append(matches, s)
		}
	}
	if len(matches) == 0 {
		if _, exists := ix.titles[title]; exists {
			// title is itself a slug (e.g. [[docs/project-notes]]) — respect it
			// literally rather than Slugify mangling its "/" into "-".
			return title, true
		}
		for _, s := range []string{namespaceSlug(ns, Slugify(title)), Slugify(title)} {
			if _, exists := ix.titles[s]; exists {
				return s, true
			}
		}
		return "", false
	}
	sort.Strings(matches)
	for _, s := range matches {
		if pageNS, _ := namespaceFor(s); pageNS == ns {
			return s, true
		}
	}
	return matches[0], true
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

	queryString := bleve.NewQueryStringQuery(q)
	typeQuery := bleve.NewTermQuery("page")
	typeQuery.SetField("Type")
	searchQuery := bleve.NewConjunctionQuery(queryString, typeQuery)
	search := bleve.NewSearchRequest(searchQuery)
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
		slug := strings.TrimPrefix(match.ID, "page:")
		hits = append(hits, SearchHit{
			Slug:    slug,
			Title:   ix.titles[slug],
			Snippet: snippet,
			Tags:    ix.pageTags[slug],
		})
	}

	return hits, nil
}

// Backlinks lists the pages whose wiki-links resolve to slug, resolved the
// same way the renderer resolves them.
// O(pages × links) scan per call, no backward map to keep in sync —
// index it if a wiki ever gets big enough to notice.
func (ix *Index) Backlinks(slug string) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	backlinks := make([]string, 0, 4)
	for source, links := range ix.forward {
		if source == slug {
			continue // a page linking to itself isn't a backlink
		}
		sourceNS, _ := namespaceFor(source)
		for _, link := range links {
			if target, ok := ix.resolveLink(link, sourceNS); ok && target == slug {
				backlinks = append(backlinks, source)
				break
			}
		}
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

// TagsInNamespace is Tags scoped to pages belonging to namespace ns, so a
// tag with no pages in ns is omitted and counts only reflect that namespace.
func (ix *Index) TagsInNamespace(ns string) []TagCount {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	result := make([]TagCount, 0, len(ix.tags))
	for tagSlug, slugs := range ix.tags {
		count := 0
		for slug := range slugs {
			if pageNS, _ := namespaceFor(slug); pageNS == ns {
				count++
			}
		}
		if count == 0 {
			continue
		}
		result = append(result, TagCount{
			Tag:   ix.tagNames[tagSlug],
			Slug:  tagSlug,
			Count: count,
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
// with no backlinks. roots are the namespace index pages, which stand in for
// their namespace's listing and so are never orphans (hidden pages are never
// indexed).
func (ix *Index) Health(roots []string) (missing map[string][]string, orphans []string) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	isRoot := make(map[string]bool, len(roots))
	for _, slug := range roots {
		isRoot[slug] = true
	}

	sources := make(map[string]map[string]bool)
	linked := make(map[string]bool)
	for src, links := range ix.forward {
		srcNS, _ := namespaceFor(src)
		for _, link := range links {
			target, ok := ix.resolveLink(link, srcNS)
			if ok {
				if target != src {
					linked[target] = true
				}
				continue
			}
			// Key the miss by the slug the renderer's "+" link points at,
			// so the health report and the page it offers to create agree.
			target = namespaceSlug(srcNS, Slugify(link))
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
		if isRoot[slug] {
			continue
		}
		if !linked[slug] {
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

// PinnedPages returns pages with `pin: true`, sorted by title.
func (ix *Index) PinnedPages() []BacklinkEntry {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	var result []BacklinkEntry
	for slug, pinned := range ix.pinned {
		if pinned {
			result = append(result, BacklinkEntry{Slug: slug, Title: ix.titles[slug]})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Title < result[j].Title })
	return result
}
