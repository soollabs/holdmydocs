package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
	Name    string    `json:"name"`
	Hash    string    `json:"hash"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires,omitzero"`
}

// expired reports whether the token is past its expiry (never, if unset).
func (t tokenRecord) expired() bool {
	return !t.Expires.IsZero() && time.Now().After(t.Expires)
}

// cachedToken is a verified PAT in the in-memory cache; expiry still has to
// be checked on every use, so it rides along with the username.
type cachedToken struct {
	user    string
	expires time.Time
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
// WidgetsAdd/WidgetsRemove let a user tweak a single widget without leaving
// their skin.
type userRecord struct {
	Hash          string        `json:"hash"`
	GitAuthor     string        `json:"git_author,omitempty"`
	Tokens        []tokenRecord `json:"tokens,omitempty"`
	Palette       string        `json:"palette,omitempty"`
	FontUI        string        `json:"font_ui,omitempty"`
	FontMono      string        `json:"font_mono,omitempty"`
	Skin          string        `json:"skin,omitempty"`
	WidgetsAdd    []string      `json:"widgets_add,omitempty"`
	WidgetsRemove []string      `json:"widgets_remove,omitempty"`
	Scopes        []string      `json:"scopes,omitempty"` // empty = full access (default, and every user before scopes existed)
}

// hasScope reports whether the user may perform an action requiring s. An
// empty Scopes list means full access, so upgrading an existing install
// doesn't lock out every user already in users.json.
func (u userRecord) hasScope(s scope) bool {
	if len(u.Scopes) == 0 {
		return true
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

// SetPrefs stores name's display preferences (palette, fonts, skin and the
// per-widget add/remove overrides).
func (a *Auth) SetPrefs(name, palette, fontUI, fontMono, skin string, widgetsAdd, widgetsRemove []string) error {
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
	rec.WidgetsAdd = widgetsAdd
	rec.WidgetsRemove = widgetsRemove
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

type Auth struct {
	usersFile    string
	sessionsFile string
	users        map[string]userRecord  // username -> record
	sessions     map[string]string      // token -> username
	tokenCache   map[string]cachedToken // verified PAT value -> user + expiry
	garden       bool                   // allow /garden/ through unauthenticated (restart-required flag)
	mu           sync.RWMutex
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
		sessions:     make(map[string]string),
		tokenCache:   make(map[string]cachedToken),
		garden:       cfg.Garden.Enabled,
	}

	// Try to load existing users file
	data, err := os.ReadFile(usersFile)
	if err == nil {
		// File exists, load users
		err = json.Unmarshal(data, &auth.users)
		if err != nil {
			return nil, fmt.Errorf("unmarshalling users: %w", err)
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

// AddToken mints a personal access token for name, labelled label, expiring
// at expires (zero = never). The token value is returned exactly once; only
// its bcrypt hash is stored. Labels are unique per user — they are the
// revocation key.
func (a *Auth) AddToken(name, label string, expires time.Time) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.users[name]
	if !ok {
		return "", fmt.Errorf("unknown user %q", name)
	}
	for _, t := range rec.Tokens {
		if t.Name == label {
			return "", fmt.Errorf("a token named %q already exists", label)
		}
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
	rec.Tokens = append(rec.Tokens, tokenRecord{Name: label, Hash: string(hash), Created: time.Now(), Expires: expires})
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
		tokens = append(tokens, tokenRecord{Name: t.Name, Created: t.Created, Expires: t.Expires})
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

// UserForBearer resolves a Bearer PAT value to its username. Verified tokens
// are cached so bcrypt runs once per token per process, not per request.
// linear scan over users' tokens on first use; fine for a handful of users
func (a *Auth) UserForBearer(token string) (string, bool) {
	if !strings.HasPrefix(token, "hmd_") {
		return "", false
	}

	a.mu.RLock()
	if cached, ok := a.tokenCache[token]; ok {
		a.mu.RUnlock()
		// Expiry is wall-clock, so the cache can't answer it once and for
		// all — check on every use.
		if !cached.expires.IsZero() && time.Now().After(cached.expires) {
			return "", false
		}
		return cached.user, true
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
				return "", false
			}
			a.mu.Lock()
			a.tokenCache[token] = cachedToken{user: c.user, expires: c.tok.Expires}
			a.mu.Unlock()
			return c.user, true
		}
	}
	return "", false
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
	a.sessions[token] = name
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
func (a *Auth) EnsureOIDCUser(name, gitAuthor string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	rec, exists := a.users[name]
	if exists && (rec.GitAuthor != "" || gitAuthor == "") {
		return nil
	}
	rec.GitAuthor = gitAuthor
	a.users[name] = rec
	if !exists {
		slog.Info("provisioned OIDC user", "user", name)
	}
	return a.save()
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

func (a *Auth) UserFor(token string) (username string, ok bool) {
	a.mu.RLock()
	username, ok = a.sessions[token]
	a.mu.RUnlock()
	return
}

// requiredScope reports which scope r needs. /settings and /admin (any
// method) need "settings". Everything else follows HTTP method: a
// body-carrying method needs "write", a safe one needs "read". /mcp bundles
// read and write tools behind a single JSON-RPC endpoint with no per-tool
// scoping yet, so it checked separately, below, against both.
//
// method-based, not route-based, so a handful of read-only-looking
// POSTs (e.g. /search) don't exist — check with the route table in
// handlers.go if a new write-shaped GET or read-shaped POST is ever added.
func requiredScope(r *http.Request) scope {
	if r.URL.Path == "/settings" || strings.HasPrefix(r.URL.Path, "/settings/") ||
		r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/") {
		return scopeSettings
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return scopeRead
	}
	return scopeWrite
}

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow /login, the OIDC flow and /static/ without authentication
		if r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/auth/oidc/") || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}

		// The public garden namespace, only when enabled — disabled means
		// /garden/ stays behind the login redirect like everything else
		if a.garden && (r.URL.Path == "/garden" || strings.HasPrefix(r.URL.Path, "/garden/")) {
			next.ServeHTTP(w, r)
			return
		}

		isAPI := strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/mcp"

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
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		}

		var user string
		var authed bool

		// Bearer PAT: an explicit credential, so a bad one is denied rather
		// than falling through to the cookie check.
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			user, authed = a.UserForBearer(strings.TrimPrefix(h, "Bearer "))
		} else if cookie, err := r.Cookie("hmd_session"); err == nil && cookie.Value != "" {
			user, authed = a.UserFor(cookie.Value)
		}
		if !authed {
			deny(http.StatusUnauthorized, "unauthorized")
			return
		}

		prefs := a.prefs(user)
		need := "mcp (read+write)"
		authorized := prefs.hasScope(scopeRead) && prefs.hasScope(scopeWrite)
		if r.URL.Path != "/mcp" {
			need = string(requiredScope(r))
			authorized = prefs.hasScope(requiredScope(r))
		}
		if !authorized {
			deny(http.StatusForbidden, need)
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, user)))
	})
}
