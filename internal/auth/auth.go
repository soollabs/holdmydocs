package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

type tokenRecord struct {
	Name       string    `json:"name"`
	Digest     string    `json:"digest"`
	Created    time.Time `json:"created"`
	Expires    time.Time `json:"expires,omitzero"`
	Scopes     []string  `json:"scopes"`
	Namespaces []string  `json:"namespaces,omitempty"`
}

func (t tokenRecord) expired() bool {
	return !t.Expires.IsZero() && time.Now().After(t.Expires)
}

func (t tokenRecord) Expired() bool { return t.expired() }

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
	return slices.Contains(p.Namespaces, namespace)
}

func (p tokenPrincipal) AllowsSlug(slug string) bool {
	namespace, _, _ := strings.Cut(slug, "/")
	return p.AllowsNamespace(namespace)
}

type scope string

const (
	scopeRead     scope = "read"
	scopeWrite    scope = "write"
	scopeSettings scope = "settings"
)

var allScopes = []scope{scopeRead, scopeWrite, scopeSettings}

type userRecord struct {
	Hash      string        `json:"hash"`
	OIDC      *oidcIdentity `json:"oidc,omitempty"`
	GitAuthor string        `json:"git_author,omitempty"`
	Tokens    []tokenRecord `json:"tokens,omitempty"`
	Palette   string        `json:"palette,omitempty"`
	FontUI    string        `json:"font_ui,omitempty"`
	FontMono  string        `json:"font_mono,omitempty"`
	Skin      string        `json:"skin,omitempty"`
	Scopes    []string      `json:"scopes"`
}

type oidcIdentity struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

type UserRecord = userRecord
type TokenRecord = tokenRecord
type TokenPrincipal = tokenPrincipal
type Scope = scope
type OIDCIdentity = oidcIdentity

const (
	ScopeRead     = scopeRead
	ScopeWrite    = scopeWrite
	ScopeSettings = scopeSettings
)

var AllScopes = allScopes

func (u userRecord) hasScope(s scope) bool {
	if slices.Contains(u.Scopes, string(scopeSettings)) {
		return true
	}
	return slices.Contains(u.Scopes, string(s))
}

func (u userRecord) HasScope(s Scope) bool { return u.hasScope(s) }

func (a *Auth) prefs(name string) userRecord {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.users[name]
}

func (a *Auth) Prefs(name string) UserRecord { return a.prefs(name) }

func TokenPrincipalFromContext(ctx context.Context) (TokenPrincipal, bool) {
	return tokenPrincipalFromContext(ctx)
}

func TokenAllowsNamespace(ctx context.Context, namespace string) bool {
	return tokenAllowsNamespace(ctx, namespace)
}

func TokenAllowsSlug(ctx context.Context, slug string) bool { return tokenAllowsSlug(ctx, slug) }
func ValidUsername(name string) bool                        { return validUsername(name) }

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

// SetScopes restricts name to exactly the given scopes ("read", "write", "settings").
func (a *Auth) SetScopes(name string, scopes []string) error {
	if len(scopes) == 0 {
		return fmt.Errorf("user scopes cannot be empty")
	}
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
	if err := a.save(); err != nil {
		return err
	}
	a.revokeUserSessionsLocked(name)
	return a.saveSessions()
}

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

const sessionTTL = 30 * 24 * time.Hour

type sessionRecord struct {
	User    string    `json:"user"`
	Expires time.Time `json:"expires"`
	CSRF    string    `json:"csrf"`
}

func (s sessionRecord) expired() bool {
	return time.Now().After(s.Expires)
}

type Auth struct {
	usersFile     string
	sessionsFile  string
	users         map[string]userRecord    // username -> record
	sessions      map[string]sessionRecord // token -> session
	tokenCache    map[string]cachedToken   // PAT digest -> user + expiry
	loginAttempts map[string]loginAttempt
	bcryptSem     chan struct{}
	mu            sync.RWMutex
}

type Options struct {
	AppDir    string
	AdminUser string
	AdminPass string
}

type loginAttempt struct {
	failures int
	until    time.Time
}

const (
	minPasswordChars = 12
	maxPasswordBytes = 72
	maxTokenLabelLen = 64
	maxTokensPerUser = 64
	maxLoginAttempts = 1024
)

var dummyPasswordHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

