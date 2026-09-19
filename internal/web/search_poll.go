package web

import (
	"context"
	"hmd/internal/api"
	"hmd/internal/wiki"
	"log/slog"
	"time"
)

func pollFS(ctx context.Context, store *Store, ix *Index, hashes map[string]string, setNamespaces func(wiki.NamespaceRegistry), setWikiConfig func(WikiConfig)) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
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
		paths, err := store.List()
		if err != nil {
			slog.Warn("pollFS: list failed", "err", err)
			continue
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
			hashes[slug] = hash
			if err := ix.UpdatePage(ParsePage(slug, content), hash); err != nil {
				slog.Error("pollFS: updating search index", "slug", slug, "err", err)
			}
		}
		for slug := range hashes {
			if !seen[slug] {
				delete(hashes, slug)
				ix.Remove(slug)
			}
		}
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
