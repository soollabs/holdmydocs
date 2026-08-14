package export

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"hmd/internal/store"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, fs.ErrClosed }

func TestExportFiles(t *testing.T) {
	if got := PagePath("guide/start"); got != "guide/start/index.html" {
		t.Fatalf("PagePath = %q", got)
	}
	if got := RelativePath("guide/start/index.html", "style.css"); got != "../../style.css" {
		t.Fatalf("RelativePath = %q", got)
	}

	out := t.TempDir()
	assets := fstest.MapFS{"static/style.css": &fstest.MapFile{Data: []byte("body{}")}}
	if err := CopyAssets(assets, "static", out); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(out, "style.css")); err != nil || string(body) != "body{}" {
		t.Fatalf("copied asset = %q, %v", body, err)
	}

	repo := t.TempDir()
	content, err := store.Open(store.Options{RepoDir: repo, Git: store.GitOptions{User: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := content.Save("attachments/notes/page/file.txt", []byte("attachment"), "seed", "test", "test@hmd.local"); err != nil {
		t.Fatal(err)
	}
	files, size := 0, int64(0)
	if err := CopyAttachments(content, out, "notes", &files, &size); err != nil {
		t.Fatal(err)
	}
	if files != 1 || size != int64(len("attachment")) {
		t.Fatalf("attachment budget = %d files, %d bytes", files, size)
	}

	var archive bytes.Buffer
	if err := Zip(&archive, out); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 2 {
		t.Fatalf("zip contains %d files, want 2", len(zr.File))
	}
	if err := Zip(failingWriter{}, out); err == nil {
		t.Fatal("Zip ignored a stream failure")
	}
}

func TestZipRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "linked")); err != nil {
		if os.IsPermission(err) || err == fs.ErrPermission {
			t.Skip("symlinks unavailable")
		}
		t.Fatal(err)
	}
	if err := Zip(&bytes.Buffer{}, dir); err == nil {
		t.Fatal("Zip followed a symlink")
	}
}