func normaliseUserRecord(name string, rec userRecord) (userRecord, error) {
	if len(rec.Scopes) == 0 {
		return userRecord{}, fmt.Errorf("user %q scopes cannot be empty", name)
	}
	scopes, err := normaliseTokenScopes(rec.Scopes)
	if err != nil {
		return userRecord{}, fmt.Errorf("user %q scopes: %w", name, err)
	}
	rec.Scopes = scopes
	if len(rec.Tokens) > maxTokensPerUser {
		return userRecord{}, fmt.Errorf("user %q has too many tokens", name)
	}
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
		if !validTokenDigest(token.Digest) {
			return userRecord{}, fmt.Errorf("user %q token %q has invalid digest", name, token.Name)
		}
		tokenScopes, err := normaliseTokenScopes(token.Scopes)
		if err != nil {
			return userRecord{}, fmt.Errorf("user %q token %q scopes: %w", name, token.Name, err)
		}
		if len(tokenScopes) == 0 {
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

func Open(cfg Options) (*Auth, error) {
	if info, err := os.Lstat(cfg.AppDir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("app dir must be a directory, not a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("checking app dir: %w", err)
	}
	err := os.MkdirAll(cfg.AppDir, 0700)
	if err != nil {
		return nil, fmt.Errorf("creating app dir: %w", err)
	}
	if err := os.Chmod(cfg.AppDir, 0700); err != nil {
		return nil, fmt.Errorf("securing app dir: %w", err)
	}

	usersFile := filepath.Join(cfg.AppDir, "users.json")
	auth := &Auth{
		usersFile:     usersFile,
		sessionsFile:  filepath.Join(cfg.AppDir, "sessions.json"),
		users:         make(map[string]userRecord),
		sessions:      make(map[string]sessionRecord),
		tokenCache:    make(map[string]cachedToken),
		loginAttempts: make(map[string]loginAttempt),
		bcryptSem:     make(chan struct{}, 4),
	}

	if err := tightenRegularFile(usersFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("checking users file: %w", err)
	}
	data, err := os.ReadFile(usersFile)
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&auth.users)
		if err != nil {
			return nil, fmt.Errorf("unmarshalling users: %w", err)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return nil, fmt.Errorf("unmarshalling users: trailing data")
		}
		for name, rec := range auth.users {
			rec, err = normaliseUserRecord(name, rec)
			if err != nil {
				return nil, err
			}
			auth.users[name] = rec
			for _, token := range rec.Tokens {
				auth.tokenCache[token.Digest] = cachedToken{user: name, scopes: append([]string(nil), token.Scopes...), namespaces: append([]string(nil), token.Namespaces...), expires: token.Expires}
			}
		}
	} else if os.IsNotExist(err) {
		if cfg.AdminUser != "" && cfg.AdminPass != "" {
			if !validBootstrapCredentials(cfg.AdminUser, cfg.AdminPass) {
				return nil, fmt.Errorf("bootstrap credentials must use a valid username, a strong non-placeholder password, and different username and password")
			}

			err = auth.AddUser(cfg.AdminUser, cfg.AdminPass)
			if err != nil {
				return nil, fmt.Errorf("bootstrapping admin: %w", err)
			}
			slog.Info("bootstrapped admin user", "user", cfg.AdminUser)
		}
	} else {
		return nil, fmt.Errorf("reading users file: %w", err)
	}

	// Session persistence is best-effort; missing or corrupt data forces users to log in again.
	if err := tightenRegularFile(auth.sessionsFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("checking sessions file: %w", err)
	}
	if data, err := os.ReadFile(auth.sessionsFile); err == nil {
		_ = json.Unmarshal(data, &auth.sessions)
		if auth.purgeExpiredSessionsLocked() {
			if err := auth.saveSessions(); err != nil {
				slog.Warn("purging expired sessions", "error", err)
			}
		}
	}

	return auth, nil
}

func tightenRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !ownedByCurrentUser(info) {
		return fmt.Errorf("must be an owned regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return os.Chmod(path, 0600)
	}
	return nil
}

func (a *Auth) AddUser(name, password string) error {
	return a.addUser(name, password, scopeNames(allScopes))
}

// AddUserWithScopes creates a user with its final policy in one persisted update.
func (a *Auth) AddUserWithScopes(name, password string, scopes []string) error {
	normalised, err := normaliseTokenScopes(scopes)
	if err != nil {
		return err
	}
	if len(normalised) == 0 {
		return fmt.Errorf("user scopes cannot be empty")
	}
	return a.addUser(name, password, normalised)
}

func (a *Auth) addUser(name, password string, scopes []string) error {
	if !validUsername(name) {
		return fmt.Errorf("invalid username")
	}
	if !validPassword(password) {
		return fmt.Errorf("password must be at least 12 characters, at most 72 bytes, and contain no control characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	a.mu.Lock()
	rec := a.users[name]
	rec.Hash = string(hash)
	rec.Scopes = append([]string(nil), scopes...)
	a.users[name] = rec
	err = a.save()
	if err == nil {
		a.revokeUserSessionsLocked(name)
		err = a.saveSessions()
	}
	a.mu.Unlock()

	if err == nil {
		slog.Info("user added", "user", name)
	}
	return err
}

func validUsername(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || len(name) > 64 || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func validPassword(password string) bool {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < minPasswordChars || len(password) > maxPasswordBytes {
		return false
	}
	for _, r := range password {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validBootstrapCredentials(name, password string) bool {
	if !validUsername(name) || !validPassword(password) || strings.EqualFold(name, password) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(password)) {
	case "change-me", "your-password", "your_password", "password":
		return false
	}
	return true
}

func validTokenLabel(label string) bool {
	return label != "" && label == strings.TrimSpace(label) && utf8.ValidString(label) && utf8.RuneCountInString(label) <= maxTokenLabelLen && !strings.ContainsFunc(label, unicode.IsControl)
}

// UserExists reports whether name has a user record.
func (a *Auth) UserExists(name string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.users[name]
	return ok
}

func (a *Auth) HasUsers() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.users) != 0
}

// UserSummary is one row in the settings-page user list.
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

func validNamespaceName(name string) bool {
	return name != "" && name != "_" && name != "attachments" &&
		!strings.ContainsAny(name, `/\`) && !strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "_")
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

func scopeNames(scopes []scope) []string {
	names := make([]string, len(scopes))
	for i, scope := range scopes {
		names[i] = string(scope)
	}
	return names
}

func effectiveTokenScopes(userScopes, tokenScopes []string) []string {
	user := userRecord{Scopes: userScopes}
	result := make([]string, 0, len(tokenScopes))
	for _, name := range tokenScopes {
		if user.hasScope(scope(name)) {
			result = append(result, name)
		}
	}
	return result
}

func EffectiveTokenScopes(userScopes, tokenScopes []string) []string {
	return effectiveTokenScopes(userScopes, tokenScopes)
}

// AddToken mints a personal access token for name, labelled label, expiring at expires (zero = never).
func (a *Auth) AddToken(name, label string, expires time.Time, scopes, namespaces []string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.users[name]
	if !ok {
		return "", fmt.Errorf("unknown user %q", name)
	}
	if !validTokenLabel(label) {
		return "", fmt.Errorf("invalid token name")
	}
	for _, t := range rec.Tokens {
		if t.Name == label {
			return "", fmt.Errorf("a token named %q already exists", label)
		}
	}
	if len(rec.Tokens) >= maxTokensPerUser {
		return "", fmt.Errorf("token limit reached")
	}
	normalisedScopes, err := normaliseTokenScopes(scopes)
	if err != nil {
		return "", err
	}
	if len(normalisedScopes) == 0 {
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

	digest := tokenDigest(token)
	rec.Tokens = append(rec.Tokens, tokenRecord{
		Name:       label,
		Digest:     digest,
		Created:    time.Now(),
		Expires:    expires,
		Scopes:     normalisedScopes,
		Namespaces: normalisedNamespaces,
	})
	a.users[name] = rec
	if err := a.save(); err != nil {
		return "", err
	}
	a.tokenCache[digest] = cachedToken{user: name, scopes: append([]string(nil), normalisedScopes...), namespaces: append([]string(nil), normalisedNamespaces...), expires: expires}
	return token, nil
}

// TokensFor returns name's stored tokens (metadata only — digests stay out of templates).
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

// RemoveToken revokes name's token labelled label and drops the user's digest index entries.
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
	for _, token := range kept {
		a.tokenCache[token.Digest] = cachedToken{user: name, scopes: append([]string(nil), token.Scopes...), namespaces: append([]string(nil), token.Namespaces...), expires: token.Expires}
	}
	return a.save()
}

// UserForBearer resolves a fixed-format Bearer PAT through its SHA-256 digest.
func (a *Auth) UserForBearer(token string) (tokenPrincipal, bool) {
	if !validBearerToken(token) {
		return tokenPrincipal{}, false
	}
	digest := tokenDigest(token)

	a.mu.RLock()
	if cached, ok := a.tokenCache[digest]; ok {
		a.mu.RUnlock()
		// Cached token expiry must be checked on every use.
		if !cached.expires.IsZero() && time.Now().After(cached.expires) {
			return tokenPrincipal{}, false
		}
		return tokenPrincipal{
			User:       cached.user,
			Scopes:     append([]string(nil), cached.scopes...),
			Namespaces: append([]string(nil), cached.namespaces...),
		}, true
	}
	a.mu.RUnlock()
	return tokenPrincipal{}, false
}

// UserForBearerLimited throttles invalid credentials by source address.
func (a *Auth) UserForBearerLimited(remote, token string) (tokenPrincipal, bool) {
	key := "bearer\x00" + loginKey(remote, "")
	a.mu.Lock()
	attempt, limited := a.loginAttempts[key]
	if limited && time.Now().Before(attempt.until) {
		a.mu.Unlock()
		return tokenPrincipal{}, false
	}
	a.mu.Unlock()

	principal, ok := a.UserForBearer(token)
	if ok {
		a.mu.Lock()
		delete(a.loginAttempts, key)
		a.mu.Unlock()
		return principal, true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recordFailedAttemptLocked(key, attempt)
	return tokenPrincipal{}, false
}

func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func validTokenDigest(digest string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func validBearerToken(token string) bool {
	if len(token) != len("hmd_")+sha256.Size*2 || !strings.HasPrefix(token, "hmd_") {
		return false
	}
	_, err := hex.DecodeString(token[len("hmd_"):])
	return err == nil
}

// AuthorFor returns the user's stored git author string, or "" if unset.
func (a *Auth) AuthorFor(name string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.users[name].GitAuthor
}

// SetAuthor stores a per-user git author ("Name <email>", or "" to clear).
func (a *Auth) SetAuthor(name, author string) error {
	if err := validateGitAuthor(author); err != nil {
		return err
	}
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

func validateGitAuthor(author string) error {
	if !utf8.ValidString(author) || utf8.RuneCountInString(author) > 256 || strings.ContainsAny(author, "\r\n") {
		return fmt.Errorf("git author must be valid UTF-8, at most 256 characters, and one line")
	}
	return nil
}

func (a *Auth) save() error {
	data, err := json.MarshalIndent(a.users, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling users: %w", err)
	}

	tmpFile := a.usersFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return fmt.Errorf("writing tmp file: %w", err)
	}
	if err := os.Rename(tmpFile, a.usersFile); err != nil {
		return fmt.Errorf("renaming tmp file: %w", err)
	}
	return nil
}

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
	return a.login(name, password)
}

func (a *Auth) LoginLimited(remote, name, password string) (token string, ok bool) {
	key := loginKey(remote, name)
	a.mu.Lock()
	attempt, limited := a.loginAttempts[key]
	if limited && time.Now().Before(attempt.until) {
		a.mu.Unlock()
		return "", false
	}
	a.mu.Unlock()

	token, ok = a.login(name, password)
	a.mu.Lock()
	defer a.mu.Unlock()
	if ok {
		delete(a.loginAttempts, key)
		return token, true
	}
	a.recordFailedAttemptLocked(key, attempt)
	return "", false
}

func (a *Auth) recordFailedAttemptLocked(key string, attempt loginAttempt) {
	if len(a.loginAttempts) >= maxLoginAttempts {
		for k, v := range a.loginAttempts {
			if time.Now().After(v.until) {
				delete(a.loginAttempts, k)
				break
			}
		}
		if len(a.loginAttempts) >= maxLoginAttempts {
			for k := range a.loginAttempts {
				delete(a.loginAttempts, k)
				break
			}
		}
	}
	attempt.failures++
	delay := 250 * time.Millisecond << min(attempt.failures-1, 5)
	attempt.until = time.Now().Add(delay)
	a.loginAttempts[key] = attempt
}

func loginKey(remote, name string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	return strings.ToLower(host) + "\x00" + strings.ToLower(name)
}

func (a *Auth) login(name, password string) (token string, ok bool) {
	a.mu.RLock()
	rec, exists := a.users[name]
	a.mu.RUnlock()

	// SSO-provisioned users have no password login; use the dummy hash for timing consistency.
	if !validPassword(password) {
		return "", false
	}
	hash := dummyPasswordHash
	if exists && rec.Hash != "" {
		hash = []byte(rec.Hash)
	}
	a.bcryptSem <- struct{}{}
	err := bcrypt.CompareHashAndPassword(hash, []byte(password))
	<-a.bcryptSem
	if !exists || rec.Hash == "" || err != nil {
		return "", false
	}

	return a.newSession(name)
}

func (a *Auth) newSession(name string) (token string, ok bool) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", false
	}
	token = hex.EncodeToString(b)
	if _, err := rand.Read(b); err != nil {
		return "", false
	}
	csrf := hex.EncodeToString(b)

	a.mu.Lock()
	a.revokeUserSessionsLocked(name)
	a.sessions[token] = sessionRecord{User: name, Expires: time.Now().Add(sessionTTL), CSRF: csrf}
	err := a.saveSessions()
	a.mu.Unlock()
	if err != nil {
		slog.Warn("saving sessions", "error", err)
	}

	return token, true
}

func (a *Auth) NewSession(name string) (string, bool) { return a.newSession(name) }

func UserFromContext(ctx context.Context) string {
	user, _ := ctx.Value(ctxUserKey{}).(string)
	return user
}

// CSRFToken returns the synchroniser token bound to a live session.
func (a *Auth) CSRFToken(session string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if rec, ok := a.sessions[session]; ok && !rec.expired() {
		return rec.CSRF
	}
	return ""
}

// EnsureOIDCUser provisions an SSO user and preserves an existing Git author.
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

func (a *Auth) revokeUserSessionsLocked(name string) {
	for token, session := range a.sessions {
		if session.User == name {
			delete(a.sessions, token)
		}
	}
}

func (a *Auth) purgeExpiredSessionsLocked() bool {
	purged := false
	for token, session := range a.sessions {
		if session.expired() {
			delete(a.sessions, token)
			purged = true
		}
	}
	return purged
}

// UserFor resolves a session token to its username.
func (a *Auth) UserFor(token string) (username string, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, exists := a.sessions[token]
	if !exists {
		return "", false
	}
	if rec.expired() {
		delete(a.sessions, token)
		if err := a.saveSessions(); err != nil {
			slog.Warn("purging expired session", "error", err)
		}
		return "", false
	}
	return rec.User, true
}

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

func anonymousEligible(path string, q url.Values) bool {
	if rest, ok := strings.CutPrefix(path, "/_/attachments/"); ok && strings.Contains(rest, "/") {
		return true
	}
	p := strings.TrimPrefix(path, "/")
	if p == "" || p == "_" || strings.HasPrefix(p, "_/") {
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
	return slices.Contains([]string{"/_/api/preview", "/_/search", "/_/search/attachments", "/_/api/search", "/_/api/search/attachments", "/_/api/health", "/_/tags", "/_/health-report", "/_/hidden", "/_/new", "/_/namespaces"}, path)
}

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_/live" || r.URL.Path == "/_/ready" || r.URL.Path == "/_/login" || strings.HasPrefix(r.URL.Path, "/_/auth/oidc/") || strings.HasPrefix(r.URL.Path, "/_/static/") || strings.HasPrefix(r.URL.Path, "/_/api/attachment-uploads/") {
			next.ServeHTTP(w, r)
			return
		}

		isAPI := strings.HasPrefix(r.URL.Path, "/_/api/") || r.URL.Path == "/_/mcp"

		// API auth failures return JSON instead of redirecting to the login page.
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

		// An invalid Bearer credential must not fall through to cookie authentication.
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			rawPrincipal, authed = a.UserForBearerLimited(r.RemoteAddr, strings.TrimPrefix(h, "Bearer "))
			if authed {
				user = rawPrincipal.User
				bearer = true
			}
		} else if cookie, err := r.Cookie("hmd_session"); err == nil && cookie.Value != "" {
			user, authed = a.UserFor(cookie.Value)
		}
		if !authed {
			if (r.Method == http.MethodGet || r.Method == http.MethodHead) && anonymousEligible(r.URL.Path, r.URL.Query()) {
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
				// Resolve effective token policy for MCP tool-level authorization.
				principal.Scopes = effectiveTokenScopes(prefs.Scopes, rawPrincipal.Scopes)
			}
			ctx = context.WithValue(ctx, ctxTokenPrincipalKey{}, principal)
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
