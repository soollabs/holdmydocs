package api

import (
	"context"
	"time"
)

// SyncCommit is one commit a bidirectional sync pulled into the repository.
type SyncCommit struct {
	Hash    string
	Message string
	Author  string
	When    time.Time
}

// SyncStatus is the transport-neutral state of repository synchronisation. It
// is shared by the sync-status and push-now operations so every adapter reports
// the same repository state.
type SyncStatus struct {
	State           string
	Detail          string
	LastSuccessUnix int64
	PagesChanged    []string
	Commits         []SyncCommit
}

// Sync reports the current synchronisation state and, in bidirectional mode
// with a remote configured, performs a fetch and fast-forward first. A fetch
// failure is reported through the state, not as an operation error, matching
// the store's own sync-state contract.
func (a *API) Sync(ctx context.Context) (SyncStatus, error) {
	if err := a.requireGlobalScope(ctx, ScopeRead); err != nil {
		return SyncStatus{}, err
	}
	cfg := a.Config()
	state, detail := a.store.SyncState()
	status := SyncStatus{State: state, Detail: detail, LastSuccessUnix: a.store.LastSyncUnix()}
	if (cfg.SyncMode != "bidirectional" && !a.readOnly()) || cfg.Git.RemoteURL == "" {
		return status, nil
	}
	result, err := a.store.FetchAndFF()
	if err != nil {
		state, detail = a.store.SyncState()
		status.State, status.Detail = state, detail
		return status, nil
	}
	status.PagesChanged = result.ChangedPaths
	for _, commit := range result.Commits {
		status.Commits = append(status.Commits, SyncCommit{
			Hash: commit.Hash, Message: commit.Message, Author: commit.Author, When: commit.When,
		})
	}
	return status, nil
}

// RefreshReadOnlyRemote keeps a browsing snapshot current without requiring a
// logged-in visitor or granting anonymous repository-wide API access.
func (a *API) RefreshReadOnlyRemote() {
	if a.readOnly() && a.Config().Git.RemoteURL != "" {
		_, _ = a.store.FetchAndFF()
	}
}

// SyncState reports the current synchronisation state without performing a
// fetch. It backs read-only status displays that must not trigger network work.
func (a *API) SyncState() (state, detail string, lastSuccessUnix int64) {
	state, detail = a.store.SyncState()
	return state, detail, a.store.LastSyncUnix()
}

// PushNow pushes committed local changes and reports the resulting sync state.
func (a *API) PushNow(ctx context.Context) (SyncStatus, error) {
	if err := a.requireGlobalScope(ctx, ScopeWrite); err != nil {
		return SyncStatus{}, err
	}
	state, detail := a.store.PushNow()
	return SyncStatus{State: state, Detail: detail, LastSuccessUnix: a.store.LastSyncUnix()}, nil
}
