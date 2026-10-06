package oauthserver

import "testing"

func TestClientDeletionRequiresDisableAndRemovesBindings(t *testing.T) {
	s, store, authn, session, id := newOAuthServiceFixture(t)
	authoriseFixture(t, s, authn, session, id)
	if err := s.DeleteClient(id); err == nil {
		t.Fatal("active client deleted")
	}
	if err := s.DisableClient(id); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteClient(id); err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Clients) != 0 || len(state.Grants) != 0 || len(state.Families) != 0 || len(state.Tokens) != 0 {
		t.Fatal("deleted client security state remains")
	}
	if err := validateState(state); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteClient(id); err == nil {
		t.Fatal("missing client accepted")
	}
}

func TestDeleteDisabledClientsPreservesActiveClients(t *testing.T) {
	s, store, _, _, activeID := newOAuthServiceFixture(t)
	disabled, err := s.ProvisionClient("Disabled MCP", []string{"https://client.example/cb"}, "none", []string{"read"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DisableClient(disabled.ClientID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDisabledClients(); err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Clients) != 1 || state.Clients[activeID].ID != activeID {
		t.Fatal("bulk deletion removed active client")
	}
}
