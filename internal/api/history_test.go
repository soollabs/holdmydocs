package api

import (
	"context"
	"testing"
)

func TestCommitAllowedClassifiesPaths(t *testing.T) {
	ctx := context.Background()
	for path, want := range map[string]bool{
		"notes/page.md":                   true,
		".wiki.yaml":                      false,
		"notes/.namespace.yaml":           false,
		".notes/template.md":              false,
		"attachments/notes/page/file.png": true,
	} {
		if got := commitAllowed(ctx, []string{path}); got != want {
			t.Errorf("commitAllowed(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestCommitAllowedRequiresEveryFile(t *testing.T) {
	ctx := context.Background()
	if commitAllowed(ctx, nil) {
		t.Error("commitAllowed(nil) = true, want false for a commit with no files")
	}
	if !commitAllowed(ctx, []string{"notes/page.md", "attachments/notes/page/file.png"}) {
		t.Error("commitAllowed with only page and attachment files = false, want true")
	}
	if commitAllowed(ctx, []string{"notes/page.md", ".wiki.yaml"}) {
		t.Error("commitAllowed with a hidden configuration file = true, want false")
	}
}
