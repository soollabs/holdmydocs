package web

import (
	"testing"

	"hmd/internal/store"
	"hmd/internal/wiki"
)

// TestReconcilePagesRetriesFailedIndexUpdate verifies the post-commit index
// reconciliation path: a failed update is not recorded as indexed, so a later
// pass retries and the page recovers once the index works again.
func TestReconcilePagesRetriesFailedIndexUpdate(t *testing.T) {
	st, err := store.Open(store.Options{RepoDir: t.TempDir(), Git: store.GitOptions{User: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	page := wiki.Page{Slug: "notes/page", Title: "Page", Body: "body"}
	if _, err := st.Save(wiki.PageFile(page.Slug), page.Encode(), "seed", "test", "test@hmd.local"); err != nil {
		t.Fatal(err)
	}
	_, hash, err := st.Read(wiki.PageFile(page.Slug))
	if err != nil {
		t.Fatal(err)
	}

	broken, err := BuildIndex(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.Close(); err != nil {
		t.Fatal(err)
	}

	hashes := map[string]string{}
	retries := map[string]indexRetry{}
	reconcilePages(st, broken, hashes, retries)
	if hashes["notes/page"] != "" {
		t.Fatal("a failed index update recorded the hash; the retry would be skipped")
	}
	if retries["notes/page"].attempts != 1 {
		t.Fatalf("retry attempts = %d, want 1", retries["notes/page"].attempts)
	}

	recovered, err := BuildIndex(nil)
	if err != nil {
		t.Fatal(err)
	}
	reconcilePages(st, recovered, hashes, retries)
	if hashes["notes/page"] != hash {
		t.Fatalf("hash after recovery = %q, want %q", hashes["notes/page"], hash)
	}
	if !recovered.Exists("notes/page") {
		t.Fatal("page not indexed after recovery")
	}
	if _, ok := retries["notes/page"]; ok {
		t.Fatal("retry bookkeeping not cleared after recovery")
	}
}
