package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// userRecord is a stored user. GitAuthor, when set, is that user's commit
// identity in "Name <email>" form and overrides the global default.
type userRecord struct {
	Hash      string `json:"hash"`
	GitAuthor string `json:"git_author,omitempty"`
}

type Auth struct {
	usersFile    string
	sessionsFile string
	users        map[string]userRecord // username -> record
	sessions     map[string]string     // token -> username
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
			slog.Warn("no users.json and no HMD_ADMIN_USER/HMD_ADMIN_PASSWORD set; run: hmd adduser <name>")
		}
	} else {
		return nil, fmt.Errorf("reading users file: %w", err)
	}

	// Sessions persisting is best-effort: a missing/corrupt file just means
	// everyone logs in again, so ignore errors rather than fail startup.
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

	if !exists {
		return "", false
	}

	err := bcrypt.CompareHashAndPassword([]byte(rec.Hash), []byte(password))
	if err != nil {
		return "", false
	}

	// Generate token
	b := make([]byte, 16)
	_, err = rand.Read(b)
	if err != nil {
		return "", false
	}
	token = hex.EncodeToString(b)

	// Store session
	a.mu.Lock()
	a.sessions[token] = name
	err = a.saveSessions()
	a.mu.Unlock()
	if err != nil {
		slog.Warn("saving sessions", "error", err)
	}

	return token, true
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

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow /login and /static/ without authentication
		if r.URL.Path == "/login" || (len(r.URL.Path) > 8 && r.URL.Path[:8] == "/static/") {
			next.ServeHTTP(w, r)
			return
		}

		// Check for valid session cookie
		cookie, err := r.Cookie("hmd_session")
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		// Verify token is valid
		_, ok := a.UserFor(cookie.Value)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		next.ServeHTTP(w, r)
	})
}
