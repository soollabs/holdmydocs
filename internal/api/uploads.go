package api

import (
	"errors"
	"time"
)

// UploadCapability is a one-use, time-bounded permission to upload one
// attachment to one page.
type UploadCapability struct {
	Slug     string
	Filename string
	User     string
	Expires  time.Time
}

// MaxPendingUploadCapabilities bounds the number of outstanding upload capabilities.
const MaxPendingUploadCapabilities = 1024

// addUploadCapability records a pending capability, purging expired entries
// first and refusing once the pending set is full.
func (a *API) addUploadCapability(token string, capability UploadCapability) error {
	a.uploadMu.Lock()
	defer a.uploadMu.Unlock()
	if a.uploads == nil {
		a.uploads = make(map[string]UploadCapability)
	}
	now := time.Now()
	for key, pending := range a.uploads {
		if now.After(pending.Expires) {
			delete(a.uploads, key)
		}
	}
	if len(a.uploads) >= MaxPendingUploadCapabilities {
		return errors.New("too many pending attachment uploads")
	}
	a.uploads[token] = capability
	return nil
}

// takeUploadCapability consumes a capability, whether or not it has expired.
func (a *API) takeUploadCapability(token string) (UploadCapability, bool) {
	a.uploadMu.Lock()
	defer a.uploadMu.Unlock()
	capability, ok := a.uploads[token]
	delete(a.uploads, token)
	return capability, ok
}
