package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// tokenRecord is a stored personal access token. The token value is shown
// once at creation; only its bcrypt hash is kept. A zero Expires means the
// token never expires.
type tokenRecord struct {
	Name       string    `json:"name"`
	Hash       string    `json:"hash"`
	Created    time.Time `json:"created"`
	Expires    time.Time `json:"expires,omitzero"`
	Scopes     []string  `json:"scopes,omitempty"`
	Namespaces []string  `json:"namespaces,omitempty"`
}

// expired reports whether the token is past its expiry (never, if unset).
func (t tokenRecord) expired() bool {
	return !t.Expires.IsZero() && time.Now().After(t.Expires)
}

// cachedToken is a verified PAT in the in-memory cache; expiry still has to
// be checked on every use, so it rides along with the username.
type cachedToken struct {
	user       string
	scopes     []string
	namespaces []string
	expires    time.Time
}

type tokenPrincipal struct {
	User       string
	Namespaces []string
	Scopes     []string
}

func (p tokenPrincipal) HasScope(need scope) bool {
	if p.Scopes != nil && len(p.Scopes) == 0 {
		return false
	}
	return userRecord{Scopes: p.Scopes}.hasScope(need)
}

func (p tokenPrincipal) Restricted() bool {
	return !p.HasScope(scopeSettings) && p.Namespaces != nil
}

func (p tokenPrincipal) AllowsNamespace(namespace string) bool {
	if !p.Restricted() {
		return true
	}
	for _, allowed := range p.Namespaces {
		if allowed == namespace {
			return true
		}
	}
	return false
}

func (p tokenPrincipal) AllowsSlug(slug string) bool {
	namespace, _ := namespaceFor(slug)
	return p.AllowsNamespace(namespace)
}

// A scope gates one slice of the app: scopeRead covers viewing pages and
// search, scopeWrite covers anything that writes to the store (saving,
// renaming, tagging, uploading), scopeSettings covers /settings itself
// (appearance, tokens, author, exports).
type scope string

const (
	scopeRead     scope = "read"
	scopeWrite    scope = "write"
	scopeSettings scope = "settings"
)

// allScopes is both the valid-scope allowlist (for CLI/validation) and the
// full-access set new users get by default.
var allScopes = []scope{scopeRead, scopeWrite, scopeSettings}

// userRecord is a stored user. GitAuthor, when set, is that user's commit
// identity in "Name <email>" form and overrides the global default.
// Palette/FontUI/FontMono/Skin are that user's cosmetic preferences,
// editable from /settings; zero values fall back to the built-in defaults.
type userRecord struct {
	Hash      string        `json:"hash"`
	OIDC      *oidcIdentity `json:"oidc,omitempty"`
	GitAuthor string        `json:"git_author,omitempty"`
	Tokens    []tokenRecord `json:"tokens,omitempty"`
	Palette   string        `json:"palette,omitempty"`
	FontUI    string        `json:"font_ui,omitempty"`
	FontMono  string        `json:"font_mono,omitempty"`
	Skin      string        `json:"skin,omitempty"`
	Scopes    []string      `json:"scopes,omitempty"` // empty = full access for existing local accounts
}

type oidcIdentity struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

// hasScope reports whether the user may perform an action requiring s. An
// empty Scopes list means full access, so upgrading an existing install
// doesn't lock out every user already in users.json.
func (u userRecord) hasScope(s scope) bool {
	if len(u.Scopes) == 0 {
		return true
	}
	for _, have := range u.Scopes {
		if have == string(scopeSettings) {
			return true
		}
	}
	for _, have := range u.Scopes {
		if have == string(s) {
			return true
		}
	}
	return false
}

// prefs is name's display preferences.
func (a *Auth) prefs(name string) userRecord {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.users[name]
}

// SetPrefs stores name's display preferences (palette, fonts, skin).
func (a *Auth) SetPrefs(name, palette, fontUI, fontMono, skin string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.users[name]
	if !ok {
		return fmt.Errorf("unknown user %q", name)
	}
	rec.Palette = palette
	rec.FontUI = fontUI
	rec.FontMono = fontMono
	rec.Skin = skin
	a.users[name] = rec
	return a.save()
}

