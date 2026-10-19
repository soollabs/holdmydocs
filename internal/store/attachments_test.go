package store

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAttachmentLayoutPaths(t *testing.T) {
	tests := []struct {
		name, got, want string
	}{
		{"namespace layout", attachmentPath("notes/reports/q3", "chart.png"), "notes/.attachments/reports/q3/chart.png"},
		{"legacy layout", legacyAttachmentPath("notes/reports/q3", "chart.png"), "attachments/notes/reports/q3/chart.png"},
		{"sidecar beside namespace attachment", extractedAttachmentPath("notes/.attachments/reports/quarterly.pdf"), "notes/.attachments/reports/.hmd/extracted/quarterly.pdf.txt"},
		{"sidecar beside legacy attachment", extractedAttachmentPath("attachments/notes/reports/quarterly.pdf"), "attachments/notes/reports/.hmd/extracted/quarterly.pdf.txt"},
		{"no sidecar for a page", extractedAttachmentPath("notes/reports.md"), ""},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

func TestParseAttachmentPath(t *testing.T) {
	for _, path := range []string{"notes/.attachments/reports/quarterly.PDF", "attachments/notes/reports/quarterly.PDF"} {
		slug, filename, ok := ParseAttachmentPath(path)
		if !ok || slug != "notes/reports" || filename != "quarterly.PDF" {
			t.Errorf("ParseAttachmentPath(%q) = %q, %q, %v", path, slug, filename, ok)
		}
	}
	for _, path := range []string{
		"attachments/notes/report/.quarterly.pdf",
		"attachments/notes/quarterly.pdf",
		"notes/.attachments/quarterly.pdf",
		"notes/.attachments/report/.hmd/extracted/quarterly.pdf.txt",
		"attachments/notes/report/.hmd/extracted/quarterly.pdf.txt",
		"notes/.attachments/../report/quarterly.pdf",
		"notes/sub/.attachments/report/quarterly.pdf",
		"notes/report/quarterly.pdf",
		".attachments/report/quarterly.pdf",
	} {
		if _, _, ok := ParseAttachmentPath(path); ok {
			t.Errorf("ParseAttachmentPath(%q) accepted invalid path", path)
		}
	}
}

func FuzzAttachmentPath(f *testing.F) {
	for _, seed := range []string{"attachments/notes/page/file.pdf", "notes/.attachments/page/file.pdf", "attachments/../secret", "notes/.attachments/../secret", "attachments/notes/page/.hidden", "attachments/notes/é/report.txt"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, path string) {
		slug, filename, ok := ParseAttachmentPath(path)
		if ok && (strings.Contains(slug, "..") || strings.ContainsAny(filename, `/\`) || strings.HasPrefix(filename, ".")) {
			t.Fatalf("ParseAttachmentPath(%q) = %q, %q", path, slug, filename)
		}
	})
}

func newAttachmentStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(Options{RepoDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func seed(t *testing.T, s *Store, files map[string]string) {
	t.Helper()
	contents := make(map[string][]byte, len(files))
	for path, body := range files {
		contents[path] = []byte(body)
	}
	if _, err := s.SaveAll(contents, "seed", "test", "test@hmd.local"); err != nil {
		t.Fatal(err)
	}
}

func readAttachment(t *testing.T, s *Store, slug, filename string) string {
	t.Helper()
	attachment, ok := s.FindAttachment(slug, filename)
	if !ok {
		t.Fatalf("FindAttachment(%q, %q) not found", slug, filename)
	}
	body, err := s.readRepositoryFile(attachment.Path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestNewUploadsUseNamespaceLayoutAndShadowLegacy(t *testing.T) {
	s := newAttachmentStore(t)
	seed(t, s, map[string]string{"notes/page.md": "page", "attachments/notes/page/a.txt": "old", "attachments/notes/page/b.txt": "legacy only"})

	saved, err := s.SaveAttachment("notes/page", "a.txt", []byte("new"), []byte("text"), "upload", "test", "test@hmd.local")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Path != "notes/.attachments/page/a.txt" {
		t.Fatalf("saved at %q", saved.Path)
	}
	if got := readAttachment(t, s, "notes/page", "a.txt"); got != "new" {
		t.Errorf("namespace copy should win, got %q", got)
	}
	if got := readAttachment(t, s, "notes/page", "b.txt"); got != "legacy only" {
		t.Errorf("legacy fallback = %q", got)
	}
	list, err := s.ListAttachments()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Path != "attachments/notes/page/b.txt" || list[1].Path != "notes/.attachments/page/a.txt" {
		t.Errorf("ListAttachments = %+v", list)
	}
	if pages, _ := s.List(); len(pages) != 1 {
		t.Errorf("attachments leaked into pages: %v", pages)
	}
}

func TestMigrateLegacyAttachments(t *testing.T) {
	s := newAttachmentStore(t)
	seed(t, s, map[string]string{
		"notes/page.md":                                   "page",
		"attachments/notes/page/a.txt":                    "stale",
		"attachments/notes/page/b.txt":                    "moved",
		"attachments/notes/page/.hmd/extracted/b.txt.txt": "sidecar",
		"attachments/gone/page/c.txt":                     "orphan",
	})
	if _, err := s.SaveAttachment("notes/page", "a.txt", []byte("newer"), nil, "upload", "test", "test@hmd.local"); err != nil {
		t.Fatal(err)
	}
	if !s.LegacyAttachments() {
		t.Fatal("legacy attachments not detected")
	}
	want := []LegacyAttachmentNamespace{{Namespace: "gone", Files: 1}, {Namespace: "notes", Files: 2, Exists: true}}
	if namespaces, err := s.LegacyAttachmentStatus(); err != nil || !slices.Equal(namespaces, want) {
		t.Fatalf("LegacyAttachmentStatus = %+v, %v", namespaces, err)
	}

	moved, err := s.MigrateLegacyAttachments("migrate", "test", "test@hmd.local")
	if err != nil || moved != 2 {
		t.Fatalf("MigrateLegacyAttachments = %d, %v", moved, err)
	}
	if got := readAttachment(t, s, "notes/page", "a.txt"); got != "newer" {
		t.Errorf("newer upload overwritten: %q", got)
	}
	if got := readAttachment(t, s, "notes/page", "b.txt"); got != "moved" {
		t.Errorf("moved attachment = %q", got)
	}
	if sidecar, err := s.OpenExtractedAttachment("notes/.attachments/page/b.txt"); err != nil {
		t.Errorf("sidecar not moved: %v", err)
	} else {
		_ = sidecar.Close()
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "attachments", "notes")); !os.IsNotExist(err) {
		t.Errorf("legacy namespace folder left behind: %v", err)
	}
	if got := readAttachment(t, s, "gone/page", "c.txt"); got != "orphan" {
		t.Errorf("orphan should stay readable in place, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "gone")); !os.IsNotExist(err) {
		t.Errorf("migration created a namespace for an orphan: %v", err)
	}
	if !s.LegacyAttachments() {
		t.Error("banner condition false while an abandoned namespace still has files")
	}
	commits, err := s.RecentCommits(1)
	if err != nil || len(commits) != 1 || commits[0].Message != "migrate" {
		t.Fatalf("latest commit = %+v, %v", commits, err)
	}
	if again, err := s.MigrateLegacyAttachments("migrate", "test", "test@hmd.local"); err != nil || again != 0 {
		t.Errorf("second run = %d, %v", again, err)
	}
}

func TestDeleteLegacyAttachments(t *testing.T) {
	s := newAttachmentStore(t)
	seed(t, s, map[string]string{
		"notes/page.md":                                  "page",
		"attachments/notes/page/kept.txt":                "kept",
		"attachments/gone/page/a.txt":                    "a",
		"attachments/gone/deep/page/b.txt":               "b",
		"attachments/gone/page/.hmd/extracted/a.txt.txt": "sidecar",
	})
	for _, namespace := range []string{"notes", "", "..", "gone/page", "missing"} {
		if _, err := s.DeleteLegacyAttachments(namespace, "delete", "test", "test@hmd.local"); err == nil {
			t.Errorf("DeleteLegacyAttachments(%q) succeeded", namespace)
		}
	}
	deleted, err := s.DeleteLegacyAttachments("gone", "delete", "test", "test@hmd.local")
	if err != nil || deleted != 2 {
		t.Fatalf("DeleteLegacyAttachments = %d, %v", deleted, err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "attachments", "gone")); !os.IsNotExist(err) {
		t.Errorf("gone namespace folder left behind: %v", err)
	}
	if got := readAttachment(t, s, "notes/page", "kept.txt"); got != "kept" {
		t.Errorf("another namespace's attachment touched: %q", got)
	}
	if namespaces, err := s.LegacyAttachmentStatus(); err != nil || slices.ContainsFunc(namespaces, func(n LegacyAttachmentNamespace) bool { return !n.Exists }) {
		t.Errorf("gone namespaces after delete = %+v, %v", namespaces, err)
	}
	if commits, err := s.RecentCommits(1); err != nil || len(commits) != 1 || commits[0].Message != "delete" {
		t.Errorf("latest commit = %+v, %v", commits, err)
	}
}
