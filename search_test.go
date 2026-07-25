package main

import (
	"sort"
	"testing"
)

func TestTags(t *testing.T) {
	pages := []Page{
		{Slug: "alpha", Title: "Alpha", Tags: []string{"go", "wiki"}, Body: "alpha body"},
		{Slug: "beta", Title: "Beta", Tags: []string{"go"}, Body: "beta body"},
		{Slug: "gamma", Title: "Gamma", Body: "gamma body, no tags"},
	}

	ix, _ := BuildIndex(pages)

	tags := ix.Tags()
	if len(tags) != 2 {
		t.Fatalf("Tags() length = %d, want 2", len(tags))
	}
	// Sort tags by tag name for deterministic comparison
	sort.Slice(tags, func(i, j int) bool { return tags[i].Tag < tags[j].Tag })
	if tags[0].Tag != "go" || tags[0].Count != 2 || tags[0].Slug != "go" {
		t.Errorf("Tags()[0] = %+v, want {go go 2}", tags[0])
	}
	if tags[1].Tag != "wiki" || tags[1].Count != 1 || tags[1].Slug != "wiki" {
		t.Errorf("Tags()[1] = %+v, want {wiki wiki 1}", tags[1])
	}

	goPages := ix.PagesForTag("go")
	if len(goPages) != 2 || goPages[0] != "alpha" || goPages[1] != "beta" {
		t.Errorf("PagesForTag(go) = %v, want [alpha beta]", goPages)
	}

	if name := ix.TagName("wiki"); name != "wiki" {
		t.Errorf("TagName(wiki) = %q, want %q", name, "wiki")
	}

	// Update alpha to drop the "wiki" tag.
	ix.Update(Page{Slug: "alpha", Title: "Alpha", Tags: []string{"go"}, Body: "alpha body"})

	tags = ix.Tags()
	sort.Slice(tags, func(i, j int) bool { return tags[i].Tag < tags[j].Tag })
	if len(tags) != 1 || tags[0].Tag != "go" || tags[0].Count != 2 {
		t.Errorf("After update, Tags() = %+v, want [{go go 2}]", tags)
	}
	if name := ix.TagName("wiki"); name != "" {
		t.Errorf("After update, TagName(wiki) = %q, want empty (tag gone)", name)
	}
}

func TestSearchAndBacklinks(t *testing.T) {
	pages := []Page{
		{Slug: "alpha", Title: "Alpha", Body: "the quick brown fox, see [[Beta]]"},
		{Slug: "beta", Title: "Beta", Body: "lazy dog"},
		{Slug: "gamma", Title: "Gamma", Body: "also [[Beta]] and [[Alpha]]"},
	}

	ix, _ := BuildIndex(pages)

	// Test search for "fox"
	results, _ := ix.Search("fox")
	if len(results) != 1 || results[0].Slug != "alpha" {
		t.Errorf("Search for 'fox' should find alpha")
	}
	if results[0].Snippet == "" {
		t.Errorf("Search result should have snippet")
	}

	// Test backlinks for beta
	backlinks := ix.Backlinks("beta")
	if len(backlinks) != 2 {
		t.Errorf("Beta should have 2 backlinks, got %d", len(backlinks))
	}
	if backlinks[0] != "alpha" || backlinks[1] != "gamma" {
		t.Errorf("Backlinks should be [alpha, gamma], got %v", backlinks)
	}

	// Test exists
	if !ix.Exists("alpha") {
		t.Errorf("Exists should return true for alpha")
	}
	if ix.Exists("nope") {
		t.Errorf("Exists should return false for nope")
	}

	// Test update: remove links from alpha
	ix.Update(Page{Slug: "alpha", Title: "Alpha", Body: "no links now"})

	// Search for "fox" should return no results
	results, _ = ix.Search("fox")
	if len(results) != 0 {
		t.Errorf("After update, search for 'fox' should find nothing")
	}

	// Beta should now have only gamma as backlink
	backlinks = ix.Backlinks("beta")
	if len(backlinks) != 1 || backlinks[0] != "gamma" {
		t.Errorf("After update, beta backlinks should be [gamma], got %v", backlinks)
	}

	// Search for "links" should find alpha (from updated body)
	results, _ = ix.Search("links")
	if len(results) != 1 || results[0].Slug != "alpha" {
		t.Errorf("After update, search for 'links' should find alpha")
	}
}

