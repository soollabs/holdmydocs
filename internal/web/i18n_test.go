package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"hmd/internal/i18n"
	"hmd/internal/presentation"
)

var templateKey = regexp.MustCompile(`\b(?:t|tn|tl) "([^"]+)"`)

func TestTemplateMessageKeysExist(t *testing.T) {
	known := i18n.Keys(i18n.Default)
	err := fs.WalkDir(webFS, "web/templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		src, err := fs.ReadFile(webFS, path)
		if err != nil {
			return err
		}
		for _, m := range templateKey.FindAllStringSubmatch(string(src), -1) {
			if !slices.Contains(known, m[1]) {
				t.Errorf("%s uses message %q missing from %s", path, m[1], i18n.Default)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

var scriptKey = regexp.MustCompile(`'(js\.[a-z_.]*[a-z_])'`)

func TestScriptMessageKeysExist(t *testing.T) {
	known := i18n.Keys(i18n.Default)
	files, err := fs.Glob(webFS, "web/static/*.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		src, err := fs.ReadFile(webFS, path)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range scriptKey.FindAllStringSubmatch(string(src), -1) {
			if !slices.Contains(known, m[1]) {
				t.Errorf("%s uses message %q missing from %s", path, m[1], i18n.Default)
			}
		}
	}
}

// Only whole literal keys; a key built from a prefix is covered by its own test.
var goKey = regexp.MustCompile(`i18n\.(?:T|TN)\(\s*"([^"]+)"\s*[,)]`)

func TestGoMessageKeysExist(t *testing.T) {
	known := i18n.Keys(i18n.Default)
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range goKey.FindAllStringSubmatch(string(src), -1) {
			if !slices.Contains(known, m[1]) {
				t.Errorf("%s uses message %q missing from %s", path, m[1], i18n.Default)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Skin descriptions, slug preset labels and sync states are looked up by a key built from
// their identifier, which the literal-key tests cannot see.
func TestBuiltMessageKeysExist(t *testing.T) {
	known := i18n.Keys(i18n.Default)
	var keys []string
	for _, name := range presentation.SkinNames {
		keys = append(keys, "settings.skin.note."+name)
	}
	for _, p := range presentation.SlugPresets {
		keys = append(keys, "namespace_edit.slug_preset."+p.Key)
	}
	for _, state := range []string{"ok", "pending", "failed"} {
		keys = append(keys, "js.sync.state."+state)
	}
	for _, key := range keys {
		if !slices.Contains(known, key) {
			t.Errorf("message %q missing from %s", key, i18n.Default)
		}
	}
}
