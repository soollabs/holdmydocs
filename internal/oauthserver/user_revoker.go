package oauthserver

import (
	"os"
	"path/filepath"
)

// StoredUserRevoker handles account deletion while OAuth is disabled. It opens
// existing state only for revocation, without starting an OAuth service.
type StoredUserRevoker struct {
	appDir string
}

func NewStoredUserRevoker(appDir string) *StoredUserRevoker {
	return &StoredUserRevoker{appDir: appDir}
}

func (r *StoredUserRevoker) RevokeUserGrants(user string) error {
	if _, err := os.Lstat(filepath.Join(r.appDir, "oauth.json")); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	store, err := OpenStore(r.appDir)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	return store.Update(func(state *oauthState) error {
		revokeUserGrantsLocked(state, user)
		return nil
	})
}
