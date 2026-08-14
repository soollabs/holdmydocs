package export

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"hmd/internal/store"
)

const (
	maxFiles = 10_000
	maxBytes = 100 << 20
)

func PagePath(rest string) string { return path.Join(rest, "index.html") }

func RelativePath(from, to string) string {
	href, err := filepath.Rel(filepath.FromSlash(path.Dir(from)), filepath.FromSlash(to))
	if err != nil {
		return to
	}
	return filepath.ToSlash(href)
}

func CopyAssets(source fs.FS, root, outDir string) error {
	return fs.WalkDir(source, root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		dest := filepath.Join(outDir, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	})
}

func CopyAttachments(content *store.Store, outDir, namespace string, files *int, bytes *int64) error {
	paths, err := content.ListAttachments()
	if err != nil {
		return err
	}
	prefix := "attachments/" + namespace + "/"
	for _, sourcePath := range paths {
		if !strings.HasPrefix(sourcePath, prefix) {
			continue
		}
		file, _, err := content.OpenAttachment(sourcePath)
		if err != nil {
			return fmt.Errorf("opening attachment %q: %w", sourcePath, err)
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return fmt.Errorf("stating attachment %q: %w", sourcePath, err)
		}
		*files++
		*bytes += info.Size()
		if *files > maxFiles || *bytes > maxBytes {
			_ = file.Close()
			return fmt.Errorf("export exceeds %d files or %d bytes", maxFiles, maxBytes)
		}
		target := filepath.Join(outDir, "attachments", filepath.FromSlash(strings.TrimPrefix(sourcePath, prefix)))
		if err := CopyFile(file, target); err != nil {
			_ = file.Close()
			return fmt.Errorf("copying attachment %q: %w", sourcePath, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("closing attachment %q: %w", sourcePath, err)
		}
	}
	return nil
}

func Zip(w io.Writer, dir string) (err error) {
	zw := zip.NewWriter(w)
	defer func() {
		if closeErr := zw.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return filepath.Walk(dir, func(name string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in export: %s", name)
		}
		rel, err := filepath.Rel(dir, name)
		if err != nil {
			return err
		}
		dest, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		source, err := os.Open(name)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(dest, source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func CopyFile(in *os.File, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
