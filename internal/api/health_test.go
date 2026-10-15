package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/search"
	"hmd/internal/store"
	"hmd/internal/wiki"
)

func TestHealthReportsStalePagesFromGitHistory(t *testing.T) {
	index, err := search.BuildIndex(nil)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := newMutationAPI(t, index)
	for _, slug := range []string{"blog/old-page", "notes/old-page"} {
		if _, err := a.SavePage(writeCtx(), SavePageInput{Slug: slug, Title: slug, Body: "content"}); err != nil {
			t.Fatalf("SavePage(%q): %v", slug, err)
		}
	}

	fresh, err := a.Health(writeCtx(), "")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if len(fresh.Stale) != 0 {
		t.Fatalf("fresh pages reported stale: %+v", fresh.Stale)
	}

	now := time.Now().Add(stalePageAge + time.Hour)
	all, err := a.healthAt(writeCtx(), "", now)
	if err != nil {
		t.Fatalf("healthAt all pages: %v", err)
	}
	if len(all.Stale) != 2 || all.Stale[0].Slug != "blog/old-page" || all.Stale[1].Slug != "notes/old-page" {
		t.Fatalf("all stale pages = %+v, want blog/old-page and notes/old-page", all.Stale)
	}
	if all.Stale[0].LastUpdated.IsZero() {
		t.Fatal("stale page has no last-updated timestamp")
	}

	blog, err := a.healthAt(writeCtx(), "blog", now)
	if err != nil {
		t.Fatalf("healthAt blog: %v", err)
	}
	if len(blog.Stale) != 1 || blog.Stale[0].Slug != "blog/old-page" {
		t.Fatalf("blog stale pages = %+v, want only blog/old-page", blog.Stale)
	}

	restricted := auth.WithTokenPrincipal(context.Background(), auth.TokenPrincipal{
		User: "reader", Scopes: []string{"read"}, Namespaces: []string{"blog"},
	})
	visible, err := a.healthAt(restricted, "", now)
	if err != nil {
		t.Fatalf("healthAt restricted caller: %v", err)
	}
	if len(visible.Stale) != 1 || visible.Stale[0].Slug != "blog/old-page" {
		t.Fatalf("restricted stale pages = %+v, want only blog/old-page", visible.Stale)
	}
}

func TestHealthSummaryDoesNotNeedGitHistory(t *testing.T) {
	index, err := search.BuildIndex([]wiki.Page{
		{Slug: "notes/page", Title: "Page", Body: "see [[missing]]"},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := New(nil, index, nil)
	summary, err := a.HealthSummary(writeCtx())
	if err != nil {
		t.Fatalf("HealthSummary: %v", err)
	}
	if summary.Missing != 1 || summary.Orphans != 1 {
		t.Fatalf("HealthSummary = %+v, want one missing and one orphan", summary)
	}
}

// newAPIWithoutCommits returns an API over a repository holding one page,
// notes/page, that was copied in but never committed.
func newAPIWithoutCommits(t *testing.T) *API {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes", "page.md"), []byte("---\ntitle: Page\n---\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Options{RepoDir: dir, Git: store.GitOptions{User: "tester"}})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	index, err := search.BuildIndex(nil)
	if err != nil {
		t.Fatal(err)
	}
	a := New(st, index, nil)
	a.SetConfig(config.Config{})
	return a
}

// Pages copied into a repository that has no commits yet have no history; the
// report still builds, with nothing stale, instead of failing.
func TestHealthWithoutCommits(t *testing.T) {
	report, err := newAPIWithoutCommits(t).Health(writeCtx(), "")
	if err != nil {
		t.Fatalf("Health on a repository without commits: %v", err)
	}
	if len(report.Stale) != 0 {
		t.Fatalf("uncommitted pages reported stale: %+v", report.Stale)
	}
}
