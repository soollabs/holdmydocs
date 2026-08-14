package web

import (
	"hmd/internal/store"
	"strings"
)

type Store = store.Store
type CommitInfo = store.CommitInfo
type CommitDetail = store.CommitDetail

const (
	defaultIndexPage = store.DefaultIndexPage
	defaultHomeMD    = store.DefaultHomeMD
	rootReadmeMD     = store.RootReadmeMD
	defaultHelpMD    = store.DefaultHelpMD
)

var (
	ErrConflict             = store.ErrConflict
	errInvalidNamespaceName = store.ErrInvalidNamespaceName
)

func storeOptions(cfg Config) store.Options {
	return store.Options{
		RepoDir:       cfg.RepoDir,
		DefaultBranch: cfg.DefaultBranch,
		Git: store.GitOptions{
			RemoteURL: cfg.Git.RemoteURL,
			User:      cfg.Git.User,
			Token:     cfg.Git.Token,
		},
	}
}

func OpenStore(cfg Config) (*Store, error) { return store.Open(storeOptions(cfg)) }

func HelpDrifted(s *Store) bool {
	raw, _, err := s.Read(".help.md")
	return err == nil && ParsePage("help", raw).Body != strings.TrimRight(defaultHelpMD, "\n")
}

func hasNamespace(dir string) bool { return store.HasNamespace(dir) }
