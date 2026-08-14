package search

import "testing"

func FuzzAttachmentPath(f *testing.F) {
	for _, seed := range []string{"attachments/notes/page/file.pdf", "attachments/../secret", "attachments/notes/page/.hidden", "attachments/notes/é/report.txt"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, path string) { _, _, _ = ParseAttachmentPath(path) })
}
