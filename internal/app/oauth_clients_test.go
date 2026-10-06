package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"hmd/internal/config"
	"hmd/internal/oauthserver"
)

func TestProvisionOAuthClientRequiresOptInAndPersistsRegistration(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{AppDir: dir}
	if _, err := ProvisionOAuthClient(cfg, "Local app",
		[]string{"https://client.example.test/callback"}, "client_secret_basic", []string{"read"}); err == nil {
		t.Fatal("client provisioned while OAuth was disabled")
	}
	cfg.OAuth.Enabled = true
	client, err := ProvisionOAuthClient(cfg, "Local app",
		[]string{"https://client.example.test/callback"}, "client_secret_basic", []string{"read"})
	if err != nil {
		t.Fatal(err)
	}
	if client.ClientID == "" || client.Secret == "" {
		t.Fatal("provisioning did not return credentials")
	}
	stateFile := filepath.Join(dir, "oauth.json")
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	// The client id is not a secret; the one-time client secret must not appear
	// in durable OAuth state.
	if bytes.Contains(data, []byte(client.Secret)) {
		t.Fatal("OAuth state contains the client secret")
	}

	store, err := oauthserver.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	record := state.Clients[client.ClientID]
	if record.ID == "" || record.SecretDigest == client.Secret {
		t.Fatal("persisted client registration is missing or stores plaintext secret")
	}
}

func TestDisableOAuthClientWorksWhenOAuthIsOff(t *testing.T) {
	dir := t.TempDir()
	state, err := oauthserver.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	client, err := state.ProvisionClient(
		"Compromised client", []string{"https://client.example.test/callback"},
		"none", []string{"read"}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	if err := DisableOAuthClient(config.Config{AppDir: dir}, client.ClientID); err != nil {
		t.Fatal(err)
	}
	state, err = oauthserver.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	snapshot, err := state.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Clients[client.ClientID].Disabled {
		t.Fatal("client was not disabled while OAuth was off")
	}
}

func TestDisableOAuthClientWorksAsEmergencyOperationWhenOAuthIsOff(t *testing.T) {
	dir := t.TempDir()
	state, err := oauthserver.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	client, err := state.ProvisionClient(
		"Compromised client", []string{"https://client.example.test/callback"},
		"none", []string{"read"}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	if err := DisableOAuthClient(config.Config{AppDir: dir}, client.ClientID); err != nil {
		t.Fatal(err)
	}
	state, err = oauthserver.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	snapshot, err := state.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Clients[client.ClientID].Disabled {
		t.Fatal("emergency disable did not persist while OAuth was disabled")
	}
}
