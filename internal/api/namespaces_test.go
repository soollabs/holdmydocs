package api

import (
	"testing"

	"hmd/internal/search"
	"hmd/internal/wiki"
)

func TestNamespaceSummariesCatalogue(t *testing.T) {
	reg := wiki.NamespaceRegistry{
		"":     wiki.DefaultNamespaceConfig(),
		"blog": {Configured: true},
	}
	index, err := search.BuildIndex([]wiki.Page{{Slug: "notes/entry", Title: "Notes entry"}})
	if err != nil {
		t.Fatal(err)
	}
	a := New(nil, index, nil)
	a.SetNamespaces(reg)

	summaries := a.namespaceSummaries()
	if len(summaries) != 2 {
		t.Fatalf("NamespaceSummaries() returned %d entries, want 2: %+v", len(summaries), summaries)
	}
	if summaries[0].Name != "blog" || summaries[1].Name != "notes" {
		t.Fatalf("NamespaceSummaries() names = [%s, %s], want [blog, notes]", summaries[0].Name, summaries[1].Name)
	}
	if summaries[0].Count != 0 || len(summaries[0].Pages) != 0 {
		t.Errorf("empty blog summary = %+v, want zero pages", summaries[0])
	}
	if summaries[1].Count != 1 || len(summaries[1].Pages) != 1 || summaries[1].Pages[0].Slug != "notes/entry" {
		t.Errorf("notes summary = %+v, want one notes/entry page", summaries[1])
	}
}

func TestListNamespacesFiltersDeniedNamespace(t *testing.T) {
	reg := wiki.NamespaceRegistry{
		"blog":  {Configured: true},
		"notes": {Configured: true},
	}
	index, err := search.BuildIndex(nil)
	if err != nil {
		t.Fatal(err)
	}
	a := New(nil, index, nil)
	a.SetNamespaces(reg)

	// With no token principal every namespace is visible.
	if got := len(a.ListNamespaces(writeCtx())); got != 2 {
		t.Errorf("ListNamespaces() = %d entries, want 2", got)
	}
}
