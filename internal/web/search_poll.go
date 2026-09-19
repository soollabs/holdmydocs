package web

import (
	"context"
	"time"

	"hmd/internal/api"
)

// pollFS is the repository refresh worker. It owns the ticker and cancellation
// and delegates the shared refresh steps — history drop, configuration reload,
// page and attachment reconciliation — to the application api, so every
// adapter refreshes the same state by the same rules. The composition root
// starts and cancels it.
func pollFS(ctx context.Context, client *api.API, hashes map[string]string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	retries := make(map[string]api.IndexRetry)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		client.DropExternalHistory()
		client.ReloadConfiguration()
		client.ReconcilePages(hashes, retries)
		client.ReconcileAttachments()
	}
}