func TestPagesForTags(t *testing.T) {
	pages := []Page{
		{Slug: "alpha", Title: "Alpha", Tags: []string{"go", "wiki"}},
		{Slug: "beta", Title: "Beta", Tags: []string{"go"}},
		{Slug: "gamma", Title: "Gamma", Tags: []string{"wiki"}},
		{Slug: "delta", Title: "Delta", Tags: []string{"rust"}},
	}

	ix, _ := BuildIndex(pages)

	// OR: "go,wiki" should match alpha (go+wiki), beta (go), gamma (wiki).
	matched := ix.PagesForTags([]string{"go", "wiki"})
	if len(matched) != 3 {
		t.Fatalf("PagesForTags([go,wiki]) = %v, want 3 slugs", matched)
	}
	if matched[0] != "alpha" || matched[1] != "beta" || matched[2] != "gamma" {
		t.Errorf("PagesForTags([go,wiki]) = %v, want [alpha beta gamma]", matched)
	}

	// Single tag works the same as PagesForTag.
	single := ix.PagesForTags([]string{"rust"})
	if len(single) != 1 || single[0] != "delta" {
		t.Errorf("PagesForTags([rust]) = %v, want [delta]", single)
	}

	// Unknown tag returns nothing.
	none := ix.PagesForTags([]string{"nope"})
	if len(none) != 0 {
		t.Errorf("PagesForTags([nope]) = %v, want []", none)
	}

	// Empty slice returns nothing.
	if got := ix.PagesForTags(nil); len(got) != 0 {
		t.Errorf("PagesForTags(nil) = %v, want []", got)
	}
}

func TestWidgetMetaAccessors(t *testing.T) {
	pages := []Page{
		{Slug: "pinned-a", Title: "Zeta", Pin: true, Body: "x"},
		{Slug: "pinned-b", Title: "Alpha", Pin: true, Body: "x"},
		{Slug: "plain", Title: "Plain", Body: "x"},
		{Slug: "unread-1", Title: "Unread One", Unread: true, Source: "https://example.com/a", ReadTime: "3 min", Body: "x"},
		{Slug: "unread-2", Title: "Unread Two", Unread: true, Source: "https://blog.example.org/b", Body: "x"},
		{Slug: "read-clip", Title: "Read Clip", Source: "https://example.com/c", Body: "x"},
	}
	ix, err := BuildIndex(pages)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	pinned := ix.PinnedPages()
	if len(pinned) != 2 || pinned[0].Title != "Alpha" || pinned[1].Title != "Zeta" {
		t.Errorf("PinnedPages() = %+v, want [Alpha Zeta] sorted by title", pinned)
	}

	unread := ix.UnreadPages()
	if len(unread) != 2 {
		t.Fatalf("UnreadPages() length = %d, want 2", len(unread))
	}
	found := map[string]UnreadEntry{}
	for _, u := range unread {
		found[u.Slug] = u
	}
	if found["unread-1"].ReadTime != "3 min" || found["unread-1"].Source != "https://example.com/a" {
		t.Errorf("UnreadPages()[unread-1] = %+v", found["unread-1"])
	}

	counts := ix.SourceCounts()
	byHost := map[string]int{}
	for _, c := range counts {
		byHost[c.Host] = c.Count
	}
	if byHost["example.com"] != 2 {
		t.Errorf("SourceCounts() example.com = %d, want 2 (unread-1 + read-clip)", byHost["example.com"])
	}
	if byHost["blog.example.org"] != 1 {
		t.Errorf("SourceCounts() blog.example.org = %d, want 1", byHost["blog.example.org"])
	}

	meta := ix.MetaFor("unread-1")
	if !meta.Unread || meta.ReadTime != "3 min" {
		t.Errorf("MetaFor(unread-1) = %+v", meta)
	}
	if got := ix.MetaFor("nonexistent"); got != (widgetMeta{}) {
		t.Errorf("MetaFor(nonexistent) = %+v, want zero value", got)
	}
}
