package api

import (
	"context"
	"errors"
	"log/slog"

	"hmd/internal/auth"
	"hmd/internal/config"
)

func (a *API) DeleteUser(ctx context.Context, name string) error {
	if err := a.RequireScope(ctx, ScopeAdmin); err != nil {
		return err
	}
	user, _ := Username(ctx)
	if name == user {
		return InvalidInput("you cannot delete your own account; ask another administrator", nil)
	}
	if err := a.auth.DeleteUser(name); err != nil {
		return userMutationError(err)
	}
	if a.userRevoker != nil {
		if err := a.userRevoker.RevokeUserGrants(name); err != nil {
			slog.Error("revoking deleted user's OAuth connections", "user", name, "error", err)
			return Unavailable("user deleted but revoking their OAuth connections failed", err)
		}
	}
	return nil
}

func (a *API) ResetUserPassword(ctx context.Context, name, password string) error {
	if err := a.RequireScope(ctx, ScopeAdmin); err != nil {
		return err
	}
	return userMutationError(a.auth.SetPassword(name, password))
}

func (a *API) ChangeOwnPassword(ctx context.Context, current, next, keepSession string) error {
	if err := a.RequireOwnSettings(ctx); err != nil {
		return err
	}
	if _, ok := auth.TokenPrincipalFromContext(ctx); ok {
		return Forbidden("changing a password needs a browser session")
	}
	user, _ := Username(ctx)
	return userMutationError(a.auth.ChangePassword(user, current, next, keepSession))
}

func userMutationError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, auth.ErrUnknownUser) {
		return NotFound("unknown user")
	}
	if errors.Is(err, auth.ErrPasswordThrottled) {
		return Unavailable(err.Error(), err)
	}
	if errors.Is(err, auth.ErrLastAdmin) || errors.Is(err, auth.ErrSSOUser) ||
		errors.Is(err, auth.ErrWrongPassword) || errors.Is(err, auth.ErrInvalidPassword) || errors.Is(err, auth.ErrSamePassword) {
		return InvalidInput(err.Error(), err)
	}
	return Unavailable("updating user failed", err)
}

// BootstrapEnvSet reports names only, never credential values.
func (a *API) BootstrapEnvSet() []string {
	cfg := a.Config()
	var names []string
	if cfg.AdminUser != "" {
		names = append(names, config.EnvName("server.admin.user"))
	}
	if cfg.AdminPass != "" {
		names = append(names, config.EnvName("server.admin.password"))
	}
	return names
}
