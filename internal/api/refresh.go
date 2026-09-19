package api

import (
	"log/slog"
	"strings"

	"hmd/internal/wiki"
)

// maxIndexRetries bounds how many refresh passes retry a page whose derived
// index update failed. A page's hash is recorded only once an update succeeds,
// so a transient failure recovers on a later pass; after the limit a persistent
// failure stops being retried until the page changes or the process restarts.
const maxIndexRetries = 5

// IndexRetry tracks one page's pending index reconciliation across refresh
// passes. The caller owns the bookkeeping map between passes.
type IndexRetry struct {
	Hash     string
	Attempts int
}

// DropExternalHistory discards cached commit history when an external process
// has moved the repository head.
func (a *API) DropExternalHistory() { a.store.DropHistoryOnExternalCommit() }

// ReloadConfiguration rebuilds the shared namespace registry and wiki
// configuration snapshots from the store. Each failure is logged and the
// previous snapshot kept, so a transient read failure never clears state.
func (a *API) ReloadConfiguration() {
	if reg, err := BuildNamespaceRegistryFromStore(a.store); err != nil {
		slog.Warn("refresh: namespace registry rebuild failed", "err", err)
	} else {
		a.SetNamespaces(reg)
	}
	if cfg, _, err := wiki.LoadWikiConfig(a.store.Dir()); err != nil {
		slog.Warn("refresh: wiki config reload failed", "err", err)
	} else {
		a.SetWikiConfig(cfg)
	}
}

// ReconcilePages re-reads the store and refreshes the derived index for pages
// whose content hash changed since the last recorded pass. It is the
// reconciliation path for a post-commit index failure: a failed update is
// retried on a later pass up to maxIndexRetries times, and the hash is recorded
// only after indexing succeeds. hashes and retries are caller-owned bookkeeping
// kept between passes.
func (a *API) ReconcilePages(hashes map[string]string, retries map[string]IndexRetry) {
	if a.index == nil {
		return
	}
	paths, err := a.store.List()
	if err != nil {
		slog.Warn("refresh: page list failed", "err", err)
		return
	}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		slug := strings.TrimSuffix(path, ".md")
		seen[slug] = true
		content, hash, err := a.store.Read(path)
		if err != nil {
			slog.Warn("refresh: page read failed", "path", path, "err", err)
			continue
		}
		if hashes[slug] == hash {
			continue
		}
		pending := retries[slug]
		if pending.Hash == hash && pending.Attempts >= maxIndexRetries {
			continue
		}
		if err := a.index.UpdatePage(wiki.ParsePage(slug, content), hash); err != nil {
			pending.Hash = hash
			pending.Attempts++
			retries[slug] = pending
			slog.Error("refresh: updating search index", "slug", slug, "attempt", pending.Attempts, "err", err)
			continue
		}
		delete(retries, slug)
		hashes[slug] = hash
	}
	for slug := range hashes {
		if !seen[slug] {
			delete(hashes, slug)
			a.index.Remove(slug)
		}
	}
}

// ReconcileAttachments reconciles the derived document index with the
// attachments currently in the store. It is a no-op when document indexing is
// disabled.
func (a *API) ReconcileAttachments() {
	if a.index == nil || !a.index.DocumentsEnabled() {
		return
	}
	paths, err := a.store.ListAttachments()
	if err != nil {
		slog.Warn("refresh: attachment list failed", "err", err)
		return
	}
	hashes := make(map[string]string, len(paths))
	for _, path := range paths {
		file, hash, err := a.store.OpenAttachment(path)
		if err != nil {
			slog.Warn("refresh: attachment hash failed", "path", path, "err", err)
			continue
		}
		if closeErr := file.Close(); closeErr != nil {
			slog.Warn("refresh: closing attachment", "path", path, "err", closeErr)
		}
		hashes[path] = hash
	}
	a.index.ReconcileAttachments(hashes)
}
