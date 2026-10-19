package auth

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrLastAdmin         = errors.New("at least one user must keep the admin scope")
	ErrUnknownUser       = errors.New("unknown user")
	ErrSSOUser           = errors.New("single sign-on users have no password")
	ErrWrongPassword     = errors.New("current password is incorrect")
	ErrInvalidPassword   = errors.New("password must be 12 to 72 bytes long, differ from the user name and not be a placeholder")
	ErrPasswordThrottled = errors.New("too many password attempts; try again later")
	ErrSamePassword      = errors.New("new password must differ from the current password")
)

// LockAppDir excludes other servers and offline account recovery commands.
func LockAppDir(appDir string) (func() error, error) {
	lock := flock.New(filepath.Join(appDir, "users.json.lock"), flock.SetPermissions(0600))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("users.json is in use by a running HMD server; stop it first")
	}
	return lock.Unlock, nil
}

func (a *Auth) adminCountLocked(except string) int {
	count := 0
	for name, rec := range a.users {
		if name != except && rec.hasScope(scopeAdmin) {
			count++
		}
	}
	return count
}

func (a *Auth) UsesPassword(name string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.users[name].Hash != ""
}

func (a *Auth) DeleteUser(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.users[name]
	if !ok {
		return ErrUnknownUser
	}
	if rec.hasScope(scopeAdmin) && a.adminCountLocked(name) == 0 {
		return ErrLastAdmin
	}
	delete(a.users, name)
	for digest, token := range a.tokenCache {
		if token.user == name {
			delete(a.tokenCache, digest)
		}
	}
	a.revokeUserSessionsLocked(name)
	if err := a.save(); err != nil {
		return err
	}
	if err := a.saveSessions(); err != nil {
		return err
	}
	slog.Info("user deleted", "user", name)
	return nil
}

func (a *Auth) SetPassword(name, password string) error {
	return a.setPassword(name, "", password, "", false)
}

func (a *Auth) ChangePassword(name, current, next, keepSession string) error {
	return a.setPassword(name, current, next, keepSession, true)
}

func (a *Auth) setPassword(name, current, next, keepSession string, verify bool) error {
	// Bound both password checks and password hashing.
	a.bcryptSem <- struct{}{}
	defer func() { <-a.bcryptSem }()
	a.mu.Lock()
	rec, ok := a.users[name]
	key := "password\x00" + name
	attempt := a.loginAttempts[key]
	if verify && time.Now().Before(attempt.until) {
		a.mu.Unlock()
		return ErrPasswordThrottled
	}
	a.mu.Unlock()
	if !ok {
		return ErrUnknownUser
	}
	if rec.OIDC != nil {
		return ErrSSOUser
	}
	if verify {
		if bcrypt.CompareHashAndPassword([]byte(rec.Hash), []byte(current)) != nil {
			a.mu.Lock()
			a.recordFailedAttemptLocked(key, a.loginAttempts[key])
			a.mu.Unlock()
			return ErrWrongPassword
		}
		if next == current {
			return ErrSamePassword
		}
	}
	if !validCredentials(name, next) {
		return ErrInvalidPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	latest, exists := a.users[name]
	if !exists {
		return ErrUnknownUser
	}
	// Do not overwrite a concurrent password reset, deletion/recreation or identity change.
	if latest.Hash != rec.Hash || latest.OIDC != nil {
		return errors.New("account changed; try again")
	}
	latest.Hash = string(hash)
	a.users[name] = latest
	for token, session := range a.sessions {
		if session.User == name && (!verify || token != keepSession) {
			delete(a.sessions, token)
		}
	}
	delete(a.loginAttempts, key)
	if err := a.save(); err != nil {
		return err
	}
	return a.saveSessions()
}
