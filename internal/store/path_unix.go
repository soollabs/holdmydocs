//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package store

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func (s *Store) openRepositoryParent(parts []string, create bool) (int, error) {
	fd, err := unix.Open(s.dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil && create && openErr == unix.ENOENT {
			if err := unix.Mkdirat(fd, part, 0o755); err != nil && err != unix.EEXIST {
				_ = unix.Close(fd)
				return -1, err
			}
			next, openErr = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func (s *Store) openRepositoryFile(path string, flags int, perm os.FileMode, createParents bool) (*os.File, error) {
	parts, err := repositoryPathParts(path)
	if err != nil {
		return nil, err
	}
	parent, err := s.openRepositoryParent(parts, createParents)
	if err != nil {
		return nil, fmt.Errorf("opening repository path %q: %w", path, err)
	}
	defer func() { _ = unix.Close(parent) }()
	fd, err := unix.Openat(parent, parts[len(parts)-1], flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm))
	if err != nil {
		return nil, fmt.Errorf("opening repository path %q: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), filepath.Base(path))
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("stating repository path %q: %w", path, err)
		}
		return nil, fmt.Errorf("repository path %q is not a regular file", path)
	}
	return f, nil
}

func (s *Store) removeRepositoryFile(path string) error {
	parts, err := repositoryPathParts(path)
	if err != nil {
		return err
	}
	parent, err := s.openRepositoryParent(parts, false)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(parent) }()
	if err := unix.Unlinkat(parent, parts[len(parts)-1], 0); err != nil {
		return err
	}
	return nil
}
