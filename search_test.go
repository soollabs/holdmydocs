package main

import (
	"sort"
	"testing"
)

// TestResolveLink covers the wiki-link/TOC bug where a namespaced page's
// slug ("health/overview") never equals Slugify(its title) ("health-overview"),
// so a naive lookup 404s at root. ResolveLink must find the real slug by
// title, preferring a match in the caller's own namespace.
func TestResolveLink(t *testing.T) {
	pages := []Page{
		{Slug: "health/overview", Title: "Overview"},
		{Slug: "work/overview", Title: "Overview"},
		{Slug: "solo", Title: "Solo"},
	}
	ix, _ := BuildIndex(pages)

	if slug, ok := ix.ResolveLink("Overview", "health"); !ok || slug != "health/overview" {
		t.Errorf("ResolveLink(Overview, health) = (%q, %v), want (health/overview, true)", slug, ok)
	}
	if slug, ok := ix.ResolveLink("Overview", "work"); !ok || slug != "work/overview" {
		t.Errorf("ResolveLink(Overview, work) = (%q, %v), want (work/overview, true)", slug, ok)
	}
	// No page named "Overview" lives at root, so the namespace-scoped lookup
	// falls back to whichever namespace has one, rather than 404ing.
	if slug, ok := ix.ResolveLink("Overview", ""); !ok || (slug != "health/overview" && slug != "work/overview") {
		t.Errorf("ResolveLink(Overview, \"\") = (%q, %v), want a fallback match", slug, ok)
	}
	// Casing/punctuation that doesn't match the title exactly still resolves
	// via the slug, namespace first — as it did before ResolveLink existed.
	if slug, ok := ix.ResolveLink("overview", "health"); !ok || slug != "health/overview" {
		t.Errorf("ResolveLink(overview, health) = (%q, %v), want (health/overview, true)", slug, ok)
	}
	if slug, ok := ix.ResolveLink("SOLO", "health"); !ok || slug != "solo" {
		t.Errorf("ResolveLink(SOLO, health) = (%q, %v), want (solo, true)", slug, ok)
	}
	if _, ok := ix.ResolveLink("Nowhere", "health"); ok {
		t.Error("ResolveLink(Nowhere, health) = ok, want false")
	}
	if slug, ok := ix.ResolveLink("Solo", "health"); !ok || slug != "solo" {
		t.Errorf("ResolveLink(Solo, health) = (%q, %v), want (solo, true)", slug, ok)
	}

	// Ambiguous fallback (no match in the caller's own namespace, two
	// candidates elsewhere) must be deterministic across repeated calls,
	// not whatever order Go's map iteration happens to produce.
	for i := 0; i < 20; i++ {
		slug, ok := ix.ResolveLink("Overview", "other")
		if !ok || slug != "health/overview" {
			t.Fatalf("ResolveLink(Overview, other) = (%q, %v), want (health/overview, true) every time", slug, ok)
		}
	}
}

// TestResolveLinkLiteralSlug covers a link written as a namespace-qualified
// slug (e.g. [[health/overview]] instead of [[Overview]]): Slugify turns "/"
// into "-", so the old fallback mangled "health/overview" into
// "health-overview" and never found the page. ResolveLink must try the link
// text as a literal slug before Slugify.
func TestResolveLinkLiteralSlug(t *testing.T) {
	pages := []Page{
		{Slug: "health/overview", Title: "Overview"},
	}
	ix, _ := BuildIndex(pages)

	if slug, ok := ix.ResolveLink("health/overview", "other"); !ok || slug != "health/overview" {
		t.Errorf("ResolveLink(health/overview, other) = (%q, %v), want (health/overview, true)", slug, ok)
	}
}

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
	if err := ix.Update(Page{Slug: "alpha", Title: "Alpha", Tags: []string{"go"}, Body: "alpha body"}); err != nil {
		t.Fatalf("updating alpha: %v", err)
	}

	tags = ix.Tags()
	sort.Slice(tags, func(i, j int) bool { return tags[i].Tag < tags[j].Tag })
	if len(tags) != 1 || tags[0].Tag != "go" || tags[0].Count != 2 {
		t.Errorf("After update, Tags() = %+v, want [{go go 2}]", tags)
	}
	if name := ix.TagName("wiki"); name != "" {
		t.Errorf("After update, TagName(wiki) = %q, want empty (tag gone)", name)
	}
}

