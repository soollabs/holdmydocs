package oauthserver

import (
	"net/url"
	"slices"
	"testing"
)

func TestProvisionClientPersistsSecretDigestOnly(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	provisioned, err := store.ProvisionClient("Example integration",
		[]string{"https://client.example.test/callback"}, clientAuthBasic,
		[]string{"read"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if provisioned.ClientID == "" || provisioned.Secret == "" {
		t.Fatal("confidential client credentials were not returned")
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	record := state.Clients[provisioned.ClientID]
	if record.SecretDigest == "" || record.SecretDigest == provisioned.Secret {
		t.Fatal("client secret was not stored as a digest")
	}
	if !(confidentialOAuthClient{record: record}).VerifyPassword(provisioned.Secret) {
		t.Fatal("stored secret digest did not verify")
	}
	if (confidentialOAuthClient{record: record}).VerifyPassword("wrong secret") {
		t.Fatal("incorrect client secret verified")
	}
}

func TestProvisionPublicClientAndRedirectValidation(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	client, err := store.ProvisionClient("Local tool",
		[]string{"http://127.0.0.1:8787/callback"}, clientAuthNone,
		[]string{"read"}, false)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if state.Clients[client.ClientID].SecretDigest != "" || client.Secret != "" {
		t.Fatal("public client unexpectedly has a client secret")
	}
	for _, redirect := range []string{
		"http://client.example.test/callback",
		"https://user@client.example.test/callback",
		"https://client.example.test/callback#fragment",
		"//client.example.test/callback",
	} {
		if err := validateRedirectURI(redirect); err == nil {
			t.Errorf("validateRedirectURI(%q) succeeded", redirect)
		}
	}
	if _, err := url.Parse("http://127.0.0.1:8787/callback"); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionClientAdministratorPolicy(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.ProvisionClient("Admin", []string{"https://client.example.test/cb"},
		clientAuthNone, []string{"settings"}, false); err == nil {
		t.Fatal("administrator scope provisioned while disabled")
	}
	client, err := store.ProvisionClient("Admin", []string{"https://client.example.test/cb"},
		clientAuthNone, []string{"settings"}, true)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(state.Clients[client.ClientID].AllowedScopes, "settings") {
		t.Fatal("administrator policy was not persisted when explicitly enabled")
	}
}
