//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package app

import (
	"fmt"
	"os"
	"path/filepath"
)

func (s *Store) openRepositoryFile(path string, flags int, perm os.FileMode, createParents bool) (*os.File, error) {
	parts, err := repositoryPathParts(path)
	if err != nil {
		return nil, err
	}
	dir := s.dir
	for _, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) && createParents {
			err = os.Mkdir(dir, 0o755)
			info, err = os.Lstat(dir)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("unsafe repository path %q", path)
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, parts[len(parts)-1]), flags, perm)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("repository path %q is not a regular file", path)
	}
	return f, nil
}

func (s *Store) removeRepositoryFile(path string) error {
	parts, err := repositoryPathParts(path)
	if err != nil {
		return err
	}
	if _, err := s.openRepositoryFile(path, os.O_RDONLY, 0, false); err != nil {
		return err
	}
	return os.Remove(filepath.Join(append([]string{s.dir}, parts...)...))
}
