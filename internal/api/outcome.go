package api

// ConflictData carries the data an adapter needs to report a rejected
// optimistic write: the caller's stale base hash and the currently committed
// revision.
type ConflictData struct {
	BaseHash    string
	CurrentHash string
}

// Mutation records a committed write. A committed mutation is a success even
// when IndexWarning is non-empty: the Git commit is durable, and only the
// derived search index failed to refresh and will be reconciled later.
type Mutation struct {
	Slug         string
	BlobHash     string
	IndexWarning string
}
