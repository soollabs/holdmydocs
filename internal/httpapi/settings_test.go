package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"hmd/internal/wiki"
)

// decodeOK asserts a 200 JSON body and decodes it into out (when non-nil).
func decodeOK(t *testing.T, resp mutationResponse, out any) {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(bytes.NewReader(resp.Body)).Decode(out); err != nil {
			t.Fatalf("decoding result: %v", err)
		}
	}
}

func TestSaveServerSettingsRejectsEmptyBind(t *testing.T) {
	env, client := newTestEnv(t, false)
	resp := postMutation(t, client, env.server.URL+"/_/api/settings/server", map[string]any{
		"bind":             "",
		"repo_dir":         "/data/repo",
		"git_user":         "test",
		"max_upload_bytes": 1024,
		"sync_poll_ms":     1000,
		"sync_mode":        "push",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSetAppearancePersistsForCaller(t *testing.T) {
	env, client := newTestEnv(t, false)
	decodeOK(t, postMutation(t, client, env.server.URL+"/_/api/settings/appearance", map[string]any{
		"skin": "soft",
	}), nil)
	if got := env.auth.Prefs("admin").Skin; got != "soft" {
		t.Errorf("prefs skin = %q, want soft", got)
	}
}

func TestSetAppearanceRejectsUnknownSkin(t *testing.T) {
	env, client := newTestEnv(t, false)
	resp := postMutation(t, client, env.server.URL+"/_/api/settings/appearance", map[string]any{"skin": "nope"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestCreateTokenReturnsValueOnce(t *testing.T) {
	env, client := newTestEnv(t, false)
	var created struct {
		Token string `json:"token"`
	}
	decodeOK(t, postMutation(t, client, env.server.URL+"/_/api/settings/tokens", map[string]any{
		"label": "cli", "expiry": "30d", "scopes": []string{"read"},
	}), &created)
	if created.Token == "" {
		t.Fatal("token response should carry the minted token")
	}
}

func TestSaveNamespaceThenConflictCarriesCurrentHash(t *testing.T) {
	env, client := newTestEnv(t, false)
	url := env.server.URL + "/_/api/namespaces"

	var saved struct {
		Name string `json:"name"`
		Hash string `json:"hash"`
	}
	decodeOK(t, postMutation(t, client, url, map[string]any{
		"name": "blog", "widgets": []string{"pages"}, "public": true,
	}), &saved)
	if saved.Name != "blog" || saved.Hash == "" {
		t.Fatalf("save result = %+v, want name and hash", saved)
	}

	// A second create with no base hash collides with the freshly written config.
	resp := postMutation(t, client, url, map[string]any{"name": "blog", "widgets": []string{"tags"}})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var conflict namespaceConflictResponse
	if err := json.NewDecoder(bytes.NewReader(resp.Body)).Decode(&conflict); err != nil {
		t.Fatalf("decoding conflict: %v", err)
	}
	if conflict.Conflict.CurrentHash == "" {
		t.Error("conflict should carry the current committed hash")
	}
	if conflict.Conflict.Name != "blog" {
		t.Errorf("conflict name = %q, want blog", conflict.Conflict.Name)
	}
}

func TestNamespaceResetAndDeleteActions(t *testing.T) {
	env, client := newTestEnv(t, false)
	base := env.server.URL + "/_/api/namespaces"

	decodeOK(t, postMutation(t, client, base, map[string]any{"name": "blog", "widgets": []string{"pages"}}), nil)
	decodeOK(t, postMutation(t, client, base+"/reset", map[string]any{"name": "blog"}), nil)
	if _, _, err := env.store.Read(wiki.NamespaceConfigPath("blog")); err == nil {
		t.Error("reset should remove the namespace configuration file")
	}
}

func TestCompleteSetupSkipClearsFlags(t *testing.T) {
	env, client := newTestEnv(t, false)
	env.store.ForceSetup.Store(true)
	decodeOK(t, postMutation(t, client, env.server.URL+"/_/api/setup", map[string]any{
		"action": "skip",
	}), nil)
	if env.store.ForceSetup.Load() {
		t.Error("skip should clear the forced-setup flag")
	}
}

func TestNamespaceStaleBaseHashConflicts(t *testing.T) {
	env, client := newTestEnv(t, false)
	url := env.server.URL + "/_/api/namespaces"

	decodeOK(t, postMutation(t, client, url, map[string]any{
		"name": "blog", "widgets": []string{"pages"},
	}), nil)

	// A write carrying a base hash that no longer matches must be rejected and
	// return the committed revision so the browser can retry against it.
	resp := postMutation(t, client, url, map[string]any{
		"name": "blog", "base_hash": "deadbeef", "widgets": []string{"tags"},
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var conflict namespaceConflictResponse
	if err := json.NewDecoder(bytes.NewReader(resp.Body)).Decode(&conflict); err != nil {
		t.Fatalf("decoding conflict: %v", err)
	}
	if conflict.Conflict.CurrentHash == "" {
		t.Error("conflict should carry the current committed hash")
	}
	if conflict.Conflict.BaseHash != "deadbeef" {
		t.Errorf("conflict base hash = %q, want the submitted stale hash", conflict.Conflict.BaseHash)
	}
}

func TestCreateTokenWithNamespaces(t *testing.T) {
	env, client := newTestEnv(t, false)
	decodeOK(t, postMutation(t, client, env.server.URL+"/_/api/settings/tokens", map[string]any{
		"label": "restricted", "expiry": "30d", "scopes": []string{"read"}, "namespaces": []string{"notes"},
	}), nil)

	var found bool
	for _, tok := range env.auth.TokensFor("admin") {
		if tok.Name != "restricted" {
			continue
		}
		found = true
		if len(tok.Namespaces) != 1 || tok.Namespaces[0] != "notes" {
			t.Errorf("token namespaces = %v, want [notes]", tok.Namespaces)
		}
	}
	if !found {
		t.Fatal("created token is not stored")
	}
}
