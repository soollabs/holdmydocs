package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxStoredPageBytes = 1<<20 + 64<<10

func repositoryPathParts(path string) ([]string, error) {
	if path == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" || strings.Contains(path, "\\") {
		return nil, fmt.Errorf("invalid repository path %q", path)
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || part == ".git" {
			return nil, fmt.Errorf("invalid repository path %q", path)
		}
	}
	return parts, nil
}

func (s *Store) readRepositoryFile(path string) ([]byte, error) {
	f, err := s.openRepositoryFile(path, os.O_RDONLY, 0, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if strings.HasSuffix(path, ".md") {
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if info.Size() > maxStoredPageBytes {
			return nil, fmt.Errorf("page exceeds %d bytes", maxStoredPageBytes)
		}
	}
	return io.ReadAll(f)
}

func (s *Store) writeRepositoryFile(path string, content []byte) error {
	f, err := s.openRepositoryFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644, true)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(content)
	return err
}