// SetScopes restricts name to exactly the given scopes ("read", "write",
// "settings"); an empty list restores full access. Unknown scope names are
// rejected rather than silently dropped, since a typo here is a permissions
// bug, not a cosmetic one.
func (a *Auth) SetScopes(name string, scopes []string) error {
	for _, s := range scopes {
		valid := false
		for _, allowed := range allScopes {
			if s == string(allowed) {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("unknown scope %q (want read, write, settings)", s)
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.users[name]
	if !ok {
		return fmt.Errorf("unknown user %q", name)
	}
	rec.Scopes = scopes
	a.users[name] = rec
	return a.save()
}

// ctxUserKey carries the Bearer-authenticated username through the request
// context; currentUser checks it before falling back to the session cookie.
type ctxUserKey struct{}

type ctxTokenPrincipalKey struct{}

func tokenPrincipalFromContext(ctx context.Context) (tokenPrincipal, bool) {
	principal, ok := ctx.Value(ctxTokenPrincipalKey{}).(tokenPrincipal)
	return principal, ok
}

func tokenAllowsNamespace(ctx context.Context, namespace string) bool {
	principal, ok := tokenPrincipalFromContext(ctx)
	return !ok || principal.AllowsNamespace(namespace)
}

func tokenAllowsSlug(ctx context.Context, slug string) bool {
	principal, ok := tokenPrincipalFromContext(ctx)
	return !ok || principal.AllowsSlug(slug)
}

// sessionTTL is the absolute lifetime of a session token, regardless of the
// cookie's own MaxAge (browser-session cookies are still bounded server-side,
// so a leaked/persisted token can't be replayed forever).
const sessionTTL = 30 * 24 * time.Hour

// sessionRecord is a stored login session: the username it belongs to and
// when it stops being valid.
type sessionRecord struct {
	User    string    `json:"user"`
	Expires time.Time `json:"expires"`
}

func (s sessionRecord) expired() bool {
	return time.Now().After(s.Expires)
}

type Auth struct {
	usersFile    string
	sessionsFile string
	users        map[string]userRecord    // username -> record
	sessions     map[string]sessionRecord // token -> session
	tokenCache   map[string]cachedToken   // verified PAT value -> user + expiry
	mu           sync.RWMutex
}

// normaliseUserRecord validates persisted access policy. Appearance preferences
// remain permissive so removing a theme option never blocks startup. Session
// data is deliberately excluded: it is disposable and handled best-effort below.
func normaliseUserRecord(name string, rec userRecord) (userRecord, error) {
	scopes, err := normaliseTokenScopes(rec.Scopes)
	if err != nil {
		return userRecord{}, fmt.Errorf("user %q scopes: %w", name, err)
	}
	rec.Scopes = scopes
	seen := make(map[string]struct{}, len(rec.Tokens))
	for i := range rec.Tokens {
		token := &rec.Tokens[i]
		token.Name = strings.TrimSpace(token.Name)
		if token.Name == "" {
			return userRecord{}, fmt.Errorf("user %q has a token without a name", name)
		}
		if _, ok := seen[token.Name]; ok {
			return userRecord{}, fmt.Errorf("user %q has duplicate token name %q", name, token.Name)
		}
		seen[token.Name] = struct{}{}
		if _, err := bcrypt.Cost([]byte(token.Hash)); err != nil {
			return userRecord{}, fmt.Errorf("user %q token %q has invalid hash: %w", name, token.Name, err)
		}
		tokenScopes, err := normaliseTokenScopes(token.Scopes)
		if err != nil {
			return userRecord{}, fmt.Errorf("user %q token %q scopes: %w", name, token.Name, err)
		}
		if token.Scopes != nil && len(tokenScopes) == 0 {
			return userRecord{}, fmt.Errorf("user %q token %q scopes cannot be empty", name, token.Name)
		}
		token.Scopes = tokenScopes
		namespaces, err := normaliseTokenNamespaces(token.Namespaces)
		if err != nil {
			return userRecord{}, fmt.Errorf("user %q token %q namespaces: %w", name, token.Name, err)
		}
		token.Namespaces = namespaces
	}
	return rec, nil
}

func OpenAuth(cfg Config) (*Auth, error) {
	// Create app dir if needed
	err := os.MkdirAll(cfg.AppDir, 0755)
	if err != nil {
		return nil, fmt.Errorf("creating app dir: %w", err)
	}

	usersFile := filepath.Join(cfg.AppDir, "users.json")
	auth := &Auth{
		usersFile:    usersFile,
		sessionsFile: filepath.Join(cfg.AppDir, "sessions.json"),
		users:        make(map[string]userRecord),
		sessions:     make(map[string]sessionRecord),
		tokenCache:   make(map[string]cachedToken),
	}

	// Try to load existing users file
	data, err := os.ReadFile(usersFile)
	if err == nil {
		// File exists, load users
		err = json.Unmarshal(data, &auth.users)
		if err != nil {
			return nil, fmt.Errorf("unmarshalling users: %w", err)
		}
		for name, rec := range auth.users {
			rec, err = normaliseUserRecord(name, rec)
			if err != nil {
				return nil, err
			}
			auth.users[name] = rec
		}
	} else if os.IsNotExist(err) {
		// File doesn't exist
		if cfg.AdminUser != "" && cfg.AdminPass != "" {
			// Bootstrap admin user
			err = auth.AddUser(cfg.AdminUser, cfg.AdminPass)
			if err != nil {
				return nil, fmt.Errorf("bootstrapping admin: %w", err)
			}
			slog.Info("bootstrapped admin user", "user", cfg.AdminUser)
		} else {
			// No bootstrap, empty users
			slog.Warn("no users.json and no HMD_ADMIN_USER/HMD_ADMIN_PASSWORD set; set them and restart to create the first user")
		}
	} else {
		return nil, fmt.Errorf("reading users file: %w", err)
	}

	// Session persistence is best-effort. A missing or corrupt file means users
	// log in again, so startup continues.
	if data, err := os.ReadFile(auth.sessionsFile); err == nil {
		_ = json.Unmarshal(data, &auth.sessions)
	}

	return auth, nil
}

func (a *Auth) AddUser(name, password string) error {
	if !validUsername(name) {
		return fmt.Errorf("invalid username")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	a.mu.Lock()
	rec := a.users[name] // preserve any existing git author
	rec.Hash = string(hash)
	a.users[name] = rec
	err = a.save()
	a.mu.Unlock()

	if err == nil {
		slog.Info("user added", "user", name)
	}
	return err
}

func validUsername(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// UserExists reports whether name has a user record.
func (a *Auth) UserExists(name string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.users[name]
	return ok
}

// UserSummary is one row in the settings-page user list. Has is keyed by
// scope name ("read", "write", "settings") so the template can tick the
// right checkboxes without needing custom template funcs.
type UserSummary struct {
	Name string
	Has  map[string]bool
}

// Users lists every user, sorted by name, for the settings page.
func (a *Auth) Users() []UserSummary {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]UserSummary, 0, len(a.users))
	for name, rec := range a.users {
		has := make(map[string]bool, len(allScopes))
		for _, s := range allScopes {
			has[string(s)] = rec.hasScope(s)
		}
		out = append(out, UserSummary{Name: name, Has: has})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func normaliseTokenNamespaces(namespaces []string) ([]string, error) {
	seen := make(map[string]struct{}, len(namespaces))
	for _, raw := range namespaces {
		// A nil list permits every namespace; an entry names one it's limited to.
		namespace := strings.TrimSpace(raw)
		if !validNamespaceName(namespace) {
			return nil, fmt.Errorf("invalid namespace %q", namespace)
		}
		seen[namespace] = struct{}{}
	}
	if len(seen) == 0 {
		return nil, nil
	}
	normalised := make([]string, 0, len(seen))
	for namespace := range seen {
		normalised = append(normalised, namespace)
	}
	sort.Strings(normalised)
	return normalised, nil
}

func normaliseTokenScopes(scopes []string) ([]string, error) {
	seen := make(map[string]struct{}, len(scopes))
	for _, raw := range scopes {
		name := strings.TrimSpace(raw)
		valid := false
		for _, allowed := range allScopes {
			if name == string(allowed) {
				valid = true
				break
			}
		}
		if !valid {
			return nil, fmt.Errorf("unknown scope %q", name)
		}
		seen[name] = struct{}{}
	}
	if len(seen) == 0 {
		return nil, nil
	}
	normalised := make([]string, 0, len(seen))
	for name := range seen {
		normalised = append(normalised, name)
	}
	sort.Strings(normalised)
	return normalised, nil
}

func effectiveTokenScopes(userScopes, tokenScopes []string) []string {
	if tokenScopes == nil {
		if len(userScopes) == 0 {
			return nil
		}
		return append([]string(nil), userScopes...)
	}
	if len(userScopes) == 0 {
		return append([]string(nil), tokenScopes...)
	}
	user := userRecord{Scopes: userScopes}
	result := make([]string, 0, len(tokenScopes))
	for _, name := range tokenScopes {
		if user.hasScope(scope(name)) {
			result = append(result, name)
		}
	}
	return result
}

// AddToken mints a personal access token for name, labelled label, expiring
// at expires (zero = never). The token value is returned exactly once; only
// its bcrypt hash is stored. Labels are unique per user — they are the
// revocation key. A nil scope list preserves the legacy unrestricted-token
// behaviour; a non-nil empty list is invalid for a newly scoped token.
func (a *Auth) AddToken(name, label string, expires time.Time, scopes, namespaces []string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.users[name]
	if !ok {
		return "", fmt.Errorf("unknown user %q", name)
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return "", fmt.Errorf("token name cannot be blank")
	}
	for _, t := range rec.Tokens {
		if t.Name == label {
			return "", fmt.Errorf("a token named %q already exists", label)
		}
	}
	normalisedScopes, err := normaliseTokenScopes(scopes)
	if err != nil {
		return "", err
	}
	if scopes != nil && len(normalisedScopes) == 0 {
		return "", fmt.Errorf("token scopes cannot be empty")
	}
	for _, scopeName := range normalisedScopes {
		if !rec.hasScope(scope(scopeName)) {
			return "", fmt.Errorf("user %q does not have scope %q", name, scopeName)
		}
	}
	normalisedNamespaces, err := normaliseTokenNamespaces(namespaces)
	if err != nil {
		return "", err
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	token := "hmd_" + hex.EncodeToString(b)

	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hashing token: %w", err)
	}
	rec.Tokens = append(rec.Tokens, tokenRecord{
		Name:       label,
		Hash:       string(hash),
		Created:    time.Now(),
		Expires:    expires,
		Scopes:     normalisedScopes,
		Namespaces: normalisedNamespaces,
	})
	a.users[name] = rec
	if err := a.save(); err != nil {
		return "", err
	}
	return token, nil
}

// TokensFor returns name's stored tokens (metadata only — the hashes are of
// no use to callers and stay out of templates).
func (a *Auth) TokensFor(name string) []tokenRecord {
	a.mu.RLock()
	defer a.mu.RUnlock()
	tokens := make([]tokenRecord, 0, len(a.users[name].Tokens))
	for _, t := range a.users[name].Tokens {
		tokens = append(tokens, tokenRecord{
			Name:       t.Name,
			Created:    t.Created,
			Expires:    t.Expires,
			Scopes:     append([]string(nil), t.Scopes...),
			Namespaces: append([]string(nil), t.Namespaces...),
		})
	}
	return tokens
}

// RemoveToken revokes name's token labelled label. All of name's cached
// verifications are dropped — we can't tell which cached value matched the
// removed hash without re-running bcrypt, so the user's other tokens simply
// re-verify on next use.
func (a *Auth) RemoveToken(name, label string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.users[name]
	if !ok {
		return fmt.Errorf("unknown user %q", name)
	}
	kept := rec.Tokens[:0]
	for _, t := range rec.Tokens {
		if t.Name != label {
			kept = append(kept, t)
		}
	}
	if len(kept) == len(rec.Tokens) {
		return fmt.Errorf("no token named %q", label)
	}
	rec.Tokens = kept
	a.users[name] = rec
	for value, cached := range a.tokenCache {
		if cached.user == name {
			delete(a.tokenCache, value)
		}
	}
	return a.save()
}

// UserForBearer resolves a Bearer PAT value to its raw policy. Verified tokens
// are cached so bcrypt runs once per token per process, not per request; the
// current user's scopes are deliberately not cached here.
// linear scan over users' tokens on first use; fine for a handful of users
func (a *Auth) UserForBearer(token string) (tokenPrincipal, bool) {
	if !strings.HasPrefix(token, "hmd_") {
		return tokenPrincipal{}, false
	}

	a.mu.RLock()
	if cached, ok := a.tokenCache[token]; ok {
		a.mu.RUnlock()
		// Expiry is wall-clock, so the cache can't answer it once and for
		// all — check on every use.
		if !cached.expires.IsZero() && time.Now().After(cached.expires) {
			return tokenPrincipal{}, false
		}
		return tokenPrincipal{
			User:       cached.user,
			Scopes:     append([]string(nil), cached.scopes...),
			Namespaces: append([]string(nil), cached.namespaces...),
		}, true
	}
	// Snapshot the candidate tokens so the slow bcrypt compares run unlocked.
	type candidate struct {
		user string
		tok  tokenRecord
	}
	var candidates []candidate
	for user, rec := range a.users {
		for _, t := range rec.Tokens {
			candidates = append(candidates, candidate{user, t})
		}
	}
	a.mu.RUnlock()

	for _, c := range candidates {
		if bcrypt.CompareHashAndPassword([]byte(c.tok.Hash), []byte(token)) == nil {
			if c.tok.expired() {
				return tokenPrincipal{}, false
			}
			a.mu.Lock()
			a.tokenCache[token] = cachedToken{
				user:       c.user,
				scopes:     append([]string(nil), c.tok.Scopes...),
				namespaces: append([]string(nil), c.tok.Namespaces...),
				expires:    c.tok.Expires,
			}
			a.mu.Unlock()
			return tokenPrincipal{
				User:       c.user,
				Scopes:     append([]string(nil), c.tok.Scopes...),
				Namespaces: append([]string(nil), c.tok.Namespaces...),
			}, true
		}
	}
	return tokenPrincipal{}, false
}

// AuthorFor returns the user's stored git author string, or "" if unset.
func (a *Auth) AuthorFor(name string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.users[name].GitAuthor
}

// SetAuthor stores a per-user git author ("Name <email>", or "" to clear).
func (a *Auth) SetAuthor(name, author string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.users[name]
	if !ok {
		return fmt.Errorf("unknown user %q", name)
	}
	rec.GitAuthor = author
	a.users[name] = rec
	return a.save()
}

// save writes the users map atomically. Caller must hold a.mu.
func (a *Auth) save() error {
	data, err := json.MarshalIndent(a.users, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling users: %w", err)
	}

	tmpFile := a.usersFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("writing tmp file: %w", err)
	}
	if err := os.Rename(tmpFile, a.usersFile); err != nil {
		return fmt.Errorf("renaming tmp file: %w", err)
	}
	return nil
}

// saveSessions writes the sessions map atomically. Caller must hold a.mu.
func (a *Auth) saveSessions() error {
	data, err := json.Marshal(a.sessions)
	if err != nil {
		return fmt.Errorf("marshalling sessions: %w", err)
	}

	tmpFile := a.sessionsFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return fmt.Errorf("writing tmp file: %w", err)
	}
	return os.Rename(tmpFile, a.sessionsFile)
}

func (a *Auth) Login(name, password string) (token string, ok bool) {
	a.mu.RLock()
	rec, exists := a.users[name]
	a.mu.RUnlock()

	// Empty hash marks an SSO-provisioned user: password login must always
	// fail for those records, so guard before bcrypt (which would error on
	// an empty hash anyway, but that is too subtle to rely on).
	if !exists || rec.Hash == "" {
		return "", false
	}

	err := bcrypt.CompareHashAndPassword([]byte(rec.Hash), []byte(password))
	if err != nil {
		return "", false
	}

	return a.newSession(name)
}

// newSession mints a session token for name and persists it.
func (a *Auth) newSession(name string) (token string, ok bool) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", false
	}
	token = hex.EncodeToString(b)

	a.mu.Lock()
	a.sessions[token] = sessionRecord{User: name, Expires: time.Now().Add(sessionTTL)}
	err := a.saveSessions()
	a.mu.Unlock()
	if err != nil {
		slog.Warn("saving sessions", "error", err)
	}

	return token, true
}

// EnsureOIDCUser provisions name on first SSO login: a record with an empty
// hash (password login impossible). gitAuthor ("Name <email>") is stored only
// when the record has none, so a user's own override is never clobbered.
func (a *Auth) EnsureOIDCUser(identity oidcIdentity, name, gitAuthor string, scopes []string) (string, error) {
	if identity.Issuer == "" || identity.Subject == "" || !validUsername(name) || len(scopes) == 0 {
		return "", fmt.Errorf("invalid OIDC user")
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	for username, rec := range a.users {
		if rec.OIDC != nil && *rec.OIDC == identity {
			return username, nil
		}
	}
	if _, exists := a.users[name]; exists {
		return "", fmt.Errorf("OIDC display username already exists")
	}
	a.users[name] = userRecord{OIDC: &identity, GitAuthor: gitAuthor, Scopes: append([]string(nil), scopes...)}
	if err := a.save(); err != nil {
		return "", err
	}
	slog.Info("provisioned OIDC user", "user", name)
	return name, nil
}

func (a *Auth) Logout(token string) {
	a.mu.Lock()
	delete(a.sessions, token)
	err := a.saveSessions()
	a.mu.Unlock()
	if err != nil {
		slog.Warn("saving sessions", "error", err)
	}
}

// UserFor resolves a session token to its username. An expired session is
// treated as absent; it is lazily dropped on the next Logout or load rather
// than requiring a background sweep.
func (a *Auth) UserFor(token string) (username string, ok bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	rec, exists := a.sessions[token]
	if !exists || rec.expired() {
		return "", false
	}
	return rec.User, true
}

// requiredScope reports which scope r needs. /settings and /admin (any
// method) need "settings". Everything else follows HTTP method: a
// body-carrying method needs "write", a safe one needs "read". MCP scopes
// are checked by each tool because its JSON-RPC endpoint carries all actions.
//
// method-based, not route-based, so a handful of read-only-looking
// POSTs (e.g. /search) don't exist — check with the route table in
// handlers.go if a new write-shaped GET or read-shaped POST is ever added.
func requiredScope(r *http.Request) scope {
	if r.URL.Path == "/_/settings" || strings.HasPrefix(r.URL.Path, "/_/settings/") ||
		r.URL.Path == "/_/admin" || strings.HasPrefix(r.URL.Path, "/_/admin/") ||
		r.URL.Path == "/_/namespaces" || strings.HasPrefix(r.URL.Path, "/_/namespaces/") {
		return scopeSettings
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return scopeRead
	}
	return scopeWrite
}

// anonymousEligible reports whether an unauthenticated GET to path (with
// query q) may reach its handler at all, deferring the public-or-404
// decision to the handler itself rather than redirecting to login. That
// keeps the decision in one place (namespace lookup + existence check) so a
// private page and a nonexistent page come out byte-identical — a redirect
// here for one case and a 404 there for the other would itself be an
// existence oracle.
//
// Eligible: a plain page view (no ?do=, not under the reserved /_/ subtree)
// and page attachments (/_/attachments/{slug}/{file}), which live under
// /_/ but belong to a page like any other. Everything else — POSTs, ?do=
// actions, the rest of the /_/ subtree, and the site root ("/", so a login
// wall stays reachable even on an all-private wiki) — stays behind auth.
func anonymousEligible(path string, q url.Values) bool {
	if rest, ok := strings.CutPrefix(path, "/_/attachments/"); ok && strings.Contains(rest, "/") {
		return true
	}
	p := strings.TrimPrefix(path, "/")
	if p == "" || reservedPath(p) {
		return false
	}
	return q.Get("do") == ""
}

func restrictedTokenPathAllowed(path string) bool {
	if path == "/_/mcp" {
		return true
	}
	if !strings.HasPrefix(path, "/_/") {
		return path != "/"
	}

	for _, prefix := range []string{"/_/attachments/", "/_/api/attachments/", "/_/api/preview/", "/_/api/search/attachments", "/_/hidden/", "/_/tags/", "/_/namespaces/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	for _, exact := range []string{"/_/api/preview", "/_/search", "/_/search/attachments", "/_/api/search", "/_/api/search/attachments", "/_/api/health", "/_/tags", "/_/health-report", "/_/hidden", "/_/new", "/_/namespaces"} {
		if path == exact {
			return true
		}
	}
	return false
}

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow /_/login, the OIDC flow and /_/static/ without authentication
		if r.URL.Path == "/_/login" || strings.HasPrefix(r.URL.Path, "/_/auth/oidc/") || strings.HasPrefix(r.URL.Path, "/_/static/") || strings.HasPrefix(r.URL.Path, "/_/api/attachment-uploads/") {
			next.ServeHTTP(w, r)
			return
		}

		isAPI := strings.HasPrefix(r.URL.Path, "/_/api/") || r.URL.Path == "/_/mcp"

		// API namespaces never redirect to the login page: auth failure is
		// a JSON body so agents and apps get a parseable answer.
		deny := func(status int, msg string) {
			if isAPI {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if _, err := w.Write([]byte(`{"error":"` + msg + `"}`)); err != nil {
					slog.Debug("writing auth error response", "err", err)
				}
				return
			}
			if status == http.StatusForbidden {
				http.Error(w, "403 Forbidden: missing "+msg+" access", http.StatusForbidden)
				return
			}
			http.Redirect(w, r, "/_/login", http.StatusSeeOther)
		}

		var user string
		var authed bool
		var bearer bool
		var rawPrincipal tokenPrincipal

		// Bearer PAT: an explicit credential, so a bad one is denied rather
		// than falling through to the cookie check.
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			rawPrincipal, authed = a.UserForBearer(strings.TrimPrefix(h, "Bearer "))
			if authed {
				user = rawPrincipal.User
				bearer = true
			}
		} else if cookie, err := r.Cookie("hmd_session"); err == nil && cookie.Value != "" {
			user, authed = a.UserFor(cookie.Value)
		}
		if !authed {
			if r.Method == http.MethodGet && anonymousEligible(r.URL.Path, r.URL.Query()) {
				next.ServeHTTP(w, r)
				return
			}
			deny(http.StatusUnauthorized, "unauthorized")
			return
		}

		prefs := a.prefs(user)
		need := "mcp authentication"
		authorized := r.URL.Path == "/_/mcp"
		principal := rawPrincipal
		if !authorized {
			need = string(requiredScope(r))
			if bearer {
				principal.Scopes = effectiveTokenScopes(prefs.Scopes, rawPrincipal.Scopes)
				authorized = principal.HasScope(requiredScope(r))
			} else {
				authorized = prefs.hasScope(requiredScope(r))
			}
		}
		if !authorized {
			deny(http.StatusForbidden, need)
			return
		}
		if bearer && principal.Restricted() && !restrictedTokenPathAllowed(r.URL.Path) {
			deny(http.StatusForbidden, "namespace")
			return
		}

		ctx := context.WithValue(r.Context(), ctxUserKey{}, user)
		if bearer {
			if authorized {
				// Resolve the effective policy even for the MCP authentication path;
				// its tool-level checks can consume the request principal later.
				principal.Scopes = effectiveTokenScopes(prefs.Scopes, rawPrincipal.Scopes)
			}
			ctx = context.WithValue(ctx, ctxTokenPrincipalKey{}, principal)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
