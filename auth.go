package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

type Auth struct {
	usersFile string
	users     map[string]string // username -> bcrypt hash
	sessions  map[string]string // token -> username
	mu        sync.RWMutex
}

func OpenAuth(cfg Config) (*Auth, error) {
	// Create app dir if needed
	err := os.MkdirAll(cfg.AppDir, 0755)
	if err != nil {
		return nil, fmt.Errorf("creating app dir: %w", err)
	}

	usersFile := filepath.Join(cfg.AppDir, "users.json")
	auth := &Auth{
		usersFile: usersFile,
		users:     make(map[string]string),
		sessions:  make(map[string]string),
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
		} else {
			// No bootstrap, empty users
			log.Printf("Warning: no users.json and no HMD_ADMIN_USER/HMD_ADMIN_PASSWORD set. Run: hmd adduser <name> to create users")
		}
	} else {
		return nil, fmt.Errorf("reading users file: %w", err)
	}

	return auth, nil
}

func (a *Auth) AddUser(name, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	a.mu.Lock()
	a.users[name] = string(hash)
	a.mu.Unlock()

	// Write file atomically
	data, err := json.MarshalIndent(a.users, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling users: %w", err)
	}

	tmpFile := a.usersFile + ".tmp"
	err = os.WriteFile(tmpFile, data, 0644)
	if err != nil {
		return fmt.Errorf("writing tmp file: %w", err)
	}

	err = os.Rename(tmpFile, a.usersFile)
	if err != nil {
		return fmt.Errorf("renaming tmp file: %w", err)
	}

	return nil
}

func (a *Auth) Login(name, password string) (token string, ok bool) {
	a.mu.RLock()
	hash, exists := a.users[name]
	a.mu.RUnlock()

	if !exists {
		return "", false
	}

	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
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
	a.mu.Unlock()

	return token, true
}

func (a *Auth) Logout(token string) {
	a.mu.Lock()
	delete(a.sessions, token)
	a.mu.Unlock()
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
