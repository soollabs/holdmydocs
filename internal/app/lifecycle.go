package app

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"hmd/internal/api"
)

// pollFS is the repository refresh worker. The composition root starts it and
// cancels it on shutdown; the worker owns its ticker and delegates the shared
// refresh steps — history drop, configuration reload and page reconciliation
// — to the application api, so every adapter refreshes the same
// state by the same rules.
func pollFS(ctx context.Context, client *api.API, hashes map[string]string) {
	ticker := time.NewTicker(refreshInterval(client))
	defer ticker.Stop()
	retries := make(map[string]api.IndexRetry)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		client.RefreshReadOnlyRemote()
		client.DropExternalHistory()
		client.ReloadConfiguration()
		client.ReconcilePages(hashes, retries)
	}
}

// pollAttachments is the attachment indexing worker. Its first pass runs
// straight away, so a new or changed document index fills in while the server
// is already answering. It is separate from pollFS because a first pass can
// take minutes (Tika and embedding for every attachment) and must not hold up
// page refreshes.
func pollAttachments(ctx context.Context, client *api.API) {
	ticker := time.NewTicker(refreshInterval(client))
	defer ticker.Stop()
	for {
		client.ReconcileAttachments(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func refreshInterval(client *api.API) time.Duration {
	if cfg := client.Config(); cfg.ReadOnly && cfg.SyncPollMs > 0 {
		return time.Duration(cfg.SyncPollMs) * time.Millisecond
	}
	return 5 * time.Second
}

// CheckReadiness probes the local readiness endpoint for the -healthcheck flag.
func CheckReadiness() error { return checkReadinessAt("http://127.0.0.1:8080/_/ready") }

func checkReadinessAt(url string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating readiness request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("checking readiness: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("checking readiness: HTTP %d", response.StatusCode)
	}
	return nil
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: address, Handler: handler, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20,
	}
}