func TestTagsInNamespace(t *testing.T) {
	pages := []Page{
		{Slug: "blog/alpha", Title: "Alpha", Tags: []string{"go", "shared"}},
		{Slug: "blog/beta", Title: "Beta", Tags: []string{"go"}},
		{Slug: "wiki/root-page", Title: "Root", Tags: []string{"shared"}},
	}
	ix, _ := BuildIndex(pages)

	blogTags := ix.TagsInNamespace("blog")
	sort.Slice(blogTags, func(i, j int) bool { return blogTags[i].Tag < blogTags[j].Tag })
	if len(blogTags) != 2 {
		t.Fatalf("TagsInNamespace(blog) length = %d, want 2: %+v", len(blogTags), blogTags)
	}
	if blogTags[0].Tag != "go" || blogTags[0].Count != 2 {
		t.Errorf("TagsInNamespace(blog)[0] = %+v, want {go go 2}", blogTags[0])
	}
	if blogTags[1].Tag != "shared" || blogTags[1].Count != 1 {
		t.Errorf("TagsInNamespace(blog)[1] = %+v, want {shared shared 1}", blogTags[1])
	}

	wikiTags := ix.TagsInNamespace("wiki")
	if len(wikiTags) != 1 || wikiTags[0].Tag != "shared" || wikiTags[0].Count != 1 {
		t.Errorf("TagsInNamespace(\"wiki\") = %+v, want [{shared shared 1}]", wikiTags)
	}
}

// TestBacklinksAndHealthNamespaced pins backlinks and Health to the same
// resolution the renderer uses: [[Overview]] on health/plan points at
// health/overview, so that's where the backlink lands — not at "overview",
// which would leave health/overview a false orphan and "overview" a false
// missing link.
func TestBacklinksAndHealthNamespaced(t *testing.T) {
	pages := []Page{
		{Slug: "home", Title: "Home", Body: "start"},
		{Slug: "health/overview", Title: "Overview", Body: "hi"},
		{Slug: "health/plan", Title: "Plan", Body: "see [[Overview]] and [[Ghost]]"},
	}
	ix, _ := BuildIndex(pages)

	if got := ix.Backlinks("health/overview"); len(got) != 1 || got[0] != "health/plan" {
		t.Errorf("Backlinks(health/overview) = %v, want [health/plan]", got)
	}
	if got := ix.Backlinks("overview"); len(got) != 0 {
		t.Errorf("Backlinks(overview) = %v, want none — nothing links to a root slug", got)
	}

	missing, orphans := ix.Health([]string{"home"})
	if got := missing["health/ghost"]; len(got) != 1 || got[0] != "health/plan" {
		t.Errorf("missing[health/ghost] = %v, want [health/plan]", got)
	}
	if _, ok := missing["health/overview"]; ok {
		t.Error("health/overview exists, must not be reported missing")
	}
	// health/plan is genuinely unlinked; health/overview and home are not.
	if len(orphans) != 1 || orphans[0] != "health/plan" {
		t.Errorf("orphans = %v, want [health/plan]", orphans)
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
	if err := ix.Update(Page{Slug: "alpha", Title: "Alpha", Body: "no links now"}); err != nil {
		t.Fatalf("updating alpha: %v", err)
	}

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

func TestPinnedPages(t *testing.T) {
	pages := []Page{
		{Slug: "pinned-a", Title: "Zeta", Pin: true, Body: "x"},
		{Slug: "pinned-b", Title: "Alpha", Pin: true, Body: "x"},
		{Slug: "plain", Title: "Plain", Body: "x"},
	}
	ix, err := BuildIndex(pages)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	pinned := ix.PinnedPages()
	if len(pinned) != 2 || pinned[0].Title != "Alpha" || pinned[1].Title != "Zeta" {
		t.Errorf("PinnedPages() = %+v, want [Alpha Zeta] sorted by title", pinned)
	}
}
