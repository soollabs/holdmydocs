package api

import (
	"context"
	"testing"

	"hmd/internal/auth"
	"hmd/internal/search"
	"hmd/internal/wiki"
)

// tagCountsByTag indexes a tag-count slice by tag name for order-independent
// assertions.
func tagCountsByTag(tags []search.TagCount) map[string]int {
	counts := make(map[string]int, len(tags))
	for _, tag := range tags {
		counts[tag.Tag] = tag.Count
	}
	return counts
}

func TestNamespaceTagsScopesTagsToNamespace(t *testing.T) {
	index, err := search.BuildIndex([]wiki.Page{
		{Slug: "blog/post", Title: "Post", Tags: []string{"release", "shared"}},
		{Slug: "notes/entry", Title: "Entry", Tags: []string{"welcome", "shared"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := New(nil, index, nil)
	ctx := context.Background()

	blog := tagCountsByTag(a.NamespaceTags(ctx, "blog"))
	if len(blog) != 2 || blog["release"] != 1 || blog["shared"] != 1 {
		t.Fatalf("NamespaceTags(\"blog\") = %+v, want release and shared each counted once", blog)
	}
	notes := tagCountsByTag(a.NamespaceTags(ctx, "notes"))
	if len(notes) != 2 || notes["welcome"] != 1 || notes["shared"] != 1 {
		t.Fatalf("NamespaceTags(\"notes\") = %+v, want welcome and shared each counted once", notes)
	}
	// A page slug always names a namespace, so the root namespace has no pages
	// and therefore no tags.
	if root := a.NamespaceTags(ctx, ""); len(root) != 0 {
		t.Fatalf("NamespaceTags(\"\") = %+v, want none (no root-namespace pages)", root)
	}
	all := tagCountsByTag(a.Tags(ctx))
	if len(all) != 3 || all["release"] != 1 || all["welcome"] != 1 || all["shared"] != 2 {
		t.Fatalf("Tags() = %+v, want three whole-wiki tags with shared counted twice", all)
	}
}

func TestPageDiffDeniesNamespace(t *testing.T) {
	a := New(nil, nil, nil)
	ctx := auth.WithTokenPrincipal(context.Background(), auth.TokenPrincipal{
		User:       "reader",
		Scopes:     []string{"read"},
		Namespaces: []string{"blog"},
	})
	_, err := a.PageDiff(ctx, "notes/page", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err == nil {
		t.Fatal("PageDiff on a denied namespace = nil error, want forbidden")
	}
	if got := CategoryOf(err); got != CategoryForbidden {
		t.Fatalf("PageDiff denied category = %v, want forbidden", got)
	}
}
