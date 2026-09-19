package web

import (
	"context"
	"hmd/internal/api"
	"hmd/internal/wiki"
	"log/slog"
	"time"
)

// indexRetryLimit bounds how many reconciliation passes retry a page whose
// derived-index update failed. A page's hash is recorded only once an update
// succeeds, so a transient failure recovers on a later pass; after the limit a
// persistent failure stops being retried until the page changes or the process
// restarts, when startup reconciliation retries from the manifest.
const indexRetryLimit = 5

// indexRetry tracks one page's pending index reconciliation across poll passes.
type indexRetry struct {
	hash     string
	attempts int
}

func pollFS(ctx context.Context, store *Store, ix *Index, hashes map[string]string, setNamespaces func(wiki.NamespaceRegistry), setWikiConfig func(WikiConfig)) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	retries := make(map[string]indexRetry)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		store.DropHistoryOnExternalCommit()
		if reg, err := api.BuildNamespaceRegistryFromStore(store); err != nil {
			slog.Warn("pollFS: namespace registry rebuild failed", "err", err)
		} else {
			setNamespaces(reg)
		}
		if cfg, _, err := LoadWikiConfig(store.Dir()); err != nil {
			slog.Warn("pollFS: wiki config reload failed", "err", err)
		} else {
			setWikiConfig(cfg)
		}
		reconcilePages(store, ix, hashes, retries)
		if !ix.DocumentsEnabled() {
			continue
		}
		attachmentPaths, err := store.ListAttachments()
		if err != nil {
			slog.Warn("pollFS: attachment list failed", "err", err)
			continue
		}
		attachmentHashes := make(map[string]string, len(attachmentPaths))
		for _, path := range attachmentPaths {
			file, hash, err := store.OpenAttachment(path)
			if err != nil {
				slog.Warn("pollFS: attachment hash failed", "path", path, "err", err)
				continue
			}
			_ = file.Close()
			attachmentHashes[path] = hash
		}
		ix.ReconcileAttachments(attachmentHashes)
	}
}

// reconcilePages re-reads the store and refreshes the derived index for pages
// whose content hash changed. It is the reconciliation path for a post-commit
// index failure: a failed update is retried on a later pass up to
// indexRetryLimit times, and the hash is recorded only after indexing succeeds
// so a transient failure eventually recovers.
func reconcilePages(store *Store, ix *Index, hashes map[string]string, retries map[string]indexRetry) {
	paths, err := store.List()
	if err != nil {
		slog.Warn("pollFS: list failed", "err", err)
		return
	}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		slug := path[:len(path)-3]
		seen[slug] = true
		content, hash, err := store.Read(path)
		if err != nil {
			slog.Warn("pollFS: read failed", "path", path, "err", err)
			continue
		}
		if hashes[slug] == hash {
			continue
		}
		pending := retries[slug]
		if pending.hash == hash && pending.attempts >= indexRetryLimit {
			continue
		}
		if err := ix.UpdatePage(ParsePage(slug, content), hash); err != nil {
			pending.hash = hash
			pending.attempts++
			retries[slug] = pending
			slog.Error("pollFS: updating search index", "slug", slug, "attempt", pending.attempts, "err", err)
			continue
		}
		delete(retries, slug)
		hashes[slug] = hash
	}
	for slug := range hashes {
		if !seen[slug] {
			delete(hashes, slug)
			ix.Remove(slug)
		}
	}
}
