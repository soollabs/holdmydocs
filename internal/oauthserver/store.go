package oauthserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"hmd/internal/config"
	"hmd/internal/wiki"
)

const (
	storeSchemaVersion = 1
	maxOAuthClients    = 256
	maxOAuthGrants     = 10000
	maxOAuthTokens     = 50000
)

var (
	errStoreClosed    = errors.New("OAuth store is closed")
	errStoreBusy      = errors.New("OAuth store is already open by another process")
	errStoreUncertain = errors.New("OAuth persistence is uncertain; restart required")
)

// ClientRecord stores only a verifier for confidential client secrets.
type ClientRecord struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	RedirectURIs  []string  `json:"redirect_uris"`
	AuthMethod    string    `json:"auth_method"`
	SecretDigest  string    `json:"secret_digest,omitempty"`
	AllowedScopes []string  `json:"allowed_scopes"`
	Disabled      bool      `json:"disabled,omitempty"`
	Dynamic       bool      `json:"dynamic,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// GrantRecord captures a user's explicit consent and the OAuth client bound to
// it. Selected namespaces are never represented by a nil slice.
type GrantRecord struct {
	ID            string    `json:"id"`
	User          string    `json:"user"`
	ClientID      string    `json:"client_id"`
	Issuer        string    `json:"issuer"`
	Resource      string    `json:"resource"`
	Scopes        []string  `json:"scopes"`
	NamespaceMode string    `json:"namespace_mode"`
	Namespaces    []string  `json:"namespaces"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	RevokedAt     time.Time `json:"revoked_at,omitzero"`
	RevokedReason string    `json:"revoked_reason,omitempty"`
}

