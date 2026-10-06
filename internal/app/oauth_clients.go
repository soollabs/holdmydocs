package app

import (
	"errors"

	"hmd/internal/config"
	"hmd/internal/oauthserver"
)

// ProvisionOAuthClient creates a pre-registered MCP OAuth client through the
// local administration path. The returned secret is shown once to the caller.
func ProvisionOAuthClient(cfg config.Config, name string, redirectURIs []string, authMethod string, scopes []string) (oauthserver.ProvisionedClient, error) {
	if !cfg.OAuth.Enabled {
		return oauthserver.ProvisionedClient{}, errors.New("OAuth must be enabled in configuration before provisioning clients")
	}
	store, err := oauthserver.OpenStore(cfg.AppDir)
	if err != nil {
		return oauthserver.ProvisionedClient{}, err
	}
	defer store.Close()
	return store.ProvisionClient(name, redirectURIs, authMethod, scopes, cfg.OAuth.AllowAdminDelegation)
}

// DisableOAuthClient permanently disables a registration and revokes its
// existing grants. The server must be stopped while the local state is open.
func DisableOAuthClient(cfg config.Config, clientID string) error {
	if clientID == "" {
		return errors.New("OAuth client ID is required")
	}
	store, err := oauthserver.OpenStore(cfg.AppDir)
	if err != nil {
		return err
	}
	defer store.Close()
	return store.DisableClient(clientID)
}
