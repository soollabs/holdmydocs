package web

import (
	"hmd/internal/auth"
	"hmd/internal/config"
	"hmd/internal/store"
)

// OpenStore and OpenAuth are test fixtures: they build the real persistence and
// identity stores from a browser-Config so package tests exercise the same
// dependencies the composition root wires.
func OpenStore(cfg config.Config) (*store.Store, error) {
	return store.Open(store.Options{
		RepoDir:       cfg.RepoDir,
		ReadOnly:      cfg.ReadOnly,
		DefaultBranch: cfg.DefaultBranch,
		Git: store.GitOptions{
			RemoteURL: cfg.Git.RemoteURL,
			User:      cfg.Git.User,
			Token:     cfg.Git.Token,
		},
	})
}

func OpenAuth(cfg config.Config) (*auth.Auth, error) {
	return auth.Open(auth.Options{AppDir: cfg.AppDir, AdminUser: cfg.AdminUser, AdminPass: cfg.AdminPass})
}
