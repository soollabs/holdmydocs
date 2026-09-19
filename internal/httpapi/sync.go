package httpapi

import (
	"context"
	"time"
)

// SyncStatus is the JSON sync-status response.
type SyncStatus struct {
	State           string       `json:"state"`
	Detail          string       `json:"detail"`
	At              string       `json:"at"`
	PagesChanged    []string     `json:"pagesChanged,omitempty"`
	Commits         []SyncCommit `json:"commits,omitempty"`
	LastSuccessUnix int64        `json:"last_success_unix"`
}

// SyncCommit is one synchronised commit in the JSON sync-status response.
type SyncCommit struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"shortHash"`
	Message   string `json:"message"`
	Author    string `json:"author"`
	When      string `json:"when"`
}

// PushResult is the JSON response for a manual sync push.
type PushResult struct {
	OK              bool   `json:"ok"`
	State           string `json:"state"`
	Detail          string `json:"detail"`
	LastSuccessUnix int64  `json:"last_success_unix"`
}

// noInput is the empty input for endpoints whose operation takes no parameters.
type noInput struct{}

// syncStatus reports the current repository synchronisation state.
func (h *Handlers) syncStatus(ctx context.Context, _ noInput) (SyncStatus, error) {
	status := h.api.Sync(ctx)
	resp := SyncStatus{
		State:           status.State,
		Detail:          status.Detail,
		At:              time.Now().Format("15:04"),
		LastSuccessUnix: status.LastSuccessUnix,
		PagesChanged:    status.PagesChanged,
	}
	for _, c := range status.Commits {
		shortHash := c.Hash
		if len(shortHash) > 8 {
			shortHash = shortHash[:8]
		}
		resp.Commits = append(resp.Commits, SyncCommit{
			Hash:      c.Hash,
			ShortHash: shortHash,
			Message:   c.Message,
			Author:    c.Author,
			When:      c.When.Format("2006-01-02 15:04"),
		})
	}
	return resp, nil
}

// syncPushNow pushes committed local changes and reports the resulting state.
func (h *Handlers) syncPushNow(ctx context.Context, _ noInput) (PushResult, error) {
	status := h.api.PushNow(ctx)
	return PushResult{
		OK:              status.State == "ok",
		State:           status.State,
		Detail:          status.Detail,
		LastSuccessUnix: status.LastSuccessUnix,
	}, nil
}
