package main

import (
	"log"
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
	}

	for _, p := range pages {
		ix.titles[p.Slug] = p.Title

		// Index the page
		doc := map[string]interface{}{
			"Title": p.Title,
			"Body":  p.Body,
			"Tags":  p.Tags,
		}
		blevIdx.Index(p.Slug, doc)

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
		paths, err := store.List()
		if err != nil {
			log.Printf("pollFS: List failed: %v", err)
			continue
		}

		seen := make(map[string]bool, len(paths))
		for _, path := range paths {
			slug := path[:len(path)-3] // remove .md
			seen[slug] = true

			content, hash, err := store.Read(path)
			if err != nil {
				log.Printf("pollFS: reading %s: %v", path, err)
				continue
			}
			if hashes[slug] == hash {
				continue
			}
			hashes[slug] = hash
			ix.Update(ParsePage(slug, content))
		}

		for slug := range hashes {
			if !seen[slug] {
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
	ix.bleve.Index(p.Slug, doc)

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

	return nil
}

// Remove deletes a page from the index — used when a page is toggled to
// hidden (or deleted), since hidden pages are excluded from search/tags/backlinks.
func (ix *Index) Remove(slug string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	ix.bleve.Delete(slug)
	delete(ix.titles, slug)

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

func (ix *Index) TagName(tagSlug string) string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.tagNames[tagSlug]
}
