package api

import (
	"context"
	"strings"
	"testing"

	"hmd/internal/auth"
	"hmd/internal/search"
	"hmd/internal/wiki"
)

// readCtx builds a read-scoped principal, optionally restricted to the given
// namespaces (an empty list means unrestricted).
func readCtx(namespaces ...string) context.Context {
	return auth.WithTokenPrincipal(context.Background(), auth.TokenPrincipal{
		User: "reader", Scopes: []string{"read"}, Namespaces: namespaces,
	})
}

// TestSearchPagesRejectsInvalidQuery checks malformed queries map to invalid
// input at the application boundary rather than reaching the index.
func TestSearchPagesRejectsInvalidQuery(t *testing.T) {
	ix, err := search.BuildIndex(nil)
	if err != nil {
		t.Fatal(err)
	}
	a := New(nil, ix, nil)
	for _, query := range []string{strings.Repeat("x", 513), "\xff\xfe"} {
		if _, err := a.SearchPages(writeCtx(), query); err == nil || CategoryOf(err) != CategoryInvalidInput {
			t.Fatalf("SearchPages(%q) = %v, want invalid input", query, err)
		}
	}
}

// TestSearchPagesFiltersDeniedNamespacesAndEscapesSnippets checks retrieval is
// followed by access filtering and that a snippet is escaped text safe to serve
// as a plain string, not raw page HTML.
func TestSearchPagesFiltersDeniedNamespacesAndEscapesSnippets(t *testing.T) {
	ix, err := search.BuildIndex([]wiki.Page{
		{Slug: "public/guide", Title: "Guide", Body: "Read the <b>uniqueterm</b> section."},
		{Slug: "private/secret", Title: "Secret", Body: "A hidden uniqueterm reference."},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := New(nil, ix, nil)

	hits, err := a.SearchPages(readCtx("public"), "uniqueterm")
	if err != nil {
		t.Fatalf("SearchPages: %v", err)
	}
	if len(hits) != 1 || hits[0].Slug != "public/guide" {
		t.Fatalf("hits = %+v, want only public/guide", hits)
	}
	if !strings.Contains(hits[0].Snippet, "uniqueterm") {
		t.Fatalf("snippet = %q, want the matched term", hits[0].Snippet)
	}
	if strings.Contains(hits[0].Snippet, "<b>") {
		t.Fatalf("snippet = %q, want escaped HTML", hits[0].Snippet)
	}

	// An unrestricted caller sees both; an unmatched query yields an empty
	// collection rather than an error.
	all, err := a.SearchPages(writeCtx(), "uniqueterm")
	if err != nil || len(all) != 2 {
		t.Fatalf("unrestricted hits = %+v, %v; want both pages", all, err)
	}
	none, err := a.SearchPages(writeCtx(), "absentterm")
	if err != nil {
		t.Fatalf("SearchPages(absent): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("absent query hits = %+v, want none", none)
	}
}

// TestSearchAttachmentsDisabled reports a distinct, non-fatal unavailable state
// when no document search is configured.
func TestSearchAttachmentsDisabled(t *testing.T) {
	ix, err := search.BuildIndex(nil)
	if err != nil {
		t.Fatal(err)
	}
	a := New(nil, ix, nil)
	if _, err := a.SearchAttachments(writeCtx(), "anything", 0); err == nil || CategoryOf(err) != CategoryUnavailable {
		t.Fatalf("SearchAttachments without document search = %v, want unavailable", err)
	}
}