// TokenRecord stores digests only. CodeDigest, AccessDigest and RefreshDigest
// are indexes for one library token object; used refresh records are retained
// as replay evidence until family expiry.
type TokenRecord struct {
	CodeDigest        string    `json:"code_digest,omitempty"`
	AccessDigest      string    `json:"access_digest,omitempty"`
	RefreshDigest     string    `json:"refresh_digest,omitempty"`
	GrantID           string    `json:"grant_id"`
	FamilyID          string    `json:"family_id"`
	ClientID          string    `json:"client_id"`
	User              string    `json:"user"`
	RedirectURI       string    `json:"redirect_uri,omitempty"`
	Issuer            string    `json:"issuer"`
	Resource          string    `json:"resource"`
	Scopes            []string  `json:"scopes"`
	NamespaceMode     string    `json:"namespace_mode,omitempty"`
	Namespaces        []string  `json:"namespaces,omitempty"`
	PKCEChallenge     string    `json:"pkce_challenge,omitempty"`
	PKCEChallengeAlg  string    `json:"pkce_challenge_method,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	CodeExpiresAt     time.Time `json:"code_expires_at,omitzero"`
	AccessExpiresAt   time.Time `json:"access_expires_at,omitzero"`
	RefreshExpiresAt  time.Time `json:"refresh_expires_at,omitzero"`
	RefreshGeneration uint64    `json:"refresh_generation,omitempty"`
	ConsumedAt        time.Time `json:"consumed_at,omitzero"`
	RefreshUsedAt     time.Time `json:"refresh_used_at,omitzero"`
	RevokedAt         time.Time `json:"revoked_at,omitzero"`
}

type FamilyRecord struct {
	ID           string    `json:"id"`
	GrantID      string    `json:"grant_id"`
	Generation   uint64    `json:"generation"`
	ExpiresAt    time.Time `json:"expires_at"`
	RevokedAt    time.Time `json:"revoked_at,omitzero"`
	RevokedCause string    `json:"revoked_reason,omitempty"`
}

type oauthState struct {
	Version  int                     `json:"version"`
	Clients  map[string]ClientRecord `json:"clients"`
	Grants   map[string]GrantRecord  `json:"grants"`
	Tokens   map[string]TokenRecord  `json:"tokens"`
	Families map[string]FamilyRecord `json:"families"`
}

func emptyOAuthState() oauthState {
	return oauthState{
		Version:  storeSchemaVersion,
		Clients:  make(map[string]ClientRecord),
		Grants:   make(map[string]GrantRecord),
		Tokens:   make(map[string]TokenRecord),
		Families: make(map[string]FamilyRecord),
	}
}

// Store is a single-process transactional OAuth state store. Open holds an
// exclusive lock until Close; each Update persists a complete working copy
// before publishing it to readers.
type Store struct {
	mu      sync.RWMutex
	path    string
	state   oauthState
	lock    *flock.Flock
	closed  bool
	failure error
	fs      persistenceIO
}

// persistenceIO keeps failure injection local to one store. A failure after
// rename cannot be treated as rollback: the new state may already be on disk.
type persistenceIO struct {
	write   func(*os.File, []byte) (int, error)
	sync    func(*os.File) error
	close   func(*os.File) error
	rename  func(string, string) error
	openDir func(string) (*os.File, error)
}

func defaultPersistenceIO() persistenceIO {
	return persistenceIO{
		write:   (*os.File).Write,
		sync:    (*os.File).Sync,
		close:   (*os.File).Close,
		rename:  os.Rename,
		openDir: os.Open,
	}
}

type oauthTxContextKey struct{}

func transactionState(ctx context.Context) *oauthState {
	state, _ := ctx.Value(oauthTxContextKey{}).(*oauthState)
	return state
}

// Transaction runs fn against one durable working state. TokenStore mutations
// require this context so code consumption and token creation can share a
// single commit boundary.
func (s *Store) Transaction(ctx context.Context, fn func(context.Context) error) error {
	return s.Update(func(state *oauthState) error {
		return fn(context.WithValue(ctx, oauthTxContextKey{}, state))
	})
}

func OpenStore(appDir string) (*Store, error) {
	path := filepath.Join(appDir, "oauth.json")
	lock := flock.New(path+".lock", flock.SetPermissions(0600))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("locking OAuth state: %w", err)
	}
	if !ok {
		return nil, errStoreBusy
	}
	s := &Store{path: path, lock: lock, state: emptyOAuthState(), fs: defaultPersistenceIO()}
	if err := s.load(); err != nil {
		_ = lock.Unlock()
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	info, err := os.Lstat(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat OAuth state: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("OAuth state must be a regular file")
	}
	if err := config.TightenRegularFile(s.path); err != nil {
		return fmt.Errorf("unsafe OAuth state: %w", err)
	}
	file, err := os.Open(s.path)
	if err != nil {
		return fmt.Errorf("open OAuth state: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64<<20))
	if err != nil {
		return fmt.Errorf("read OAuth state: %w", err)
	}
	if len(data) == 64<<20 {
		return fmt.Errorf("OAuth state exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state oauthState
	if err := decoder.Decode(&state); err != nil {
		return fmt.Errorf("decode OAuth state: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("OAuth state contains trailing data")
	}
	if state.Version != storeSchemaVersion {
		return fmt.Errorf("unsupported OAuth state schema version %d", state.Version)
	}
	if state.Clients == nil || state.Grants == nil || state.Tokens == nil || state.Families == nil {
		return fmt.Errorf("OAuth state is missing required collections")
	}
	if err := validateState(state); err != nil {
		return fmt.Errorf("invalid OAuth state: %w", err)
	}
	s.state = state
	return nil
}

func validateState(state oauthState) error {
	if len(state.Clients) > maxOAuthClients || len(state.Grants) > maxOAuthGrants ||
		len(state.Tokens) > maxOAuthTokens || len(state.Families) > maxOAuthGrants {
		return errors.New("OAuth state exceeds record limits")
	}
	for key, client := range state.Clients {
		if key == "" || client.ID != key || client.ID == "" || client.AuthMethod == "" ||
			len(client.RedirectURIs) == 0 || len(client.AllowedScopes) == 0 {
			return fmt.Errorf("invalid OAuth client record %q", key)
		}
		if client.AuthMethod != clientAuthNone && client.AuthMethod != clientAuthBasic && client.AuthMethod != clientAuthPost {
			return fmt.Errorf("invalid OAuth client authentication method %q", key)
		}
		if client.AuthMethod == clientAuthNone && client.SecretDigest != "" ||
			client.AuthMethod != clientAuthNone && client.SecretDigest == "" {
			return fmt.Errorf("invalid OAuth client secret policy %q", key)
		}
		scopes, err := NormaliseScopes(client.AllowedScopes)
		if err != nil || len(scopes) != len(client.AllowedScopes) {
			return fmt.Errorf("invalid OAuth client scopes %q", key)
		}
		for i, redirect := range client.RedirectURIs {
			if validateRedirectURI(redirect) != nil || slices.Contains(client.RedirectURIs[:i], redirect) {
				return fmt.Errorf("invalid OAuth client redirect URI %q", key)
			}
		}
	}
	for key, grant := range state.Grants {
		if key == "" || grant.ID != key || grant.User == "" || grant.ClientID == "" ||
			grant.Resource == "" || (grant.NamespaceMode != "all" && grant.NamespaceMode != "selected") ||
			(grant.NamespaceMode == "selected" && grant.Namespaces == nil) ||
			(grant.NamespaceMode == "all" && grant.Namespaces != nil) {
			return fmt.Errorf("invalid OAuth grant record %q", key)
		}
		client, ok := state.Clients[grant.ClientID]
		if !ok || client.ID != grant.ClientID || grant.Issuer == "" ||
			grant.Resource != grant.Issuer+mcpResourcePath {
			return fmt.Errorf("invalid OAuth grant binding %q", key)
		}
		scopes, err := NormaliseScopes(grant.Scopes)
		if err != nil || len(scopes) != len(grant.Scopes) || !scopeSubset(grant.Scopes, client.AllowedScopes) {
			return fmt.Errorf("invalid OAuth grant scopes %q", key)
		}
		if grant.NamespaceMode == "selected" {
			if _, err := validateNamespaceSelection(grant.Namespaces, grant.Namespaces); err != nil {
				return fmt.Errorf("invalid OAuth grant namespaces %q", key)
			}
			for _, namespace := range grant.Namespaces {
				if !wiki.ValidNamespaceName(namespace) {
					return fmt.Errorf("invalid OAuth grant namespace %q", key)
				}
			}
		}
	}
	for key, family := range state.Families {
		grant, ok := state.Grants[family.GrantID]
		if key == "" || family.ID != key || !ok || family.GrantID == "" ||
			!grant.ExpiresAt.IsZero() && !family.ExpiresAt.IsZero() && family.ExpiresAt.After(grant.ExpiresAt) {
			return fmt.Errorf("invalid OAuth token family %q", key)
		}
	}
	for key, token := range state.Tokens {
		if key == "" || token.GrantID == "" || token.ClientID == "" ||
			token.CodeDigest == "" && token.AccessDigest == "" && token.RefreshDigest == "" {
			return fmt.Errorf("invalid OAuth token record %q", key)
		}
		grant, grantOK := state.Grants[token.GrantID]
		family, familyOK := state.Families[token.FamilyID]
		if !grantOK || !familyOK || family.GrantID != grant.ID ||
			token.ClientID != grant.ClientID || token.User != grant.User ||
			token.Issuer != grant.Issuer || token.Resource != grant.Resource {
			return fmt.Errorf("invalid OAuth token binding %q", key)
		}
		tokenScopes, err := NormaliseScopes(token.Scopes)
		if err != nil || len(tokenScopes) != len(token.Scopes) ||
			!scopeSubset(token.Scopes, grant.Scopes) {
			return fmt.Errorf("invalid OAuth token scopes %q", key)
		}
		kind, digest, ok := strings.Cut(key, ":")
		if !ok || len(digest) != 64 {
			return fmt.Errorf("invalid OAuth token digest key %q", key)
		}
		switch kind {
		case "c":
			if token.CodeDigest != digest {
				return fmt.Errorf("invalid OAuth code index %q", key)
			}
		case "a":
			if token.AccessDigest != digest {
				return fmt.Errorf("invalid OAuth access-token index %q", key)
			}
		case "r":
			if token.RefreshDigest != digest {
				return fmt.Errorf("invalid OAuth refresh-token index %q", key)
			}
		default:
			return fmt.Errorf("invalid OAuth token index kind %q", key)
		}
	}
	return nil
}

func cloneState(state oauthState) (oauthState, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return oauthState{}, err
	}
	var clone oauthState
	if err := json.Unmarshal(data, &clone); err != nil {
		return oauthState{}, err
	}
	return clone, nil
}

// Snapshot returns a detached copy for read-only policy decisions.
func (s *Store) Snapshot() (oauthState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return oauthState{}, errStoreClosed
	}
	if s.failure != nil {
		return oauthState{}, s.failure
	}
	return cloneState(s.state)
}

// Update applies fn to a private working copy and atomically persists it.
// Failed callbacks or writes leave the visible in-memory state unchanged.
func (s *Store) Update(fn func(*oauthState) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errStoreClosed
	}
	if s.failure != nil {
		return s.failure
	}
	next, err := cloneState(s.state)
	if err != nil {
		return err
	}
	garbageCollectOAuthState(&next, time.Now().UTC())
	if err := fn(&next); err != nil {
		return err
	}
	if next.Version != storeSchemaVersion {
		return fmt.Errorf("unsupported OAuth state schema version %d", next.Version)
	}
	if err := validateState(next); err != nil {
		return err
	}
	if replaced, err := s.save(next); err != nil {
		if replaced {
			// Keep the process lock, but reject all reads and writes until
			// reopening reconciles the store with the on-disk state.
			s.failure = errors.Join(errStoreUncertain, err)
			return s.failure
		}
		return err
	}
	s.state = next
	return nil
}

func garbageCollectOAuthState(state *oauthState, now time.Time) {
	const replayRetention = 5 * time.Minute
	const codeRetention = 10 * time.Minute

	for key, token := range state.Tokens {
		switch {
		case strings.HasPrefix(key, "c:"):
			expired := !token.CodeExpiresAt.IsZero() && now.After(token.CodeExpiresAt.Add(codeRetention))
			consumed := !token.ConsumedAt.IsZero() && now.After(token.ConsumedAt.Add(codeRetention))
			if expired || consumed {
				delete(state.Tokens, key)
			}
		case strings.HasPrefix(key, "a:"):
			family, exists := state.Families[token.FamilyID]
			if !exists || !family.ExpiresAt.IsZero() && now.After(family.ExpiresAt.Add(replayRetention)) ||
				!token.AccessExpiresAt.IsZero() && now.After(token.AccessExpiresAt.Add(replayRetention)) ||
				!token.RevokedAt.IsZero() {
				delete(state.Tokens, key)
			}
		case strings.HasPrefix(key, "r:"):
			family, exists := state.Families[token.FamilyID]
			if !exists || !family.ExpiresAt.IsZero() && now.After(family.ExpiresAt.Add(replayRetention)) {
				delete(state.Tokens, key)
			}
		}
	}
	for id, family := range state.Families {
		if !family.ExpiresAt.IsZero() && now.After(family.ExpiresAt.Add(replayRetention)) {
			delete(state.Families, id)
		}
	}
	for id, grant := range state.Grants {
		if !grant.ExpiresAt.IsZero() && now.After(grant.ExpiresAt.Add(replayRetention)) {
			delete(state.Grants, id)
		}
	}
}

func (s *Store) save(state oauthState) (bool, error) {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode OAuth state: %w", err)
	}
	dir := filepath.Dir(s.path)
	file, err := os.CreateTemp(dir, ".oauth-*.tmp")
	if err != nil {
		return false, fmt.Errorf("create OAuth state temporary file: %w", err)
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("secure OAuth state temporary file: %w", err)
	}
	if n, err := s.fs.write(file, data); err != nil || n != len(data) {
		_ = file.Close()
		if err == nil {
			err = io.ErrShortWrite
		}
		return false, fmt.Errorf("write OAuth state: %w", err)
	}
	if err := s.fs.sync(file); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("sync OAuth state: %w", err)
	}
	if err := s.fs.close(file); err != nil {
		return false, fmt.Errorf("close OAuth state: %w", err)
	}
	if err := s.fs.rename(tmp, s.path); err != nil {
		return false, fmt.Errorf("replace OAuth state: %w", err)
	}
	d, err := s.fs.openDir(dir)
	if err != nil {
		return true, fmt.Errorf("open OAuth state directory: %w", err)
	}
	defer d.Close()
	if err := s.fs.sync(d); err != nil {
		return true, fmt.Errorf("sync OAuth state directory: %w", err)
	}
	return true, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.lock.Unlock()
}
