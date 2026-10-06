package oauthserver

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testStoreClient() ClientRecord {
	return ClientRecord{
		ID: "client-a", Name: "Test client", RedirectURIs: []string{"https://client.example.test/callback"},
		AuthMethod: "none", AllowedScopes: []string{"read"}, CreatedAt: time.Now().UTC(),
	}
}

func TestStoreUpdatePersistsAndReopens(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Update(func(state *oauthState) error {
		client := testStoreClient()
		state.Clients[client.ID] = client
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "oauth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("OAuth state mode = %o, want 600", info.Mode().Perm())
	}

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	state, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Clients["client-a"].Name; got != "Test client" {
		t.Fatalf("persisted client name = %q", got)
	}
}

func TestStoreGarbageCollectionRemovesExpiredReplayHistory(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now().UTC()
	err = store.Update(func(state *oauthState) error {
		state.Clients["client-a"] = testStoreClient()
		state.Grants["grant-a"] = GrantRecord{
			ID: "grant-a", User: "alice", ClientID: "client-a",
			Issuer: "https://wiki.example.test", Resource: "https://wiki.example.test/_/mcp",
			Scopes: []string{"read"}, NamespaceMode: "selected", Namespaces: []string{"notes"},
			CreatedAt: now.Add(-40 * 24 * time.Hour), ExpiresAt: now.Add(-6 * time.Minute),
		}
		state.Families["family-a"] = FamilyRecord{
			ID: "family-a", GrantID: "grant-a", ExpiresAt: now.Add(-6 * time.Minute),
		}
		bindings := TokenRecord{
			GrantID: "grant-a", FamilyID: "family-a", ClientID: "client-a",
			User: "alice", Issuer: "https://wiki.example.test",
			Resource: "https://wiki.example.test/_/mcp",
		}
		digest := tokenDigest("old")
		code := bindings
		code.CodeDigest = digest
		code.CodeExpiresAt = now.Add(-20 * time.Minute)
		state.Tokens["c:"+digest] = code
		access := bindings
		access.AccessDigest = digest
		access.AccessExpiresAt = now.Add(-20 * time.Minute)
		state.Tokens["a:"+digest] = access
		refresh := bindings
		refresh.RefreshDigest = digest
		state.Tokens["r:"+digest] = refresh
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(*oauthState) error { return nil }); err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Grants) != 0 || len(state.Families) != 0 || len(state.Tokens) != 0 {
		t.Fatalf("expired OAuth history was not collected: grants=%d families=%d tokens=%d",
			len(state.Grants), len(state.Families), len(state.Tokens))
	}
}

func TestStoreUpdateRollsBackCallbackError(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	sentinel := errors.New("transaction failed")
	if err := store.Update(func(state *oauthState) error {
		client := testStoreClient()
		state.Clients[client.ID] = client
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("Update error = %v, want callback error", err)
	}
	state, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Clients) != 0 {
		t.Fatalf("failed transaction published %d clients", len(state.Clients))
	}
}

func TestStoreRejectsConcurrentOpen(t *testing.T) {
	dir := t.TempDir()
	first, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	if _, err := OpenStore(dir); !errors.Is(err, errStoreBusy) {
		t.Fatalf("second open error = %v, want busy", err)
	}
}

func TestStoreFailsClosedOnCorruptOrUnknownState(t *testing.T) {
	for name, contents := range map[string]string{
		"corrupt":  `{"version":`,
		"unknown":  `{"version":2,"clients":{},"grants":{},"tokens":{},"families":{}}`,
		"trailing": `{"version":1,"clients":{},"grants":{},"tokens":{},"families":{}}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "oauth.json"), []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			if store, err := OpenStore(dir); err == nil {
				_ = store.Close()
				t.Fatal("OpenStore unexpectedly accepted invalid state")
			}
		})
	}
}

func TestStoreRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "oauth.json")); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenStore(dir); err == nil {
		_ = store.Close()
		t.Fatal("OpenStore unexpectedly accepted symlink")
	}
}

func TestStorePersistenceFailures(t *testing.T) {
	for _, stage := range []string{"write", "short write", "file sync", "close", "rename", "directory open", "directory sync"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			client := testStoreClient()
			if err := store.Update(func(state *oauthState) error {
				state.Clients[client.ID] = client
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("injected persistence failure")
			switch stage {
			case "write":
				store.fs.write = func(*os.File, []byte) (int, error) { return 0, sentinel }
			case "short write":
				store.fs.write = func(*os.File, []byte) (int, error) { return 0, nil }
			case "file sync", "directory sync":
				store.fs.sync = func(file *os.File) error {
					isDir := file.Name() == dir
					if isDir == (stage == "directory sync") {
						return sentinel
					}
					return file.Sync()
				}
			case "close":
				store.fs.close = func(file *os.File) error {
					_ = file.Close()
					return sentinel
				}
			case "rename":
				store.fs.rename = func(string, string) error { return sentinel }
			case "directory open":
				store.fs.openDir = func(string) (*os.File, error) { return nil, sentinel }
			}
			err = store.Update(func(state *oauthState) error {
				client := state.Clients["client-a"]
				client.Disabled = true
				state.Clients[client.ID] = client
				return nil
			})
			if err == nil {
				t.Fatal("failed persistence returned success")
			}
			uncertain := stage == "directory open" || stage == "directory sync"
			if errors.Is(err, errStoreUncertain) != uncertain {
				t.Fatalf("uncertain persistence = %v, want %v: %v", errors.Is(err, errStoreUncertain), uncertain, err)
			}
			state, readErr := store.Snapshot()
			if uncertain {
				if !errors.Is(readErr, errStoreUncertain) {
					t.Fatalf("poisoned store read = %v", readErr)
				}
				called := false
				if err := store.Update(func(*oauthState) error {
					called = true
					return nil
				}); !errors.Is(err, errStoreUncertain) || called {
					t.Fatalf("poisoned store accepted mutation: called=%v err=%v", called, err)
				}
			} else if readErr != nil || state.Clients["client-a"].Disabled {
				t.Fatalf("pre-rename failure changed visible state: %v", readErr)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			state, err = reopened.Snapshot()
			if err != nil || state.Clients["client-a"].Disabled != uncertain {
				t.Fatalf("reopened state does not match rename outcome: %v", err)
			}
		})
	}
}
